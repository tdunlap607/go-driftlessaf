/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import (
	"context"
	"fmt"
	"time"

	"chainguard.dev/driftlessaf/reconcilers/linearreconciler"
	"github.com/chainguard-dev/clog"
)

// StateManager is a generic wrapper around linearreconciler.StateManager
// that adds state-machine bookkeeping. It is constructed by
// Reconciler.NewStateManager(issue), parameterised over the bot's wrapper
// State type.
//
// Usage shape:
//
//	mgr := r.NewStateManager(issue)
//	state, _, err := mgr.Load(ctx)
//	state.SetStatus(metareconciler.StatusActive)
//	state.SetPRURL(prURL)
//	ctx = metareconciler.WithTrigger(ctx, metareconciler.TriggerInitialRun)
//	changed, err := mgr.Save(ctx, state)
//
// Save snapshots Status and FailureMode at Load time, diffs them at Save
// time, and appends a StateTransition to History automatically when
// something changed. Callers therefore never write to History directly.
//
// The (changed bool, err error) return from Save lets callers gate
// side-effects like posting a one-time bot comment on PR open.
type StateManager[T any, PT StateConstraint[T]] struct {
	sm                  *linearreconciler.StateManager
	snapshotStatus      Status
	snapshotFailureMode FailureMode
	snapshotPRURL       string
	loaded              bool
	now                 func() time.Time // injectable clock for tests
}

// NewStateManager returns a fresh state-machine-aware manager for the given
// issue, bound to the Reconciler's linearClient. Each manager is single-use
// within one reconcile pass; do not share across goroutines or reuse across
// reconciles (it caches the snapshot from Load to diff against at Save).
func (r *Reconciler[Req, Resp, CB, T, PT]) NewStateManager(issue *linearreconciler.Issue) *StateManager[T, PT] {
	return NewStateManager[T, PT](r.linearClient, issue)
}

// NewStateManager is the free-function form of (*Reconciler).NewStateManager,
// for callers that need a state-machine-aware manager before the Reconciler
// is constructed (e.g. a RepoTargetResolver supplied as an option to New).
// Type parameters must be spelled out explicitly:
//
//	mgr := metareconciler.NewStateManager[mybot.State, *mybot.State](client, issue)
func NewStateManager[T any, PT StateConstraint[T]](client *linearreconciler.Client, issue *linearreconciler.Issue) *StateManager[T, PT] {
	return &StateManager[T, PT]{
		sm:  client.NewStateManager(issue),
		now: time.Now,
	}
}

// Load deserializes the state attachment into a fresh PT. Returns the loaded
// instance, whether an attachment was found, and any error. Status,
// FailureMode, and PRURL at load time are snapshotted internally; subsequent
// Save calls diff against this snapshot to decide whether to persist and
// whether to append a History entry.
//
// When no attachment exists, Load returns a zero-valued PT with loaded=false.
// Callers can mutate it and Save to create the attachment.
func (m *StateManager[T, PT]) Load(ctx context.Context) (PT, bool, error) {
	var t T
	pt := PT(&t)
	loaded, err := m.sm.Load(ctx, pt)
	if err != nil {
		return pt, false, fmt.Errorf("load state: %w", err)
	}
	m.snapshotStatus = pt.GetStatus()
	m.snapshotFailureMode = pt.GetFailureMode()
	m.snapshotPRURL = pt.GetPRURL()
	m.loaded = loaded
	return pt, loaded, nil
}

// Save persists the state, automatically appending a StateTransition to
// History when Status or FailureMode differs from the snapshot taken at
// Load. Returns (changed=true, nil) when something was actually persisted;
// callers can use this to gate side-effects.
//
// Invariants enforced:
//
//   - History append on Status or FailureMode change. Actor + Trigger are
//     read from context (use metareconciler.WithActor / WithTrigger). A
//     missing actor or trigger results in empty fields on the entry —
//     silent and harmless, but the bot is responsible for setting them.
//   - FailureMode is cleared automatically when transitioning to a non-
//     StatusFailed status. Observability never sees Status=active alongside
//     a stale FailureMode.
//   - History is capped at historyCap entries with FIFO eviction.
//     Downstream mirrors are the place for unbounded analytics history.
//   - Same-status, same-mode, same-PRURL Save is a no-op (returns false, nil).
//     Skipping the save avoids the feedback loop where a save would trigger
//     an attachment-update event that re-enqueues the issue immediately.
//
// The whole PT (including bot-specific fields embedded alongside State) is
// serialized; bot fields persist through framework Saves because they live
// on the wrapper type the manager is generic over.
func (m *StateManager[T, PT]) Save(ctx context.Context, pt PT) (bool, error) {
	currentStatus := pt.GetStatus()
	currentFailureMode := pt.GetFailureMode()
	currentPRURL := pt.GetPRURL()

	// FailureMode invariant: clear on transition out of StatusFailed before
	// computing the diff, so the History entry's Mode field reflects the
	// post-clear value.
	if currentStatus != StatusFailed && currentFailureMode != "" {
		pt.SetFailureMode("")
		currentFailureMode = ""
	}

	statusOrModeChanged := currentStatus != m.snapshotStatus || currentFailureMode != m.snapshotFailureMode
	prURLChanged := currentPRURL != m.snapshotPRURL

	// Persist whenever any tracked field changed, OR when we never loaded
	// (first save creates the attachment). PRURL-only changes save without
	// a History append — a from==to entry would mislead observability — but
	// the URL itself must persist so consumers see the current PR.
	dirty := statusOrModeChanged || prURLChanged || !m.loaded

	if !dirty {
		return false, nil
	}

	if statusOrModeChanged {
		actor, _ := ActorFromContext(ctx)
		trigger, _ := TriggerFromContext(ctx)
		pt.AppendHistory(StateTransition{
			From:    m.snapshotStatus,
			To:      currentStatus,
			At:      m.now().UTC(),
			Actor:   actor,
			Trigger: trigger,
			Mode:    currentFailureMode,
		})
	}

	if err := m.sm.Save(ctx, pt); err != nil {
		return false, fmt.Errorf("save state: %w", err)
	}

	clog.InfoContext(ctx, "Reconciler state saved",
		"from", m.snapshotStatus,
		"to", currentStatus,
		"changed", statusOrModeChanged,
	)

	// Refresh the snapshot so a second Save in the same reconcile (rare,
	// but possible) doesn't re-append the same transition or re-flag the
	// PRURL as changed.
	m.snapshotStatus = currentStatus
	m.snapshotFailureMode = currentFailureMode
	m.snapshotPRURL = currentPRURL
	m.loaded = true

	return true, nil
}

// UpsertBotComment passes through to the underlying StateManager. Exposed
// here so callers don't need to reach past StateManager[T, PT] to the
// linearreconciler.StateManager for non-state Linear operations.
func (m *StateManager[T, PT]) UpsertBotComment(ctx context.Context, body string) error {
	return m.sm.UpsertBotComment(ctx, body)
}

// UnprocessedComments passes through to the underlying StateManager.
func (m *StateManager[T, PT]) UnprocessedComments(botUserID string) []linearreconciler.Comment {
	return m.sm.UnprocessedComments(botUserID)
}
