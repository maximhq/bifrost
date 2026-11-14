package soniox

import (
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.soniox.com"

	// Soniox serves its REST API from api.<region>, text-to-speech from tts-rt.<region> and real-time
	// speech-to-text from stt-rt.<region>.
	apiHostPrefix      = "api."
	ttsHostPrefix      = "tts-rt."
	realtimeHostPrefix = "stt-rt."
	realtimePath       = "/transcribe-websocket"
	sonioxHostSuffix   = ".soniox.com"
	ttsWebSocketPath   = "/tts-websocket"

	transcriptionPollInitial = 500 * time.Millisecond
	transcriptionPollMax     = 3 * time.Second
	cleanupTimeout           = 10 * time.Second
)

// bifrostToSonioxSpeechFormat maps OpenAI response formats to Soniox audio_format values.
// Soniox requires audio_format, so an empty format resolves to the OpenAI default (mp3).
var bifrostToSonioxSpeechFormat = map[string]string{
	"":     "mp3",
	"mp3":  "mp3",
	"opus": "opus",
	"aac":  "aac",
	"flac": "flac",
	"wav":  "wav",
	"pcm":  "pcm_s16le",
}

// convertBifrostSpeechFormatToSoniox maps a Bifrost response format to a Soniox audio_format;
// native Soniox formats such as pcm_mulaw pass through unchanged.
func convertBifrostSpeechFormatToSoniox(format string) string {
	if sonioxFormat, ok := bifrostToSonioxSpeechFormat[format]; ok {
		return sonioxFormat
	}
	return format
}

// deriveTTSBaseURL returns the text-to-speech base URL for a REST base URL by swapping the
// leading api. host label for tts-rt., so a regional base URL (api.<region>.soniox.com) reaches
// the same region's TTS host. Any other host, such as a proxy, is used as is, so the key never
// goes to a host the operator did not configure.
func deriveTTSBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	swapAPIHostPrefix(parsed, ttsHostPrefix)
	return strings.TrimRight(parsed.String(), "/"), nil
}

// deriveRealtimeURL returns the real-time transcription WebSocket URL for a REST base URL, on the stt-rt.
// host of the same region. Any other host, such as a proxy, is used as is.
func deriveRealtimeURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	swapAPIHostPrefix(parsed, realtimeHostPrefix)
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	}
	return strings.TrimRight(parsed.String(), "/") + realtimePath, nil
}

// ttsWebSocketURL returns the text-to-speech WebSocket URL for a text-to-speech base URL.
func ttsWebSocketURL(ttsBaseURL string) string {
	switch {
	case strings.HasPrefix(ttsBaseURL, "https://"):
		ttsBaseURL = "wss://" + strings.TrimPrefix(ttsBaseURL, "https://")
	case strings.HasPrefix(ttsBaseURL, "http://"):
		ttsBaseURL = "ws://" + strings.TrimPrefix(ttsBaseURL, "http://")
	}
	return ttsBaseURL + ttsWebSocketPath
}

// swapAPIHostPrefix replaces the leading api. host label of a Soniox host with prefix, leaving any other host
// alone, so the key never goes to a host the operator did not configure.
func swapAPIHostPrefix(parsed *url.URL, prefix string) {
	if strings.HasPrefix(parsed.Host, apiHostPrefix) && strings.HasSuffix(parsed.Hostname(), sonioxHostSuffix) {
		parsed.Host = prefix + strings.TrimPrefix(parsed.Host, apiHostPrefix)
	}
}
