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
)

// HandlePREvent processes a GitHub PR URL by extracting the linked Linear
// issue ID from the PR body and re-queuing it on the Linear workqueue.
// All terminal-transition decisions (merged → StatusComplete, closed →
// StatusFailed/PRClosed) are deferred to reconcileIssue itself, which
// detects them via maybeTransitionFromPRState. Routing-only here so all
// State writes for a Linear issue serialize through the linear
// workqueue's per-key lock — eliminating the cross-workqueue
// read-modify-write race that previously left downstream mirrors stale
// when a github PR-close handler raced with a linear cancel webhook.
//
// The re-queue key is the Linear issue UUID (not the human identifier
// like ENG-123) because the linear-events trampoline keys workqueue
// items by UUID (see linear-metareconciler module:
// extension_key = "issueid"). Using the UUID keeps the two paths
// consistent so the workqueue can dedupe.
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

	// Fetch the PR body to extract the LinearIssueID marker. This is the
	// only github API call HandlePREvent needs — the PR's open/closed/merged
	// state is re-derived inside reconcileIssue (where it's serialized with
	// every other write to this Linear issue's State).
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

	clog.InfoContext(ctx, "Re-queuing Linear issue from PR event", "linear_issue_id", data.LinearIssueID)
	return &workqueue.ProcessResponse{
		QueueKeys: []*workqueue.QueueKeyRequest{{Key: data.LinearIssueID}},
	}, nil
}
