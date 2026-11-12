package traceingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// LangfuseEvent is one entry of a Langfuse legacy batch (POST /api/public/ingestion).
type LangfuseEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Body      map[string]any `json:"body"`
}

// LangfuseScore is a score sent through the batch or through POST /api/public/scores.
type LangfuseScore struct {
	ID            string
	TraceID       string
	ObservationID string
	Name          string
	Value         *float64
	StringValue   string
	DataType      string
	Comment       string
}

// MapLangfuseEvent maps one batch event. It returns a span part, a score, or neither for an event
// type that carries no trace data (sdk-log). An error means the event is malformed and is reported
// back to the SDK for that event alone.
//
// Langfuse has a trace object apart from its observations. Bifrost has only spans, so the trace
// becomes the root span, with the trace id as its span id, and an observation with no parent hangs
// under it. A "-create" event is the base row of its span and an "-update" event is a merge row.
func MapLangfuseEvent(event *LangfuseEvent) (*SpanPart, *LangfuseScore, error) {
	body := attributes(event.Body)
	kind, action, _ := strings.Cut(event.Type, "-")
	switch kind {
	case "trace":
		id := body.str("id")
		if id == "" {
			return nil, nil, fmt.Errorf("trace event has no id")
		}
		part := &SpanPart{
			TraceID: id, SpanID: id, Name: body.str("name"), Kind: logstore.TraceSpanKindTask,
			Input: jsonContent(event.Body["input"]), Output: jsonContent(event.Body["output"]),
			SessionID: body.str("sessionId"), UserID: body.str("userId"), Environment: body.str("environment"),
			Tags: appendTags(nil, event.Body["tags"]), Metadata: langfuseMetadata(event.Body),
			StartTime: langfuseTime(body.str("timestamp"), event.Timestamp),
		}
		if release := body.str("release"); release != "" {
			part.Attributes = map[string]any{"langfuse.release": release}
		}
		return part, nil, nil
	case "score":
		score := &LangfuseScore{
			ID: body.str("id"), TraceID: body.str("traceId"), ObservationID: body.str("observationId"), Name: body.str("name"),
			DataType: strings.ToLower(body.str("dataType")), Comment: body.str("comment"),
		}
		if score.TraceID == "" || score.Name == "" {
			return nil, nil, fmt.Errorf("score event needs traceId and name")
		}
		switch value := event.Body["value"].(type) {
		case float64:
			score.Value = &value
		case bool:
			score.Value = new(0.0)
			if value {
				score.Value = new(1.0)
			}
		case string:
			score.StringValue = value
		default:
			return nil, nil, fmt.Errorf("score event has no value")
		}
		if score.ID == "" {
			score.ID = event.ID
		}
		return nil, score, nil
	case "span", "generation", "event", "observation":
		id, traceID := body.str("id"), body.str("traceId")
		if id == "" || traceID == "" {
			return nil, nil, fmt.Errorf("%s event needs id and traceId", kind)
		}
		if kind == "observation" {
			kind = strings.ToLower(body.str("type"))
		}
		part := &SpanPart{
			TraceID: traceID, SpanID: id, IsMerge: action == "update", Name: body.str("name"), Kind: KindFor(kind),
			Input: jsonContent(event.Body["input"]), Output: jsonContent(event.Body["output"]),
			Model: body.str("model"), Environment: body.str("environment"), Metadata: langfuseMetadata(event.Body),
		}
		if !part.IsMerge {
			// An observation with no parent sits under the trace's root span.
			part.ParentSpanIDs = []string{traceID}
			part.StartTime = langfuseTime(body.str("startTime"), event.Timestamp)
		} else if start := langfuseTime(body.str("startTime"), ""); start != nil {
			part.StartTime = start
		}
		if parent := body.str("parentObservationId"); parent != "" {
			part.ParentSpanIDs = []string{parent}
		}
		part.EndTime = langfuseTime(body.str("endTime"), "")
		if kind == "event" && part.EndTime == nil {
			part.EndTime = part.StartTime
		}
		if strings.EqualFold(body.str("level"), "ERROR") {
			part.Status = logstore.TraceSpanStatusError
			part.Error = body.str("statusMessage")
		}
		for _, key := range []string{"usageDetails", "usage"} {
			if usage, ok := event.Body[key].(map[string]any); ok {
				u := attributes(usage)
				setIntIfZero(&part.PromptTokens, u.integer("input", "promptTokens", "prompt_tokens", "input_tokens"))
				setIntIfZero(&part.CompletionTokens, u.integer("output", "completionTokens", "completion_tokens", "output_tokens"))
				setIntIfZero(&part.TotalTokens, u.integer("total", "totalTokens", "total_tokens"))
				setIntIfZero(&part.CachedReadTokens, u.integer("cache_read_input_tokens", "input_cached_tokens", "input_cache_read"))
				setIntIfZero(&part.CachedWriteTokens, u.integer("cache_creation_input_tokens", "input_cache_creation"))
				if cost, ok := u.num("totalCost"); ok && part.Cost == nil {
					part.Cost = &cost
				}
			}
		}
		if costs, ok := event.Body["costDetails"].(map[string]any); ok {
			if total, ok := attributes(costs).num("total"); ok {
				part.Cost = &total
			}
		}
		if first := langfuseTime(body.str("completionStartTime"), ""); first != nil && part.StartTime != nil {
			part.TimeToFirstToken = new(float64(first.Sub(*part.StartTime)) / float64(time.Millisecond))
		}
		if parameters, ok := event.Body["modelParameters"].(map[string]any); ok && len(parameters) > 0 {
			part.Attributes = map[string]any{"langfuse.model_parameters": parameters}
		}
		return part, nil, nil
	}
	return nil, nil, nil
}

// jsonContent passes a JSON value through as span content. A string holding JSON is decoded, as
// the SDKs sometimes serialise content before sending it.
func jsonContent(value any) any {
	if text, ok := value.(string); ok {
		return decodeValue(text)
	}
	return value
}

// langfuseMetadata reads the metadata object of an event body. A non-object metadata value is kept
// under one key.
func langfuseMetadata(body map[string]any) map[string]any {
	switch metadata := body["metadata"].(type) {
	case map[string]any:
		return metadata
	case nil:
		return nil
	default:
		return map[string]any{"metadata": metadata}
	}
}

// langfuseTime parses the first of the given ISO 8601 timestamps that is set.
func langfuseTime(values ...string) *time.Time {
	for _, value := range values {
		if value == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999-07:00"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return new(parsed.UTC())
			}
		}
	}
	return nil
}

// AddScores stores scores that arrive on their own, apart from a span row, and queues the rollup of
// the traces they belong to.
func (s *Service) AddScores(ctx context.Context, principal *Principal, projectID string, scores []*logstore.TraceScore) error {
	if len(scores) == 0 {
		return nil
	}
	if !principal.allows(projectID) {
		return ErrForbidden
	}
	keys := make([]logstore.TraceKey, 0, len(scores))
	for _, score := range scores {
		score.ProjectID = projectID
		if score.ID == "" {
			score.ID = rowID("score", projectID, score.TraceID, score.SpanID, score.Name, score.Source)
		}
		keys = append(keys, logstore.TraceKey{ProjectID: projectID, TraceID: score.TraceID})
	}
	if err := s.store.UpsertTraceScores(ctx, scores); err != nil {
		return fmt.Errorf("failed to store scores: %w", err)
	}
	return s.store.MarkTracesDirty(ctx, keys)
}

// AddComments stores comments that arrive on their own, apart from a span row.
func (s *Service) AddComments(ctx context.Context, principal *Principal, projectID string, comments []*logstore.TraceComment) error {
	if !principal.allows(projectID) {
		return ErrForbidden
	}
	for _, comment := range comments {
		comment.ProjectID = projectID
		if comment.ID == "" {
			comment.ID = rowID("comment", projectID, comment.TraceID, comment.SpanID, comment.Text)
		}
		if err := s.store.CreateTraceComment(ctx, comment); err != nil {
			return fmt.Errorf("failed to store comment: %w", err)
		}
	}
	return nil
}

// TraceScore turns a Langfuse score into a stored score.
func (score *LangfuseScore) TraceScore(source string) *logstore.TraceScore {
	spanID := score.ObservationID
	dataType := score.DataType
	if dataType == "" {
		dataType = "numeric"
		if score.Value == nil {
			dataType = "categorical"
		}
	}
	return &logstore.TraceScore{ID: score.ID, TraceID: score.TraceID, SpanID: spanID, Name: score.Name, Value: score.Value, StringValue: score.StringValue, DataType: dataType, Comment: score.Comment, Source: source}
}
