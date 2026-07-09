package sarvam

// SarvamError is Sarvam's error envelope, structurally close to but distinct
// from OpenAI's (uses "code" instead of "type", and carries a request_id).
// Used by ListModels (a direct request, not delegated to the shared openai
// adapter's own error parsing).
type SarvamError struct {
	Error *SarvamErrorDetail `json:"error"`
}

type SarvamErrorDetail struct {
	RequestID *string `json:"request_id"`
	Message   string  `json:"message"`
	Code      string  `json:"code"`
}
