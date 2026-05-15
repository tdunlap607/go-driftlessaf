/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package evals_test

import (
	"errors"
	"testing"

	"chainguard.dev/driftlessaf/agents/agenttrace"
	"chainguard.dev/driftlessaf/agents/evals"
)

func TestNoReadFileOnDirectory(t *testing.T) {
	tests := []struct {
		name       string
		toolCalls  []*agenttrace.ToolCall[string]
		expectFail bool
	}{
		{
			name:       "no tool calls",
			toolCalls:  nil,
			expectFail: false,
		},
		{
			name: "read_file succeeded",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "read_file", Params: map[string]any{"path": "foo.go"}},
			},
			expectFail: false,
		},
		{
			name: "read_file on directory",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "read_file", Params: map[string]any{"path": "foo"}, Error: errors.New("read /tmp/foo: is a directory")},
			},
			expectFail: true,
		},
		{
			name: "read_file failed with a different error",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "read_file", Params: map[string]any{"path": "foo"}, Error: errors.New("permission denied")},
			},
			expectFail: false,
		},
		{
			name: "list_directory error is not flagged",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "list_directory", Params: map[string]any{"path": "foo"}, Error: errors.New("read /tmp/foo: is a directory")},
			},
			expectFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := &mockObserver{}
			trace := &agenttrace.Trace[string]{ToolCalls: tt.toolCalls}
			evals.NoReadFileOnDirectory[string]()(obs, trace)
			gotFail := len(obs.failures) > 0
			if gotFail != tt.expectFail {
				t.Errorf("got fail=%v, want %v (failures=%v)", gotFail, tt.expectFail, obs.failures)
			}
		})
	}
}

func TestNoHallucinatedPaths(t *testing.T) {
	tests := []struct {
		name       string
		toolCalls  []*agenttrace.ToolCall[string]
		expectFail bool
	}{
		{
			name: "read_file on missing path",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "read_file", Params: map[string]any{"path": "missing.go"}, Error: errors.New("open /tmp/missing.go: no such file or directory")},
			},
			expectFail: true,
		},
		{
			name: "list_directory on missing path",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "list_directory", Params: map[string]any{"path": "missing"}, Error: errors.New("open /tmp/missing: no such file or directory")},
			},
			expectFail: true,
		},
		{
			name: "edit_file with same error is not flagged",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "edit_file", Params: map[string]any{"path": "missing.go"}, Error: errors.New("open /tmp/missing.go: no such file or directory")},
			},
			expectFail: false,
		},
		{
			name: "read_file with different error is not flagged",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "read_file", Params: map[string]any{"path": "foo"}, Error: errors.New("is a directory")},
			},
			expectFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := &mockObserver{}
			trace := &agenttrace.Trace[string]{ToolCalls: tt.toolCalls}
			evals.NoHallucinatedPaths[string]()(obs, trace)
			gotFail := len(obs.failures) > 0
			if gotFail != tt.expectFail {
				t.Errorf("got fail=%v, want %v (failures=%v)", gotFail, tt.expectFail, obs.failures)
			}
		})
	}
}

func TestEditStringExists(t *testing.T) {
	tests := []struct {
		name       string
		toolCalls  []*agenttrace.ToolCall[string]
		expectFail bool
	}{
		{
			name: "edit_file succeeded",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "edit_file", Params: map[string]any{"path": "foo.go"}},
			},
			expectFail: false,
		},
		{
			name: "edit_file old_string snake_case mismatch",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "edit_file", Params: map[string]any{"path": "foo.go"}, Error: errors.New("old_string not found in file")},
			},
			expectFail: true,
		},
		{
			name: "edit_file old string space mismatch",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "edit_file", Params: map[string]any{"path": "foo.go"}, Error: errors.New("old string not found in \"foo.go\"")},
			},
			expectFail: true,
		},
		{
			name: "edit_file with unrelated error is not flagged",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "edit_file", Params: map[string]any{"path": "foo.go"}, Error: errors.New("permission denied")},
			},
			expectFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := &mockObserver{}
			trace := &agenttrace.Trace[string]{ToolCalls: tt.toolCalls}
			evals.EditStringExists[string]()(obs, trace)
			gotFail := len(obs.failures) > 0
			if gotFail != tt.expectFail {
				t.Errorf("got fail=%v, want %v (failures=%v)", gotFail, tt.expectFail, obs.failures)
			}
		})
	}
}

func TestValidRegexPattern(t *testing.T) {
	tests := []struct {
		name       string
		toolCalls  []*agenttrace.ToolCall[string]
		expectFail bool
	}{
		{
			name: "search_codebase succeeded",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "search_codebase", Params: map[string]any{"pattern": "foo"}},
			},
			expectFail: false,
		},
		{
			name: "search_codebase perl lookaround",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "search_codebase", Params: map[string]any{"pattern": "(?!foo)"}, Error: errors.New("invalid pattern: error parsing regexp: invalid or unsupported Perl syntax: `(?!`")},
			},
			expectFail: true,
		},
		{
			name: "search_codebase invalid escape",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "search_codebase", Params: map[string]any{"pattern": ":= \\u0026"}, Error: errors.New("invalid pattern: error parsing regexp: invalid escape sequence: `\\u`")},
			},
			expectFail: true,
		},
		{
			name: "search_codebase bare repetition",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "search_codebase", Params: map[string]any{"pattern": "+= 1"}, Error: errors.New("invalid pattern: error parsing regexp: missing argument to repetition operator: `+`")},
			},
			expectFail: true,
		},
		{
			name: "search_codebase with unrelated error is not flagged",
			toolCalls: []*agenttrace.ToolCall[string]{
				{Name: "search_codebase", Params: map[string]any{"pattern": "foo"}, Error: errors.New("path not found")},
			},
			expectFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := &mockObserver{}
			trace := &agenttrace.Trace[string]{ToolCalls: tt.toolCalls}
			evals.ValidRegexPattern[string]()(obs, trace)
			gotFail := len(obs.failures) > 0
			if gotFail != tt.expectFail {
				t.Errorf("got fail=%v, want %v (failures=%v)", gotFail, tt.expectFail, obs.failures)
			}
		})
	}
}
