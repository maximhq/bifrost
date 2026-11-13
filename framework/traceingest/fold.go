package traceingest

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Fold builds a span from its parts. It does not trust arrival order: SDKs post batches in
// parallel and retry them, so a merge row can land before the base row it updates.
//
//   - The row that is not a merge is the base, wherever it arrived.
//   - Merge rows are laid over the base in receive order. Maps merge key by key, tags are unioned,
//     and a field a part does not set keeps its value.
//
// When parts holds the base row, the span is rebuilt from the parts alone, so the same parts give
// the same span in any order. When it does not (the base expired, or has not arrived), the parts
// are laid over the stored span, so a late merge row still applies.
//
// stored is the span as last folded, nil when there is none. Fields the fold does not own (the
// gateway link, the cost estimate, the first-seen time) are carried over from it.
func Fold(stored *logstore.TraceSpan, parts []*SpanPart) *SpanPart {
	ordered := slices.Clone(parts)
	slices.SortStableFunc(ordered, func(a, b *SpanPart) int {
		if a.IsMerge != b.IsMerge {
			if a.IsMerge {
				return 1
			}
			return -1
		}
		if c := a.ReceivedAt.Compare(b.ReceivedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	hasBase := len(ordered) > 0 && !ordered[0].IsMerge
	acc := &SpanPart{IsMerge: true}
	if !hasBase && stored != nil {
		acc = partFromSpan(stored)
	}
	for _, part := range ordered {
		overlay(acc, part)
		if !part.IsMerge {
			acc.IsMerge = false
		}
	}
	return acc
}

// overlay lays src over dst: a field src sets wins, a field it leaves empty is kept.
func overlay(dst, src *SpanPart) {
	if dst.TraceID == "" {
		dst.TraceID, dst.SpanID = src.TraceID, src.SpanID
	}
	if dst.ReceivedAt.IsZero() || (!src.ReceivedAt.IsZero() && src.ReceivedAt.Before(dst.ReceivedAt)) {
		dst.ReceivedAt = src.ReceivedAt
	}
	if len(src.ParentSpanIDs) > 0 {
		dst.ParentSpanIDs = src.ParentSpanIDs
	}
	for _, field := range []struct{ dst, src *string }{
		{&dst.RowID, &src.RowID}, {&dst.Name, &src.Name}, {&dst.Kind, &src.Kind}, {&dst.Status, &src.Status},
		{&dst.Error, &src.Error}, {&dst.Model, &src.Model}, {&dst.Provider, &src.Provider},
		{&dst.SessionID, &src.SessionID}, {&dst.UserID, &src.UserID}, {&dst.Environment, &src.Environment},
		{&dst.ExperimentID, &src.ExperimentID}, {&dst.DatasetID, &src.DatasetID},
		{&dst.ProviderResponseID, &src.ProviderResponseID},
	} {
		if *field.src != "" {
			*field.dst = *field.src
		}
	}
	for _, field := range []struct{ dst, src *int }{
		{&dst.PromptTokens, &src.PromptTokens}, {&dst.CompletionTokens, &src.CompletionTokens},
		{&dst.TotalTokens, &src.TotalTokens}, {&dst.CachedReadTokens, &src.CachedReadTokens},
		{&dst.CachedWriteTokens, &src.CachedWriteTokens}, {&dst.ReasoningTokens, &src.ReasoningTokens},
	} {
		if *field.src != 0 {
			*field.dst = *field.src
		}
	}
	if src.StartTime != nil {
		dst.StartTime = src.StartTime
	}
	if src.EndTime != nil {
		dst.EndTime = src.EndTime
	}
	if src.Cost != nil {
		dst.Cost = src.Cost
	}
	if src.TimeToFirstToken != nil {
		dst.TimeToFirstToken = src.TimeToFirstToken
	}
	dst.Input = mergeValue(dst.Input, src.Input)
	dst.Output = mergeValue(dst.Output, src.Output)
	dst.Expected = mergeValue(dst.Expected, src.Expected)
	dst.Metadata = mergeMap(dst.Metadata, src.Metadata)
	dst.Attributes = mergeMap(dst.Attributes, src.Attributes)
	for _, tag := range src.Tags {
		if !slices.Contains(dst.Tags, tag) {
			dst.Tags = append(dst.Tags, tag)
		}
	}
	slices.Sort(dst.Tags)
	for name, value := range src.Scores {
		if dst.Scores == nil {
			dst.Scores = make(map[string]float64, len(src.Scores))
		}
		dst.Scores[name] = value
	}
	for _, comment := range src.Comments {
		if !slices.Contains(dst.Comments, comment) {
			dst.Comments = append(dst.Comments, comment)
		}
	}
}

// mergeValue lays src over dst. Two objects merge key by key; anything else is replaced. A nil src
// says nothing and leaves dst alone.
func mergeValue(dst, src any) any {
	if src == nil {
		return dst
	}
	dstMap, dstIsMap := dst.(map[string]any)
	srcMap, srcIsMap := src.(map[string]any)
	if dstIsMap && srcIsMap {
		return mergeMap(dstMap, srcMap)
	}
	return src
}

func mergeMap(dst, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	merged := make(map[string]any, len(dst)+len(src))
	for key, value := range dst {
		merged[key] = value
	}
	for key, value := range src {
		merged[key] = mergeValue(merged[key], value)
	}
	return merged
}

// partFromSpan reads a stored span back into the shape parts are folded in.
func partFromSpan(span *logstore.TraceSpan) *SpanPart {
	part := &SpanPart{
		TraceID: span.TraceID, SpanID: span.SpanID, ParentSpanIDs: span.ParentSpanIDsParsed, RowID: span.RowID,
		IsMerge: span.Partial, Name: span.Name, Status: span.Status, Error: span.Error, EndTime: span.EndTime,
		Metadata: span.MetadataParsed, Tags: slices.Clone(span.TagsParsed), Attributes: span.AttributesParsed,
		Model: span.Model, Provider: span.Provider, PromptTokens: span.PromptTokens, CompletionTokens: span.CompletionTokens,
		TotalTokens: span.TotalTokens, CachedReadTokens: span.CachedReadTokens, CachedWriteTokens: span.CachedWriteTokens,
		ReasoningTokens: span.ReasoningTokens, TimeToFirstToken: span.TimeToFirstTokenMs,
		SessionID: span.SessionID, UserID: span.UserID, Environment: span.Environment, ExperimentID: span.ExperimentID,
		DatasetID: span.DatasetID, ProviderResponseID: span.ProviderResponseID,
		Input: decodeValue(span.Input), Output: decodeValue(span.Output), Expected: decodeValue(span.Expected),
	}
	if span.Kind != logstore.TraceSpanKindUnknown {
		part.Kind = span.Kind
	}
	if !span.StartTime.IsZero() {
		part.StartTime = &span.StartTime
	}
	if !span.CostEstimated {
		part.Cost = span.Cost
	}
	return part
}

// Span turns a folded part into the row the trace store keeps. stored is the row it replaces, nil
// when there is none.
func (p *SpanPart) Span(projectID, source string, stored *logstore.TraceSpan) *logstore.TraceSpan {
	span := &logstore.TraceSpan{
		ProjectID: projectID, TraceID: p.TraceID, SpanID: p.SpanID, ParentSpanIDsParsed: p.ParentSpanIDs, RowID: p.RowID,
		Source: source, Name: p.Name, Kind: p.Kind, Status: p.Status, Error: p.Error, Partial: p.IsMerge,
		EndTime: p.EndTime, MetadataParsed: p.Metadata, TagsParsed: p.Tags, AttributesParsed: p.Attributes,
		Model: p.Model, Provider: p.Provider, PromptTokens: p.PromptTokens, CompletionTokens: p.CompletionTokens,
		TotalTokens: p.TotalTokens, CachedReadTokens: p.CachedReadTokens, CachedWriteTokens: p.CachedWriteTokens,
		ReasoningTokens: p.ReasoningTokens, Cost: p.Cost, TimeToFirstTokenMs: p.TimeToFirstToken,
		SessionID: p.SessionID, UserID: p.UserID, Environment: p.Environment, ExperimentID: p.ExperimentID,
		DatasetID: p.DatasetID, ProviderResponseID: p.ProviderResponseID,
		Input: encodeValue(p.Input), Output: encodeValue(p.Output), Expected: encodeValue(p.Expected),
	}
	if span.Kind == "" {
		span.Kind = logstore.TraceSpanKindUnknown
	}
	if span.TotalTokens == 0 {
		span.TotalTokens = span.PromptTokens + span.CompletionTokens
	}
	if span.Status == "" {
		span.Status = logstore.TraceSpanStatusOK
		if span.Error != "" {
			span.Status = logstore.TraceSpanStatusError
		}
	}
	switch {
	case p.StartTime != nil:
		span.StartTime = *p.StartTime
	case stored != nil:
		span.StartTime = stored.StartTime
	default:
		// A merge row that carries no start time, ahead of its base: place it at arrival.
		span.StartTime = time.Now().UTC()
		if !p.ReceivedAt.IsZero() {
			span.StartTime = p.ReceivedAt
		}
	}
	if stored != nil {
		span.CreatedAt = stored.CreatedAt
		span.ExpiresAt = stored.ExpiresAt
		span.ContentHidden = stored.ContentHidden
		span.LinkMethod, span.LinkSnapshotParsed, span.LinkedLogExpired = stored.LinkMethod, stored.LinkSnapshotParsed, stored.LinkedLogExpired
		if span.Cost == nil && stored.CostEstimated {
			span.Cost, span.CostEstimated = stored.Cost, true
		}
	}
	// Derive the tree parent and the duration now, so the span reads the same before it is stored.
	_ = span.SerializeFields()
	return span
}

// encodeValue stores a span value as JSON. A plain string is stored as it is, so text reads as text.
func encodeValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	data, err := schemas.MarshalSorted(value)
	if err != nil {
		return ""
	}
	return string(data)
}

// decodeValue reads a stored span value back. Text that is not a JSON object or array is a string.
func decodeValue(stored string) any {
	if stored == "" {
		return nil
	}
	if trimmed := strings.TrimSpace(stored); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var value any
		if err := sonic.Unmarshal([]byte(stored), &value); err == nil {
			return value
		}
	}
	return stored
}
