package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestShouldReturnSpeechJSON(t *testing.T) {
	subtitle := "https://example.com/subtitles.json"
	tests := []struct {
		name          string
		provider      schemas.ModelProvider
		hasTimestamps bool
		response      *schemas.BifrostSpeechResponse
		want          bool
	}{
		{name: "binary speech", provider: schemas.MiniMax, response: &schemas.BifrostSpeechResponse{}, want: false},
		{name: "MiniMax subtitle metadata", provider: schemas.MiniMax, response: &schemas.BifrostSpeechResponse{SubtitleFile: &subtitle}, want: true},
		{name: "ElevenLabs timestamps", provider: schemas.Elevenlabs, hasTimestamps: true, response: &schemas.BifrostSpeechResponse{}, want: true},
		{name: "nil response", provider: schemas.MiniMax, response: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReturnSpeechJSON(tc.provider, tc.hasTimestamps, tc.response); got != tc.want {
				t.Fatalf("shouldReturnSpeechJSON() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpeechContentType(t *testing.T) {
	tests := map[string]string{
		"mp3":          "audio/mpeg",
		"pcm":          "application/octet-stream",
		"flac":         "audio/flac",
		"wav":          "audio/wav",
		"pcmu_raw":     "application/octet-stream",
		"pcmu_wav":     "audio/wav",
		"opus":         "audio/ogg",
		"aac":          "audio/aac",
		"mp3_22050_32": "audio/mpeg",
		"pcm_16000":    "application/octet-stream",
		"ulaw_8000":    "application/octet-stream",
		"alaw_8000":    "application/octet-stream",
	}
	for format, want := range tests {
		if got := speechContentType(format); got != want {
			t.Errorf("speechContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestSpeechAttachmentFilename(t *testing.T) {
	tests := map[string]string{
		"mp3":      "speech.mp3",
		"pcm":      "speech.pcm",
		"pcmu_raw": "speech.ulaw",
		"pcmu_wav": "speech.wav",
		"wav":      "speech.wav",
		"opus":     "speech.opus",
	}
	for format, want := range tests {
		if got := speechAttachmentFilename(format); got != want {
			t.Errorf("speechAttachmentFilename(%q) = %q, want %q", format, got, want)
		}
	}
}
