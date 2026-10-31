package maxim

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/maxim-go/logging"
)

// Inject queues completed A2A operations through the same SDK logger as LLM
// exports. Original times remain metadata until the SDK supports overriding them.
func (plugin *Plugin) Inject(_ context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}
	var ops []*schemas.Span
	for _, span := range trace.Spans {
		if span != nil && span.Kind == schemas.SpanKindA2AOperation {
			ops = append(ops, span)
		}
	}
	if len(ops) == 0 {
		return nil
	}
	repo := plugin.defaultLogRepoID
	if override := trace.RequestHeaders["x-bf-maxim-log-repo-id"]; override != "" {
		repo = override
	}
	if repo == "" {
		return nil
	}
	logger, err := plugin.getOrCreateLogger(repo)
	if err != nil {
		return err
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(trace.RequestID+":"+trace.InternalID+":"+trace.TraceID)).String()
	name := ops[0].Name
	config := logging.TraceConfig{Id: id, Name: &name}
	if session := schemas.GetStringAttr(trace.Attributes, schemas.TraceAttrSessionID); session != "" {
		config.SessionId = &session
	}
	logged := logger.Trace(&config)
	logged.AddMetadata(map[string]interface{}{"trace_id": trace.TraceID, "request_id": trace.RequestID, "start_time": trace.StartTime, "end_time": trace.EndTime})
	for key, value := range schemas.RedactSensitiveHeaders(schemas.FilterHeaders(trace.RequestHeaders, plugin.requestHeaders)) {
		logged.AddTag(key, value)
	}
	content := !schemas.ContentLoggingDisabledForTrace(trace)
	for _, op := range ops {
		tags := a2aTags(op.Attributes)
		tags["status"] = string(op.Status)
		span := logged.AddSpan(&logging.SpanConfig{Id: uuid.New().String(), Name: &op.Name, Tags: &tags})
		data := map[string]interface{}{"start_time": op.StartTime, "end_time": op.EndTime}
		if content {
			if input := schemas.GetStringAttr(op.Attributes, schemas.AttrInputMessages); input != "" {
				logged.SetInput(input)
				data[schemas.AttrInputMessages] = input
			}
			if output := schemas.GetStringAttr(op.Attributes, schemas.AttrOutputMessages); output != "" {
				logged.SetOutput(output)
				data[schemas.AttrOutputMessages] = output
			}
		}
		span.AddMetadata(data)
		if !op.StartTime.IsZero() && !op.EndTime.IsZero() {
			span.AddMetric("duration_ms", float64(op.EndTime.Sub(op.StartTime).Microseconds())/1000)
		}
		if op.Status == schemas.SpanStatusError {
			message := schemas.GetStringAttr(op.Attributes, schemas.AttrErrorTypeSpec)
			if content && op.StatusMsg != "" {
				message = op.StatusMsg
			}
			failure := span.AddError(&logging.ErrorConfig{Id: uuid.New().String(), Name: &message, Tags: &tags})
			failure.AddMetadata(map[string]interface{}{"error_type": schemas.GetStringAttr(op.Attributes, schemas.AttrErrorTypeSpec), "message": message})
			failure.End()
		}
		for _, event := range op.Events {
			eventTags := a2aTags(event.Attributes)
			eventTags["timestamp"] = event.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00")
			if content {
				if body := schemas.GetStringAttr(event.Attributes, schemas.AttrOutputMessages); body != "" {
					eventTags[schemas.AttrOutputMessages] = body
				}
			}
			span.AddEvent(uuid.New().String(), event.Name, &eventTags)
		}
		for k, v := range tags {
			logged.AddTag(k, v)
		}
		span.End()
	}
	logged.End()
	return nil
}

// RequestHeaderPatterns requests the repository override and configured headers.
func (plugin *Plugin) RequestHeaderPatterns() []string {
	return append(append([]string(nil), plugin.requestHeaders...), "x-bf-maxim-log-repo-id")
}

// a2aTags selects protocol and governance metadata without copying content or
// raw payload carriers into tags, including when content logging is disabled.
func a2aTags(attrs map[string]any) map[string]string {
	tags := make(map[string]string)
	for _, key := range []string{
		"bifrost.a2a.push.config.id", "bifrost.a2a.push.delivery.id", "bifrost.a2a.push.attempt.id",
		schemas.AttrAgentName, schemas.AttrBifrostA2AOperationName,
		schemas.AttrA2AMethodName, schemas.AttrBifrostA2AUpstreamTransport,
		schemas.AttrA2ATaskID, schemas.AttrA2ATaskState, schemas.AttrConversationID,
		schemas.AttrA2AMessageID, schemas.AttrA2AArtifactID, schemas.AttrErrorTypeSpec,
		schemas.AttrBifrostVirtualKeyID, schemas.AttrBifrostVirtualKeyName,
		schemas.AttrBifrostTeamID, schemas.AttrBifrostTeamName,
		schemas.AttrBifrostCustomerID, schemas.AttrBifrostCustomerName,
		schemas.AttrBifrostBusinessUnitID, schemas.AttrBifrostBusinessUnitName,
		schemas.AttrBifrostProjectID, schemas.AttrBifrostProjectName,
	} {
		if value, ok := attrs[key]; ok {
			tags[key] = fmt.Sprint(value)
		}
	}
	return tags
}
