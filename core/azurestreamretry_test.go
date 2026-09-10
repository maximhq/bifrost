package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/azure"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestAzureChatUsageCommitsStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	usage := &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{TotalTokens: 10},
		},
	}
	lateError := &schemas.BifrostStreamChunk{
		BifrostError: createBifrostError("stream interrupted", nil, nil, false),
	}
	source := make(chan *schemas.BifrostStreamChunk, 2)
	source <- usage
	source <- lateError
	close(source)

	stream, done, err := providerUtils.CheckStreamPreambleForError(
		ctx, t.Name(), source, azure.IsStreamPreamble,
	)
	if err != nil {
		t.Fatalf("usage must commit the stream before the error: %v", err)
	}
	for _, want := range []*schemas.BifrostStreamChunk{usage, lateError} {
		select {
		case got, ok := <-stream:
			if !ok || got != want {
				t.Fatal("usage and subsequent error must be preserved in order")
			}
		case <-ctx.Done():
			t.Fatal("timed out receiving stream")
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("stream cleanup timed out")
	}
}

func TestAzureMediaStreamPreamble(t *testing.T) {
	t.Run("speech", func(t *testing.T) {
		tests := []struct {
			event string
			want  bool
		}{
			{`{"type":"speech.audio.delta","audio":""}`, true},
			{`{"type":"speech.audio.delta","audio":"AQ=="}`, false},
			{`{"type":"speech.audio.done","audio":""}`, false},
			{`{"type":"unknown","audio":""}`, false},
		}
		for _, tt := range tests {
			var response schemas.BifrostSpeechStreamResponse
			if err := schemas.Unmarshal([]byte(tt.event), &response); err != nil {
				t.Fatal(err)
			}
			chunk := &schemas.BifrostStreamChunk{
				BifrostSpeechStreamResponse: &response,
			}
			if got := azure.IsStreamPreamble(chunk); got != tt.want {
				t.Errorf("%s: got %v, want %v", tt.event, got, tt.want)
			}
		}
	})
	t.Run("images", func(t *testing.T) {
		tests := []struct {
			event string
			want  bool
		}{
			{`{"type":"image_generation.partial_image","b64_json":""}`, true},
			{`{"type":"image_edit.partial_image","b64_json":""}`, true},
			{`{"type":"image_generation.partial_image","b64_json":"AQ=="}`, false},
			{`{"type":"image_edit.partial_image","b64_json":"AQ=="}`, false},
			{`{"type":"image_generation.completed"}`, false},
			{`{"type":"image_edit.completed"}`, false},
			{`{"type":"error"}`, false},
			{`{"type":"unknown"}`, false},
		}
		for _, tt := range tests {
			var response schemas.BifrostImageGenerationStreamResponse
			if err := schemas.Unmarshal([]byte(tt.event), &response); err != nil {
				t.Fatal(err)
			}
			chunk := &schemas.BifrostStreamChunk{
				BifrostImageGenerationStreamResponse: &response,
			}
			if got := azure.IsStreamPreamble(chunk); got != tt.want {
				t.Errorf("%s: got %v, want %v", tt.event, got, tt.want)
			}
		}
	})
}

func TestAzureStreamPreamblePayloadBoundaries(t *testing.T) {
	tests := map[string]*schemas.BifrostStreamChunk{
		"nil":   nil,
		"empty": {},
		"error": {
			BifrostError: &schemas.BifrostError{},
		},
		"speech": {
			BifrostSpeechStreamResponse: &schemas.BifrostSpeechStreamResponse{},
		},
		"transcription": {
			BifrostTranscriptionStreamResponse: &schemas.BifrostTranscriptionStreamResponse{},
		},
		"image": {
			BifrostImageGenerationStreamResponse: &schemas.BifrostImageGenerationStreamResponse{},
		},
		"text and chat": {
			BifrostTextCompletionResponse: &schemas.BifrostTextCompletionResponse{},
			BifrostChatResponse:           &schemas.BifrostChatResponse{},
		},
		"text and responses": {
			BifrostTextCompletionResponse: &schemas.BifrostTextCompletionResponse{},
			BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeCreated,
			},
		},
	}
	for name, chunk := range tests {
		t.Run(name, func(t *testing.T) {
			if azure.IsStreamPreamble(chunk) {
				t.Fatal("unexpected preamble classification")
			}
		})
	}
}

func TestAzureTextStreamPreamble(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"no choices", `{"choices":[]}`, true},
		{"empty choice", `{"choices":[{}]}`, true},
		{"empty text", `{"choices":[{"text":""}]}`, true},
		{"null text", `{"choices":[{"text":null}]}`, true},
		{"text", `{"choices":[{"text":"hello"}]}`, false},
		{"whitespace", `{"choices":[{"text":" "}]}`, false},
		{"finished", `{"choices":[{"finish_reason":"stop"}]}`, false},
		{"filtered", `{"choices":[{"finish_reason":"content_filter"}]}`, false},
		{"empty finish reason", `{"choices":[{"finish_reason":""}]}`, false},
		{"logprobs", `{"choices":[{"logprobs":{}}]}`, false},
		{"chat delta", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"chat message", `{"choices":[{"message":{"role":"assistant"}}]}`, false},
		{"later output", `{"choices":[{"text":""},{"text":"hello"}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response schemas.BifrostTextCompletionResponse
			if err := schemas.Unmarshal([]byte(tt.payload), &response); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			chunk := &schemas.BifrostStreamChunk{
				BifrostTextCompletionResponse: &response,
			}
			if got := azure.IsStreamPreamble(chunk); got != tt.want {
				t.Errorf("IsStreamPreamble = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAzureResponsesRetriesAfterStartupEvents(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	config := createTestConfig(1, time.Millisecond, time.Millisecond)
	logger := NewDefaultLogger(schemas.LogLevelError)
	attempts := 0
	success := &schemas.BifrostStreamChunk{
		BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeCompleted,
		},
	}

	handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		attempts++
		stream := make(chan *schemas.BifrostStreamChunk, 3)
		if attempts == 1 {
			for _, event := range []schemas.ResponsesStreamResponseType{
				schemas.ResponsesStreamResponseTypeCreated,
				schemas.ResponsesStreamResponseTypeInProgress,
			} {
				stream <- &schemas.BifrostStreamChunk{
					BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
						Type: event,
					},
				}
			}
			stream <- &schemas.BifrostStreamChunk{
				BifrostError: createBifrostError("rate limit exceeded", nil, nil, false),
			}
		} else {
			stream <- success
		}
		close(stream)
		return stream, nil
	}

	stream, err := executeRequestWithRetries(
		ctx, config, handler, nil, schemas.ResponsesStreamRequest,
		schemas.Azure, "test-model", nil, logger,
	)
	if err != nil {
		t.Fatalf("expected successful retry, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	select {
	case chunk := <-stream:
		if chunk != success {
			t.Fatal("expected successful attempt; failed preamble must not escape")
		}
	case <-parent.Done():
		t.Fatal("timed out waiting for successful retry")
	}
}

func TestAzureResponsesOutputPreamble(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  bool
	}{
		{
			"empty assistant item",
			`{"type":"response.output_item.added","item":{"type":"message","role":"assistant","status":"in_progress","content":[]}}`,
			true,
		},
		{
			"tool call commits",
			`{"type":"response.output_item.added","item":{"type":"function_call","name":"lookup","arguments":"","call_id":"call_1"}}`,
			false,
		},
		{
			"empty text part",
			`{"type":"response.content_part.added","part":{"type":"output_text","text":"","annotations":[]}}`,
			true,
		},
		{
			"whitespace commits",
			`{"type":"response.content_part.added","part":{"type":"output_text","text":" "}}`,
			false,
		},
		{
			"refusal commits",
			`{"type":"response.content_part.added","part":{"type":"refusal","refusal":"Cannot comply"}}`,
			false,
		},
		{
			"unknown event commits",
			`{"type":"response.future_event"}`,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response schemas.BifrostResponsesStreamResponse
			if err := schemas.Unmarshal([]byte(tt.event), &response); err != nil {
				t.Fatalf("invalid event fixture: %v", err)
			}
			chunk := &schemas.BifrostStreamChunk{
				BifrostResponsesStreamResponse: &response,
			}
			if got := azure.IsStreamPreamble(chunk); got != tt.want {
				t.Errorf("IsStreamPreamble = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAzureChatRetriesAfterStartupEvents(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	config := createTestConfig(1, time.Millisecond, time.Millisecond)
	logger := NewDefaultLogger(schemas.LogLevelError)
	attempts := 0
	success := &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: schemas.Ptr("stop"),
			}},
		},
	}

	handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		attempts++
		stream := make(chan *schemas.BifrostStreamChunk, 3)
		if attempts == 1 {
			stream <- &schemas.BifrostStreamChunk{
				BifrostChatResponse: &schemas.BifrostChatResponse{},
			}
			stream <- &schemas.BifrostStreamChunk{
				BifrostChatResponse: &schemas.BifrostChatResponse{
					Choices: []schemas.BifrostResponseChoice{{
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								Role: schemas.Ptr("assistant"),
							},
						},
					}},
				},
			}
			stream <- &schemas.BifrostStreamChunk{
				BifrostError: createBifrostError("rate limit exceeded", nil, nil, false),
			}
		} else {
			stream <- success
		}
		close(stream)
		return stream, nil
	}

	stream, err := executeRequestWithRetries(
		ctx, config, handler, nil, schemas.ChatCompletionStreamRequest,
		schemas.Azure, "test-model", nil, logger,
	)
	if err != nil {
		t.Fatalf("expected successful retry, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	select {
	case chunk := <-stream:
		if chunk != success {
			t.Fatal("expected successful attempt; failed preamble must not escape")
		}
	case <-parent.Done():
		t.Fatal("timed out waiting for successful retry")
	}
}
