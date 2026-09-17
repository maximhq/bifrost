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
