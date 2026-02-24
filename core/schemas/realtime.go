package schemas

import "encoding/json"

// RealtimeEventType represents the type of a Bifrost unified Realtime event.
type RealtimeEventType string

// Client-to-server event types (sent by the client through Bifrost)
const (
	RTEventSessionUpdate          RealtimeEventType = "session.update"
	RTEventConversationItemCreate RealtimeEventType = "conversation.item.create"
	RTEventConversationItemDelete RealtimeEventType = "conversation.item.delete"
	RTEventResponseCreate         RealtimeEventType = "response.create"
	RTEventResponseCancel         RealtimeEventType = "response.cancel"
	RTEventInputAudioAppend       RealtimeEventType = "input_audio_buffer.append"
	RTEventInputAudioCommit       RealtimeEventType = "input_audio_buffer.commit"
	RTEventInputAudioClear        RealtimeEventType = "input_audio_buffer.clear"
)

// Server-to-client event types (received from the provider, forwarded to client)
const (
	RTEventSessionCreated             RealtimeEventType = "session.created"
	RTEventSessionUpdated             RealtimeEventType = "session.updated"
	RTEventConversationCreated        RealtimeEventType = "conversation.created"
	RTEventConversationItemCreated    RealtimeEventType = "conversation.item.created"
	RTEventConversationItemDone       RealtimeEventType = "conversation.item.done"
	RTEventResponseCreated            RealtimeEventType = "response.created"
	RTEventResponseDone               RealtimeEventType = "response.done"
	RTEventResponseTextDelta          RealtimeEventType = "response.text.delta"
	RTEventResponseTextDone           RealtimeEventType = "response.text.done"
	RTEventResponseAudioDelta         RealtimeEventType = "response.audio.delta"
	RTEventResponseAudioDone          RealtimeEventType = "response.audio.done"
	RTEventResponseAudioTransDelta    RealtimeEventType = "response.audio_transcript.delta"
	RTEventResponseAudioTransDone     RealtimeEventType = "response.audio_transcript.done"
	RTEventResponseOutputItemAdded    RealtimeEventType = "response.output_item.added"
	RTEventResponseOutputItemDone     RealtimeEventType = "response.output_item.done"
	RTEventResponseContentPartAdded   RealtimeEventType = "response.content_part.added"
	RTEventResponseContentPartDone    RealtimeEventType = "response.content_part.done"
	RTEventInputAudioTransCompleted   RealtimeEventType = "conversation.item.input_audio_transcription.completed"
	RTEventInputAudioTransDelta       RealtimeEventType = "conversation.item.input_audio_transcription.delta"
	RTEventInputAudioTransFailed      RealtimeEventType = "conversation.item.input_audio_transcription.failed"
	RTEventInputAudioBufferCommitted  RealtimeEventType = "input_audio_buffer.committed"
	RTEventInputAudioBufferCleared    RealtimeEventType = "input_audio_buffer.cleared"
	RTEventInputAudioSpeechStarted    RealtimeEventType = "input_audio_buffer.speech_started"
	RTEventInputAudioSpeechStopped    RealtimeEventType = "input_audio_buffer.speech_stopped"
	RTEventError                      RealtimeEventType = "error"
)

// BifrostRealtimeEvent is the unified Bifrost envelope for all Realtime events.
// Provider converters translate between this format and the provider-native protocol.
type BifrostRealtimeEvent struct {
	Type    RealtimeEventType `json:"type"`
	EventID string            `json:"event_id,omitempty"`

	Session *RealtimeSession `json:"session,omitempty"`
	Item    *RealtimeItem    `json:"item,omitempty"`
	Delta   *RealtimeDelta   `json:"delta,omitempty"`
	Audio   []byte           `json:"audio,omitempty"`
	Error   *RealtimeError   `json:"error,omitempty"`

	// RawData preserves the original provider event for pass-through or debugging.
	RawData json.RawMessage `json:"raw_data,omitempty"`
}

// RealtimeSession describes session configuration for the Realtime connection.
type RealtimeSession struct {
	ID               string          `json:"id,omitempty"`
	Model            string          `json:"model,omitempty"`
	Modalities       []string        `json:"modalities,omitempty"`
	Instructions     string          `json:"instructions,omitempty"`
	Voice            string          `json:"voice,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	MaxOutputTokens  json.RawMessage `json:"max_output_tokens,omitempty"`
	TurnDetection    json.RawMessage `json:"turn_detection,omitempty"`
	InputAudioFormat string          `json:"input_audio_format,omitempty"`
	OutputAudioType  string          `json:"output_audio_type,omitempty"`
	Tools            json.RawMessage `json:"tools,omitempty"`
}

// RealtimeItem represents a conversation item in the Realtime protocol.
type RealtimeItem struct {
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Role     string          `json:"role,omitempty"`
	Status   string          `json:"status,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	Name     string          `json:"name,omitempty"`
	CallID   string          `json:"call_id,omitempty"`
	Arguments string         `json:"arguments,omitempty"`
	Output   string          `json:"output,omitempty"`
}

// RealtimeDelta carries incremental content for streaming events.
type RealtimeDelta struct {
	Text       string `json:"text,omitempty"`
	Audio      string `json:"audio,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	ItemID     string `json:"item_id,omitempty"`
	OutputIdx  int    `json:"output_index,omitempty"`
	ContentIdx int    `json:"content_index,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
}

// RealtimeError describes an error from the Realtime API.
type RealtimeError struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Param   string `json:"param,omitempty"`
}

// RealtimeProvider is an optional interface that providers can implement to
// indicate support for bidirectional Realtime API (audio/text streaming).
// Checked via type assertion: provider.(RealtimeProvider).
type RealtimeProvider interface {
	SupportsRealtimeAPI() bool
	RealtimeWebSocketURL(key Key, model string) string
	RealtimeHeaders(key Key) map[string]string
	ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*BifrostRealtimeEvent, error)
	ToProviderRealtimeEvent(bifrostEvent *BifrostRealtimeEvent) (json.RawMessage, error)
}
