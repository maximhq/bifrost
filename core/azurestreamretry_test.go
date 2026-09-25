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
			"safeguards update is not output",
			`{"type":"response.safeguards_update","safeguard_results":[{"classifier":"auto_mode","verdict":"allow"}]}`,
			true,
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

// Anthropic sends safeguards_update before any content. A provider that stalls
// right after it has produced no first token, so the TTFT deadline still cuts
// the attempt off.
func TestTTFTDeadlineSurvivesSafeguardsUpdate(t *testing.T) {
	abort := providerUtils.NewAttemptAbort(100 * time.Millisecond)
	defer abort.Disarm()
	// Bound the wait: if the abort never fires, fail here instead of at the
	// suite timeout.
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx := context.WithValue(parent, schemas.BifrostContextKeyStreamAttemptAbort, abort)
	source := make(chan *schemas.BifrostStreamChunk, 2)
	source <- &schemas.BifrostStreamChunk{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
	}}
	source <- &schemas.BifrostStreamChunk{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
		Type:             schemas.ResponsesStreamResponseTypeSafeguardsUpdate,
		SafeguardResults: []byte(`[{"classifier":"auto_mode","verdict":"allow"}]`),
	}}
	defer close(source)

	stream, _, err := providerUtils.CheckStreamPreambleForError(ctx, t.Name(), source, azure.IsStreamPreamble)
	if stream != nil {
		t.Fatal("safeguards_update committed the stream; the stall after it was never cut off")
	}
	if err == nil || err.Error == nil || err.Error.Code == nil || *err.Error.Code != schemas.FirstTokenTimeoutErrorCode {
		t.Fatalf("expected the TTFT timeout error, got %+v", err)
	}
}

// OpenAI models emit startup events (response.created, in_progress, an empty
// role delta) before an in-stream error on every host that serves them, not
// only Azure. A retryable error must retry; a terminal one (an overload, which
// OpenAI sends with no HTTP status) must return synchronously so the provider
// fallback runs, instead of being forwarded inside an already-committed stream.
func TestOpenAIModelsRetryAfterStartupEvents(t *testing.T) {
	responsesPreamble := func() []*schemas.BifrostStreamChunk {
		return []*schemas.BifrostStreamChunk{
			{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeCreated,
			}},
			{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeInProgress,
			}},
		}
	}
	chatPreamble := func() []*schemas.BifrostStreamChunk {
		return []*schemas.BifrostStreamChunk{{
			BifrostChatResponse: &schemas.BifrostChatResponse{
				Choices: []schemas.BifrostResponseChoice{{
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							Role: schemas.Ptr("assistant"),
						},
					},
				}},
			},
		}}
	}
	requests := []struct {
		name        string
		requestType schemas.RequestType
		preamble    func() []*schemas.BifrostStreamChunk
	}{
		{"responses", schemas.ResponsesStreamRequest, responsesPreamble},
		{"chat", schemas.ChatCompletionStreamRequest, chatPreamble},
	}
	failures := []struct {
		name      string
		err       func() *schemas.BifrostError
		retryable bool
	}{
		{"rate_limit", func() *schemas.BifrostError {
			return createBifrostError("rate limit exceeded", nil, nil, false)
		}, true},
		// Shape of an in-stream OpenAI overload after conversion: no status code.
		{"overloaded", func() *schemas.BifrostError {
			return &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: "The server is overloaded. Please try again later.",
					Type:    schemas.Ptr("server_error"),
					Code:    schemas.Ptr("server_is_overloaded"),
				},
			}
		}, false},
	}
	hosts := []struct {
		provider     schemas.ModelProvider
		baseProvider schemas.ModelProvider // set on ctx as the worker does for custom providers
		model        string
		checkStartup bool
	}{
		{schemas.OpenAI, "", "gpt-4o", true},
		{schemas.Bedrock, "", "openai.gpt-oss-120b-1:0", true},
		{schemas.BedrockMantle, "", "gpt-oss-120b", true},
		{schemas.Vertex, "", "openai/gpt-oss-20b", true},
		{schemas.ModelProvider("astra-openai"), "", "gpt-5", true},
		// OpenAI upstreams whose model ids carry no OpenAI family marker.
		{schemas.OpenAI, "", "preamble-error", true},
		{schemas.ModelProvider("astra"), schemas.OpenAI, "astra-large", true},
		// Non-OpenAI families outside Azure keep first-chunk behavior.
		{schemas.Vertex, "", "gemini-2.5-pro", false},
	}

	for _, host := range hosts {
		for _, req := range requests {
			for _, failure := range failures {
				name := string(host.provider) + "/" + host.model + "/" + req.name + "/" + failure.name
				t.Run(name, func(t *testing.T) {
					parent, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
					ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
					if host.baseProvider != "" {
						ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, host.baseProvider)
					}
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
						stream := make(chan *schemas.BifrostStreamChunk, 4)
						if attempts == 1 {
							for _, chunk := range req.preamble() {
								stream <- chunk
							}
							stream <- &schemas.BifrostStreamChunk{BifrostError: failure.err()}
						} else {
							stream <- success
						}
						close(stream)
						return stream, nil
					}

					stream, err := executeRequestWithRetries(
						ctx, config, handler, nil, req.requestType,
						host.provider, host.model, nil, logger,
					)

					switch {
					case !host.checkStartup:
						// The first startup event commits the stream; the error stays inside it.
						if err != nil || attempts != 1 {
							t.Fatalf("expected first-chunk behavior (stream, 1 attempt), got err=%v attempts=%d", err, attempts)
						}
						for range stream {
						}

					case failure.retryable:
						if err != nil || attempts != 2 {
							t.Fatalf("expected retry to succeed (2 attempts), got err=%v attempts=%d", err, attempts)
						}
						select {
						case chunk := <-stream:
							if chunk != success {
								t.Fatal("expected successful attempt; failed preamble must not escape")
							}
						case <-parent.Done():
							t.Fatal("timed out waiting for successful retry")
						}

					default:
						// Terminal on this provider: must surface synchronously so the
						// caller's fallback chain runs, not ride inside the stream.
						if err == nil {
							for range stream {
							}
							t.Fatal("overload after startup events was committed into the stream; fallback cannot run")
						}
						if attempts != 1 {
							t.Fatalf("expected 1 attempt for a non-retryable error, got %d", attempts)
						}
						if err.AllowFallbacks != nil && !*err.AllowFallbacks {
							t.Fatal("overload must stay fallback-eligible")
						}
					}
				})
			}
		}
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
