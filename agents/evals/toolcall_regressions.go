/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package evals

import (
	"fmt"
	"strings"

	"chainguard.dev/driftlessaf/agents/agenttrace"
)

// NoReadFileOnDirectory fails the trace if any read_file call errored with
// "is a directory". This catches the staging-regression class where the agent
// passes a directory path to read_file instead of using list_directory first.
func NoReadFileOnDirectory[T any]() ObservableTraceCallback[T] {
	return func(o Observer, trace *agenttrace.Trace[T]) {
		for _, tc := range trace.ToolCalls {
			if tc.Name != "read_file" || tc.Error == nil {
				continue
			}
			if strings.Contains(tc.Error.Error(), "is a directory") {
				path, _ := tc.Params["path"].(string)
				o.Fail(fmt.Sprintf("read_file called on directory %q: %v", path, tc.Error))
				return
			}
		}
	}
}

// NoHallucinatedPaths fails the trace if any read_file or list_directory call
// errored with "no such file or directory". This catches the class where the
// agent invents a path that does not exist in the repo.
func NoHallucinatedPaths[T any]() ObservableTraceCallback[T] {
	return func(o Observer, trace *agenttrace.Trace[T]) {
		for _, tc := range trace.ToolCalls {
			if tc.Error == nil {
				continue
			}
			if tc.Name != "read_file" && tc.Name != "list_directory" {
				continue
			}
			if strings.Contains(tc.Error.Error(), "no such file or directory") {
				path, _ := tc.Params["path"].(string)
				o.Fail(fmt.Sprintf("%s called on non-existent path %q: %v", tc.Name, path, tc.Error))
				return
			}
		}
	}
}

// EditStringExists fails the trace if any edit_file call errored because the
// supplied old_string was not found in the target file. This catches the class
// where the agent fabricates or misremembers the string to be replaced.
func EditStringExists[T any]() ObservableTraceCallback[T] {
	return func(o Observer, trace *agenttrace.Trace[T]) {
		for _, tc := range trace.ToolCalls {
			if tc.Name != "edit_file" || tc.Error == nil {
				continue
			}
			msg := tc.Error.Error()
			if strings.Contains(msg, "old_string not found") || strings.Contains(msg, "old string not found") {
				path, _ := tc.Params["path"].(string)
				o.Fail(fmt.Sprintf("edit_file old_string did not match in %q: %v", path, tc.Error))
				return
			}
		}
	}
}

// ValidRegexPattern fails the trace if any search_codebase call errored because
// the supplied pattern was not a valid Go RE2 regex. Common failure modes:
// Perl-syntax features (lookarounds like (?!), backreferences), invalid escape
// sequences (\u, \p), and bare repetition operators (+, * without a base).
func ValidRegexPattern[T any]() ObservableTraceCallback[T] {
	return func(o Observer, trace *agenttrace.Trace[T]) {
		for _, tc := range trace.ToolCalls {
			if tc.Name != "search_codebase" || tc.Error == nil {
				continue
			}
			if strings.Contains(tc.Error.Error(), "parsing regexp") {
				pattern, _ := tc.Params["pattern"].(string)
				o.Fail(fmt.Sprintf("search_codebase used invalid regex pattern %q: %v", pattern, tc.Error))
				return
			}
		}
	}
}
