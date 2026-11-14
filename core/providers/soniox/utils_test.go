package soniox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveTTSBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		want    string
	}{
		{"global", "https://api.soniox.com", "https://tts-rt.soniox.com"},
		{"regional host keeps its region", "https://api.in.soniox.com", "https://tts-rt.in.soniox.com"},
		{"trailing slash is trimmed", "https://api.jp.soniox.com/", "https://tts-rt.jp.soniox.com"},
		{"non-api host is used as is", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"proxy path is kept", "https://proxy.example.com/soniox", "https://proxy.example.com/soniox"},
		{"an api. host that is not Soniox's is used as is", "https://api.gateway.example", "https://api.gateway.example"},
		{"such a host keeps its port and path", "https://api.gateway.example:8443/soniox", "https://api.gateway.example:8443/soniox"},
		{"a Soniox host with a port still maps", "https://api.eu.soniox.com:443", "https://tts-rt.eu.soniox.com:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveTTSBaseURL(tc.baseURL)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := deriveTTSBaseURL("://bad")
	assert.Error(t, err)
}

func TestDeriveRealtimeURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		want    string
	}{
		{"global", "https://api.soniox.com", "wss://stt-rt.soniox.com/transcribe-websocket"},
		{"regional host keeps its region", "https://api.in.soniox.com", "wss://stt-rt.in.soniox.com/transcribe-websocket"},
		{"trailing slash is trimmed", "https://api.jp.soniox.com/", "wss://stt-rt.jp.soniox.com/transcribe-websocket"},
		{"plain http becomes ws", "http://127.0.0.1:8080", "ws://127.0.0.1:8080/transcribe-websocket"},
		{"proxy path is kept", "https://proxy.example.com/soniox", "wss://proxy.example.com/soniox/transcribe-websocket"},
		{"an api. host that is not Soniox's is used as is", "https://api.gateway.example", "wss://api.gateway.example/transcribe-websocket"},
		{"such a host keeps its port and path", "https://api.gateway.example:8443/soniox", "wss://api.gateway.example:8443/soniox/transcribe-websocket"},
		{"a Soniox host with a port still maps", "https://api.eu.soniox.com:443", "wss://stt-rt.eu.soniox.com:443/transcribe-websocket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveRealtimeURL(tc.baseURL)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := deriveRealtimeURL("://bad")
	assert.Error(t, err)
}

func TestTTSWebSocketURL(t *testing.T) {
	for baseURL, want := range map[string]string{
		"https://tts-rt.soniox.com":        "wss://tts-rt.soniox.com/tts-websocket",
		"http://127.0.0.1:8080":            "ws://127.0.0.1:8080/tts-websocket",
		"https://proxy.example.com/soniox": "wss://proxy.example.com/soniox/tts-websocket",
	} {
		assert.Equal(t, want, ttsWebSocketURL(baseURL), baseURL)
	}
}

func TestConvertBifrostSpeechFormatToSoniox(t *testing.T) {
	for format, want := range map[string]string{
		"":          "mp3",
		"mp3":       "mp3",
		"opus":      "opus",
		"aac":       "aac",
		"flac":      "flac",
		"wav":       "wav",
		"pcm":       "pcm_s16le",
		"pcm_mulaw": "pcm_mulaw", // native Soniox formats pass through
	} {
		assert.Equal(t, want, convertBifrostSpeechFormatToSoniox(format), "format %q", format)
	}
}
