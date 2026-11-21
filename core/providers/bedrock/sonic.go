package bedrock

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

const (
	sonicDefaultMaxTokens    = 1024
	sonicDefaultInstructions = "You are a helpful assistant."
	sonicMaxOutputTokens     = 65535
	sonicDefaultVoice        = "matthew"
	sonicDefaultSampleRate   = 24000
	sonicMediaTypeText       = "text/plain"
	sonicMediaTypeAudio      = "audio/lpcm"
	sonicMediaTypeJSON       = "application/json"

	sonicStageFinal       = "FINAL"
	sonicStageSpeculative = "SPECULATIVE"
	sonicDroppedContent   = "DROPPED" // FINAL text of a reply that already closed
)

// realtimeCodec: clients speak OpenAI Realtime; the connection translates it to Nova Sonic.
var realtimeCodec = &openai.OpenAIProvider{}

// sonicSession translates OpenAI Realtime events to and from Nova Sonic stream events.
type sonicSession struct {
	model      string
	sessionID  string
	promptName string
	seq        atomic.Uint64 // shared by both halves
	idSeq      atomic.Uint64

	// client → model
	config           sonicSessionConfig
	started          bool
	liveInput        bool
	audioContentName string
	history          []sonicHistoryMessage

	// model → client
	contents       map[string]sonicContent
	userItemID     string
	userTranscript strings.Builder
	response       *sonicResponse
	dropFinal      bool // the reply already closed; its late FINAL text opens no response
	usageTotal     BedrockSonicUsageDirection
	usageBilled    BedrockSonicUsageDirection
}

// sonicSessionConfig is the session configuration accumulated from session.update events.
type sonicSessionConfig struct {
	instructions string
	voice        string
	inputRate    int
	outputRate   int
	maxTokens    int
	temperature  *float64
	endpointing  string
	tools        []BedrockSonicTool
	rawTools     json.RawMessage
	toolChoice   json.RawMessage
}

// sonicHistoryMessage is a text turn sent as conversation history before audio starts.
type sonicHistoryMessage struct {
	role string
	text string
}

// sonicContent remembers what an open model content block carries.
type sonicContent struct {
	kind  string // TEXT, AUDIO or TOOL
	role  string // USER, ASSISTANT or TOOL
	stage string // FINAL or SPECULATIVE; empty when the model sends none
}

// sonicResponse is the assistant response currently being streamed to the client.
type sonicResponse struct {
	id                string
	messageItemID     string
	messageIndex      int
	audioSent         bool
	cancelled         bool
	speculative       strings.Builder
	final             strings.Builder
	outputs           []map[string]any
	messageItemOpened bool
}

// newSonicSession prepares a translator with Nova Sonic defaults for an OpenAI-shaped client.
func newSonicSession(model string) *sonicSession {
	return &sonicSession{
		model:      model,
		sessionID:  "sess_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		promptName: uuid.NewString(),
		contents:   map[string]sonicContent{},
		config: sonicSessionConfig{
			voice:      sonicDefaultVoice,
			inputRate:  sonicDefaultSampleRate,
			outputRate: sonicDefaultSampleRate,
			maxTokens:  sonicDefaultMaxTokens,
		},
	}
}

// nextEventID returns a session-unique event id for synthesized client events.
func (s *sonicSession) nextEventID() string {
	return "event_" + s.sessionID[5:17] + "_" + strconv.FormatUint(s.seq.Add(1), 10)
}

// nextID returns a session-unique id with the given prefix for items and responses.
func (s *sonicSession) nextID(prefix string) string {
	return prefix + "_" + s.sessionID[5:17] + strconv.FormatUint(s.idSeq.Add(1), 10)
}

// sessionCreated is the first event a client receives, before any upstream traffic.
func (s *sonicSession) sessionCreated() []byte {
	return s.clientMessage(map[string]any{"type": "session.created", "session": s.sessionObject()})
}

// sessionObject renders the effective session in the OpenAI GA shape.
func (s *sonicSession) sessionObject() map[string]any {
	session := map[string]any{
		"id":                s.sessionID,
		"object":            "realtime.session",
		"type":              "realtime",
		"model":             s.model,
		"output_modalities": []string{"audio"},
		"max_output_tokens": s.config.maxTokens,
		"audio": map[string]any{
			"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": s.config.inputRate}},
			"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": s.config.outputRate}, "voice": s.config.voice},
		},
	}
	if s.config.instructions != "" {
		session["instructions"] = s.config.instructions
	}
	if len(s.config.rawTools) > 0 {
		session["tools"] = s.config.rawTools
	}
	if len(s.config.toolChoice) > 0 {
		session["tool_choice"] = s.config.toolChoice
	}
	return session
}

// clientEvent returns the Nova Sonic events for one client event plus any direct replies.
func (s *sonicSession) clientEvent(raw []byte) (upstream [][]byte, replies [][]byte) {
	eventType := gjson.GetBytes(raw, "type").String()
	switch eventType {
	case string(schemas.RTEventSessionUpdate):
		if s.started {
			return nil, s.errorReply("invalid_request_error", "session_update_after_start", "Nova Sonic fixes the session configuration when the conversation starts; send session.update before audio or response.create")
		}
		if err := sonicAudioOutput(gjson.GetBytes(raw, "session")); err != nil {
			return nil, s.errorReply("invalid_request_error", "unsupported_modalities", err.Error())
		}
		if err := s.applySessionUpdate(gjson.GetBytes(raw, "session")); err != nil {
			return nil, s.errorReply("invalid_request_error", "invalid_session_config", err.Error())
		}
		return nil, [][]byte{s.clientMessage(map[string]any{"type": "session.updated", "session": s.sessionObject()})}

	case string(schemas.RTEventInputAudioAppend):
		audio := gjson.GetBytes(raw, "audio").String()
		if audio == "" {
			return nil, s.errorReply("invalid_request_error", "missing_audio", "input_audio_buffer.append requires audio")
		}
		upstream = s.ensureStarted(false)
		return append(upstream, s.input(&BedrockSonicInputEventBody{AudioInput: &BedrockSonicContentInput{
			PromptName: s.promptName, ContentName: s.audioContentName, Content: audio,
		}})), nil

	case string(schemas.RTEventConversationItemCreate):
		return s.conversationItem(gjson.GetBytes(raw, "item"))

	case string(schemas.RTEventResponseCreate):
		if err := sonicAudioOutput(gjson.GetBytes(raw, "response")); err != nil {
			return nil, s.errorReply("invalid_request_error", "unsupported_modalities", err.Error())
		}
		// Nova Sonic answers on its own once input arrives; response.create only starts the stream.
		return s.ensureStarted(true), nil

	case string(schemas.RTEventInputAudioCommit), string(schemas.RTEventInputAudioClear),
		string(schemas.RTEventResponseCancel), string(schemas.RTEventConversationItemDelete),
		"conversation.item.truncate", "conversation.item.retrieve", "output_audio_buffer.clear":
		// Nova Sonic owns turn detection and barge-in; these have no upstream equivalent.
		return nil, nil
	}
	return nil, s.errorReply("invalid_request_error", "unsupported_event", fmt.Sprintf("event type %q is not supported by Nova Sonic", eventType))
}

// applySessionUpdate merges the fields a session.update carries into the pending configuration.
func (s *sonicSession) applySessionUpdate(session gjson.Result) error {
	if !session.Exists() {
		return nil
	}
	cfg := s.config
	if v := session.Get("instructions"); v.Exists() {
		cfg.instructions = v.String()
	}
	if v := firstString(session, "audio.output.voice", "voice"); v != "" {
		cfg.voice = v
	}
	if v := session.Get("temperature"); v.Exists() {
		t := v.Float()
		if t < 0 || t > 1 {
			return fmt.Errorf("temperature %v is not supported; Nova Sonic accepts 0.0 to 1.0", t)
		}
		cfg.temperature = &t
	}
	if v := firstExisting(session, "max_output_tokens", "max_response_output_tokens"); v.Exists() && v.Type == gjson.Number {
		n := v.Int()
		if n < 1 || n > sonicMaxOutputTokens {
			return fmt.Errorf("max_output_tokens %d is not supported; Nova Sonic accepts 1 to %d", n, sonicMaxOutputTokens)
		}
		cfg.maxTokens = int(n)
	}
	if v := firstString(session, "audio.input.turn_detection.eagerness", "turn_detection.eagerness"); v != "" {
		switch strings.ToLower(v) {
		case "low", "medium", "high":
			cfg.endpointing = strings.ToUpper(v)
		case "auto":
			cfg.endpointing = ""
		default:
			return fmt.Errorf("unsupported turn_detection.eagerness %q", v)
		}
	}
	rate, err := sonicAudioRate(session, "audio.input.format", "input_audio_format", cfg.inputRate)
	if err != nil {
		return fmt.Errorf("input audio: %w", err)
	}
	cfg.inputRate = rate
	if rate, err = sonicAudioRate(session, "audio.output.format", "output_audio_format", cfg.outputRate); err != nil {
		return fmt.Errorf("output audio: %w", err)
	}
	cfg.outputRate = rate
	if v := session.Get("tools"); v.Exists() {
		tools, err := sonicTools(v)
		if err != nil {
			return err
		}
		cfg.tools = tools
		cfg.rawTools = json.RawMessage(v.Raw)
	}
	if v := session.Get("tool_choice"); v.Exists() {
		cfg.toolChoice = json.RawMessage(v.Raw)
	}
	if _, err := sonicToolConfig(cfg.tools, cfg.toolChoice); err != nil {
		return err
	}
	s.config = cfg
	return nil
}

// sonicAudioOutput rejects output modalities without audio, since Nova Sonic always answers out loud.
func sonicAudioOutput(root gjson.Result) error {
	modalities := firstExisting(root, "output_modalities", "modalities")
	if !modalities.Exists() {
		return nil
	}
	for _, m := range modalities.Array() {
		if m.String() == "audio" {
			return nil
		}
	}
	return fmt.Errorf("output_modalities %s is not supported; Nova Sonic always answers with audio, so include \"audio\"", modalities.Raw)
}

// sonicAudioRate reads a GA format object or a beta format string and checks Nova Sonic supports it.
func sonicAudioRate(session gjson.Result, gaPath, betaPath string, current int) (int, error) {
	if format := session.Get(gaPath); format.Exists() {
		if t := format.Get("type").String(); t != "" && t != "audio/pcm" {
			return 0, fmt.Errorf("format %q is not supported; Nova Sonic accepts audio/pcm only", t)
		}
		if rate := format.Get("rate"); rate.Exists() {
			switch r := int(rate.Int()); r {
			case 8000, 16000, 24000:
				return r, nil
			default:
				return 0, fmt.Errorf("sample rate %d is not supported; use 8000, 16000 or 24000", r)
			}
		}
		return current, nil
	}
	if format := session.Get(betaPath); format.Exists() {
		if format.String() != "pcm16" {
			return 0, fmt.Errorf("format %q is not supported; Nova Sonic accepts pcm16 only", format.String())
		}
		return sonicDefaultSampleRate, nil
	}
	return current, nil
}

// sonicToolConfig maps tools and an OpenAI tool_choice to Nova Sonic's tool configuration; nil means no tools.
func sonicToolConfig(tools []BedrockSonicTool, choice json.RawMessage) (*BedrockSonicToolConfig, error) {
	switch c := gjson.ParseBytes(choice); {
	case len(choice) == 0, c.String() == "auto":
	case c.String() == "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("tool_choice %s is not supported; Nova Sonic accepts auto or none", c.Raw)
	}
	if len(tools) == 0 {
		return nil, nil
	}
	return &BedrockSonicToolConfig{Tools: tools}, nil
}

// sonicTools converts OpenAI Realtime function tools into Nova Sonic tool specs.
func sonicTools(tools gjson.Result) ([]BedrockSonicTool, error) {
	if !tools.IsArray() {
		return nil, fmt.Errorf("tools must be an array")
	}
	var out []BedrockSonicTool
	var convErr error
	tools.ForEach(func(_, tool gjson.Result) bool {
		if t := tool.Get("type").String(); t != "" && t != "function" {
			convErr = fmt.Errorf("tool type %q is not supported by Nova Sonic", t)
			return false
		}
		name := tool.Get("name").String()
		if name == "" {
			convErr = fmt.Errorf("function tools require a name")
			return false
		}
		schema := "{}"
		if params := tool.Get("parameters"); params.Exists() {
			var parsed any
			if err := sonic.Unmarshal([]byte(params.Raw), &parsed); err != nil {
				convErr = fmt.Errorf("tool %q has invalid parameters: %w", name, err)
				return false
			}
			sorted, err := providerUtils.MarshalSorted(parsed)
			if err != nil {
				convErr = err
				return false
			}
			schema = string(sorted)
		}
		out = append(out, BedrockSonicTool{ToolSpec: BedrockSonicToolSpec{
			Name: name, Description: tool.Get("description").String(), InputSchema: BedrockSonicToolInputJSON{JSON: schema},
		}})
		return true
	})
	return out, convErr
}

// conversationItem maps a conversation.item.create onto history, cross-modal text or a tool result.
func (s *sonicSession) conversationItem(item gjson.Result) (upstream [][]byte, replies [][]byte) {
	switch item.Get("type").String() {
	case "message":
		role := item.Get("role").String()
		text := sonicItemText(item.Get("content"))
		if text == "" {
			return nil, s.errorReply("invalid_request_error", "unsupported_content", "Nova Sonic conversation items must carry text content")
		}
		switch role {
		case "system":
			if s.started {
				return nil, s.errorReply("invalid_request_error", "history_after_start", "system messages must be sent before the conversation starts")
			}
			s.config.instructions = joinNonEmpty(s.config.instructions, text)
			return nil, nil
		case "user", "assistant":
			if !s.started {
				s.history = append(s.history, sonicHistoryMessage{role: role, text: text})
				return nil, nil
			}
			if role == "assistant" {
				return nil, s.errorReply("invalid_request_error", "history_after_start", "assistant history must be sent before the conversation starts")
			}
			return s.textBlock("USER", text, true), nil
		}
		return nil, s.errorReply("invalid_request_error", "unsupported_role", fmt.Sprintf("message role %q is not supported by Nova Sonic", role))

	case "function_call_output":
		callID := item.Get("call_id").String()
		if callID == "" {
			return nil, s.errorReply("invalid_request_error", "missing_call_id", "function_call_output requires call_id")
		}
		if !s.started {
			return nil, s.errorReply("invalid_request_error", "tool_output_before_start", "function_call_output must answer a tool call from this session")
		}
		name := s.nextContentName()
		return [][]byte{
			s.input(&BedrockSonicInputEventBody{ContentStart: &BedrockSonicContentStart{
				PromptName: s.promptName, ContentName: name, Type: "TOOL", Role: "TOOL",
				ToolResultInputConfiguration: &BedrockSonicToolResultConfig{
					ToolUseID: callID, Type: "TEXT", TextInputConfiguration: BedrockSonicMediaType{MediaType: sonicMediaTypeText},
				},
			}}),
			s.input(&BedrockSonicInputEventBody{ToolResult: &BedrockSonicContentInput{PromptName: s.promptName, ContentName: name, Content: item.Get("output").String()}}),
			s.input(&BedrockSonicInputEventBody{ContentEnd: &BedrockSonicContentRef{PromptName: s.promptName, ContentName: name}}),
		}, nil
	}
	return nil, s.errorReply("invalid_request_error", "unsupported_item", fmt.Sprintf("conversation item type %q is not supported by Nova Sonic", item.Get("type").String()))
}

// sonicItemText concatenates the text parts of an OpenAI Realtime message item.
func sonicItemText(content gjson.Result) string {
	var parts []string
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "input_text", "text", "output_text":
			if t := part.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		}
		return true
	})
	return strings.Join(parts, "\n")
}

// ensureStarted opens session, prompt, history and audio block once; trailing user text can be answered live.
func (s *sonicSession) ensureStarted(answerTrailingUser bool) [][]byte {
	if s.started {
		return nil
	}
	s.started = true
	cfg := s.config
	var events [][]byte

	start := &BedrockSonicSessionStart{InferenceConfiguration: BedrockSonicInferenceConfig{
		MaxTokens: cfg.maxTokens, Temperature: cfg.temperature,
	}}
	if cfg.endpointing != "" {
		start.TurnDetectionConfiguration = &BedrockSonicTurnDetectionConfig{EndpointingSensitivity: cfg.endpointing}
	}
	events = append(events, s.input(&BedrockSonicInputEventBody{SessionStart: start}))

	prompt := &BedrockSonicPromptStart{
		PromptName:              s.promptName,
		TextOutputConfiguration: BedrockSonicMediaType{MediaType: sonicMediaTypeText},
		AudioOutputConfiguration: BedrockSonicAudioConfig{
			MediaType: sonicMediaTypeAudio, SampleRateHertz: cfg.outputRate, SampleSizeBits: 16, ChannelCount: 1,
			VoiceID: cfg.voice, Encoding: "base64", AudioType: "SPEECH",
		},
	}
	if toolConfig, _ := sonicToolConfig(cfg.tools, cfg.toolChoice); toolConfig != nil {
		prompt.ToolUseOutputConfiguration = &BedrockSonicMediaType{MediaType: sonicMediaTypeJSON}
		prompt.ToolConfiguration = toolConfig
	}
	events = append(events, s.input(&BedrockSonicInputEventBody{PromptStart: prompt}))

	instructions := cfg.instructions
	if instructions == "" {
		instructions = sonicDefaultInstructions // Nova Sonic refuses a conversation whose first content is not SYSTEM
	}
	events = append(events, s.textBlock("SYSTEM", instructions, false)...)
	history := s.history
	var trailing []sonicHistoryMessage
	if answerTrailingUser {
		for len(history) > 0 && history[len(history)-1].role == "user" {
			trailing = append([]sonicHistoryMessage{history[len(history)-1]}, trailing...)
			history = history[:len(history)-1]
		}
	}
	for _, msg := range history {
		events = append(events, s.textBlock(strings.ToUpper(msg.role), msg.text, false)...)
	}
	s.history = nil

	s.audioContentName = s.nextContentName()
	events = append(events, s.input(&BedrockSonicInputEventBody{ContentStart: &BedrockSonicContentStart{
		PromptName: s.promptName, ContentName: s.audioContentName, Type: "AUDIO", Interactive: true, Role: "USER",
		AudioInputConfiguration: &BedrockSonicAudioConfig{
			MediaType: sonicMediaTypeAudio, SampleRateHertz: cfg.inputRate, SampleSizeBits: 16, ChannelCount: 1,
			Encoding: "base64", AudioType: "SPEECH",
		},
	}}))
	for _, msg := range trailing {
		events = append(events, s.textBlock("USER", msg.text, true)...)
	}
	return events
}

// textBlock wraps text in a contentStart/textInput/contentEnd triple.
func (s *sonicSession) textBlock(role, text string, interactive bool) [][]byte {
	name := s.nextContentName()
	return [][]byte{
		s.input(&BedrockSonicInputEventBody{ContentStart: &BedrockSonicContentStart{
			PromptName: s.promptName, ContentName: name, Type: "TEXT", Interactive: interactive, Role: role,
			TextInputConfiguration: &BedrockSonicMediaType{MediaType: sonicMediaTypeText},
		}}),
		s.input(&BedrockSonicInputEventBody{TextInput: &BedrockSonicContentInput{PromptName: s.promptName, ContentName: name, Content: text}}),
		s.input(&BedrockSonicInputEventBody{ContentEnd: &BedrockSonicContentRef{PromptName: s.promptName, ContentName: name}}),
	}
}

// closeEvents ends the audio block, prompt and session so Bedrock releases them cleanly.
func (s *sonicSession) closeEvents() [][]byte {
	if !s.started {
		return nil
	}
	return [][]byte{
		s.input(&BedrockSonicInputEventBody{ContentEnd: &BedrockSonicContentRef{PromptName: s.promptName, ContentName: s.audioContentName}}),
		s.input(&BedrockSonicInputEventBody{PromptEnd: &BedrockSonicPromptRef{PromptName: s.promptName}}),
		s.input(&BedrockSonicInputEventBody{SessionEnd: &struct{}{}}),
	}
}

func (s *sonicSession) nextContentName() string {
	return uuid.NewString()
}

// input marshals one Nova Sonic input event. Every field is a typed struct, so it cannot fail.
func (s *sonicSession) input(body *BedrockSonicInputEventBody) []byte {
	out, _ := providerUtils.MarshalSorted(BedrockSonicInputEvent{Event: *body})
	return out
}

// modelEvent translates one Nova Sonic output event into OpenAI Realtime server events.
func (s *sonicSession) modelEvent(raw []byte) [][]byte {
	var event BedrockSonicOutputEvent
	if err := sonic.Unmarshal(raw, &event); err != nil {
		return s.errorReply("server_error", "invalid_upstream_event", "failed to decode Nova Sonic event")
	}
	ev := &event.Event
	switch {
	case ev.ContentStart != nil:
		return s.contentStart(ev.ContentStart)
	case ev.TextOutput != nil:
		return s.textOutput(ev.TextOutput)
	case ev.AudioOutput != nil:
		return s.audioOutput(ev.AudioOutput)
	case ev.ToolUse != nil:
		return s.toolUse(ev.ToolUse)
	case ev.ContentEnd != nil:
		return s.contentEnd(ev.ContentEnd)
	case ev.CompletionEnd != nil:
		return s.finishResponse()
	case ev.UsageEvent != nil:
		s.usageTotal = ev.UsageEvent.Details.Total
	}
	return nil
}

func (s *sonicSession) contentStart(c *BedrockSonicOutputContent) [][]byte {
	content := sonicContent{kind: c.Type, role: c.Role, stage: gjson.Get(c.AdditionalModelFields, "generationStage").String()}
	s.contents[c.ContentID] = content
	if content.role == "USER" {
		// A new user turn closes whatever the assistant was still saying.
		s.dropFinal = false
		out := s.finishResponse()
		if s.userItemID == "" {
			s.userItemID = s.nextID("item")
			s.userTranscript.Reset()
			out = append(out, s.clientMessage(map[string]any{"type": "input_audio_buffer.committed", "item_id": s.userItemID}))
			item := map[string]any{
				"id": s.userItemID, "object": "realtime.item", "type": "message", "status": "completed", "role": "user",
				"content": []any{map[string]any{"type": "input_audio", "transcript": nil}},
			}
			out = append(out,
				s.clientMessage(map[string]any{"type": "conversation.item.added", "item": item}),
				s.clientMessage(map[string]any{"type": "conversation.item.done", "item": item}),
			)
		}
		return out
	}
	if content.kind == "TEXT" && content.stage == sonicStageFinal && s.dropFinal && s.response == nil {
		content.kind = sonicDroppedContent
		s.contents[c.ContentID] = content
		return nil
	}
	s.dropFinal = false
	out := s.ensureResponse()
	if content.kind == "TEXT" || content.kind == "AUDIO" {
		out = append(out, s.ensureMessageItem()...)
	}
	return out
}

func (s *sonicSession) textOutput(c *BedrockSonicOutputContent) [][]byte {
	content, ok := s.contents[c.ContentID]
	if !ok {
		content = sonicContent{kind: "TEXT", role: c.Role}
	}
	if content.role == "USER" {
		if s.userItemID == "" {
			return nil
		}
		if content.stage != sonicStageSpeculative {
			s.userTranscript.WriteString(c.Content)
		}
		return [][]byte{s.clientMessage(map[string]any{
			"type": "conversation.item.input_audio_transcription.delta", "item_id": s.userItemID, "content_index": 0, "delta": c.Content,
		})}
	}
	if gjson.Get(c.Content, "interrupted").Bool() {
		// Barge-in: OpenAI clients flush playback when they see speech_started.
		if s.response != nil {
			s.response.cancelled = true
		}
		return [][]byte{s.clientMessage(map[string]any{"type": "input_audio_buffer.speech_started", "audio_start_ms": 0})}
	}
	if content.kind == sonicDroppedContent {
		return nil
	}
	out := s.ensureResponse()
	out = append(out, s.ensureMessageItem()...)
	r := s.response
	if content.stage == sonicStageFinal {
		r.final.WriteString(c.Content)
		return out
	}
	r.speculative.WriteString(c.Content)
	return append(out, s.clientMessage(map[string]any{
		"type": "response.output_audio_transcript.delta", "response_id": r.id, "item_id": r.messageItemID,
		"output_index": r.messageIndex, "content_index": 0, "delta": c.Content,
	}))
}

func (s *sonicSession) audioOutput(c *BedrockSonicOutputContent) [][]byte {
	out := s.ensureResponse()
	out = append(out, s.ensureMessageItem()...)
	r := s.response
	r.audioSent = true
	delta, _ := providerUtils.MarshalSorted(BedrockSonicAudioDelta{
		Type: "response.output_audio.delta", EventID: s.nextEventID(), ResponseID: r.id, ItemID: r.messageItemID,
		OutputIndex: r.messageIndex, Delta: c.Content,
	})
	return append(out, delta)
}

func (s *sonicSession) toolUse(c *BedrockSonicOutputContent) [][]byte {
	out := s.ensureResponse()
	r := s.response
	item := map[string]any{
		"id": s.nextID("item"), "object": "realtime.item", "type": "function_call", "status": "completed",
		"call_id": c.ToolUseID, "name": c.ToolName, "arguments": c.Content,
	}
	index := len(r.outputs)
	r.outputs = append(r.outputs, item)
	return append(out,
		s.clientMessage(map[string]any{"type": "response.output_item.added", "response_id": r.id, "output_index": index, "item": withStatus(item, "in_progress")}),
		s.clientMessage(map[string]any{
			"type": "response.function_call_arguments.done", "response_id": r.id, "item_id": item["id"], "output_index": index,
			"call_id": c.ToolUseID, "name": c.ToolName, "arguments": c.Content,
		}),
		s.clientMessage(map[string]any{"type": "response.output_item.done", "response_id": r.id, "output_index": index, "item": item}),
	)
}

func (s *sonicSession) contentEnd(c *BedrockSonicOutputContent) [][]byte {
	content, ok := s.contents[c.ContentID]
	delete(s.contents, c.ContentID)
	if !ok {
		return nil
	}
	if content.role == "USER" {
		if content.stage == sonicStageSpeculative || s.userItemID == "" {
			return nil
		}
		itemID := s.userItemID
		s.userItemID = ""
		return [][]byte{s.clientMessage(map[string]any{
			"type": "conversation.item.input_audio_transcription.completed", "item_id": itemID, "content_index": 0,
			"transcript": s.userTranscript.String(),
		})}
	}
	if c.StopReason == "INTERRUPTED" && s.response != nil {
		s.response.cancelled = true
	}
	turnEnded := c.StopReason == "END_TURN" || c.StopReason == "INTERRUPTED"
	switch {
	case content.kind == "TOOL":
		return s.finishResponse()
	case turnEnded && (content.kind == "AUDIO" || content.kind == "TEXT" && content.stage == sonicStageFinal):
		s.dropFinal = true
		return s.finishResponse()
	}
	return nil
}

// ensureResponse opens a response for model output that arrives with none in progress.
func (s *sonicSession) ensureResponse() [][]byte {
	if s.response != nil {
		return nil
	}
	s.response = &sonicResponse{id: s.nextID("resp")}
	return [][]byte{s.clientMessage(map[string]any{"type": "response.created", "response": map[string]any{
		"object": "realtime.response", "id": s.response.id, "status": "in_progress", "output": []any{},
	}})}
}

// ensureMessageItem opens the assistant message item that carries audio and its transcript.
func (s *sonicSession) ensureMessageItem() [][]byte {
	r := s.response
	if r.messageItemOpened {
		return nil
	}
	r.messageItemOpened = true
	r.messageItemID = s.nextID("item")
	r.messageIndex = len(r.outputs)
	r.outputs = append(r.outputs, nil) // filled at finish
	return [][]byte{
		s.clientMessage(map[string]any{"type": "response.output_item.added", "response_id": r.id, "output_index": r.messageIndex, "item": map[string]any{
			"id": r.messageItemID, "object": "realtime.item", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
		}}),
		s.clientMessage(map[string]any{
			"type": "response.content_part.added", "response_id": r.id, "item_id": r.messageItemID, "output_index": r.messageIndex,
			"content_index": 0, "part": map[string]any{"type": "audio", "transcript": ""},
		}),
	}
}

// finishResponse closes the open response with its transcript, outputs and the usage it consumed.
func (s *sonicSession) finishResponse() [][]byte {
	r := s.response
	if r == nil {
		return nil
	}
	s.response = nil
	status := "completed"
	if r.cancelled {
		status = "incomplete"
	}
	var out [][]byte
	if r.messageItemOpened {
		transcript := r.final.String()
		if transcript == "" {
			transcript = r.speculative.String()
		}
		common := map[string]any{"response_id": r.id, "item_id": r.messageItemID, "output_index": r.messageIndex, "content_index": 0}
		if r.audioSent {
			out = append(out, s.clientMessage(merged(common, map[string]any{"type": "response.output_audio.done"})))
		}
		out = append(out, s.clientMessage(merged(common, map[string]any{"type": "response.output_audio_transcript.done", "transcript": transcript})))
		out = append(out, s.clientMessage(merged(common, map[string]any{"type": "response.content_part.done", "part": map[string]any{"type": "audio", "transcript": transcript}})))
		item := map[string]any{
			"id": r.messageItemID, "object": "realtime.item", "type": "message", "role": "assistant", "status": status,
			"content": []any{map[string]any{"type": "output_audio", "transcript": transcript}},
		}
		r.outputs[r.messageIndex] = item
		out = append(out, s.clientMessage(map[string]any{"type": "response.output_item.done", "response_id": r.id, "output_index": r.messageIndex, "item": item}))
	}
	response := map[string]any{
		"object": "realtime.response", "id": r.id, "status": status, "output": r.outputs, "usage": s.consumeUsage(),
	}
	if r.cancelled {
		response["status"] = "cancelled"
		response["status_details"] = map[string]any{"type": "cancelled", "reason": "turn_detected"}
	}
	return append(out, s.clientMessage(map[string]any{"type": "response.done", "response": response}))
}

// consumeUsage bills the growth in Nova's cumulative totals since the previous response.
func (s *sonicSession) consumeUsage() map[string]any {
	total, billed := s.usageTotal, s.usageBilled
	s.usageBilled = total
	inText := total.Input.TextTokens - billed.Input.TextTokens
	inAudio := total.Input.SpeechTokens - billed.Input.SpeechTokens
	outText := total.Output.TextTokens - billed.Output.TextTokens
	outAudio := total.Output.SpeechTokens - billed.Output.SpeechTokens
	return map[string]any{
		"total_tokens":         inText + inAudio + outText + outAudio,
		"input_tokens":         inText + inAudio,
		"output_tokens":        outText + outAudio,
		"input_token_details":  map[string]any{"text_tokens": inText, "audio_tokens": inAudio},
		"output_token_details": map[string]any{"text_tokens": outText, "audio_tokens": outAudio},
	}
}

// errorReply builds an OpenAI Realtime error event for the client.
func (s *sonicSession) errorReply(errorType, code, message string) [][]byte {
	return [][]byte{s.clientMessage(map[string]any{"type": "error", "error": map[string]any{"type": errorType, "code": code, "message": message}})}
}

// clientMessage stamps an event id on a synthesized client event and serializes it.
func (s *sonicSession) clientMessage(event map[string]any) []byte {
	event["event_id"] = s.nextEventID()
	out, err := providerUtils.MarshalSorted(event)
	if err != nil {
		out, _ = providerUtils.MarshalSorted(map[string]any{"type": "error", "event_id": event["event_id"], "error": map[string]any{
			"type": "server_error", "code": "encode_failed", "message": "failed to encode realtime event",
		}})
	}
	return out
}

func withStatus(item map[string]any, status string) map[string]any {
	return merged(item, map[string]any{"status": status})
}

func merged(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func firstString(root gjson.Result, paths ...string) string {
	for _, p := range paths {
		if v := root.Get(p); v.Type == gjson.String && v.String() != "" {
			return v.String()
		}
	}
	return ""
}

func firstExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, p := range paths {
		if v := root.Get(p); v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n" + b
}
