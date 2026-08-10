package deepgram


// SPEECH TYPES

// DeepgramSpeechRequest is the POST /v1/speak body. Only text is serialized;
// model/encoding/container/sample_rate/speed are query parameters (see
// buildBaseSpeechRequestURL).
type DeepgramSpeechRequest struct {
	Text string `json:"text"`

	Model      string  `json:"-"`
	Encoding   string  `json:"-"`
	Container  string  `json:"-"`
	SampleRate int     `json:"-"`
	Speed      float64 `json:"-"`

	ExtraParams map[string]interface{} `json:"-"`
}


// GetExtraParams implements the providerUtils.RequestBodyWithExtraParams interface.
func (r *DeepgramSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}


// TRANSCRIPTION TYPES
type DeepgramTranscriptionRequest struct {
	File     []byte `form:"file"`
	Filename string

	Model string `form:"model"`

	// Booleans are pointers so unset values are omitted and Deepgram server-side
	// defaults (e.g. smart_format/punctuate enabled) are preserved.
	SmartFormat    *bool    `form:"smart_format,omitempty"`
	Punctuate      *bool    `form:"punctuate,omitempty"`
	Diarize        *bool    `form:"diarize,omitempty"`
	Paragraphs     *bool    `form:"paragraphs,omitempty"`
	Utterances     *bool    `form:"utterances,omitempty"`
	Numerals       *bool    `form:"numerals,omitempty"`
	DetectLanguage *bool    `form:"detect_language,omitempty"`
	Language       string   `form:"language,omitempty"`
	Keywords       []string `form:"keywords,omitempty"`
	Replace        []string `form:"replace,omitempty"`
	Redact         string   `form:"redact,omitempty"`
	Search         []string `form:"search,omitempty"`
	Summarize      *bool    `form:"summarize,omitempty"`
	Topics         *bool    `form:"topics,omitempty"`
	Intents        *bool    `form:"intents,omitempty"`
	Sentiment      *bool    `form:"sentiment,omitempty"`
}

type DeepgramMetadata struct {
	Duration float64 `json:"duration"`
}

type DeepgramWord struct {
	Word       string  `json:"word"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Confidence float64 `json:"confidence"`
	Speaker    *int    `json:"speaker,omitempty"`
}
type DeepgramAlternative struct {
	Transcript string         `json:"transcript"`
	Confidence float64        `json:"confidence"`
	Words      []DeepgramWord `json:"words"`
}
type DeepgramChannel struct {
	Alternatives []DeepgramAlternative `json:"alternatives"`
}

type DeepgramResults struct {
	Channels []DeepgramChannel `json:"channels"`
}
type DeepgramTranscriptionResponse struct {
	Metadata DeepgramMetadata `json:"metadata"`
	Results  DeepgramResults  `json:"results"`
}


type DeepgramAdditionalFormatResponse struct {
	RequestedFormat string `json:"requested_format"`
	FileExtension   string `json:"file_extension"`
	ContentType     string `json:"content_type"`
	IsBase64Encoded bool   `json:"is_base64_encoded"`
	Content         string `json:"content"`
}


// ERROR TYPES
type DeepgramError struct {
	ErrCode string `json:"err_code,omitempty"`
	ErrMsg  string `json:"err_msg,omitempty"`

	Category string `json:"category,omitempty"`
	Message  string `json:"message,omitempty"`
	Details  string `json:"details,omitempty"`

	RequestID string `json:"request_id,omitempty"`
}

// MODEL TYPES
type DeepgramModel struct {
	Name          string   `json:"name"`
	CanonicalName string   `json:"canonical_name"`
	Architecture  string   `json:"architecture"`
	Languages     []string `json:"languages"`
	Version       string   `json:"version"`
	UUID          string   `json:"uuid"`

	// STT only
	Batch           bool `json:"batch,omitempty"`
	Streaming       bool `json:"streaming,omitempty"`
	FormattedOutput bool `json:"formatted_output,omitempty"`

	// TTS only
	Metadata *DeepgramModelMetadata `json:"metadata,omitempty"`
}

type DeepgramModelMetadata struct {
	Accent   string   `json:"accent"`
	Age      string   `json:"age"`
	Color    string   `json:"color"`
	Image    string   `json:"image"`
	Sample   string   `json:"sample"`
	Tags     []string `json:"tags"`
	UseCases []string `json:"use_cases"`
}

type DeepgramListModelsResponse struct {
	STT []DeepgramModel `json:"stt"`
	TTS []DeepgramModel `json:"tts"`
}
