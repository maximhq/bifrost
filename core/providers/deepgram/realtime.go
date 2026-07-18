package deepgram

import (
	"encoding/json"
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// SupportsRealtimeAPI returns true — Deepgram's Voice Agent API
// (wss://api.deepgram.com/v1/agent/converse) is a Realtime provider.
func (provider *DeepgramProvider) SupportsRealtimeAPI() bool {
	return true
}

// defaultDeepgramRESTBaseURL is the default BaseURL set by NewDeepgramProvider
// for REST (Listen/Speak) calls.
const defaultDeepgramRESTBaseURL = "https://api.deepgram.com"

// RealtimeWebSocketURL returns the WSS URL for Deepgram's Voice Agent API.
//
// Unlike REST Listen/Speak (api.deepgram.com), Deepgram's Voice Agent API is
// served from a separate host, agent.deepgram.com — confirmed live (a Settings
// message against wss://agent.deepgram.com/v1/agent/converse gets a real
// Welcome/SettingsApplied response; the same request against
// wss://api.deepgram.com/v1/agent/converse 404s). If the caller has configured
// a custom BaseURL (e.g. a dedicated/self-hosted Deepgram deployment, which
// Deepgram's docs say serves Agent from the same custom host), that override
// is respected as-is; only the default public API host is redirected to
// agent.deepgram.com.
//
// Unlike OpenAI/Azure, Deepgram Agent does not take a `model` query parameter —
// model/provider selection for listen/think/speak all happens inside the
// Settings message body (see ToProviderRealtimeEvent). The model argument is
// accepted only for interface conformance and is otherwise unused.
func (provider *DeepgramProvider) RealtimeWebSocketURL(key schemas.Key, model string) string {
	base := strings.TrimRight(provider.networkConfig.BaseURL, "/")
	if base == defaultDeepgramRESTBaseURL {
		base = "https://agent.deepgram.com"
	}
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return base + "/v1/agent/converse"
}

// RealtimeHeaders returns the headers required for Deepgram's Voice Agent WebSocket.
func (provider *DeepgramProvider) RealtimeHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers := map[string]string{
		"Authorization": "Token " + key.Value.GetValue(),
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		headers[k] = v
	}
	return headers, nil
}

// SupportsRealtimeWebRTC returns false — Deepgram Agent has no documented WebRTC/SDP mode.
func (provider *DeepgramProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not supported for Deepgram.
func (provider *DeepgramProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(400),
		Error:          &schemas.ErrorField{Type: schemas.Ptr("invalid_request_error"), Message: "WebRTC SDP exchange is not supported for Deepgram"},
	}
}

// SupportsRealtimeBinaryAudioInput reports that Deepgram's Agent API accepts
// raw binary WebSocket frames (its "Media" message) as client audio input,
// unlike OpenAI/Azure/ElevenLabs which wrap audio in a JSON
// input_audio_buffer.append event. Served over the dedicated
// /v1/realtime/audio route (see wsrealtimebinary.go).
func (provider *DeepgramProvider) SupportsRealtimeBinaryAudioInput() bool {
	return true
}

func (provider *DeepgramProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

func (provider *DeepgramProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

// Deepgram Agent WebSocket message types (client -> server)
const (
	dgClientSettings             = "Settings"
	dgClientUpdatePrompt         = "UpdatePrompt"
	dgClientUpdateListen         = "UpdateListen"
	dgClientUpdateThink          = "UpdateThink"
	dgClientUpdateSpeak          = "UpdateSpeak"
	dgClientInjectUserMessage    = "InjectUserMessage"
	dgClientInjectAgentMessage   = "InjectAgentMessage"
	dgClientFunctionCallResponse = "FunctionCallResponse"
	dgClientKeepAlive            = "KeepAlive"
)

// Deepgram Agent WebSocket message types (server -> client)
const (
	dgServerWelcome           = "Welcome"
	dgServerSettingsApplied   = "SettingsApplied"
	dgServerUserStartedSpeak  = "UserStartedSpeaking"
	dgServerConversationText  = "ConversationText"
	dgServerAgentThinking     = "AgentThinking"
	dgServerFunctionCallReq   = "FunctionCallRequest"
	dgServerAgentStartedSpeak = "AgentStartedSpeaking"
	dgServerAgentAudioDone    = "AgentAudioDone"
	dgServerHistory           = "History"
	dgServerListenUpdated     = "ListenUpdated"
	dgServerThinkUpdated      = "ThinkUpdated"
	dgServerSpeakUpdated      = "SpeakUpdated"
	dgServerPromptUpdated     = "PromptUpdated"
	dgServerInjectionRefused  = "InjectionRefused"
	dgServerError             = "Error"
	dgServerWarning           = "Warning"
)

// Ad-hoc RealtimeEventType literals for Deepgram Agent concepts with no
// equivalent in the OpenAI-Realtime-shaped RTEvent* vocabulary. Mirrors the
// precedent in core/providers/elevenlabs/realtime.go (e.g. its raw
// "client_tool_call"/"pong" literals) for provider-specific events that don't
// force-fit the canonical schema.
const (
	dgEventAgentThinking     schemas.RealtimeEventType = "agent_thinking"
	dgEventAgentStartedSpeak schemas.RealtimeEventType = "agent_started_speaking"
	dgEventHistory           schemas.RealtimeEventType = "history"
	dgEventWarning           schemas.RealtimeEventType = "warning"
)

type dgAgentEventEnvelope struct {
	Type string `json:"type"`

	// Server events
	RequestID   string `json:"request_id,omitempty"`
	Role        string `json:"role,omitempty"`
	Content     string `json:"content,omitempty"`
	Description string `json:"description,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`

	// FunctionCallRequest carries one or more function calls; only the first
	// is promoted into the unified schema's single Item (see ToBifrostRealtimeEvent).
	Functions []dgAgentFunctionCall `json:"functions,omitempty"`
}

type dgAgentFunctionCall struct {
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	ClientSide bool            `json:"client_side,omitempty"`
}

// ToBifrostRealtimeEvent converts a Deepgram Voice Agent event to the unified Bifrost format.
func (provider *DeepgramProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var raw dgAgentEventEnvelope
	if err := json.Unmarshal(providerEvent, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Deepgram realtime event: %w", err)
	}

	event := &schemas.BifrostRealtimeEvent{
		RawData: providerEvent,
	}

	switch raw.Type {
	case dgServerWelcome:
		event.Type = schemas.RTEventSessionCreated
		event.Session = &schemas.RealtimeSession{ID: raw.RequestID}

	case dgServerSettingsApplied:
		event.Type = schemas.RTEventSessionUpdated

	case dgServerUserStartedSpeak:
		event.Type = schemas.RTEventInputAudioSpeechStarted

	case dgServerConversationText:
		if raw.Role == "user" {
			event.Type = schemas.RTEventInputAudioTransCompleted
		} else {
			event.Type = schemas.RTEventResponseAudioTransDelta
		}
		event.Delta = &schemas.RealtimeDelta{Transcript: raw.Content}

	case dgServerAgentThinking:
		event.Type = dgEventAgentThinking
		event.Delta = &schemas.RealtimeDelta{Text: raw.Content}

	case dgServerFunctionCallReq:
		event.Type = schemas.RTEventResponseOutputItemDone
		if len(raw.Functions) > 0 {
			// event.Item carries a single scalar Name/CallID/Arguments tuple,
			// so only the first function call is promoted there — that
			// field feeds turn-start detection and any single-item consumer.
			// The client itself is never shorted: RawData (set above)
			// preserves the full untranslated message, and the relay writes
			// RawData verbatim to the client instead of re-encoding from
			// Item whenever it's set. Any additional calls beyond the first
			// are carried via ExtraParams[RealtimeExtraParamKeyAdditionalItems]
			// so turn-level tool-call accumulation (for Bifrost's own audit
			// logging, which Deepgram's terminal AgentAudioDone event has no
			// aggregated summary for, unlike OpenAI's response.done) doesn't
			// silently drop them either.
			fn := raw.Functions[0]
			event.Item = &schemas.RealtimeItem{
				Type:      "function_call",
				Name:      fn.Name,
				CallID:    fn.ID,
				Arguments: string(fn.Arguments),
			}
			if len(raw.Functions) > 1 {
				additional := make([]schemas.RealtimeItem, 0, len(raw.Functions)-1)
				for _, extra := range raw.Functions[1:] {
					additional = append(additional, schemas.RealtimeItem{
						Type:      "function_call",
						Name:      extra.Name,
						CallID:    extra.ID,
						Arguments: string(extra.Arguments),
					})
				}
				if encoded, err := json.Marshal(additional); err == nil {
					event.ExtraParams = map[string]json.RawMessage{
						schemas.RealtimeExtraParamKeyAdditionalItems: encoded,
					}
				}
			}
		}

	case dgServerAgentStartedSpeak:
		event.Type = dgEventAgentStartedSpeak

	case dgServerAgentAudioDone:
		event.Type = schemas.RTEventResponseAudioDone

	case dgServerHistory:
		event.Type = dgEventHistory

	case dgServerListenUpdated, dgServerThinkUpdated, dgServerSpeakUpdated, dgServerPromptUpdated:
		event.Type = schemas.RTEventSessionUpdated

	case dgServerInjectionRefused:
		event.Type = schemas.RTEventError
		event.Error = &schemas.RealtimeError{Message: raw.Message}

	case dgServerError:
		event.Type = schemas.RTEventError
		event.Error = &schemas.RealtimeError{Code: raw.Code, Message: raw.Description}

	case dgServerWarning:
		event.Type = dgEventWarning
		event.Error = &schemas.RealtimeError{Code: raw.Code, Message: raw.Description}

	case dgClientFunctionCallResponse:
		// Deepgram echoes FunctionCallResponse back for server-executed tools.
		event.Type = schemas.RealtimeEventType("function_call_response")

	default:
		event.Type = schemas.RealtimeEventType(raw.Type)
	}

	return event, nil
}

// ToProviderRealtimeEvent converts a unified Bifrost Realtime event to Deepgram's native JSON.
//
// Deepgram Agent's Settings message configures an entire listen/think/speak
// provider stack per session (it is itself a bring-your-own-providers
// orchestrator), which doesn't map 1:1 onto RealtimeSession's flatter
// model/voice/instructions fields (true for OpenAI/ElevenLabs, not Deepgram
// Agent). What maps directly is applied; the rest is expected to be carried
// via Session.ExtraParams["agent"] / ["audio"] (raw JSON matching Deepgram's
// own Settings schema) as an escape hatch, same pattern used elsewhere in
// Bifrost for provider-specific fields.
func (provider *DeepgramProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	switch bifrostEvent.Type {
	case schemas.RTEventSessionUpdate:
		return provider.buildSettingsMessage(bifrostEvent.Session)

	case schemas.RealtimeEventType(dgClientKeepAlive):
		return schemas.MarshalSorted(map[string]interface{}{"type": dgClientKeepAlive})

	case schemas.RTEventConversationItemCreate:
		if bifrostEvent.Item != nil && bifrostEvent.Item.Type == "function_call_output" {
			return schemas.MarshalSorted(map[string]interface{}{
				"type":    dgClientFunctionCallResponse,
				"id":      bifrostEvent.Item.CallID,
				"name":    bifrostEvent.Item.Name,
				"content": bifrostEvent.Item.Output,
			})
		}
		if bifrostEvent.Item != nil && bifrostEvent.Item.Role == "user" {
			return schemas.MarshalSorted(map[string]interface{}{
				"type":    dgClientInjectUserMessage,
				"content": bifrostEvent.Item.Content,
			})
		}
		return nil, fmt.Errorf("unsupported conversation.item.create shape for Deepgram Agent")

	case schemas.RTEventInputAudioAppend:
		// Deepgram Agent audio input is raw binary (Media frames), sent via the
		// dedicated /v1/realtime/audio route's binary passthrough, bypassing
		// this JSON translation path entirely. A client sending the
		// OpenAI-style JSON input_audio_buffer.append event to a Deepgram
		// session instead of raw binary frames is a client integration error.
		return nil, fmt.Errorf("Deepgram Agent expects raw binary audio frames over /v1/realtime/audio, not input_audio_buffer.append JSON")

	default:
		// Passthrough for Deepgram-native literal type strings the caller may
		// send directly (e.g. "Settings", "UpdateListen", "UpdateThink",
		// "UpdateSpeak", "UpdatePrompt", "InjectAgentMessage") — same escape
		// hatch pattern as ElevenLabs' default case.
		out := map[string]interface{}{"type": string(bifrostEvent.Type)}
		for k, v := range bifrostEvent.ExtraParams {
			out[k] = v
		}
		return schemas.MarshalSorted(out)
	}
}

// buildSettingsMessage constructs Deepgram's Settings message from the
// canonical RealtimeSession, with ExtraParams["agent"]/["audio"] as the
// primary carrier for Deepgram's rich nested listen/think/speak config.
func (provider *DeepgramProvider) buildSettingsMessage(session *schemas.RealtimeSession) (json.RawMessage, error) {
	settings := map[string]interface{}{"type": dgClientSettings}
	if session == nil {
		return schemas.MarshalSorted(settings)
	}

	agent := map[string]interface{}{}
	if session.Instructions != "" || session.Model != "" {
		think := map[string]interface{}{}
		if session.Instructions != "" {
			think["prompt"] = session.Instructions
		}
		if session.Model != "" {
			// Deepgram's agent.think.provider also expects a vendor "type"
			// (e.g. "open_ai", "anthropic"), which RealtimeSession has no
			// field for — callers needing a non-default vendor should set it
			// via session.ExtraParams["agent"], which merges on top of this.
			think["provider"] = map[string]interface{}{"model": session.Model}
		}
		agent["think"] = think
	}
	if session.Voice != "" {
		agent["speak"] = map[string]interface{}{"provider": map[string]interface{}{
			"type":  "deepgram",
			"model": session.Voice,
		}}
	}

	for key, raw := range session.ExtraParams {
		var parsed interface{}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("failed to parse session.extra_params[%q] for Deepgram Settings message: %w", key, err)
		}
		switch key {
		case "agent":
			agentOverride, ok := parsed.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("session.extra_params[\"agent\"] must be a JSON object, got %T", parsed)
			}
			// Recursive merge (not a shallow overwrite): a caller supplying
			// agent.think.model, for example, must not clobber the
			// agent.think.prompt set above from session.Instructions.
			providerUtils.MergeExtraParams(agent, agentOverride)
		case "audio":
			settings["audio"] = parsed
		default:
			settings[key] = parsed
		}
	}

	if len(agent) > 0 {
		settings["agent"] = agent
	}

	return schemas.MarshalSorted(settings)
}

// ShouldStartRealtimeTurn reports whether the event should start pre-hooks.
// Deepgram signals turn-start explicitly via UserStartedSpeaking, unlike
// ElevenLabs (which has no such signal and always returns false).
func (provider *DeepgramProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	return event != nil && event.Type == schemas.RTEventInputAudioSpeechStarted
}

// RealtimeTurnFinalEvent returns AgentAudioDone — Deepgram's clearest
// turn-boundary signal (the agent finished speaking).
func (provider *DeepgramProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseAudioDone
}

func (provider *DeepgramProvider) ShouldForwardRealtimeEvent(event *schemas.BifrostRealtimeEvent) bool {
	return true
}

func (provider *DeepgramProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	return eventType == schemas.RTEventResponseAudioTransDelta || eventType == schemas.RTEventResponseAudioDone
}
