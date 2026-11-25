package openai

// Benchmarks for the OpenAI chat-completions translation layer.
//
// Every request that reaches the gateway on the /v1/chat/completions surface
// walks this path: parse the caller's payload into OpenAIChatRequest, lower it
// into the neutral schema, then raise it back into the wire shape for whichever
// OpenAI-family provider serves it (OpenAI, Azure, Groq, xAI, DeepSeek, ...).
// The per-provider compatibility passes run inside ToOpenAIChatRequest, so a
// regression there is charged to every single request.
//
// Run:
//
//	go test ./core/providers/openai/ -bench ChatRequest -benchmem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// A payload representative of what an SDK sends: a short multi-turn
// conversation, a tool result replayed back, and two tool definitions with
// nested JSON-Schema parameters.
const benchOpenAIChatRequestJSON = `{
	"model": "openai/gpt-4o",
	"messages": [
		{"role": "system", "content": "You are a concise assistant. Answer in one sentence."},
		{"role": "user", "content": "What's the weather in San Francisco and Bengaluru?"},
		{
			"role": "assistant",
			"content": "",
			"tool_calls": [
				{
					"id": "call_9xKmNpQrStUvWxYz01",
					"type": "function",
					"function": {"name": "get_weather", "arguments": "{\"location\":\"San Francisco, CA\",\"unit\":\"celsius\"}"}
				}
			]
		},
		{"role": "tool", "tool_call_id": "call_9xKmNpQrStUvWxYz01", "content": "{\"temp_c\":14.4,\"condition\":\"foggy\"}"},
		{
			"role": "user",
			"content": [
				{"type": "text", "text": "And how does that compare to yesterday?"}
			]
		}
	],
	"temperature": 0.2,
	"top_p": 0.95,
	"max_completion_tokens": 1024,
	"parallel_tool_calls": true,
	"stream": false,
	"user": "user_42",
	"tools": [
		{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get the current weather for a location",
				"parameters": {
					"type": "object",
					"properties": {
						"location": {"type": "string", "description": "City and state, e.g. San Francisco, CA"},
						"unit": {"type": "string", "enum": ["celsius", "fahrenheit"]},
						"days": {"type": "integer", "minimum": 1, "maximum": 14}
					},
					"required": ["location"],
					"additionalProperties": false
				}
			}
		},
		{
			"type": "function",
			"function": {
				"name": "search_docs",
				"description": "Search the internal documentation index",
				"parameters": {
					"type": "object",
					"properties": {
						"query": {"type": "string"},
						"top_k": {"type": "integer", "minimum": 1, "maximum": 50},
						"filters": {
							"type": "object",
							"properties": {
								"product": {"type": "string"},
								"updated_after": {"type": "string"}
							}
						}
					},
					"required": ["query"]
				}
			}
		}
	]
}`

func benchOpenAIChatRequest(b *testing.B) *OpenAIChatRequest {
	b.Helper()
	var req OpenAIChatRequest
	if err := schemas.Unmarshal([]byte(benchOpenAIChatRequestJSON), &req); err != nil {
		b.Fatalf("unmarshal OpenAI chat request: %v", err)
	}
	return &req
}

// BenchmarkOpenAIChatRequestUnmarshal measures parsing the inbound payload,
// including the extra-params capture that keeps unknown client fields.
func BenchmarkOpenAIChatRequestUnmarshal(b *testing.B) {
	data := []byte(benchOpenAIChatRequestJSON)
	b.ReportAllocs()
	for b.Loop() {
		var req OpenAIChatRequest
		if err := schemas.Unmarshal(data, &req); err != nil {
			b.Fatalf("unmarshal OpenAI chat request: %v", err)
		}
	}
}

// BenchmarkOpenAIChatRequestToBifrost measures lowering the provider shape into
// the neutral schema Bifrost routes on.
func BenchmarkOpenAIChatRequestToBifrost(b *testing.B) {
	req := benchOpenAIChatRequest(b)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		if out := req.ToBifrostChatRequest(ctx); out == nil {
			b.Fatal("nil Bifrost request")
		}
	}
}

// BenchmarkToOpenAIChatRequest measures raising the neutral schema back into
// the OpenAI wire shape, capability gating and tool normalization included.
func BenchmarkToOpenAIChatRequest(b *testing.B) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := benchOpenAIChatRequest(b).ToBifrostChatRequest(ctx)
	b.ReportAllocs()
	for b.Loop() {
		if out := ToOpenAIChatRequest(ctx, bifrostReq); out == nil {
			b.Fatal("nil OpenAI request")
		}
	}
}

// BenchmarkToOpenAIChatRequestGroq exercises a provider whose conversion runs
// the compatibility filters (unsupported parameters dropped, assistant
// reasoning renamed) rather than returning early like OpenAI itself.
func BenchmarkToOpenAIChatRequestGroq(b *testing.B) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := benchOpenAIChatRequest(b).ToBifrostChatRequest(ctx)
	bifrostReq.Provider = schemas.Groq
	bifrostReq.Model = "llama-3.3-70b-versatile"
	b.ReportAllocs()
	for b.Loop() {
		if out := ToOpenAIChatRequest(ctx, bifrostReq); out == nil {
			b.Fatal("nil OpenAI request")
		}
	}
}

// BenchmarkToOpenAIChatRequestMistral exercises the Mistral compatibility pass
// with a reasoning effort set and a datasheet effort ladder in place.
func BenchmarkToOpenAIChatRequestMistral(b *testing.B) {
	ladder := &schemas.ModelCapabilities{ReasoningEffortLevels: []string{"low", "high", "max"}}
	schemas.SetCapabilityResolver(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if p == schemas.Mistral && m == "zai-glm-5-3" {
			return ladder
		}
		return nil
	})
	b.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := benchOpenAIChatRequest(b).ToBifrostChatRequest(ctx)
	bifrostReq.Provider = schemas.Mistral
	bifrostReq.Model = "zai-glm-5-3"
	bifrostReq.Params.Reasoning = &schemas.ChatReasoning{Effort: schemas.Ptr("max")}
	b.ReportAllocs()
	for b.Loop() {
		if out := ToOpenAIChatRequest(ctx, bifrostReq); out == nil {
			b.Fatal("nil OpenAI request")
		}
	}
}

// BenchmarkOpenAIChatRequestMarshal measures serializing the outbound payload.
// OpenAIChatRequest has a hand-written marshaller that preserves key order for
// provider-side prompt caching, so this is not plain reflection cost.
func BenchmarkOpenAIChatRequestMarshal(b *testing.B) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	req := ToOpenAIChatRequest(ctx, benchOpenAIChatRequest(b).ToBifrostChatRequest(ctx))
	if req == nil {
		b.Fatal("nil OpenAI request")
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := schemas.Marshal(req)
		if err != nil {
			b.Fatalf("marshal OpenAI chat request: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("empty payload")
		}
	}
}

// BenchmarkOpenAIChatRequestRoundTrip measures the full inbound-to-outbound
// pipeline for a single request: parse, lower, raise, serialize.
func BenchmarkOpenAIChatRequestRoundTrip(b *testing.B) {
	data := []byte(benchOpenAIChatRequestJSON)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		var req OpenAIChatRequest
		if err := schemas.Unmarshal(data, &req); err != nil {
			b.Fatalf("unmarshal OpenAI chat request: %v", err)
		}
		out := ToOpenAIChatRequest(ctx, req.ToBifrostChatRequest(ctx))
		if out == nil {
			b.Fatal("nil OpenAI request")
		}
		payload, err := schemas.Marshal(out)
		if err != nil {
			b.Fatalf("marshal OpenAI chat request: %v", err)
		}
		if len(payload) == 0 {
			b.Fatal("empty payload")
		}
	}
}

func newBenchOpenAIStreamingClient() *fasthttp.Client {
	base := &fasthttp.Client{
		ReadTimeout:         30 * time.Second,
		WriteTimeout:        30 * time.Second,
		MaxConnsPerHost:     64,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  30 * time.Second,
		MaxConnDuration:     300 * time.Second,
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	base = providerUtils.ConfigureDialer(base, true)
	return providerUtils.BuildStreamingClient(base)
}

// BenchmarkHandleOpenAIChatCompletionStreaming measures sequential chat SSE
// streams ending with data: [DONE] over a shared streaming client.
func BenchmarkHandleOpenAIChatCompletionStreaming(b *testing.B) {
	stop := "stop"
	frame := []byte(chatChunk("hello", nil) + chatChunk("", &stop))
	doneFrame := []byte("data: [DONE]\n\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = w.Write(frame)
		flusher.Flush()
		_, _ = w.Write(doneFrame)
		flusher.Flush()
	}))
	b.Cleanup(srv.Close)

	client := newBenchOpenAIStreamingClient()
	req := basicChatRequest()

	b.ReportAllocs()
	for b.Loop() {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		stream, bifrostErr := HandleOpenAIChatCompletionStreaming(
			ctx, client, srv.URL, req, nil, nil, 30,
			false, false, schemas.OpenAI, passthroughPostHook,
			nil, nil, nil, nil, nil, nil, testNoopLogger{}, nil,
		)
		if bifrostErr != nil {
			b.Fatalf("stream setup failed: %v", bifrostErr)
		}
		for chunk := range stream {
			if chunk != nil && chunk.BifrostError != nil {
				b.Fatalf("stream chunk error: %v", chunk.BifrostError)
			}
		}
	}
}

// BenchmarkHandleOpenAITextCompletionStreaming measures sequential text-completion
// SSE streams ending with data: [DONE] over a shared streaming client.
func BenchmarkHandleOpenAITextCompletionStreaming(b *testing.B) {
	frame := []byte(`data: {"id":"cmpl-bench","object":"text_completion","created":1,"model":"repro-model","choices":[{"index":0,"text":"hello","finish_reason":"stop"}]}` + "\n\n")
	doneFrame := []byte("data: [DONE]\n\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = w.Write(frame)
		flusher.Flush()
		_, _ = w.Write(doneFrame)
		flusher.Flush()
	}))
	b.Cleanup(srv.Close)

	client := newBenchOpenAIStreamingClient()
	req := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input:    &schemas.TextCompletionInput{PromptStr: schemas.Ptr("hi")},
	}

	b.ReportAllocs()
	for b.Loop() {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		stream, bifrostErr := HandleOpenAITextCompletionStreaming(
			ctx, client, srv.URL, req, nil, nil, 30,
			false, false, schemas.OpenAI, nil, passthroughPostHook,
			nil, nil, testNoopLogger{}, nil,
		)
		if bifrostErr != nil {
			b.Fatalf("stream setup failed: %v", bifrostErr)
		}
		for chunk := range stream {
			if chunk != nil && chunk.BifrostError != nil {
				b.Fatalf("stream chunk error: %v", chunk.BifrostError)
			}
		}
	}
}
