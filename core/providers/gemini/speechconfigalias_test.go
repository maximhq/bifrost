package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeechConfigAcceptsCamelCaseVoice(t *testing.T) {
	var cfg SpeechConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"alloy"}},
		"languageCode":"en-US"
	}`), &cfg))

	require.NotNil(t, cfg.VoiceConfig)
	require.NotNil(t, cfg.VoiceConfig.PrebuiltVoiceConfig)
	assert.Equal(t, "alloy", cfg.VoiceConfig.PrebuiltVoiceConfig.VoiceName)
	assert.Equal(t, "en-US", cfg.LanguageCode)
}

func TestSpeechConfigAcceptsSnakeCaseVoice(t *testing.T) {
	var cfg SpeechConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"voice_config":{"prebuilt_voice_config":{"voice_name":"Kore"}},
		"language_code":"en-GB"
	}`), &cfg))

	require.NotNil(t, cfg.VoiceConfig)
	require.NotNil(t, cfg.VoiceConfig.PrebuiltVoiceConfig)
	assert.Equal(t, "Kore", cfg.VoiceConfig.PrebuiltVoiceConfig.VoiceName)
	assert.Equal(t, "en-GB", cfg.LanguageCode)
}

func TestSpeechConfigCamelCaseWins(t *testing.T) {
	var cfg SpeechConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"alloy"}},
		"voice_config":{"prebuilt_voice_config":{"voice_name":"Kore"}}
	}`), &cfg))

	require.NotNil(t, cfg.VoiceConfig)
	require.NotNil(t, cfg.VoiceConfig.PrebuiltVoiceConfig)
	assert.Equal(t, "alloy", cfg.VoiceConfig.PrebuiltVoiceConfig.VoiceName)
}
