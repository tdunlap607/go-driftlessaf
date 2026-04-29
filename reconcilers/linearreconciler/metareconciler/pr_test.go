/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"chainguard.dev/driftlessaf/reconcilers/linearreconciler"
	"github.com/google/go-github/v84/github"
)

// linearStateFixture is a small mock of the bits of Linear's GraphQL API +
// asset CDN that markIssueComplete exercises: fetch issue, load state JSON,
// upload new state, register the new attachment, delete the old one.
//
// `lastSavedState` captures the body of the upload PUT so tests can assert
// the persisted JSON without poking at Linear under test.
type linearStateFixture struct {
	server         *httptest.Server
	issue          *linearreconciler.Issue
	lastSavedState atomic.Pointer[[]byte]
	saveCount      atomic.Int32
}

func newLinearStateFixture(t *testing.T, initialStateJSON string) *linearStateFixture {
	t.Helper()
	f := &linearStateFixture{}

	mux := http.NewServeMux()
	mux.HandleFunc("/state.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(initialStateJSON))
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bcopy := append([]byte(nil), body...)
		f.lastSavedState.Store(&bcopy)
		f.saveCount.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Everything else is treated as a Linear GraphQL request.
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "viewer"):
			data = map[string]any{"viewer": map[string]any{"id": "bot-1", "name": "Test Bot"}}
		case strings.Contains(req.Query, "issue"):
			// Lazy: f.issue is set below after the server URL is known.
			data = map[string]any{"issue": f.issue}
		case strings.Contains(req.Query, "fileUpload"):
			data = map[string]any{"fileUpload": map[string]any{"uploadFile": map[string]any{
				"uploadUrl": f.server.URL + "/upload",
				"assetUrl":  "https://uploads.linear.app/test/asset",
				"headers":   []any{},
			}}}
		case strings.Contains(req.Query, "attachmentDelete"):
			data = map[string]any{"attachmentDelete": map[string]any{"success": true}}
		case strings.Contains(req.Query, "attachmentCreate"):
			data = map[string]any{"attachmentCreate": map[string]any{"success": true}}
		default:
			data = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	// The issue's attachment URL needs the server URL, which only exists
	// post-construction — populate after the server is up.
	f.issue = &linearreconciler.Issue{
		ID:         "issue-1",
		Identifier: "TEST-1",
		Title:      "Materializer-managed test issue",
	}
	f.issue.Attachments.Nodes = []linearreconciler.Attachment{
		{ID: "att-1", Title: "materializer_state", URL: f.server.URL + "/state.json"},
	}
	return f
}

// newClient builds a Linear client wired through the fixture's mock endpoint
// with the materializer state-prefix set (so attachments titled
// "materializer_state" route correctly).
func (f *linearStateFixture) newClient(t *testing.T) *linearreconciler.Client {
	t.Helper()
	client := linearreconciler.NewClientWithAPIKey("test-token").
		WithHTTPClient(f.server.Client()).
		WithEndpoint(f.server.URL)
	// linearreconciler.New is the only public path that sets statePrefix on
	// the Client; we use it for its side-effect and discard the Reconciler.
	if _, err := linearreconciler.New(t.Context(), client, linearreconciler.WithStatePrefix("materializer")); err != nil {
		t.Fatalf("init linearreconciler: %v", err)
	}
	return client
}

func TestMarkIssueComplete_TransitionsActiveToComplete(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	client := f.newClient(t)

	if err := markIssueComplete(t.Context(), client, f.issue.ID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("markIssueComplete: %v", err)
	}

	if got := f.saveCount.Load(); got != 1 {
		t.Fatalf("save count = %d, want 1", got)
	}
	saved := string(*f.lastSavedState.Load())
	if !strings.Contains(saved, `"status":"complete"`) {
		t.Errorf("saved state status: got = %q, want substring %q", saved, `"status":"complete"`)
	}
	if !strings.Contains(saved, `"pr_url":"https://github.com/o/r/pull/1"`) {
		t.Errorf("saved state pr_url: got = %q, want substring %q", saved, `"pr_url":"https://github.com/o/r/pull/1"`)
	}
}

func TestMarkIssueComplete_IdempotentWhenAlreadyComplete(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"complete"}`)
	client := f.newClient(t)

	if err := markIssueComplete(t.Context(), client, f.issue.ID, "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("markIssueComplete: %v", err)
	}

	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("save count: got = %d, want = 0 (same-status call must be a no-op)", got)
	}
}

// TestMarkIssueComplete_BackfillsPRURLEvenWhenAlreadyComplete covers the
// edge case the dirty-flag refactor exists for: a state already at
// StatusComplete but missing PRURL must still be repaired by a later call,
// rather than short-circuiting on the same-status check.
func TestMarkIssueComplete_BackfillsPRURLEvenWhenAlreadyComplete(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"complete"}`)
	client := f.newClient(t)

	const prURL = "https://github.com/o/r/pull/99"
	if err := markIssueComplete(t.Context(), client, f.issue.ID, prURL); err != nil {
		t.Fatalf("markIssueComplete: %v", err)
	}

	if got := f.saveCount.Load(); got != 1 {
		t.Fatalf("save count = %d, want 1 (backfill must trigger a save)", got)
	}
	saved := string(*f.lastSavedState.Load())
	wantPRURL := `"pr_url":"` + prURL + `"`
	if !strings.Contains(saved, wantPRURL) {
		t.Errorf("saved state pr_url: got = %q, want substring %q", saved, wantPRURL)
	}
}

func TestMarkIssueComplete_BackfillsPRURLWhenAbsent(t *testing.T) {
	// State exists but has no pr_url — this is what happens when the
	// triggering event is the first signal we ever see for an issue (e.g.
	// someone merged a PR before the materializer's StatusActive write
	// landed).
	f := newLinearStateFixture(t, `{"status":"active"}`)
	client := f.newClient(t)

	const prURL = "https://github.com/o/r/pull/42"
	if err := markIssueComplete(t.Context(), client, f.issue.ID, prURL); err != nil {
		t.Fatalf("markIssueComplete: %v", err)
	}

	saved := string(*f.lastSavedState.Load())
	wantPRURL := `"pr_url":"` + prURL + `"`
	if !strings.Contains(saved, wantPRURL) {
		t.Errorf("saved state pr_url: got = %q, want substring %q", saved, wantPRURL)
	}
	if !strings.Contains(saved, `"status":"complete"`) {
		t.Errorf("saved state status: got = %q, want substring %q", saved, `"status":"complete"`)
	}
}

func TestMarkIssueFailed_TransitionsActiveToFailed(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	client := f.newClient(t)

	if err := markIssueFailed(t.Context(), client, f.issue.ID, "https://github.com/o/r/pull/1", FailureModePRClosed); err != nil {
		t.Fatalf("markIssueFailed: %v", err)
	}

	if got := f.saveCount.Load(); got != 1 {
		t.Fatalf("save count = %d, want 1", got)
	}
	saved := string(*f.lastSavedState.Load())
	if !strings.Contains(saved, `"status":"failed"`) {
		t.Errorf("saved state status: got = %q, want substring %q", saved, `"status":"failed"`)
	}
	if !strings.Contains(saved, `"failure_mode":"pr_closed"`) {
		t.Errorf("saved state failure_mode: got = %q, want substring %q", saved, `"failure_mode":"pr_closed"`)
	}
}

func TestMarkIssueFailed_IdempotentWhenSameModeAndStatus(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"max_turns"}`)
	client := f.newClient(t)

	if err := markIssueFailed(t.Context(), client, f.issue.ID, "https://github.com/o/r/pull/1", FailureModeMaxTurns); err != nil {
		t.Fatalf("markIssueFailed: %v", err)
	}

	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("save count: got = %d, want = 0 (same status+mode call must be a no-op)", got)
	}
}

// TestMarkIssueFailed_ReclassifiesMode covers the case where the same issue
// re-enters the failure path with a different FailureMode (e.g. an earlier
// max_turns gets reclassified by a future detection rule). The save MUST
// happen so observability sees the new classification.
func TestMarkIssueFailed_ReclassifiesMode(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"max_turns"}`)
	client := f.newClient(t)

	if err := markIssueFailed(t.Context(), client, f.issue.ID, "https://github.com/o/r/pull/1", FailureModePRClosed); err != nil {
		t.Fatalf("markIssueFailed: %v", err)
	}

	if got := f.saveCount.Load(); got != 1 {
		t.Fatalf("save count = %d, want 1 (mode change must persist)", got)
	}
	saved := string(*f.lastSavedState.Load())
	if !strings.Contains(saved, `"failure_mode":"pr_closed"`) {
		t.Errorf("saved state failure_mode: got = %q, want substring %q", saved, `"failure_mode":"pr_closed"`)
	}
}

// TestMarkIssueFailed_BackfillsPRURLEvenWhenAlreadyFailed mirrors the
// markIssueComplete equivalent: a state already at StatusFailed but missing
// PRURL must still be repaired by a later call.
func TestMarkIssueFailed_BackfillsPRURLEvenWhenAlreadyFailed(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"failed","failure_mode":"pr_closed"}`)
	client := f.newClient(t)

	const prURL = "https://github.com/o/r/pull/77"
	if err := markIssueFailed(t.Context(), client, f.issue.ID, prURL, FailureModePRClosed); err != nil {
		t.Fatalf("markIssueFailed: %v", err)
	}

	if got := f.saveCount.Load(); got != 1 {
		t.Fatalf("save count = %d, want 1 (backfill must trigger a save)", got)
	}
	saved := string(*f.lastSavedState.Load())
	wantPRURL := `"pr_url":"` + prURL + `"`
	if !strings.Contains(saved, wantPRURL) {
		t.Errorf("saved state pr_url: got = %q, want substring %q", saved, wantPRURL)
	}
}

// TestDispatchMergedOrRequeue exercises the three-way decision in
// dispatchMergedOrRequeue: a merged PR must persist StatusComplete, a
// closed-without-merge PR must persist StatusFailed/pr_closed, and an
// open PR must re-queue without touching state.
func TestDispatchMergedOrRequeue(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/1"

	tests := []struct {
		name            string
		merged          bool
		state           string // GitHub PR state: "open" or "closed"
		wantRequeueKeys int
		wantSaveCount   int32
		wantStatusJSON  string // substring expected in the saved state, if any save happened
	}{
		{
			name:            "merged → mark complete, no requeue",
			merged:          true,
			state:           "closed", // merged PRs are also closed
			wantRequeueKeys: 0,
			wantSaveCount:   1,
			wantStatusJSON:  `"status":"complete"`,
		},
		{
			// Defensive case: GitHub never returns merged=true with state=open
			// in practice, but locking this in guards against future refactors
			// that could reorder the merged/closed branches in dispatchMergedOrRequeue.
			name:            "merged with state=open (defensive) → mark complete, no requeue",
			merged:          true,
			state:           "open",
			wantRequeueKeys: 0,
			wantSaveCount:   1,
			wantStatusJSON:  `"status":"complete"`,
		},
		{
			name:            "closed without merge → mark failed (pr_closed), no requeue",
			merged:          false,
			state:           "closed",
			wantRequeueKeys: 0,
			wantSaveCount:   1,
			wantStatusJSON:  `"status":"failed"`,
		},
		{
			name:            "open PR → requeue, no state change",
			merged:          false,
			state:           "open",
			wantRequeueKeys: 1,
			wantSaveCount:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinearStateFixture(t, `{"pr_url":"`+prURL+`","status":"active"}`)
			client := f.newClient(t)

			pr := &github.PullRequest{
				Merged: github.Ptr(tc.merged),
				State:  github.Ptr(tc.state),
			}
			resp, err := dispatchMergedOrRequeue(t.Context(), pr, client, f.issue.ID, prURL)
			if err != nil {
				t.Fatalf("dispatchMergedOrRequeue: %v", err)
			}

			if got := len(resp.QueueKeys); got != tc.wantRequeueKeys {
				t.Errorf("len(QueueKeys): got = %d, want = %d", got, tc.wantRequeueKeys)
			}
			if got := f.saveCount.Load(); got != tc.wantSaveCount {
				t.Errorf("save count: got = %d, want = %d", got, tc.wantSaveCount)
			}
			if tc.wantStatusJSON != "" {
				saved := string(*f.lastSavedState.Load())
				if !strings.Contains(saved, tc.wantStatusJSON) {
					t.Errorf("saved state: got = %q, want substring %q", saved, tc.wantStatusJSON)
				}
			}
		})
	}
}
