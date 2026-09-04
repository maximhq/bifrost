package otel

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// AttrSystemInstructions is the OTel GenAI semantic-conventions attribute for the system
// prompt: a JSON array of parts. Bifrost records instructions as the plain string
// gen_ai.request.instructions; the parts message format mirrors it here so spec-following
// backends can display it alongside the input messages.
const AttrSystemInstructions = "gen_ai.system_instructions"

// flatMessage is the union of the message shapes the framework serializes into
// gen_ai.input.messages / gen_ai.output.messages (MessageSummary for chat, plus the
// tool_call_id field of ResponsesMessageSummary). It is decoded leniently: unknown fields
// are ignored and every field is optional.
type flatMessage struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ToolCalls        []flatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ReasoningDetails []flatReasoning `json:"reasoning_details,omitempty"`
	Refusal          string          `json:"refusal,omitempty"`
	Audio            *flatAudio      `json:"audio,omitempty"`
}

type flatToolCall struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

type flatReasoning struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type flatAudio struct {
	ID         string `json:"id,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

// partsMessage is one message in the semantic-conventions shape. Parts is always emitted,
// even when empty, because the spec marks it required.
type partsMessage struct {
	Role  string        `json:"role"`
	Parts []messagePart `json:"parts"`
}

// messagePart covers the spec part types Bifrost can populate: text, reasoning, tool_call
// and tool_call_response. Refusals have no spec part; they are emitted as a generic
// "refusal" part, which the spec allows for extension types.
type messagePart struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments any    `json:"arguments,omitempty"`
	Response  any    `json:"response,omitempty"`
}

// applyPartsMessageFormat rewrites the GenAI message attributes of an exported span into the
// semantic-conventions parts shape and mirrors gen_ai.request.instructions as
// gen_ai.system_instructions. Values are rewritten in place; anything that is not the
// framework's flat shape (the root span carries a bare text excerpt, transcription output is
// a bare string) is wrapped as a single text part so no content is lost.
func applyPartsMessageFormat(kvs []*KeyValue) []*KeyValue {
	var instructions string
	for _, kv := range kvs {
		if kv == nil || kv.Value == nil {
			continue
		}
		raw, ok := kv.Value.Value.(*StringValue)
		if !ok {
			continue
		}
		switch kv.Key {
		case schemas.AttrInputMessages:
			kv.Value = &AnyValue{Value: &StringValue{StringValue: toPartsMessages(raw.StringValue, "user")}}
		case schemas.AttrOutputMessages:
			kv.Value = &AnyValue{Value: &StringValue{StringValue: toPartsMessages(raw.StringValue, "assistant")}}
		case schemas.AttrInstructions:
			instructions = raw.StringValue
		}
	}
	if instructions != "" {
		if data, err := schemas.MarshalString([]messagePart{{Type: "text", Content: instructions}}); err == nil {
			kvs = append(kvs, kvStr(AttrSystemInstructions, data))
		}
	}
	return kvs
}

// toPartsMessages converts one serialized message attribute to the parts shape. A value that
// is not a JSON array of flat messages becomes a single message with the given role.
func toPartsMessages(raw string, fallbackRole string) string {
	var flat []flatMessage
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		if err := schemas.Unmarshal([]byte(raw), &flat); err != nil {
			flat = nil
		}
	}
	var out []partsMessage
	if flat == nil {
		out = []partsMessage{{Role: fallbackRole, Parts: []messagePart{{Type: "text", Content: raw}}}}
	} else {
		out = make([]partsMessage, 0, len(flat))
		for i := range flat {
			out = append(out, toPartsMessage(&flat[i]))
		}
	}
	data, err := schemas.MarshalString(out)
	if err != nil {
		return raw
	}
	return data
}

// toPartsMessage maps one flat message to parts. Order follows the model's own sequence:
// reasoning first, then the visible content, then the tool calls it decided to make.
func toPartsMessage(m *flatMessage) partsMessage {
	parts := make([]messagePart, 0, 2+len(m.ToolCalls))
	if m.Reasoning != "" {
		parts = append(parts, messagePart{Type: "reasoning", Content: m.Reasoning})
	}
	for _, d := range m.ReasoningDetails {
		if d.Text != "" && d.Text != m.Reasoning {
			parts = append(parts, messagePart{Type: "reasoning", Content: d.Text})
		}
	}
	// A tool result is a tool_call_response part even when the result is empty: the spec
	// requires the response field, and the empty string is the truthful value.
	if m.Role == "tool" || m.ToolCallID != "" {
		parts = append(parts, messagePart{Type: "tool_call_response", ID: m.ToolCallID, Response: m.Content})
	} else if m.Content != "" {
		parts = append(parts, messagePart{Type: "text", Content: m.Content})
	}
	if m.Refusal != "" {
		parts = append(parts, messagePart{Type: "refusal", Content: m.Refusal})
	}
	if m.Audio != nil && m.Audio.Transcript != "" {
		parts = append(parts, messagePart{Type: "text", Content: m.Audio.Transcript})
	}
	for _, tc := range m.ToolCalls {
		parts = append(parts, messagePart{Type: "tool_call", ID: tc.ID, Name: tc.Name, Arguments: toolArguments(tc.Args)})
	}
	return partsMessage{Role: m.Role, Parts: parts}
}

// toolArguments returns the tool call arguments as structured JSON when they parse, else as
// the raw string, so a collector sees an object rather than an escaped string wherever
// possible. Empty arguments are omitted (the spec marks the field optional).
func toolArguments(args string) any {
	if args == "" {
		return nil
	}
	var v any
	if err := schemas.Unmarshal([]byte(args), &v); err != nil {
		return args
	}
	return v
}
