/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package agenttrace

import (
	"context"
)

// tracerKey is the context key for storing values of type T
type tracerKey[T any] struct{}

// Tracer is the interface for creating and managing traces
type Tracer[T any] interface {
	// NewTrace creates a new trace with the given prompt. opts customize
	// root-span attributes (agent name, per-invocation label callback, etc.).
	NewTrace(ctx context.Context, prompt string, opts ...StartTraceOption) *Trace[T]
	// RecordTrace records a completed trace
	RecordTrace(trace *Trace[T])
}

// WithTracer returns a new context with the given tracer
func WithTracer[T any](ctx context.Context, tracer Tracer[T]) context.Context {
	return context.WithValue(ctx, tracerKey[T]{}, tracer)
}

// TracerFromContext returns the tracer from the context, or creates a default tracer
func TracerFromContext[T any](ctx context.Context) Tracer[T] {
	if tracer, ok := ctx.Value(tracerKey[T]{}).(Tracer[T]); ok {
		return tracer
	}
	return NewDefaultTracer[T](ctx)
}

// StartTrace starts a new trace using the tracer from the context and returns
// the trace along with a done callback. The caller must invoke done(result, err)
// when the operation completes; this fills in the trace and records it via the
// tracer. Capturing the tracer at start time means decorator composition works
// without a second context lookup.
//
// opts customize the root invoke_agent span attributes — e.g. WithAgentName
// stamps gen_ai.agent.name, and WithNameFn produces a dynamic
// driftlessaf.invocation.label based on ExecutionContext.
func StartTrace[T any](ctx context.Context, prompt string, opts ...StartTraceOption) (*Trace[T], func(T, error)) {
	tracer := TracerFromContext[T](ctx)
	trace := tracer.NewTrace(ctx, prompt, opts...)
	done := func(result T, err error) {
		trace.complete(result, err)
		tracer.RecordTrace(trace)
	}
	return trace, done
}
