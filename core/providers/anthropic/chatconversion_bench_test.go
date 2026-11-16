package anthropic

// Benchmarks for the Anthropic Messages translation layer.
//
// This is the most expensive provider translation in the gateway: Anthropic's
// wire format differs structurally from the neutral schema (content blocks
// instead of a content string, tool results as user turns, thinking blocks,
// server tools), so the conversion is real work rather than a field copy. It
// runs once per request on the way out and once per response - or once per
// EVENT on a stream, where the state machine below is the hot loop.
//
// The cross-API benchmark is the shape most Bifrost deployments actually serve:
// an OpenAI-format request from the caller, translated to Anthropic on the way
// to the provider.
//
// Run:
//
//	go test ./core/providers/anthropic/ -bench Anthropic -benchmem

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const benchAnthropicModel = "claude-sonnet-4-5-20250929"

// An OpenAI-shaped request as a caller sends it, with a replayed tool result
// and two tool definitions.
const benchOpenAICompatRequestJSON = `{
	"model": "anthropic/claude-sonnet-4-5-20250929",
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
	"max_completion_tokens": 1024,
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
						"top_k": {"type": "integer", "minimum": 1, "maximum": 50}
					},
					"required": ["query"]
				}
			}
		}
	]
}`

// A Messages response carrying thinking, text and a tool_use block - the three
// block kinds the response converter has to demultiplex into the neutral shape.
const benchAnthropicResponseJSON = `{
	"id": "msg_01XyZaBcDeFgHiJkLmNoPqRs",
	"type": "message",
	"role": "assistant",
	"model": "claude-sonnet-4-5-20250929",
	"stop_reason": "tool_use",
	"stop_sequence": null,
	"content": [
		{
			"type": "thinking",
			"thinking": "The user asked about two cities. I should call the weather tool once per city and compare with yesterday's reading.",
			"signature": "EqoBCkgIARABGAIiQL2ZbW5vcHFy"
		},
		{
			"type": "text",
			"text": "Let me check the current conditions for both cities before comparing them with yesterday."
		},
		{
			"type": "tool_use",
			"id": "toolu_01AbCdEfGhIjKlMnOpQrStUv",
			"name": "get_weather",
			"input": {"location": "Bengaluru, KA", "unit": "celsius", "days": 2}
		}
	],
	"usage": {
		"input_tokens": 1284,
		"output_tokens": 96,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens": 1024
	}
}`

func benchBifrostChatRequest(b *testing.B, ctx *schemas.BifrostContext) *schemas.BifrostChatRequest {
	b.Helper()
	var openAIReq openai.OpenAIChatRequest
	if err := schemas.Unmarshal([]byte(benchOpenAICompatRequestJSON), &openAIReq); err != nil {
		b.Fatalf("unmarshal OpenAI-compatible request: %v", err)
	}
	return openAIReq.ToBifrostChatRequest(ctx)
}

// BenchmarkToAnthropicChatRequest measures the neutral-to-Anthropic request
// conversion: message re-grouping, tool schema lowering, parameter gating.
func BenchmarkToAnthropicChatRequest(b *testing.B) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := benchBifrostChatRequest(b, ctx)
	b.ReportAllocs()
	for b.Loop() {
		out, err := ToAnthropicChatRequest(ctx, bifrostReq)
		if err != nil {
			b.Fatalf("convert to Anthropic request: %v", err)
		}
		if out == nil {
			b.Fatal("nil Anthropic request")
		}
	}
}

// BenchmarkAnthropicRequestCrossAPI measures the whole outbound path a
// caller-facing OpenAI request pays for when Anthropic serves it: parse,
// lower to neutral, raise to Messages, serialize.
func BenchmarkAnthropicRequestCrossAPI(b *testing.B) {
	data := []byte(benchOpenAICompatRequestJSON)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		var openAIReq openai.OpenAIChatRequest
		if err := schemas.Unmarshal(data, &openAIReq); err != nil {
			b.Fatalf("unmarshal OpenAI-compatible request: %v", err)
		}
		anthropicReq, err := ToAnthropicChatRequest(ctx, openAIReq.ToBifrostChatRequest(ctx))
		if err != nil {
			b.Fatalf("convert to Anthropic request: %v", err)
		}
		payload, err := schemas.Marshal(anthropicReq)
		if err != nil {
			b.Fatalf("marshal Anthropic request: %v", err)
		}
		if len(payload) == 0 {
			b.Fatal("empty payload")
		}
	}
}

// BenchmarkAnthropicResponseUnmarshal measures parsing the Messages response.
func BenchmarkAnthropicResponseUnmarshal(b *testing.B) {
	data := []byte(benchAnthropicResponseJSON)
	b.ReportAllocs()
	for b.Loop() {
		var resp AnthropicMessageResponse
		if err := schemas.Unmarshal(data, &resp); err != nil {
			b.Fatalf("unmarshal Anthropic response: %v", err)
		}
	}
}

// BenchmarkAnthropicResponseToBifrost measures demultiplexing the content
// blocks (thinking, text, tool_use) into the neutral response.
func BenchmarkAnthropicResponseToBifrost(b *testing.B) {
	var resp AnthropicMessageResponse
	if err := schemas.Unmarshal([]byte(benchAnthropicResponseJSON), &resp); err != nil {
		b.Fatalf("unmarshal Anthropic response: %v", err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		if out := resp.ToBifrostChatResponse(ctx); out == nil {
			b.Fatal("nil Bifrost response")
		}
	}
}

// benchAnthropicStreamEvents is one complete streaming turn: message_start,
// a text block with deltas, a tool_use block whose arguments stream as JSON
// fragments, then the terminal events.
func benchAnthropicStreamEvents() []string {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_01XyZaBcDeFgHiJkLmNoPqRs","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1284,"output_tokens":1,"cache_read_input_tokens":1024}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	}
	for range 24 {
		events = append(events, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" incrementally streamed token"}}`)
	}
	events = append(events,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01AbCdEfGhIjKlMnOpQrStUv","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Bengaluru, KA\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":96}}`,
		`{"type":"message_stop"}`,
	)
	return events
}

// BenchmarkAnthropicStreamTurnToBifrost measures converting a full streaming
// turn event by event, including the block-index state the converter carries
// across events. This is the per-token cost of every Anthropic stream.
func BenchmarkAnthropicStreamTurnToBifrost(b *testing.B) {
	raw := benchAnthropicStreamEvents()
	events := make([]AnthropicStreamEvent, len(raw))
	for i, payload := range raw {
		if err := schemas.Unmarshal([]byte(payload), &events[i]); err != nil {
			b.Fatalf("unmarshal stream event %d: %v", i, err)
		}
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		state := NewAnthropicStreamState()
		for i := range events {
			_, bifrostErr, _ := events[i].ToBifrostChatCompletionStream(ctx, "", state)
			if bifrostErr != nil {
				b.Fatalf("convert stream event %d: %v", i, bifrostErr)
			}
		}
	}
}

// BenchmarkAnthropicStreamTurnParseAndConvert adds the per-event JSON parse
// that the provider loop pays before the conversion above.
func BenchmarkAnthropicStreamTurnParseAndConvert(b *testing.B) {
	raw := benchAnthropicStreamEvents()
	payloads := make([][]byte, len(raw))
	for i, payload := range raw {
		payloads[i] = []byte(payload)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		state := NewAnthropicStreamState()
		for i, payload := range payloads {
			var event AnthropicStreamEvent
			if err := schemas.Unmarshal(payload, &event); err != nil {
				b.Fatalf("unmarshal stream event %d: %v", i, err)
			}
			_, bifrostErr, _ := event.ToBifrostChatCompletionStream(ctx, "", state)
			if bifrostErr != nil {
				b.Fatalf("convert stream event %d: %v", i, bifrostErr)
			}
		}
	}
}

// BenchmarkToAnthropicChatResponse measures the reverse response conversion,
// used when a non-Anthropic provider serves a caller on the Messages surface.
func BenchmarkToAnthropicChatResponse(b *testing.B) {
	var resp AnthropicMessageResponse
	if err := schemas.Unmarshal([]byte(benchAnthropicResponseJSON), &resp); err != nil {
		b.Fatalf("unmarshal Anthropic response: %v", err)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostResp := resp.ToBifrostChatResponse(ctx)
	if bifrostResp == nil {
		b.Fatal("nil Bifrost response")
	}
	bifrostResp.Model = benchAnthropicModel
	b.ReportAllocs()
	for b.Loop() {
		if out := ToAnthropicChatResponse(bifrostResp); out == nil {
			b.Fatal("nil Anthropic response")
		}
	}
}
