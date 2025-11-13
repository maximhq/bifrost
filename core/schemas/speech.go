package schemas

import (
	"fmt"

	"github.com/bytedance/sonic"
)

type BifrostSpeechRequest struct {
	Provider       ModelProvider     `json:"provider"`
	Model          string            `json:"model"`
	Input          *SpeechInput      `json:"input,omitempty"`
	Params         *SpeechParameters `json:"params,omitempty"`
	Fallbacks      []Fallback        `json:"fallbacks,omitempty"`
	RawRequestBody []byte            `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

func (r *BifrostSpeechRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

type BifrostSpeechResponse struct {
	Audio       []byte                     `json:"audio"`
	Usage       *SpeechUsage               `json:"usage"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// SpeechInput represents the input for a speech request.
type SpeechInput struct {
	Input string `json:"input"`
}

type SpeechParameters struct {
	VoiceConfig    *SpeechVoiceInput `json:"voice"`
	Instructions   string            `json:"instructions,omitempty"`
	ResponseFormat string            `json:"response_format,omitempty"` // Default is "mp3"
	Speed          *float64          `json:"speed,omitempty"`

	// Elevenlabs-specific fields
	Stability                       *float64                               `json:"stability,omitempty"`
	UseSpeakerBoost                 *bool                                  `json:"use_speaker_boost,omitempty"`
	SimilarityBoost                 *float64                               `json:"similarity_boost,omitempty"`
	Style                           *float64                               `json:"style,omitempty"`
	LanguageCode                    *string                                `json:"language_code,omitempty"`
	Seed                            *int                                   `json:"seed,omitempty"`
	PronunciationDictionaryLocators []SpeechPronunciationDictionaryLocator `json:"pronunciation_dictionary_locators"`
	PreviousText                    *string                                `json:"previous_text,omitempty"`
	NextText                        *string                                `json:"next_text,omitempty"`
	PreviousRequestIDs              []string                               `json:"previous_request_ids"`
	NextRequestIDs                  []string                               `json:"next_request_ids"`
	ApplyTextNormalization          *SpeechTextNormalization               `json:"apply_text_normalization,omitempty"`
	ApplyLanguageTextNormalization  *bool                                  `json:"apply_language_text_normalization,omitempty"`
	UsePVCAsIVC                     *bool                                  `json:"use_pvc_as_ivc,omitempty"`

	// Elevenlabs-specific query parameters
	EnableLogging            *bool               `json:"enable_logging,omitempty"`
	OptimizeStreamingLatency *bool               `json:"optimize_streaming_latency,omitempty"`
	OutputFormat             *SpeechOutputFormat `json:"output_format,omitempty"`

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type SpeechOutputFormat string

const (
	SpeechOutputFormatMP3_22050_32   SpeechOutputFormat = "mp3_22050_32"
	SpeechOutputFormatMP3_24000_48   SpeechOutputFormat = "mp3_24000_48"
	SpeechOutputFormatMP3_44100_32   SpeechOutputFormat = "mp3_44100_32"
	SpeechOutputFormatMP3_44100_64   SpeechOutputFormat = "mp3_44100_64"
	SpeechOutputFormatMP3_44100_96   SpeechOutputFormat = "mp3_44100_96"
	SpeechOutputFormatMP3_44100_128  SpeechOutputFormat = "mp3_44100_128"
	SpeechOutputFormatMP3_44100_192  SpeechOutputFormat = "mp3_44100_192"
	SpeechOutputFormatPCM_8000       SpeechOutputFormat = "pcm_8000"
	SpeechOutputFormatPCM_16000      SpeechOutputFormat = "pcm_16000"
	SpeechOutputFormatPCM_22050      SpeechOutputFormat = "pcm_22050"
	SpeechOutputFormatPCM_24000      SpeechOutputFormat = "pcm_24000"
	SpeechOutputFormatPEM_32000      SpeechOutputFormat = "pem_32000"
	SpeechOutputFormatPCM_44100      SpeechOutputFormat = "pcm_44100"
	SpeechOutputFormatPCM_48000      SpeechOutputFormat = "pcm_48000"
	SpeechOutputFormatULAW_8000      SpeechOutputFormat = "ulaw_8000"
	SpeechOutputFormatALAW_8000      SpeechOutputFormat = "alaw_8000"
	SpeechOutputFormatOpus_48000_32  SpeechOutputFormat = "opus_48000_32"
	SpeechOutputFormatOpus_48000_64  SpeechOutputFormat = "opus_48000_64"
	SpeechOutputFormatOpus_48000_96  SpeechOutputFormat = "opus_48000_96"
	SpeechOutputFormatOpus_48000_128 SpeechOutputFormat = "opus_48000_128"
	SpeechOutputFormatOpus_48000_192 SpeechOutputFormat = "opus_48000_192"
)

type SpeechPronunciationDictionaryLocator struct {
	PronunciationDictionaryID string  `json:"pronunciation_dictionary_id"`
	VersionID                 *string `json:"version_id,omitempty"`
}

type SpeechTextNormalization string

const (
	SpeechTextNormalizationAuto SpeechTextNormalization = "auto"
	SpeechTextNormalizationOn   SpeechTextNormalization = "on"
	SpeechTextNormalizationOff  SpeechTextNormalization = "off"
)

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

type SpeechStreamResponseType string

const (
	SpeechStreamResponseTypeDelta SpeechStreamResponseType = "speech.audio.delta"
	SpeechStreamResponseTypeDone  SpeechStreamResponseType = "speech.audio.done"
)

type BifrostSpeechStreamResponse struct {
	Type        SpeechStreamResponseType   `json:"type"`
	Audio       []byte                     `json:"audio"`
	Usage       *SpeechUsage               `json:"usage"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

type SpeechUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}