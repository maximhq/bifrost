package elevenlabs

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
)

// Scribe realtime message types, the message_type of a server frame.
const (
	scribePartialTranscript   = "partial_transcript"
	scribeCommittedTranscript = "committed_transcript"
	scribeWordsFrame          = "committed_transcript_with_timestamps"
	scribeEntitiesFrame       = "committed_transcript_entities"
	scribeRedactedFrame       = "redacted_transcript"
	scribeEditedFrame         = "edited_transcript"
	scribeInputAudioChunk     = "input_audio_chunk"
)

// The Bifrost extension events carry what a transcription provider reports that OpenAI Realtime has no
// event for. They have the same shape whichever provider sent the data, and a session receives them only
// when it sets session.bifrost.extensions.
const (
	bifrostEventWords         = "bifrost.transcription.words"
	bifrostEventEntities      = "bifrost.transcription.entities"
	bifrostEventRedacted      = "bifrost.transcription.redacted"
	bifrostEventEdited        = "bifrost.transcription.edited"
	bifrostEventProviderEvent = "bifrost.provider_event"
)

// scribeErrorTypes maps the error frames Scribe sends to the OpenAI error type the client sees. A frame
// not listed here is not an error.
var scribeErrorTypes = map[string]string{
	"error":                       "server_error",
	"transcriber_error":           "server_error",
	"auth_error":                  "authentication_error",
	"unaccepted_terms":            "authentication_error",
	"quota_exceeded":              "rate_limit_exceeded",
	"rate_limited":                "rate_limit_exceeded",
	"commit_throttled":            "rate_limit_exceeded",
	"queue_overflow":              "rate_limit_exceeded",
	"resource_exhausted":          "rate_limit_exceeded",
	"invalid_request":             "invalid_request_error",
	"input_error":                 "invalid_request_error",
	"chunk_size_exceeded":         "invalid_request_error",
	"insufficient_audio_activity": "invalid_request_error",
	"session_time_limit_exceeded": "invalid_request_error",
}

// scribeFrame holds the fields of a server frame the translator reads. A frame it has no neutral event for
// is relayed whole inside a bifrost.provider_event.
type scribeFrame struct {
	MessageType     string             `json:"message_type"`
	Text            string             `json:"text"`
	Error           string             `json:"error"`
	TurnProbability *float64           `json:"turn_probability"`
	LanguageCode    *string            `json:"language_code"`
	Words           []scribeWireWord   `json:"words"`
	Entities        []scribeWireEntity `json:"entities"`
	RedactedText    string             `json:"redacted_text"`
	EditedText      string             `json:"edited_text"`
}

// scribeWireWord is one entry of a committed_transcript_with_timestamps frame. Its spacing entries and
// per-character timings are not forwarded.
type scribeWireWord struct {
	Text      string   `json:"text"`
	Start     float64  `json:"start"`
	End       float64  `json:"end"`
	Type      string   `json:"type"`
	SpeakerID *string  `json:"speaker_id"`
	LogProb   *float64 `json:"logprob"`
}

// scribeWireEntity is one entry of a committed_transcript_entities frame.
type scribeWireEntity struct {
	Text       string `json:"text"`
	EntityType string `json:"entity_type"`
	StartChar  int    `json:"start_char"`
	EndChar    int    `json:"end_char"`
}

// scribeAudioChunk is the one frame a client sends to Scribe.
type scribeAudioChunk struct {
	MessageType string `json:"message_type"`
	AudioBase64 string `json:"audio_base_64"`
	Commit      bool   `json:"commit"`
	SampleRate  int    `json:"sample_rate"`
}

// scribeEvent is one OpenAI Realtime server event of a transcription session. The events share one struct,
// as they do for Gemini Live. Fields where zero is a real value are pointers.
type scribeEvent struct {
	Type            string         `json:"type"`
	EventID         string         `json:"event_id"`
	Session         *scribeSession `json:"session,omitempty"`
	Item            *scribeItem    `json:"item,omitempty"`
	Error           *scribeError   `json:"error,omitempty"`
	ItemID          string         `json:"item_id,omitempty"`
	PreviousItemID  string         `json:"previous_item_id,omitempty"`
	ContentIndex    *int           `json:"content_index,omitempty"`
	Delta           *string        `json:"delta,omitempty"`
	Transcript      *string        `json:"transcript,omitempty"`
	TurnProbability *float64       `json:"turn_probability,omitempty"`
	Usage           *scribeUsage   `json:"usage,omitempty"`
}

// scribeUsage is the duration usage a transcription turn reports; Bifrost bills it per audio second.
type scribeUsage struct {
	Type    string  `json:"type"`
	Seconds float64 `json:"seconds"`
}

type scribeError struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type scribeItem struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Type    string       `json:"type"`
	Status  string       `json:"status"`
	Role    string       `json:"role"`
	Content []scribePart `json:"content"`
}

type scribePart struct {
	Type       string  `json:"type"`
	Transcript *string `json:"transcript,omitempty"`
}

// scribeSession is the session object of session.created and session.updated.
type scribeSession struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Type    string                `json:"type"`
	Audio   scribeSessionIO       `json:"audio"`
	Bifrost *scribeBifrostOptions `json:"bifrost,omitempty"`
}

// scribeBifrostOptions echoes the Bifrost options a session has set.
type scribeBifrostOptions struct {
	Extensions bool `json:"extensions"`
}

type scribeSessionIO struct {
	Input scribeSessionInput `json:"input"`
}

type scribeSessionInput struct {
	Format        scribeAudioFormat    `json:"format"`
	Transcription scribeTranscription  `json:"transcription"`
	TurnDetection *scribeTurnDetection `json:"turn_detection"`
}

// scribeExtensionHeader starts every Bifrost extension event. item_id names the segment the event
// describes, which is how a client joins it to that segment's completed event: these events follow it.
type scribeExtensionHeader struct {
	Type    string `json:"type"`
	EventID string `json:"event_id"`
	ItemID  string `json:"item_id,omitempty"`
}

// scribeWord is one word of a bifrost.transcription.words event. Times are seconds from the start of the
// audio sent, and confidence is 0 to 1.
type scribeWord struct {
	Text       string   `json:"text"`
	Start      float64  `json:"start"`
	End        float64  `json:"end"`
	Confidence *float64 `json:"confidence,omitempty"`
	Speaker    string   `json:"speaker,omitempty"`
}

// scribeEntity is one entity of a bifrost.transcription.entities event; start_char and end_char are
// character offsets into the segment's transcript.
type scribeEntity struct {
	Text      string `json:"text"`
	Type      string `json:"type"`
	StartChar int    `json:"start_char"`
	EndChar   int    `json:"end_char"`
}

type scribeWordsEvent struct {
	scribeExtensionHeader
	Language string       `json:"language,omitempty"`
	Words    []scribeWord `json:"words"`
}

type scribeEntitiesEvent struct {
	scribeExtensionHeader
	Entities []scribeEntity `json:"entities"`
}

type scribeRedactedEvent struct {
	scribeExtensionHeader
	RedactedText string `json:"redacted_text"`
}

type scribeEditedEvent struct {
	scribeExtensionHeader
	EditedText string `json:"edited_text"`
}

// scribeProviderEvent carries a provider frame that has no neutral event, as the provider sent it.
type scribeProviderEvent struct {
	scribeExtensionHeader
	Provider string          `json:"provider"`
	Event    json.RawMessage `json:"event"`
}

// scribeOutput collects a translator step's frames.
type scribeOutput struct {
	schemas.RealtimeTranslation
	err error
}

// marshal encodes a frame, or returns nil and records the error.
func (o *scribeOutput) marshal(value any) json.RawMessage {
	frame, err := schemas.MarshalSorted(value)
	if err != nil {
		if o.err == nil {
			o.err = err
		}
		return nil
	}
	return frame
}

// client queues an event for the client: an OpenAI Realtime event or a Bifrost extension event.
func (o *scribeOutput) client(event any) {
	if frame := o.marshal(event); frame != nil {
		o.Client = append(o.Client, frame)
	}
}
