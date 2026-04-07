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
	Filename               string   `json:"filename"`                          // Original filename, used to preserve file format extension
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

// ============================================================================
// OCR Types
// ============================================================================

// MistralOCRDocument represents the document input for a Mistral OCR request.
type MistralOCRDocument struct {
	Type        string `json:"type"`
	DocumentURL string `json:"document_url,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
}

// MistralOCRRequest represents a Mistral OCR API request.
type MistralOCRRequest struct {
	Model                    string                 `json:"model"`
	ID                       string                 `json:"id,omitempty"`
	Document                 MistralOCRDocument     `json:"document"`
	IncludeImageBase64       *bool                  `json:"include_image_base64,omitempty"`
	Pages                    []int                  `json:"pages,omitempty"`
	ImageLimit               *int                   `json:"image_limit,omitempty"`
	ImageMinSize             *int                   `json:"image_min_size,omitempty"`
	TableFormat              *string                `json:"table_format,omitempty"`
	ExtractHeader            *bool                  `json:"extract_header,omitempty"`
	ExtractFooter            *bool                  `json:"extract_footer,omitempty"`
	BBoxAnnotationFormat     *string                `json:"bbox_annotation_format,omitempty"`
	DocumentAnnotationFormat *string                `json:"document_annotation_format,omitempty"`
	DocumentAnnotationPrompt *string                `json:"document_annotation_prompt,omitempty"`
	ExtraParams              map[string]interface{} `json:"-"`
}

// MistralOCRPageImage represents an extracted image in Mistral's OCR response.
type MistralOCRPageImage struct {
	ID           string  `json:"id"`
	TopLeftX     float64 `json:"top_left_x"`
	TopLeftY     float64 `json:"top_left_y"`
	BottomRightX float64 `json:"bottom_right_x"`
	BottomRightY float64 `json:"bottom_right_y"`
	ImageBase64  *string `json:"image_base64,omitempty"`
}

// MistralOCRPageDimensions represents page dimensions in Mistral's OCR response.
type MistralOCRPageDimensions struct {
	DPI    int `json:"dpi"`
	Height int `json:"height"`
	Width  int `json:"width"`
}

// MistralOCRPage represents a single page in Mistral's OCR response.
type MistralOCRPage struct {
	Index      int                       `json:"index"`
	Markdown   string                    `json:"markdown"`
	Images     []MistralOCRPageImage     `json:"images,omitempty"`
	Dimensions *MistralOCRPageDimensions `json:"dimensions,omitempty"`
}

// MistralOCRUsageInfo represents usage information in Mistral's OCR response.
type MistralOCRUsageInfo struct {
	PagesProcessed int `json:"pages_processed"`
	DocSizeBytes   int `json:"doc_size_bytes"`
}

// MistralOCRResponse represents Mistral's OCR API response.
type MistralOCRResponse struct {
	Model              string              `json:"model"`
	Pages              []MistralOCRPage    `json:"pages"`
	UsageInfo          *MistralOCRUsageInfo `json:"usage_info,omitempty"`
	DocumentAnnotation *string             `json:"document_annotation,omitempty"`
}
