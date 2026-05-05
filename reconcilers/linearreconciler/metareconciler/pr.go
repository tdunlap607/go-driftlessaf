/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import (
	"context"
	"fmt"

	"chainguard.dev/driftlessaf/reconcilers/githubreconciler"
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
func (r *Reconciler[Req, Resp, CB, T, PT]) HandlePREvent(ctx context.Context, prURL string) (*workqueue.ProcessResponse, error) {
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

	return r.dispatchMergedOrRequeue(ctx, pr, data.LinearIssueID, prURL)
}

// dispatchMergedOrRequeue picks between three PR-event responses:
//   - merged PR → transition the linked Linear issue to StatusComplete,
//     no re-queue (a merged PR has nothing for the agent to do, and
//     skipping the re-queue avoids burning a clone lease)
//   - closed PR (without merge) → transition to StatusFailed with
//     FailureModePRClosed, no re-queue (a human abandoned the work;
//     reviving the PR forever is the wrong response)
//   - open PR → re-queue the Linear issue for the standard reconcile loop
//     (review threads, CI iteration, etc.)
//
// A method on *Reconciler so it can use the configured linearClient and
// identity directly, and so the StateManager pattern is uniform with
// reconcileIssue.
func (r *Reconciler[Req, Resp, CB, T, PT]) dispatchMergedOrRequeue(ctx context.Context, pr *github.PullRequest, linearIssueID, prURL string) (*workqueue.ProcessResponse, error) {
	switch {
	case pr.GetMerged():
		return r.transitionPR(ctx, linearIssueID, prURL, StatusComplete, "", TriggerPRMerge)
	case pr.GetState() == "closed":
		return r.transitionPR(ctx, linearIssueID, prURL, StatusFailed, FailureModePRClosed, TriggerPRClosed)
	default:
		clog.InfoContext(ctx, "Re-queuing Linear issue from PR event", "linear_issue_id", linearIssueID)
		return &workqueue.ProcessResponse{
			QueueKeys: []*workqueue.QueueKeyRequest{{Key: linearIssueID}},
		}, nil
	}
}

// transitionPR loads the issue's state, sets Status (and optional FailureMode),
// backfills PRURL when missing (events may arrive in unpredictable order so
// first-write-wins for PRURL on event-driven paths), and saves. The History
// append is automatic in StateManager.Save based on the diff against Load.
func (r *Reconciler[Req, Resp, CB, T, PT]) transitionPR(ctx context.Context, linearIssueID, prURL string, to Status, mode FailureMode, trigger string) (*workqueue.ProcessResponse, error) {
	issue, err := r.linearClient.GetIssue(ctx, linearIssueID)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	mgr := r.NewStateManager(issue)
	s, _, err := mgr.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	s.SetStatus(to)
	if mode != "" {
		s.SetFailureMode(mode)
	}
	// Backfill PRURL only — events arrive in unpredictable order; a prior
	// reconcile may have already written the canonical URL.
	if s.GetPRURL() == "" {
		s.SetPRURL(prURL)
	}
	ctx = WithActor(ctx, r.identity)
	ctx = WithTrigger(ctx, trigger)
	if _, err := mgr.Save(ctx, s); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return &workqueue.ProcessResponse{}, nil
}
