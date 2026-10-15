package logging

import (
	"context"
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
			SafeHeaders: map[string]string{"content-type": "application/json"},
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
	})
}

func TestMarkPassthroughForParsing(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	chat := &logstore.Log{Object: string(schemas.ChatCompletionRequest), Provider: "openai"}
	markPassthroughForParsing(ctx, chat, true)
	require.Empty(t, chat.PassthroughParseFormat, "typed rows are never flagged")

	off := &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "openai"}
	markPassthroughForParsing(ctx, off, false)
	require.Empty(t, off.PassthroughParseFormat, "rows without stored content are never flagged")

	first := &logstore.Log{Object: string(schemas.PassthroughStreamRequest), Provider: "openai"}
	markPassthroughForParsing(ctx, first, true)
	require.Equal(t, schemas.OpenAI, first.PassthroughParseFormat, "without a stamped base type the row's provider is the format")

	// A fallback attempt runs PostLLMHook with its own stamped base type.
	ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Anthropic)
	fallback := &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "my-claude"}
	markPassthroughForParsing(ctx, fallback, true)
	require.Equal(t, schemas.Anthropic, fallback.PassthroughParseFormat)
}

// TestApplyParsedPassthroughStream covers the streaming row, whose response body is the raw SSE text
// the passthrough accumulator concatenated.
func TestApplyParsedPassthroughStream(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}}
	entry := &logstore.Log{
		ID:                      "req-pt-stream",
		Object:                  string(schemas.PassthroughStreamRequest),
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

// BenchmarkMarkPassthroughForParsing records the request-path cost: a string compare for typed rows
// and passthrough rows without content, plus one context read for a flagged passthrough row.
func BenchmarkMarkPassthroughForParsing(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
	cases := []struct {
		name    string
		entry   *logstore.Log
		content bool
	}{
		{"typed_row", &logstore.Log{Object: string(schemas.ChatCompletionRequest), Provider: "openai"}, true},
		{"passthrough_content_off", &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "openai"}, false},
		{"passthrough_flagged", &logstore.Log{Object: string(schemas.PassthroughRequest), Provider: "openai"}, true},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c.entry.PassthroughParseFormat = ""
				markPassthroughForParsing(ctx, c.entry, c.content)
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
