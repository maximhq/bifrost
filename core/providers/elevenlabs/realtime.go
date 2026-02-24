package elevenlabs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
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
		headers[k] = v
	}
	return headers
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

	elUserAudioChunk  = "user_audio_chunk"
	elPong            = "pong"
	elClientToolResult = "client_tool_result"
	elContextualUpdate = "contextual_update"
)

// elevenlabsEvent represents a raw ElevenLabs Conversational AI WebSocket event.
type elevenlabsEvent struct {
	Type string `json:"type"`

	// Server events
	ConversationInitMetadata json.RawMessage `json:"conversation_initiation_metadata_event,omitempty"`
	Audio                    json.RawMessage `json:"audio_event,omitempty"`
	UserTranscript           json.RawMessage `json:"user_transcript_event,omitempty"`
	AgentResponse            json.RawMessage `json:"agent_response_event,omitempty"`
	AgentResponseCorrection  json.RawMessage `json:"agent_response_correction_event,omitempty"`
	ClientToolCall           json.RawMessage `json:"client_tool_call,omitempty"`
	PingEvent                json.RawMessage `json:"ping_event,omitempty"`

	// Client events
	UserAudioChunk json.RawMessage `json:"user_audio_chunk,omitempty"`
}

// elevenlabsAudioEvent is the audio event structure from ElevenLabs.
type elevenlabsAudioEvent struct {
	Audio     string          `json:"audio,omitempty"`
	Alignment json.RawMessage `json:"alignment,omitempty"`
}

// elevenlabsTranscriptEvent is the user/agent transcript event from ElevenLabs.
type elevenlabsTranscriptEvent struct {
	UserTranscript  string `json:"user_transcript,omitempty"`
	AgentResponse   string `json:"agent_response,omitempty"`
	AgentResponseID string `json:"agent_response_id,omitempty"`
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
		event.Type = schemas.RTEventResponseTextDone
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
			var correction elevenlabsTranscriptEvent
			if err := json.Unmarshal(raw.AgentResponseCorrection, &correction); err == nil {
				event.Delta = &schemas.RealtimeDelta{
					Text: correction.AgentResponse,
				}
			}
		}

	case elInterruption:
		event.Type = schemas.RTEventResponseCancel

	case elClientToolCall:
		event.Type = schemas.RealtimeEventType("client_tool_call")
		if raw.ClientToolCall != nil {
			var toolCall struct {
				ToolName   string `json:"tool_name"`
				Parameters string `json:"parameters"`
				ToolCallID string `json:"tool_call_id"`
			}
			if err := json.Unmarshal(raw.ClientToolCall, &toolCall); err == nil {
				event.Item = &schemas.RealtimeItem{
					Type:      "function_call",
					Name:      toolCall.ToolName,
					CallID:    toolCall.ToolCallID,
					Arguments: toolCall.Parameters,
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
	if bifrostEvent.RawData != nil {
		return bifrostEvent.RawData, nil
	}

	switch bifrostEvent.Type {
	case schemas.RTEventInputAudioAppend:
		if bifrostEvent.Delta == nil {
			return nil, fmt.Errorf("delta must be set for input_audio_buffer.append events")
		}
		out := map[string]interface{}{
			"type":             elUserAudioChunk,
			"user_audio_chunk": bifrostEvent.Delta.Audio,
		}
		return json.Marshal(out)

	case schemas.RealtimeEventType("pong"):
		return json.Marshal(map[string]interface{}{
			"type": "pong",
		})

	default:
		out := map[string]interface{}{
			"type": string(bifrostEvent.Type),
		}
		return json.Marshal(out)
	}
}
