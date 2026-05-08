/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"chainguard.dev/driftlessaf/reconcilers/linearreconciler"
)

const testActor = "test-bot"

// fixedClock returns a clock function that always returns the same time, so
// History entries' At fields are deterministic.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// loadSavedState parses the fixture's last-saved JSON into the given state
// pointer. Tests use it to inspect History and other fields directly rather
// than substring-matching on the JSON.
func loadSavedState(t *testing.T, f *linearStateFixture, into any) {
	t.Helper()
	raw := f.lastSavedState.Load()
	if raw == nil {
		t.Fatalf("no saved state captured")
	}
	if err := json.Unmarshal(*raw, into); err != nil {
		t.Fatalf("unmarshal saved state: %v", err)
	}
}

// newReconcilerForFixture builds a minimal Reconciler wired to the fixture's
// Linear client. State type defaults to the framework's State; tests that
// need bot-extension fields construct their own State type and parameterise
// directly.
func newReconcilerForFixture(t *testing.T, f *linearStateFixture) *Reconciler[testReq, testResp, testCB, State, *State] {
	t.Helper()
	return &Reconciler[testReq, testResp, testCB, State, *State]{
		identity:     testActor,
		linearClient: f.newClient(t),
	}
}

func TestStateManager_FromActiveToComplete(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	mgr.now = fixedClock(now)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusComplete)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRMerge)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true")
	}
	if got := f.saveCount.Load(); got != 1 {
		t.Fatalf("save count: got = %d, want = 1", got)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.Status != StatusComplete {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusComplete)
	}
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	entry := saved.History[0]
	if entry.From != StatusActive || entry.To != StatusComplete {
		t.Errorf("History[0] from/to: got = %q→%q, want = %q→%q", entry.From, entry.To, StatusActive, StatusComplete)
	}
	if entry.Actor != testActor || entry.Trigger != TriggerPRMerge {
		t.Errorf("History[0] actor/trigger: got = %q/%q, want = %q/%q", entry.Actor, entry.Trigger, testActor, TriggerPRMerge)
	}
	if !entry.At.Equal(now) {
		t.Errorf("History[0].At: got = %v, want = %v", entry.At, now)
	}
}

// TestStateManager_FromActiveToFailed covers the path the deleted
// markIssueFailed tests previously exercised: a status transition INTO
// StatusFailed with FailureMode set must persist both fields and append a
// History entry whose Mode field reflects the new classification.
func TestStateManager_FromActiveToFailed(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusFailed)
	s.SetFailureMode(FailureModePRClosed)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRClosed)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.Status != StatusFailed {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusFailed)
	}
	if saved.FailureMode != FailureModePRClosed {
		t.Errorf("FailureMode: got = %q, want = %q", saved.FailureMode, FailureModePRClosed)
	}
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	if saved.History[0].Mode != FailureModePRClosed {
		t.Errorf("History[0].Mode: got = %q, want = %q", saved.History[0].Mode, FailureModePRClosed)
	}
}

// TestStateManager_FromActiveToFailedNoDiff covers the no_diff terminal
// path added for the empty-commit-retry-loop bug: when the agent runs
// but produces no changes, the framework transitions State to
// StatusFailed + FailureModeNoDiff. The structured FailureMode is what
// downstream consumers match on to surface the "agent ran but had
// nothing to do" outcome instead of a stuck-active retry storm.
func TestStateManager_FromActiveToFailedNoDiff(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusFailed)
	s.SetFailureMode(FailureModeNoDiff)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerNoDiff)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.Status != StatusFailed {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusFailed)
	}
	if saved.FailureMode != FailureModeNoDiff {
		t.Errorf("FailureMode: got = %q, want = %q", saved.FailureMode, FailureModeNoDiff)
	}
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	entry := saved.History[0]
	if entry.From != StatusActive || entry.To != StatusFailed {
		t.Errorf("History[0] from/to: got = %q→%q, want = %q→%q", entry.From, entry.To, StatusActive, StatusFailed)
	}
	if entry.Trigger != TriggerNoDiff {
		t.Errorf("History[0].Trigger: got = %q, want = %q", entry.Trigger, TriggerNoDiff)
	}
	if entry.Mode != FailureModeNoDiff {
		t.Errorf("History[0].Mode: got = %q, want = %q", entry.Mode, FailureModeNoDiff)
	}
}

// TestStateManager_CurrentFindings_RoundTrip verifies that the
// CurrentFindings snapshot persists through Save and reloads cleanly,
// and that passing nil to Set clears stale entries from a previous
// iteration. The reconcile loop relies on this clear-on-reset
// behaviour so downstream consumers don't see findings that have
// since been resolved.
func TestStateManager_CurrentFindings_RoundTrip(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []FindingRef{
		{Kind: "ciCheck", Identifier: "job-1", DetailsURL: "https://github.com/o/r/actions/runs/1/job/2"},
		{Kind: "review", Identifier: "thread-9", DetailsURL: "https://github.com/o/r/pull/1#discussion_r9"},
	}
	s.SetCurrentFindings(want)
	// Advance status so Save isn't a no-op.
	s.SetStatus(StatusComplete)
	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRMerge)
	if _, err := mgr.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got := saved.GetCurrentFindings(); len(got) != len(want) {
		t.Fatalf("CurrentFindings length: got = %d, want = %d", len(got), len(want))
	}
	for i, w := range want {
		got := saved.GetCurrentFindings()[i]
		if got != w {
			t.Errorf("CurrentFindings[%d]: got = %+v, want = %+v", i, got, w)
		}
	}

	// Clear path: passing nil drops the field.
	s.SetCurrentFindings(nil)
	s.SetStatus(StatusActive)
	if _, err := mgr.Save(ctx, s); err != nil {
		t.Fatalf("Save (clear): %v", err)
	}
	var cleared State
	loadSavedState(t, f, &cleared)
	if got := cleared.GetCurrentFindings(); got != nil {
		t.Errorf("CurrentFindings after clear: got = %v, want = nil", got)
	}
}

func TestStateManager_NoOpWhenSameState(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"complete"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// No mutations — Save should be a no-op.
	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if changed {
		t.Errorf("changed: got = true, want = false (no-op call must not save)")
	}
	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("save count: got = %d, want = 0", got)
	}
}

// TestStateManager_ClearsFailureModeOnRecovery verifies the FailureMode
// invariant: when a previously-failed issue transitions back to a non-failed
// status, FailureMode is cleared so observability never sees an active issue
// with a stale failure classification.
func TestStateManager_ClearsFailureModeOnRecovery(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"max_turns"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusActive)
	// Note: caller did NOT clear FailureMode — Save must do it automatically.

	ctx := WithTrigger(t.Context(), TriggerMergeConflict)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.Status != StatusActive {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusActive)
	}
	if saved.FailureMode != "" {
		t.Errorf("FailureMode: got = %q, want = \"\" (must clear on transition out of StatusFailed)", saved.FailureMode)
	}
	// Manager clears FailureMode BEFORE the History append, so the entry's
	// Mode field must reflect the post-clear value. Asserting this turns an
	// implicit invariant into a test-driven one.
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	if saved.History[0].Mode != "" {
		t.Errorf("History[0].Mode: got = %q, want = \"\" (cleared before append)", saved.History[0].Mode)
	}
}

// TestStateManager_PRURLOnlyChangePersists is the regression test for the
// dirty predicate covering PRURL changes. Prior to including PRURL in the
// snapshot, a same-status reconcile with a different prURL would return
// (false, nil) and silently drop the new URL — leaving stale data on the
// Linear issue and breaking the bot-comment gate that depends on changed=true.
func TestStateManager_PRURLOnlyChangePersists(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Status unchanged; only PRURL changes (e.g. PR was recreated, or Upsert
	// returned a refreshed URL).
	const newPRURL = "https://github.com/o/r/pull/2"
	s.SetPRURL(newPRURL)

	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true (PRURL change must persist)")
	}
	if got := f.saveCount.Load(); got != 1 {
		t.Errorf("save count: got = %d, want = 1", got)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.PRURL != newPRURL {
		t.Errorf("PRURL: got = %q, want = %q", saved.PRURL, newPRURL)
	}
	// PRURL-only changes must NOT append a History entry — a from==to entry
	// would mislead observability. The save fires; the audit trail stays clean.
	if got, want := len(saved.History), 0; got != want {
		t.Errorf("History len: got = %d, want = %d (PRURL-only change must not append History)", got, want)
	}
}

// TestStateManager_FindingsAndPendingChangesPersist is the regression test
// for the dirty predicate covering CurrentFindings / CurrentPendingChecks.
// Without these in the snapshot, a same-status reconcile that only updated
// the live findings/pending snapshot would return (false, nil) and skip
// the Linear write — leaving the source-of-truth attachment empty even
// though the post-save callback had already pushed the new data to
// downstream mirrors. The next reconcile's Load would then read empty
// findings/pending from Linear and the framework would observably "lose"
// the data on the round trip.
func TestStateManager_FindingsAndPendingChangesPersist(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Status and PRURL unchanged; only findings/pending change. This is the
	// shape produced by the saveNoDiffIterationState / savePendingChecksState
	// paths, where the agent observed CI activity but the state machine did
	// not transition.
	wantFindings := []FindingRef{
		{Kind: "ciCheck", Identifier: "job-7", DetailsURL: "https://github.com/o/r/actions/runs/1/job/7"},
	}
	wantPending := []string{"build", "lint"}
	s.SetCurrentFindings(wantFindings)
	s.SetCurrentPendingChecks(wantPending)

	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true (findings/pending change must persist)")
	}
	if got := f.saveCount.Load(); got != 1 {
		t.Errorf("save count: got = %d, want = 1", got)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got := saved.GetCurrentFindings(); !slices.Equal(got, wantFindings) {
		t.Errorf("CurrentFindings: got = %+v, want = %+v", got, wantFindings)
	}
	if got := saved.GetCurrentPendingChecks(); !slices.Equal(got, wantPending) {
		t.Errorf("CurrentPendingChecks: got = %+v, want = %+v", got, wantPending)
	}
	// Findings/pending-only changes must NOT append a History entry — the
	// state machine didn't transition.
	if got, want := len(saved.History), 0; got != want {
		t.Errorf("History len: got = %d, want = %d (findings/pending-only change must not append History)", got, want)
	}

	// Same-content re-save is a no-op: snapshots refreshed after the previous
	// save mean nothing looks dirty this time.
	noopChanged, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("no-op Save: %v", err)
	}
	if noopChanged {
		t.Errorf("no-op changed: got = true, want = false (same findings/pending must not re-write)")
	}
	if got := f.saveCount.Load(); got != 1 {
		t.Errorf("save count after no-op: got = %d, want = 1", got)
	}
}

// TestFindingsEqual covers the order-insensitive snapshot comparison the
// HasFindings dedup uses to decide whether the agent has already iterated
// against this exact set of CI findings. Pin each case so future tweaks
// to the comparison heuristic don't silently change which webhook bursts
// get deduped.
func TestFindingsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []FindingRef
		want bool
	}{
		{
			name: "both empty are equal",
			a:    nil,
			b:    []FindingRef{},
			want: true,
		},
		{
			name: "different lengths are not equal",
			a:    []FindingRef{{Kind: "ciCheck", Identifier: "1"}},
			b:    []FindingRef{{Kind: "ciCheck", Identifier: "1"}, {Kind: "ciCheck", Identifier: "2"}},
			want: false,
		},
		{
			name: "same set, same order",
			a:    []FindingRef{{Kind: "ciCheck", Identifier: "1", Name: "build"}},
			b:    []FindingRef{{Kind: "ciCheck", Identifier: "1", Name: "build"}},
			want: true,
		},
		{
			name: "same set, reversed order (sort makes them equal)",
			a:    []FindingRef{{Kind: "ciCheck", Identifier: "1"}, {Kind: "ciCheck", Identifier: "2"}},
			b:    []FindingRef{{Kind: "ciCheck", Identifier: "2"}, {Kind: "ciCheck", Identifier: "1"}},
			want: true,
		},
		{
			name: "different identifier (re-run with new job ID counts as different)",
			a:    []FindingRef{{Kind: "ciCheck", Identifier: "1"}},
			b:    []FindingRef{{Kind: "ciCheck", Identifier: "2"}},
			want: false,
		},
		{
			name: "same identifier, different name (still treated as different — fields compared whole)",
			a:    []FindingRef{{Kind: "ciCheck", Identifier: "1", Name: "build"}},
			b:    []FindingRef{{Kind: "ciCheck", Identifier: "1", Name: "lint"}},
			want: false,
		},
		{
			name: "different kind disambiguates same identifier",
			a:    []FindingRef{{Kind: "ciCheck", Identifier: "1"}},
			b:    []FindingRef{{Kind: "review", Identifier: "1"}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := findingsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("findingsEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestStateManager_ReactivationReset_ClearsTerminalFields exercises the
// state mutation pattern reconcileIssue applies when a human moves a
// previously-canceled Linear issue back to a non-terminal workflow state:
// transition Status from Failed back to Active, clear FailureMode, drop
// PRURL/findings/pending/no-diff-counter so the next reconcile starts
// fresh from the !state.HasPR branch.
//
// Pins the exact set of fields that get cleared. If a future change adds
// a new sticky terminal-marker field, this test should grow to include it
// — otherwise stale data leaks into the post-reactivation reconcile.
func TestStateManager_ReactivationReset_ClearsTerminalFields(t *testing.T) {
	const initial = `{
		"pr_url":"https://github.com/o/r/pull/41",
		"status":"failed",
		"failure_mode":"pr_closed",
		"current_findings":[{"kind":"ciCheck","identifier":"job-1"}],
		"current_pending_checks":["build"],
		"no_diff_iteration_count":2
	}`
	f := newLinearStateFixture(t, initial)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Apply the same mutations reconcileIssue's reactivation branch does.
	s.SetStatus(StatusActive)
	s.SetFailureMode("")
	s.SetPRURL("")
	s.SetCurrentFindings(nil)
	s.SetCurrentPendingChecks(nil)
	s.SetNoDiffIterationCount(0)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerReactivated)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true (reactivation must persist)")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.Status != StatusActive {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusActive)
	}
	if saved.FailureMode != "" {
		t.Errorf("FailureMode: got = %q, want = empty", saved.FailureMode)
	}
	if saved.PRURL != "" {
		t.Errorf("PRURL: got = %q, want = empty", saved.PRURL)
	}
	if len(saved.CurrentFindings) != 0 {
		t.Errorf("CurrentFindings: got = %+v, want = nil/empty", saved.CurrentFindings)
	}
	if len(saved.CurrentPendingChecks) != 0 {
		t.Errorf("CurrentPendingChecks: got = %+v, want = nil/empty", saved.CurrentPendingChecks)
	}
	if got, want := saved.GetNoDiffIterationCount(), 0; got != want {
		t.Errorf("NoDiffIterationCount: got = %d, want = %d", got, want)
	}
	// Failed→Active transition must append a History entry recording the
	// reactivation trigger so operators can audit who/when reactivated.
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	entry := saved.History[0]
	if entry.From != StatusFailed || entry.To != StatusActive {
		t.Errorf("History[0] from/to: got = %q→%q, want = %q→%q", entry.From, entry.To, StatusFailed, StatusActive)
	}
	if entry.Trigger != TriggerReactivated {
		t.Errorf("History[0].Trigger: got = %q, want = %q", entry.Trigger, TriggerReactivated)
	}
	if entry.Mode != "" {
		t.Errorf("History[0].Mode: got = %q, want = empty (transition out of Failed clears mode)", entry.Mode)
	}
}

// TestGateApplyReactivationReset_PredicateAndMutate exercises the
// gateApplyReactivationReset helper directly. Locks in two contracts:
//
//  1. Predicate: only fires when Linear is non-terminal AND
//     Status==Failed && FailureMode==PRClosed. Failed/NoDiff and
//     Failed/NoProgress (which don't auto-cancel Linear) must NOT reset,
//     because the human reactivating Linear gives no new signal —
//     re-running would just reproduce the same no-op. Linear-terminal
//     issues must NOT reset either: the cancel-gate now runs AFTER this
//     helper, so we explicitly check terminal here rather than relying
//     on cancel-gate ordering.
//  2. Mutate-only: the helper mutates `existing` in place and returns
//     bool; it does NOT save. Caller batches mutations into a single
//     gate-stage save for History-entry hygiene.
//
// Catches refactor regressions on the gate predicate without needing a
// full reconcileIssue mock chain — the harder e2e form is intentionally
// skipped because driving changeSession + github clients + agent for a
// one-line predicate would be net-negative for the test suite's
// signal-to-noise ratio.
func TestGateApplyReactivationReset_PredicateAndMutate(t *testing.T) {
	tests := []struct {
		name              string
		initial           string
		linearStateType   string
		wantApplied       bool
		wantStatusAfter   Status
		wantPRURLAfter    string
		wantNoDiffAfter   int
		wantFindingsAfter int
	}{
		{
			name:              "PRClosed + Linear non-terminal triggers reset",
			initial:           `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"pr_closed","current_findings":[{"kind":"ciCheck","identifier":"job-1"}],"current_pending_checks":["build"],"no_diff_iteration_count":2}`,
			linearStateType:   "started",
			wantApplied:       true,
			wantStatusAfter:   StatusActive,
			wantPRURLAfter:    "",
			wantNoDiffAfter:   0,
			wantFindingsAfter: 0,
		},
		{
			name:            "PRClosed + Linear canceled does NOT trigger reset",
			initial:         `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"pr_closed"}`,
			linearStateType: "canceled",
			wantApplied:     false,
			wantStatusAfter: StatusFailed,
			wantPRURLAfter:  "https://github.com/o/r/pull/1",
		},
		{
			name:            "PRClosed + Linear completed does NOT trigger reset",
			initial:         `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"pr_closed"}`,
			linearStateType: "completed",
			wantApplied:     false,
			wantStatusAfter: StatusFailed,
			wantPRURLAfter:  "https://github.com/o/r/pull/1",
		},
		{
			name:            "NoDiff does not trigger reset",
			initial:         `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"no_diff"}`,
			linearStateType: "started",
			wantApplied:     false,
			wantStatusAfter: StatusFailed,
			wantPRURLAfter:  "https://github.com/o/r/pull/1",
		},
		{
			name:            "NoProgress does not trigger reset",
			initial:         `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"no_progress"}`,
			linearStateType: "started",
			wantApplied:     false,
			wantStatusAfter: StatusFailed,
			wantPRURLAfter:  "https://github.com/o/r/pull/1",
		},
		{
			name:            "Active status does not trigger reset",
			initial:         `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`,
			linearStateType: "started",
			wantApplied:     false,
			wantStatusAfter: StatusActive,
			wantPRURLAfter:  "https://github.com/o/r/pull/1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinearStateFixture(t, tc.initial)
			f.issue.State.Type = tc.linearStateType
			r := newReconcilerForFixture(t, f)
			mgr := r.NewStateManager(f.issue)

			s, _, err := mgr.Load(t.Context())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			got := r.gateApplyReactivationReset(t.Context(), s, f.issue)
			if got != tc.wantApplied {
				t.Errorf("gateApplyReactivationReset returned: got = %v, want = %v", got, tc.wantApplied)
			}

			// Mutate-only: helper must NOT save. Caller batches the save.
			if saveCount := f.saveCount.Load(); saveCount != 0 {
				t.Errorf("helper must not save; got saveCount = %d, want = 0", saveCount)
			}

			// Inspect the in-memory mutation on `s` directly.
			if s.GetStatus() != tc.wantStatusAfter {
				t.Errorf("Status after: got = %q, want = %q", s.GetStatus(), tc.wantStatusAfter)
			}
			if s.GetPRURL() != tc.wantPRURLAfter {
				t.Errorf("PRURL after: got = %q, want = %q", s.GetPRURL(), tc.wantPRURLAfter)
			}
			if tc.wantApplied {
				if len(s.GetCurrentFindings()) != tc.wantFindingsAfter {
					t.Errorf("CurrentFindings after: got len = %d, want = %d", len(s.GetCurrentFindings()), tc.wantFindingsAfter)
				}
				if got := s.GetNoDiffIterationCount(); got != tc.wantNoDiffAfter {
					t.Errorf("NoDiffIterationCount after: got = %d, want = %d", got, tc.wantNoDiffAfter)
				}
				if s.GetFailureMode() != "" {
					t.Errorf("FailureMode after: got = %q, want = empty", s.GetFailureMode())
				}
			}
		})
	}
}

// TestGateMirrorLinearWorkflowState exercises the linear-state mirror
// gate helper. Mutate-only: returns true when it changed anything,
// false on no-op. The caller batches the save.
func TestGateMirrorLinearWorkflowState(t *testing.T) {
	tests := []struct {
		name          string
		initialType   string
		initialName   string
		liveType      string
		liveName      string
		wantChanged   bool
		wantTypeAfter string
		wantNameAfter string
	}{
		{
			name:          "first observation populates fields",
			initialType:   "",
			initialName:   "",
			liveType:      "started",
			liveName:      "In Progress",
			wantChanged:   true,
			wantTypeAfter: "started",
			wantNameAfter: "In Progress",
		},
		{
			name:          "type change (started → canceled)",
			initialType:   "started",
			initialName:   "In Progress",
			liveType:      "canceled",
			liveName:      "Canceled",
			wantChanged:   true,
			wantTypeAfter: "canceled",
			wantNameAfter: "Canceled",
		},
		{
			name:          "name-only change (team renamed In Progress → Working)",
			initialType:   "started",
			initialName:   "In Progress",
			liveType:      "started",
			liveName:      "Working",
			wantChanged:   true,
			wantTypeAfter: "started",
			wantNameAfter: "Working",
		},
		{
			name:          "no change is no-op",
			initialType:   "started",
			initialName:   "In Progress",
			liveType:      "started",
			liveName:      "In Progress",
			wantChanged:   false,
			wantTypeAfter: "started",
			wantNameAfter: "In Progress",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &State{
				LinearStateType: tc.initialType,
				LinearStateName: tc.initialName,
			}
			issue := &linearreconciler.Issue{}
			issue.State.Type = tc.liveType
			issue.State.Name = tc.liveName

			got := gateMirrorLinearWorkflowState[State, *State](s, issue)
			if got != tc.wantChanged {
				t.Errorf("changed: got = %v, want = %v", got, tc.wantChanged)
			}
			if s.LinearStateType != tc.wantTypeAfter {
				t.Errorf("LinearStateType after: got = %q, want = %q", s.LinearStateType, tc.wantTypeAfter)
			}
			if s.LinearStateName != tc.wantNameAfter {
				t.Errorf("LinearStateName after: got = %q, want = %q", s.LinearStateName, tc.wantNameAfter)
			}
		})
	}
}

// TestStateManager_LinearStatePersist verifies that LinearStateType and
// LinearStateName round-trip through Save/Load and trigger the dirty
// check on change. The framework refreshes these from the live Linear
// issue at the top of every reconcile so downstream consumers see the
// human's workflow-state changes — including cancellations driven from
// Linear UI / MCP that the bot's auto-cancel saveCallback never observes.
func TestStateManager_LinearStatePersist(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetLinearStateType("started")
	s.SetLinearStateName("In Progress")

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerLinearStateSync)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true (linear-state change must persist)")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got, want := saved.GetLinearStateType(), "started"; got != want {
		t.Errorf("LinearStateType: got = %q, want = %q", got, want)
	}
	if got, want := saved.GetLinearStateName(), "In Progress"; got != want {
		t.Errorf("LinearStateName: got = %q, want = %q", got, want)
	}

	// Same-value re-save is a no-op: snapshot refresh after the previous
	// save means the linear-state fields don't look dirty this time.
	noopChanged, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("no-op Save: %v", err)
	}
	if noopChanged {
		t.Errorf("no-op changed: got = true, want = false (same linear-state must not re-write)")
	}
}

// TestReconcileIssue_GateTriggerPriority_ReactivationWins exercises the
// trigger-priority orchestration in reconcileIssue's gate stage: when a
// single reconcile observes BOTH a Linear workflow-state change AND the
// PRClosed reactivation reset condition, the gate-stage Save must use
// TriggerReactivated (not TriggerLinearStateSync) so the History entry
// records the more significant transition.
//
// Mirrors the orchestration at the top of reconcileIssue (gateMirror →
// gateApplyReactivationReset → conditional Save) directly, since
// scaffolding the full reconcileIssue path requires github + change-
// session mocks. Locks in the contract that the second helper's trigger
// overwrites the first's when both fire, AND that the persisted History
// entry's Trigger field reflects the post-overwrite value.
func TestReconcileIssue_GateTriggerPriority_ReactivationWins(t *testing.T) {
	// Fixture: Linear issue is now non-terminal (started), but State has
	// stale linear-state metadata (canceled) AND is in the bot-auto-cancel
	// terminal mode (Failed/PRClosed). Both gate predicates must fire.
	const initial = `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"pr_closed","linear_state_type":"canceled","linear_state_name":"Canceled"}`
	f := newLinearStateFixture(t, initial)
	f.issue.State.Type = "started"
	f.issue.State.Name = "In Progress"
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	gateTrigger := ""
	if gateMirrorLinearWorkflowState[State, *State](s, f.issue) {
		gateTrigger = TriggerLinearStateSync
	}
	if r.gateApplyReactivationReset(t.Context(), s, f.issue) {
		gateTrigger = TriggerReactivated
	}
	if gateTrigger != TriggerReactivated {
		t.Fatalf("trigger after both helpers fired: got = %q, want = %q (reactivation must win)", gateTrigger, TriggerReactivated)
	}

	sctx := WithActor(t.Context(), testActor)
	sctx = WithTrigger(sctx, gateTrigger)
	if _, err := mgr.Save(sctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	// Reactivation cleared the failure mode and bumped to Active — the
	// status transition triggers a History append using the Save's
	// trigger. Linear-state mirror's contribution to the same Save is
	// invisible in History (mirror-only changes don't transition Status/
	// Mode), but its field changes are persisted alongside.
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	if got, want := saved.History[0].Trigger, TriggerReactivated; got != want {
		t.Errorf("History[0].Trigger: got = %q, want = %q (reactivation must win over linear-state-sync)", got, want)
	}
	if got, want := saved.GetLinearStateType(), "started"; got != want {
		t.Errorf("LinearStateType: got = %q, want = %q (mirror change must persist alongside reactivation)", got, want)
	}
}

// TestStateManager_NotifyProgress_FiresCallback verifies the in-flight
// notification path delivers a marshaled state snapshot to the configured
// callback without writing the Linear attachment. This is the
// downstream-freshness primitive used before multi-minute agent runs.
func TestStateManager_NotifyProgress_FiresCallback(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)

	var (
		gotIssueID string
		gotJSON    []byte
	)
	mgr := r.NewStateManager(f.issue).SetSaveCallback(func(_ context.Context, issueID string, stateJSON []byte) {
		gotIssueID = issueID
		gotJSON = append([]byte(nil), stateJSON...)
	})

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := mgr.NotifyProgress(t.Context(), s); err != nil {
		t.Fatalf("NotifyProgress: %v", err)
	}

	if gotIssueID != f.issue.ID {
		t.Errorf("callback issueID: got = %q, want = %q", gotIssueID, f.issue.ID)
	}
	if len(gotJSON) == 0 {
		t.Fatal("callback received empty JSON")
	}
	var seen State
	if err := json.Unmarshal(gotJSON, &seen); err != nil {
		t.Fatalf("unmarshal callback JSON: %v", err)
	}
	if seen.Status != StatusActive {
		t.Errorf("callback Status: got = %q, want = %q", seen.Status, StatusActive)
	}
	// Linear must NOT be written — that's the whole point of NotifyProgress
	// vs Save (avoids amplifying the duplicate-attachment race).
	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("Linear save count after NotifyProgress: got = %d, want = 0", got)
	}
}

// TestStateManager_NotifyProgress_NoCallbackIsNoOp verifies NotifyProgress
// returns cleanly when no callback is configured (e.g., bot didn't opt into
// downstream mirroring). Important so framework code can call it
// unconditionally.
func TestStateManager_NotifyProgress_NoCallbackIsNoOp(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	// Deliberately do NOT call SetSaveCallback.
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := mgr.NotifyProgress(t.Context(), s); err != nil {
		t.Fatalf("NotifyProgress with no callback: got error %v, want nil", err)
	}
	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("Linear save count: got = %d, want = 0", got)
	}
}

// TestStateManager_NotifyProgress_DoesNotRefreshSnapshot verifies a subsequent
// Save still sees the original load-time snapshot for its dirty check, so
// changes that happened after Load (and were mirrored via NotifyProgress)
// still trip the dirty check and persist to Linear at end-of-reconcile.
func TestStateManager_NotifyProgress_DoesNotRefreshSnapshot(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue).SetSaveCallback(func(context.Context, string, []byte) {})

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Mutate findings (a change the dirty check covers) then NotifyProgress.
	wantFindings := []FindingRef{{Kind: "ciCheck", Identifier: "job-1"}}
	s.SetCurrentFindings(wantFindings)
	if err := mgr.NotifyProgress(t.Context(), s); err != nil {
		t.Fatalf("NotifyProgress: %v", err)
	}
	if got := f.saveCount.Load(); got != 0 {
		t.Fatalf("Linear save count after NotifyProgress: got = %d, want = 0", got)
	}

	// Now Save with the same mutated state. If NotifyProgress had refreshed
	// the snapshot, the dirty check would see findings as unchanged and
	// skip the Linear write — a regression that would silently mute
	// end-of-reconcile persistence.
	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("Save changed: got = false, want = true (NotifyProgress must not refresh the snapshot)")
	}
	if got := f.saveCount.Load(); got != 1 {
		t.Errorf("Linear save count after Save: got = %d, want = 1", got)
	}
}

// TestStateManager_NoDiffIterationCountPersist verifies that the
// consecutive-no-diff counter round-trips through Save/Load. Critical because
// the cap that fires FailureModeNoProgress relies on the counter accumulating
// across reconciles — if Linear's persisted state drops the field, every new
// reconcile starts from zero and the cap never trips.
func TestStateManager_NoDiffIterationCountPersist(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetNoDiffIterationCount(2)

	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true (counter change must persist)")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got, want := saved.GetNoDiffIterationCount(), 2; got != want {
		t.Errorf("NoDiffIterationCount: got = %d, want = %d", got, want)
	}

	// Same-value re-save is a no-op: snapshot refresh after the previous save
	// means the counter doesn't look dirty this time.
	noopChanged, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("no-op Save: %v", err)
	}
	if noopChanged {
		t.Errorf("no-op changed: got = true, want = false (same counter must not re-write)")
	}
}

// TestSaveNoDiffIterationState_IncrementsCounter verifies the no-diff path
// increments the counter from 0 → 1 on the first call without flipping the
// state machine.
func TestSaveNoDiffIterationState_IncrementsCounter(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active"}`)
	r := newReconcilerForFixture(t, f)

	if err := r.saveNoDiffIterationState(t.Context(), f.issue, nil, nil, TriggerCIFailureIteration); err != nil {
		t.Fatalf("saveNoDiffIterationState: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got, want := saved.GetNoDiffIterationCount(), 1; got != want {
		t.Errorf("NoDiffIterationCount: got = %d, want = %d", got, want)
	}
	if saved.Status != StatusActive {
		t.Errorf("Status: got = %q, want = %q (must not flip below cap)", saved.Status, StatusActive)
	}
	if saved.FailureMode != "" {
		t.Errorf("FailureMode: got = %q, want = empty (must not set below cap)", saved.FailureMode)
	}
}

// TestSaveNoDiffIterationState_TransitionsAtCap verifies the no-diff path
// transitions to StatusFailed + FailureModeNoProgress when the counter
// reaches maxNoDiffIterations. Seeded with one less than the cap so a single
// invocation crosses the threshold.
func TestSaveNoDiffIterationState_TransitionsAtCap(t *testing.T) {
	initial := fmt.Sprintf(`{"pr_url":"https://github.com/o/r/pull/1","status":"active","no_diff_iteration_count":%d}`, maxNoDiffIterations-1)
	f := newLinearStateFixture(t, initial)
	r := newReconcilerForFixture(t, f)

	if err := r.saveNoDiffIterationState(t.Context(), f.issue, nil, nil, TriggerCIFailureIteration); err != nil {
		t.Fatalf("saveNoDiffIterationState: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got, want := saved.GetNoDiffIterationCount(), maxNoDiffIterations; got != want {
		t.Errorf("NoDiffIterationCount: got = %d, want = %d", got, want)
	}
	if saved.Status != StatusFailed {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusFailed)
	}
	if saved.FailureMode != FailureModeNoProgress {
		t.Errorf("FailureMode: got = %q, want = %q", saved.FailureMode, FailureModeNoProgress)
	}
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d (cap-induced transition must append a History entry)", got, want)
	}
	entry := saved.History[0]
	if entry.To != StatusFailed || entry.Mode != FailureModeNoProgress {
		t.Errorf("History[0] to/mode: got = %q/%q, want = %q/%q", entry.To, entry.Mode, StatusFailed, FailureModeNoProgress)
	}
	if entry.Trigger != TriggerNoProgress {
		t.Errorf("History[0].Trigger: got = %q, want = %q (cap-induced transition must override the originating trigger)", entry.Trigger, TriggerNoProgress)
	}
}

// TestStateManager_FailureModeReclassification verifies a Failed→Failed
// transition with a different FailureMode persists the new mode.
func TestStateManager_FailureModeReclassification(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"failed","failure_mode":"max_turns"}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetFailureMode(FailureModePRClosed) // reclassify

	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true (mode change must persist)")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.FailureMode != FailureModePRClosed {
		t.Errorf("FailureMode: got = %q, want = %q", saved.FailureMode, FailureModePRClosed)
	}
	if got, want := len(saved.History), 1; got != want {
		t.Fatalf("History len: got = %d, want = %d", got, want)
	}
	if saved.History[0].Mode != FailureModePRClosed {
		t.Errorf("History[0].Mode: got = %q, want = %q", saved.History[0].Mode, FailureModePRClosed)
	}
}

// TestStateManager_HistoryAccumulates verifies that successive transitions
// build up a chronological History rather than overwriting prior entries.
func TestStateManager_HistoryAccumulates(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"active","history":[{"to":"active","at":"2026-04-29T11:00:00Z","actor":"test-bot","trigger":"initial_run"}]}`)
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusComplete)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRMerge)
	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Fatalf("changed: got = false, want = true")
	}

	var saved State
	loadSavedState(t, f, &saved)
	if got, want := len(saved.History), 2; got != want {
		t.Fatalf("History len: got = %d, want = %d (must append, not replace)", got, want)
	}
	if saved.History[0].Trigger != TriggerInitialRun {
		t.Errorf("History[0].Trigger: got = %q, want = %q (existing entry preserved)", saved.History[0].Trigger, TriggerInitialRun)
	}
	if saved.History[1].Trigger != TriggerPRMerge {
		t.Errorf("History[1].Trigger: got = %q, want = %q (new entry appended)", saved.History[1].Trigger, TriggerPRMerge)
	}
}

// TestStateManager_HistoryCapFIFOEvicts verifies that History stops growing
// past historyCap, with oldest entries dropped first.
func TestStateManager_HistoryCapFIFOEvicts(t *testing.T) {
	// Pre-fill state with historyCap entries so the next append triggers eviction.
	s := &State{}
	s.History = make([]StateTransition, historyCap)
	for i := range s.History {
		s.History[i] = StateTransition{To: StatusActive, Trigger: "fill", At: time.Unix(int64(i), 0).UTC()}
	}
	// One more append should evict the oldest.
	s.AppendHistory(StateTransition{To: StatusComplete, Trigger: "after-cap", At: time.Unix(int64(historyCap), 0).UTC()})

	if got, want := len(s.History), historyCap; got != want {
		t.Errorf("History len: got = %d, want = %d (cap should hold)", got, want)
	}
	if s.History[len(s.History)-1].Trigger != "after-cap" {
		t.Errorf("History tail: got = %q, want = %q", s.History[len(s.History)-1].Trigger, "after-cap")
	}
	if s.History[0].At.Unix() != 1 {
		t.Errorf("oldest entry should have been evicted: got first At = %v", s.History[0].At)
	}
}

// extendedState is a synthetic bot-side wrapper used to verify that bot-private
// fields round-trip through framework Save/Load unchanged. Mirrors what a real
// bot would do in its `internal/state` package.
type extendedState struct {
	State
	BotField string `json:"bot_field,omitempty"`
}

// TestStateManager_PreservesBotFields verifies the load-bearing claim that
// generic StateManager preserves bot-specific fields across framework writes.
// If this test ever fails, the embedding pattern is broken and bots will lose
// data on every reconcile.
func TestStateManager_PreservesBotFields(t *testing.T) {
	f := newLinearStateFixture(t, `{"pr_url":"https://github.com/o/r/pull/1","status":"active","bot_field":"keep-me"}`)

	// Construct a Reconciler over the bot's wrapper type.
	r := &Reconciler[testReq, testResp, testCB, extendedState, *extendedState]{
		identity:     testActor,
		linearClient: f.newClient(t),
	}
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.BotField != "keep-me" {
		t.Fatalf("bot field on Load: got = %q, want = %q", s.BotField, "keep-me")
	}
	s.SetStatus(StatusComplete) // framework-side mutation

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRMerge)
	if _, err := mgr.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var saved extendedState
	loadSavedState(t, f, &saved)
	if saved.BotField != "keep-me" {
		t.Errorf("bot field after framework Save: got = %q, want = %q (bot fields must round-trip)", saved.BotField, "keep-me")
	}
	if saved.Status != StatusComplete {
		t.Errorf("Status: got = %q, want = %q", saved.Status, StatusComplete)
	}
}

// TestStateManager_InjectsIssueIDAndURLOnSave verifies that StateManager
// captures issue.ID and issue.URL at construction and writes them into the
// persisted state on every save, regardless of whether the bot touched
// them. Bots get a self-contained attachment for free.
func TestStateManager_InjectsIssueIDAndURLOnSave(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"active"}`)
	f.issue.URL = "https://linear.app/example/issue/ENG-1"
	r := newReconcilerForFixture(t, f)
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusComplete)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRMerge)
	if _, err := mgr.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var saved State
	loadSavedState(t, f, &saved)
	if saved.IssueID != f.issue.ID {
		t.Errorf("IssueID: got = %q, want = %q", saved.IssueID, f.issue.ID)
	}
	if saved.IssueURL != f.issue.URL {
		t.Errorf("IssueURL: got = %q, want = %q", saved.IssueURL, f.issue.URL)
	}
}

// TestStateManager_SaveCallback_FiresWithStateJSONAndIssueID covers the
// post-save hook surface that downstream consumers plug into. The callback
// must fire on every successful save, receive the linear issue ID, and the
// marshaled bot State (post-Set of framework-managed IssueID/IssueURL).
func TestStateManager_SaveCallback_FiresWithStateJSONAndIssueID(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"active"}`)
	f.issue.URL = "https://linear.app/example/issue/ENG-1"
	r := newReconcilerForFixture(t, f)

	type capture struct {
		issueID string
		raw     []byte
		fired   int
	}
	var got capture
	mgr := r.NewStateManager(f.issue).SetSaveCallback(func(_ context.Context, issueID string, stateJSON []byte) {
		got.issueID = issueID
		got.raw = stateJSON
		got.fired++
	})

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.SetStatus(StatusComplete)

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerPRMerge)
	if _, err := mgr.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if got.fired != 1 {
		t.Fatalf("callback fired: got = %d, want = 1", got.fired)
	}
	if got.issueID != f.issue.ID {
		t.Errorf("callback issueID: got = %q, want = %q", got.issueID, f.issue.ID)
	}

	var captured State
	if err := json.Unmarshal(got.raw, &captured); err != nil {
		t.Fatalf("unmarshal callback stateJSON: %v", err)
	}
	if captured.IssueID != f.issue.ID {
		t.Errorf("captured IssueID: got = %q, want = %q", captured.IssueID, f.issue.ID)
	}
	if captured.IssueURL != f.issue.URL {
		t.Errorf("captured IssueURL: got = %q, want = %q", captured.IssueURL, f.issue.URL)
	}
	if captured.Status != StatusComplete {
		t.Errorf("captured Status: got = %q, want = %q", captured.Status, StatusComplete)
	}
}

// TestStateManager_SaveCallback_FiresOnNoOpSave verifies that the post-save
// callback fires even when the framework skips the Linear write. Downstream
// mirrors need to see every Save call so wrapper-specific fields populated
// by a bot's BeforeSave hook (e.g. iteration markers) reach observability
// stores even when the state machine didn't transition. The Linear save is
// still gated by the dirty check — the callback fire is independent.
func TestStateManager_SaveCallback_FiresOnNoOpSave(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"complete","pr_url":"https://github.com/o/r/pull/1"}`)
	r := newReconcilerForFixture(t, f)

	var fired int
	mgr := r.NewStateManager(f.issue).SetSaveCallback(func(_ context.Context, _ string, _ []byte) {
		fired++
	})

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// No mutation — Linear write is skipped (changed=false), but callback
	// fires anyway so the downstream mirror sees a fresh state JSON.
	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if changed {
		t.Errorf("changed: got = true, want = false (no field changed, Linear write should be skipped)")
	}
	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("save count: got = %d, want = 0 (no Linear write expected)", got)
	}
	if fired != 1 {
		t.Errorf("callback fired: got = %d, want = 1 (callback must fire even on no-op save)", fired)
	}
}

// stateWithBeforeSave is a synthetic bot wrapper that exercises the
// BeforeSave shadow pattern. callCount tracks invocations; forceSave
// controls the return value. BotField is populated from the trigger
// context so the test can assert that BeforeSave's mutations are visible
// in the callback's stateJSON.
type stateWithBeforeSave struct {
	State
	BotField string `json:"bot_field,omitempty"`

	callCount int
	forceSave bool
}

func (s *stateWithBeforeSave) BeforeSave(ctx context.Context) bool {
	s.callCount++
	if trigger, _ := TriggerFromContext(ctx); trigger != "" {
		s.BotField = "set-by-before-save:" + trigger
	}
	return s.forceSave
}

// TestStateManager_BeforeSave_CalledOnEveryNoOpSave verifies the bot's
// shadowed BeforeSave runs even when nothing else triggers a save, and
// that the wrapper-field mutations it makes reach the post-save callback
// even though Linear isn't written.
func TestStateManager_BeforeSave_CalledOnEveryNoOpSave(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"active","pr_url":"https://github.com/o/r/pull/1"}`)
	r := &Reconciler[testReq, testResp, testCB, stateWithBeforeSave, *stateWithBeforeSave]{
		identity:     testActor,
		linearClient: f.newClient(t),
	}

	var captured []byte
	mgr := r.NewStateManager(f.issue).SetSaveCallback(func(_ context.Context, _ string, stateJSON []byte) {
		captured = stateJSON
	})

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerCIFailureIteration)

	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if changed {
		t.Errorf("changed: got = true, want = false (BeforeSave returns false; no Linear write expected)")
	}
	if got := f.saveCount.Load(); got != 0 {
		t.Errorf("save count: got = %d, want = 0", got)
	}
	if s.callCount != 1 {
		t.Errorf("BeforeSave callCount: got = %d, want = 1", s.callCount)
	}

	var seen stateWithBeforeSave
	if err := json.Unmarshal(captured, &seen); err != nil {
		t.Fatalf("unmarshal callback stateJSON: %v", err)
	}
	if want := "set-by-before-save:" + TriggerCIFailureIteration; seen.BotField != want {
		t.Errorf("callback saw BotField: got = %q, want = %q (BeforeSave mutations must reach the callback)", seen.BotField, want)
	}
}

// TestStateManager_BeforeSave_ForceSaveTriggersLinear verifies that a bot
// that returns true from BeforeSave forces a Linear write even when no
// framework-tracked field changed. The escape hatch lets bots persist
// wrapper-specific changes durably to the attachment when needed.
func TestStateManager_BeforeSave_ForceSaveTriggersLinear(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"active","pr_url":"https://github.com/o/r/pull/1"}`)
	r := &Reconciler[testReq, testResp, testCB, stateWithBeforeSave, *stateWithBeforeSave]{
		identity:     testActor,
		linearClient: f.newClient(t),
	}
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s.forceSave = true

	ctx := WithActor(t.Context(), testActor)
	ctx = WithTrigger(ctx, TriggerCIFailureIteration)

	changed, err := mgr.Save(ctx, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !changed {
		t.Errorf("changed: got = false, want = true (BeforeSave returned true; Linear write expected)")
	}
	if got := f.saveCount.Load(); got != 1 {
		t.Errorf("save count: got = %d, want = 1", got)
	}

	var saved stateWithBeforeSave
	loadSavedState(t, f, &saved)
	if want := "set-by-before-save:" + TriggerCIFailureIteration; saved.BotField != want {
		t.Errorf("saved BotField: got = %q, want = %q", saved.BotField, want)
	}
	// Status didn't change, so no History entry should be appended even
	// though the Linear write fired — the diff-based history append is
	// independent of the Linear-write decision.
	if got := len(saved.History); got != 0 {
		t.Errorf("History len: got = %d, want = 0 (BeforeSave force-save must not fabricate transitions)", got)
	}
}

// TestStateManager_BeforeSave_DefaultIsNoOp verifies that a bot wrapper
// that does not shadow BeforeSave inherits the framework's no-op default
// via embedding. This is the load-bearing claim for the DX promise that
// future bots get the hook for free without implementing anything.
func TestStateManager_BeforeSave_DefaultIsNoOp(t *testing.T) {
	f := newLinearStateFixture(t, `{"status":"complete","pr_url":"https://github.com/o/r/pull/1"}`)
	// extendedState (defined above) does NOT shadow BeforeSave, so it
	// inherits the *State.BeforeSave default returning false.
	r := &Reconciler[testReq, testResp, testCB, extendedState, *extendedState]{
		identity:     testActor,
		linearClient: f.newClient(t),
	}
	mgr := r.NewStateManager(f.issue)

	s, _, err := mgr.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// No mutation, no shadowed BeforeSave → no Linear write.
	changed, err := mgr.Save(t.Context(), s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if changed {
		t.Errorf("changed: got = true, want = false (default BeforeSave must not force-save)")
	}
}

// TestState_SetCurrentPendingChecks_TruncatesAndSortsAtCap locks in the
// pendingChecksCap safety net: callers handing in more than the cap get
// the alphabetically-first N entries, deterministically, regardless of
// caller-supplied order. A future bump to the cap should be a one-line
// const change here — this test guards against the cap being silently
// removed or the sort being dropped.
func TestState_SetCurrentPendingChecks_TruncatesAndSortsAtCap(t *testing.T) {
	// Generate cap+10 entries in REVERSE alphabetical order so the sort
	// has to actually do work, then assert the persisted slice is the
	// alphabetical first cap entries.
	const overflow = 10
	input := make([]string, 0, pendingChecksCap+overflow)
	for i := pendingChecksCap + overflow; i > 0; i-- {
		input = append(input, fmt.Sprintf("check-%03d", i))
	}

	var s State
	s.SetCurrentPendingChecks(input)

	got := s.GetCurrentPendingChecks()
	if len(got) != pendingChecksCap {
		t.Fatalf("len: got = %d, want = %d", len(got), pendingChecksCap)
	}
	for i, c := range got {
		want := fmt.Sprintf("check-%03d", i+1)
		if c != want {
			t.Errorf("got[%d] = %q, want = %q (entries past the cap should be the alphabetical tail)", i, c, want)
		}
	}
}
