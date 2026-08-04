package openai

import (
	"testing"
)

// Issue #5604: when an upstream's terminal SSE chunk legally carries
// delta.content, finish_reason, and usage in one frame (always the case for
// finish_reason "length", and common whenever the last content token coincides
// with the stop), Bifrost split it into a content frame (finish_reason kept,
// usage null) plus a synthetic empty-delta frame carrying the usage with no
// finish_reason key. The last data frame before [DONE] then read
// finish_reason: null, breaking clients that key on terminal-frame shape.
// These tests reuse the mock SSE harness from streamtruncation_test.go.

// combinedTerminalChunk is the exact frame shape from the issue report:
// content + finish_reason + usage together in a single chunk.
func combinedTerminalChunk(content, finishReason string) string {
	return `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":"` + finishReason + `"}],` +
		`"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}` + "\n\n"
}

// TestChatStreamCombinedTerminalChunkNotSplit asserts the upstream's single
// terminal frame stays a single frame: the last emitted chunk must carry the
// content, the finish_reason, and the usage together, with no trailing
// empty-delta frame after it.
func TestChatStreamCombinedTerminalChunkNotSplit(t *testing.T) {
	server := completeSSEServer(t, chatChunk("one two", nil)+combinedTerminalChunk(" three.", "stop")+"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}
	if len(chunks) != 2 {
		t.Fatalf("expected exactly 2 chunks (content, combined terminal), got %d", len(chunks))
	}

	final := chunks[len(chunks)-1].BifrostChatResponse
	if final == nil {
		t.Fatalf("expected the final chunk to be a chat response, got %+v", chunks[len(chunks)-1])
	}
	if len(final.Choices) == 0 {
		t.Fatal("final chunk has no choices")
	}
	choice := final.Choices[0]
	if choice.FinishReason == nil || *choice.FinishReason != "stop" {
		t.Errorf("final chunk finish_reason = %v, want stop", choice.FinishReason)
	}
	if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil ||
		choice.ChatStreamResponseChoice.Delta.Content == nil || *choice.ChatStreamResponseChoice.Delta.Content != " three." {
		t.Errorf("final chunk lost the terminal content delta: %+v", choice.ChatStreamResponseChoice)
	}
	if final.Usage == nil || final.Usage.TotalTokens != 20 {
		t.Errorf("final chunk usage = %+v, want total_tokens 20 on the same frame", final.Usage)
	}
}

// TestChatStreamNoUsageNotStampedZero asserts that when the upstream withholds
// usage entirely, the synthesized terminal chunk does not fabricate
// total_tokens: 0 — a zero-usage frame makes a healthy completed stream
// byte-indistinguishable from an interrupted one for stream-death detection.
func TestChatStreamNoUsageNotStampedZero(t *testing.T) {
	stop := "stop"
	server := completeSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop)+"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	final := chunks[len(chunks)-1].BifrostChatResponse
	if final == nil {
		t.Fatalf("expected a final chat chunk, got %+v", chunks[len(chunks)-1])
	}
	if len(final.Choices) == 0 || final.Choices[0].FinishReason == nil || *final.Choices[0].FinishReason != "stop" {
		t.Errorf("final chunk must still deliver finish_reason stop, got %+v", final.Choices)
	}
	if final.Usage != nil {
		t.Errorf("upstream sent no usage, but final chunk fabricated %+v", final.Usage)
	}
}

// TestChatStreamSplitUsageFrameStillMerged is the guard for OpenAI's own
// framing: finish_reason on an empty-delta chunk, usage on a separate
// choices-less chunk afterwards. The synthesized terminal chunk must still
// merge both, exactly as before.
func TestChatStreamSplitUsageFrameStillMerged(t *testing.T) {
	stop := "stop"
	usageOnly := `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}` + "\n\n"
	server := completeSSEServer(t, chatChunk("hello", nil)+chatChunk("", &stop)+usageOnly+"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	final := chunks[len(chunks)-1].BifrostChatResponse
	if final == nil {
		t.Fatalf("expected a final chat chunk, got %+v", chunks[len(chunks)-1])
	}
	if len(final.Choices) == 0 || final.Choices[0].FinishReason == nil || *final.Choices[0].FinishReason != "stop" {
		t.Errorf("final chunk finish_reason = %+v, want stop", final.Choices)
	}
	if final.Usage == nil || final.Usage.TotalTokens != 20 {
		t.Errorf("final chunk usage = %+v, want merged total_tokens 20", final.Usage)
	}
}
