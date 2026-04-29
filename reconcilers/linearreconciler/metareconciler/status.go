/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package metareconciler

// Status is the materializer's progress state on a Linear issue. It is a
// distinct string type so callers cannot assign arbitrary strings (typos
// like "actve" become compile-time errors).
type Status string

// Status values persisted on the Linear issue's state attachment.
const (
	StatusActive         Status = "active"
	StatusComplete       Status = "complete"
	StatusFailed         Status = "failed"
	StatusWaitingForRepo Status = "waiting_for_repo"
)

// FailureMode classifies why MaterializerState landed on StatusFailed.
// Only the modes we have detection paths for today are defined; new modes
// are added when their detection logic lands.
type FailureMode string

// FailureMode values persisted alongside StatusFailed on the state attachment.
const (
	// FailureModeMaxTurns means the agent exhausted its commit budget without
	// converging on a green PR. The PR also gets a "turn-limit" label.
	FailureModeMaxTurns FailureMode = "max_turns"
	// FailureModePRClosed means a human closed the PR without merging it,
	// abandoning the work.
	FailureModePRClosed FailureMode = "pr_closed"
)

// MaterializerState tracks the materializer's progress on a Linear issue.
// This is persisted as a file attachment on the issue via the StateManager
// with the "materializer" prefix.
type MaterializerState struct {
	PRURL       string      `json:"pr_url,omitempty"`
	Status      Status      `json:"status,omitempty"`
	FailureMode FailureMode `json:"failure_mode,omitempty"`
}
