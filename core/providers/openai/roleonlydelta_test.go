package openai

// Regression test for issue #6523: the upstream's opening role-only SSE delta
// ({"delta":{"role":"assistant","content":"","refusal":null}} - the exact
// opening-chunk shape OpenAI's API sends) must be forwarded to the client as
// the first stream chunk. OpenAI-compatible SDKs (e.g. @langchain/openai) key
// their stream-assembly mode off whether the FIRST delta carries "role";
// dropping it silently breaks tool-call and usage mapping downstream: a
// tool-calling turn that works direct-to-model returns zero tool calls through
// the gateway with no error raised anywhere.

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// The exact sequence from the issue report: role-only opener, content delta,
// tool_calls delta, finish, then the usage chunk and [DONE].
func roleOnlyOpenerSSEBody() string {
	head := `data: {"id":"chatcmpl-6523","object":"chat.completion.chunk","created":1,"model":"repro-model",`
	return head + `"choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"finish_reason":null}]}` + "\n\n" +
		head + `"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}` + "\n\n" +
		head + `"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		head + `"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		head + `"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n" +
		"data: [DONE]\n\n"
}

func TestChatStreamForwardsRoleOnlyOpeningDelta(t *testing.T) {
	server := completeSSEServer(t, roleOnlyOpenerSSEBody())
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a well-formed stream")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}

	first := chunks[0].BifrostChatResponse
	if first == nil || len(first.Choices) == 0 {
		t.Fatalf("first chunk carries no chat response/choices: %+v", chunks[0])
	}
	firstChoice := first.Choices[0]
	if firstChoice.ChatStreamResponseChoice == nil || firstChoice.ChatStreamResponseChoice.Delta == nil {
		t.Fatalf("first chunk carries no delta: %+v", firstChoice)
	}
	firstDelta := firstChoice.ChatStreamResponseChoice.Delta
	if firstDelta.Role == nil || *firstDelta.Role != string(schemas.ChatMessageRoleAssistant) {
		t.Fatalf("first emitted chunk must carry delta.role == \"assistant\" (the upstream role-only opener); got role=%v content=%v tool_calls=%d",
			firstDelta.Role, firstDelta.Content, len(firstDelta.ToolCalls))
	}

	// Deltas are forwarded verbatim: the upstream sets role only on the opener,
	// so no later chunk may grow one, and the tool-call delta must survive.
	sawToolCall := false
	for i, chunk := range chunks[1:] {
		resp := chunk.BifrostChatResponse
		if resp == nil || len(resp.Choices) == 0 {
			continue
		}
		c := resp.Choices[0]
		if c.ChatStreamResponseChoice == nil || c.ChatStreamResponseChoice.Delta == nil {
			continue
		}
		d := c.ChatStreamResponseChoice.Delta
		if d.Role != nil {
			t.Errorf("chunk %d must not carry a role (upstream sets it only on the opener), got %q", i+1, *d.Role)
		}
		if len(d.ToolCalls) > 0 {
			sawToolCall = true
		}
	}
	if !sawToolCall {
		t.Error("tool_calls delta lost from the stream")
	}
}

// A refusal-only delta carries payload and must be forwarded too; it fell into
// the same skip branch as the role-only opener.
func TestChatStreamForwardsRefusalOnlyDelta(t *testing.T) {
	head := `data: {"id":"chatcmpl-6523r","object":"chat.completion.chunk","created":1,"model":"repro-model",`
	body := head + `"choices":[{"index":0,"delta":{"role":"assistant","refusal":"I cannot help with that."},"finish_reason":null}]}` + "\n\n" +
		head + `"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	server := completeSSEServer(t, body)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	sawRefusal := false
	for _, chunk := range chunks {
		resp := chunk.BifrostChatResponse
		if resp == nil || len(resp.Choices) == 0 {
			continue
		}
		c := resp.Choices[0]
		if c.ChatStreamResponseChoice != nil && c.ChatStreamResponseChoice.Delta != nil &&
			c.ChatStreamResponseChoice.Delta.Refusal != nil {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Error("refusal delta was dropped from the stream")
	}
}
