package azure

const AzureAnthropicAPIVersionDefault = "2023-06-01"

// AzureAPIVersionPreview is the preview api-version string required by endpoints
// such as the Responses API that have no stable GA version yet.
const AzureAPIVersionPreview = "preview"

// DefaultAzureAPIVersion is the fallback api-version injected for classic
// /deployments/ passthrough routes when the caller does not supply one.
const DefaultAzureAPIVersion = "2025-04-01-preview"

type AzureModelCapabilities struct {
	FineTune       bool `json:"fine_tune"`
	Inference      bool `json:"inference"`
	Completion     bool `json:"completion"`
	ChatCompletion bool `json:"chat_completion"`
	Embeddings     bool `json:"embeddings"`
}

type AzureModelDeprecation struct {
	FineTune  int64 `json:"fine_tune,omitempty"`
	Inference int64 `json:"inference,omitempty"`
}

type AzureModel struct {
	ID              string                 `json:"id"`
	Status          string                 `json:"status"`
	FineTune        string                 `json:"fine_tune,omitempty"`
	Capabilities    AzureModelCapabilities `json:"capabilities,omitempty"`
	LifecycleStatus string                 `json:"lifecycle_status"`
	Deprecation     *AzureModelDeprecation `json:"deprecation,omitempty"`
	CreatedAt       int64                  `json:"created_at"`
	Object          string                 `json:"object"`
}

type AzureListModelsResponse struct {
	Object string       `json:"object"`
	Data   []AzureModel `json:"data"`
}

// AzureSpeechTranscriptionDefinition is the "definition" form part of a fast transcription request.
type AzureSpeechTranscriptionDefinition struct {
	Locales             []string                 `json:"locales,omitempty"`
	Channels            []int                    `json:"channels,omitempty"`
	Diarization         *AzureSpeechDiarization  `json:"diarization,omitempty"`
	ProfanityFilterMode *string                  `json:"profanityFilterMode,omitempty"` // None, Masked, Removed or Tags
	PhraseList          *AzureSpeechPhraseList   `json:"phraseList,omitempty"`
	EnhancedMode        *AzureSpeechEnhancedMode `json:"enhancedMode,omitempty"`
}

type AzureSpeechDiarization struct {
	Enabled     bool `json:"enabled"`
	MaxSpeakers *int `json:"maxSpeakers,omitempty"`
}

type AzureSpeechPhraseList struct {
	Phrases []string `json:"phrases"`
}

// AzureSpeechEnhancedMode selects LLM Speech (task) or a MAI-Transcribe model (model).
type AzureSpeechEnhancedMode struct {
	Enabled        bool                     `json:"enabled"`
	Task           *string                  `json:"task,omitempty"` // transcribe or translate
	TargetLanguage *string                  `json:"targetLanguage,omitempty"`
	Prompt         []string                 `json:"prompt,omitempty"`
	Model          *string                  `json:"model,omitempty"`
	ModelOptions   *AzureSpeechModelOptions `json:"modelOptions,omitempty"`
}

type AzureSpeechModelOptions struct {
	Timestamps      *string `json:"timestamps,omitempty"`      // word, segment or none
	TranscribeStyle *string `json:"transcribeStyle,omitempty"` // verbatim or clean
}

// AzureSpeechTranscriptionResponse is a fast transcription result.
type AzureSpeechTranscriptionResponse struct {
	DurationMilliseconds int64                       `json:"durationMilliseconds"`
	CombinedPhrases      []AzureSpeechCombinedPhrase `json:"combinedPhrases"`
	Phrases              []AzureSpeechPhrase         `json:"phrases"`
}

type AzureSpeechCombinedPhrase struct {
	Channel *int   `json:"channel,omitempty"`
	Text    string `json:"text"`
}

type AzureSpeechPhrase struct {
	Channel              *int              `json:"channel,omitempty"`
	Speaker              *int              `json:"speaker,omitempty"`
	OffsetMilliseconds   int64             `json:"offsetMilliseconds"`
	DurationMilliseconds int64             `json:"durationMilliseconds"`
	Text                 string            `json:"text"`
	Words                []AzureSpeechWord `json:"words,omitempty"`
	Locale               string            `json:"locale,omitempty"`
	Confidence           float64           `json:"confidence,omitempty"`
}

type AzureSpeechWord struct {
	Text                 string `json:"text"`
	OffsetMilliseconds   int64  `json:"offsetMilliseconds"`
	DurationMilliseconds int64  `json:"durationMilliseconds"`
}

// AzureSpeechError covers the three error bodies Azure Speech returns: fast transcription's
// top-level {code, message, innerError}, the gateway's {"error": {...}}, and RFC 9110 problem details.
type AzureSpeechError struct {
	Code       string                `json:"code,omitempty"`
	Message    string                `json:"message,omitempty"`
	InnerError *AzureSpeechErrorBody `json:"innerError,omitempty"`
	Error      *AzureSpeechErrorBody `json:"error,omitempty"`
	Title      string                `json:"title,omitempty"`
	Errors     map[string][]string   `json:"errors,omitempty"`
}

type AzureSpeechErrorBody struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
