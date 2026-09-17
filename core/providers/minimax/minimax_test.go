package minimax

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

type testLogger struct{}

func (testLogger) Debug(string, ...interface{})                                    {}
func (testLogger) Info(string, ...interface{})                                     {}
func (testLogger) Warn(string, ...interface{})                                     {}
func (testLogger) Error(string, ...interface{})                                    {}
func (testLogger) Fatal(string, ...interface{})                                    {}
func (testLogger) SetLevel(schemas.LogLevel)                                       {}
func (testLogger) SetOutputType(schemas.LoggerOutputType)                          {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder { return nil }

func speechRequest() *schemas.BifrostSpeechRequest {
	voice := "English_expressive_narrator"
	format := "mp3"
	speed := 1.25
	language := "English"
	return &schemas.BifrostSpeechRequest{
		Provider: schemas.MiniMax,
		Model:    "speech-2.8-turbo",
		Input:    &schemas.SpeechInput{Input: "hello"},
		Params: &schemas.SpeechParameters{
			VoiceConfig:    &schemas.SpeechVoiceInput{Voice: &voice},
			ResponseFormat: format,
			Speed:          &speed,
			LanguageCode:   &language,
			ExtraParams: map[string]interface{}{
				"voice_setting":   map[string]interface{}{"vol": 2.0, "pitch": 1.0},
				"audio_setting":   map[string]interface{}{"sample_rate": 32000.0, "bitrate": 128000.0, "channel": 1.0},
				"subtitle_enable": true,
			},
		},
	}
}

func TestToMiniMaxSpeechRequest(t *testing.T) {
	req, err := ToMiniMaxSpeechRequest(speechRequest(), true)
	require.NoError(t, err)
	require.Equal(t, "speech-2.8-turbo", req.Model)
	require.Equal(t, "hello", req.Text)
	require.True(t, req.Stream)
	require.Equal(t, "English_expressive_narrator", req.VoiceSetting.VoiceID)
	require.Equal(t, 1.25, *req.VoiceSetting.Speed)
	require.Equal(t, 2.0, *req.VoiceSetting.Vol)
	require.Equal(t, 1, *req.VoiceSetting.Pitch)
	require.Equal(t, "mp3", *req.AudioSetting.Format)
	require.Equal(t, 32000, *req.AudioSetting.SampleRate)
	require.Equal(t, "English", *req.LanguageBoost)
	require.True(t, *req.StreamOptions.ExcludeAggregatedAudio)
	require.True(t, *req.SubtitleEnable)
	require.Empty(t, req.ExtraParams)
}

func TestMiniMaxSpeechXKey(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/t2a_v2", r.URL.Path)
		require.Equal(t, "secret", r.Header.Get("x-key"))
		require.Empty(t, r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, sonic.Unmarshal(body, &captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"audio":"494433","subtitle_file":"https://example.com/subtitles.json","status":2},"extra_info":{"usage_characters":5},"trace_id":"trace-1","base_resp":{"status_code":0,"status_msg":"success"}}`))
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		MiniMaxConfig: &schemas.MiniMaxConfig{AuthType: schemas.MiniMaxAuthTypeXKey},
	}, testLogger{})
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("secret")}, speechRequest())
	require.Nil(t, bifrostErr)
	require.Equal(t, []byte("ID3"), response.Audio)
	require.Equal(t, 5, response.Usage.InputChars)
	require.NotNil(t, response.SubtitleFile)
	require.Equal(t, "https://example.com/subtitles.json", *response.SubtitleFile)
	require.Equal(t, "hello", captured["text"])
	require.Equal(t, "hex", captured["output_format"])
}

func TestMiniMaxSpeechBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":1004,"status_msg":"authentication failed"},"trace_id":"trace-2"}`))
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Speech(ctx, schemas.Key{}, speechRequest())
	require.NotNil(t, bifrostErr)
	require.Equal(t, "authentication failed", bifrostErr.Error.Message)
	require.NotNil(t, bifrostErr.StatusCode)
	require.Equal(t, http.StatusUnauthorized, *bifrostErr.StatusCode)
}

func TestMiniMaxSpeechStream(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, sonic.Unmarshal(body, &captured))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"data\":{\"audio\":\"0102\",\"status\":1},\"base_resp\":{\"status_code\":0}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"data\":{\"audio\":\"0304\",\"subtitle_file\":\"https://example.com/stream-subtitles.json\",\"status\":2},\"extra_info\":{\"usage_characters\":5},\"base_resp\":{\"status_code\":0}}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	postHook := func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return response, err
	}
	stream, bifrostErr := provider.SpeechStream(ctx, postHook, nil, schemas.Key{Value: *schemas.NewSecretVar("secret")}, speechRequest())
	require.Nil(t, bifrostErr)

	var audio []byte
	var done *schemas.BifrostSpeechStreamResponse
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostSpeechStreamResponse == nil {
			continue
		}
		if chunk.BifrostSpeechStreamResponse.Type == schemas.SpeechStreamResponseTypeDelta {
			audio = append(audio, chunk.BifrostSpeechStreamResponse.Audio...)
		} else if chunk.BifrostSpeechStreamResponse.Type == schemas.SpeechStreamResponseTypeDone {
			done = chunk.BifrostSpeechStreamResponse
		}
	}
	require.Equal(t, []byte{1, 2, 3, 4}, audio)
	require.NotNil(t, done)
	require.Equal(t, 5, done.Usage.InputChars)
	require.NotNil(t, done.SubtitleFile)
	require.Equal(t, "https://example.com/stream-subtitles.json", *done.SubtitleFile)
	require.Equal(t, true, captured["stream"])
	streamOptions := captured["stream_options"].(map[string]interface{})
	require.Equal(t, true, streamOptions["exclude_aggregated_audio"])
}

func TestMiniMaxListModelsAndCustomProviderName(t *testing.T) {
	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "exchange-minimax",
			BaseProviderType:  schemas.MiniMax,
		},
	}, testLogger{})
	require.NoError(t, err)
	require.Equal(t, schemas.ModelProvider("exchange-minimax"), provider.GetProviderKey())

	response, bifrostErr := provider.ListModels(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		[]schemas.Key{{Models: schemas.WhiteList{"speech-2.8-turbo"}}},
		&schemas.BifrostListModelsRequest{},
	)
	require.Nil(t, bifrostErr)
	require.Len(t, response.Data, 1)
	require.Equal(t, "exchange-minimax/speech-2.8-turbo", response.Data[0].ID)
}

func TestNewMiniMaxProviderRejectsUnknownAuthType(t *testing.T) {
	_, err := NewMiniMaxProvider(&schemas.ProviderConfig{
		MiniMaxConfig: &schemas.MiniMaxConfig{AuthType: schemas.MiniMaxAuthType("unknown")},
	}, testLogger{})
	require.ErrorContains(t, err, "unsupported MiniMax auth_type")
}

func TestMiniMaxBusinessErrorStatusMapping(t *testing.T) {
	tests := []struct {
		code       int
		wantStatus int
	}{
		{code: 1001, wantStatus: http.StatusGatewayTimeout},
		{code: 1002, wantStatus: http.StatusTooManyRequests},
		{code: 1004, wantStatus: http.StatusUnauthorized},
		{code: 2049, wantStatus: http.StatusUnauthorized},
		{code: 1008, wantStatus: http.StatusPaymentRequired},
		{code: 1039, wantStatus: http.StatusTooManyRequests},
		{code: 1041, wantStatus: http.StatusTooManyRequests},
		{code: 2056, wantStatus: http.StatusTooManyRequests},
		{code: 1042, wantStatus: http.StatusBadRequest},
		{code: 2013, wantStatus: http.StatusBadRequest},
		{code: 20132, wantStatus: http.StatusBadRequest},
		{code: 1000, wantStatus: http.StatusBadGateway},
	}
	for _, tc := range tests {
		err := miniMaxBusinessError(&MiniMaxSpeechResponse{
			BaseResp: MiniMaxBaseResponse{StatusCode: tc.code, StatusMsg: "provider error"},
		}, http.StatusOK)
		require.NotNil(t, err)
		require.NotNil(t, err.StatusCode)
		require.Equal(t, tc.wantStatus, *err.StatusCode)
	}
}

func TestMiniMaxSpeechStreamRejectsPrematureEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"data\":{\"audio\":\"0102\",\"status\":1},\"base_resp\":{\"status_code\":0}}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.SpeechStream(ctx, passthroughPostHook, nil, schemas.Key{}, speechRequest())
	require.Nil(t, bifrostErr)

	var gotError *schemas.BifrostError
	var gotDone bool
	for chunk := range stream {
		if chunk.BifrostError != nil {
			gotError = chunk.BifrostError
		}
		if chunk.BifrostSpeechStreamResponse != nil && chunk.BifrostSpeechStreamResponse.Type == schemas.SpeechStreamResponseTypeDone {
			gotDone = true
		}
	}
	require.False(t, gotDone)
	require.NotNil(t, gotError)
	require.Equal(t, http.StatusBadGateway, *gotError.StatusCode)
}

func TestMiniMaxSpeechStreamRejectsEmptyCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"data\":{\"status\":2},\"base_resp\":{\"status_code\":0}}\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.SpeechStream(ctx, passthroughPostHook, nil, schemas.Key{}, speechRequest())
	require.Nil(t, bifrostErr)

	var gotError *schemas.BifrostError
	for chunk := range stream {
		if chunk.BifrostError != nil {
			gotError = chunk.BifrostError
		}
		require.Nil(t, chunk.BifrostSpeechStreamResponse)
	}
	require.NotNil(t, gotError)
	require.Equal(t, http.StatusBadGateway, *gotError.StatusCode)
}

func passthroughPostHook(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return response, err
}

func TestMiniMaxSpeechRejectsIncompleteNonStreamingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"audio":"494433","status":1},"base_resp":{"status_code":0}}`))
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Speech(ctx, schemas.Key{}, speechRequest())
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	require.Equal(t, http.StatusBadGateway, *bifrostErr.StatusCode)
	require.Contains(t, bifrostErr.Error.Message, "incomplete")
}

func miniMaxChatRequest() *schemas.BifrostChatRequest {
	content := "What is the weather in Shanghai?"
	return &schemas.BifrostChatRequest{
		Provider: schemas.MiniMax,
		Model:    "MiniMax-M3",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &content},
		}},
		Params: &schemas.ChatParameters{
			Reasoning:   &schemas.ChatReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortHigh)},
			ExtraParams: map[string]interface{}{"unexpected_field": true},
			Tools: []schemas.ChatTool{{
				Type: "function",
				Function: &schemas.ChatToolFunction{
					Name: "get_weather",
				},
			}},
		},
	}
}

func TestMiniMaxChatCompletion(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, defaultChatPath, r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, sonic.Unmarshal(body, &captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","object":"chat.completion","created":1,"model":"MiniMax-M3","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","reasoning_content":"thinking","reasoning_details":[{"type":"reasoning.text","id":"reasoning-text-1","index":0,"text":"thinking"}],"tool_calls":[{"id":"call_1","type":"function","index":0,"function":{"name":"get_weather","arguments":"{\"location\":\"Shanghai\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},"base_resp":{"status_code":0,"status_msg":""}}`))
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("secret")}, miniMaxChatRequest())
	require.Nil(t, bifrostErr)
	require.Nil(t, ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams))
	require.Len(t, response.Choices, 1)
	require.Len(t, response.Choices[0].Message.ReasoningDetails, 1)
	require.Equal(t, "thinking", *response.Choices[0].Message.ReasoningDetails[0].Text)
	require.Len(t, response.Choices[0].Message.ToolCalls, 1)

	thinking, ok := captured["thinking"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "adaptive", thinking["type"])
	require.Equal(t, true, captured["reasoning_split"])
	require.NotContains(t, captured, "reasoning_effort")
	require.NotContains(t, captured, "unexpected_field", "extra params must remain opt-in")
}

func TestMiniMaxChatCompletionBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":1004,"status_msg":"authentication failed"},"trace_id":"trace-chat"}`))
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, miniMaxChatRequest())
	require.NotNil(t, bifrostErr)
	require.Equal(t, "authentication failed", bifrostErr.Error.Message)
	require.Equal(t, http.StatusUnauthorized, *bifrostErr.StatusCode)
}

func TestMiniMaxChatStreamLeavesCustomGatewayDeltasUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, defaultChatPath, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		frames := []string{
			`{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"MiniMax-M3","choices":[{"index":0,"finish_reason":null,"delta":{"content":"a"}}],"base_resp":{"status_code":0}}`,
			`{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"MiniMax-M3","choices":[{"index":0,"finish_reason":null,"delta":{"content":"apple"}}],"base_resp":{"status_code":0}}`,
			`{"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"MiniMax-M3","choices":[{"index":0,"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4},"base_resp":{"status_code":0}}`,
		}
		for _, frame := range frames {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{}, testLogger{})
	require.NoError(t, err)
	require.True(t, provider.usesCumulativeChatStream(provider.networkConfig.BaseURL+defaultChatPath))
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, server.URL+defaultChatPath)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, schemas.Key{}, miniMaxChatRequest())
	require.Nil(t, bifrostErr)

	var content strings.Builder
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
				content.WriteString(*choice.Delta.Content)
			}
		}
	}
	require.Equal(t, "aapple", content.String())
}

func miniMaxResponsesRequest() *schemas.BifrostResponsesRequest {
	content := "Hello"
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.MiniMax,
		Model:    "MiniMax-M3",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: &content},
		}},
		Params: &schemas.ResponsesParameters{
			Reasoning:   &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortHigh)},
			ExtraParams: map[string]interface{}{"unexpected_field": true},
		},
	}
}

func TestMiniMaxResponses(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, defaultResponsesPath, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, sonic.Unmarshal(body, &captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"model":"MiniMax-M3","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"output_text":"Hello","usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2},"parallel_tool_calls":true,"store":false,"truncation":"disabled","base_resp":{"status_code":0}}`))
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Responses(ctx, schemas.Key{}, miniMaxResponsesRequest())
	require.Nil(t, bifrostErr)
	require.NotNil(t, response.ID)
	require.Equal(t, "resp_1", *response.ID)
	require.Len(t, response.Output, 1)

	reasoning, ok := captured["reasoning"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "high", reasoning["effort"])
	require.NotContains(t, captured, "unexpected_field", "extra params must remain opt-in")
}

func TestMiniMaxResponsesRejectsForcedToolChoice(t *testing.T) {
	request := miniMaxResponsesRequest()
	request.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr(string(schemas.ResponsesToolChoiceTypeRequired))}

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Responses(ctx, schemas.Key{}, request)
	require.NotNil(t, bifrostErr)
	require.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
}

func TestMiniMaxResponsesStream(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, defaultResponsesPath, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, sonic.Unmarshal(body, &captured))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello\"}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"MiniMax-M3\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, schemas.Key{}, miniMaxResponsesRequest())
	require.Nil(t, bifrostErr)

	var sawDelta, sawCompleted bool
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		switch chunk.BifrostResponsesStreamResponse.Type {
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			sawDelta = true
		case schemas.ResponsesStreamResponseTypeCompleted:
			sawCompleted = true
		}
	}
	require.True(t, sawDelta)
	require.True(t, sawCompleted)
	require.Equal(t, true, captured["stream"])
}

func TestMiniMaxListModelsIncludesLanguageCapabilities(t *testing.T) {
	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{}, testLogger{})
	require.NoError(t, err)

	response, bifrostErr := provider.ListModels(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		[]schemas.Key{{Models: schemas.WhiteList{"MiniMax-M3"}}},
		&schemas.BifrostListModelsRequest{},
	)
	require.Nil(t, bifrostErr)
	require.Len(t, response.Data, 1)
	require.Equal(t, "minimax/MiniMax-M3", response.Data[0].ID)
	require.ElementsMatch(t, languageModelMethods, response.Data[0].SupportedMethods)
}

func TestMiniMaxCumulativeStreamScopeUsesResolvedRequestURL(t *testing.T) {
	official, err := NewMiniMaxProvider(&schemas.ProviderConfig{}, testLogger{})
	require.NoError(t, err)
	require.True(t, official.usesCumulativeChatStream("https://api.minimax.io/v1/chat/completions"))
	require.True(t, official.usesCumulativeChatStream("https://api.minimax.cn/v1/chat/completions"))
	require.False(t, official.usesCumulativeChatStream("https://gateway.example.com/v1/chat/completions"), "an absolute URL override must not inherit the configured base URL's stream semantics")

	gateway, err := NewMiniMaxProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "https://gateway.example.com"},
	}, testLogger{})
	require.NoError(t, err)
	require.True(t, gateway.usesCumulativeChatStream("https://api.minimax.io/v1/chat/completions"), "an absolute URL override to the official API must enable cumulative normalization")

	xKeyGateway, err := NewMiniMaxProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "https://gateway.example.com"},
		MiniMaxConfig: &schemas.MiniMaxConfig{AuthType: schemas.MiniMaxAuthTypeXKey},
	}, testLogger{})
	require.NoError(t, err)
	require.False(t, xKeyGateway.usesCumulativeChatStream("https://api.minimax.io/v1/chat/completions"), "x-key compatible gateways use standard delta streams")
}

func TestMiniMaxRejectsLargePayloadMode(t *testing.T) {
	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	body := bytes.NewBufferString(`{"model":"minimax/MiniMax-M3"}`)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, body)

	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, miniMaxChatRequest())
	require.NotNil(t, bifrostErr)
	require.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	require.Contains(t, bifrostErr.Error.Message, "large-payload passthrough")
	require.NotNil(t, bifrostErr.AllowFallbacks)
	require.False(t, *bifrostErr.AllowFallbacks, "a drained one-shot body cannot be retried by a fallback provider")
	require.Zero(t, body.Len(), "rejected uploads must be drained before returning")
}

func TestMiniMaxResponsesRejectsUnsupportedReasoningEffort(t *testing.T) {
	request := miniMaxResponsesRequest()
	request.Params.Reasoning.Effort = schemas.Ptr(schemas.ReasoningEffortXHigh)

	provider, err := NewMiniMaxProvider(&schemas.ProviderConfig{}, testLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Responses(ctx, schemas.Key{}, request)
	require.NotNil(t, bifrostErr)
	require.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	require.Contains(t, bifrostErr.Error.Message, "unsupported MiniMax reasoning effort")
}
