package datasets

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
)

// SpanReader reads spans of traces pushed by observability SDKs. The log store implements it.
type SpanReader interface {
	FindTraceSpans(ctx context.Context, projectID string, keys []logstore.TraceSpanKey) ([]*logstore.TraceSpan, error)
}

// SetSpanReader gives the service the trace store. Without one, rows cannot be added from spans.
func (s *Service) SetSpanReader(spans SpanReader) { s.spans = spans }

// ErrNoTraceStore is returned by AddFromSpans when the gateway has no trace store.
var ErrNoTraceStore = invalid("adding rows from traces needs a log store, and this gateway has none")

// SpanRef names one span of a trace.
type SpanRef struct {
	ProjectID string `json:"project_id"`
	TraceID   string `json:"trace_id"`
	SpanID    string `json:"span_id"`
}

func (r SpanRef) id() string { return r.TraceID + "/" + r.SpanID }

// spanMessage is one message of a span's logged input.
type spanMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// spanText flattens logged content to text: a string as it is, content parts joined.
func spanText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			if text := spanText(part); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "message_content"} {
			if inner, ok := v[key]; ok {
				return spanText(inner)
			}
		}
	}
	return ""
}

// spanMessages reads a message list out of a span's stored input or output, in the shapes SDKs
// log one: a bare list, an object holding one, and OpenInference's {"message": {...}} wrappers.
// It returns nil when the value is not a message list.
func spanMessages(stored string) []spanMessage {
	trimmed := strings.TrimSpace(stored)
	if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"messages", "input", "choices"} {
			if inner, found := object[key]; found {
				value = inner
				break
			}
		}
	}
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	var messages []spanMessage
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if wrapped, isWrapped := object["message"].(map[string]any); isWrapped {
			object = wrapped
		}
		role, _ := object["role"].(string)
		var content any
		for _, key := range []string{"content", "contents", "parts"} {
			if inner, found := object[key]; found {
				content = inner
				break
			}
		}
		if role == "" {
			continue
		}
		messages = append(messages, spanMessage{Role: role, Content: spanText(content)})
	}
	return messages
}

// SpanCells builds a row's cells from one span, by column type. A model call's input is a message
// list: its last user message is the input and the messages before it are the history. Any other
// span's input is copied as it is. It returns false when the span holds no content, which is the
// case when its tracing key had content off.
func SpanCells(span *logstore.TraceSpan, columns []Column) (map[string]string, bool) {
	if span.Input == "" && span.Output == "" {
		return nil, false
	}
	input, history := span.Input, ""
	if messages := spanMessages(span.Input); len(messages) > 0 {
		last := -1
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				last = i
				break
			}
		}
		if last >= 0 {
			input = messages[last].Content
			if last > 0 {
				history = marshalString(messages[:last])
			}
		}
	}
	output := span.Output
	if replies := spanMessages(span.Output); len(replies) > 0 {
		output = replies[len(replies)-1].Content
	}
	cells := make(map[string]string, 4)
	for _, column := range columns {
		switch column.Type {
		case ColumnInput:
			cells[column.ID] = input
		case ColumnConversationHistory:
			if history != "" {
				cells[column.ID] = history
			}
		case ColumnOutput:
			cells[column.ID] = output
		case ColumnExpectedOutput:
			if span.Expected != "" {
				cells[column.ID] = span.Expected
			}
		}
	}
	return cells, true
}

// AddFromSpans adds one row per span of a pushed trace, mapped by column type. Spans that are
// missing or hold no content are skipped and reported.
func (s *Service) AddFromSpans(ctx context.Context, id string, refs []SpanRef, splitID, actor string) (*CurateResult, error) {
	if s.spans == nil {
		return nil, ErrNoTraceStore
	}
	seen := make(map[SpanRef]struct{}, len(refs))
	unique := make([]SpanRef, 0, len(refs))
	for _, ref := range refs {
		if ref.ProjectID == "" || ref.TraceID == "" || ref.SpanID == "" {
			return nil, invalid("each span needs project_id, trace_id and span_id")
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		unique = append(unique, ref)
	}
	if len(unique) == 0 {
		return nil, invalid("at least one span is required")
	}
	if len(unique) > MaxLogsPerRequest {
		return nil, invalid("at most %d spans can be added in one call", MaxLogsPerRequest)
	}
	if _, err := s.store.GetEvalDataset(ctx, id); err != nil {
		return nil, err
	}
	result := &CurateResult{Skipped: []Skipped{}}
	spans := make([]*logstore.TraceSpan, 0, len(unique))
	for _, ref := range unique {
		found, err := s.spans.FindTraceSpans(ctx, ref.ProjectID, []logstore.TraceSpanKey{{TraceID: ref.TraceID, SpanID: ref.SpanID}})
		if err != nil || len(found) == 0 {
			result.Skipped = append(result.Skipped, Skipped{ID: ref.id(), Reason: "span not found"})
			continue
		}
		spans = append(spans, found[0])
	}
	version, err := s.write(ctx, id, actor, "Added from traces", func(tx configstore.EvalDatasetTx) error {
		stored, err := tx.Columns()
		if err != nil {
			return err
		}
		columns := toColumns(stored)
		encoded := make([]string, 0, len(spans))
		for _, span := range spans {
			ref := SpanRef{TraceID: span.TraceID, SpanID: span.SpanID}
			cells, ok := SpanCells(span, columns)
			if !ok {
				result.Skipped = append(result.Skipped, Skipped{ID: ref.id(), Reason: "the span has no content; its tracing key does not store input and output"})
				continue
			}
			if len(cells) == 0 {
				result.Skipped = append(result.Skipped, Skipped{ID: ref.id(), Reason: "the dataset has no input, output, conversation history or expected output column to fill"})
				continue
			}
			encoded = append(encoded, EncodeCellsMap(columns, cells))
		}
		if _, err := tx.AddRows(encoded, splitID); err != nil {
			return err
		}
		result.RowsAdded = len(encoded)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if version != nil {
		result.Version = new(version.Number)
	}
	return result, nil
}
