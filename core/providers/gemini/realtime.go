package gemini

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unsafe"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// liveEndpointPath is the Live API (BidiGenerateContent) WebSocket endpoint under the API host.
const liveEndpointPath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

// SupportsRealtimeAPI reports that Gemini serves the Live API.
func (provider *GeminiProvider) SupportsRealtimeAPI() bool {
	return true
}

// RealtimeWebSocketURL returns the Live API endpoint. The model is not part of the URL: Live learns it
// from the setup frame.
func (provider *GeminiProvider) RealtimeWebSocketURL(_ *schemas.BifrostContext, _ schemas.Key, _, intent string) (string, *schemas.BifrostError) {
	if bifrostErr := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.RealtimeRequest); bifrostErr != nil {
		return "", bifrostErr
	}
	if intent == "transcription" {
		return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, provider.GetProviderKey())
	}
	endpoint, err := url.Parse(provider.networkConfig.BaseURL)
	if err != nil {
		return "", providerUtils.NewBifrostOperationError("invalid gemini base url", err)
	}
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	}
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/v1beta") + liveEndpointPath
	endpoint.RawQuery = ""
	return endpoint.String(), nil
}

// RealtimeHeaders authenticates the WebSocket handshake with the API key header, as the other Gemini
// endpoints do, so the key never appears in the URL.
func (provider *GeminiProvider) RealtimeHeaders(_ *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers := make(map[string]string, len(provider.networkConfig.ExtraHeaders)+1)
	for name, value := range provider.networkConfig.ExtraHeaders {
		headers[name] = value.GetValue()
	}
	if apiKey := key.Value.GetValue(); apiKey != "" {
		headers["x-goog-api-key"] = apiKey
	}
	return headers, nil
}

// SupportsRealtimeWebRTC reports that Live has no WebRTC signaling.
func (provider *GeminiProvider) SupportsRealtimeWebRTC() bool {
	return false
}

// ExchangeRealtimeWebRTCSDP is not supported by Live.
func (provider *GeminiProvider) ExchangeRealtimeWebRTCSDP(_ *schemas.BifrostContext, _ schemas.Key, _ string, _ string, _ json.RawMessage) (string, *schemas.BifrostError) {
	return "", providerUtils.NewUnsupportedOperationError(schemas.RealtimeRequest, provider.GetProviderKey())
}

// NewRealtimeTranslator returns the per-connection translator between the OpenAI Realtime protocol
// the client speaks and Live's frames.
func (provider *GeminiProvider) NewRealtimeTranslator(_ schemas.Key, model string) (schemas.RealtimeTranslator, *schemas.BifrostError) {
	if strings.TrimSpace(model) == "" {
		return nil, providerUtils.NewBifrostOperationError("model is required for a gemini live session", nil)
	}
	translator, err := newLiveTranslator(model)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create the gemini live translator", err)
	}
	return translator, nil
}

// The methods below serve a client that speaks Live itself, such as the Google GenAI SDK on /genai.
// Its frames are relayed as they are; these methods only tell the turn pipeline what each frame
// means and read a turn's usage off the frame that ends it. A client that speaks OpenAI Realtime is
// served by the translator and never reaches them.

// LiveSetupModel returns the model a client's setup frame asks for, which Live carries in that first
// frame rather than the URL. A Gemini client writes it as "models/<name>"; Bifrost addresses it as
// "<name>", optionally behind a "<provider>/" prefix.
func LiveSetupModel(frame []byte) (string, error) {
	if !providerUtils.JSONFieldExists(frame, "setup") {
		return "", errors.New("the first message of a gemini live session must be a setup message")
	}
	model := strings.TrimPrefix(strings.TrimSpace(providerUtils.GetJSONField(frame, "setup.model").String()), "models/")
	if model == "" {
		return "", errors.New("setup.model is required")
	}
	return model, nil
}

// LiveSetupWithModel returns the client's setup frame addressed to the model Bifrost resolved, which
// differs from the client's when a key aliases it or the client named a provider.
func LiveSetupWithModel(frame []byte, model string) ([]byte, error) {
	return providerUtils.SetJSONField(frame, "setup.model", "models/"+model)
}

// ToBifrostRealtimeEvent classifies one Live frame, from the client or from Live, as the event the
// turn pipeline understands. The frame stays the event's RawData, which is what gets relayed, so no
// frame is ever rewritten and an unrecognised one is simply relayed.
func (provider *GeminiProvider) ToBifrostRealtimeEvent(frame json.RawMessage) (*schemas.BifrostRealtimeEvent, error) {
	return liveFrameEvent(frame), nil
}

// ToProviderRealtimeEvent returns the client's frame as it came in.
func (provider *GeminiProvider) ToProviderRealtimeEvent(event *schemas.BifrostRealtimeEvent) (json.RawMessage, error) {
	if len(event.RawData) == 0 {
		return nil, errors.New("a gemini live frame is relayed as it came in, and this event has none")
	}
	return event.RawData, nil
}

// ShouldStartRealtimeTurn reports whether the event opens a turn: the client submitting input for the
// model to answer, or the model starting to answer input it detected itself.
func (provider *GeminiProvider) ShouldStartRealtimeTurn(event *schemas.BifrostRealtimeEvent) bool {
	switch event.Type {
	case schemas.RTEventResponseCreate, schemas.RTEventResponseAudioDelta, schemas.RTEventResponseTextDelta,
		schemas.RTEventResponseAudioTransDelta, schemas.RTEventResponseOutputItemDone:
		return true
	}
	return false
}

// RealtimeTurnFinalEvent is the event a frame with turnComplete becomes.
func (provider *GeminiProvider) RealtimeTurnFinalEvent() schemas.RealtimeEventType {
	return schemas.RTEventResponseDone
}

func (provider *GeminiProvider) RealtimeWebRTCDataChannelLabel() string {
	return ""
}

func (provider *GeminiProvider) RealtimeWebSocketSubprotocol() string {
	return ""
}

func (provider *GeminiProvider) ShouldForwardRealtimeEvent(*schemas.BifrostRealtimeEvent) bool {
	return true
}

// ShouldAccumulateRealtimeOutput collects the text the model speaks or writes for the turn's log. The
// frame that ends a turn can carry the last of it.
func (provider *GeminiProvider) ShouldAccumulateRealtimeOutput(eventType schemas.RealtimeEventType) bool {
	switch eventType {
	case schemas.RTEventResponseTextDelta, schemas.RTEventResponseAudioTransDelta, schemas.RTEventResponseDone:
		return true
	}
	return false
}

// ExtractRealtimeTurnUsage reads the turn's usage off the frame that ended it, counted the way the
// OpenAI-protocol route counts it.
func (provider *GeminiProvider) ExtractRealtimeTurnUsage(terminalFrame []byte) *schemas.BifrostLLMUsage {
	raw := providerUtils.GetJSONField(terminalFrame, "usageMetadata").Raw
	if raw == "" {
		return nil
	}
	var metadata GeminiLiveUsageMetadata
	if schemas.Unmarshal([]byte(raw), &metadata) != nil {
		return nil
	}
	var usage liveUsage
	usage.add(&metadata)
	return usage.bifrost()
}

// ExtractRealtimeTurnOutput leaves the turn's output to the text collected from its frames.
func (provider *GeminiProvider) ExtractRealtimeTurnOutput([]byte) *schemas.ChatMessage {
	return nil
}

// liveFrame is what one pass over a Live frame finds in it.
type liveFrame struct {
	turnComplete bool // from Live
	toolCall     bool
	modelTurn    bool // the frame carries the model's parts
	audio        bool
	outputText   string // text parts, in a text-only session
	outputSpeech string // the transcript of the audio the model speaks
	inputSpeech  string // the transcript of the audio the user spoke

	audioIn     bool // from the client
	audioEnd    bool
	submitsTurn bool   // the client asks for an answer
	inputText   string // the client's text, kept for the turn's log
	holdsText   bool   // the client's text waits for a later turn
}

// liveFrameEvent classifies a frame by walking it once and copying nothing but the text it extracts,
// because a voice session streams audio frames continuously in both directions.
func liveFrameEvent(frame []byte) *schemas.BifrostRealtimeEvent {
	text := unsafe.String(unsafe.SliceData(frame), len(frame))
	var f liveFrame
	gjson.Parse(text).ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "serverContent":
			f.readServerContent(value)
		case "toolCall":
			f.toolCall = true
		case "realtimeInput", "realtime_input":
			f.readRealtimeInput(value)
		case "clientContent", "client_content":
			f.readClientContent(value)
		case "toolResponse", "tool_response":
			f.submitsTurn = true
		}
		return true
	})
	if f.modelTurn {
		// Looked up by path, which stops at the audio's MIME type instead of reading the audio itself.
		if gjson.Get(text, "serverContent.modelTurn.parts.0.inlineData.mimeType").Exists() {
			f.audio = true
		} else {
			gjson.Get(text, "serverContent.modelTurn.parts").ForEach(func(_, part gjson.Result) bool {
				if answer := part.Get("text"); answer.Exists() && !part.Get("thought").Bool() {
					f.outputText += answer.String()
				}
				return true
			})
		}
	}

	event := &schemas.BifrostRealtimeEvent{RawData: frame}
	if f.outputText != "" || f.outputSpeech != "" {
		event.Delta = &schemas.RealtimeDelta{Text: f.outputText, Transcript: f.outputSpeech}
	}
	switch {
	case f.turnComplete:
		event.Type = schemas.RTEventResponseDone
	case f.toolCall:
		event.Type = schemas.RTEventResponseOutputItemDone
	case f.outputText != "":
		event.Type = schemas.RTEventResponseTextDelta
	case f.outputSpeech != "":
		event.Type = schemas.RTEventResponseAudioTransDelta
	case f.audio:
		event.Type = schemas.RTEventResponseAudioDelta
	case f.inputSpeech != "":
		event.Type = schemas.RTEventInputAudioTransCompleted
		transcript, _ := providerUtils.MarshalSorted(f.inputSpeech)
		event.ExtraParams = map[string]json.RawMessage{"transcript": transcript}
	case f.submitsTurn:
		event.Type = schemas.RTEventResponseCreate
		if f.inputText != "" {
			event.ExtraParams = map[string]json.RawMessage{"response": liveResponseInput(f.inputText)}
		}
	case f.holdsText:
		event.Type = schemas.RTEventConversationItemCreate
		content, _ := providerUtils.MarshalSorted(liveInputMessageFor(f.inputText).Content)
		event.Item = &schemas.RealtimeItem{Type: "message", Role: "user", Content: content}
	case f.audioIn:
		event.Type = schemas.RTEventInputAudioAppend
	case f.audioEnd:
		event.Type = schemas.RTEventInputAudioCommit
	}
	return event
}

func (f *liveFrame) readServerContent(content gjson.Result) {
	content.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "turnComplete":
			f.turnComplete = value.Bool()
		case "outputTranscription":
			f.outputSpeech = value.Get("text").String()
		case "inputTranscription":
			f.inputSpeech = value.Get("text").String()
		case "modelTurn":
			f.modelTurn = true
		}
		return true
	})
}

func (f *liveFrame) readRealtimeInput(input gjson.Result) {
	input.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "audio", "mediaChunks", "media_chunks":
			f.audioIn = true
		case "audioStreamEnd", "audio_stream_end":
			f.audioEnd = value.Bool()
		case "activityEnd", "activity_end":
			f.submitsTurn = true
		case "text":
			f.submitsTurn = true
			f.inputText = value.String()
		}
		return true
	})
}

func (f *liveFrame) readClientContent(content gjson.Result) {
	var text []string
	content.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "turnComplete", "turn_complete":
			f.submitsTurn = value.Bool()
		case "turns":
			value.ForEach(func(_, turn gjson.Result) bool {
				turn.Get("parts").ForEach(func(_, part gjson.Result) bool {
					if part.Get("text").Exists() {
						text = append(text, part.Get("text").String())
					}
					return true
				})
				return true
			})
		}
		return true
	})
	f.inputText = strings.Join(text, "\n")
	f.holdsText = !f.submitsTurn && f.inputText != ""
}

// liveInputMessage is an OpenAI user message, the shape the turn pipeline reads a turn's input from.
type liveInputMessage struct {
	Type    string     `json:"type"`
	Role    string     `json:"role"`
	Content []livePart `json:"content"`
}

func liveInputMessageFor(text string) liveInputMessage {
	return liveInputMessage{Type: "message", Role: "user", Content: []livePart{{Type: "input_text", Text: &text}}}
}

// liveResponseInput is the response.create body that carries the text the user submitted.
func liveResponseInput(text string) json.RawMessage {
	body, _ := providerUtils.MarshalSorted(struct {
		Input []liveInputMessage `json:"input"`
	}{[]liveInputMessage{liveInputMessageFor(text)}})
	return body
}
