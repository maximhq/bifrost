package kiro

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// maxFrameBytes caps one eventstream message, the bound the AWS eventstream format itself sets.
const maxFrameBytes = 16 * 1024 * 1024

var errFrameTooLarge = errors.New("kiro: eventstream message exceeds 16 MiB")

// frameReader feeds the eventstream decoder and tracks how much of the current message has been
// consumed. The decoder reports a connection cut on a field boundary (or inside a payload, which
// it copies with io.Copy) as a plain io.EOF, indistinguishable from a clean end of stream; the
// byte count tells the two apart. It also caps a message at maxFrameBytes so a hostile length
// prefix cannot make the decoder buffer without bound.
type frameReader struct {
	r        io.Reader
	consumed int
}

func (f *frameReader) Read(p []byte) (int, error) {
	remaining := maxFrameBytes - f.consumed
	if remaining <= 0 {
		return 0, errFrameTooLarge
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := f.r.Read(p)
	f.consumed += n
	return n, err
}

// decode reads one message. It returns io.EOF only at a clean message boundary and
// io.ErrUnexpectedEOF when the stream ended inside a message.
func (f *frameReader) decode(decoder *eventstream.Decoder, payloadBuf []byte) (eventstream.Message, error) {
	f.consumed = 0
	msg, err := decoder.Decode(f, payloadBuf)
	if err == nil {
		return msg, nil
	}
	if errors.Is(err, io.EOF) {
		if f.consumed > 0 {
			return eventstream.Message{}, io.ErrUnexpectedEOF
		}
		return eventstream.Message{}, io.EOF
	}
	return eventstream.Message{}, err
}

// outputKind is one kind of assembled stream output.
type outputKind int

const (
	outputText outputKind = iota
	outputReasoning
	outputReasoningSignature
	outputReasoningRedacted
	outputToolStart
	outputToolArgs
)

// streamOutput is one piece of assembled assistant output, in wire order.
type streamOutput struct {
	kind      outputKind
	text      string
	toolIndex int
	toolID    string
	toolName  string
}

// kiroTokenUsage is metadataEvent.tokenUsage. Real captures have never carried it, but it is
// authoritative when present.
type kiroTokenUsage struct {
	UncachedInputTokens   int `json:"uncachedInputTokens"`
	CacheReadInputTokens  int `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	OutputTokens          int `json:"outputTokens"`
	TotalTokens           int `json:"totalTokens"`
}

// kiroEvent is the union of the event payload fields this provider reads.
type kiroEvent struct {
	Content                *string         `json:"content"`
	ModelID                string          `json:"modelId"`
	Text                   *string         `json:"text"`
	Signature              string          `json:"signature"`
	RedactedContent        string          `json:"redactedContent"`
	Name                   string          `json:"name"`
	ToolUseID              string          `json:"toolUseId"`
	Input                  any             `json:"input"`
	Stop                   bool            `json:"stop"`
	StopReason             string          `json:"stopReason"`
	TokenUsage             *kiroTokenUsage `json:"tokenUsage"`
	ContextUsagePercentage *float64        `json:"contextUsagePercentage"`
	Message                string          `json:"message"`
	MessageUpper           string          `json:"Message"`
	Reason                 string          `json:"reason"`
	Type                   string          `json:"type"`
	AWSType                string          `json:"__type"`
}

// truncationKeys are the payload fields whose value can announce a truncated response.
var truncationKeys = []string{"finish_reason", "finishReason", "stop_reason", "stopReason", "completionReason", "reason"}

var truncationValuePattern = regexp.MustCompile(`(?i)length|max[_-]?tokens?|truncat|incomplete|context_length`)

// openToolCall is a tool call whose input fragments are still arriving.
type openToolCall struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// streamParser assembles decoded eventstream messages into assistant output. One instance serves
// one response.
type streamParser struct {
	nameMap        map[string]string
	think          thinkTagParser
	open           *openToolCall
	toolCount      int
	latinChars     int
	cjkChars       int
	sawText        bool
	sawReasoning   bool
	truncated      bool
	stopReason     string
	modelID        string
	contextPercent *float64
	tokenUsage     *kiroTokenUsage
}

func newStreamParser(modelID string, nameMap map[string]string) *streamParser {
	return &streamParser{modelID: modelID, nameMap: nameMap}
}

// handle processes one decoded eventstream message.
func (p *streamParser) handle(msg eventstream.Message) ([]streamOutput, *schemas.BifrostError) {
	messageType := headerString(msg.Headers, ":message-type")
	switch messageType {
	case "", "event":
	case "exception", "error":
		exceptionType := headerString(msg.Headers, ":exception-type")
		if exceptionType == "" {
			exceptionType = headerString(msg.Headers, ":error-type")
		}
		return nil, newStreamExceptionError(exceptionType, msg.Payload)
	default:
		return nil, protocolError(fmt.Sprintf("kiro: unsupported eventstream message type %q", messageType))
	}

	eventType := headerString(msg.Headers, ":event-type")
	switch eventType {
	case "assistantResponseEvent", "reasoningContentEvent", "toolUseEvent", "messageMetadataEvent",
		"initial-response", "metadataEvent", "contextUsageEvent", "meteringEvent", "invalidStateEvent", "error":
	default:
		// Unknown event types are ignored without parsing their payload.
		return nil, nil
	}

	var event kiroEvent
	if len(msg.Payload) > 0 {
		if err := sonic.Unmarshal(msg.Payload, &event); err != nil {
			return nil, protocolError(fmt.Sprintf("kiro: invalid %s payload", eventType))
		}
	}
	// metadataEvent.stopReason is Kiro's own terminal verdict and is interpreted below, and error
	// events stay errors; every other payload is sniffed for a truncation marker.
	if eventType != "metadataEvent" && eventType != "error" && eventType != "invalidStateEvent" && payloadSignalsTruncation(msg.Payload) {
		p.truncated = true
		return nil, nil
	}

	switch eventType {
	case "assistantResponseEvent":
		if event.ModelID != "" {
			p.modelID = event.ModelID
		}
		if event.Content == nil || *event.Content == "" {
			return nil, nil
		}
		if p.open != nil {
			return nil, protocolError("kiro: assistant text arrived while a tool call was still open")
		}
		outputs := p.think.feed(*event.Content)
		p.observe(outputs)
		return outputs, nil

	case "reasoningContentEvent":
		var outputs []streamOutput
		if event.Text != nil && *event.Text != "" {
			outputs = append(outputs, streamOutput{kind: outputReasoning, text: *event.Text})
		}
		if event.Signature != "" {
			outputs = append(outputs, streamOutput{kind: outputReasoningSignature, text: event.Signature})
		} else if event.RedactedContent != "" {
			outputs = append(outputs, streamOutput{kind: outputReasoningRedacted, text: event.RedactedContent})
		}
		p.observe(outputs)
		return outputs, nil

	case "toolUseEvent":
		return p.handleToolUse(&event)

	case "metadataEvent":
		if event.StopReason != "" {
			p.stopReason = event.StopReason
		}
		if event.TokenUsage != nil {
			p.tokenUsage = event.TokenUsage
		}
		if event.ContextUsagePercentage != nil && !math.IsNaN(*event.ContextUsagePercentage) {
			p.contextPercent = event.ContextUsagePercentage
		}
		return nil, nil

	case "contextUsageEvent":
		if event.ContextUsagePercentage != nil && !math.IsNaN(*event.ContextUsagePercentage) {
			p.contextPercent = event.ContextUsagePercentage
		}
		return nil, nil

	case "invalidStateEvent":
		message := event.Message
		if message == "" {
			message = "invalid conversation state"
		}
		return nil, newKiroError(http.StatusBadGateway, "server_error", "upstream_server_error",
			"Kiro invalid state: "+truncateText(sanitizeErrorText(message), 500))

	case "error":
		reason := firstNonEmpty(event.Reason, event.Type, event.AWSType)
		return nil, newStreamExceptionError(reason, msg.Payload)
	}
	// messageMetadataEvent, initial-response and meteringEvent carry nothing the response needs.
	return nil, nil
}

func (p *streamParser) handleToolUse(event *kiroEvent) ([]streamOutput, *schemas.BifrostError) {
	fragment := ""
	switch v := event.Input.(type) {
	case string:
		fragment = v
	case nil:
	default:
		// Some captures carry the whole input as an object rather than a string fragment.
		encoded, err := sonic.Marshal(v)
		if err != nil {
			return nil, protocolError("kiro: unreadable tool input")
		}
		fragment = string(encoded)
	}

	var outputs []streamOutput
	if p.open == nil {
		if event.ToolUseID == "" || event.Name == "" {
			if event.Stop && fragment == "" {
				// A bare stop for a call that was already closed.
				return nil, protocolError("kiro: tool call stop arrived with no open tool call")
			}
			return nil, protocolError("kiro: tool call started without an id or name")
		}
		name := event.Name
		if original, ok := p.nameMap[name]; ok {
			name = original
		}
		p.open = &openToolCall{index: p.toolCount, id: event.ToolUseID, name: name}
		p.toolCount++
		outputs = append(outputs, streamOutput{kind: outputToolStart, toolIndex: p.open.index, toolID: event.ToolUseID, toolName: name})
	} else if (event.ToolUseID != "" && event.ToolUseID != p.open.id) || (event.Name != "" && p.restore(event.Name) != p.open.name) {
		return nil, protocolError("kiro: a tool call event does not match the open tool call")
	}
	if fragment != "" {
		p.open.args.WriteString(fragment)
		outputs = append(outputs, streamOutput{kind: outputToolArgs, toolIndex: p.open.index, text: fragment})
		p.countChars(fragment)
	}
	if event.Stop {
		closing, bErr := p.closeTool()
		if bErr != nil {
			return nil, bErr
		}
		outputs = append(outputs, closing...)
	}
	return outputs, nil
}

// closeTool validates the assembled input and closes the open tool call. A call with no input
// gets "{}" so clients always receive a JSON object.
func (p *streamParser) closeTool() ([]streamOutput, *schemas.BifrostError) {
	open := p.open
	p.open = nil
	input := strings.TrimSpace(open.args.String())
	if input == "" {
		return []streamOutput{{kind: outputToolArgs, toolIndex: open.index, text: "{}"}}, nil
	}
	if !isCompleteToolInput(input) {
		return nil, truncatedError(fmt.Sprintf("kiro: the arguments of tool call %q were truncated", open.name))
	}
	return nil, nil
}

func (p *streamParser) restore(name string) string {
	if original, ok := p.nameMap[name]; ok {
		return original
	}
	return name
}

// finish flushes held-back text and an unterminated tool call at the end of the stream, and
// reports Kiro's terminal verdicts as errors where they are errors.
func (p *streamParser) finish() ([]streamOutput, *schemas.BifrostError) {
	outputs := p.think.flush()
	p.observe(outputs)
	if p.open != nil {
		closing, bErr := p.closeTool()
		if bErr != nil {
			return nil, bErr
		}
		outputs = append(outputs, closing...)
	}
	if strings.EqualFold(p.stopReason, "MODEL_CONTEXT_WINDOW_EXCEEDED") {
		return nil, newKiroError(http.StatusBadRequest, "invalid_request_error", "context_length_exceeded",
			"Kiro rejected the request because the conversation exceeds the model's context window. Compact or reduce the history, or start a new session.")
	}
	if !p.sawText && !p.sawReasoning && p.toolCount == 0 && !p.truncated {
		return nil, newKiroError(http.StatusBadGateway, "server_error", "empty_kiro_stream",
			"Kiro returned an empty response")
	}
	return outputs, nil
}

// finishReason maps the assembled response to an OpenAI finish reason.
func (p *streamParser) finishReason() string {
	stop := strings.ToUpper(p.stopReason)
	switch {
	case p.toolCount > 0:
		return string(schemas.BifrostFinishReasonToolCalls)
	case p.truncated || stop == "MAX_TOKENS" || truncationValuePattern.MatchString(stop):
		return string(schemas.BifrostFinishReasonLength)
	case stop == "CONTENT_FILTERED" || stop == "GUARDRAIL_INTERVENED":
		return "content_filter"
	default:
		return string(schemas.BifrostFinishReasonStop)
	}
}

// fillUsage writes the current usage into dst in place, so a handle registered for cancel/timeout
// billing always reflects what was generated so far.
func (p *streamParser) fillUsage(dst *schemas.BifrostLLMUsage, estimatedInput int) {
	*dst = *p.usage(estimatedInput)
}

// usage reports authoritative usage when Kiro sent it, else the estimate: input from the payload,
// output from the emitted characters, with input raised to what the context-usage percentage
// implies when that is larger.
func (p *streamParser) usage(estimatedInput int) *schemas.BifrostLLMUsage {
	if u := p.tokenUsage; u != nil {
		prompt := u.UncachedInputTokens + u.CacheReadInputTokens + u.CacheWriteInputTokens
		usage := &schemas.BifrostLLMUsage{PromptTokens: prompt, CompletionTokens: u.OutputTokens, TotalTokens: max(u.TotalTokens, prompt+u.OutputTokens)}
		if u.CacheReadInputTokens > 0 || u.CacheWriteInputTokens > 0 {
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
				CachedReadTokens:  u.CacheReadInputTokens,
				CachedWriteTokens: u.CacheWriteInputTokens,
			}
		}
		return usage
	}
	completion := estimateOutputTokens(p.latinChars, p.cjkChars)
	prompt := estimatedInput
	if p.contextPercent != nil {
		if window := kiroContextWindow(p.modelID); window > 0 {
			percent := min(max(*p.contextPercent, 0), 100)
			if implied := int(math.Ceil(float64(window)*percent/100)) - completion; implied > prompt {
				prompt = implied
			}
		}
	}
	return &schemas.BifrostLLMUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

func (p *streamParser) observe(outputs []streamOutput) {
	for _, out := range outputs {
		switch out.kind {
		case outputText:
			if strings.TrimSpace(out.text) != "" {
				p.sawText = true
			}
			p.countChars(out.text)
		case outputReasoning:
			p.sawReasoning = true
			p.countChars(out.text)
		case outputReasoningSignature, outputReasoningRedacted:
			p.sawReasoning = true
		}
	}
}

func (p *streamParser) countChars(s string) {
	latin, cjk := charCounts(s)
	p.latinChars += latin
	p.cjkChars += cjk
}

// isCompleteToolInput reports whether assembled tool input is a complete JSON object or array.
func isCompleteToolInput(input string) bool {
	if input == "" {
		return true
	}
	if input[0] != '{' && input[0] != '[' {
		return false
	}
	var probe any
	return sonic.UnmarshalString(input, &probe) == nil && probe != nil
}

// payloadSignalsTruncation reports a truncated:true flag or a truncation-like finish reason.
func payloadSignalsTruncation(payload []byte) bool {
	// Every truncation key contains "eason" except the truncated flag; skip the lookups for the
	// common payload that has neither.
	if !bytes.Contains(payload, []byte("eason")) && !bytes.Contains(payload, []byte("truncated")) {
		return false
	}
	if truncated := providerUtils.GetJSONField(payload, "truncated"); truncated.Type == gjson.True {
		return true
	}
	for _, key := range truncationKeys {
		if value := providerUtils.GetJSONField(payload, key); value.Type == gjson.String && truncationValuePattern.MatchString(value.Str) {
			return true
		}
	}
	return false
}

func headerString(headers eventstream.Headers, name string) string {
	value := headers.Get(name)
	if value == nil {
		return ""
	}
	return value.String()
}

func protocolError(message string) *schemas.BifrostError {
	return newKiroError(http.StatusBadGateway, "server_error", "kiro_protocol_error", message)
}

func truncatedError(message string) *schemas.BifrostError {
	bErr := newKiroError(http.StatusBadGateway, "server_error", "stream_truncated", message)
	return bErr
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// thinkTagParser splits a leading <thinking>, <think> or <reasoning> block out of the assistant
// text. Kiro emits at most one leading block; partial tags across chunk boundaries are held back.
type thinkTagParser struct {
	state          thinkState
	preWhitespace  string
	preBuffer      string
	thinkingBuffer string
	closeTag       string
}

type thinkState int

const (
	thinkPre thinkState = iota
	thinkThinking
	thinkStreaming
)

var openThinkTags = []string{"<thinking>", "<think>", "<reasoning>"}

const (
	maxOpenThinkTag  = len("<reasoning>")
	maxCloseThinkTag = len("</reasoning>")
)

func (t *thinkTagParser) feed(text string) []streamOutput {
	if text == "" {
		return nil
	}
	switch t.state {
	case thinkStreaming:
		return []streamOutput{{kind: outputText, text: text}}
	case thinkThinking:
		input := t.thinkingBuffer + text
		t.thinkingBuffer = ""
		return t.drain(input, 0)
	}

	var input string
	if t.preBuffer != "" {
		input = t.preBuffer + text
		t.preBuffer = ""
	} else {
		stripped := strings.TrimLeft(text, " \t\r\n\f\v")
		t.preWhitespace += text[:len(text)-len(stripped)]
		if stripped == "" {
			return nil
		}
		input = stripped
	}
	for _, tag := range openThinkTags {
		if strings.HasPrefix(input, tag) {
			t.preWhitespace = ""
			t.state = thinkThinking
			t.closeTag = "</" + tag[1:]
			return t.drain(input, len(tag))
		}
	}
	if len(input) <= maxOpenThinkTag && isOpenThinkTagPrefix(input) {
		t.preBuffer = input
		return nil
	}
	t.state = thinkStreaming
	out := t.preWhitespace + input
	t.preWhitespace = ""
	return []streamOutput{{kind: outputText, text: out}}
}

func (t *thinkTagParser) drain(input string, offset int) []streamOutput {
	var outputs []streamOutput
	if idx := strings.Index(input[offset:], t.closeTag); idx >= 0 {
		if idx > 0 {
			outputs = append(outputs, streamOutput{kind: outputReasoning, text: input[offset : offset+idx]})
		}
		after := strings.TrimLeft(input[offset+idx+len(t.closeTag):], " \t\r\n\f\v")
		t.state = thinkStreaming
		if after != "" {
			outputs = append(outputs, streamOutput{kind: outputText, text: after})
		}
		return outputs
	}
	// Hold back only what could be the start of the close tag, on a rune boundary.
	cut := max(offset, len(input)-(maxCloseThinkTag-1))
	for cut > offset && !utf8.RuneStart(input[cut]) {
		cut--
	}
	if cut > offset {
		outputs = append(outputs, streamOutput{kind: outputReasoning, text: input[offset:cut]})
	}
	t.thinkingBuffer = input[cut:]
	return outputs
}

// flush releases held-back text at the end of the stream. An unclosed thinking block is reported
// as reasoning.
func (t *thinkTagParser) flush() []streamOutput {
	switch t.state {
	case thinkThinking:
		out := t.thinkingBuffer
		t.thinkingBuffer = ""
		t.state = thinkStreaming
		if out == "" {
			return nil
		}
		return []streamOutput{{kind: outputReasoning, text: out}}
	case thinkPre:
		out := t.preWhitespace + t.preBuffer
		t.preWhitespace, t.preBuffer = "", ""
		t.state = thinkStreaming
		if out == "" {
			return nil
		}
		return []streamOutput{{kind: outputText, text: out}}
	}
	return nil
}

func isOpenThinkTagPrefix(s string) bool {
	for _, tag := range openThinkTags {
		if len(s) < len(tag) && strings.HasPrefix(tag, s) {
			return true
		}
	}
	return false
}
