package schemas

import (
	"fmt"

	"github.com/bytedance/sonic"
)

// SpeechInput represents the input for a speech request.
type SpeechInput struct {
	Input string `json:"input"`
}

type SpeechParameters struct {
	VoiceConfig    *SpeechVoiceInput `json:"voice"`
	Instructions   string            `json:"instructions,omitempty"`
	ResponseFormat string            `json:"response_format,omitempty"` // Default is "mp3"
	Speed          *float64          `json:"speed,omitempty"`

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type SpeechVoiceInput struct {
	Voice            *string
	MultiVoiceConfig []VoiceConfig
}

type VoiceConfig struct {
	Speaker string `json:"speaker"`
	Voice   string `json:"voice"`
}

// MarshalJSON implements custom JSON marshalling for SpeechVoiceInput.
// It marshals either Voice or MultiVoiceConfig directly without wrapping.
func (vi *SpeechVoiceInput) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if vi.Voice != nil && len(vi.MultiVoiceConfig) > 0 {
		return nil, fmt.Errorf("both Voice and MultiVoiceConfig are set; only one should be non-nil")
	}

	if vi.Voice != nil {
		return sonic.Marshal(*vi.Voice)
	}
	if len(vi.MultiVoiceConfig) > 0 {
		return sonic.Marshal(vi.MultiVoiceConfig)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for SpeechVoiceInput.
// It determines whether "voice" is a string or a VoiceConfig object/array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (vi *SpeechVoiceInput) UnmarshalJSON(data []byte) error {
	// Reset receiver state before attempting any decode to avoid stale data
	vi.Voice = nil
	vi.MultiVoiceConfig = nil

	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		vi.Voice = &stringContent
		return nil
	}

	// Try to unmarshal as an array of VoiceConfig objects
	var voiceConfigs []VoiceConfig
	if err := sonic.Unmarshal(data, &voiceConfigs); err == nil {
		// Validate each VoiceConfig and build a new slice deterministically
		validConfigs := make([]VoiceConfig, 0, len(voiceConfigs))
		for _, config := range voiceConfigs {
			if config.Voice == "" {
				return fmt.Errorf("voice config has empty voice field")
			}
			validConfigs = append(validConfigs, config)
		}
		vi.MultiVoiceConfig = validConfigs
		return nil
	}

	return fmt.Errorf("voice field is neither a string, nor an array of VoiceConfig objects")
}

type BifrostSpeech struct {
	Usage *AudioLLMUsage `json:"usage,omitempty"`
	Audio []byte         `json:"audio"`

	*BifrostSpeechStreamResponse
}

type BifrostSpeechStreamResponse struct {
	Type string `json:"type"`
}
