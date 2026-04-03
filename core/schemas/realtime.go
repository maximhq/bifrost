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
	RTEventSessionCreated            RealtimeEventType = "session.created"
	RTEventSessionUpdated            RealtimeEventType = "session.updated"
	RTEventConversationCreated       RealtimeEventType = "conversation.created"
	RTEventConversationItemAdded     RealtimeEventType = "conversation.item.added"
	RTEventConversationItemCreated   RealtimeEventType = "conversation.item.created"
	RTEventConversationItemRetrieved RealtimeEventType = "conversation.item.retrieved"
	RTEventConversationItemDone      RealtimeEventType = "conversation.item.done"
	RTEventResponseCreated           RealtimeEventType = "response.created"
	RTEventResponseDone              RealtimeEventType = "response.done"
	RTEventResponseTextDelta         RealtimeEventType = "response.text.delta"
	RTEventResponseTextDone          RealtimeEventType = "response.text.done"
	RTEventResponseAudioDelta        RealtimeEventType = "response.audio.delta"
	RTEventResponseAudioDone         RealtimeEventType = "response.audio.done"
	RTEventResponseAudioTransDelta   RealtimeEventType = "response.audio_transcript.delta"
	RTEventResponseAudioTransDone    RealtimeEventType = "response.audio_transcript.done"
	RTEventResponseOutputItemAdded   RealtimeEventType = "response.output_item.added"
	RTEventResponseOutputItemDone    RealtimeEventType = "response.output_item.done"
	RTEventResponseContentPartAdded  RealtimeEventType = "response.content_part.added"
	RTEventResponseContentPartDone   RealtimeEventType = "response.content_part.done"
	RTEventRateLimitsUpdated         RealtimeEventType = "rate_limits.updated"
	RTEventInputAudioTransCompleted  RealtimeEventType = "conversation.item.input_audio_transcription.completed"
	RTEventInputAudioTransDelta      RealtimeEventType = "conversation.item.input_audio_transcription.delta"
	RTEventInputAudioTransFailed     RealtimeEventType = "conversation.item.input_audio_transcription.failed"
	RTEventInputAudioBufferCommitted RealtimeEventType = "input_audio_buffer.committed"
	RTEventInputAudioBufferCleared   RealtimeEventType = "input_audio_buffer.cleared"
	RTEventInputAudioSpeechStarted   RealtimeEventType = "input_audio_buffer.speech_started"
	RTEventInputAudioSpeechStopped   RealtimeEventType = "input_audio_buffer.speech_stopped"
	RTEventError                     RealtimeEventType = "error"
)

// IsRealtimeConversationItemEventType reports whether the event carries a
// canonical conversation item payload after provider translation.
func IsRealtimeConversationItemEventType(eventType RealtimeEventType) bool {
	switch eventType {
	case RTEventConversationItemCreate,
		RTEventConversationItemAdded,
		RTEventConversationItemCreated,
		RTEventConversationItemRetrieved,
		RTEventConversationItemDone:
		return true
	default:
		return false
	}
}

// IsRealtimeUserInputEvent reports whether the event represents a finalized
// user input item in the canonical Bifrost realtime schema.
func IsRealtimeUserInputEvent(event *BifrostRealtimeEvent) bool {
	return event != nil &&
		event.Item != nil &&
		event.Item.Role == "user" &&
		IsRealtimeConversationItemEventType(event.Type)
}

// IsRealtimeToolOutputEvent reports whether the event represents a finalized
// tool output item in the canonical Bifrost realtime schema.
func IsRealtimeToolOutputEvent(event *BifrostRealtimeEvent) bool {
	return event != nil &&
		event.Item != nil &&
		event.Item.Type == "function_call_output" &&
		IsRealtimeConversationItemEventType(event.Type)
}

// IsRealtimeInputTranscriptEvent reports whether the event carries a finalized
// input-audio transcript in the canonical Bifrost realtime schema.
func IsRealtimeInputTranscriptEvent(event *BifrostRealtimeEvent) bool {
	return event != nil && event.Type == RTEventInputAudioTransCompleted
}

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

	// ExtraParams preserves provider-specific top-level event fields that are not
	// promoted into the common Bifrost schema.
	ExtraParams map[string]json.RawMessage `json:"extra_params,omitempty"`

	// RawData preserves the original provider event for pass-through or debugging.
	RawData json.RawMessage `json:"raw_data,omitempty"`
}

// RealtimeSession describes session configuration for the Realtime connection.
type RealtimeSession struct {
	ID               string                     `json:"id,omitempty"`
	Model            string                     `json:"model,omitempty"`
	Modalities       []string                   `json:"modalities,omitempty"`
	Instructions     string                     `json:"instructions,omitempty"`
	Voice            string                     `json:"voice,omitempty"`
	Temperature      *float64                   `json:"temperature,omitempty"`
	MaxOutputTokens  json.RawMessage            `json:"max_output_tokens,omitempty"`
	TurnDetection    json.RawMessage            `json:"turn_detection,omitempty"`
	InputAudioFormat string                     `json:"input_audio_format,omitempty"`
	OutputAudioType  string                     `json:"output_audio_type,omitempty"`
	Tools            json.RawMessage            `json:"tools,omitempty"`
	ExtraParams      map[string]json.RawMessage `json:"extra_params,omitempty"`
}

// RealtimeItem represents a conversation item in the Realtime protocol.
type RealtimeItem struct {
	ID          string                     `json:"id,omitempty"`
	Type        string                     `json:"type,omitempty"`
	Role        string                     `json:"role,omitempty"`
	Status      string                     `json:"status,omitempty"`
	Content     json.RawMessage            `json:"content,omitempty"`
	Name        string                     `json:"name,omitempty"`
	CallID      string                     `json:"call_id,omitempty"`
	Arguments   string                     `json:"arguments,omitempty"`
	Output      string                     `json:"output,omitempty"`
	ExtraParams map[string]json.RawMessage `json:"extra_params,omitempty"`
}

// RealtimeDelta carries incremental content for streaming events.
type RealtimeDelta struct {
	Text       string `json:"text,omitempty"`
	Audio      string `json:"audio,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	ItemID     string `json:"item_id,omitempty"`
	OutputIdx  *int   `json:"output_index,omitempty"`
	ContentIdx *int   `json:"content_index,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
}

// RealtimeError describes an error from the Realtime API.
type RealtimeError struct {
	Type        string                     `json:"type,omitempty"`
	Code        string                     `json:"code,omitempty"`
	Message     string                     `json:"message,omitempty"`
	Param       string                     `json:"param,omitempty"`
	ExtraParams map[string]json.RawMessage `json:"extra_params,omitempty"`
}

// RealtimeSessionEndpointType identifies the public ephemeral-token endpoint
// shape the client called so providers can preserve versioned behavior.
type RealtimeSessionEndpointType string

const (
	RealtimeSessionEndpointClientSecrets RealtimeSessionEndpointType = "client_secrets"
	RealtimeSessionEndpointSessions      RealtimeSessionEndpointType = "sessions"
)

// RealtimeSessionRoute describes a provider-registered public route for
// ephemeral-token creation.
type RealtimeSessionRoute struct {
	Path            string
	EndpointType    RealtimeSessionEndpointType
	DefaultProvider ModelProvider
}

// RealtimeProvider is an optional interface that providers can implement to
// indicate support for bidirectional Realtime API (audio/text streaming).
// Checked via type assertion: provider.(RealtimeProvider).
type RealtimeProvider interface {
	SupportsRealtimeAPI() bool
	RealtimeWebSocketURL(key Key, model string) string
	RealtimeHeaders(key Key) map[string]string
	// SupportsRealtimeWebRTC reports whether the provider supports WebRTC SDP exchange.
	SupportsRealtimeWebRTC() bool
	// ExchangeRealtimeWebRTCSDP performs the provider-specific SDP signaling exchange.
	// The provider owns the HTTP specifics (URL, headers, body format).
	// session may be nil if the signaling format doesn't include session config.
	ExchangeRealtimeWebRTCSDP(ctx *BifrostContext, key Key, model string, sdp string, session json.RawMessage) (string, *BifrostError)
	ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*BifrostRealtimeEvent, error)
	ToProviderRealtimeEvent(bifrostEvent *BifrostRealtimeEvent) (json.RawMessage, error)
	// ShouldStartRealtimeTurn reports whether the canonical client-side event
	// should start pre-hooks. Providers without an explicit turn-start signal
	// return false and rely on finalize-time fallback hooks.
	ShouldStartRealtimeTurn(event *BifrostRealtimeEvent) bool
	// RealtimeTurnFinalEvent returns the canonical provider event that completes
	// a turn and should trigger post-hooks.
	RealtimeTurnFinalEvent() RealtimeEventType
	RealtimeWebRTCDataChannelLabel() string
	RealtimeWebSocketSubprotocol() string
	ShouldForwardRealtimeEvent(event *BifrostRealtimeEvent) bool
	ShouldAccumulateRealtimeOutput(eventType RealtimeEventType) bool
}

// RealtimeLegacyWebRTCProvider is an optional interface for providers that
// support the beta WebRTC handshake (e.g., OpenAI's /v1/realtime).
// Only checked for legacy integration routes via type assertion.
// Takes SDP offer + optional session JSON, same as ExchangeRealtimeWebRTCSDP
// but targets the provider's legacy/beta endpoint.
type RealtimeLegacyWebRTCProvider interface {
	ExchangeLegacyRealtimeWebRTCSDP(ctx *BifrostContext, key Key, sdp string, session json.RawMessage, model string) (string, *BifrostError)
}

// RealtimeUsageExtractor lets providers parse terminal-turn usage/output from
// their native wire payloads without coupling handlers to a specific protocol.
type RealtimeUsageExtractor interface {
	ExtractRealtimeTurnUsage(terminalEventRaw []byte) *BifrostLLMUsage
	ExtractRealtimeTurnOutput(terminalEventRaw []byte) *ChatMessage
}

// RealtimeSessionProvider is an optional interface for providers that can mint
// short-lived client secrets for browser/client-side Realtime connections.
// Checked via type assertion: provider.(RealtimeSessionProvider).
type RealtimeSessionProvider interface {
	CreateRealtimeClientSecret(ctx *BifrostContext, key Key, endpointType RealtimeSessionEndpointType, rawRequest json.RawMessage) (*BifrostPassthroughResponse, *BifrostError)
}

// ParseRealtimeEvent decodes a client/provider realtime event while preserving
// unknown top-level fields in ExtraParams for provider-specific round-tripping.
func ParseRealtimeEvent(raw []byte) (*BifrostRealtimeEvent, error) {
	type realtimeEventAlias struct {
		Type    RealtimeEventType `json:"type"`
		EventID string            `json:"event_id,omitempty"`
		Session *RealtimeSession  `json:"session,omitempty"`
		Item    *RealtimeItem     `json:"item,omitempty"`
		Delta   *RealtimeDelta    `json:"delta,omitempty"`
		Audio   []byte            `json:"audio,omitempty"`
		Error   *RealtimeError    `json:"error,omitempty"`
	}

	var alias realtimeEventAlias
	if err := json.Unmarshal(raw, &alias); err != nil {
		return nil, err
	}

	event := &BifrostRealtimeEvent{
		Type:    alias.Type,
		EventID: alias.EventID,
		Session: alias.Session,
		Item:    alias.Item,
		Delta:   alias.Delta,
		Audio:   alias.Audio,
		Error:   alias.Error,
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	savedSession := root["session"]
	savedItem := root["item"]
	savedError := root["error"]
	for _, key := range []string{"type", "event_id", "session", "item", "delta", "audio", "error", "raw_data"} {
		delete(root, key)
	}
	if len(root) > 0 {
		event.ExtraParams = root
	}
	if event.Session != nil {
		var sessionRoot map[string]json.RawMessage
		if len(savedSession) > 0 && json.Unmarshal(savedSession, &sessionRoot) == nil {
			for _, key := range []string{
				"id", "model", "modalities", "instructions", "voice", "temperature",
				"max_output_tokens", "turn_detection", "input_audio_format", "output_audio_type", "tools",
			} {
				delete(sessionRoot, key)
			}
			if len(sessionRoot) > 0 {
				event.Session.ExtraParams = sessionRoot
			}
		}
	}
	if event.Item != nil {
		var itemRoot map[string]json.RawMessage
		if len(savedItem) > 0 && json.Unmarshal(savedItem, &itemRoot) == nil {
			for _, key := range []string{
				"id", "type", "role", "status", "content", "name", "call_id", "arguments", "output",
			} {
				delete(itemRoot, key)
			}
			if len(itemRoot) > 0 {
				event.Item.ExtraParams = itemRoot
			}
		}
	}
	if event.Error != nil {
		var errorRoot map[string]json.RawMessage
		if len(savedError) > 0 && json.Unmarshal(savedError, &errorRoot) == nil {
			for _, key := range []string{"type", "code", "message", "param"} {
				delete(errorRoot, key)
			}
			if len(errorRoot) > 0 {
				event.Error.ExtraParams = errorRoot
			}
		}
	}

	return event, nil
}
