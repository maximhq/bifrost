package bifrost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
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

	// Trace-lifecycle counters, used by the abandoned-request tests.
	deferred  atomic.Int64
	cleared   atomic.Int64
	completed atomic.Int64
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

func (r *recordingTracer) DeferTraceCompletion(_ string)         { r.deferred.Add(1) }
func (r *recordingTracer) ClearTraceCompletionDeferral(_ string) { r.cleared.Add(1) }
func (r *recordingTracer) CompleteAndFlushTrace(_ string)        { r.completed.Add(1) }

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

// An MCP agent loop re-enters handleRequest once per turn on the same context. Every turn's
// provider wait must stay in upstream; dropping an earlier turn reports that model time as
// Bifrost overhead on the response, the context and the root span the log row is built from.
func TestOverheadAccumulatesAcrossAgentLoopTurns(t *testing.T) {
	const turnDelay = 200 * time.Millisecond
	server, models := agentLoopServer(t, turnDelay)
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "openai-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newAgentLoopClient(t, account)
	tracer := newRecordingTracer()
	client.SetTracer(tracer)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	defer ctx.Cancel()
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-overhead-test")

	start := time.Now()
	resp, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    testChatModel,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("echo hi")}},
		},
	})
	total := time.Since(start)
	if bifrostErr != nil {
		t.Fatalf("request failed: %s", bifrostErr.Error.Message)
	}
	if got := len(models()); got != 2 {
		t.Fatalf("provider served %d turns, want 2", got)
	}
	bothTurns := 2 * turnDelay

	upstream, ok := schemas.GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("no upstream accumulator on the context after the agent loop")
	}
	if upstream < bothTurns {
		t.Errorf("context upstream = %v, want >= %v (both turns' provider time)", upstream, bothTurns)
	}

	if body := resp.ExtraFields.UpstreamLatency; body == nil {
		t.Error("ExtraFields.UpstreamLatency not populated on the final response")
	} else if *body < bothTurns.Milliseconds() {
		t.Errorf("ExtraFields.UpstreamLatency = %dms, want >= %dms", *body, bothTurns.Milliseconds())
	}
	if overhead := resp.ExtraFields.OverheadLatency; overhead == nil {
		t.Error("ExtraFields.OverheadLatency not populated on the final response")
	} else if *overhead >= turnDelay.Milliseconds() {
		t.Errorf("ExtraFields.OverheadLatency = %dms, want < %dms: an earlier turn's provider time is reported as overhead", *overhead, turnDelay.Milliseconds())
	}

	raw, ok := tracer.get(schemas.AttrBifrostUpstreamDurationMs)
	if !ok {
		t.Fatalf("%s was never stamped on the root span", schemas.AttrBifrostUpstreamDurationMs)
	}
	if ms, _ := raw.(float64); ms < float64(bothTurns.Milliseconds()) {
		t.Errorf("root span upstream = %vms, want >= %dms (log rows derive overhead from it)", ms, bothTurns.Milliseconds())
	}

	t.Logf("total=%v upstream=%v", total, upstream)
}

// An agent turn runs its auto-executed tool calls in parallel. Their waits overlap, so
// upstream must count the union, not the sum, or it exceeds the request's wall time.
func TestOverheadParallelToolCallsCountedOnce(t *testing.T) {
	const toolDelay = 300 * time.Millisecond
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if turns.Add(1) > 1 {
			fmt.Fprint(w, `{"id":"chatcmpl-2","object":"chat.completion","created":1,"model":"m",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[`+
			`{"id":"call-1","type":"function","function":{"name":"bifrostInternal-slow","arguments":"{}"}},`+
			`{"id":"call-2","type":"function","function":{"name":"bifrostInternal-slow","arguments":"{}"}}]},"finish_reason":"tool_calls"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "openai-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newAgentLoopClient(t, account)
	slow := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
		Name:       "slow",
		Parameters: &schemas.ToolFunctionParameters{Type: "object"},
	}}
	if err := client.RegisterMCPTool("slow", "Waits, then answers", func(any) (string, error) {
		time.Sleep(toolDelay)
		return "done", nil
	}, slow); err != nil {
		t.Fatalf("RegisterMCPTool failed: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	defer ctx.Cancel()
	start := time.Now()
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    testChatModel,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("run slow twice")}},
		},
	})
	total := time.Since(start)
	if bifrostErr != nil {
		t.Fatalf("request failed: %s", bifrostErr.Error.Message)
	}
	if got := turns.Load(); got != 2 {
		t.Fatalf("provider served %d turns, want 2", got)
	}
	upstream, ok := schemas.GetUpstreamLatency(ctx)
	if !ok {
		t.Fatal("no upstream accumulator on the context after the agent loop")
	}
	if upstream > total {
		t.Errorf("upstream = %v exceeds the request's wall time %v: parallel tool waits were summed", upstream, total)
	}
	if upstream < toolDelay {
		t.Errorf("upstream = %v, want >= %v (the parallel tool wait)", upstream, toolDelay)
	}
	t.Logf("total=%v upstream=%v", total, upstream)
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

// attemptWindowRecorder is an LLM plugin that keeps the attempt window each PostLLMHook sees,
// which is what the telemetry plugin reads to observe provider latency and queue wait.
type attemptWindowRecorder struct {
	mu      sync.Mutex
	windows []schemas.AttemptWindow
}

func (r *attemptWindowRecorder) GetName() string { return "attempt-window-recorder" }
func (r *attemptWindowRecorder) Cleanup() error  { return nil }

func (r *attemptWindowRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (r *attemptWindowRecorder) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

func (r *attemptWindowRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if IsFinalChunk(ctx) || !isStreamRequest(resp, bifrostErr) {
		w, _ := schemas.GetAttemptWindow(ctx)
		r.mu.Lock()
		r.windows = append(r.windows, w)
		r.mu.Unlock()
	}
	return resp, bifrostErr, nil
}

func isStreamRequest(resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) bool {
	rt, _, _, _ := GetResponseFields(resp, bifrostErr)
	return IsStreamRequestType(rt)
}

func (r *attemptWindowRecorder) snapshot() []schemas.AttemptWindow {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]schemas.AttemptWindow(nil), r.windows...)
}

// One worker and one queue slot: a request behind a busy worker waits in the queue, and a
// request behind a full queue waits in the blocking send. Both must show up as queue wait, and
// neither may show up as upstream.
func TestQueueWaitIsSeparateFromUpstreamLatency(t *testing.T) {
	const providerDelay = 300 * time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(providerDelay)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatCompletionBody)
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "openai-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	recorder := &attemptWindowRecorder{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{recorder},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer client.Shutdown()

	// Three at once: the worker takes the first, the second fills the slot, the third blocks
	// in the send until the worker frees the slot.
	const n = 3
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
			defer ctx.Cancel()
			if _, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest()); bifrostErr != nil {
				t.Errorf("request %d failed: %s", i, bifrostErr.Error.Message)
			}
		}()
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()

	windows := recorder.snapshot()
	if len(windows) != n {
		t.Fatalf("recorded %d attempt windows, want %d", len(windows), n)
	}
	var waits []time.Duration
	for i, w := range windows {
		if up := w.Upstream(); up < providerDelay || up > providerDelay*2 {
			t.Errorf("attempt %d upstream = %v, want ~%v: queue wait must not be counted as provider time", i, up, providerDelay)
		}
		waits = append(waits, w.QueueWait)
	}
	slices.Sort(waits)
	if waits[0] > providerDelay/2 {
		t.Errorf("shortest queue wait = %v, want ~0 for the request a worker took at once", waits[0])
	}
	if waits[1] < providerDelay/2 {
		t.Errorf("middle queue wait = %v, want >= %v for the request that sat in the slot", waits[1], providerDelay/2)
	}
	if waits[2] < providerDelay {
		t.Errorf("longest queue wait = %v, want >= %v for the request blocked in the send on a full queue", waits[2], providerDelay)
	}
	t.Logf("queue waits=%v", waits)
}

// Retries run inside one attempt, so the attempt's upstream is the sum of its calls and its
// queue wait is paid once: the worker does not dequeue again for a retry.
func TestQueueWaitIsPaidOncePerAttemptAcrossRetries(t *testing.T) {
	const providerDelay = 80 * time.Millisecond
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(providerDelay)
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"transient","type":"server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatCompletionBody)
	}))
	defer server.Close()

	recorder := &attemptWindowRecorder{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    mockAccountFor(server.URL),
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{recorder},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest()); bifrostErr != nil {
		t.Fatalf("request failed after retries: %s", bifrostErr.Error.Message)
	}
	windows := recorder.snapshot()
	if len(windows) != 1 {
		t.Fatalf("recorded %d attempt windows, want 1: retries must not re-run hooks", len(windows))
	}
	w := windows[0]
	if w.Upstream() < 3*providerDelay {
		t.Errorf("upstream = %v, want >= %v (three calls)", w.Upstream(), 3*providerDelay)
	}
	if w.Upstream() > w.End.Sub(w.Start) {
		t.Errorf("upstream = %v exceeds the attempt's own window %v", w.Upstream(), w.End.Sub(w.Start))
	}
	if w.QueueWait > providerDelay {
		t.Errorf("queue wait = %v on an idle queue, want ~0: backoff and retried calls are not queue time", w.QueueWait)
	}
}

// A fallback re-enters the queue, so it carries its own queue wait and its own upstream, and the
// primary's provider time is not repeated in it.
func TestFallbackAttemptCarriesItsOwnQueueWaitAndUpstream(t *testing.T) {
	const primaryDelay, fallbackDelay = 90 * time.Millisecond, 40 * time.Millisecond
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(primaryDelay)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"down","type":"server_error"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(fallbackDelay)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatCompletionBody)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Groq, 1, 1, fallback.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Groq].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100}})
	account.SetKeysForProvider(schemas.Groq, []schemas.Key{{ID: "fallback-key", Value: *schemas.NewSecretVar("sk-fallback"), Models: schemas.WhiteList{"*"}, Weight: 100}})
	recorder := &attemptWindowRecorder{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{recorder},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer client.Shutdown()

	req := chatRequest()
	req.Fallbacks = []schemas.Fallback{{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := client.ChatCompletionRequest(ctx, req); bifrostErr != nil {
		t.Fatalf("fallback request failed: %s", bifrostErr.Error.Message)
	}

	windows := recorder.snapshot()
	if len(windows) != 2 {
		t.Fatalf("recorded %d attempt windows, want 2 (primary, fallback)", len(windows))
	}
	if got := windows[0].Upstream(); got < primaryDelay || got > primaryDelay+fallbackDelay {
		t.Errorf("primary upstream = %v, want ~%v", got, primaryDelay)
	}
	if got := windows[1].Upstream(); got < fallbackDelay || got >= primaryDelay+fallbackDelay {
		t.Errorf("fallback upstream = %v, want ~%v and not the request-wide %v", got, fallbackDelay, primaryDelay+fallbackDelay)
	}
	for i, w := range windows {
		if w.QueueWait <= 0 {
			t.Errorf("attempt %d queue wait = %v, want > 0: every attempt that reaches a worker records its wait", i, w.QueueWait)
		}
	}
}

// A plugin that answers from its own cache never reaches a worker, so the attempt has neither
// provider time nor queue wait.
func TestShortCircuitAttemptHasNoQueueWaitOrUpstream(t *testing.T) {
	client, _, calls := newBifrostWithMockProvider(t, 10*time.Millisecond)
	recorder := &attemptWindowRecorder{}
	// The recorder goes first: post-hooks run only for plugins whose pre-hook ran.
	client.llmPlugins.Store(&[]schemas.LLMPlugin{recorder, &cacheHitPlugin{}})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := client.ChatCompletionRequest(ctx, chatRequest()); bifrostErr != nil {
		t.Fatalf("request failed: %s", bifrostErr.Error.Message)
	}
	if *calls != 0 {
		t.Fatalf("provider called %d times, want 0", *calls)
	}
	windows := recorder.snapshot()
	if len(windows) != 1 {
		t.Fatalf("recorded %d attempt windows, want 1", len(windows))
	}
	if windows[0].QueueWait != 0 || windows[0].Upstream() != 0 {
		t.Errorf("queue wait = %v, upstream = %v, want 0 and 0 for an attempt that never reached a worker", windows[0].QueueWait, windows[0].Upstream())
	}
}

// cacheHitPlugin answers every request from its PreLLMHook, as a cache hit does.
type cacheHitPlugin struct{}

func (*cacheHitPlugin) GetName() string { return "short-circuit" }
func (*cacheHitPlugin) Cleanup() error  { return nil }
func (*cacheHitPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (*cacheHitPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: testChatModel}}}, nil
}
func (*cacheHitPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// A stream's provider time keeps growing until it drains, so the window read at the final chunk
// covers the generation, and the queue wait it carries is the wait before the stream started.
func TestStreamAttemptWindowCoversGenerationAndQueueWait(t *testing.T) {
	const ttfb, perChunk, chunks = 50 * time.Millisecond, 40 * time.Millisecond, 4
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		time.Sleep(ttfb)
		for i := range chunks {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,"+
				"\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tok%d\"}}]}\n\n", testChatModel, i)
			flusher.Flush()
			time.Sleep(perChunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	recorder := &attemptWindowRecorder{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    mockAccountFor(server.URL),
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{recorder},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, chatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream request failed: %s", bifrostErr.Error.Message)
	}
	for range stream {
	}

	windows := recorder.snapshot()
	if len(windows) != 1 {
		t.Fatalf("recorded %d final-chunk windows, want 1", len(windows))
	}
	w := windows[0]
	if w.Upstream() < ttfb+chunks*perChunk/2 {
		t.Errorf("stream upstream = %v, want >= %v: the generation window must be counted", w.Upstream(), ttfb+chunks*perChunk/2)
	}
	if w.QueueWait <= 0 || w.QueueWait >= ttfb {
		t.Errorf("stream queue wait = %v, want a small positive wait that excludes the generation", w.QueueWait)
	}
}
