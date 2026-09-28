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
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
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
