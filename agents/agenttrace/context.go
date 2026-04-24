/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package agenttrace

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// ExecutionContext provides reconciler-level context for agent executions.
// This context is used to enrich metrics with labels for tracking token usage
// and tool calls per reconciler (PR, path, etc.).
type ExecutionContext struct {
	ReconcilerKey  string `json:"reconciler_key,omitempty"`  // Primary identifier: "pr:chainguard-dev/enterprise-packages/41025" or "path:chainguard-dev/mono/main/images/nginx"
	ReconcilerType string `json:"reconciler_type,omitempty"` // Type of reconciler: "pr" or "path"
	CommitSHA      string `json:"commit_sha,omitempty"`      // Git commit SHA (optional, for git-based reconcilers)
	TurnNumber     int    `json:"turn_number,omitempty"`     // Turn number for multi-turn agents (optional, 1, 2, 3, ...)
}

// Repository extracts the repository from the reconciler key.
// For "pr:chainguard-dev/enterprise-packages/41025" returns "chainguard-dev/enterprise-packages"
// For "path:chainguard-dev/mono/main/images/nginx" returns "chainguard-dev/mono"
// Returns empty string if the format is invalid.
func (e ExecutionContext) Repository() string {
	if e.ReconcilerKey == "" {
		return ""
	}

	// Split at colon to get the identifier part
	_, identifier, found := strings.Cut(e.ReconcilerKey, ":")
	if !found {
		return ""
	}

	// Find the second slash to extract "owner/repo"
	firstSlash := strings.IndexByte(identifier, '/')
	if firstSlash == -1 {
		return ""
	}

	secondSlash := strings.IndexByte(identifier[firstSlash+1:], '/')
	if secondSlash == -1 {
		return ""
	}

	return identifier[:firstSlash+1+secondSlash]
}

// EnrichAttributes adds execution context attributes to the provided base attributes.
// This is used to enrich metrics with reconciler context using only BOUNDED labels.
//
// Note: reconciler_key and commit_sha are NOT included in metrics to prevent unbounded
// cardinality (every PR and commit creates a new time series). These fields remain in
// the ExecutionContext for traces where cardinality is not a concern. Use trace exemplars
// to link from aggregated metrics to detailed per-PR traces.
func (e ExecutionContext) EnrichAttributes(baseAttrs []attribute.KeyValue) []attribute.KeyValue {
	// Pre-allocate for base + up to 3 additional attributes
	attrs := make([]attribute.KeyValue, len(baseAttrs), len(baseAttrs)+3)
	copy(attrs, baseAttrs)

	// Add reconciler type (bounded: "pr" or "path")
	if e.ReconcilerType != "" {
		attrs = append(attrs, attribute.String("reconciler_type", e.ReconcilerType))
	}

	// Extract and add repository from reconciler_key for aggregation
	// This is bounded: ~100-500 repositories vs unlimited PRs
	if repo := e.Repository(); repo != "" {
		attrs = append(attrs, attribute.String("repository", repo))
	}

	// Add turn number (bounded: typically 0-10 for multi-turn agents)
	if e.TurnNumber > 0 {
		attrs = append(attrs, attribute.Int("turn", e.TurnNumber))
	}

	return attrs
}

// contextKey is used for storing execution context in context.Context
type contextKey string

const (
	executionContextKey contextKey = "execution_context"
	agentNameKey        contextKey = "agent_name"
	nameFnKey           contextKey = "name_fn"
	payloadsEnabledKey  contextKey = "payloads_enabled"
)

// WithExecutionContext adds execution context to the Go context
func WithExecutionContext(ctx context.Context, execCtx ExecutionContext) context.Context {
	return context.WithValue(ctx, executionContextKey, execCtx)
}

// GetExecutionContext retrieves execution context from the Go context
func GetExecutionContext(ctx context.Context) ExecutionContext {
	if val := ctx.Value(executionContextKey); val != nil {
		if execCtx, ok := val.(ExecutionContext); ok {
			return execCtx
		}
	}
	return ExecutionContext{}
}

// WithDefaultAgentName returns a context carrying the given agent name so
// any subsequent StartTrace call without an explicit WithAgentName option
// emits gen_ai.agent.name=<name> on the root invoke_agent span. Callers
// building a reconciler that drives multiple executors can set this once
// at the top of the chain to attach a stable agent name (e.g. "loganalyzer",
// "judge", "fixer") to every trace.
func WithDefaultAgentName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, agentNameKey, name)
}

// GetDefaultAgentName returns the agent name stored in the context by
// WithDefaultAgentName, or "" if none was set.
func GetDefaultAgentName(ctx context.Context) string {
	if val, ok := ctx.Value(agentNameKey).(string); ok {
		return val
	}
	return ""
}

// WithDefaultNameFn returns a context carrying a name function invoked by
// newTrace to build the braintrust.span_attributes.name attribute from the
// ExecutionContext. Callers at the reconciler layer (where the GitHub
// Resource is available) use this to stamp PR-aware labels such as
// "autofix: pr:chainguard-dev/mono#38632" on every subsequent trace.
func WithDefaultNameFn(ctx context.Context, fn func(ExecutionContext) string) context.Context {
	return context.WithValue(ctx, nameFnKey, fn)
}

// GetDefaultNameFn returns the name function stored in the context by
// WithDefaultNameFn, or nil if none was set.
func GetDefaultNameFn(ctx context.Context) func(ExecutionContext) string {
	if val, ok := ctx.Value(nameFnKey).(func(ExecutionContext) string); ok {
		return val
	}
	return nil
}

// WithPayloadsEnabled returns a context that opts in (or out) of emitting
// raw prompt / completion payloads as OTel attributes on the root
// invoke_agent span (gen_ai.prompt, gen_ai.input.messages, gen_ai.completion,
// gen_ai.output.messages). The default — when nothing is set on ctx — is
// false so library consumers don't accidentally leak PII-bearing prompts
// to a third-party eval backend.
//
// Consumer main packages read their own env var (e.g. DRIFTLESSAF_LLM_PAYLOADS)
// at startup and set this flag on the base context before handing off to
// the reconciler or executor. Keeping the decision on ctx (rather than a
// process-wide env read at package init) matches the repository's
// go-standards rule that library packages accept configuration as
// parameters instead of reading the environment directly.
func WithPayloadsEnabled(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, payloadsEnabledKey, enabled)
}

// payloadsEnabledFrom returns the opt-in flag stored in ctx by
// WithPayloadsEnabled, or false when nothing was set. Kept unexported
// because callers outside this package should make the policy decision
// via the setter, not observe it.
func payloadsEnabledFrom(ctx context.Context) bool {
	if val, ok := ctx.Value(payloadsEnabledKey).(bool); ok {
		return val
	}
	return false
}
