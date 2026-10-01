package elevenlabs

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestToElevenlabsSpeechRequestSendsOnlySetVoiceSettings pins that voice_settings
// carries only the fields the caller set. ElevenLabs applies every field present
// as an override of the voice's stored settings, so a zero value for an unset
// field (stability 0, similarity_boost 0, speed 0) changes the voice or is
// rejected instead of keeping the voice's own setting.
func TestToElevenlabsSpeechRequestSendsOnlySetVoiceSettings(t *testing.T) {
	tests := []struct {
		name   string
		params *schemas.SpeechParameters
		want   string
	}{
		{
			name:   "speed only",
			params: &schemas.SpeechParameters{Speed: schemas.Ptr(1.1)},
			want:   `{"speed":1.1}`,
		},
		{
			name: "stability only",
			params: &schemas.SpeechParameters{ExtraParams: map[string]interface{}{
				"stability": 0.3,
			}},
			want: `{"stability":0.3}`,
		},
		{
			name: "explicit zero and false values are kept",
			params: &schemas.SpeechParameters{ExtraParams: map[string]interface{}{
				"style":             0.0,
				"use_speaker_boost": false,
			}},
			want: `{"use_speaker_boost":false,"style":0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ToElevenlabsSpeechRequest(&schemas.BifrostSpeechRequest{
				Provider: schemas.Elevenlabs,
				Model:    "eleven_multilingual_v2",
				Input:    &schemas.SpeechInput{Input: "hello"},
				Params:   tt.params,
			})
			require.NotNil(t, req)
			require.NotNil(t, req.VoiceSettings)

			raw, err := json.Marshal(req.VoiceSettings)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(raw))
		})
	}
}

// TestToElevenlabsSpeechRequestOmitsVoiceSettingsWhenUnset keeps the voice's
// stored settings untouched when the caller sets none of them.
func TestToElevenlabsSpeechRequestOmitsVoiceSettingsWhenUnset(t *testing.T) {
	req := ToElevenlabsSpeechRequest(&schemas.BifrostSpeechRequest{
		Provider: schemas.Elevenlabs,
		Model:    "eleven_multilingual_v2",
		Input:    &schemas.SpeechInput{Input: "hello"},
		Params:   &schemas.SpeechParameters{},
	})
	require.NotNil(t, req)
	require.Nil(t, req.VoiceSettings)
}
