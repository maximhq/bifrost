package minimax

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestToMiniMaxSpeechRequestRejectsMalformedRecognizedExtra(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value interface{}
	}{
		{name: "voice setting", key: "voice_setting", value: "not-an-object"},
		{name: "audio setting", key: "audio_setting", value: []string{"not-an-object"}},
		{name: "subtitle enable", key: "subtitle_enable", value: "yes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := speechRequest()
			req.Params.ExtraParams[tc.key] = tc.value
			_, err := ToMiniMaxSpeechRequest(req, false)
			require.ErrorContains(t, err, "invalid MiniMax "+tc.key)
		})
	}
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

	firstPage, bifrostErr := provider.ListModels(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		nil,
		&schemas.BifrostListModelsRequest{PageSize: 1},
	)
	require.Nil(t, bifrostErr)
	require.Len(t, firstPage.Data, 1)
	require.NotEmpty(t, firstPage.NextPageToken)

	secondPage, bifrostErr := provider.ListModels(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		nil,
		&schemas.BifrostListModelsRequest{PageSize: 1, PageToken: firstPage.NextPageToken},
	)
	require.Nil(t, bifrostErr)
	require.Len(t, secondPage.Data, 1)
	require.NotEqual(t, firstPage.Data[0].ID, secondPage.Data[0].ID)
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
		{code: 1039, wantStatus: http.StatusTooManyRequests},
		{code: 1042, wantStatus: http.StatusBadRequest},
		{code: 2013, wantStatus: http.StatusBadRequest},
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
