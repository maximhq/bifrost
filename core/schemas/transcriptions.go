package schemas

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

type TranscriptionInput struct {
	File []byte `json:"file"`
}

type TranscriptionParameters struct {
	Language       *string `json:"language,omitempty"`
	Prompt         *string `json:"prompt,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"` // Default is "json"
	Format         *string `json:"file_format,omitempty"`     // Type of file, not required in openai, but required in gemini

	// Elevenlabs-specific fields
	TagAudioEvents        *bool                                      `json:"tag_audio_events,omitempty"`
	NumSpeakers           *int                                       `json:"num_speakers,omitempty"`
	TimestampsGranularity *BifrostTranscriptionTimestampsGranularity `json:"timestamps_granularity,omitempty"`
	Diarize               *bool                                      `json:"diarize,omitempty"`
	DiarizationThreshold  *float64                                   `json:"diarization_threshold,omitempty"`
	AdditionalFormats     []BifrostTranscriptionAdditionalFormat     `json:"additional_formats,omitempty"`
	FileFormat            *BifrostTranscriptionFileFormat            `json:"elevenlabs_file_format,omitempty"`
	CloudStorageURL       *string                                    `json:"cloud_storage_url,omitempty"`
	Webhook               *bool                                      `json:"webhook,omitempty"`
	WebhookID             *string                                    `json:"webhook_id,omitempty"`
	Temparature           *float64                                   `json:"temperature,omitempty"`
	Seed                  *int                                       `json:"seed,omitempty"`
	UseMultiChannel       *bool                                      `json:"use_multichannel,omitempty"`
	WebhookMetadata       interface{}                                `json:"webhook_metadata,omitempty"`

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type BifrostTranscriptionTimestampsGranularity string

const (
	BifrostTranscriptionTimestampsGranularityNone      BifrostTranscriptionTimestampsGranularity = "none"
	BifrostTranscriptionTimestampsGranularityWord      BifrostTranscriptionTimestampsGranularity = "word"
	BifrostTranscriptionTimestampsGranularityCharacter BifrostTranscriptionTimestampsGranularity = "character"
)

type BifrostTranscriptionFileFormat string

const (
	BifrostTranscriptionFileFormatPcmS16le16 BifrostTranscriptionFileFormat = "pcm_s16le_16"
	BifrostTranscriptionFileFormatOther      BifrostTranscriptionFileFormat = "other"
)

type BifrostTranscriptionAdditionalFormat struct {
	Format                      BifrostTranscriptionExportOptions `json:"format"`
	IncludeSpeakers             *bool                             `json:"include_speakers,omitempty"`
	IncludeTimestamps           *bool                             `json:"include_timestamps,omitempty"`
	SegmentOnSilenceLongerThanS *float64                          `json:"segment_on_silence_longer_than_s,omitempty"`
	MaxSegmentDurationS         *float64                          `json:"max_segment_duration_s,omitempty"`
	MaxSegmentChars             *int                              `json:"max_segment_chars,omitempty"`
	MaxCharactersPerLine        *int                              `json:"max_characters_per_line,omitempty"`
}

type BifrostTranscriptionExportOptions string

const (
	BifrostTranscriptionExportOptionsSegmentedJson BifrostTranscriptionExportOptions = "segmented_json"
	BifrostTranscriptionExportOptionsDocx          BifrostTranscriptionExportOptions = "docx"
	BifrostTranscriptionExportOptionsPdf           BifrostTranscriptionExportOptions = "pdf"
	BifrostTranscriptionExportOptionsTxt           BifrostTranscriptionExportOptions = "txt"
	BifrostTranscriptionExportOptionsHtml          BifrostTranscriptionExportOptions = "html"
	BifrostTranscriptionExportOptionsSrt           BifrostTranscriptionExportOptions = "srt"
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