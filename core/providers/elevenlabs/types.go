package elevenlabs

// SPEECH TYPES
type TextNormalization string

const (
	// TextNormalizationAuto corresponds to "auto"
	TextNormalizationAuto TextNormalization = "auto"
	// TextNormalizationOn corresponds to "on"
	TextNormalizationOn TextNormalization = "on"
	// TextNormalizationOff corresponds to "off"
	TextNormalizationOff TextNormalization = "off"
)

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
	ApplyTextNormalization          *TextNormalization                         `json:"apply_text_normalization,omitempty"`
	ApplyLanguageTextNormalization  *bool                                      `json:"apply_language_text_normalization,omitempty"`
	UsePVCAsIVC                     *bool                                      `json:"use_pvc_as_ivc,omitempty"` // deprecated
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
	ModelID string `json:"model_id"`
	File    []byte `json:"-"`

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
	Temparature           *float64                         `json:"temperature,omitempty"`
	Seed                  *int                             `json:"seed,omitempty"`
	UseMultiChannel       *bool                            `json:"use_multi_channel,omitempty"`
	WebhookMetadata       interface{}                      `json:"webhook_metadata,omitempty"`
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
// ElevenlabsValidationError represents the 422 validation error format
type ElevenlabsValidationError struct {
	Detail []struct {
		Loc  []string `json:"loc"`
		Msg  string   `json:"msg"`
		Type string   `json:"type"`
	} `json:"detail"`
}

// ElevenlabsGenericError represents other Elevenlabs error formats
type ElevenlabsGenericError struct {
	Detail struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"detail"`
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
