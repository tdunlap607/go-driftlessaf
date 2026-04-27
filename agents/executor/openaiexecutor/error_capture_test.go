/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package openaiexecutor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"chainguard.dev/driftlessaf/agents/agenttrace"
	"chainguard.dev/driftlessaf/agents/executor/openaiexecutor"
	"chainguard.dev/driftlessaf/agents/executor/retry"
	"chainguard.dev/driftlessaf/agents/promptbuilder"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

type errCapRequest struct{}

func (errCapRequest) Bind(p *promptbuilder.Prompt) (*promptbuilder.Prompt, error) {
	return p, nil
}

type errCapResponse struct {
	Answer string `json:"answer"`
}

type recordingTracer struct {
	traces []*agenttrace.Trace[errCapResponse]
}

func (r *recordingTracer) NewTrace(ctx context.Context, prompt string, opts ...agenttrace.StartTraceOption) *agenttrace.Trace[errCapResponse] {
	def := agenttrace.NewDefaultTracer[errCapResponse](ctx)
	return def.NewTrace(ctx, prompt, opts...)
}

func (r *recordingTracer) RecordTrace(t *agenttrace.Trace[errCapResponse]) {
	r.traces = append(r.traces, t)
}

func fastRetry(maxRetries int) retry.RetryConfig {
	return retry.RetryConfig{
		MaxRetries:  maxRetries,
		BaseBackoff: time.Microsecond,
		MaxBackoff:  time.Microsecond,
		MaxJitter:   0,
	}
}

// A non-retryable API failure (401) must mark the LLM turn as Failed and
// land the error string in Errors. This is the regression guard for the
// deferred Fail(err) wiring inside executeTurn — the openai executor's
// equivalent of the claude test, since the wiring is duplicated across
// executors and a future contributor refactoring one in isolation could
// break it without noticing the others are tested.
func TestExecutorMarksTurnFailedOnAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithBaseURL(srv.URL),
		option.WithAPIKey("test"),
		option.WithMaxRetries(0),
	)

	prompt, err := promptbuilder.NewPrompt("hello")
	if err != nil {
		t.Fatalf("NewPrompt: %v", err)
	}

	exec, err := openaiexecutor.New[errCapRequest, errCapResponse](
		client,
		prompt,
		openaiexecutor.WithRetryConfig[errCapRequest, errCapResponse](fastRetry(0)),
		openaiexecutor.WithMaxTurns[errCapRequest, errCapResponse](1),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tracer := &recordingTracer{}
	ctx := agenttrace.WithTracer[errCapResponse](t.Context(), tracer)

	if _, execErr := exec.Execute(ctx, errCapRequest{}, nil); execErr == nil {
		t.Fatal("Execute: got nil error, want non-nil from upstream 401")
	}

	if len(tracer.traces) != 1 {
		t.Fatalf("recorded traces: got = %d, want = 1", len(tracer.traces))
	}
	got := tracer.traces[0].Turns
	if len(got) != 1 {
		t.Fatalf("trace.Turns: got = %d, want = 1", len(got))
	}
	if !got[0].Failed {
		t.Errorf("trace.Turns[0].Failed: got = false, want = true (executor wiring did not call Fail)")
	}
	if len(got[0].Errors) == 0 {
		t.Errorf("trace.Turns[0].Errors: got = empty, want = non-empty")
	}
}

// Retryable upstream failures that exhaust retries must surface every
// transient attempt in Errors via the OnAttemptError callback wired into
// the per-turn retry config, plus the final wrapped err via Fail.
func TestExecutorRecordsTransientErrorsViaRetryCallback(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream 503","type":"server_error"}}`))
	}))
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithBaseURL(srv.URL),
		option.WithAPIKey("test"),
		option.WithMaxRetries(0),
	)

	prompt, err := promptbuilder.NewPrompt("hello")
	if err != nil {
		t.Fatalf("NewPrompt: %v", err)
	}

	const maxRetries = 2
	exec, err := openaiexecutor.New[errCapRequest, errCapResponse](
		client,
		prompt,
		openaiexecutor.WithRetryConfig[errCapRequest, errCapResponse](fastRetry(maxRetries)),
		openaiexecutor.WithMaxTurns[errCapRequest, errCapResponse](1),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tracer := &recordingTracer{}
	ctx := agenttrace.WithTracer[errCapResponse](t.Context(), tracer)

	if _, execErr := exec.Execute(ctx, errCapRequest{}, nil); execErr == nil {
		t.Fatal("Execute: got nil error, want non-nil after retry exhaustion")
	}

	if got, want := attempts.Load(), int32(maxRetries+1); got != want {
		t.Fatalf("HTTP attempts: got = %d, want = %d", got, want)
	}

	if len(tracer.traces) != 1 || len(tracer.traces[0].Turns) != 1 {
		t.Fatalf("expected exactly one trace with one turn, got %d traces", len(tracer.traces))
	}
	got := tracer.traces[0].Turns[0]
	if !got.Failed {
		t.Error("trace.Turns[0].Failed: got = false, want = true")
	}
	// 2 transients via OnAttemptError + 1 terminal via Fail = 3.
	if got, want := len(got.Errors), 3; got < want {
		t.Errorf("trace.Turns[0].Errors length: got = %d, want >= %d", got, want)
	}
}
