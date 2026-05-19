/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package agenttrace

import (
	"testing"
)

func TestWithExecutionContext_preservesUpstreamFields(t *testing.T) {
	// Guards against the original manifest-gen footgun: a deep call site that
	// owned one field (TurnNumber) and called WithExecutionContext with a
	// partial struct used to wipe ReconcilerKey/ReconcilerType/CommitSHA set
	// by the enclosing reconciler. Non-zero-merge semantics must preserve
	// untouched fields.
	ctx := WithExecutionContext(t.Context(), ExecutionContext{
		ReconcilerKey:  "pr:chainguard-dev/mono/40044",
		ReconcilerType: "pr",
		CommitSHA:      "0adc2e9",
	})

	ctx = WithExecutionContext(ctx, ExecutionContext{TurnNumber: 3})

	got := GetExecutionContext(ctx)
	if got.ReconcilerKey != "pr:chainguard-dev/mono/40044" {
		t.Errorf("ReconcilerKey clobbered: %q", got.ReconcilerKey)
	}
	if got.ReconcilerType != "pr" {
		t.Errorf("ReconcilerType clobbered: %q", got.ReconcilerType)
	}
	if got.CommitSHA != "0adc2e9" {
		t.Errorf("CommitSHA clobbered: %q", got.CommitSHA)
	}
	if got.TurnNumber != 3 {
		t.Errorf("TurnNumber not applied: %d", got.TurnNumber)
	}
}

func TestWithExecutionContext_overridesOnNonZero(t *testing.T) {
	ctx := WithExecutionContext(t.Context(), ExecutionContext{
		ReconcilerKey: "pr:a/b/1",
		CommitSHA:     "old-sha",
		TurnNumber:    1,
	})

	ctx = WithExecutionContext(ctx, ExecutionContext{
		CommitSHA:  "new-sha",
		TurnNumber: 2,
	})

	got := GetExecutionContext(ctx)
	if got.ReconcilerKey != "pr:a/b/1" {
		t.Errorf("ReconcilerKey should remain: %q", got.ReconcilerKey)
	}
	if got.CommitSHA != "new-sha" {
		t.Errorf("CommitSHA should be overridden: %q", got.CommitSHA)
	}
	if got.TurnNumber != 2 {
		t.Errorf("TurnNumber should be overridden: %d", got.TurnNumber)
	}
}

func TestWithExecutionContext_emptyCtx(t *testing.T) {
	ctx := WithExecutionContext(t.Context(), ExecutionContext{
		ReconcilerKey:  "path:a/b@main:c",
		ReconcilerType: "path",
	})

	got := GetExecutionContext(ctx)
	if got.ReconcilerKey != "path:a/b@main:c" || got.ReconcilerType != "path" {
		t.Errorf("merge on empty ctx failed: %+v", got)
	}
}
