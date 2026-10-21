package bifrost

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type gatewayPassthroughWorkload struct {
	name                               string
	events, textBytes, escapeEvery     int
	usageEvery, nullFinish, jsonArray  bool
	upstreamDelay, pace, consumerDelay time.Duration
	parallel                           bool
}

func gatewayPassthroughWorkloads() []gatewayPassthroughWorkload {
	return []gatewayPassthroughWorkload{
		{name: "plain", events: 256, textBytes: 32},
		{name: "mixed_usage", events: 256, textBytes: 128, escapeEvery: 4, usageEvery: true},
		{name: "escaped", events: 256, textBytes: 128, escapeEvery: 1, usageEvery: true},
		{name: "null_finish", events: 256, textBytes: 128, usageEvery: true, nullFinish: true},
		{name: "json_array", events: 256, textBytes: 128, escapeEvery: 4, usageEvery: true, jsonArray: true},
		{name: "small", events: 32, textBytes: 32, escapeEvery: 4, usageEvery: true},
		{name: "large", events: 32, textBytes: 8192, escapeEvery: 4, usageEvery: true},
		{name: "paced_2ms", events: 32, textBytes: 128, escapeEvery: 4, usageEvery: true, upstreamDelay: 50 * time.Millisecond, pace: 2 * time.Millisecond},
		{name: "paced_20ms", events: 32, textBytes: 128, escapeEvery: 4, usageEvery: true, upstreamDelay: 50 * time.Millisecond, pace: 20 * time.Millisecond},
		{name: "slow_consumer", events: 128, textBytes: 8192, escapeEvery: 4, usageEvery: true, consumerDelay: time.Millisecond},
		{name: "parallel", events: 256, textBytes: 32, parallel: true},
	}
}

func (workload gatewayPassthroughWorkload) frames(b *testing.B) [][]byte {
	b.Helper()
	frames := make([][]byte, 0, workload.events+1)
	for i := range workload.events {
		text := strings.Repeat("x", workload.textBytes)
		if workload.escapeEvery > 0 && i%workload.escapeEvery == 0 {
			text = "code:\n\"quoted text\" " + text
		}
		encoded, err := schemas.MarshalSorted(text)
		if err != nil {
			b.Fatal(err)
		}
		finish := ""
		if workload.nullFinish {
			finish = `,"finishReason":null`
		}
		payload := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%s}],"role":"model"},"index":0%s}],"modelVersion":"gemini-2.5-flash"`, encoded, finish)
		if workload.usageEvery {
			payload += fmt.Sprintf(`,"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":%d,"totalTokenCount":%d}`, i+1, i+6)
		}
		frames = append(frames, []byte("data: "+payload+"}\n\n"))
	}
	frames = append(frames, []byte(fmt.Sprintf("data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":%d,\"totalTokenCount\":%d}}\n\n", workload.events, workload.events+5)))
	return frames
}

// Includes actual core admission, queueing, key selection, Gemini HTTP calls,
// usage extraction and complete client draining; the upstream is local, not Google.
func BenchmarkGatewayPassthroughStream(b *testing.B) {
	for _, workload := range gatewayPassthroughWorkloads() {
		b.Run(workload.name, func(b *testing.B) {
			frames := workload.frames(b)
			data := bytes.Join(frames, nil)
			query, contentType := "alt=sse", "text/event-stream"
			if workload.jsonArray {
				elements := make([][]byte, len(frames))
				for i, frame := range frames {
					elements[i] = frame[len("data: ") : len(frame)-len("\n\n")]
				}
				data = append(append([]byte("["), bytes.Join(elements, []byte(","))...), ']')
				query, contentType = "alt=json", "application/json"
			}
			requestBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" || r.URL.RawQuery != query || r.Header.Get("x-goog-api-key") != "test-key" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if workload.upstreamDelay > 0 {
					time.Sleep(workload.upstreamDelay)
				}
				w.Header().Set("Content-Type", contentType)
				if workload.pace == 0 {
					_, _ = w.Write(data)
					return
				}
				for i, frame := range frames {
					if i > 0 {
						time.Sleep(workload.pace)
					}
					if _, err := w.Write(frame); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
			}))
			defer upstream.Close()
			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.Gemini, 8, 100, upstream.URL)
			account.SetKeysForProvider(schemas.Gemini, []schemas.Key{{ID: "benchmark-key", Value: *schemas.NewSecretVar("test-key"), Models: schemas.WhiteList{"gemini-2.5-flash"}, Weight: 1}})
			client, err := Init(context.Background(), schemas.BifrostConfig{Account: account, Logger: NewDefaultLogger(schemas.LogLevelError)})
			if err != nil {
				b.Fatal(err)
			}
			defer client.Shutdown()
			run := func() {
				ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
				stream, err := client.PassthroughStream(ctx, schemas.Gemini, &schemas.BifrostPassthroughRequest{
					Model: "gemini-2.5-flash", Method: http.MethodPost,
					Path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent", RawQuery: query, Body: requestBody,
				})
				if err != nil {
					b.Fatalf("PassthroughStream: %v", err)
				}
				got := make([]byte, 0, len(data))
				var usage *schemas.BifrostPassthroughUsage
				for chunk := range stream {
					if chunk.BifrostError != nil || chunk.BifrostPassthroughResponse == nil || chunk.BifrostPassthroughResponse.StatusCode != 200 {
						b.Fatalf("unexpected chunk: %+v", chunk)
					}
					response := chunk.BifrostPassthroughResponse
					got = append(got, response.Body...)
					if response.PassthroughUsage != nil {
						usage = response.PassthroughUsage
					}
					if workload.consumerDelay > 0 && len(response.Body) > 0 {
						time.Sleep(workload.consumerDelay)
					}
				}
				if !bytes.Equal(got, data) || usage == nil || usage.LLMUsage == nil || usage.LLMUsage.TotalTokens != workload.events+5 || ctx.Err() != nil {
					b.Fatalf("bytes %d/%d, usage %v, context error %v", len(got), len(data), usage, ctx.Err())
				}
			}
			run() // Equivalent untimed warm-up, including codec and connection setup.
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			if workload.parallel {
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						run()
					}
				})
			} else {
				for b.Loop() {
					run()
				}
			}
			b.StopTimer()
		})
	}
}

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

// A semantic terminal event must close the stream even when the upstream has
// not ended its HTTP body. The EOF control distinguishes terminal detection
// from a cleanup path that waits to drain the rest of the response.
func TestPassthroughStreamCompletionDoesNotWaitForUpstreamEOF(t *testing.T) {
	const model = "gemini-2.5-flash"
	const body = "data: " + `{"candidates":[{"content":{"parts":[{"text":"hello"}],"role":"model"},"index":0}]}` + "\n\n" +
		"data: " + `{"candidates":[{"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}` + "\n\n"

	for _, upstreamEOF := range []bool{true, false} {
		t.Run(fmt.Sprintf("upstreamEOF=%t", upstreamEOF), func(t *testing.T) {
			releaseUpstream := make(chan struct{})
			release := sync.OnceFunc(func() { close(releaseUpstream) })
			// Always release the mock body before cleanup waits for server/worker
			// shutdown, including when the closure assertion fails.
			defer release()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/"+model+":streamGenerateContent" ||
					r.URL.RawQuery != "alt=sse" || r.Header.Get("x-goog-api-key") != "test-key" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := w.Write([]byte(body)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				if !upstreamEOF {
					<-releaseUpstream
				}
			}))
			t.Cleanup(upstream.Close)

			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.Gemini, 1, 10, upstream.URL)
			account.SetKeysForProvider(schemas.Gemini, []schemas.Key{{
				ID: "terminal-test-key", Value: *schemas.NewSecretVar("test-key"),
				Models: schemas.WhiteList{model}, Weight: 1,
			}})
			client, err := Init(context.Background(), schemas.BifrostConfig{
				Account: account, Logger: NewDefaultLogger(schemas.LogLevelError),
			})
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Cleanup(client.Shutdown)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			t.Cleanup(ctx.Cancel)
			stream, bifrostErr := client.PassthroughStream(ctx, schemas.Gemini, &schemas.BifrostPassthroughRequest{
				Model: model, Method: http.MethodPost,
				Path: "/v1beta/models/" + model + ":streamGenerateContent", RawQuery: "alt=sse",
				Body: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
			})
			if bifrostErr != nil {
				t.Fatalf("PassthroughStream: %v", bifrostErr)
			}

			var forwarded []byte
			finalSeen := false
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for !finalSeen {
				select {
				case chunk, ok := <-stream:
					if !ok {
						t.Fatal("stream closed without terminal usage")
					}
					if chunk == nil || chunk.BifrostError != nil || chunk.BifrostPassthroughResponse == nil {
						t.Fatalf("unexpected chunk: %+v", chunk)
					}
					resp := chunk.BifrostPassthroughResponse
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("unexpected upstream status: %d", resp.StatusCode)
					}
					forwarded = append(forwarded, resp.Body...)
					if usage := resp.PassthroughUsage; usage != nil {
						if usage.LLMUsage == nil || usage.LLMUsage.TotalTokens != 6 {
							t.Fatalf("unexpected terminal usage: %+v", usage)
						}
						finalSeen = true
					}
				case <-timer.C:
					t.Fatal("no terminal usage received")
				}
			}
			if string(forwarded) != body {
				t.Fatalf("forwarded bytes differ: got %q, want %q", forwarded, body)
			}

			timer.Reset(time.Second)
			select {
			case chunk, ok := <-stream:
				if ok {
					t.Fatalf("unexpected chunk after terminal usage: %+v", chunk)
				}
			case <-timer.C:
				t.Fatalf("stream did not close after terminal usage (upstreamEOF=%t, cleanupClaim=%v, exhausted=%v, parked=%v)",
					upstreamEOF, ctx.Value(schemas.BifrostContextKeyConnectionClosed),
					ctx.Value(schemas.BifrostContextKeyStreamBodyExhausted),
					ctx.Value(schemas.BifrostContextKeyStreamParkedAfterFinish))
			}
		})
	}
}

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
