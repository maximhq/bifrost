package openai

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

// Issue #5900: continuation deltas of a streamed tool call legally omit the
// optional id/type/function.name members. Bifrost must not materialize them as
// explicit JSON nulls when re-encoding: strict clients distinguish absent from
// null, and the OpenAI schema marks these members optional, not nullable.
func TestToolCallContinuationDeltaOmitsAbsentMetadata(t *testing.T) {
	// First chunk carries full metadata; continuation carries only arguments,
	// mirroring the issue's DeepSeek capture.
	openerChunk := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}` + "\n\n"
	continuationChunk := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]},"finish_reason":null}]}` + "\n\n"
	finishChunk := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"

	server := completeSSEServer(t, openerChunk+continuationChunk+finishChunk+"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	var continuationJSON string
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected error chunk: %+v", chunk.BifrostError)
		}
		// Marshal exactly as the transport does before writing the SSE frame.
		data, err := sonic.Marshal(chunk)
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}
		s := string(data)
		if strings.Contains(s, `"arguments":"{"`) {
			continuationJSON = s
		}
	}
	if continuationJSON == "" {
		t.Fatal("continuation delta chunk not found in stream output")
	}

	for _, forbidden := range []string{`"id":null`, `"type":null`, `"name":null`} {
		if strings.Contains(continuationJSON, forbidden) {
			t.Errorf("continuation delta materializes %s: %s", forbidden, continuationJSON)
		}
	}
}
