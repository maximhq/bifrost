package elevenlabs_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassthroughSpeechPath(t *testing.T) {
	for _, tc := range []struct {
		method, path      string
		generates, stream bool
	}{
		{http.MethodPost, "/v1/text-to-speech/voice123", true, false},
		{http.MethodPost, "/v1/text-to-speech/voice123/with-timestamps", true, false},
		{http.MethodPost, "/v1/text-to-speech/voice123/stream", true, true},
		{http.MethodPost, "/v1/text-to-speech/voice123/stream/with-timestamps", true, true},
		// A voice named like an endpoint is still a voice.
		{http.MethodPost, "/v1/text-to-speech/stream", true, false},
		{http.MethodPost, "/v1/text-to-speech/with-timestamps/stream", true, true},
		{http.MethodGet, "/v1/text-to-speech/voice123", false, false},
		{http.MethodGet, "/v1/models", false, false},
		{http.MethodPost, "/v1/text-to-speech/", false, false},
		{http.MethodPost, "/v1/text-to-speech/voice123/other", false, false},
		{http.MethodPost, "/v1/text-to-dialogue", false, false},
		{http.MethodPost, "/v1/speech-to-text", false, false},
	} {
		generates, stream := elevenlabs.PassthroughSpeechPath(tc.method, tc.path)
		assert.Equal(t, tc.generates, generates, "%s %s generates", tc.method, tc.path)
		assert.Equal(t, tc.stream, stream, "%s %s streams", tc.method, tc.path)
		assert.Equal(t, tc.stream, elevenlabs.IsPassthroughStreamPath(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}

// The dialogue stream endpoints answer as streams but are not billed from the request, so only the stream answer is theirs.
func TestIsPassthroughStreamPathForDialogue(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		stream       bool
	}{
		{http.MethodPost, "/v1/text-to-dialogue/stream", true},
		{http.MethodPost, "/v1/text-to-dialogue/stream/with-timestamps", true},
		{http.MethodPost, "/v1/text-to-dialogue", false},
		{http.MethodPost, "/v1/text-to-dialogue/with-timestamps", false},
		{http.MethodPost, "/v1/text-to-dialogue/stream/other", false},
		{http.MethodGet, "/v1/text-to-dialogue/stream", false},
	} {
		assert.Equal(t, tc.stream, elevenlabs.IsPassthroughStreamPath(tc.method, tc.path), "%s %s", tc.method, tc.path)
		generates, speechStreams := elevenlabs.PassthroughSpeechPath(tc.method, tc.path)
		assert.False(t, generates || speechStreams, "%s %s is not speech", tc.method, tc.path)
	}
}

func TestExtractPassthroughUsage(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"text counts runes", http.MethodPost, "/v1/text-to-speech/v", `{"text":"नमस्ते duniya","model_id":"eleven_v4_turbo"}`, 13},
		{"no text is no usage", http.MethodPost, "/v1/text-to-speech/v", `{"model_id":"eleven_v4_turbo"}`, 0},
		{"empty text is no usage", http.MethodPost, "/v1/text-to-speech/v", `{"text":""}`, 0},
		{"a non-string text is no usage", http.MethodPost, "/v1/text-to-speech/v", `{"text":7}`, 0},
		{"another path is no usage", http.MethodPost, "/v1/speech-to-text", `{"text":"hello"}`, 0},
		{"another method is no usage", http.MethodGet, "/v1/text-to-speech/v", `{"text":"hello"}`, 0},
		{"an invalid body is no usage", http.MethodPost, "/v1/text-to-speech/v", `{"text":`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := elevenlabs.ExtractPassthroughUsage(tc.method, tc.path, []byte(tc.body))
			if tc.want == 0 {
				assert.Nil(t, usage)
				return
			}
			require.NotNil(t, usage)
			assert.Equal(t, tc.want, usage.AudioInputChars)
		})
	}
}

// passthroughProvider is a provider whose base URL is an upstream that records what reached it.
func passthroughProvider(t *testing.T, base string) *elevenlabs.ElevenlabsProvider {
	t.Helper()
	return elevenlabs.NewElevenlabsProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: base, AllowPrivateNetwork: true},
	}, bifrost.NewNoOpLogger())
}

func passthroughRequest(path, query, body string) *schemas.BifrostPassthroughRequest {
	return &schemas.BifrostPassthroughRequest{
		Provider:    schemas.Elevenlabs,
		Model:       "eleven_v4_turbo",
		Method:      http.MethodPost,
		Path:        path,
		RawQuery:    query,
		Body:        []byte(body),
		SafeHeaders: map[string]string{"content-type": "application/json", "xi-api-key": "sk-bf-client-virtual-key", "user-agent": "elevenlabs/2.78.0"},
	}
}

// The request reaches the key's base URL, path prefix included, as the client sent it, carrying the
// key's credential and never the client's.
func TestPassthroughForwardsAsSent(t *testing.T) {
	var gotPath, gotQuery, gotBody string
	var gotKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotKeys = r.Header.Values("xi-api-key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("request-id", "req-1")
		w.Header().Set("history-item-id", "hist-1")
		w.Header().Set("character-cost", "22")
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer server.Close()

	provider := passthroughProvider(t, server.URL+"/residency/")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	body := `{"text":"hello there","model_id":"eleven_v4_turbo","voice_settings":{"stability":0.3},"seed":7}`
	resp, bifrostErr := provider.Passthrough(ctx, schemas.Key{Value: *schemas.NewSecretVar("provider-key")},
		passthroughRequest("/v1/text-to-speech/voice123", "output_format=mp3_22050_32&optimize_streaming_latency=3&enable_logging=false", body))
	require.Nil(t, bifrostErr)

	assert.Equal(t, "/residency/v1/text-to-speech/voice123", gotPath)
	assert.Equal(t, "output_format=mp3_22050_32&optimize_streaming_latency=3&enable_logging=false", gotQuery)
	assert.Equal(t, body, gotBody)
	assert.Equal(t, []string{"provider-key"}, gotKeys, "only the selected key's credential goes upstream")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "audio-bytes", string(resp.Body))
	for header, want := range map[string]string{"request-id": "req-1", "history-item-id": "hist-1", "character-cost": "22", "Content-Type": "audio/mpeg"} {
		assert.Equal(t, want, resp.Headers[http.CanonicalHeaderKey(header)], header)
	}
	require.NotNil(t, resp.PassthroughUsage)
	assert.Equal(t, 11, resp.PassthroughUsage.AudioInputChars)
}

// A key without a value sends no credential, and the client's is still dropped.
func TestPassthroughDropsTheClientCredentialWithoutAKeyValue(t *testing.T) {
	var gotKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKeys = r.Header.Values("xi-api-key")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, bifrostErr := passthroughProvider(t, server.URL).Passthrough(ctx, schemas.Key{}, passthroughRequest("/v1/models", "", ""))
	require.Nil(t, bifrostErr)
	assert.Empty(t, gotKeys)
}

// An upstream error reaches the client as ElevenLabs wrote it, and bills nothing.
func TestPassthroughRelaysUpstreamErrors(t *testing.T) {
	const upstreamError = `{"detail":{"status":"quota_exceeded","message":"This request exceeds your quota."}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(upstreamError))
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	resp, bifrostErr := passthroughProvider(t, server.URL).Passthrough(ctx, schemas.Key{Value: *schemas.NewSecretVar("k")},
		passthroughRequest("/v1/text-to-speech/voice123", "", `{"text":"hello","model_id":"eleven_v4_turbo"}`))
	require.Nil(t, bifrostErr)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.JSONEq(t, upstreamError, string(resp.Body))
	assert.Nil(t, resp.PassthroughUsage)
}

// The audio is forwarded as it arrives and the final chunk carries the characters sent.
func TestPassthroughStreamForwardsAudioAndReportsUsage(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("xi-api-key")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("request-id", "req-2")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{"aaaa", "bbbb", "cccc"} {
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
		}
	}))
	defer server.Close()

	var finals []*schemas.BifrostPassthroughResponse
	var audio []byte
	runner := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if resp != nil && resp.PassthroughResponse != nil {
			if resp.PassthroughResponse.Body != nil {
				audio = append(audio, resp.PassthroughResponse.Body...)
			} else {
				finals = append(finals, resp.PassthroughResponse)
			}
		}
		return resp, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	stream, bifrostErr := passthroughProvider(t, server.URL).PassthroughStream(ctx, runner, nil, schemas.Key{Value: *schemas.NewSecretVar("provider-key")},
		passthroughRequest("/v1/text-to-speech/voice123/stream", "", `{"text":"hello","model_id":"eleven_v4_turbo"}`))
	require.Nil(t, bifrostErr)
	for range stream {
	}

	assert.Equal(t, "/v1/text-to-speech/voice123/stream", gotPath)
	assert.Equal(t, "provider-key", gotKey)
	assert.Equal(t, "aaaabbbbcccc", string(audio))
	require.Len(t, finals, 1)
	assert.Equal(t, "req-2", finals[0].Headers["Request-Id"])
	require.NotNil(t, finals[0].PassthroughUsage)
	assert.Equal(t, 5, finals[0].PassthroughUsage.AudioInputChars)
}

// A stream the upstream refuses carries its status and error body, and no usage.
func TestPassthroughStreamRelaysUpstreamErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail":{"status":"too_many_concurrent_requests","message":"slow down"}}`))
	}))
	defer server.Close()

	var statuses []int
	var body []byte
	var usage []*schemas.BifrostPassthroughUsage
	runner := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if resp != nil && resp.PassthroughResponse != nil {
			statuses = append(statuses, resp.PassthroughResponse.StatusCode)
			body = append(body, resp.PassthroughResponse.Body...)
			usage = append(usage, resp.PassthroughResponse.PassthroughUsage)
		}
		return resp, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	stream, bifrostErr := passthroughProvider(t, server.URL).PassthroughStream(ctx, runner, nil, schemas.Key{Value: *schemas.NewSecretVar("k")},
		passthroughRequest("/v1/text-to-speech/voice123/stream", "", `{"text":"hi","model_id":"eleven_v4_turbo"}`))
	require.Nil(t, bifrostErr)
	for range stream {
	}

	require.NotEmpty(t, statuses)
	for _, status := range statuses {
		assert.Equal(t, http.StatusTooManyRequests, status)
	}
	assert.Contains(t, string(body), "too_many_concurrent_requests")
	for _, u := range usage {
		assert.Nil(t, u)
	}
}

// A path that would leave the key's host is refused before any request is sent.
func TestPassthroughRefusesAPathOffTheHost(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, bifrostErr := passthroughProvider(t, "https://api.elevenlabs.io").Passthrough(ctx, schemas.Key{Value: *schemas.NewSecretVar("k")},
		passthroughRequest("//evil.example/v1/models", "", ""))
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
}

// BenchmarkExtractPassthroughUsage is the cost of reading the characters of a text-to-speech body, which the
// provider does once per request.
func BenchmarkExtractPassthroughUsage(b *testing.B) {
	body := []byte(`{"text":"Namaste, this is a sentence of the kind a voice agent speaks.","model_id":"eleven_v4_turbo","voice_settings":{"stability":0.4,"similarity_boost":0.8},"seed":7}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if elevenlabs.ExtractPassthroughUsage(http.MethodPost, "/v1/text-to-speech/voice123/stream", body) == nil {
			b.Fatal("no usage")
		}
	}
}

// BenchmarkPassthroughSpeechPath is the cost of classifying a request path, which the router does once per request.
func BenchmarkPassthroughSpeechPath(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if !elevenlabs.IsPassthroughStreamPath(http.MethodPost, "/v1/text-to-speech/voice123/stream/with-timestamps") {
			b.Fatal("not a stream")
		}
	}
}

// The router hands the provider the decoded path, so a voice that carries a query or fragment delimiter stays a
// voice: it is escaped back into the path ElevenLabs is called under and never becomes a query or a fragment.
func TestPassthroughKeepsAVoiceInsideItsPathSegment(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, bifrostErr := passthroughProvider(t, server.URL).Passthrough(ctx, schemas.Key{Value: *schemas.NewSecretVar("k")},
		passthroughRequest("/v1/text-to-speech/a?b#c%d", "output_format=mp3_44100_128", `{"text":"hi","model_id":"eleven_v4_turbo"}`))
	require.Nil(t, bifrostErr)

	assert.Equal(t, "/v1/text-to-speech/a%3Fb%23c%25d", gotPath)
	assert.Equal(t, "output_format=mp3_44100_128", gotQuery, "only the client's own query is the query")
}
