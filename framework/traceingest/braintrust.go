package traceingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// BraintrustRow is one row of a Braintrust logs3 request after mapping. A row is a span part, a
// comment on an existing row, or feedback on an existing row. Feedback and comments name their
// target by row id only, so the caller resolves that id to a span before storing them.
type BraintrustRow struct {
	// ProjectID, ExperimentID and DatasetID are the object the row was logged to.
	ProjectID    string
	ExperimentID string
	DatasetID    string
	// Part is the span part. For feedback its TraceID and SpanID are empty until TargetRowID is resolved.
	Part *SpanPart
	// TargetRowID is the row a feedback or comment row points at.
	TargetRowID string
	// Comment is the text of a comment row.
	Comment string
	// Deleted marks a row the SDK asked to delete.
	Deleted bool
}

// MapBraintrustRow maps one logs3 row. Braintrust's own field names:
//
//	id (the row id), span_id, root_span_id (the trace), span_parents, span_attributes {name, type},
//	input, output, expected, error, scores, metadata, tags,
//	metrics {start, end, prompt_tokens, completion_tokens, tokens, prompt_cached_tokens, ...},
//	_is_merge, _object_delete, origin {id}, comment {text}
//
// Times in metrics are epoch seconds. A row with no span id is feedback or a comment on the row
// named by id (feedback) or origin.id (comment).
func MapBraintrustRow(row map[string]any) (*BraintrustRow, error) {
	fields := attributes(row)
	mapped := &BraintrustRow{ProjectID: fields.str("project_id"), ExperimentID: fields.str("experiment_id"), DatasetID: fields.str("dataset_id")}
	if deleted, _ := row["_object_delete"].(bool); deleted {
		mapped.Deleted = true
	}
	rowID := fields.str("id")
	if comment, ok := row["comment"].(map[string]any); ok {
		mapped.Comment = attributes(comment).str("text")
		if origin, ok := row["origin"].(map[string]any); ok {
			mapped.TargetRowID = attributes(origin).str("id")
		}
		if mapped.TargetRowID == "" {
			mapped.TargetRowID = rowID
		}
		if mapped.Comment == "" || mapped.TargetRowID == "" {
			return nil, fmt.Errorf("comment row needs text and a target row")
		}
		return mapped, nil
	}

	isMerge, _ := row["_is_merge"].(bool)
	part := &SpanPart{
		TraceID: fields.str("root_span_id"), SpanID: fields.str("span_id"), RowID: rowID, IsMerge: isMerge,
		Input: row["input"], Output: row["output"], Expected: row["expected"],
		ExperimentID: mapped.ExperimentID, DatasetID: mapped.DatasetID,
		Tags: appendTags(nil, row["tags"]),
	}
	if parents, ok := row["span_parents"].([]any); ok {
		for _, parent := range parents {
			if id, isString := parent.(string); isString && id != "" {
				part.ParentSpanIDs = append(part.ParentSpanIDs, id)
			}
		}
	}
	if spanAttributes, ok := row["span_attributes"].(map[string]any); ok {
		sa := attributes(spanAttributes)
		part.Name = sa.str("name")
		part.Kind = KindFor(sa.str("type"))
		rest := make(map[string]any)
		for key, value := range spanAttributes {
			if key != "name" && key != "type" {
				rest[key] = value
			}
		}
		if len(rest) > 0 {
			part.Attributes = map[string]any{"span_attributes": rest}
		}
	}
	if !isMerge && part.Kind == "" {
		// Braintrust leaves the type off plain traced() spans. They are units of work.
		part.Kind = logstore.TraceSpanKindTask
	}
	if metadata, ok := row["metadata"].(map[string]any); ok && len(metadata) > 0 {
		part.Metadata = metadata
		md := attributes(metadata)
		part.Model = md.str("model")
		part.Provider = md.str("provider")
		part.SessionID = md.str("session_id", "sessionId", "thread_id", "conversation_id")
		part.UserID = md.str("user_id", "userId")
		part.Environment = md.str("environment", "env")
	}
	if metrics, ok := row["metrics"].(map[string]any); ok {
		m := attributes(metrics)
		part.StartTime = epochSeconds(m, "start")
		part.EndTime = epochSeconds(m, "end")
		part.PromptTokens = m.integer("prompt_tokens")
		part.CompletionTokens = m.integer("completion_tokens")
		part.TotalTokens = m.integer("tokens", "total_tokens")
		part.CachedReadTokens = m.integer("prompt_cached_tokens")
		part.CachedWriteTokens = m.integer("prompt_cache_creation_tokens")
		part.ReasoningTokens = m.integer("completion_reasoning_tokens")
		if ttft, ok := m.num("time_to_first_token"); ok {
			part.TimeToFirstToken = new(ttft * 1000)
		}
		if cost, ok := m.num("estimated_cost"); ok {
			part.Cost = &cost
		}
	}
	if part.StartTime == nil && !isMerge {
		if created := langfuseTime(fields.str("created")); created != nil {
			part.StartTime = created
		}
	}
	switch failure := row["error"].(type) {
	case nil:
	case string:
		part.Error = failure
	default:
		part.Error = encodeValue(failure)
	}
	if part.Error != "" {
		part.Status = logstore.TraceSpanStatusError
	}
	if scores, ok := row["scores"].(map[string]any); ok {
		for name, value := range scores {
			if number, isNumber := value.(float64); isNumber && !math.IsNaN(number) {
				if part.Scores == nil {
					part.Scores = make(map[string]float64, len(scores))
				}
				part.Scores[name] = number
			}
		}
	}
	if context, ok := row["context"].(map[string]any); ok && len(context) > 0 {
		if part.Attributes == nil {
			part.Attributes = make(map[string]any, 1)
		}
		part.Attributes["context"] = context
	}
	mapped.Part = part
	if part.SpanID == "" || part.TraceID == "" {
		// Feedback: scores, expected, tags or metadata laid over an existing row.
		if rowID == "" {
			return nil, fmt.Errorf("row has neither a span id nor a row id")
		}
		mapped.TargetRowID = rowID
		part.IsMerge = true
		part.Kind = ""
	}
	return mapped, nil
}

// epochSeconds reads a metrics time, which Braintrust sends as fractional epoch seconds.
func epochSeconds(m *attrs, key string) *time.Time {
	seconds, ok := m.num(key)
	if !ok || seconds <= 0 {
		return nil
	}
	whole, fraction := math.Modf(seconds)
	return new(time.Unix(int64(whole), int64(fraction*1e9)).UTC())
}

// BraintrustObjectID builds the id Bifrost issues for an experiment or a dataset of a project. The
// id carries its project, so a row logged to the object finds its project with no lookup, and the
// same name always gets the same id.
func BraintrustObjectID(kind, projectID, name string) string {
	sum := sha256.Sum256([]byte(name))
	return kind + "-" + projectID + "-" + hex.EncodeToString(sum[:6])
}

// BraintrustObjectProject returns the project an issued experiment or dataset id belongs to.
func BraintrustObjectProject(kind, id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, kind+"-")
	if !ok || len(rest) < 14 || rest[len(rest)-13] != '-' {
		return "", false
	}
	return rest[:len(rest)-13], true
}
