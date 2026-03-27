package elevenlabs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// SupportsRealtimeAPI returns true since ElevenLabs supports Conversational AI via WebSocket.
func (provider *ElevenlabsProvider) SupportsRealtimeAPI() bool {
	return true
}

// RealtimeWebSocketURL returns the WSS URL for the ElevenLabs Conversational AI endpoint.
// The model parameter is used as the agent_id query parameter.
// Format: wss://api.elevenlabs.io/v1/convai/conversation?agent_id=<model>
func (provider *ElevenlabsProvider) RealtimeWebSocketURL(key schemas.Key, model string) string {
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return base + "/v1/convai/conversation?agent_id=" + model
}

// RealtimeHeaders returns the headers required for the ElevenLabs Conversational AI WebSocket.
func (provider *ElevenlabsProvider) RealtimeHeaders(key schemas.Key) map[string]string {
	headers := map[string]string{
		"xi-api-key": key.Value.GetValue(),
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(k, "xi-api-key") {
			continue
		}
		headers[k] = v
	}
	return headers
}

// SupportsRealtimeWebRTC returns false — ElevenLabs WebRTC SDP exchange is not yet implemented.
func (provider *ElevenlabsProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not yet implemented for ElevenLabs.
func (provider *ElevenlabsProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(400),
		Error:          &schemas.ErrorField{Type: schemas.Ptr("invalid_request_error"), Message: "WebRTC SDP exchange is not yet implemented for ElevenLabs"},
	}
}

func (provider *ElevenlabsProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return false
}

func (provider *ElevenlabsProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseDone
}

func (provider *ElevenlabsProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

func (provider *ElevenlabsProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

func (provider *ElevenlabsProvider) ShouldForwardRealtimeEvent(event *schemas.BifrostRealtimeEvent) bool {
	return true
}

func (provider *ElevenlabsProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	return eventType == schemas.RTEventResponseDone
}

// ElevenLabs Conversational AI WebSocket event types
const (
	elConversationInitMetadata = "conversation_initiation_metadata"
	elPing                     = "ping"
	elAudio                    = "audio"
	elUserTranscript           = "user_transcript"
	elAgentResponse            = "agent_response"
	elAgentResponseCorrection  = "agent_response_correction"
	elInterruption             = "interruption"
	elClientToolCall           = "client_tool_call"

	elUserAudioChunk   = "user_audio_chunk"
	elPong             = "pong"
	elClientToolResult = "client_tool_result"
	elContextualUpdate = "contextual_update"
)

// elevenlabsEvent represents a raw ElevenLabs Conversational AI WebSocket event.
type elevenlabsEvent struct {
	Type string `json:"type"`

	// Server events
	ConversationInitMetadata json.RawMessage `json:"conversation_initiation_metadata_event,omitempty"`
	Audio                    json.RawMessage `json:"audio_event,omitempty"`
	UserTranscript           json.RawMessage `json:"user_transcription_event,omitempty"`
	AgentResponse            json.RawMessage `json:"agent_response_event,omitempty"`
	AgentResponseCorrection  json.RawMessage `json:"agent_response_correction_event,omitempty"`
	ClientToolCall           json.RawMessage `json:"client_tool_call,omitempty"`
	PingEvent                json.RawMessage `json:"ping_event,omitempty"`

	// Client events
	UserAudioChunk json.RawMessage `json:"user_audio_chunk,omitempty"`
}

// elevenlabsAudioEvent is the audio event structure from ElevenLabs.
type elevenlabsAudioEvent struct {
	Audio     string          `json:"audio_base_64,omitempty"`
	Alignment json.RawMessage `json:"alignment,omitempty"`
}

// elevenlabsTranscriptEvent is the user/agent transcript event from ElevenLabs.
type elevenlabsTranscriptEvent struct {
	UserTranscript  string `json:"user_transcript,omitempty"`
	AgentResponse   string `json:"agent_response,omitempty"`
	AgentResponseID string `json:"agent_response_id,omitempty"`
}

// elevenlabsCorrectionEvent is the agent response correction event from ElevenLabs.
type elevenlabsCorrectionEvent struct {
	OriginalAgentResponse  string `json:"original_agent_response,omitempty"`
	CorrectedAgentResponse string `json:"corrected_agent_response,omitempty"`
}

// ToBifrostRealtimeEvent converts an ElevenLabs Conversational AI event to the unified Bifrost format.
func (provider *ElevenlabsProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var raw elevenlabsEvent
	if err := json.Unmarshal(providerEvent, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ElevenLabs realtime event: %w", err)
	}

	event := &schemas.BifrostRealtimeEvent{
		RawData: providerEvent,
	}

	switch raw.Type {
	case elConversationInitMetadata:
		event.Type = schemas.RTEventSessionCreated
		event.Session = &schemas.RealtimeSession{}

	case elPing:
		event.Type = schemas.RealtimeEventType("ping")

	case elAudio:
		event.Type = schemas.RTEventResponseAudioDelta
		if raw.Audio != nil {
			var audioEvt elevenlabsAudioEvent
			if err := json.Unmarshal(raw.Audio, &audioEvt); err == nil {
				event.Delta = &schemas.RealtimeDelta{
					Audio: audioEvt.Audio,
				}
			}
		}

	case elUserTranscript:
		event.Type = schemas.RTEventInputAudioTransCompleted
		if raw.UserTranscript != nil {
			var transcript elevenlabsTranscriptEvent
			if err := json.Unmarshal(raw.UserTranscript, &transcript); err == nil {
				event.Delta = &schemas.RealtimeDelta{
					Transcript: transcript.UserTranscript,
				}
			}
		}

	case elAgentResponse:
		event.Type = schemas.RTEventResponseDone
		if raw.AgentResponse != nil {
			var agentResp elevenlabsTranscriptEvent
			if err := json.Unmarshal(raw.AgentResponse, &agentResp); err == nil {
				event.Delta = &schemas.RealtimeDelta{
					Text: agentResp.AgentResponse,
				}
			}
		}

	case elAgentResponseCorrection:
		event.Type = schemas.RTEventResponseTextDelta
		if raw.AgentResponseCorrection != nil {
			var correction elevenlabsCorrectionEvent
			if err := json.Unmarshal(raw.AgentResponseCorrection, &correction); err == nil {
				event.Delta = &schemas.RealtimeDelta{
					Text: correction.CorrectedAgentResponse,
				}
			}
		}

	case elInterruption:
		event.Type = schemas.RTEventResponseCancel

	case elClientToolCall:
		event.Type = schemas.RealtimeEventType("client_tool_call")
		if raw.ClientToolCall != nil {
			var toolCall struct {
				ToolName   string          `json:"tool_name"`
				Parameters json.RawMessage `json:"parameters"`
				ToolCallID string          `json:"tool_call_id"`
			}
			if err := json.Unmarshal(raw.ClientToolCall, &toolCall); err == nil {
				args := string(toolCall.Parameters)
				if len(toolCall.Parameters) > 0 {
					var parsed interface{}
					if err := json.Unmarshal(toolCall.Parameters, &parsed); err == nil {
						if sorted, err := providerUtils.MarshalSorted(parsed); err == nil {
							args = string(sorted)
						}
					}
				}
				event.Item = &schemas.RealtimeItem{
					Type:      "function_call",
					Name:      toolCall.ToolName,
					CallID:    toolCall.ToolCallID,
					Arguments: args,
				}
			}
		}

	default:
		event.Type = schemas.RealtimeEventType(raw.Type)
	}

	return event, nil
}

// ToProviderRealtimeEvent converts a unified Bifrost Realtime event to ElevenLabs' native JSON.
func (provider *ElevenlabsProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	switch bifrostEvent.Type {
	case schemas.RTEventInputAudioAppend:
		if bifrostEvent.Delta == nil {
			return nil, fmt.Errorf("delta must be set for input_audio_buffer.append events")
		}
		out := map[string]interface{}{
			"type":             elUserAudioChunk,
			"user_audio_chunk": bifrostEvent.Delta.Audio,
		}
		return schemas.MarshalSorted(out)

	case schemas.RealtimeEventType("pong"):
		return schemas.MarshalSorted(map[string]interface{}{
			"type": "pong",
		})

	default:
		out := map[string]interface{}{
			"type": string(bifrostEvent.Type),
		}
		return schemas.MarshalSorted(out)
	}
}
