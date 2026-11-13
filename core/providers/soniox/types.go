package soniox

// SonioxSpeechRequest is the body of POST /tts.
type SonioxSpeechRequest struct {
	Model             string   `json:"model"`
	Language          string   `json:"language,omitempty"`
	Voice             string   `json:"voice,omitempty"`
	AudioFormat       string   `json:"audio_format"`
	Text              string   `json:"text"`
	SampleRate        *int     `json:"sample_rate,omitempty"`
	Bitrate           *int     `json:"bitrate,omitempty"`
	Speed             *float64 `json:"speed,omitempty"`
	ReduceSilence     *bool    `json:"reduce_silence,omitempty"`
	ClientReferenceID *string  `json:"client_reference_id,omitempty"`

	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams satisfies providerUtils.RequestBodyWithExtraParams.
func (r *SonioxSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// SonioxTTSStreamConfig opens a stream on the TTS WebSocket. Its fields are those of POST /tts without the text.
type SonioxTTSStreamConfig struct {
	StreamID          string   `json:"stream_id"`
	Model             string   `json:"model"`
	Language          string   `json:"language,omitempty"`
	Voice             string   `json:"voice,omitempty"`
	AudioFormat       string   `json:"audio_format"`
	SampleRate        *int     `json:"sample_rate,omitempty"`
	Bitrate           *int     `json:"bitrate,omitempty"`
	Speed             *float64 `json:"speed,omitempty"`
	ReduceSilence     *bool    `json:"reduce_silence,omitempty"`
	ClientReferenceID *string  `json:"client_reference_id,omitempty"`
	ReturnTimestamps  *bool    `json:"return_timestamps,omitempty"`

	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams satisfies providerUtils.RequestBodyWithExtraParams.
func (c *SonioxTTSStreamConfig) GetExtraParams() map[string]interface{} {
	return c.ExtraParams
}

// SonioxTTSText sends text on a stream. Text is never omitted: a message that ends a stream must carry "".
type SonioxTTSText struct {
	StreamID string `json:"stream_id"`
	Text     string `json:"text"`
	TextEnd  bool   `json:"text_end"`
}

// SonioxTTSCancel cancels a stream.
type SonioxTTSCancel struct {
	StreamID string `json:"stream_id"`
	Cancel   bool   `json:"cancel"`
}

// SonioxTTSFrame is a frame of the TTS WebSocket: audio, the end of a stream, or an error.
type SonioxTTSFrame struct {
	SonioxError
	StreamID   string              `json:"stream_id"`
	Audio      string              `json:"audio"` // base64
	AudioEnd   bool                `json:"audio_end"`
	Terminated bool                `json:"terminated"`
	Timestamps SonioxTTSTimestamps `json:"timestamps"`
	MoreInfo   string              `json:"more_info"`
}

// SonioxTTSTimestamps is the character timing of one audio frame, in seconds.
type SonioxTTSTimestamps struct {
	Characters                 []string  `json:"characters"`
	CharacterStartTimesSeconds []float64 `json:"character_start_times_seconds"`
	CharacterEndTimesSeconds   []float64 `json:"character_end_times_seconds"`
}

// SonioxTranscriptionRequest carries the audio and the body of POST /v1/transcriptions.
type SonioxTranscriptionRequest struct {
	Model                        string   `json:"model"`
	FileID                       string   `json:"file_id,omitempty"`
	LanguageHints                []string `json:"language_hints,omitempty"`
	EnableSpeakerDiarization     *bool    `json:"enable_speaker_diarization,omitempty"`
	EnableLanguageIdentification *bool    `json:"enable_language_identification,omitempty"`
	ClientReferenceID            *string  `json:"client_reference_id,omitempty"`

	File        []byte                 `json:"-"` // uploaded first through POST /v1/files
	Filename    string                 `json:"-"`
	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams satisfies providerUtils.RequestBodyWithExtraParams.
func (r *SonioxTranscriptionRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// SonioxFile is the response of POST /v1/files.
type SonioxFile struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// SonioxTranscription is a transcription job, from POST /v1/transcriptions and GET /v1/transcriptions/{id}.
type SonioxTranscription struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"` // queued | processing | completed | error
	Model           string  `json:"model"`
	AudioDurationMs *int    `json:"audio_duration_ms"`
	ErrorType       *string `json:"error_type"`
	ErrorMessage    *string `json:"error_message"`
}

// SonioxTranscript is the response of GET /v1/transcriptions/{id}/transcript.
type SonioxTranscript struct {
	ID     string        `json:"id"`
	Text   string        `json:"text"`
	Tokens []SonioxToken `json:"tokens"`
}

// SonioxToken is one sub-word unit of a transcript.
type SonioxToken struct {
	Text       string  `json:"text"`
	StartMs    int     `json:"start_ms"`
	EndMs      int     `json:"end_ms"`
	Confidence float64 `json:"confidence"`
	Speaker    *string `json:"speaker"`
	Language   *string `json:"language"`
}

// SonioxModel is one entry of GET /v1/models or GET /v1/tts-models.
type SonioxModel struct {
	ID             string  `json:"id"`
	AliasedModelID *string `json:"aliased_model_id"`
	Name           string  `json:"name"`
}

// SonioxListModelsResponse is the response of GET /v1/models and GET /v1/tts-models.
type SonioxListModelsResponse struct {
	Models []SonioxModel `json:"models"`
}

// SonioxError covers both error shapes: the REST API uses status_code/message and the
// TTS host uses error_code/error_message.
type SonioxError struct {
	StatusCode       int                     `json:"status_code"`
	ErrorCode        int                     `json:"error_code"`
	ErrorType        string                  `json:"error_type"`
	Message          string                  `json:"message"`
	ErrorMessage     string                  `json:"error_message"`
	ValidationErrors []SonioxValidationError `json:"validation_errors"`
	RequestID        string                  `json:"request_id"`
}

// SonioxValidationError is one entry of a REST validation failure.
type SonioxValidationError struct {
	ErrorType string `json:"error_type"`
	Location  string `json:"location"`
	Message   string `json:"message"`
}
