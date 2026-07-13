package cartesia

// SPEECH TYPES

// CartesiaSpeechRequest is the request body for Cartesia's /tts/bytes and /tts/sse endpoints.
type CartesiaSpeechRequest struct {
	ModelID          string                    `json:"model_id"`
	Transcript       string                    `json:"transcript"`
	Voice            CartesiaVoice             `json:"voice"`
	OutputFormat     CartesiaOutputFormat      `json:"output_format"`
	Language         *string                   `json:"language,omitempty"`
	AddTimestamps    *bool                     `json:"add_timestamps,omitempty"`
	GenerationConfig *CartesiaGenerationConfig `json:"generation_config,omitempty"`
	ExtraParams      map[string]interface{}    `json:"-"`
}

// GetExtraParams implements the providerUtils.RequestBodyWithExtraParams interface.
func (r *CartesiaSpeechRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// CartesiaVoice identifies the voice to synthesize with. Only "id" mode is supported.
type CartesiaVoice struct {
	Mode string `json:"mode"` // "id"
	ID   string `json:"id"`   // voice UUID
}

// CartesiaOutputFormat describes the audio container/encoding/sample rate.
// Encoding is omitted for the "mp3" container; BitRate applies to "mp3" only.
type CartesiaOutputFormat struct {
	Container  string  `json:"container"` // "mp3" | "wav" | "raw"
	Encoding   *string `json:"encoding,omitempty"`
	SampleRate int     `json:"sample_rate"`
	BitRate    *int    `json:"bit_rate,omitempty"`
}

// CartesiaGenerationConfig carries generation tuning options (e.g. speed).
type CartesiaGenerationConfig struct {
	Speed *float64 `json:"speed,omitempty"`
}

// STREAM TYPES

// CartesiaSSEEvent is a single event emitted on the /tts/sse stream.
// For "chunk" events, Data holds base64-encoded raw PCM audio.
// For "error" events, the structured error fields (Title/Message/ErrorCode/
// RequestID) mirror Cartesia's HTTP error body (see CartesiaError).
type CartesiaSSEEvent struct {
	Type       string  `json:"type"` // "chunk" | "done" | "error"
	Done       bool    `json:"done"`
	StatusCode int     `json:"status_code,omitempty"`
	StepTime   float64 `json:"step_time,omitempty"`
	ContextID  string  `json:"context_id,omitempty"`
	Data       string  `json:"data,omitempty"` // base64 raw PCM (chunk events)
	// Error-event fields (Cartesia-Version 2026-03-01+).
	Title     string `json:"title,omitempty"`
	Message   string `json:"message,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	DocURL    string `json:"doc_url,omitempty"`
	Error     string `json:"error,omitempty"` // legacy/plain-text fallback
}

// ERROR TYPES

// CartesiaError models Cartesia's structured JSON error body for
// Cartesia-Version 2026-03-01 and newer (see https://docs.cartesia.ai/use-the-api/api-errors):
//
//	{"error_code": "...", "title": "...", "message": "...", "request_id": "...", "doc_url": "..."}
//
// Error is retained as a fallback for older/plain-text error responses.
// parseCartesiaError falls back to the generic status-code message when none of
// these fields are populated.
type CartesiaError struct {
	ErrorCode string `json:"error_code,omitempty"`
	Title     string `json:"title,omitempty"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	DocURL    string `json:"doc_url,omitempty"`
	Error     string `json:"error,omitempty"` // legacy/plain-text fallback
}
