package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// SupportsRealtimeAPI returns true — Gemini Live (BidiGenerateContent) is supported
// for the T<->T, S<->T, T<->S and S<->S modality combinations. Video input is not
// yet supported (no canonical Bifrost schema slot exists for it).
func (provider *GeminiProvider) SupportsRealtimeAPI() bool {
	return true
}

// RealtimeWebSocketURL returns the Gemini Live BidiGenerateContent WS endpoint.
// Auth is via the `key` query parameter — Gemini's websocket handshake does not
// accept the API key as a request header, confirmed against the live endpoint.
func (provider *GeminiProvider) RealtimeWebSocketURL(key schemas.Key, model string) string {
	base := provider.networkConfig.BaseURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	base = strings.TrimSuffix(base, "/v1beta")
	return base + "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=" + url.QueryEscape(key.Value.GetValue())
}

// RealtimeHeaders returns no headers — Gemini Live auth rides on the URL's `key`
// query parameter, not a request header.
func (provider *GeminiProvider) RealtimeHeaders(_ *schemas.BifrostContext, _ schemas.Key) (map[string]string, *schemas.BifrostError) {
	return map[string]string{}, nil
}

// SupportsRealtimeWebRTC returns false — Gemini Live has no public WebRTC SDP-exchange spec.
func (provider *GeminiProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not implemented for Gemini.
func (provider *GeminiProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(400),
		Error:          &schemas.ErrorField{Type: schemas.Ptr("invalid_request_error"), Message: "WebRTC SDP exchange is not implemented for Gemini"},
	}
}

func (provider *GeminiProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

func (provider *GeminiProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

// ShouldStartRealtimeTurn starts a Bifrost turn when the client finalizes its
// content (clientContent with turnComplete, mapped to response.create) or commits
// buffered audio input (mirrors OpenAI's input_audio_buffer.committed trigger).
func (provider *GeminiProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	switch event.Type {
	case schemas.RTEventResponseCreate, schemas.RTEventInputAudioCommit:
		return true
	default:
		return false
	}
}

// RealtimeTurnFinalEvent — Gemini signals turn completion via
// serverContent.turnComplete, which we map onto the canonical response.done.
func (provider *GeminiProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseDone
}

func (provider *GeminiProvider) ShouldForwardRealtimeEvent(_ *schemas.BifrostRealtimeEvent) bool {
	return true
}

// ShouldAccumulateRealtimeOutput accumulates text/audio-transcript deltas so the
// full assistant turn text can be reconstructed for logging even though it only
// streams as deltas.
func (provider *GeminiProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	switch eventType {
	// RTEventResponseAudioDelta is included because Gemini bundles outputTranscription
	// into the *same* serverContent message as the audio parts (confirmed live: both
	// fields present together) — unlike OpenAI, which emits them as separate messages.
	// The transport appends whichever of Delta.Text/Delta.Transcript is non-empty, so
	// this is what makes the transcript actually get logged for audio-out turns.
	// RTEventResponseDone is included because Gemini's final serverContent message
	// can carry BOTH turnComplete AND the last modelTurn/outputTranscription chunk
	// together — that final chunk's Delta must still get accumulated for logging.
	case schemas.RTEventResponseTextDelta, schemas.RTEventResponseAudioDelta, schemas.RTEventResponseAudioTransDelta, schemas.RTEventInputAudioTransCompleted, schemas.RTEventResponseDone:
		return true
	default:
		return false
	}
}

// Gemini Live (BidiGenerateContent) wire-protocol pass-through event types.
// These have no clean 1:1 canonical equivalent, so they're cast rather than
// added to the shared core/schemas/realtime.go enum (same technique ElevenLabs
// uses for "ping"/"client_tool_call").
const (
	geminiEventToolCall             = schemas.RealtimeEventType("tool_call")
	geminiEventToolCallCancellation = schemas.RealtimeEventType("tool_call_cancellation")
	geminiEventToolResponse         = schemas.RealtimeEventType("tool_response")
	geminiEventInterrupted          = schemas.RealtimeEventType("interrupted")
	geminiEventGoAway               = schemas.RealtimeEventType("go_away")
)

// --- Gemini Live wire-format structs ---

type geminiRealtimeServerMessage struct {
	SetupComplete        json.RawMessage      `json:"setupComplete,omitempty"`
	ServerContent        *geminiServerContent `json:"serverContent,omitempty"`
	ToolCall             *geminiToolCall      `json:"toolCall,omitempty"`
	ToolCallCancellation json.RawMessage      `json:"toolCallCancellation,omitempty"`
	GoAway               json.RawMessage      `json:"goAway,omitempty"`
	UsageMetadata        *geminiUsageMetadata `json:"usageMetadata,omitempty"`
}

type geminiServerContent struct {
	ModelTurn           *geminiContent       `json:"modelTurn,omitempty"`
	TurnComplete        bool                 `json:"turnComplete,omitempty"`
	Interrupted         bool                 `json:"interrupted,omitempty"`
	InputTranscription  *geminiTranscription `json:"inputTranscription,omitempty"`
	OutputTranscription *geminiTranscription `json:"outputTranscription,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts,omitempty"`
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"` // base64
}

type geminiTranscription struct {
	Text     string `json:"text,omitempty"`
	Finished bool   `json:"finished,omitempty"`
}

type geminiToolCall struct {
	FunctionCalls []geminiFunctionCall `json:"functionCalls,omitempty"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount   int `json:"promptTokenCount,omitempty"`
	ResponseTokenCount int `json:"responseTokenCount,omitempty"`
	TotalTokenCount    int `json:"totalTokenCount,omitempty"`
}

// Client-sent (Bifrost -> Gemini) message shapes.

type geminiSetupMessage struct {
	Setup *geminiSetup `json:"setup"`
}

type geminiSetup struct {
	Model             string          `json:"model,omitempty"`
	GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
	SystemInstruction json.RawMessage `json:"systemInstruction,omitempty"`
	Tools             json.RawMessage `json:"tools,omitempty"`
	// OutputAudioTranscription: top-level per BidiGenerateContentSetup's protobuf shape
	// (nesting it under generationConfig, as one Google doc example for a different
	// model shows, was rejected outright: "Cannot find field" on gemini-3.1-flash-live-preview).
	// Confirmed live: enables a text transcript of the audio response, delivered inside
	// the SAME serverContent message as the audio parts — this is what makes T->T/S->T
	// achievable today despite Gemini Live having no TEXT-only responseModalities option.
	OutputAudioTranscription json.RawMessage `json:"outputAudioTranscription,omitempty"`
	InputAudioTranscription  json.RawMessage `json:"inputAudioTranscription,omitempty"`
}

type geminiGenerationConfig struct {
	ResponseModalities []string `json:"responseModalities,omitempty"`
}

type geminiClientContentMessage struct {
	ClientContent *geminiClientContent `json:"clientContent"`
}

type geminiClientContent struct {
	Turns        []geminiContent `json:"turns,omitempty"`
	TurnComplete bool            `json:"turnComplete"`
}

type geminiRealtimeInputMessage struct {
	RealtimeInput *geminiRealtimeInput `json:"realtimeInput"`
}

type geminiRealtimeInput struct {
	Audio *geminiInlineData `json:"audio,omitempty"`
}

// ToBifrostRealtimeEvent converts a raw Gemini Live server message into the
// canonical Bifrost realtime envelope.
func (provider *GeminiProvider) ToBifrostRealtimeEvent(providerEvent json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	var raw geminiRealtimeServerMessage
	if err := json.Unmarshal(providerEvent, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Gemini realtime event: %w", err)
	}

	event := &schemas.BifrostRealtimeEvent{
		RawData: providerEvent,
	}

	switch {
	case raw.SetupComplete != nil:
		event.Type = schemas.RTEventSessionCreated
		event.Session = &schemas.RealtimeSession{}

	case raw.ServerContent != nil:
		sc := raw.ServerContent
		// Gemini's terminal serverContent message can carry turnComplete AND the last
		// modelTurn/transcription chunk TOGETHER in the same frame (confirmed live) —
		// content extraction must not be gated behind an early turnComplete/interrupted
		// branch, or that final chunk is silently dropped from the event stream and from
		// turn-output accumulation.
		delta := &schemas.RealtimeDelta{}
		var text strings.Builder
		var audioBytes []byte
		if sc.ModelTurn != nil {
			for _, part := range sc.ModelTurn.Parts {
				if part.Text != "" {
					text.WriteString(part.Text)
				}
				// A single modelTurn can carry more than one audio part (the
				// BidiGenerateContent protobuf allows it) — decode and concatenate the
				// raw PCM from every audio part rather than keeping only the first, or
				// any part beyond the first is silently lost with no error or log.
				if part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/") {
					if decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data); err == nil {
						audioBytes = append(audioBytes, decoded...)
					}
				}
			}
		}
		audio := ""
		if len(audioBytes) > 0 {
			audio = base64.StdEncoding.EncodeToString(audioBytes)
			delta.Audio = audio
		}
		if text.Len() > 0 {
			delta.Text = text.String()
		}
		switch {
		case sc.OutputTranscription != nil:
			delta.Transcript = sc.OutputTranscription.Text
		case sc.InputTranscription != nil:
			delta.Transcript = sc.InputTranscription.Text
		}
		hasContent := audio != "" || text.Len() > 0 || delta.Transcript != ""
		if hasContent {
			event.Delta = delta
		}

		switch {
		case sc.TurnComplete:
			event.Type = schemas.RTEventResponseDone
		case sc.Interrupted:
			event.Type = geminiEventInterrupted
		case audio != "":
			event.Type = schemas.RTEventResponseAudioDelta
		case text.Len() > 0:
			event.Type = schemas.RTEventResponseTextDelta
		case sc.InputTranscription != nil:
			event.Type = schemas.RTEventInputAudioTransCompleted
		case sc.OutputTranscription != nil:
			event.Type = schemas.RTEventResponseAudioTransDelta
		default:
			event.Type = schemas.RealtimeEventType("server_content")
		}

	case raw.ToolCall != nil:
		event.Type = geminiEventToolCall
		if len(raw.ToolCall.FunctionCalls) > 0 {
			call := raw.ToolCall.FunctionCalls[0]
			args := ""
			if len(call.Args) > 0 {
				if sorted, err := providerUtils.MarshalSorted(json.RawMessage(call.Args)); err == nil {
					args = string(sorted)
				} else {
					args = string(call.Args)
				}
			}
			event.Item = &schemas.RealtimeItem{
				Type:      "function_call",
				Name:      call.Name,
				CallID:    call.ID,
				Arguments: args,
			}
			// Gemini supports parallel function calling — a single toolCall message can
			// carry more than one functionCall, but the canonical Item shape only has room
			// for one. Preserve the full list in ExtraParams (and RawData already has the
			// untouched original) rather than silently dropping calls beyond the first;
			// callers that need multi-call handling can read ExtraParams["function_calls"].
			if len(raw.ToolCall.FunctionCalls) > 1 {
				if allCalls, err := providerUtils.MarshalSorted(raw.ToolCall.FunctionCalls); err == nil {
					event.Item.ExtraParams = map[string]json.RawMessage{"function_calls": allCalls}
				}
			}
		}

	case raw.ToolCallCancellation != nil:
		event.Type = geminiEventToolCallCancellation

	case raw.GoAway != nil:
		event.Type = geminiEventGoAway

	default:
		event.Type = schemas.RealtimeEventType("unknown")
	}

	return event, nil
}

// extractTextFromItemContent pulls concatenated text out of a canonical
// RealtimeItem.Content payload, which mirrors OpenAI's content-part shape:
// either a plain string, or an array of {"type":"input_text","text":"..."}.
func extractTextFromItemContent(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}

	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}

	return ""
}

// ToProviderRealtimeEvent converts a canonical Bifrost realtime event into
// Gemini Live's native BidiGenerateContent client-message JSON.
//
// Known limitation (flagged in review, deliberately deferred): Gemini requires
// its `setup` message to be the connection's first frame — if a client sends
// conversation.item.create/input_audio_buffer.append/response.create before any
// session.update, this function has no way to inject a synthesized setup first,
// since translation is a pure per-event function with no connection-level state
// (the RealtimeProvider interface doesn't pass one in, and adding it would touch
// the shared contract used by OpenAI/Azure/ElevenLabs too). A compliant client —
// including Google's own SDKs — always configures the session before sending
// content, matching how every other realtime provider here is used in practice.
func (provider *GeminiProvider) ToProviderRealtimeEvent(bifrostEvent *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	switch bifrostEvent.Type {

	case schemas.RTEventSessionUpdate:
		setup := &geminiSetup{}
		if bifrostEvent.Session != nil {
			setup.Model = toGeminiModelResourceName(bifrostEvent.Session.Model)
			if bifrostEvent.Session.Instructions != "" {
				if instr, err := providerUtils.MarshalSorted(map[string]interface{}{
					"parts": []map[string]string{{"text": bifrostEvent.Session.Instructions}},
				}); err == nil {
					setup.SystemInstruction = instr
				}
			}
			// Session.Tools carries the client's canonical (OpenAI-shaped) tool array
			// as raw JSON — Gemini expects tools: [{functionDeclarations: [...]}], a
			// different wire shape, so it must go through the same converter the
			// non-realtime chat/responses paths use rather than being forwarded verbatim.
			if len(bifrostEvent.Session.Tools) > 0 {
				var chatTools []schemas.ChatTool
				if err := json.Unmarshal(bifrostEvent.Session.Tools, &chatTools); err == nil {
					if geminiTools, err := convertBifrostToolsToGemini(chatTools); err == nil && len(geminiTools) > 0 {
						if toolsJSON, err := providerUtils.MarshalSorted(geminiTools); err == nil {
							setup.Tools = toolsJSON
						}
					}
				}
			}
		}
		// Current Gemini Live models only support AUDIO output (TEXT responseModalities
		// is rejected at setup time, confirmed against the live endpoint) — always request
		// AUDIO and always enable outputAudioTranscription so a text delta is still
		// available as a side-channel. This is what makes T->T and S->T achievable today:
		// the client gets text via the transcript, audio via the delta, and can ignore
		// whichever it doesn't need.
		genCfg, err := providerUtils.MarshalSorted(geminiGenerationConfig{
			ResponseModalities: []string{"AUDIO"},
		})
		if err == nil {
			setup.GenerationConfig = genCfg
		}
		setup.OutputAudioTranscription = json.RawMessage("{}")
		setup.InputAudioTranscription = json.RawMessage("{}")
		return providerUtils.MarshalSorted(geminiSetupMessage{Setup: setup})

	case schemas.RTEventConversationItemCreate:
		// The canonical client protocol represents a submitted tool result as a
		// conversation.item.create with item.type="function_call_output" (see
		// schemas.IsRealtimeToolOutputEvent) — this must translate to Gemini's
		// toolResponse message, not a plain user text turn, or tool-calling round
		// trips break silently (the client-sent result never reaches Gemini).
		if bifrostEvent.Item != nil && bifrostEvent.Item.Type == "function_call_output" {
			return providerUtils.MarshalSorted(map[string]interface{}{
				"toolResponse": buildGeminiToolResponse(bifrostEvent.Item),
			})
		}
		// Otherwise: Gemini has no standalone "add item" concept — buffer this as a
		// non-final clientContent turn; the following response.create finalizes it.
		// Role is hardcoded "user" rather than read from bifrostEvent.Item.Role: Gemini's
		// Content.role only accepts "user"/"model", and function_call_output (the other
		// item type this path could see) is already routed above — so every remaining
		// item here is genuinely a user turn.
		content := geminiContent{Role: "user"}
		if bifrostEvent.Item != nil {
			text := extractTextFromItemContent(bifrostEvent.Item.Content)
			if text != "" {
				content.Parts = append(content.Parts, geminiPart{Text: text})
			}
		}
		return providerUtils.MarshalSorted(geminiClientContentMessage{
			ClientContent: &geminiClientContent{Turns: []geminiContent{content}, TurnComplete: false},
		})

	case schemas.RTEventResponseCreate:
		return providerUtils.MarshalSorted(geminiClientContentMessage{
			ClientContent: &geminiClientContent{TurnComplete: true},
		})

	case schemas.RTEventInputAudioAppend:
		// Clients populate the top-level Audio field (raw bytes, base64-encoded on the
		// wire) for input_audio_buffer.append; Delta.Audio is a fallback for callers that
		// pre-encoded it there instead (mirrors OpenAI's dual-check).
		audioB64 := ""
		if len(bifrostEvent.Audio) > 0 {
			audioB64 = base64.StdEncoding.EncodeToString(bifrostEvent.Audio)
		} else if bifrostEvent.Delta != nil {
			audioB64 = bifrostEvent.Delta.Audio
		}
		if audioB64 == "" {
			return nil, fmt.Errorf("audio must be set for input_audio_buffer.append events")
		}
		return providerUtils.MarshalSorted(geminiRealtimeInputMessage{
			RealtimeInput: &geminiRealtimeInput{
				Audio: &geminiInlineData{MimeType: "audio/pcm;rate=16000", Data: audioB64},
			},
		})

	case schemas.RTEventInputAudioCommit:
		return providerUtils.MarshalSorted(geminiClientContentMessage{
			ClientContent: &geminiClientContent{TurnComplete: true},
		})

	case geminiEventToolResponse:
		if bifrostEvent.Item == nil {
			return nil, nil
		}
		return providerUtils.MarshalSorted(map[string]interface{}{
			"toolResponse": buildGeminiToolResponse(bifrostEvent.Item),
		})

	default:
		// Unmapped client event types are intentionally dropped — returning a nil
		// payload here (checked by the transport before writing upstream) rather than
		// an empty JSON object, since Gemini's wire protocol is strictly typed and an
		// unrecognized top-level key can terminate the connection.
		return nil, nil
	}
}

// buildGeminiToolResponse builds a single functionResponses entry from a canonical
// tool-output RealtimeItem (item.type="function_call_output"). Gemini requires
// "response" to be a JSON object — Item.Output is a plain string on the canonical
// schema, so it's parsed and used only when it decodes to an object; any other
// JSON shape (bare number/bool/array/null) or invalid JSON falls back to being
// wrapped, since forwarding a non-object would violate Gemini's contract.
func buildGeminiToolResponse(item *schemas.RealtimeItem) map[string]interface{} {
	response := map[string]interface{}{"result": item.Output}
	if item.Output != "" {
		var parsed map[string]interface{}
		// json.Unmarshal of the JSON literal "null" into a map succeeds with err==nil
		// but leaves parsed as a nil map — guard against that too, or a literal "null"
		// output would silently become {"response":null} instead of the wrapped fallback.
		if err := json.Unmarshal([]byte(item.Output), &parsed); err == nil && parsed != nil {
			response = parsed
		}
	}
	return map[string]interface{}{
		"functionResponses": []map[string]interface{}{
			{"id": item.CallID, "name": item.Name, "response": response},
		},
	}
}

// ExtractRealtimeTurnUsage parses usageMetadata from the terminal serverContent
// event into Bifrost's canonical usage shape.
func (provider *GeminiProvider) ExtractRealtimeTurnUsage(terminalEventRaw []byte) *schemas.BifrostLLMUsage {
	if len(terminalEventRaw) == 0 {
		return nil
	}
	var raw geminiRealtimeServerMessage
	if err := json.Unmarshal(terminalEventRaw, &raw); err != nil || raw.UsageMetadata == nil {
		return nil
	}
	u := raw.UsageMetadata
	return &schemas.BifrostLLMUsage{
		PromptTokens:     u.PromptTokenCount,
		CompletionTokens: u.ResponseTokenCount,
		TotalTokens:      u.TotalTokenCount,
	}
}

// ExtractRealtimeTurnOutput synthesizes an assistant ChatMessage from the final
// modelTurn content, for turn-level logging.
func (provider *GeminiProvider) ExtractRealtimeTurnOutput(terminalEventRaw []byte) *schemas.ChatMessage {
	if len(terminalEventRaw) == 0 {
		return nil
	}
	var raw geminiRealtimeServerMessage
	if err := json.Unmarshal(terminalEventRaw, &raw); err != nil || raw.ServerContent == nil || raw.ServerContent.ModelTurn == nil {
		return nil
	}
	var text strings.Builder
	for _, part := range raw.ServerContent.ModelTurn.Parts {
		text.WriteString(part.Text)
	}
	if text.Len() == 0 {
		return nil
	}
	return &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text.String())},
	}
}
