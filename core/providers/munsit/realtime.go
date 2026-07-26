package munsit

import (
	"encoding/json"
	"fmt"
	"strings"
	"net/url"
	"github.com/maximhq/bifrost/core/schemas"
)

// SupportsRealtimeAPI returns true since Munsit supports streaming TTS via WebSocket.
func (provider *MunsitProvider) SupportsRealtimeAPI() bool {
	return true
}

// RealtimeWebSocketURL returns the WSS URL for the Munsit Text-to-Speech WebSocket endpoint.
// Munsit's endpoint is fixed (no model in the URL/path, unlike OpenAI/ElevenLabs) — model
// selection happens inside the initConnection message instead, via ToProviderRealtimeEvent's
// handling of session.update. The model argument is intentionally unused here.
func (provider *MunsitProvider) RealtimeWebSocketURL(key schemas.Key, model string) string {
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return fmt.Sprintf(
		"%s/api/v1/websocket/text-to-speech?x-api-key=%s",
		base,
		url.QueryEscape(key.Value.GetValue()),
	)
}

// RealtimeHeaders returns the headers required for the Munsit WebSocket connection.
//
// NOTE: Munsit's WS docs only document query-param (?x-api-key=) or in-message (x_api_key)
// auth — presumably because browser WebSocket clients can't set custom handshake headers.
// Bifrost dials server-side, so this sends the same x-api-key header the REST provider uses
// (see munsit.go Speech/SpeechStream). VERIFY THIS AGAINST THE REAL SERVER — if Munsit's WS
// handshake rejects header auth, switch to appending the key as a query parameter in
// RealtimeWebSocketURL instead.
func (provider *MunsitProvider) RealtimeHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers := map[string]string{
		"x-api-key": key.Value.GetValue(),
	}
	for k, v := range provider.networkConfig.ExtraHeaders {
		if strings.EqualFold(k, "x-api-key") {
			continue
		}
		headers[k] = v
	}
	return headers, nil
}

// SupportsRealtimeWebRTC returns false — Munsit has no WebRTC SDP exchange.
func (provider *MunsitProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not supported by Munsit.
func (provider *MunsitProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(400),
		Error:          &schemas.ErrorField{Type: schemas.Ptr("invalid_request_error"), Message: "WebRTC SDP exchange is not supported for Munsit"},
	}
}

// ShouldStartRealtimeTurn always returns false — Munsit's WebSocket protocol has no
// multi-turn conversation concept (no tool calls, no user/assistant turns), just
// continuous text-in/audio-out on a single session. Turn-level governance hooks are
// bypassed entirely, matching the ElevenLabs Conversational AI provider's own choice.
func (provider *MunsitProvider) ShouldStartRealtimeTurn(_ *schemas.BifrostRealtimeEvent) bool {
	return false
}

// RealtimeTurnFinalEvent is never actually reached since ShouldStartRealtimeTurn is always
// false, but the interface requires a value.
func (provider *MunsitProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseAudioDone
}

// RealtimeWebRTCDataChannelLabel returns empty — no WebRTC support.
func (provider *MunsitProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

// RealtimeWebSocketSubprotocol returns empty — Munsit's WS endpoint requires no subprotocol.
func (provider *MunsitProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

// ShouldForwardRealtimeEvent forwards every translated event to the client.
func (provider *MunsitProvider) ShouldForwardRealtimeEvent(_ *schemas.BifrostRealtimeEvent) bool {
	return true
}

// ShouldAccumulateRealtimeOutput always returns false — Munsit is TTS-only (no transcript
// or assistant text to accumulate for turn history, since turns are never started).
func (provider *MunsitProvider) ShouldAccumulateRealtimeOutput(_ schemas.RealtimeEventType) bool {
	return false
}

// munsitInitConnection mirrors the initConnection message shape from Munsit's
// WebSocket API reference.
type munsitInitConnection struct {
	Type          string                       `json:"type"`
	ModelID       string                       `json:"model_id,omitempty"`
	VoiceID       string                       `json:"voice_id"`
	VoiceSettings *munsitRealtimeVoiceSettings `json:"voice_settings,omitempty"`
	OutputFormat  string                       `json:"output_format,omitempty"`
}

type munsitRealtimeVoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
}

// munsitTextMessage mirrors the "text" message shape from Munsit's WebSocket API reference.
type munsitTextMessage struct {
	Type                 string `json:"type"`
	Text                 string `json:"text"`
	Flush                bool   `json:"flush,omitempty"`
	TryTriggerGeneration bool   `json:"try_trigger_generation,omitempty"`
}

// munsitRealtimeEvent is a superset parser covering every possible Munsit WS server message:
// connectionInitialized, an audio chunk (which carries no "type" field at all), or an error.
type munsitRealtimeEvent struct {
	Type         string  `json:"type,omitempty"`
	Audio        *string `json:"audio,omitempty"`
	SampleRate   *int    `json:"sampleRate,omitempty"`
	ErrorCode    *int    `json:"errorCode,omitempty"`
	ErrorMessage *string `json:"errorMessage,omitempty"`
}

// ToBifrostRealtimeEvent converts a raw Munsit WebSocket server message into the unified
// Bifrost Realtime event format.
func (provider *MunsitProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var raw munsitRealtimeEvent
	if err := json.Unmarshal(providerEvent, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Munsit realtime event: %w", err)
	}

	event := &schemas.BifrostRealtimeEvent{
		RawData: providerEvent,
	}

	switch {
	case raw.Type == "connectionInitialized":
		event.Type = schemas.RTEventSessionCreated
		event.Session = &schemas.RealtimeSession{}

	case raw.Type == "error":
		event.Type = schemas.RTEventError
		errorCode := ""
		if raw.ErrorCode != nil {
			errorCode = fmt.Sprintf("%d", *raw.ErrorCode)
		}
		message := ""
		if raw.ErrorMessage != nil {
			message = *raw.ErrorMessage
		}
		event.Error = &schemas.RealtimeError{
			Type:    "munsit_error",
			Code:    errorCode,
			Message: message,
		}

	case raw.Audio != nil:
		// Audio chunk responses carry no "type" field at all — presence of "audio" is
		// what identifies them per Munsit's spec.
		event.Type = schemas.RTEventResponseAudioDelta
		event.Delta = &schemas.RealtimeDelta{
			Audio: *raw.Audio,
		}

	default:
		event.Type = schemas.RealtimeEventType(raw.Type)
	}

	return event, nil
}

// extractRealtimeItemText pulls plain text out of an OpenAI-Realtime-shaped conversation
// item content array (e.g. [{"type":"input_text","text":"hello"}]), concatenating all
// text parts in order. Falls back to treating Content as a plain JSON string if it isn't
// an array (defensive — real OpenAI-shaped clients always send the array form).
func extractRealtimeItemText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}

	var plain string
	if err := json.Unmarshal(content, &plain); err == nil {
		return plain
	}

	return ""
}

const munsitDefaultRealtimeModel = "faseeh-v1-preview"

// ToProviderRealtimeEvent converts a unified Bifrost Realtime event into a Munsit
// WebSocket client message.
func (provider *MunsitProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	fmt.Println("EVENT:", bifrostEvent.Type)
	switch bifrostEvent.Type {
	case schemas.RTEventSessionUpdate:
		msg := munsitInitConnection{Type: "initConnection", ModelID: munsitDefaultRealtimeModel}
		if bifrostEvent.Session != nil {
			if bifrostEvent.Session.Model != "" {
				msg.ModelID = bifrostEvent.Session.Model
			}
			msg.VoiceID = bifrostEvent.Session.Voice
		}
		payload, _ := schemas.MarshalSorted(msg)
		fmt.Println(string(payload))
		return payload, nil
		// return schemas.MarshalSorted(msg)

	case schemas.RTEventConversationItemCreate:
		text := ""
		if bifrostEvent.Item != nil {
			text = extractRealtimeItemText(bifrostEvent.Item.Content)
		}
		msg := munsitTextMessage{Type: "text", Text: text, Flush: false}
		payload, _ := schemas.MarshalSorted(msg)
		fmt.Println(string(payload))
		return payload, nil
		// return schemas.MarshalSorted(msg)

	case schemas.RTEventResponseCreate:
		// No new text to add — just force generation of whatever's buffered so far.
		msg := munsitTextMessage{Type: "text", Text: "", Flush: true}
		payload, _ := schemas.MarshalSorted(msg)
		fmt.Println(string(payload))
		return payload, nil
		// return schemas.MarshalSorted(msg)

	default:
		out := map[string]interface{}{
			"type": string(bifrostEvent.Type),
		}
		return schemas.MarshalSorted(out)
	}
}