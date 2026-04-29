/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import (
	"context"
	"fmt"

	"chainguard.dev/driftlessaf/reconcilers/githubreconciler"
	"chainguard.dev/driftlessaf/reconcilers/linearreconciler"
	"chainguard.dev/driftlessaf/workqueue"
	"github.com/chainguard-dev/clog"
	"github.com/google/go-github/v84/github"
)

// HandlePREvent processes a GitHub PR URL by extracting the Linear issue ID
// from the PR body and re-queuing it. This enables the CI feedback loop:
// PR CI fails → PR event → extract Linear issue ID → re-queue → iterate.
//
// The re-queue key is the Linear issue UUID (not the human identifier like
// ENG-123) because the linear-events trampoline keys workqueue items by UUID
// (see linear-metareconciler module: extension_key = "issueid"). Using the
// UUID keeps the two paths consistent so the workqueue can dedupe.
func (r *Reconciler[Req, Resp, CB]) HandlePREvent(ctx context.Context, prURL string) (*workqueue.ProcessResponse, error) {
	ctx = clog.WithValues(ctx, "pr_url", prURL)

	res, err := githubreconciler.ParseURL(prURL)
	if err != nil {
		return nil, workqueue.NonRetriableError(
			fmt.Errorf("parsing PR URL: %w", err),
			"invalid GitHub PR URL",
		)
	}
	if res.Type != githubreconciler.ResourceTypePullRequest {
		clog.InfoContext(ctx, "Not a pull request URL, skipping")
		return &workqueue.ProcessResponse{}, nil
	}

	gh, err := r.githubClients.Get(ctx, res.Owner, res.Repo)
	if err != nil {
		return nil, fmt.Errorf("get GitHub client: %w", err)
	}

	pr, _, err := gh.PullRequests.Get(ctx, res.Owner, res.Repo, res.Number)
	if err != nil {
		return nil, fmt.Errorf("fetch PR: %w", err)
	}

	// Most PRs in the repo aren't ours; Extract returns an error whenever the
	// PRData marker isn't present. Log at Info with the underlying error so
	// genuine schema-drift cases are still investigable, but don't escalate.
	data, err := r.changeManager.Extract(pr.GetBody())
	if err != nil {
		clog.InfoContext(ctx, "No PRData marker in PR body, skipping", "error", err)
		return &workqueue.ProcessResponse{}, nil
	}
	if data.LinearIssueID == "" {
		// The marker WAS present and parsed cleanly, but the embedded data
		// has no LinearIssueID. That's a real schema bug worth surfacing.
		clog.WarnContext(ctx, "PRData marker present but LinearIssueID is empty, skipping")
		return &workqueue.ProcessResponse{}, nil
	}

	return dispatchMergedOrRequeue(ctx, pr, r.linearClient, data.LinearIssueID, prURL)
}

// dispatchMergedOrRequeue picks between three PR-event responses:
//   - merged PR → mark the linked Linear issue StatusComplete, no re-queue
//     (a merged PR has nothing for the agent to do, and skipping the re-queue
//     avoids burning a clone lease)
//   - closed PR (without merge) → mark the linked issue StatusFailed with
//     FailureModePRClosed, no re-queue (a human abandoned the work; reviving
//     the PR forever is the wrong response)
//   - open PR → re-queue the Linear issue for the standard reconcile loop
//     (review threads, CI iteration, etc.)
//
// Extracted from HandlePREvent so the gate is unit-testable without wiring
// up a real GitHub client.
func dispatchMergedOrRequeue(ctx context.Context, pr *github.PullRequest, client *linearreconciler.Client, linearIssueID, prURL string) (*workqueue.ProcessResponse, error) {
	switch {
	case pr.GetMerged():
		if err := markIssueComplete(ctx, client, linearIssueID, prURL); err != nil {
			return nil, fmt.Errorf("mark issue complete: %w", err)
		}
		return &workqueue.ProcessResponse{}, nil
	case pr.GetState() == "closed":
		if err := markIssueFailed(ctx, client, linearIssueID, prURL, FailureModePRClosed); err != nil {
			return nil, fmt.Errorf("mark issue failed (%s): %w", FailureModePRClosed, err)
		}
		return &workqueue.ProcessResponse{}, nil
	default:
		clog.InfoContext(ctx, "Re-queuing Linear issue from PR event", "linear_issue_id", linearIssueID)
		return &workqueue.ProcessResponse{
			QueueKeys: []*workqueue.QueueKeyRequest{{Key: linearIssueID}},
		}, nil
	}
}

// markIssueComplete sets MaterializerState.Status to StatusComplete on the
// given Linear issue and backfills PRURL when the loaded state has none
// (which happens when the triggering event is the first signal we see for
// an issue — e.g. someone manually merged a PR whose materializer_state
// attachment never received a StatusActive write).
//
// Save is skipped when nothing would change (already complete AND PRURL is
// already populated), so repeated event arrivals are cheap.
//
// Concurrency: there's a known lost-update window with reconcileIssue's
// StatusActive writes (StateManager.Save is delete-then-create, not atomic).
// Benign in practice because StatusComplete is terminal — downstream readers
// stop reacting once they see it — but a CAS / version field on
// MaterializerState would make this airtight if needed.
func markIssueComplete(ctx context.Context, client *linearreconciler.Client, linearIssueID, prURL string) error {
	issue, err := client.GetIssue(ctx, linearIssueID)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	sm := client.NewStateManager(issue)
	var state MaterializerState
	if _, err := sm.Load(ctx, &state); err != nil {
		return fmt.Errorf("load materializer state: %w", err)
	}

	// Compute changes first so the already-complete case still backfills
	// PRURL when it's missing — without this ordering a
	// `{Status: complete, PRURL: ""}` state could never be repaired.
	dirty := false
	if state.PRURL == "" && prURL != "" {
		state.PRURL = prURL
		dirty = true
	}
	if state.Status != StatusComplete {
		state.Status = StatusComplete
		dirty = true
	}
	if !dirty {
		return nil
	}

	if err := sm.Save(ctx, &state); err != nil {
		return fmt.Errorf("save materializer state: %w", err)
	}
	clog.InfoContext(ctx, "Materializer state set to complete", "linear_issue_id", linearIssueID, "pr_url", prURL)
	return nil
}

// markIssueFailed sets MaterializerState.Status to StatusFailed with the
// given FailureMode on the linked Linear issue, and backfills PRURL when
// the loaded state has none. Mirrors markIssueComplete's shape exactly,
// except it carries the FailureMode classification.
//
// Save is skipped when nothing would change (already failed with the same
// mode AND PRURL is already populated). A different FailureMode IS a
// change worth persisting — lets a re-classification land without
// requiring an explicit reset.
//
// Concurrency: same lost-update window as markIssueComplete (StateManager.Save
// is delete-then-create). Benign for terminal statuses.
func markIssueFailed(ctx context.Context, client *linearreconciler.Client, linearIssueID, prURL string, mode FailureMode) error {
	issue, err := client.GetIssue(ctx, linearIssueID)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	sm := client.NewStateManager(issue)
	var state MaterializerState
	if _, err := sm.Load(ctx, &state); err != nil {
		return fmt.Errorf("load materializer state: %w", err)
	}

	dirty := false
	if state.PRURL == "" && prURL != "" {
		state.PRURL = prURL
		dirty = true
	}
	if state.Status != StatusFailed {
		state.Status = StatusFailed
		dirty = true
	}
	if state.FailureMode != mode {
		state.FailureMode = mode
		dirty = true
	}
	if !dirty {
		return nil
	}

	if err := sm.Save(ctx, &state); err != nil {
		return fmt.Errorf("save materializer state: %w", err)
	}
	clog.InfoContext(ctx, "Materializer state set to failed", "linear_issue_id", linearIssueID, "pr_url", prURL, "failure_mode", mode)
	return nil
}
