package schemas

import (
	"fmt"

	"github.com/tidwall/gjson"
)

// LiveEventType is the type of an OpenAI GPT Live event.
type LiveEventType string

// Client-to-server event types.
const (
	LiveEventSessionStart       LiveEventType = "session.start"
	LiveEventSessionUpdate      LiveEventType = "session.update"
	LiveEventSessionClose       LiveEventType = "session.close"
	LiveEventInputAudioAppend   LiveEventType = "session.input_audio.append" // also reflected to sidebands as a server event
	LiveEventInputAudioMute     LiveEventType = "session.input_audio.mute"
	LiveEventInputAudioUnmute   LiveEventType = "session.input_audio.unmute"
	LiveEventInstructionsAppend LiveEventType = "session.instructions.append"
	LiveEventThinkingAppend     LiveEventType = "session.thinking.append"
	LiveEventCommentaryAppend   LiveEventType = "session.commentary.append"
	LiveEventResponseItemCreate LiveEventType = "response.item.create"
	LiveEventResponseCreate     LiveEventType = "response.create"
)

// Server-to-client event types.
const (
	LiveEventSessionStarted        LiveEventType = "session.started"
	LiveEventSessionUpdated        LiveEventType = "session.updated"
	LiveEventSessionClosed         LiveEventType = "session.closed"
	LiveEventInputAudioMuted       LiveEventType = "session.input_audio.muted"
	LiveEventInputAudioUnmuted     LiveEventType = "session.input_audio.unmuted"
	LiveEventInstructionsAppended  LiveEventType = "session.instructions.appended"
	LiveEventThinkingAppended      LiveEventType = "session.thinking.appended"
	LiveEventCommentaryAppended    LiveEventType = "session.commentary.appended"
	LiveEventOutputAudioDelta      LiveEventType = "session.output_audio.delta"
	LiveEventInputTranscriptDelta  LiveEventType = "session.input_transcript.delta"
	LiveEventOutputTranscriptDelta LiveEventType = "session.output_transcript.delta"
	LiveEventDelegationCreated     LiveEventType = "session.delegation.created"
	LiveEventUsageUpdated          LiveEventType = "session.usage.updated"
	LiveEventResponseEvent         LiveEventType = "response.event"
	LiveEventError                 LiveEventType = "error"
	LiveEventInfo                  LiveEventType = "info"
	LiveEventTransportDTMFReceived LiveEventType = "transport.dtmf.received"
	LiveEventTransportDTMFSend     LiveEventType = "transport.dtmf.send"
	LiveEventTransportRinging      LiveEventType = "transport.ringing"
	LiveEventTransportAnswered     LiveEventType = "transport.answered"
	LiveEventTransportFailed       LiveEventType = "transport.failed"
)

// LiveEventTypeOf reads an event's type without decoding the frame.
func LiveEventTypeOf(raw []byte) LiveEventType {
	return LiveEventType(gjson.GetBytes(raw, "type").Str)
}

// LiveDelegationType names who handles work delegated by the Live model.
type LiveDelegationType string

const (
	LiveDelegationClient    LiveDelegationType = "client"
	LiveDelegationResponses LiveDelegationType = "responses"
)

// LiveCloseReason is why a Live session ended.
type LiveCloseReason string

const (
	LiveCloseRequested      LiveCloseReason = "close_requested"
	LiveCloseExpired        LiveCloseReason = "expired"
	LiveCloseContent        LiveCloseReason = "content"
	LiveCloseRemoteHangup   LiveCloseReason = "remote_hangup"
	LiveCloseConnectionLost LiveCloseReason = "connection_lost"
)

// BifrostLiveEvent is a decoded Live control event.
// Frames are forwarded as RawData; this is a read view, not a re-serialization source.
type BifrostLiveEvent struct {
	Type          LiveEventType `json:"type"`
	EventID       string        `json:"event_id,omitempty"`
	ClientEventID string        `json:"client_event_id,omitempty"`

	Session   *LiveSession `json:"session,omitempty"`
	SessionID string       `json:"session_id,omitempty"` // transport.* events

	// session.*.append commands
	Content      *string `json:"content,omitempty"`
	DelegationID *string `json:"delegation_id,omitempty"` // also on response.event

	// Transcript text; audio deltas share this key, so audio frames are never decoded.
	Delta string `json:"delta,omitempty"`

	// Session-timeline positions in milliseconds.
	StartMs  *int64 `json:"start_ms,omitempty"`
	EndMs    *int64 `json:"end_ms,omitempty"`
	OffsetMs *int64 `json:"offset_ms,omitempty"`

	Delegation *LiveDelegation   `json:"delegation,omitempty"` // session.delegation.created
	Event      *LiveEventPayload `json:"event,omitempty"`      // response.event, transport.dtmf.*
	Item       *ResponsesMessage `json:"item,omitempty"`       // response.item.create

	Usage         *LiveSessionUsage  `json:"usage,omitempty"`
	ContextWindow *LiveContextWindow `json:"context_window,omitempty"`
	Reason        LiveCloseReason    `json:"reason,omitempty"` // session.closed

	Error *LiveError `json:"error,omitempty"`

	// info events
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`

	RawData []byte `json:"-"`
}

// LiveSession is the session object on session.start, session.update and the
// server snapshots. input and client are not decoded; they travel in the raw frame.
type LiveSession struct {
	ID        string `json:"id,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // unix seconds
	Status    string `json:"status,omitempty"`
	Type      string `json:"type,omitempty"` // "live" on the SIP accept body

	Model        string                `json:"model,omitempty"`
	Instructions *string               `json:"instructions,omitempty"`
	Audio        *LiveAudio            `json:"audio,omitempty"`
	Delegation   *LiveDelegationConfig `json:"delegation,omitempty"`
	Store        *bool                 `json:"store,omitempty"`
}

type LiveAudio struct {
	Format *LiveAudioFormat `json:"format,omitempty"`
	Output *LiveAudioOutput `json:"output,omitempty"`
}

// LiveAudioFormat is the WebSocket audio encoding: audio/pcm, audio/pcmu or audio/pcma.
type LiveAudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type LiveAudioOutput struct {
	Voice *LiveVoice `json:"voice,omitempty"`
}

// LiveVoice is a built-in voice name or a custom voice object.
type LiveVoice struct {
	Name   *string
	Custom *LiveCustomVoice
}

type LiveCustomVoice struct {
	ID string `json:"id"`
}

func (v LiveVoice) MarshalJSON() ([]byte, error) {
	if v.Name != nil && v.Custom != nil {
		return nil, fmt.Errorf("both Name and Custom are set; only one should be non-nil")
	}
	if v.Name != nil {
		return MarshalSorted(v.Name)
	}
	if v.Custom != nil {
		return MarshalSorted(v.Custom)
	}
	return MarshalSorted(nil)
}

func (v *LiveVoice) UnmarshalJSON(data []byte) error {
	var name string
	if err := Unmarshal(data, &name); err == nil {
		v.Name = &name
		return nil
	}
	// An unrecognized shape stays empty: the frame is relayed raw, so it must not fail the parse.
	var custom LiveCustomVoice
	if err := Unmarshal(data, &custom); err == nil && custom.ID != "" {
		v.Custom = &custom
	}
	return nil
}

// LiveDelegationConfig is session.delegation. Absent or null selects client delegation.
type LiveDelegationConfig struct {
	Type      LiveDelegationType       `json:"type"`
	Responses *LiveResponsesDelegation `json:"responses,omitempty"`
}

// LiveResponsesDelegation configures the Responses backend that OpenAI calls on
// the session's own key. Model is required at startup and optional on session.update.
type LiveResponsesDelegation struct {
	Model             string                        `json:"model,omitempty"`
	Instructions      *string                       `json:"instructions,omitempty"`
	MaxOutputTokens   *int                          `json:"max_output_tokens,omitempty"`
	ParallelToolCalls *bool                         `json:"parallel_tool_calls,omitempty"`
	Reasoning         *ResponsesParametersReasoning `json:"reasoning,omitempty"`
	ServiceTier       *BifrostServiceTier           `json:"service_tier,omitempty"`
	Text              *ResponsesTextConfig          `json:"text,omitempty"`
	ToolChoice        *ResponsesToolChoice          `json:"tool_choice,omitempty"`
	Tools             []ResponsesTool               `json:"tools,omitempty"`
}

// LiveDelegation is the metadata on session.delegation.created. It carries no task text.
type LiveDelegation struct {
	ID         string             `json:"id"`
	Target     LiveDelegationType `json:"target"`
	Type       string             `json:"type,omitempty"`
	ResponseID *string            `json:"response_id,omitempty"` // responses target only
}

// LiveSessionUsage is the cumulative voice duration. Snapshots replace, never sum.
type LiveSessionUsage struct {
	Seconds float64 `json:"seconds"`
}

type LiveContextWindow struct {
	UsageRatio float64 `json:"usage_ratio"`
}

// LiveError is the error object on error and transport.failed events.
type LiveError struct {
	Type          string  `json:"type"`
	Code          *string `json:"code,omitempty"`
	Message       string  `json:"message"`
	Param         *string `json:"param,omitempty"`
	ClientEventID *string `json:"client_event_id,omitempty"`
}

// LiveEventPayload is the overloaded "event" field: a nested Responses streaming
// event on response.event, or a DTMF key on transport.dtmf.*.
type LiveEventPayload struct {
	Response *BifrostResponsesStreamResponse
	DTMF     *string
}

func (p LiveEventPayload) MarshalJSON() ([]byte, error) {
	if p.Response != nil && p.DTMF != nil {
		return nil, fmt.Errorf("both Response and DTMF are set; only one should be non-nil")
	}
	if p.Response != nil {
		return MarshalSorted(p.Response)
	}
	if p.DTMF != nil {
		return MarshalSorted(p.DTMF)
	}
	return MarshalSorted(nil)
}

func (p *LiveEventPayload) UnmarshalJSON(data []byte) error {
	var dtmf string
	if err := Unmarshal(data, &dtmf); err == nil {
		p.DTMF = &dtmf
		return nil
	}
	var response BifrostResponsesStreamResponse
	if err := Unmarshal(data, &response); err != nil {
		return err
	}
	p.Response = &response
	return nil
}

// ParseLiveEvent decodes a Live control event and keeps the original frame in RawData.
func ParseLiveEvent(raw []byte) (*BifrostLiveEvent, error) {
	var event BifrostLiveEvent
	if err := Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	event.RawData = raw
	return &event, nil
}

// LiveConnectionKind is which Live WebSocket a connection dials.
type LiveConnectionKind string

const (
	LiveConnectionPrimary  LiveConnectionKind = "primary"  // /v1/live/sessions
	LiveConnectionSideband LiveConnectionKind = "sideband" // /v1/live/sessions/{id}/attach
	LiveConnectionFork     LiveConnectionKind = "fork"     // /v1/live/sessions/{id}/fork
)

// LiveProvider is an optional interface for providers that serve GPT Live sessions.
// Checked via type assertion: provider.(LiveProvider).
type LiveProvider interface {
	// LiveWebSocketURL returns the upstream URL. sessionID is required for sideband and fork.
	LiveWebSocketURL(key Key, kind LiveConnectionKind, sessionID string) (string, *BifrostError)
	LiveHeaders(ctx *BifrostContext, key Key) (map[string]string, *BifrostError)
	// CreateLiveWebRTCSession starts a WebRTC session. body is the create request,
	// {session, transport: {type: "webrtc", sdp}}; the response carries the SDP answer.
	CreateLiveWebRTCSession(ctx *BifrostContext, key Key, body []byte) (*LiveCreateResponse, *BifrostError)
	// LiveSessionContent downloads a stored session's recording.
	LiveSessionContent(ctx *BifrostContext, key Key, sessionID string) (*LiveContentResponse, *BifrostError)
}

// BifrostLiveContentRequest downloads a stored session's recording.
type BifrostLiveContentRequest struct {
	Provider  ModelProvider `json:"provider"`
	SessionID string        `json:"session_id"`
}

// LiveContentResponse is a session recording: stereo WAV, caller left and assistant right.
type LiveContentResponse struct {
	SessionID   string `json:"session_id"`
	Content     []byte `json:"-"`
	ContentType string `json:"content_type,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}

// LiveCreateResponse is the answer to POST /v1/live/sessions.
type LiveCreateResponse struct {
	Session   *LiveSession   `json:"session"`
	Transport *LiveTransport `json:"transport"`
}

// LiveTransport is the media transport of a Live session; its SDP is an offer in the request
// and an answer in the response.
type LiveTransport struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// LiveSessionLog is what Bifrost logs for one GPT Live session: how it ran, what was said, and
// what the backend did on its behalf. The transport fills the session facts and the transcript;
// the logging plugin fills the costs and the delegations it billed.
type LiveSessionLog struct {
	Transport         string               `json:"transport,omitempty"` // websocket or webrtc
	ProviderSessionID string               `json:"provider_session_id,omitempty"`
	VoiceSeconds      float64              `json:"voice_seconds"`
	VoiceCost         *float64             `json:"voice_cost,omitempty"`
	BackendCost       *float64             `json:"backend_cost,omitempty"`
	Transcript        []LiveTranscriptLine `json:"transcript,omitempty"`
	Delegations       []LiveDelegationLog  `json:"delegations,omitempty"`
}

// LiveTranscriptLine is one speaker turn of a session's transcript, on the session's timeline.
type LiveTranscriptLine struct {
	Role    string `json:"role"` // user or assistant
	Text    string `json:"text"`
	StartMs int64  `json:"start_ms,omitempty"`
	EndMs   int64  `json:"end_ms,omitempty"`
}

// LiveDelegationLog is one task the voice model handed to the backend: the Responses calls that
// ran it (a function call spans two), what they produced, and what they cost.
type LiveDelegationLog struct {
	DelegationID string             `json:"delegation_id,omitempty"` // the provider's id, shared by the task's responses
	RequestID    string             `json:"request_id"`              // the first billing unit's id
	ResponseIDs  []string           `json:"response_ids,omitempty"`
	Model        string             `json:"model"`
	StartedMs    int64              `json:"started_ms,omitempty"` // on the session's timeline
	Usage        *BifrostLLMUsage   `json:"usage,omitempty"`
	Cost         *float64           `json:"cost,omitempty"`
	Output       []ResponsesMessage `json:"output,omitempty"` // the backend's output items, in order
	Error        string             `json:"error,omitempty"`
}
