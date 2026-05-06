/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import (
	"slices"
	"testing"
)

// findingRefSliceEqual is a deep-equal helper for []FindingRef so the
// sticky-findings tests can assert preservation across save paths.
func findingRefSliceEqual(a, b []FindingRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTransitionToNoDiff_OrdersCommentBeforeSave locks in the load-bearing
// invariant in transitionToNoDiff: UpsertBotComment must run before Save.
//
// The framework's linearreconciler.StateManager only persists the comment-
// tracking commentID alongside the state attachment on the next Save call.
// Saving first would persist commentID="" and leave the next reconcile
// posting a fresh comment instead of updating the existing one. This test
// will fail loudly if a future refactor swaps the call order.
func TestTransitionToNoDiff_OrdersCommentBeforeSave(t *testing.T) {
	f := newLinearStateFixture(t, `{}`)
	r := newReconcilerForFixture(t, f)

	if err := r.transitionToNoDiff(t.Context(), f.issue, "test note"); err != nil {
		t.Fatalf("transitionToNoDiff: %v", err)
	}

	got := f.getCallOrder()
	commentIdx := slices.Index(got, "comment")
	saveIdx := slices.Index(got, "save")
	if commentIdx < 0 {
		t.Fatalf("expected a comment call, sequence was %v", got)
	}
	if saveIdx < 0 {
		t.Fatalf("expected a save call, sequence was %v", got)
	}
	if commentIdx >= saveIdx {
		t.Errorf("UpsertBotComment must run BEFORE Save; sequence was %v", got)
	}
}

// TestStateManager_StickyFindings_SurviveTerminalSave locks in the
// sticky-forever findings contract documented on issue.go's reconcileIssue
// and savePendingChecksState: a terminal Save (e.g. StatusComplete on
// PR-merge) must preserve CurrentFindings so consumers can render "what
// was the last CI failure that blocked progress?" retrospectively.
//
// A future change that decides to clear findings on terminal transitions
// must update both the docs AND this test deliberately, not silently.
func TestStateManager_StickyFindings_SurviveTerminalSave(t *testing.T) {
	initial := `{"pr_url":"https://github.com/o/r/pull/1","status":"active","current_findings":[{"kind":"ciCheck","identifier":"job-7","name":"Lint"}]}`
	f := newLinearStateFixture(t, initial)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Transition to terminal Complete WITHOUT touching findings — the
	// sticky-forever contract says they must persist as-is.
	s.SetStatus(StatusComplete)
	if _, err := mgr.Save(t.Context(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	want := []FindingRef{{Kind: "ciCheck", Identifier: "job-7", Name: "Lint"}}
	if got := saved.GetCurrentFindings(); !findingRefSliceEqual(got, want) {
		t.Errorf("findings dropped on terminal Save: got = %+v, want = %+v", got, want)
	}
	if got := saved.Status; got != StatusComplete {
		t.Errorf("status: got = %q, want = %q", got, StatusComplete)
	}
}

// TestSavePendingChecksState_DoesNotClobberFindings nails down the
// promise in the comment at issue.go's savePendingChecksState: pending-
// checks-only saves leave CurrentFindings untouched. Without this, a
// reconcile that observed CI activity (findings populated) followed by
// a reconcile that observed only pending checks would silently lose
// the failure record.
func TestSavePendingChecksState_DoesNotClobberFindings(t *testing.T) {
	initial := `{"pr_url":"https://github.com/o/r/pull/1","status":"active","current_findings":[{"kind":"ciCheck","identifier":"job-7","name":"Lint"}]}`
	f := newLinearStateFixture(t, initial)
	r := newReconcilerForFixture(t, f)

	if err := r.savePendingChecksState(t.Context(), f.issue, []string{"build"}); err != nil {
		t.Fatalf("savePendingChecksState: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	wantPending := []string{"build"}
	if got := saved.GetCurrentPendingChecks(); !slices.Equal(got, wantPending) {
		t.Errorf("pending: got = %+v, want = %+v", got, wantPending)
	}
	wantFindings := []FindingRef{{Kind: "ciCheck", Identifier: "job-7", Name: "Lint"}}
	if got := saved.GetCurrentFindings(); !findingRefSliceEqual(got, wantFindings) {
		t.Errorf("findings clobbered by savePendingChecksState: got = %+v, want = %+v", got, wantFindings)
	}
}
