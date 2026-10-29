package openai

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// A stream chunk that fails to JSON-decode used to be logged and skipped, and
// the stream carried on. The truncation guard (#5546) infers a healthy stream
// from a terminal signal ([DONE] or finish_reason), and one arriving either side
// of the hole makes a stream that is missing content look like a clean
// completion, so the caller got a short HTTP 200 response with no error. In
// practice the decode failures are "eof" syntax errors: the body ends mid-SSE-line
// and bufio.Scanner flushes the partial final line as a token.
//
// These tests pin that a dropped chunk terminates the stream with an error on
// each of the three shapes the OpenAI-family handlers use to finish a stream:
// the post-loop guard (chat completions), an early return on a terminal event
// (Responses, Responses retrieve), and an early return on a completed flag
// (image generation).

// partialChatFrame is a "data:" line whose JSON object is cut off mid-value, the
// shape bufio.Scanner hands the handler when the response body ends mid-line.
// It carries no "error" key, so it reaches the decoder rather than the in-band
// provider-error branch ahead of it.
const partialChatFrame = `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,` +
	`"model":"repro-model","choices":[{"index":0,"delta":{"content":"lost"`

// partialUsageFrame is the trailing usage-only frame cut off mid-value: the
// shape seen in practice, where the terminal signal has already been seen and
// the body then ends mid-line.
const partialUsageFrame = `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,` +
	`"model":"repro-model","choices":[],"usage":{"prompt_tokens":11,"completion`

// assertUndecodableChunkError checks the error carries the dedicated
// undecodable-chunk message rather than the truncation message (the connection
// did not end mid-stream here), and the retryable upstream-connection shape that
// keeps executeRequestWithRetries retrying instead of breaking out early.
func assertUndecodableChunkError(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an undecodable-chunk error, got nil")
	}
	if err.IsBifrostError {
		t.Error("undecodable-chunk error must have IsBifrostError=false so the retry loop does not break early")
	}
	if err.StatusCode == nil || *err.StatusCode != 502 {
		t.Errorf("expected StatusCode 502, got %v", err.StatusCode)
	}
	if err.Error == nil {
		t.Fatal("error chunk carried no error field")
	}
	if err.Error.Message == schemas.ErrProviderStreamTruncated {
		t.Fatal("a dropped chunk must not be reported as a truncated stream: a completion marker was received")
	}
	if err.Error.Message != schemas.ErrProviderStreamChunkUndecodable {
		t.Errorf("expected message %q, got %q", schemas.ErrProviderStreamChunkUndecodable, err.Error.Message)
	}
	if err.Error.Type == nil || *err.Error.Type != schemas.ProviderConnectionFailed {
		t.Errorf("expected error type %q, got %v", schemas.ProviderConnectionFailed, err.Error.Type)
	}
}

// lastError returns the last error carried by the collected chunks.
func lastError(chunks []*schemas.BifrostStreamChunk) *schemas.BifrostError {
	for i := len(chunks) - 1; i >= 0; i-- {
		if chunks[i].BifrostError != nil {
			return chunks[i].BifrostError
		}
	}
	return nil
}

// countErrors reports how many collected chunks carried an error.
func countErrors(chunks []*schemas.BifrostStreamChunk) int {
	n := 0
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			n++
		}
	}
	return n
}

// The hole is after the terminal signal: content, finish_reason, then a trailing
// usage frame cut off mid-line. finish_reason alone satisfies the post-loop
// guard, so this is the shape that returned HTTP 200 with content
// missing and no usage to bill.
func TestChatStreamUndecodableChunkAfterFinishReasonFailsStream(t *testing.T) {
	stop := "stop"
	server := completeSSEServer(t,
		chatChunk("hello", nil)+
			chatChunk("", &stop)+
			partialUsageFrame)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if got := countErrors(chunks); got != 1 {
		t.Fatalf("expected exactly one error chunk for one dropped frame, got %d out of %d chunks", got, len(chunks))
	}
	assertUndecodableChunkError(t, lastError(chunks))
}

// The decoder error is the only record of what was wrong with the frame, so it
// must survive into the surfaced error rather than collapsing into the message.
func TestChatStreamUndecodableChunkCarriesDecoderCause(t *testing.T) {
	stop := "stop"
	server := completeSSEServer(t,
		partialChatFrame+"\n\n"+
			chatChunk("", &stop)+
			"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	err := lastError(collectChunks(t, stream))
	if err == nil || err.Error == nil {
		t.Fatal("expected an error chunk")
	}
	if err.Error.Error == nil {
		t.Fatal("expected the decoder error to be carried as the cause")
	}
	cause := err.Error.Error.Error()
	if !strings.Contains(strings.ToLower(cause), "syntax") {
		t.Errorf("expected the decoder's syntax error as the cause, got %q", cause)
	}
	if strings.Contains(cause, "lost") {
		t.Errorf("cause must not carry the chunk excerpt, got %q", cause)
	}
}

// Control: a well-formed stream must behave exactly as before - every delta
// forwarded, the synthesized terminal chunk emitted, and no error at all.
func TestChatStreamWithoutDroppedChunksStaysClean(t *testing.T) {
	stop := "stop"
	server := completeSSEServer(t,
		chatChunk("hello ", nil)+
			chatChunk("world", nil)+
			chatChunk("", &stop)+
			"data: [DONE]\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if got := countErrors(chunks); got != 0 {
		t.Fatalf("a healthy stream must not surface any error, got %d", got)
	}

	var text strings.Builder
	for _, delta := range collectChatDeltas(chunks) {
		if delta.Content != nil {
			text.WriteString(*delta.Content)
		}
	}
	if text.String() != "hello world" {
		t.Errorf("expected every delta forwarded, got %q", text.String())
	}

	final := chunks[len(chunks)-1]
	if final.BifrostChatResponse == nil {
		t.Fatalf("expected a synthesized final chat chunk, got %+v", final)
	}
	if len(final.BifrostChatResponse.Choices) == 0 ||
		final.BifrostChatResponse.Choices[0].FinishReason == nil ||
		*final.BifrostChatResponse.Choices[0].FinishReason != stop {
		t.Errorf("expected the final chunk to carry finish_reason %q, got %+v", stop, final.BifrostChatResponse.Choices)
	}
}

func basicResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

// The Responses loop returns from inside the read loop the moment a terminal
// event arrives, so the post-loop guard is never reached on this path. A drop
// followed by response.completed therefore has to be caught at the terminal
// event itself.
func TestResponsesStreamUndecodableChunkBeforeCompletedFailsStream(t *testing.T) {
	server := completeSSEServer(t,
		`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"partial"}`+"\n\n"+
			`data: {"type":"response.output_text.delta","sequence_number":2,"delta":"lost`+"\n\n"+
			`data: {"type":"response.completed","sequence_number":3,"response":{"id":"r1","object":"response","created_at":1,"model":"repro-model","status":"completed"}}`+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), basicResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if got := countErrors(chunks); got != 1 {
		t.Fatalf("expected exactly one error chunk for one dropped frame, got %d out of %d chunks", got, len(chunks))
	}
	assertUndecodableChunkError(t, lastError(chunks))

	for i, chunk := range chunks {
		if chunk.BifrostResponsesStreamResponse != nil &&
			chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			t.Errorf("chunk %d delivered a completed event for a stream that lost a frame", i)
		}
	}
}

const openAICompletedImageEvent = `{"type":"image_generation.completed","b64_json":"aGk=","partial_image_index":0,"sequence_number":3,` +
	`"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`

// The image-generation loop returns on image_generation.completed, another
// terminal path that never reaches the post-loop guard. A caller keeping only
// the partial images must be told the final one is missing.
func TestImageGenerationStreamUndecodableChunkBeforeCompletedFailsStream(t *testing.T) {
	server := completeSSEServer(t,
		"data: "+openAIPartialImageEvent+"\n\n"+
			`data: {"type":"image_generation.partial_image","b64_json":"aGk=","partial_image_index":1`+"\n\n"+
			"data: "+openAICompletedImageEvent+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input:    &schemas.ImageGenerationInput{Prompt: "a cat"},
	}
	stream, bifrostErr := provider.ImageGenerationStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if got := countErrors(chunks); got != 1 {
		t.Fatalf("expected exactly one error chunk for one dropped frame, got %d out of %d chunks", got, len(chunks))
	}
	assertUndecodableChunkError(t, lastError(chunks))

	for i, chunk := range chunks {
		if chunk.BifrostImageGenerationStreamResponse != nil &&
			chunk.BifrostImageGenerationStreamResponse.Type == schemas.ImageGenerationEventTypeCompleted {
			t.Errorf("chunk %d delivered a completed image event for a stream that lost a frame", i)
		}
	}
}

// ResponsesRetrieveStream replays a stored response and, like the live
// Responses loop, returns from inside the read loop the moment the terminal
// event arrives. Its post-loop guard is therefore unreachable on this path, so
// the in-loop check at the terminal event is the only thing standing between a
// dropped frame and a clean completed event. That in-loop check is the novel
// mechanism of this fix and the part a later refactor is most likely to undo,
// which is why it is pinned here rather than left to code reading.
func TestResponsesRetrieveStreamUndecodableChunkBeforeCompletedFailsStream(t *testing.T) {
	server := completeSSEServer(t,
		`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"partial"}`+"\n\n"+
			`data: {"type":"response.output_text.delta","sequence_number":2,"delta":"lost`+"\n\n"+
			`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp-repro","object":"response","created_at":1,"model":"repro-model","status":"completed"}}`+"\n\n")
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostResponsesRetrieveRequest{
		Provider:   schemas.OpenAI,
		ResponseID: "resp-repro",
	}
	stream, bifrostErr := provider.ResponsesRetrieveStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectChunks(t, stream)
	if got := countErrors(chunks); got != 1 {
		t.Fatalf("expected exactly one error chunk for one dropped frame, got %d out of %d chunks", got, len(chunks))
	}
	assertUndecodableChunkError(t, lastError(chunks))

	for i, chunk := range chunks {
		if chunk.BifrostResponsesStreamResponse != nil &&
			chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			t.Errorf("chunk %d delivered a completed event for a replayed stream that lost a frame", i)
		}
	}
}
