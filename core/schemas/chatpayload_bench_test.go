package schemas

// Benchmarks for the JSON payload handling every chat request pays for.
//
// Bifrost sits on the request path of every LLM call, so the cost of turning a
// provider payload into the neutral schema (and back) is charged once per
// request - and once per CHUNK on a stream, where a single response can carry
// several hundred of them. These benchmarks pin that cost:
//
//   - response/chunk unmarshal: what the provider sends us
//   - response/chunk marshal: what we send back to the caller
//   - tool schema round-trip: OrderedMap key-order preservation, which is the
//     most expensive part of a tools-heavy request
//   - message marshal: ChatMessage's hand-written marshaller, which splices
//     embedded tool/assistant fragments textually for every message on the wire
//
// Run:
//
//	go test ./core/schemas/ -bench 'Chat|Tool' -benchmem

import (
	"testing"
)

// A non-streaming chat completion as an OpenAI-family provider returns it.
// OpenAI's response shape IS the neutral shape, so this is the payload the
// gateway parses on every non-streaming request to those providers.
const benchChatResponseJSON = `{
	"id": "chatcmpl-Bq9xK2LmNpQrStUvWxYz",
	"object": "chat.completion",
	"created": 1742914380,
	"model": "gpt-4o-2024-11-20",
	"system_fingerprint": "fp_9b1c2d3e4f",
	"choices": [
		{
			"index": 0,
			"finish_reason": "tool_calls",
			"logprobs": null,
			"message": {
				"role": "assistant",
				"content": "I'll look up the current weather for both cities before answering.",
				"tool_calls": [
					{
						"id": "call_9xKmNpQrStUvWxYz01",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"San Francisco, CA\",\"unit\":\"celsius\"}"
						}
					},
					{
						"id": "call_9xKmNpQrStUvWxYz02",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"Bengaluru, KA\",\"unit\":\"celsius\"}"
						}
					}
				]
			}
		}
	],
	"usage": {
		"prompt_tokens": 1284,
		"completion_tokens": 96,
		"total_tokens": 1380,
		"prompt_tokens_details": {"cached_tokens": 1024},
		"completion_tokens_details": {"reasoning_tokens": 0}
	}
}`

// One streaming delta, the unit of work a streaming response repeats for every
// token it emits.
const benchChatStreamChunkJSON = `{
	"id": "chatcmpl-Bq9xK2LmNpQrStUvWxYz",
	"object": "chat.completion.chunk",
	"created": 1742914380,
	"model": "gpt-4o-2024-11-20",
	"choices": [
		{
			"index": 0,
			"finish_reason": null,
			"logprobs": null,
			"delta": {"content": " incrementally streamed token"}
		}
	]
}`

// A tool schema with nested properties and $defs. Key order matters here (LLMs
// are sensitive to it), which is why these go through OrderedMap rather than a
// plain Go map - and why the round-trip is worth tracking.
const benchToolSchemaJSON = `{
	"type": "object",
	"properties": {
		"location": {"type": "string", "description": "City and state, e.g. San Francisco, CA"},
		"unit": {"type": "string", "enum": ["celsius", "fahrenheit"]},
		"days": {"type": "integer", "minimum": 1, "maximum": 14},
		"include": {
			"type": "array",
			"items": {"type": "string", "enum": ["hourly", "daily", "alerts"]}
		},
		"caller": {"$ref": "#/$defs/caller"}
	},
	"required": ["location"],
	"additionalProperties": false,
	"$defs": {
		"caller": {
			"type": "object",
			"properties": {
				"id": {"type": "string"},
				"tier": {"type": "string", "enum": ["free", "pro", "enterprise"]}
			},
			"required": ["id"]
		}
	}
}`

func BenchmarkChatResponseUnmarshal(b *testing.B) {
	data := []byte(benchChatResponseJSON)
	b.ReportAllocs()
	for b.Loop() {
		var resp BifrostChatResponse
		if err := Unmarshal(data, &resp); err != nil {
			b.Fatalf("unmarshal chat response: %v", err)
		}
	}
}

func BenchmarkChatResponseMarshal(b *testing.B) {
	var resp BifrostChatResponse
	if err := Unmarshal([]byte(benchChatResponseJSON), &resp); err != nil {
		b.Fatalf("unmarshal chat response: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := Marshal(&resp)
		if err != nil {
			b.Fatalf("marshal chat response: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("empty payload")
		}
	}
}

func BenchmarkChatStreamChunkUnmarshal(b *testing.B) {
	data := []byte(benchChatStreamChunkJSON)
	b.ReportAllocs()
	for b.Loop() {
		var chunk BifrostChatResponse
		if err := Unmarshal(data, &chunk); err != nil {
			b.Fatalf("unmarshal stream chunk: %v", err)
		}
	}
}

func BenchmarkChatStreamChunkMarshal(b *testing.B) {
	var chunk BifrostChatResponse
	if err := Unmarshal([]byte(benchChatStreamChunkJSON), &chunk); err != nil {
		b.Fatalf("unmarshal stream chunk: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := Marshal(&chunk)
		if err != nil {
			b.Fatalf("marshal stream chunk: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("empty payload")
		}
	}
}

func BenchmarkToolSchemaUnmarshal(b *testing.B) {
	data := []byte(benchToolSchemaJSON)
	b.ReportAllocs()
	for b.Loop() {
		var params ToolFunctionParameters
		if err := Unmarshal(data, &params); err != nil {
			b.Fatalf("unmarshal tool schema: %v", err)
		}
	}
}

func BenchmarkToolSchemaMarshal(b *testing.B) {
	var params ToolFunctionParameters
	if err := Unmarshal([]byte(benchToolSchemaJSON), &params); err != nil {
		b.Fatalf("unmarshal tool schema: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := Marshal(&params)
		if err != nil {
			b.Fatalf("marshal tool schema: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("empty payload")
		}
	}
}

// BenchmarkChatSchemaNormalized measures the deterministic re-ordering applied
// to tool schemas before they go out to a provider. It runs on every tools
// request and exists to keep provider-side prompt caches hitting.
func BenchmarkToolSchemaNormalized(b *testing.B) {
	var params ToolFunctionParameters
	if err := Unmarshal([]byte(benchToolSchemaJSON), &params); err != nil {
		b.Fatalf("unmarshal tool schema: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if got := params.Normalized(); got == nil {
			b.Fatal("nil normalized params")
		}
	}
}

// benchChatMessages is a short multi-turn conversation with a tool call and its
// result - the shape ChatMessage's hand-written marshaller has to splice.
func benchChatMessages() []ChatMessage {
	return []ChatMessage{
		{
			Role:    ChatMessageRoleSystem,
			Content: &ChatMessageContent{ContentStr: Ptr("You are a concise assistant. Answer in one sentence.")},
		},
		{
			Role:    ChatMessageRoleUser,
			Content: &ChatMessageContent{ContentStr: Ptr("What's the weather in San Francisco and Bengaluru?")},
		},
		{
			Role:    ChatMessageRoleAssistant,
			Content: &ChatMessageContent{ContentStr: Ptr("")},
			ChatAssistantMessage: &ChatAssistantMessage{
				ToolCalls: []ChatAssistantMessageToolCall{
					{
						ID:   Ptr("call_9xKmNpQrStUvWxYz01"),
						Type: Ptr("function"),
						Function: ChatAssistantMessageToolCallFunction{
							Name:      Ptr("get_weather"),
							Arguments: `{"location":"San Francisco, CA","unit":"celsius"}`,
						},
					},
				},
			},
		},
		{
			Role:    ChatMessageRoleTool,
			Content: &ChatMessageContent{ContentStr: Ptr(`{"temp_c":14.4,"condition":"foggy"}`)},
			ChatToolMessage: &ChatToolMessage{
				ToolCallID: Ptr("call_9xKmNpQrStUvWxYz01"),
			},
		},
		{
			Role: ChatMessageRoleUser,
			Content: &ChatMessageContent{ContentBlocks: []ChatContentBlock{
				{Type: ChatContentBlockTypeText, Text: Ptr("And how does that compare to yesterday?")},
			}},
		},
	}
}

func BenchmarkChatMessagesMarshal(b *testing.B) {
	messages := benchChatMessages()
	b.ReportAllocs()
	for b.Loop() {
		out, err := Marshal(messages)
		if err != nil {
			b.Fatalf("marshal messages: %v", err)
		}
		if len(out) == 0 {
			b.Fatal("empty payload")
		}
	}
}

func BenchmarkChatMessagesUnmarshal(b *testing.B) {
	data, err := Marshal(benchChatMessages())
	if err != nil {
		b.Fatalf("marshal messages: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var messages []ChatMessage
		if err := Unmarshal(data, &messages); err != nil {
			b.Fatalf("unmarshal messages: %v", err)
		}
	}
}
