package schemas

import "sync"

type BifrostTranscriptionRequest struct {
	Provider       ModelProvider            `json:"provider"`
	Model          string                   `json:"model"`
	Input          *TranscriptionInput      `json:"input,omitempty"`
	Params         *TranscriptionParameters `json:"params,omitempty"`
	Fallbacks      []Fallback               `json:"fallbacks,omitempty"`
	RawRequestBody []byte                   `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

func (r *BifrostTranscriptionRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

type BifrostTranscriptionResponse struct {
	Duration    *float64                   `json:"duration,omitempty"` // Duration in seconds
	Language    *string                    `json:"language,omitempty"` // e.g., "english"
	LogProbs    []TranscriptionLogProb     `json:"logprobs,omitempty"`
	Segments    []TranscriptionSegment     `json:"segments,omitempty"`
	Task        *string                    `json:"task,omitempty"` // e.g., "transcribe"
	Text        string                     `json:"text"`
	Usage       *TranscriptionUsage        `json:"usage,omitempty"`
	Words       []TranscriptionWord        `json:"words,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// bifrostTranscriptionResponsePool provides a pool for BifrostTranscriptionResponse objects.
var bifrostTranscriptionResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostTranscriptionResponse{}
	},
}

// AcquireBifrostTranscriptionResponse gets a BifrostTranscriptionResponse from the pool and resets it.
func AcquireBifrostTranscriptionResponse() *BifrostTranscriptionResponse {
	r := bifrostTranscriptionResponsePool.Get().(*BifrostTranscriptionResponse)
	*r = BifrostTranscriptionResponse{}
	return r
}

// ReleaseBifrostTranscriptionResponse returns a BifrostTranscriptionResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostTranscriptionResponse(r *BifrostTranscriptionResponse) {
	if r == nil {
		return
	}
	r.Duration = nil
	r.Language = nil
	r.LogProbs = nil
	r.Segments = nil
	r.Task = nil
	r.Text = ""
	r.Usage = nil
	r.Words = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostTranscriptionResponsePool.Put(r)
}

type TranscriptionInput struct {
	File []byte `json:"file"`
}

type TranscriptionParameters struct {
	Language                *string  `json:"language,omitempty"`
	Prompt                  *string  `json:"prompt,omitempty"`
	ResponseFormat          *string  `json:"response_format,omitempty"`           // Default is "json"
	Temperature             *float64 `json:"temperature,omitempty"`               // Sampling temperature (0.0-1.0)
	TimestampGranularities  []string `json:"timestamp_granularities,omitempty"`   // "word" and/or "segment"; requires response_format=verbose_json
	Include                 []string `json:"include,omitempty"`                   // Additional response info (e.g., logprobs)
	Format                  *string  `json:"file_format,omitempty"`               // Type of file, not required in openai, but required in gemini
	MaxLength               *int     `json:"max_length,omitempty"`                // Maximum length of the transcription used by HuggingFace
	MinLength               *int     `json:"min_length,omitempty"`                // Minimum length of the transcription used by HuggingFace
	MaxNewTokens            *int     `json:"max_new_tokens,omitempty"`            // Maximum new tokens to generate used by HuggingFace
	MinNewTokens            *int     `json:"min_new_tokens,omitempty"`            // Minimum new tokens to generate used by HuggingFace

	// Elevenlabs-specific fields
	AdditionalFormats []TranscriptionAdditionalFormat `json:"additional_formats,omitempty"`
	WebhookMetadata   interface{}                     `json:"webhook_metadata,omitempty"`

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type TranscriptionAdditionalFormat struct {
	Format                      TranscriptionExportOptions `json:"format"`
	IncludeSpeakers             *bool                      `json:"include_speakers,omitempty"`
	IncludeTimestamps           *bool                      `json:"include_timestamps,omitempty"`
	SegmentOnSilenceLongerThanS *float64                   `json:"segment_on_silence_longer_than_s,omitempty"`
	MaxSegmentDurationS         *float64                   `json:"max_segment_duration_s,omitempty"`
	MaxSegmentChars             *int                       `json:"max_segment_chars,omitempty"`
	MaxCharactersPerLine        *int                       `json:"max_characters_per_line,omitempty"`
}

type TranscriptionExportOptions string

const (
	TranscriptionExportOptionsSegmentedJson TranscriptionExportOptions = "segmented_json"
	TranscriptionExportOptionsDocx          TranscriptionExportOptions = "docx"
	TranscriptionExportOptionsPdf           TranscriptionExportOptions = "pdf"
	TranscriptionExportOptionsTxt           TranscriptionExportOptions = "txt"
	TranscriptionExportOptionsHtml          TranscriptionExportOptions = "html"
	TranscriptionExportOptionsSrt           TranscriptionExportOptions = "srt"
)

// TranscriptionLogProb represents log probability information for transcription
type TranscriptionLogProb struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

// TranscriptionWord represents word-level timing information
type TranscriptionWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// TranscriptionSegment represents segment-level transcription information
type TranscriptionSegment struct {
	ID               int     `json:"id"`
	Seek             int     `json:"seek"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens"`
	Temperature      float64 `json:"temperature"`
	AvgLogProb       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
}

// TranscriptionUsage represents usage information for transcription
type TranscriptionUsage struct {
	Type              string                               `json:"type"` // "tokens" or "duration"
	InputTokens       *int                                 `json:"input_tokens,omitempty"`
	InputTokenDetails *TranscriptionUsageInputTokenDetails `json:"input_token_details,omitempty"`
	OutputTokens      *int                                 `json:"output_tokens,omitempty"`
	TotalTokens       *int                                 `json:"total_tokens,omitempty"`
	Seconds           *int                                 `json:"seconds,omitempty"` // For duration-based usage
}

type TranscriptionUsageInputTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens"`
}

type TranscriptionStreamResponseType string

const (
	TranscriptionStreamResponseTypeDelta TranscriptionStreamResponseType = "transcript.text.delta"
	TranscriptionStreamResponseTypeDone  TranscriptionStreamResponseType = "transcript.text.done"
)

// BifrostTranscriptionStreamResponse represents streaming specific fields only
type BifrostTranscriptionStreamResponse struct {
	Delta       *string                         `json:"delta,omitempty"` // For delta events
	LogProbs    []TranscriptionLogProb          `json:"logprobs,omitempty"`
	Text        string                          `json:"text"`
	Type        TranscriptionStreamResponseType `json:"type"`
	Usage       *TranscriptionUsage             `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields      `json:"extra_fields"`
}

// bifrostTranscriptionStreamResponsePool provides a pool for BifrostTranscriptionStreamResponse objects.
var bifrostTranscriptionStreamResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostTranscriptionStreamResponse{}
	},
}

// AcquireBifrostTranscriptionStreamResponse gets a BifrostTranscriptionStreamResponse from the pool and resets it.
func AcquireBifrostTranscriptionStreamResponse() *BifrostTranscriptionStreamResponse {
	r := bifrostTranscriptionStreamResponsePool.Get().(*BifrostTranscriptionStreamResponse)
	*r = BifrostTranscriptionStreamResponse{}
	return r
}

// ReleaseBifrostTranscriptionStreamResponse returns a BifrostTranscriptionStreamResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostTranscriptionStreamResponse(r *BifrostTranscriptionStreamResponse) {
	if r == nil {
		return
	}
	r.Delta = nil
	r.LogProbs = nil
	r.Text = ""
	r.Type = ""
	r.Usage = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostTranscriptionStreamResponsePool.Put(r)
}
