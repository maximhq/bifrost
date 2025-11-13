package elevenlabs

// REQUEST TYPES
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
	ModelID                         *string                                    `json:"model_id,omitempty"` // defaults to "eleven_multilingual_v2"
	LanguageCode                    *string                                    `json:"language_code,omitempty"`
	VoiceSettings                   *ElevenLabsVoiceSettings                   `json:"voice_settings,omitempty"`
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

type ElevenLabsVoiceSettings struct {
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
type ElevenLabsModel struct {
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
	Languages                          []ElevenLabsLanguage `json:"languages"`
	RequiresAlphaAccess                bool                 `json:"requires_alpha_access"`
	MaxCharactersRequestFreeUser       int                  `json:"max_characters_request_free_user"`
	MaxCharactersRequestSubscribedUser int                  `json:"max_characters_request_subscribed_user"`
	MaxTextLengthPerRequest            int                  `json:"maximum_text_length_per_request"`
	ModelRates                         ElevenLabsModelRate  `json:"model_rates"`
	ConcurrencyGroup                   string               `json:"concurrency_group"`
}

type ElevenLabsLanguage struct {
	LanguageID string `json:"language_id"`
	Name       string `json:"name"`
}

type ElevenLabsModelRate struct {
	CharacterCostMultiplier float64 `json:"character_cost_multiplier"`
}

type ElevenlabsListModelsResponse []ElevenLabsModel