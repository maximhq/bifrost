package logging

import (
	"bytes"
	"context"
	"mime/multipart"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

const (
	ptChatRequest  = `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"weather in Paris?"}],"temperature":0.2,"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	ptChatResponse = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`

	ptAnthropicRequest  = `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"say hi"}]}`
	ptAnthropicResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`

	ptAnthropicStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"h\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"i\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

type passthroughCall struct {
	contentType  string // defaults to application/json
	provider     schemas.ModelProvider
	model        string
	path         string
	requestBody  string
	status       int
	responseBody string
}

// runPassthroughCall drives one non-streaming passthrough call through PreLLMHook and PostLLMHook,
// flushes the writer and returns the stored row. setup runs on the context before PreLLMHook,
// afterPre between the hooks (where governance stamps a key's decision).
func runPassthroughCall(t *testing.T, cfg *Config, requestID string, call passthroughCall, setup, afterPre func(*schemas.BifrostContext)) *logstore.Log {
	t.Helper()
	store := newTestStore(t)
	plugin, err := Init(context.Background(), cfg, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	contentType := call.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	if setup != nil {
		setup(ctx)
	}
	_, _, err = plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.PassthroughRequest,
		PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider:    call.provider,
			Model:       call.model,
			Method:      "POST",
			Path:        call.path,
			Body:        []byte(call.requestBody),
			SafeHeaders: map[string]string{"content-type": contentType},
		},
	})
	require.NoError(t, err)
	if afterPre != nil {
		afterPre(ctx)
	}
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{
		PassthroughResponse: &schemas.BifrostPassthroughResponse{
			StatusCode: call.status,
			Body:       []byte(call.responseBody),
			Path:       call.path,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.PassthroughRequest,
				Provider:    call.provider,
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	row, err := store.FindByID(context.Background(), requestID)
	require.NoError(t, err)
	return row
}

func passthroughParamsMap(t *testing.T, row *logstore.Log) map[string]interface{} {
	t.Helper()
	params, ok := row.ParamsParsed.(map[string]interface{})
	require.True(t, ok, "params should deserialize to a map, got %T", row.ParamsParsed)
	return params
}

var openAIChatCall = passthroughCall{provider: schemas.OpenAI, model: "gpt-4o", path: "/v1/chat/completions", requestBody: ptChatRequest, status: 200, responseBody: ptChatResponse}

func TestPassthroughLogParsesOpenAIChat(t *testing.T) {
	row := runPassthroughCall(t, &Config{}, "req-pt-chat", openAIChatCall, nil, nil)

	// Stored under the inferred type, tagged as passthrough.
	require.Equal(t, string(schemas.ChatCompletionRequest), row.Object)
	require.True(t, row.IsPassthrough)

	require.Len(t, row.InputHistoryParsed, 2)
	require.Equal(t, "weather in Paris?", *row.InputHistoryParsed[1].Content.ContentStr)
	require.Len(t, row.ToolsParsed, 1)
	require.Equal(t, "get_weather", row.ToolsParsed[0].Function.Name)
	require.NotNil(t, row.OutputMessageParsed)
	require.NotNil(t, row.OutputMessageParsed.ChatAssistantMessage)
	require.Len(t, row.OutputMessageParsed.ChatAssistantMessage.ToolCalls, 1)
	require.NotNil(t, row.StopReason)
	require.Equal(t, "tool_calls", *row.StopReason)
	require.Contains(t, row.ToolCallNames, "get_weather")

	params := passthroughParamsMap(t, row)
	require.Equal(t, "/v1/chat/completions", params["path"])
	require.EqualValues(t, 200, params["status_code"])
	require.Equal(t, string(schemas.ChatCompletionRequest), params["parsed_type"])
	require.NotContains(t, params, "base_provider", "a first-party provider needs no wire-format note")
	modelParams, ok := params["model_params"].(map[string]interface{})
	require.True(t, ok, "model_params should be recorded, got %T", params["model_params"])
	require.EqualValues(t, 0.2, modelParams["temperature"])

	// The raw bodies are kept alongside the parsed fields.
	require.Equal(t, ptChatRequest, row.PassthroughRequestBody)
	require.Equal(t, ptChatResponse, row.PassthroughResponseBody)
	require.Contains(t, row.ContentSummary, "weather in Paris?")
}

func TestPassthroughLogParsesAnthropicMessages(t *testing.T) {
	row := runPassthroughCall(t, &Config{}, "req-pt-anthropic", passthroughCall{
		provider: schemas.Anthropic, model: "claude-sonnet-4-5", path: "/v1/messages",
		requestBody: ptAnthropicRequest, status: 200, responseBody: ptAnthropicResponse,
	}, nil, nil)

	require.Len(t, row.ResponsesInputHistoryParsed, 1)
	require.NotEmpty(t, row.ResponsesOutputParsed)
	require.Nil(t, row.OutputMessageParsed)
	require.NotNil(t, row.StopReason)
	require.Equal(t, string(schemas.ResponsesRequest), passthroughParamsMap(t, row)["parsed_type"])
}

// TestPassthroughLogParsesCustomProviderByBaseType pins that a custom provider's row parses with the
// wire format of the provider it wraps (the base type the core worker stamps), while the row keeps
// the custom provider's name.
func TestPassthroughLogParsesCustomProviderByBaseType(t *testing.T) {
	row := runPassthroughCall(t, &Config{}, "req-pt-custom", passthroughCall{
		provider: "my-claude", model: "claude-sonnet-4-5", path: "/v1/messages",
		requestBody: ptAnthropicRequest, status: 200, responseBody: ptAnthropicResponse,
	}, func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Anthropic)
	}, nil)

	require.Equal(t, "my-claude", row.Provider)
	require.Equal(t, string(schemas.ResponsesRequest), row.Object)
	require.True(t, row.IsPassthrough)
	require.NotEmpty(t, row.ResponsesOutputParsed)
	params := passthroughParamsMap(t, row)
	require.Equal(t, string(schemas.Anthropic), params["base_provider"])
	require.Equal(t, string(schemas.ResponsesRequest), params["parsed_type"])
}

// TestPassthroughLogSkipsParsingWithoutContentLogging pins the content gate: when the final policy
// does not store content (static flag, a virtual key that turns it off late, or the per-request
// header), nothing is parsed and no bodies are kept.
func TestPassthroughLogSkipsParsingWithoutContentLogging(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *Config
		setup    func(*schemas.BifrostContext)
		afterPre func(*schemas.BifrostContext)
	}{
		{name: "static flag", cfg: &Config{DisableContentLogging: new(true)}},
		{name: "virtual key stamped after PreLLMHook", cfg: &Config{}, afterPre: func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, true)
		}},
		{name: "per-request header", cfg: &Config{}, setup: func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, true)
			ctx.SetValue(schemas.BifrostContextKeyDisableContentLogging, true)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := runPassthroughCall(t, tt.cfg, "req-pt-off", openAIChatCall, tt.setup, tt.afterPre)
			// The route still types the row: typing reads no content.
			require.Equal(t, string(schemas.ChatCompletionRequest), row.Object)
			require.True(t, row.IsPassthrough)
			require.Empty(t, row.InputHistoryParsed)
			require.Empty(t, row.ResponsesInputHistoryParsed)
			require.Nil(t, row.OutputMessageParsed)
			require.Empty(t, row.ResponsesOutputParsed)
			require.Empty(t, row.ToolsParsed)
			require.Empty(t, row.PassthroughRequestBody)
			require.Empty(t, row.PassthroughResponseBody)
			if params, ok := row.ParamsParsed.(map[string]interface{}); ok {
				require.NotContains(t, params, "parsed_type")
				require.NotContains(t, params, "model_params")
			}
		})
	}
}

// TestPassthroughLogParsingIsBestEffort pins that a body that does not decode never affects the row
// beyond leaving it unparsed: it is still written, with its status and raw bodies, and whatever side
// did decode is kept.
func TestPassthroughLogParsingIsBestEffort(t *testing.T) {
	t.Run("garbage response keeps parsed input", func(t *testing.T) {
		call := openAIChatCall
		call.responseBody = `{"choices": [`
		row := runPassthroughCall(t, &Config{}, "req-pt-garbage", call, nil, nil)
		require.Equal(t, logStatusSuccess, row.Status)
		require.Len(t, row.InputHistoryParsed, 2)
		require.Nil(t, row.OutputMessageParsed)
		require.Equal(t, `{"choices": [`, row.PassthroughResponseBody)
	})
	t.Run("garbage on both sides leaves the row raw", func(t *testing.T) {
		call := openAIChatCall
		call.requestBody = `{"messages": 1}`
		call.responseBody = `not json`
		row := runPassthroughCall(t, &Config{}, "req-pt-raw", call, nil, nil)
		require.Equal(t, logStatusSuccess, row.Status)
		require.Empty(t, row.InputHistoryParsed)
		require.Nil(t, row.OutputMessageParsed)
		require.Equal(t, `{"messages": 1}`, row.PassthroughRequestBody)
		require.Equal(t, `not json`, row.PassthroughResponseBody)
		require.NotContains(t, passthroughParamsMap(t, row), "parsed_type")
	})
	t.Run("upstream error parses only the request", func(t *testing.T) {
		call := openAIChatCall
		call.status = 400
		call.responseBody = `{"error":{"message":"bad request"}}`
		row := runPassthroughCall(t, &Config{}, "req-pt-400", call, nil, nil)
		require.Equal(t, logStatusError, row.Status)
		require.Len(t, row.InputHistoryParsed, 2)
		require.Nil(t, row.OutputMessageParsed)
	})
	t.Run("route with no typed equivalent stays raw", func(t *testing.T) {
		row := runPassthroughCall(t, &Config{}, "req-pt-ft", passthroughCall{
			provider: schemas.OpenAI, path: "/v1/fine-tuning/jobs", requestBody: `{"training_file":"f"}`, status: 200, responseBody: `{"id":"ftjob-1"}`,
		}, nil, nil)
		require.Empty(t, row.InputHistoryParsed)
		require.Equal(t, `{"training_file":"f"}`, row.PassthroughRequestBody)
		require.Equal(t, string(schemas.PassthroughRequest), row.Object)
		require.True(t, row.IsPassthrough)
	})
}

// TestPassthroughLogParsesMediaRoutes pins that non-text passthrough routes fill the same dedicated
// fields a typed request does, through the shared capture and output helpers.
func TestPassthroughLogParsesMediaRoutes(t *testing.T) {
	t.Run("openai embeddings", func(t *testing.T) {
		row := runPassthroughCall(t, &Config{}, "req-pt-embed", passthroughCall{
			provider: schemas.OpenAI, model: "text-embedding-3-small", path: "/v1/embeddings", status: 200,
			requestBody:  `{"model":"text-embedding-3-small","input":"hello"}`,
			responseBody: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1}}`,
		}, nil, nil)
		require.Len(t, row.EmbeddingInputParsed, 1)
		require.Len(t, row.EmbeddingOutputParsed, 1)
		require.Equal(t, string(schemas.EmbeddingRequest), passthroughParamsMap(t, row)["parsed_type"])
	})
	t.Run("openai speech input", func(t *testing.T) {
		row := runPassthroughCall(t, &Config{}, "req-pt-speech", passthroughCall{
			provider: schemas.OpenAI, model: "gpt-4o-mini-tts", path: "/v1/audio/speech", status: 200,
			requestBody:  `{"model":"gpt-4o-mini-tts","input":"Hi there","voice":"alloy"}`,
			responseBody: "\x00\xffID3 binary",
		}, nil, nil)
		require.NotNil(t, row.SpeechInputParsed)
		require.Equal(t, "Hi there", row.SpeechInputParsed.Input)
		require.Equal(t, string(schemas.SpeechRequest), passthroughParamsMap(t, row)["parsed_type"])
	})
	t.Run("openai image generation", func(t *testing.T) {
		row := runPassthroughCall(t, &Config{}, "req-pt-image", passthroughCall{
			provider: schemas.OpenAI, model: "gpt-image-1", path: "/v1/images/generations", status: 200,
			requestBody:  `{"model":"gpt-image-1","prompt":"a red fox"}`,
			responseBody: `{"created":1,"data":[{"b64_json":"aGk="}]}`,
		}, nil, nil)
		require.NotNil(t, row.ImageGenerationInputParsed)
		require.Equal(t, "a red fox", row.ImageGenerationInputParsed.Prompt)
		require.NotNil(t, row.ImageGenerationOutputParsed)
		require.Len(t, row.ImageGenerationOutputParsed.Data, 1)
	})
}

// TestPassthroughLogSummarizesMultipartUploads pins that a multipart passthrough upload is logged as
// its form fields and file sizes (never the file bytes) and parses into the typed transcription row.
func TestPassthroughLogSummarizesMultipartUploads(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "whisper-1"))
	require.NoError(t, w.WriteField("language", "en"))
	audio := bytes.Repeat([]byte{0x00, 0xff, 0x7f}, 2000)
	part, err := w.CreateFormFile("file", "clip.mp3")
	require.NoError(t, err)
	_, err = part.Write(audio)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	row := runPassthroughCall(t, &Config{}, "req-pt-multipart", passthroughCall{
		contentType: w.FormDataContentType(), provider: schemas.OpenAI, model: "whisper-1", path: "/v1/audio/transcriptions",
		requestBody: buf.String(), status: 200, responseBody: `{"text":"hello from the clip"}`,
	}, nil, nil)

	require.NotContains(t, row.PassthroughRequestBody, string(audio[:60]), "file bytes must never be stored")
	require.Contains(t, row.PassthroughRequestBody, `"filename":"clip.mp3"`)
	require.Contains(t, row.PassthroughRequestBody, `"bytes":6000`)
	params := passthroughParamsMap(t, row)
	require.Equal(t, true, params["multipart_summary"])
	require.Equal(t, string(schemas.TranscriptionRequest), params["parsed_type"])
	require.NotNil(t, row.TranscriptionInputParsed)
	require.Equal(t, "clip.mp3", row.TranscriptionInputParsed.Filename)
	require.Empty(t, row.TranscriptionInputParsed.File)
	require.NotNil(t, row.TranscriptionOutputParsed)
	require.Equal(t, "hello from the clip", row.TranscriptionOutputParsed.Text)
}

// TestPassthroughErrorWithoutPendingEntryIsTagged pins that a passthrough call that fails with no
// pending entry (the minimal error row PostLLMHook writes on its own) is still tagged is_passthrough,
// so every passthrough row carries the tag the logs API documents.
func TestPassthroughErrorWithoutPendingEntryIsTagged(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-pt-error-no-pending")
	statusCode := 502
	_, _, err = plugin.PostLLMHook(ctx, nil, &schemas.BifrostError{
		StatusCode: &statusCode,
		Error:      &schemas.ErrorField{Message: "upstream failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.PassthroughRequest,
			Provider:    schemas.OpenAI,
		},
	})
	require.NoError(t, err)
	require.NoError(t, plugin.Cleanup())

	row, err := store.FindByID(context.Background(), "req-pt-error-no-pending")
	require.NoError(t, err)
	require.Equal(t, string(schemas.PassthroughRequest), row.Object, "with no route recorded the row keeps its passthrough type")
	require.True(t, row.IsPassthrough)
	require.Empty(t, row.InputHistoryParsed)
}

func TestTagPassthroughEntry(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	chatRoute := &InitialLogData{PassthroughMethod: "POST", PassthroughPath: "/v1/chat/completions"}

	chat := &logstore.Log{Object: string(schemas.ChatCompletionRequest), Provider: "openai"}
	tagPassthroughEntry(ctx, chat, chatRoute, true)
	require.Empty(t, chat.PassthroughParseFormat, "typed rows are never flagged")
	require.False(t, chat.IsPassthrough)

	off := &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "openai", Model: "gpt-4o"}
	tagPassthroughEntry(ctx, off, chatRoute, false)
	require.Empty(t, off.PassthroughParseFormat, "rows without stored content are never parsed")
	require.True(t, off.IsPassthrough)
	require.Equal(t, string(schemas.ChatCompletionRequest), off.Object, "the route types the row whatever the content policy")

	stream := &logstore.Log{Object: string(schemas.PassthroughStreamRequest), Provider: "openai", Model: "gpt-4o"}
	tagPassthroughEntry(ctx, stream, chatRoute, true)
	require.Equal(t, schemas.OpenAI, stream.PassthroughParseFormat, "without a stamped base type the row's provider is the format")
	require.Equal(t, string(schemas.ChatCompletionStreamRequest), stream.Object)

	// A GET on a generation route (listing stored chat completions) is not that request.
	list := &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "openai", Model: "gpt-4o"}
	tagPassthroughEntry(ctx, list, &InitialLogData{PassthroughMethod: "GET", PassthroughPath: "/v1/chat/completions"}, true)
	require.True(t, list.IsPassthrough)
	require.Equal(t, string(schemas.PassthroughRequest), list.Object)

	unknown := &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "openai"}
	tagPassthroughEntry(ctx, unknown, &InitialLogData{PassthroughMethod: "POST", PassthroughPath: "/v1/fine-tuning/jobs"}, true)
	require.True(t, unknown.IsPassthrough)
	require.Equal(t, string(schemas.PassthroughRequest), unknown.Object, "a route with no typed equivalent stays passthrough")

	// A fallback attempt runs PostLLMHook with its own stamped base type.
	ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Anthropic)
	fallback := &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "my-claude", Model: "claude-sonnet-4-5"}
	tagPassthroughEntry(ctx, fallback, &InitialLogData{PassthroughMethod: "POST", PassthroughPath: "/v1/messages"}, true)
	require.Equal(t, schemas.Anthropic, fallback.PassthroughParseFormat)
	require.Equal(t, string(schemas.ResponsesRequest), fallback.Object)
	require.Equal(t, "my-claude", fallback.Provider)
}

// TestApplyParsedPassthroughStream covers the streaming row, whose response body is the raw SSE text
// the passthrough accumulator concatenated.
func TestApplyParsedPassthroughStream(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}}
	entry := &logstore.Log{
		ID:                      "req-pt-stream",
		Object:                  string(schemas.ResponsesStreamRequest),
		IsPassthrough:           true,
		Stream:                  true,
		Provider:                "anthropic",
		PassthroughParseFormat:  schemas.Anthropic,
		ParamsParsed:            &schemas.PassthroughLogParams{Method: "POST", Path: "/v1/messages", StatusCode: 200, Model: "claude-sonnet-4-5"},
		PassthroughRequestBody:  ptAnthropicRequest,
		PassthroughResponseBody: ptAnthropicStream,
	}
	plugin.applyParsedPassthrough(entry)

	require.Empty(t, entry.PassthroughParseFormat, "the hint is consumed so a retried batch does not parse twice")
	require.Len(t, entry.ResponsesInputHistoryParsed, 1)
	require.NotEmpty(t, entry.ResponsesOutputParsed)
	var text string
	for _, item := range entry.ResponsesOutputParsed {
		if item.Content == nil {
			continue
		}
		for _, block := range item.Content.ContentBlocks {
			if block.Text != nil {
				text += *block.Text
			}
		}
	}
	require.Equal(t, "hi", text)
	require.Equal(t, schemas.ResponsesRequest, entry.ParamsParsed.(*schemas.PassthroughLogParams).ParsedType)
}

func TestApplyParsedPassthroughLeavesUnflaggedRowsAlone(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}}
	entry := &logstore.Log{
		Object:                  string(schemas.PassthroughRequest),
		Provider:                "openai",
		ParamsParsed:            &schemas.PassthroughLogParams{Method: "POST", Path: "/v1/chat/completions", StatusCode: 200},
		PassthroughRequestBody:  ptChatRequest,
		PassthroughResponseBody: ptChatResponse,
	}
	plugin.applyParsedPassthrough(entry)
	require.Empty(t, entry.InputHistoryParsed)
	require.Nil(t, entry.OutputMessageParsed)
}

// BenchmarkTagPassthroughEntry records the request-path cost: a string compare for typed rows, and
// for a passthrough row one context read plus the route inference (suffix checks), and a second
// context read when content is stored.
func BenchmarkTagPassthroughEntry(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
	route := &InitialLogData{PassthroughMethod: "POST", PassthroughPath: "/v1/chat/completions"}
	cases := []struct {
		name    string
		object  string
		content bool
	}{
		{"typed_row", string(schemas.ChatCompletionRequest), true},
		{"passthrough_content_off", string(schemas.PassthroughRequest), false},
		{"passthrough_flagged", string(schemas.PassthroughRequest), true},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			entry := &logstore.Log{Provider: "openai", Model: "gpt-4o"}
			b.ReportAllocs()
			for b.Loop() {
				entry.Object = c.object
				entry.PassthroughParseFormat = ""
				tagPassthroughEntry(ctx, entry, route, c.content)
			}
		})
	}
}

// BenchmarkApplyParsedPassthrough records the writer-goroutine cost per row.
func BenchmarkApplyParsedPassthrough(b *testing.B) {
	plugin := &LoggerPlugin{logger: testLogger{}}
	b.Run("unflagged_row", func(b *testing.B) {
		entry := &logstore.Log{Object: string(schemas.ChatCompletionRequest)}
		b.ReportAllocs()
		for b.Loop() {
			plugin.applyParsedPassthrough(entry)
		}
	})
	b.Run("openai_chat", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			entry := &logstore.Log{
				Object:                  string(schemas.PassthroughRequest),
				Provider:                "openai",
				PassthroughParseFormat:  schemas.OpenAI,
				ParamsParsed:            &schemas.PassthroughLogParams{Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, Model: "gpt-4o"},
				PassthroughRequestBody:  ptChatRequest,
				PassthroughResponseBody: ptChatResponse,
			}
			plugin.applyParsedPassthrough(entry)
		}
	})
}

// TestPassthroughLogKeepsGenericRelayRaw pins the row a generic (non-LLM) relay leaves behind: it
// stays a passthrough row whatever path it was sent to, with its raw JSON bodies stored and no
// typed fields invented from them.
func TestPassthroughLogKeepsGenericRelayRaw(t *testing.T) {
	row := runPassthroughCall(t, &Config{}, "req-pt-generic", passthroughCall{
		provider: "tavily", path: "/search",
		requestBody: `{"query":"bifrost","search_depth":"basic"}`, status: 200, responseBody: `{"results":[{"title":"Bifrost"}]}`,
	}, func(ctx *schemas.BifrostContext) {
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Generic)
	}, nil)

	require.Equal(t, "tavily", row.Provider)
	require.Equal(t, string(schemas.PassthroughRequest), row.Object)
	require.True(t, row.IsPassthrough)
	require.Equal(t, `{"query":"bifrost","search_depth":"basic"}`, row.PassthroughRequestBody)
	require.Equal(t, `{"results":[{"title":"Bifrost"}]}`, row.PassthroughResponseBody)
	require.Empty(t, row.InputHistoryParsed)
	require.Empty(t, row.ResponsesOutputParsed)
	params := passthroughParamsMap(t, row)
	require.Equal(t, "/search", params["path"])
	require.Nil(t, params["parsed_type"])
}
