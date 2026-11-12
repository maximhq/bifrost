package traceingest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Link methods name how a span was matched to its gateway call, strongest first.
const (
	LinkMethodHeader      = "header"      // a link header the caller added to the gateway request
	LinkMethodTraceparent = "traceparent" // W3C trace context propagated by the SDK
	LinkMethodResponseID  = "response_id" // the provider's response id, seen on both sides
	LinkMethodFingerprint = "fingerprint" // same model, time window and last user message, one candidate only
)

// GatewayLink is the trace position a gateway request named for itself.
type GatewayLink struct {
	TraceID      string
	ParentSpanID string
	Method       string
}

// LinkFromRequest reads the trace a gateway request says it belongs to. headers are the request
// headers with lowercased names. exportTraceID and parentSpanID are what the tracing middleware
// parsed from traceparent; they count only when the request really carried that header, because
// without one the middleware mints a trace id of its own.
//
// Order: Bifrost's own headers, Braintrust's x-bt-parent, the LiteLLM-style Langfuse headers, then
// traceparent. An explicit header always beats propagated context, since the caller chose it.
func LinkFromRequest(headers map[string]string, exportTraceID, parentSpanID string) *GatewayLink {
	if traceID := strings.TrimSpace(headers["x-bf-trace-id"]); traceID != "" {
		return &GatewayLink{TraceID: traceID, ParentSpanID: strings.TrimSpace(headers["x-bf-parent-span-id"]), Method: LinkMethodHeader}
	}
	if parent := strings.TrimSpace(headers["x-bt-parent"]); parent != "" {
		if components, err := DecodeBraintrustParent(parent); err == nil && components.RootSpanID != "" {
			return &GatewayLink{TraceID: components.RootSpanID, ParentSpanID: components.SpanID, Method: LinkMethodHeader}
		}
	}
	if traceID := strings.TrimSpace(headers["langfuse_trace_id"]); traceID != "" {
		return &GatewayLink{TraceID: traceID, ParentSpanID: strings.TrimSpace(headers["langfuse_parent_observation_id"]), Method: LinkMethodHeader}
	}
	if headers["traceparent"] != "" && exportTraceID != "" && parentSpanID != "" {
		return &GatewayLink{TraceID: exportTraceID, ParentSpanID: parentSpanID, Method: LinkMethodTraceparent}
	}
	return nil
}

// BraintrustParent is a decoded Braintrust span.export() value.
type BraintrustParent struct {
	ObjectType string // experiment | project_logs | playground_logs
	ObjectID   string
	RowID      string
	SpanID     string
	RootSpanID string
	// PropagatedEvent is merged into every span created under this parent.
	PropagatedEvent map[string]any
	// ProjectName is set when the SDK had not resolved the project id yet.
	ProjectName string
}

var braintrustObjectTypes = map[byte]string{1: "experiment", 2: "project_logs", 3: "playground_logs"}

const (
	btFieldObjectID   = 1
	btFieldRowID      = 2
	btFieldSpanID     = 3
	btFieldRootSpanID = 4
)

// DecodeBraintrustParent decodes the string Braintrust's span.export() returns, the value its SDKs
// send as x-bt-parent. Layout (SDK span_identifier_v3 and v4): standard base64 of
//
//	[version][object type][N] then N entries of [field id][bytes] then optional JSON
//
// In version 3 every entry is a 16-byte UUID. In version 4 only span id (8 bytes) and root span id
// (16 bytes) are binary, written as hex. A value that does not fit its binary form travels in the
// trailing JSON under its field name.
func DecodeBraintrustParent(value string) (*BraintrustParent, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	}
	if err != nil {
		return nil, fmt.Errorf("x-bt-parent is not base64: %w", err)
	}
	if len(raw) < 3 {
		return nil, fmt.Errorf("x-bt-parent is too short")
	}
	version := raw[0]
	if version != 3 && version != 4 {
		return nil, fmt.Errorf("x-bt-parent version %d is not supported", version)
	}
	parent := &BraintrustParent{ObjectType: braintrustObjectTypes[raw[1]]}
	if parent.ObjectType == "" {
		return nil, fmt.Errorf("x-bt-parent has unknown object type %d", raw[1])
	}
	entries, rest := int(raw[2]), raw[3:]
	for range entries {
		if len(rest) < 1 {
			return nil, fmt.Errorf("x-bt-parent is truncated")
		}
		field := rest[0]
		size := 16
		if version == 4 && field == btFieldSpanID {
			size = 8
		}
		if len(rest) < 1+size {
			return nil, fmt.Errorf("x-bt-parent is truncated")
		}
		data := rest[1 : 1+size]
		rest = rest[1+size:]
		text := hex.EncodeToString(data)
		if version == 3 {
			text = text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
		}
		switch field {
		case btFieldObjectID:
			parent.ObjectID = text
		case btFieldRowID:
			parent.RowID = text
		case btFieldSpanID:
			parent.SpanID = text
		case btFieldRootSpanID:
			parent.RootSpanID = text
		}
	}
	if len(rest) > 0 {
		var extra struct {
			ObjectID        string         `json:"object_id"`
			RowID           string         `json:"row_id"`
			SpanID          string         `json:"span_id"`
			RootSpanID      string         `json:"root_span_id"`
			PropagatedEvent map[string]any `json:"propagated_event"`
			ComputeArgs     map[string]any `json:"compute_object_metadata_args"`
		}
		if err := sonic.Unmarshal(rest, &extra); err != nil {
			return nil, fmt.Errorf("x-bt-parent has malformed JSON: %w", err)
		}
		for _, field := range []struct{ dst, src *string }{
			{&parent.ObjectID, &extra.ObjectID}, {&parent.RowID, &extra.RowID}, {&parent.SpanID, &extra.SpanID}, {&parent.RootSpanID, &extra.RootSpanID},
		} {
			if *field.src != "" {
				*field.dst = *field.src
			}
		}
		parent.PropagatedEvent = extra.PropagatedEvent
		if name, ok := extra.ComputeArgs["project_name"].(string); ok {
			parent.ProjectName = name
		}
	}
	return parent, nil
}

// TextFingerprint is the fingerprint of one prompt text: a short hash of the text with whitespace
// collapsed. The gateway stores it for a request's last user message and the matcher computes it
// for a span's, so a call can be matched even when the gateway keeps no content.
func TextFingerprint(text string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:12])
}

// ChatFingerprint fingerprints the last user message of a chat request.
func ChatFingerprint(messages []schemas.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != schemas.ChatMessageRoleUser || message.Content == nil {
			continue
		}
		if message.Content.ContentStr != nil {
			return TextFingerprint(*message.Content.ContentStr)
		}
		var text strings.Builder
		for _, block := range message.Content.ContentBlocks {
			if block.Text != nil {
				text.WriteString(*block.Text)
				text.WriteString(" ")
			}
		}
		return TextFingerprint(text.String())
	}
	return ""
}

// ResponsesFingerprint fingerprints the last user message of a Responses API request.
func ResponsesFingerprint(messages []schemas.ResponsesMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role == nil || *message.Role != schemas.ResponsesInputMessageRoleUser || message.Content == nil {
			continue
		}
		if message.Content.ContentStr != nil {
			return TextFingerprint(*message.Content.ContentStr)
		}
		var text strings.Builder
		for _, block := range message.Content.ContentBlocks {
			if block.Text != nil {
				text.WriteString(*block.Text)
				text.WriteString(" ")
			}
		}
		return TextFingerprint(text.String())
	}
	return ""
}

// SpanInputFingerprint fingerprints the last user message found in a span's input, in whichever
// shape the SDK logged it: a message list, an object holding one under "messages" or "input",
// OpenInference's {"message": {...}} wrappers, GenAI "parts", or a bare prompt string.
func SpanInputFingerprint(input any) string {
	switch v := input.(type) {
	case string:
		return TextFingerprint(v)
	case map[string]any:
		for _, key := range []string{"messages", "input", "prompt", "contents"} {
			if inner, ok := v[key]; ok {
				if fingerprint := SpanInputFingerprint(inner); fingerprint != "" {
					return fingerprint
				}
			}
		}
		return ""
	case []any:
		for i := len(v) - 1; i >= 0; i-- {
			message, ok := v[i].(map[string]any)
			if !ok {
				continue
			}
			if wrapped, isWrapped := message["message"].(map[string]any); isWrapped {
				message = wrapped
			}
			if role, _ := message["role"].(string); role != "user" {
				continue
			}
			return TextFingerprint(messageText(message))
		}
	}
	return ""
}

// messageText joins the text of one logged message.
func messageText(message map[string]any) string {
	var text strings.Builder
	var collect func(value any)
	collect = func(value any) {
		switch v := value.(type) {
		case string:
			text.WriteString(v)
			text.WriteString(" ")
		case []any:
			for _, part := range v {
				collect(part)
			}
		case map[string]any:
			for _, key := range []string{"text", "content"} {
				if inner, ok := v[key]; ok {
					collect(inner)
					return
				}
			}
			// OpenInference wraps each content part as {"message_content": {"text": ...}}.
			if inner, ok := v["message_content"]; ok {
				collect(inner)
			}
		}
	}
	for _, key := range []string{"content", "contents", "parts"} {
		if inner, ok := message[key]; ok {
			collect(inner)
			break
		}
	}
	return text.String()
}
