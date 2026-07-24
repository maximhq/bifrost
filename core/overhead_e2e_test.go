package bifrost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// chatCompletionBody is a minimal well-formed OpenAI chat completion response.
const chatCompletionBody = `{
  "id": "chatcmpl-overhead-test",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4o-mini",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
}`

// recordingTracer captures attributes stamped on the root span. Everything not
// overridden falls through to NoOpTracer, so a request runs normally.
type recordingTracer struct {
	*schemas.NoOpTracer
	mu    sync.Mutex
	attrs map[string]any
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{NoOpTracer: &schemas.NoOpTracer{}, attrs: map[string]any{}}
}

// NoOpTracer returns an empty trace ID, which the stamp treats as "no trace".
func (r *recordingTracer) CreateTrace(_ string, _ ...string) string {
	return "trace-overhead-test"
}

// A non-nil handle is required for StampUpstreamLatency to proceed.
func (r *recordingTracer) GetSpanHandleByID(_ string, _ *string) schemas.SpanHandle {
	return "root-span"
}

func (r *recordingTracer) SetAttribute(_ schemas.SpanHandle, key string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attrs[key] = value
}

func (r *recordingTracer) get(key string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.attrs[key]
	return v, ok
}

// newBifrostWithMockProvider wires a real Bifrost against an httptest server
// that stalls for providerDelay before answering.
func newBifrostWithMockProvider(t *testing.T, providerDelay time.Duration) (*Bifrost, *httptest.Server, *int32) {
	t.Helper()

	var calls int32
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(providerDelay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, chatCompletionBody)
	}))
	t.Cleanup(server.Close)

	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: mockAccountFor(server.URL),
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(client.Shutdown)

	return client, server, &calls
}

// mockAccountFor builds an account whose single OpenAI key explicitly supports
// the test model — key selection rejects keys with no matching model.
func mockAccountFor(baseURL string) *MockAccount {
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 5, 100, baseURL)
	account.mu.Lock()
	defer account.mu.Unlock()
	for i := range account.keys[schemas.OpenAI] {
		account.keys[schemas.OpenAI][i].Models = []string{testChatModel}
	}
	return account
}

const testChatModel = "gpt-4o-mini"

func chatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    testChatModel,
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
	}
}

// The whole feature, end to end: a real request through a real Bifrost against a
// provider that takes a known amount of time. The accumulated upstream figure
// must track the provider's delay, and the remainder is Bifrost's own cost.
func TestOverheadEndToEndUnary(t *testing.T) {
	const providerDelay = 200 * time.Millisecond
	client, _, calls := newBifrostWithMockProvider(t, providerDelay)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	start := time.Now()
	resp, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest())
	total := time.Since(start)

	if bifrostErr != nil {
		t.Fatalf("request failed: %v", bifrostErr.Error.Message)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	if *calls != 1 {
		t.Fatalf("provider called %d times, want 1", *calls)
	}

	upstream, ok := schemas.GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("no upstream accumulator on the context after a full request")
	}

	// Upstream must cover the provider's stall...
	if upstream < providerDelay {
		t.Fatalf("upstream = %v, want >= %v (the provider's own delay)", upstream, providerDelay)
	}
	// ...but must not swallow the whole request; that would mean overhead is
	// structurally zero and the metric is worthless.
	if upstream > total {
		t.Fatalf("upstream = %v exceeds total %v", upstream, total)
	}

	overhead, ok := schemas.CalculateOverhead(ctx, total)
	if !ok {
		t.Fatal("overhead not derivable")
	}
	if overhead <= 0 {
		t.Fatalf("overhead = %v, want > 0 — marshalling and pipeline work are never free", overhead)
	}
	// Sanity bound: a mock provider on loopback should not make Bifrost look slow.
	if overhead > total/2 {
		t.Fatalf("overhead = %v is more than half of total %v — instrumentation likely missing a wire segment", overhead, total)
	}

	// The body carrier has to agree with the context, since it is the only one
	// that survives a proxy hop between gateway nodes.
	body := resp.ExtraFields.UpstreamLatency
	if body == nil {
		t.Fatal("ExtraFields.UpstreamLatency not populated on the response")
	}
	if want := upstream.Milliseconds(); *body != want {
		t.Fatalf("ExtraFields.UpstreamLatency = %dms, want %dms (context total)", *body, want)
	}

	t.Logf("total=%v upstream=%v overhead=%v (%.1f%%)",
		total, upstream, overhead, 100*float64(overhead)/float64(total))
}

// Retries must sum, not overwrite. This is the case ExtraFields.Latency gets
// wrong today: it holds only the last attempt, so earlier attempts would be
// misreported as Bifrost overhead.
func TestOverheadEndToEndAccumulatesAcrossRetries(t *testing.T) {
	const providerDelay = 100 * time.Millisecond

	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()

		time.Sleep(providerDelay)
		// Fail the first two attempts with a retriable status.
		if attempt <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"transient","type":"server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, chatCompletionBody)
	}))
	defer server.Close()

	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: mockAccountFor(server.URL),
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest()); bifrostErr != nil {
		t.Fatalf("request failed after retries: %v", bifrostErr.Error.Message)
	}

	mu.Lock()
	attempts := calls
	mu.Unlock()
	if attempts < 3 {
		t.Fatalf("provider called %d times, want 3 (two failures then success)", attempts)
	}

	upstream, ok := schemas.GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("no upstream accumulator")
	}
	// All three attempts must be represented. Anything near a single delay means
	// the counter is being overwritten rather than accumulated.
	if want := time.Duration(attempts) * providerDelay; upstream < want {
		t.Fatalf("upstream = %v, want >= %v (%d attempts x %v)", upstream, want, attempts, providerDelay)
	}

	t.Logf("attempts=%d upstream=%v", attempts, upstream)
}

// The number has to reach the root span, or the trace-based connectors never see it.
func TestOverheadStampedOnRootSpan(t *testing.T) {
	client, _, _ := newBifrostWithMockProvider(t, 150*time.Millisecond)

	// Must go on the instance: core overwrites the context's tracer key with
	// bifrost.getTracer() at the start of every request.
	tracer := newRecordingTracer()
	client.SetTracer(tracer)

	// Stands in for the HTTP transport's tracing middleware, which is what
	// creates the trace and root span on a real request. The pure SDK path has
	// neither, so nothing would be stamped.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-overhead-test")

	if _, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest()); bifrostErr != nil {
		t.Fatalf("request failed: %v", bifrostErr.Error.Message)
	}

	raw, ok := tracer.get(schemas.AttrBifrostUpstreamDurationMs)
	if !ok {
		t.Fatalf("%s was never stamped on the root span", schemas.AttrBifrostUpstreamDurationMs)
	}
	ms, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s is %T, want float64", schemas.AttrBifrostUpstreamDurationMs, raw)
	}
	if ms < 150 {
		t.Fatalf("stamped upstream = %vms, want >= 150ms", ms)
	}

	t.Logf("stamped %s = %.2fms", schemas.AttrBifrostUpstreamDurationMs, ms)
}

// Two requests sharing one process-global context (the nil-ctx SDK path) must not
// inherit each other's totals.
func TestOverheadResetBetweenRequests(t *testing.T) {
	const providerDelay = 120 * time.Millisecond
	client, _, _ := newBifrostWithMockProvider(t, providerDelay)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	for i := range 3 {
		if _, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest()); bifrostErr != nil {
			t.Fatalf("request %d failed: %v", i, bifrostErr.Error.Message)
		}
		upstream, ok := schemas.GetUpstreamLatency(ctx)
		if !ok {
			t.Fatalf("request %d: no accumulator", i)
		}
		// Each request re-zeroes the counter, so this stays near one delay
		// regardless of how many requests came before.
		if upstream > 2*providerDelay {
			t.Fatalf("request %d: upstream = %v, want ~%v — counter leaked across requests",
				i, upstream, providerDelay)
		}
	}
}

// Streaming is where the old measurement was worst: fasthttp returns as soon as
// headers arrive, so the only provider time ever recorded was TTFB and the whole
// token-generation window looked like Bifrost overhead. This asserts the
// generation window is now counted as upstream.
func TestOverheadEndToEndStreaming(t *testing.T) {
	const (
		ttfb       = 100 * time.Millisecond
		perChunk   = 60 * time.Millisecond
		chunkCount = 5
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server cannot flush; SSE simulation impossible")
			return
		}

		// Stall before the first byte of the body.
		time.Sleep(ttfb)
		for i := range chunkCount {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,"+
				"\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tok%d\"}}]}\n\n", testChatModel, i)
			flusher.Flush()
			time.Sleep(perChunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: mockAccountFor(server.URL),
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	start := time.Now()
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, chatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream request failed: %v", bifrostErr.Error.Message)
	}

	received := 0
	for chunk := range stream {
		if chunk != nil {
			received++
		}
	}
	total := time.Since(start)

	if received == 0 {
		t.Fatal("no chunks received")
	}

	upstream, ok := schemas.GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("no upstream accumulator after streaming")
	}

	// The generation window alone is ~chunkCount*perChunk. If only TTFB were
	// measured (the old behaviour) this would sit near 100ms and fail.
	generation := time.Duration(chunkCount) * perChunk
	if upstream < ttfb+generation/2 {
		t.Fatalf("upstream = %v, want >= %v — generation window not counted, "+
			"only TTFB is being measured", upstream, ttfb+generation/2)
	}
	if upstream > total {
		t.Fatalf("upstream = %v exceeds total %v", upstream, total)
	}

	overhead, ok := schemas.CalculateOverhead(ctx, total)
	if !ok {
		t.Fatal("overhead not derivable")
	}
	// The decisive assertion: Bifrost's share of a stream must stay small. Before
	// this work it would have been essentially the entire generation window.
	if overhead > generation/2 {
		t.Fatalf("overhead = %v of total %v — generation time is leaking into overhead", overhead, total)
	}

	t.Logf("chunks=%d total=%v upstream=%v overhead=%v (%.1f%%)",
		received, total, upstream, overhead, 100*float64(overhead)/float64(total))
}
