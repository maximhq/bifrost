package schemas

import "encoding/json"

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
	Duration         *float64                       `json:"duration,omitempty"` // Duration in seconds
	Language         *string                        `json:"language,omitempty"` // e.g., "english"
	LogProbs         []TranscriptionLogProb         `json:"logprobs,omitempty"`
	Segments         []TranscriptionSegment         `json:"segments,omitempty"` // Verbose-json style segments
	DiarizedSegments []TranscriptionDiarizedSegment `json:"-"`                  // Diarized-json style segments (response_format=diarized_json); see MarshalJSON
	Task             *string                        `json:"task,omitempty"`     // e.g., "transcribe"
	Text             string                         `json:"text"`
	Usage            *TranscriptionUsage            `json:"usage,omitempty"`
	Words            []TranscriptionWord            `json:"words,omitempty"`
	ResponseFormat   *string                        `json:"-"` // Set by provider for non-JSON formats (text, srt, vtt); used by integration response converters
	ExtraFields      BifrostResponseExtraFields     `json:"extra_fields"`
}

// MarshalJSON serializes DiarizedSegments (response_format=diarized_json)
// under the same "segments" key that verbose_json's Segments would otherwise
// use, since the two shapes are mutually exclusive per request and the
// consuming client expects them under one field name. Segments and
// DiarizedSegments cannot share a literal json tag on the struct itself
// without an encoding/json field-name conflict, hence the shadowing here.
//
// DiarizedSegments is checked for nil (not len>0): OpenAI's diarized_json
// schema treats "segments" as a required, non-optional array (unlike
// verbose_json's Optional segments), so a zero-segment diarized response
// (e.g. silent audio) must still emit "segments":[] rather than omit the
// key, or OpenAI SDK clients parsing it will fail on a missing required field.
//
// An "is_diarized" marker is also written whenever DiarizedSegments is set,
// including when empty: an empty JSON array unmarshals successfully into
// either segment type, so a zero-length diarized response has no shape of
// its own for UnmarshalJSON to sniff. Real OpenAI SDK clients tolerate the
// extra field (their generated models use extra="allow"); it exists purely
// so Bifrost's own round-trip (e.g. framework/logstore's persist/reload) can
// disambiguate the empty case.
func (r BifrostTranscriptionResponse) MarshalJSON() ([]byte, error) {
	type Alias BifrostTranscriptionResponse
	if r.DiarizedSegments != nil {
		return json.Marshal(&struct {
			Segments   []TranscriptionDiarizedSegment `json:"segments"`
			IsDiarized bool                           `json:"is_diarized,omitempty"`
			Alias
		}{Segments: r.DiarizedSegments, IsDiarized: true, Alias: Alias(r)})
	}
	return json.Marshal(Alias(r))
}

// UnmarshalJSON is the symmetric counterpart to MarshalJSON: since both
// Segments and DiarizedSegments serialize under the same "segments" key, the
// key alone doesn't say which shape was written. This matters beyond the
// initial provider response - e.g. framework/logstore persists
// BifrostTranscriptionResponse as JSON and reloads it via GORM's AfterFind
// hook on every read, so a diarized response must round-trip back into
// DiarizedSegments (string id/speaker/type), not get mis-decoded as
// TranscriptionSegment (int id) and fail or silently corrupt the data.
//
// The "is_diarized" marker (see MarshalJSON) is authoritative when present.
// Data persisted before this marker existed won't have it, so as a fallback
// the shape is sniffed by attempting verbose_json's int-id segments first
// and falling back to the diarized string-id shape on a type mismatch - this
// fallback can't distinguish an empty verbose array from an empty diarized
// one, which is exactly why new writes always include the marker.
func (r *BifrostTranscriptionResponse) UnmarshalJSON(data []byte) error {
	type Alias BifrostTranscriptionResponse
	aux := &struct {
		Segments   json.RawMessage `json:"segments"`
		IsDiarized bool            `json:"is_diarized,omitempty"`
		*Alias
	}{Alias: (*Alias)(r)}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Segments) == 0 || string(aux.Segments) == "null" {
		if aux.IsDiarized {
			r.DiarizedSegments = []TranscriptionDiarizedSegment{}
		}
		return nil
	}

	if aux.IsDiarized {
		var diarized []TranscriptionDiarizedSegment
		if err := json.Unmarshal(aux.Segments, &diarized); err != nil {
			return err
		}
		r.DiarizedSegments = diarized
		return nil
	}

	var verbose []TranscriptionSegment
	if err := json.Unmarshal(aux.Segments, &verbose); err == nil {
		r.Segments = verbose
		return nil
	}

	var diarized []TranscriptionDiarizedSegment
	if err := json.Unmarshal(aux.Segments, &diarized); err != nil {
		return err
	}
	r.DiarizedSegments = diarized
	return nil
}

func (r *BifrostTranscriptionResponse) BackfillParams(req *BifrostTranscriptionRequest) {
	if r == nil || req == nil || req.Params == nil || req.Params.ResponseFormat == nil {
		return
	}
	r.ResponseFormat = req.Params.ResponseFormat
}

// IsPlainTextTranscriptionFormat returns true if the given response format
// produces a plain-text response body (not JSON).
func IsPlainTextTranscriptionFormat(format *string) bool {
	if format == nil {
		return false
	}
	switch *format {
	case "text", "srt", "vtt":
		return true
	default:
		return false
	}
}

// IsDiarizedTranscriptionFormat returns true if the given response format
// produces speaker-diarized segments (OpenAI's response_format=diarized_json,
// used by models like gpt-4o-transcribe-diarize).
func IsDiarizedTranscriptionFormat(format *string) bool {
	return format != nil && *format == "diarized_json"
}

type TranscriptionInput struct {
	File     []byte `json:"file"`
	Filename string `json:"filename,omitempty"` // Original filename, used to preserve file format extension
}

type TranscriptionParameters struct {
	Language               *string  `json:"language,omitempty"`
	Prompt                 *string  `json:"prompt,omitempty"`
	ResponseFormat         *string  `json:"response_format,omitempty"`         // Default is "json"
	Temperature            *float64 `json:"temperature,omitempty"`             // Sampling temperature (0.0-1.0)
	TimestampGranularities []string `json:"timestamp_granularities,omitempty"` // "word" and/or "segment"; requires response_format=verbose_json
	Include                []string `json:"include,omitempty"`                 // Additional response info (e.g., logprobs)
	Format                 *string  `json:"file_format,omitempty"`             // Type of file, not required in openai, but required in gemini
	MaxLength              *int     `json:"max_length,omitempty"`              // Maximum length of the transcription used by HuggingFace
	MinLength              *int     `json:"min_length,omitempty"`              // Minimum length of the transcription used by HuggingFace
	MaxNewTokens           *int     `json:"max_new_tokens,omitempty"`          // Maximum new tokens to generate used by HuggingFace
	MinNewTokens           *int     `json:"min_new_tokens,omitempty"`          // Minimum new tokens to generate used by HuggingFace

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
	Word    string  `json:"word"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker *string `json:"speaker,omitempty"` // Speaker label/id when diarization is enabled (e.g. ElevenLabs' speaker_id)
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

// TranscriptionDiarizedSegment represents a speaker-diarized segment of transcript
// text, as returned by response_format=diarized_json (e.g. OpenAI's
// gpt-4o-transcribe-diarize). Unlike TranscriptionSegment, the segment id here
// is a string (e.g. "seg_154") and there is no seek/tokens/logprob data.
type TranscriptionDiarizedSegment struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"` // Always "transcript.text.segment"
	Speaker string  `json:"speaker"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
}

// TranscriptionUsage represents usage information for transcription
type TranscriptionUsage struct {
	Type              string                               `json:"type"` // "tokens" or "duration"
	InputTokens       *int                                 `json:"input_tokens,omitempty"`
	InputTokenDetails *TranscriptionUsageInputTokenDetails `json:"input_token_details,omitempty"`
	OutputTokens      *int                                 `json:"output_tokens,omitempty"`
	TotalTokens       *int                                 `json:"total_tokens,omitempty"`
	Seconds           *float64                             `json:"seconds,omitempty"` // For duration-based usage (fractional, e.g. 523.5)
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
