package deepgram

import "encoding/json"

// TRANSCRIPTION (LISTEN) TYPES

// DeepgramTranscribeURLRequest is the JSON body Deepgram expects on /v1/listen
// when the client supplies a URL instead of raw audio bytes.
type DeepgramTranscribeURLRequest struct {
	URL string `json:"url"`
}

// DeepgramTranscriptionResponse mirrors Deepgram's /v1/listen pre-recorded
// response shape (results.channels[].alternatives[].{transcript,confidence,words}).
// Analytics sidecars (sentiment/summary/topics/intents/entities) have no home in
// Bifrost's unified transcription schema yet, so they're kept as raw JSON for
// passthrough via ExtraFields rather than dropped.
type DeepgramTranscriptionResponse struct {
	Metadata *DeepgramTranscriptionMetadata `json:"metadata,omitempty"`
	Results  *DeepgramTranscriptionResults  `json:"results,omitempty"`
}

type DeepgramTranscriptionMetadata struct {
	RequestID string   `json:"request_id,omitempty"`
	Duration  *float64 `json:"duration,omitempty"`
	Channels  *int     `json:"channels,omitempty"`
}

type DeepgramTranscriptionResults struct {
	Channels []DeepgramTranscriptionChannel `json:"channels,omitempty"`

	// Analytics sidecars, kept as raw JSON (no unified-schema field exists for these).
	Utterances json.RawMessage `json:"utterances,omitempty"`
	Summary    json.RawMessage `json:"summary,omitempty"`
	Sentiments json.RawMessage `json:"sentiments,omitempty"`
	Topics     json.RawMessage `json:"topics,omitempty"`
	Intents    json.RawMessage `json:"intents,omitempty"`
}

type DeepgramTranscriptionChannel struct {
	Alternatives     []DeepgramTranscriptionAlternative `json:"alternatives,omitempty"`
	DetectedLanguage *string                            `json:"detected_language,omitempty"`
}

type DeepgramTranscriptionAlternative struct {
	Transcript string                      `json:"transcript"`
	Confidence float64                     `json:"confidence,omitempty"`
	Words      []DeepgramTranscriptionWord `json:"words,omitempty"`
	Entities   json.RawMessage             `json:"entities,omitempty"`
}

type DeepgramTranscriptionWord struct {
	Word           string  `json:"word"`
	Start          float64 `json:"start"`
	End            float64 `json:"end"`
	Confidence     float64 `json:"confidence,omitempty"`
	PunctuatedWord *string `json:"punctuated_word,omitempty"`
	Speaker        *int    `json:"speaker,omitempty"`
}

// DeepgramErrorResponse mirrors Deepgram's REST error body shape
// ({"err_code": "...", "err_msg": "...", "request_id": "..."}).
type DeepgramErrorResponse struct {
	ErrCode   *string `json:"err_code,omitempty"`
	ErrMsg    *string `json:"err_msg,omitempty"`
	RequestID *string `json:"request_id,omitempty"`
}

// SPEAK TYPES

// DeepgramSpeakRequest is the JSON body for POST /v1/speak.
type DeepgramSpeakRequest struct {
	Text string `json:"text"`
}
