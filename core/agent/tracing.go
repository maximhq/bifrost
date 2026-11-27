package agent

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/maximhq/bifrost/core/schemas"
)

const a2aTraceContentLimit = 1 << 20
const a2aTraceEventLimit = 256

// a2aTraceObservation keeps only small counters beside the operation. Content
// lives on the tracing manager's span, never in the request context.
type a2aTraceObservation struct {
	tracer        schemas.Tracer
	handle        schemas.SpanHandle
	root          schemas.SpanHandle
	spanID        string
	content, raw  bool
	failed        bool
	events, bytes int
}

// startA2AObservation captures the post-pre-hook input using the same content
// keys as LLM spans, so every connector's content controls apply.
func startA2AObservation(tracer schemas.Tracer, handle schemas.SpanHandle, ctx *schemas.BifrostContext, req *schemas.BifrostA2ARequest) *a2aTraceObservation {
	if tracer == nil || handle == nil || req == nil {
		return nil
	}
	o := &a2aTraceObservation{tracer: tracer, handle: handle, content: true}
	o.spanID, _ = ctx.Value(schemas.BifrostContextKeySpanID).(string)
	if consumer, ok := tracer.(interface{ A2AContentDemand() (bool, bool) }); ok {
		o.content, o.raw = consumer.A2AContentDemand()
	}
	if disabled, _ := ctx.Value(schemas.BifrostContextKeyGovernanceDisableContentLogging).(bool); disabled {
		o.content, o.raw = false, false
	}
	if traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string); ok {
		o.root = tracer.GetSpanHandleByID(traceID, nil)
	}
	if push := req.BifrostA2APushRequest; push != nil {
		o.pushIdentity(push.PushConfigID, push.DeliveryID, push.AttemptID)
	}
	if o.content && req.RequestBody != nil {
		// Message operations expose the message, while lifecycle operations expose
		// their sanitized protocol arguments. Raw capture remains a separate opt-in.
		var body map[string]json.RawMessage
		if len(*req.RequestBody) <= a2aTraceContentLimit && json.Unmarshal([]byte(*req.RequestBody), &body) == nil {
			if message := body["message"]; len(message) > 0 {
				if safe := sanitizeA2AJSON(string(message)); safe != "" {
					o.setContent(schemas.AttrInputMessages, "["+safe+"]")
				}
			} else if safe := sanitizeA2AJSON(*req.RequestBody); safe != "" {
				o.setContent(schemas.AttrInputMessages, "["+safe+"]")
			}
		}
	}
	return o
}

// pushIdentity preserves correlation across push ingress and delivery attempts.
func (o *a2aTraceObservation) pushIdentity(config, delivery, attempt string) {
	for key, value := range map[string]string{
		"bifrost.a2a.push.config.id":   config,
		"bifrost.a2a.push.delivery.id": delivery,
		"bifrost.a2a.push.attempt.id":  attempt,
	} {
		if value != "" {
			o.tracer.SetAttribute(o.handle, key, value)
		}
	}
}

// setContent duplicates structured content onto the root for trace-level views.
// Connector-specific root suppression is applied later during conversion.
func (o *a2aTraceObservation) setContent(key, value string) {
	if value == "" {
		return
	}
	o.tracer.SetAttribute(o.handle, key, value)
	if o.root != nil {
		o.tracer.SetAttribute(o.root, key, value)
	}
}

// identity records returned IDs even when no content is retained.
func (o *a2aTraceObservation) identity(task, context, message, state string) {
	for key, value := range map[string]string{
		schemas.AttrA2ATaskID: task, schemas.AttrConversationID: context,
		schemas.AttrA2AMessageID: message, schemas.AttrA2ATaskState: state,
	} {
		if value != "" {
			o.tracer.SetAttribute(o.handle, key, value)
		}
	}
	if state != "" {
		o.failed = false
		switch a2a.TaskState(state) {
		case a2a.TaskStateFailed:
			o.failed = true
			o.tracer.SetAttribute(o.handle, schemas.AttrErrorTypeSpec, "a2a.task.failed")
		case a2a.TaskStateRejected:
			o.failed = true
			o.tracer.SetAttribute(o.handle, schemas.AttrErrorTypeSpec, "a2a.task.rejected")
		}
	}
}

// observe retains a bounded sequence of accepted stream events, including
// artifact append semantics and status messages, without flattening away updates.
func (o *a2aTraceObservation) observe(event *schemas.BifrostA2AEvent) {
	if o == nil || event == nil {
		return
	}
	o.identity(event.TaskID, event.ContextID, event.MessageID, event.TaskState)
	if o.events >= a2aTraceEventLimit {
		o.tracer.SetAttribute(o.handle, "bifrost.a2a.events.dropped", true)
		return
	}
	attrs := map[string]any{"a2a.event.sequence": event.Sequence}
	for key, value := range map[string]string{
		schemas.AttrA2ATaskID: event.TaskID, schemas.AttrConversationID: event.ContextID,
		schemas.AttrA2AMessageID: event.MessageID, schemas.AttrA2ATaskState: event.TaskState,
		schemas.AttrA2AArtifactID: event.ArtifactID,
	} {
		if value != "" {
			attrs[key] = value
		}
	}
	if o.content && event.Body != nil {
		if o.bytes+len(*event.Body) <= a2aTraceContentLimit {
			if body := sanitizeA2AJSON(*event.Body); body != "" {
				if o.bytes+len(body) <= a2aTraceContentLimit {
					attrs[schemas.AttrOutputMessages] = body
					o.bytes += len(body)
				} else {
					o.tracer.SetAttribute(o.handle, "bifrost.a2a.content.truncated", true)
				}
			}
		} else {
			o.tracer.SetAttribute(o.handle, "bifrost.a2a.content.truncated", true)
		}
	}
	o.tracer.AddEvent(o.handle, "a2a."+string(event.EventType), attrs)
	o.events++
}

// finish enriches unary and stream outcomes after post-hooks and before capture
// cleanup. Raw bodies stay separate from serializable span attributes.
func (o *a2aTraceObservation) finish(ctx *schemas.BifrostContext, response *schemas.BifrostA2AResponse) {
	if o == nil {
		return
	}
	if response != nil {
		if push := response.BifrostA2APushResponse; push != nil {
			o.pushIdentity(push.PushConfigID, push.DeliveryID, push.AttemptID)
		}
		if r := response.BifrostA2ASendMessageResponse; r != nil {
			o.identity(r.TaskID, r.ContextID, r.MessageID, r.State)
		}
		if r := response.BifrostA2ATaskResponse; r != nil {
			o.identity(r.TaskID, r.ContextID, "", r.State)
		}
		if response.ResponseBody != nil && len(*response.ResponseBody) <= a2aTraceContentLimit {
			if o.content {
				if safe := sanitizeA2AJSON(*response.ResponseBody); safe != "" {
					o.setContent(schemas.AttrOutputMessages, "["+safe+"]")
				}
			}
		}
	}
	if !o.raw && (!o.content || o.events == 0) {
		return
	}
	span := o.tracer.SpanFromHandle(o.handle)
	if span == nil {
		return
	}
	if o.content && o.events > 0 {
		// Events already live on the span. Build one ordered output for backends
		// that display attributes but do not display span events.
		o.setContent(schemas.AttrOutputMessages, span.A2AStreamOutput(o.spanID))
	}
	if o.raw {
		if capture := schemas.GetA2ARawCapture(ctx); capture != nil {
			request, response := capture.Bodies()
			span.SetA2ARawPayloads(o.spanID, &schemas.A2ASpanData{RawRequest: sanitizeA2ARaw(request), RawResponse: sanitizeA2ARaw(response)})
		}
	}
}

// sanitizeA2AJSON removes authentication/configuration fields before any backend
// sees content. Invalid bodies are omitted rather than exported unsanitized.
func sanitizeA2AJSON(body string) string {
	if body == "" || len(body) > a2aTraceContentLimit {
		return ""
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || !json.Valid([]byte(body)) {
		return ""
	}
	if !redactA2ACredentials(value) {
		return body
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > a2aTraceContentLimit {
		return ""
	}
	return string(data)
}

// redactA2ACredentials strips protocol credentials recursively, including push
// callback config and URL credentials. Application text remains ordinary content.
func redactA2ACredentials(value any) bool {
	changed := false
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			switch normalized {
			case "authentication", "security", "securityschemes", "credentials", "token", "password", "clientsecret", "accesstoken", "refreshtoken", "apikey", "pushnotificationconfig", "pushnotificationconfigs", "taskpushnotificationconfig", "taskpushnotificationconfigs":
				v[key], changed = schemas.RedactedAttrValue, true
				continue
			}
			if schemas.IsSensitiveHeader(key) {
				v[key], changed = schemas.RedactedAttrValue, true
				continue
			}
			if text, ok := child.(string); ok {
				if parsed, err := url.Parse(text); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "ws" || parsed.Scheme == "wss") && (parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
					v[key], changed = schemas.RedactedAttrValue, true
					continue
				}
			}
			if redactA2ACredentials(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range v {
			if redactA2ACredentials(child) {
				changed = true
			}
		}
	}
	return changed
}

// sanitizeA2ARaw handles JSON and SSE HTTP captures. gRPC has no HTTP capture;
// malformed/non-JSON captures are omitted because they cannot be safely redacted.
func sanitizeA2ARaw(body string) string {
	if json.Valid([]byte(body)) {
		return sanitizeA2AJSON(body)
	}
	if body == "" || len(body) > a2aTraceContentLimit {
		return ""
	}
	lines := strings.SplitAfter(body, "\n")
	seen := false
	var dataLines []int
	var data []string
	flush := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		payload := strings.Join(data, "\n")
		safe := payload
		if payload != "[DONE]" {
			safe = sanitizeA2AJSON(payload)
		}
		if safe == "" {
			return false
		}
		if safe != payload {
			lines[dataLines[0]] = "data: " + safe + "\n"
			for _, index := range dataLines[1:] {
				lines[index] = ""
			}
		}
		dataLines, data = dataLines[:0], data[:0]
		seen = true
		return true
	}
	for i, line := range lines {
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if !flush() {
				return ""
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, i)
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "id:") && !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "retry:") {
			return ""
		}
	}
	if !flush() || !seen {
		return ""
	}
	return strings.Join(lines, "")
}
