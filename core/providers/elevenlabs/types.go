package elevenlabs

import (
	"strings"

	"github.com/bytedance/sonic"
)

// SPEECH TYPES

type ElevenlabsSpeechRequest struct {
	Text                            string                                     `json:"text"`
	ModelID                         string                                     `json:"model_id"` // defaults to "eleven_multilingual_v2"
	LanguageCode                    *string                                    `json:"language_code,omitempty"`
	VoiceSettings                   *ElevenlabsVoiceSettings                   `json:"voice_settings,omitempty"`
	PronunciationDictionaryLocators []ElevenlabsPronunciationDictionaryLocator `json:"pronunciation_dictionary_locators"`
	Seed                            *int                                       `json:"seed,omitempty"`
	PreviousText                    *string                                    `json:"previous_text,omitempty"`
	NextText                        *string                                    `json:"next_text,omitempty"`
	PreviousRequestIDs              []string                                   `json:"previous_request_ids"`
	NextRequestIDs                  []string                                   `json:"next_request_ids"`
	ApplyTextNormalization          *string                                    `json:"apply_text_normalization,omitempty"`
	ApplyLanguageTextNormalization  *bool                                      `json:"apply_language_text_normalization,omitempty"`
	UsePVCAsIVC                     *bool                                      `json:"use_pvc_as_ivc,omitempty"` // deprecated
	ExtraParams                     map[string]interface{}                     `json:"-"`
}

// GetExtraParams implements the providerUtils.RequestBodyWithExtraParams interface.
func (r *ElevenlabsSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// ElevenlabsSpeechWithTimestampsResponse represents the response from the with-timestamps endpoint
type ElevenlabsSpeechWithTimestampsResponse struct {
	AudioBase64         string               `json:"audio_base64"`
	Alignment           *ElevenlabsAlignment `json:"alignment,omitempty"`
	NormalizedAlignment *ElevenlabsAlignment `json:"normalized_alignment,omitempty"`
}

// ElevenlabsAlignment represents character-level timing information
type ElevenlabsAlignment struct {
	CharStartTimesMs []float64 `json:"char_start_times_ms"`
	CharEndTimesMs   []float64 `json:"char_end_times_ms"`
	Characters       []string  `json:"characters"`
}

type ElevenlabsVoiceSettings struct {
	Stability       float64 `json:"stability"`         // 0-1, default 0.5
	UseSpeakerBoost bool    `json:"use_speaker_boost"` // default true
	SimilarityBoost float64 `json:"similarity_boost"`  // 0-1, default 0.75
	Style           float64 `json:"style"`             // default 0
	Speed           float64 `json:"speed"`             // default 1
}

type ElevenlabsPronunciationDictionaryLocator struct {
	PronunciationDictionaryID string  `json:"pronunciation_dictionary_id"`
	VersionID                 *string `json:"version_id,omitempty"`
}

// TRANSCRIPTION TYPES
type ElevenlabsTranscriptionRequest struct {
	ModelID               string                           `json:"model_id"`
	File                  []byte                           `json:"-"`
	LanguageCode          *string                          `json:"language_code,omitempty"`
	TagAudioEvents        *bool                            `json:"tag_audio_events,omitempty"`
	NumSpeakers           *int                             `json:"num_speakers,omitempty"`
	TimestampsGranularity *ElevenlabsTimestampsGranularity `json:"timestamps_granularity,omitempty"`
	Diarize               *bool                            `json:"diarize,omitempty"`
	DiarizationThreshold  *float64                         `json:"diarization_threshold,omitempty"`
	AdditionalFormats     []ElevenlabsAdditionalFormat     `json:"additional_formats,omitempty"`
	FileFormat            *ElevenlabsFileFormat            `json:"file_format,omitempty"`
	CloudStorageURL       *string                          `json:"cloud_storage_url,omitempty"`
	Webhook               *bool                            `json:"webhook,omitempty"`
	WebhookID             *string                          `json:"webhook_id,omitempty"`
	Temperature           *float64                         `json:"temperature,omitempty"`
	Seed                  *int                             `json:"seed,omitempty"`
	UseMultiChannel       *bool                            `json:"use_multi_channel,omitempty"`
	WebhookMetadata       interface{}                      `json:"webhook_metadata,omitempty"`
	ExtraParams           map[string]interface{}           `json:"-"`
}

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (req *ElevenlabsTranscriptionRequest) GetExtraParams() map[string]interface{} {
	return req.ExtraParams
}

type ElevenlabsTimestampsGranularity string

const (
	ElevenlabsTimestampsGranularityNone      ElevenlabsTimestampsGranularity = "none"
	ElevenlabsTimestampsGranularityWord      ElevenlabsTimestampsGranularity = "word"
	ElevenlabsTimestampsGranularityCharacter ElevenlabsTimestampsGranularity = "character"
)

type ElevenlabsFileFormat string

const (
	ElevenlabsFileFormatPcmS16le16 ElevenlabsFileFormat = "pcm_s16le_16"
	ElevenlabsFileFormatOther      ElevenlabsFileFormat = "other"
)

type ElevenlabsAdditionalFormat struct {
	Format                      ElevenlabsExportOptions `json:"format"`
	IncludeSpeakers             *bool                   `json:"include_speakers,omitempty"`
	IncludeTimestamps           *bool                   `json:"include_timestamps,omitempty"`
	SegmentOnSilenceLongerThanS *float64                `json:"segment_on_silence_longer_than_s,omitempty"`
	MaxSegmentDurationS         *float64                `json:"max_segment_duration_s,omitempty"`
	MaxSegmentChars             *int                    `json:"max_segment_chars,omitempty"`
	MaxCharactersPerLine        *int                    `json:"max_characters_per_line,omitempty"`
}

type ElevenlabsExportOptions string

const (
	ElevenlabsExportOptionsSegmentedJson ElevenlabsExportOptions = "segmented_json"
	ElevenlabsExportOptionsDocx          ElevenlabsExportOptions = "docx"
	ElevenlabsExportOptionsPdf           ElevenlabsExportOptions = "pdf"
	ElevenlabsExportOptionsTxt           ElevenlabsExportOptions = "txt"
	ElevenlabsExportOptionsHtml          ElevenlabsExportOptions = "html"
	ElevenlabsExportOptionsSrt           ElevenlabsExportOptions = "srt"
)

type ElevenlabsSpeechToTextChunkResponse struct {
	LanguageCode        string                                `json:"language_code"`
	LanguageProbability *float64                              `json:"language_probability,omitempty"`
	Text                string                                `json:"text"`
	Words               []ElevenlabsSpeechToTextWord          `json:"words"`
	ChannelIndex        *int                                  `json:"channel_index,omitempty"`
	AdditionalFormats   []*ElevenlabsAdditionalFormatResponse `json:"additional_formats,omitempty"`
	TranscriptionID     *string                               `json:"transcription_id,omitempty"`
}

type ElevenlabsSpeechToTextWord struct {
	Text       string                            `json:"text"`
	Start      *float64                          `json:"start,omitempty"`
	End        *float64                          `json:"end,omitempty"`
	Type       string                            `json:"type"`
	SpeakerID  *string                           `json:"speaker_id,omitempty"`
	LogProb    float64                           `json:"logprob"`
	Characters []ElevenlabsSpeechToTextCharacter `json:"characters,omitempty"`
}

type ElevenlabsSpeechToTextCharacter struct {
	Text  string   `json:"text"`
	Start *float64 `json:"start,omitempty"`
	End   *float64 `json:"end,omitempty"`
}

type ElevenlabsAdditionalFormatResponse struct {
	RequestedFormat string `json:"requested_format"`
	FileExtension   string `json:"file_extension"`
	ContentType     string `json:"content_type"`
	IsBase64Encoded bool   `json:"is_base64_encoded"`
	Content         string `json:"content"`
}

type ElevenlabsMultichannelSpeechToTextResponse struct {
	Transcripts     []ElevenlabsSpeechToTextChunkResponse `json:"transcripts"`
	TranscriptionID *string                               `json:"transcription_id,omitempty"`
}

type ElevenlabsSpeechToTextWebhookResponse struct {
	Message         string  `json:"message"`
	RequestID       string  `json:"request_id"`
	TranscriptionID *string `json:"transcription_id,omitempty"`
}

// ERROR TYPES
type ElevenlabsError struct {
	Detail *ElevenlabsErrorDetail `json:"detail,omitempty"`
}

// ElevenlabsErrorDetail handles both single object (non-validation errors) and
// array of objects (validation errors) formats from ElevenLabs API.
type ElevenlabsErrorDetail struct {
	// Non-validation error fields (when detail is a single object)
	Status  *string `json:"status,omitempty"`
	Message *string `json:"message,omitempty"`

	// Validation error fields (when detail is an array)
	ValidationErrors []ElevenlabsValidationError `json:"-"`
}

// ElevenlabsValidationError represents a single validation error entry
type ElevenlabsValidationError struct {
	Loc     []string `json:"loc"`
	Msg     string   `json:"msg"`
	Message string   `json:"message"` // Some APIs use "message" instead of "msg"
	Type    string   `json:"type"`
}

// UnmarshalJSON implements custom JSON unmarshaling to handle both
// single object and array formats from ElevenLabs API.
func (d *ElevenlabsErrorDetail) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as an array (validation errors)
	// Check if it's an array by looking at the first non-whitespace character
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var validationErrors []ElevenlabsValidationError
		if err := sonic.Unmarshal(data, &validationErrors); err != nil {
			return err
		}
		d.ValidationErrors = validationErrors
		// Extract message from first validation error if available
		if len(validationErrors) > 0 {
			if validationErrors[0].Message != "" {
				d.Message = &validationErrors[0].Message
			} else if validationErrors[0].Msg != "" {
				d.Message = &validationErrors[0].Msg
			}
		}
		return nil
	}

	// If not an array, try to unmarshal as a single object (non-validation error)
	var obj struct {
		Type    *string  `json:"type,omitempty"`
		Loc     []string `json:"loc,omitempty"`
		Message *string  `json:"message,omitempty"`
		Status  *string  `json:"status,omitempty"`
		Msg     *string  `json:"msg,omitempty"` // Some APIs use "msg" instead of "message"
	}
	if err := sonic.Unmarshal(data, &obj); err != nil {
		return err
	}

	// Populate non-validation error fields
	d.Status = obj.Status
	if obj.Message != nil {
		d.Message = obj.Message
	} else if obj.Msg != nil {
		d.Message = obj.Msg
	}

	// If this object has validation-like fields (Loc, Type), treat it as a single validation error
	if len(obj.Loc) > 0 || obj.Type != nil {
		validationErr := ElevenlabsValidationError{
			Loc: obj.Loc,
			Type: func() string {
				if obj.Type != nil {
					return *obj.Type
				}
				return ""
			}(),
		}
		if obj.Message != nil {
			validationErr.Message = *obj.Message
		} else if obj.Msg != nil {
			validationErr.Msg = *obj.Msg
			validationErr.Message = *obj.Msg
		}
		d.ValidationErrors = []ElevenlabsValidationError{validationErr}
	}

	return nil
}

// MODEL TYPES
type ElevenlabsModel struct {
	ModelID                            string               `json:"model_id"`
	Name                               string               `json:"name"`
	Description                        string               `json:"description"`
	ServesProVoices                    bool                 `json:"serves_pro_voices"`
	TokenCostFactor                    float64              `json:"token_cost_factor"`
	CanBeFinetuned                     bool                 `json:"can_be_finetuned"`
	CanDoTextToSpeech                  bool                 `json:"can_do_text_to_speech"`
	CanDoVoiceConversion               bool                 `json:"can_do_voice_conversion"`
	CanUseStyle                        bool                 `json:"can_use_style"`
	CanUseSpeakerBoost                 bool                 `json:"can_use_speaker_boost"`
	Languages                          []ElevenlabsLanguage `json:"languages"`
	RequiresAlphaAccess                bool                 `json:"requires_alpha_access"`
	MaxCharactersRequestFreeUser       int                  `json:"max_characters_request_free_user"`
	MaxCharactersRequestSubscribedUser int                  `json:"max_characters_request_subscribed_user"`
	MaxTextLengthPerRequest            int                  `json:"maximum_text_length_per_request"`
	ModelRates                         ElevenlabsModelRate  `json:"model_rates"`
	ConcurrencyGroup                   string               `json:"concurrency_group"`
}

type ElevenlabsLanguage struct {
	LanguageID string `json:"language_id"`
	Name       string `json:"name"`
}

type ElevenlabsModelRate struct {
	CharacterCostMultiplier float64 `json:"character_cost_multiplier"`
}

type ElevenlabsListModelsResponse []ElevenlabsModel
