/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

import "context"

// Generic context plumbing for state-machine bookkeeping and bot-side
// telemetry correlation. The keys carry opaque strings — the framework does
// not endorse any particular vocabulary for actor identities or trigger
// names. Bots and the framework agree on values out-of-band.

type actorKey struct{}
type triggerKey struct{}
type linearIssueIDKey struct{}

// WithActor returns a context carrying the actor identity (e.g. the bot's
// own identity, or "manual" for human edits). StateManager.Save reads this
// when appending a StateTransition to History.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext returns the actor identity stored on ctx, and true when
// one is present.
func ActorFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(actorKey{}).(string)
	return v, ok
}

// WithTrigger returns a context carrying the trigger string for the next
// state transition (see Trigger* constants in this package, e.g.
// TriggerPRMerge, TriggerCIFailureIteration). StateManager.Save reads this
// when appending to History; bots set it before calling Save.
func WithTrigger(ctx context.Context, trigger string) context.Context {
	return context.WithValue(ctx, triggerKey{}, trigger)
}

// TriggerFromContext returns the trigger string stored on ctx, and true
// when one is present.
func TriggerFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(triggerKey{}).(string)
	return v, ok
}

// WithLinearIssueID returns a context carrying the Linear issue UUID this
// reconcile is operating on. The framework injects this before calling the
// bot's agent so bot-side decorators (telemetry capture, etc.) can correlate
// log events to issues without piggy-backing on the agent's request struct.
func WithLinearIssueID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, linearIssueIDKey{}, id)
}

// LinearIssueIDFromContext returns the Linear issue UUID stored on ctx, and
// true when one is present.
func LinearIssueIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(linearIssueIDKey{}).(string)
	return v, ok
}
