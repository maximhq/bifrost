package mistral

// MistralModel represents a single model in the Mistral Models API response
type MistralModel struct {
	ID                          string       `json:"id"`
	Object                      string       `json:"object"`
	Created                     int64        `json:"created"`
	OwnedBy                     string       `json:"owned_by"`
	Capabilities                Capabilities `json:"capabilities"`
	Name                        string       `json:"name"`
	Description                 string       `json:"description"`
	MaxContextLength            int          `json:"max_context_length"`
	Aliases                     []string     `json:"aliases"`
	Deprecation                 *string      `json:"deprecation,omitempty"`
	DeprecationReplacementModel *string      `json:"deprecation_replacement_model,omitempty"`
	DefaultModelTemperature     float64      `json:"default_model_temperature"`
	Type                        string       `json:"type"`
}

// Capabilities describes the model's supported features
type Capabilities struct {
	CompletionChat  bool `json:"completion_chat"`
	CompletionFim   bool `json:"completion_fim"`
	FunctionCalling bool `json:"function_calling"`
	FineTuning      bool `json:"fine_tuning"`
	Vision          bool `json:"vision"`
	Classification  bool `json:"classification"`
}

// MistralListModelsResponse is the root response object from the Mistral Models API
type MistralListModelsResponse struct {
	Object string         `json:"object"`
	Data   []MistralModel `json:"data"`
}

// ============================================================================
// Transcription Types
// ============================================================================

// MistralTranscriptionRequest represents a Mistral audio transcription request.
// Based on: https://docs.mistral.ai/capabilities/audio_transcription
type MistralTranscriptionRequest struct {
	Model                  string   `json:"model"`                             // Required: e.g., "mistral-audio-transcribe"
	File                   []byte   `json:"file"`                              // Required: Binary audio data
	Language               *string  `json:"language,omitempty"`                // Optional: ISO 639-1 language code
	Prompt                 *string  `json:"prompt,omitempty"`                  // Optional: Context hint for transcription
	ResponseFormat         *string  `json:"response_format,omitempty"`         // Optional: "json", "text", "srt", "verbose_json", "vtt"
	Temperature            *float64 `json:"temperature,omitempty"`             // Optional: Sampling temperature (0 to 1)
	Stream                 *bool    `json:"stream,omitempty"`                  // Optional: Enable streaming mode
	TimestampGranularities []string `json:"timestamp_granularities,omitempty"` // Optional: "word" or "segment"
}

// MistralTranscriptionResponse represents Mistral's transcription response.
type MistralTranscriptionResponse struct {
	Text     string                        `json:"text"`               // Transcribed text
	Duration *float64                      `json:"duration,omitempty"` // Audio duration in seconds
	Language *string                       `json:"language,omitempty"` // Detected language
	Segments []MistralTranscriptionSegment `json:"segments,omitempty"` // Segments (verbose_json format)
	Words    []MistralTranscriptionWord    `json:"words,omitempty"`    // Word-level timestamps
}

// MistralTranscriptionSegment represents a segment in verbose_json format.
type MistralTranscriptionSegment struct {
	ID               int     `json:"id"`
	Seek             int     `json:"seek,omitempty"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
	AvgLogProb       float64 `json:"avg_logprob,omitempty"`
	CompressionRatio float64 `json:"compression_ratio,omitempty"`
	NoSpeechProb     float64 `json:"no_speech_prob,omitempty"`
}

// MistralTranscriptionWord represents word-level timing information.
type MistralTranscriptionWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// ============================================================================
// Transcription Streaming Types
// ============================================================================

// MistralTranscriptionStreamEventType represents the type of streaming event.
type MistralTranscriptionStreamEventType string

const (
	// MistralTranscriptionStreamEventLanguage is the language detection event.
	MistralTranscriptionStreamEventLanguage MistralTranscriptionStreamEventType = "transcription.language"
	// MistralTranscriptionStreamEventSegment is the segment event.
	MistralTranscriptionStreamEventSegment MistralTranscriptionStreamEventType = "transcription.segment"
	// MistralTranscriptionStreamEventTextDelta is the text delta event.
	MistralTranscriptionStreamEventTextDelta MistralTranscriptionStreamEventType = "transcription.text.delta"
	// MistralTranscriptionStreamEventDone is the done event with usage info.
	MistralTranscriptionStreamEventDone MistralTranscriptionStreamEventType = "transcription.done"
)

// MistralTranscriptionStreamEvent represents a streaming transcription event from Mistral.
type MistralTranscriptionStreamEvent struct {
	Event string                          `json:"event"`
	Data  *MistralTranscriptionStreamData `json:"data,omitempty"`
}

// MistralTranscriptionStreamData represents the data payload for streaming events.
type MistralTranscriptionStreamData struct {
	// For transcription.text.delta events
	Text string `json:"text,omitempty"`

	// For transcription.language events
	Language string `json:"language,omitempty"`

	// For transcription.segment events
	Segment *MistralTranscriptionStreamSegment `json:"segment,omitempty"`

	// For transcription.done events
	Model string                     `json:"model,omitempty"`
	Usage *MistralTranscriptionUsage `json:"usage,omitempty"`
}

// MistralTranscriptionStreamSegment represents a segment in streaming response.
type MistralTranscriptionStreamSegment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// MistralTranscriptionUsage represents usage information in streaming done event.
type MistralTranscriptionUsage struct {
	PromptAudioSeconds int `json:"prompt_audio_seconds,omitempty"`
	PromptTokens       int `json:"prompt_tokens,omitempty"`
	TotalTokens        int `json:"total_tokens,omitempty"`
	CompletionTokens   int `json:"completion_tokens,omitempty"`
}
