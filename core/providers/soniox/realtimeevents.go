package soniox

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// The tokens Soniox adds to its stream to mark where a segment ends: <end> when its endpoint detection ends
// an utterance, and <fin> when it answers a finalize message.
const (
	sonioxEndToken      = "<end>"
	sonioxFinalizeToken = "<fin>"
)

// bifrostEventWords is the Bifrost extension event that carries the words of a completed segment. It has the
// same shape whichever provider sent the data, and a session receives it only when it sets
// session.bifrost.extensions.
const bifrostEventWords = "bifrost.transcription.words"

// sonioxFinalizeFrame asks Soniox to make every token it holds final and to answer with a <fin> token.
var sonioxFinalizeFrame = []byte(`{"type":"finalize"}`)

// sonioxFrame holds the fields of a server frame the translator reads. A frame carries either tokens or an
// error, and always how much audio Soniox has processed.
type sonioxFrame struct {
	Tokens           []sonioxStreamToken `json:"tokens"`
	TotalAudioProcMs int                 `json:"total_audio_proc_ms"`
	ErrorCode        int                 `json:"error_code"`
	ErrorType        string              `json:"error_type"`
	ErrorMessage     string              `json:"error_message"`
}

// sonioxStreamToken is one token of a server frame. A final token is sent once; the non-final tokens of a
// frame replace the ones of the frame before.
type sonioxStreamToken struct {
	Text     string  `json:"text"`
	StartMs  int     `json:"start_ms"`
	EndMs    int     `json:"end_ms"`
	IsFinal  bool    `json:"is_final"`
	Speaker  *string `json:"speaker"`
	Language *string `json:"language"`
}

// sonioxErrorType maps the code of an error frame to the OpenAI error type the client sees.
func sonioxErrorType(code int) string {
	switch {
	case code == 401 || code == 403:
		return "authentication_error"
	case code == 402:
		return "insufficient_quota"
	case code == 429:
		return "rate_limit_exceeded"
	case code >= 400 && code < 500:
		return "invalid_request_error"
	default:
		return "server_error"
	}
}

// sonioxEvent is one OpenAI Realtime server event of a transcription session. The events share one struct, as
// they do for Gemini Live and Scribe. Fields where zero is a real value are pointers.
type sonioxEvent struct {
	Type           string         `json:"type"`
	EventID        string         `json:"event_id"`
	Session        *sonioxSession `json:"session,omitempty"`
	Item           *sonioxItem    `json:"item,omitempty"`
	Error          *sonioxError   `json:"error,omitempty"`
	ItemID         string         `json:"item_id,omitempty"`
	PreviousItemID string         `json:"previous_item_id,omitempty"`
	ContentIndex   *int           `json:"content_index,omitempty"`
	Delta          *string        `json:"delta,omitempty"`
	Transcript     *string        `json:"transcript,omitempty"`
	Usage          *sonioxUsage   `json:"usage,omitempty"`
}

// sonioxUsage is the duration usage a transcription turn reports; Bifrost bills it per audio second.
type sonioxUsage struct {
	Type    string  `json:"type"`
	Seconds float64 `json:"seconds"`
}

type sonioxError struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type sonioxItem struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Type    string       `json:"type"`
	Status  string       `json:"status"`
	Role    string       `json:"role"`
	Content []sonioxPart `json:"content"`
}

type sonioxPart struct {
	Type       string  `json:"type"`
	Transcript *string `json:"transcript,omitempty"`
}

// sonioxExtensionHeader starts every Bifrost extension event. item_id names the segment the event describes,
// which is how a client joins it to that segment's completed event: these events follow it.
type sonioxExtensionHeader struct {
	Type    string `json:"type"`
	EventID string `json:"event_id"`
	ItemID  string `json:"item_id,omitempty"`
}

// sonioxWord is one word of a bifrost.transcription.words event. Times are seconds from the start of the
// audio sent.
type sonioxWord struct {
	Text    string  `json:"text"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
}

type sonioxWordsEvent struct {
	sonioxExtensionHeader
	Language string       `json:"language,omitempty"`
	Words    []sonioxWord `json:"words"`
}

// sonioxOutput collects a translator step's frames.
type sonioxOutput struct {
	schemas.RealtimeTranslation
	err error
}

// client queues an event for the client: an OpenAI Realtime event or a Bifrost extension event.
func (o *sonioxOutput) client(event any) {
	frame, err := schemas.MarshalSorted(event)
	if err != nil {
		if o.err == nil {
			o.err = err
		}
		return
	}
	o.Client = append(o.Client, frame)
}
