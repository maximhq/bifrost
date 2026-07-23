package handlers

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// TestPrepareSpeechRequestVoiceValidation locks in the transport-layer voice
// requirement and its ElevenLabs carve-out. The transport cannot resolve
// virtual-key aliases, so it cannot tell a voice-less ElevenLabs sound-effect
// model (including an alias that resolves to one) from a text-to-speech model
// that omitted its voice. For ElevenLabs the voice check is therefore deferred
// to ElevenlabsProvider.Speech (which runs after alias resolution); every other
// provider keeps the handler-level "voice is required" 400.
func TestPrepareSpeechRequestVoiceValidation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError bool
	}{
		{
			// Regression: an alias like "my-sfx" -> "eleven_text_to_sound_v2"
			// carries no "eleven_text_to_sound" substring, so a name-based check
			// would wrongly 400 it before the provider could dispatch it.
			name:      "elevenlabs alias without voice is deferred to provider",
			body:      `{"model":"elevenlabs/my-sfx","input":"glass shattering on concrete"}`,
			wantError: false,
		},
		{
			name:      "elevenlabs literal sound model without voice is allowed",
			body:      `{"model":"elevenlabs/eleven_text_to_sound_v2","input":"rain on a tin roof"}`,
			wantError: false,
		},
		{
			name:      "elevenlabs tts without voice is deferred to provider (no transport 400)",
			body:      `{"model":"elevenlabs/eleven_multilingual_v2","input":"hello there"}`,
			wantError: false,
		},
		{
			name:      "non-elevenlabs provider without voice still errors at the transport",
			body:      `{"model":"openai/tts-1","input":"hello there"}`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBodyString(tt.body)

			_, _, err := prepareSpeechRequest(ctx, nil)
			if tt.wantError && err == nil {
				t.Fatalf("expected voice-required error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tt.wantError && err != nil && err.Error() != "voice is required for speech completion" {
				t.Fatalf("error = %q, want %q", err.Error(), "voice is required for speech completion")
			}
		})
	}
}
