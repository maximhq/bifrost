package logging

import (
	"sort"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/overhead"
)

// rowLatency is one log row's own share of a multi-row trace.
type rowLatency struct {
	latencyMs  float64
	upstreamMs float64
	overheadMs float64
	breakdown  []logstore.OverheadBucket
}

// splitTraceLatency gives each row of a trace that logged several rows (agent-loop
// turns, fallback attempts, list_models pages) its own latency, upstream and overhead.
//
// Sequential rows each cover their own attempt window. The first row also covers the
// work before it (transport parse, routing) back to the root span's start, and the last
// row the work after it (response write) up to the root span's end and the request's
// final upstream total. Time between rows, such as MCP tool execution between agent
// turns, belongs to no row. Each row's breakdown is computed from the spans that start
// inside its window.
//
// Rows that overlap in time (a concurrent list_models fan-out) keep their own windows
// with no edge extension and no breakdown: their spans interleave and cannot be split.
func splitTraceLatency(trace *schemas.Trace, windows []schemas.AttemptWindow, rootUpstreamMs float64, rootUpstreamOK bool) []rowLatency {
	out := make([]rowLatency, len(windows))
	order := make([]int, len(windows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return windows[order[a]].Start.Before(windows[order[b]].Start) })

	sequential := true
	for k := 1; k < len(order); k++ {
		if windows[order[k]].Start.Before(windows[order[k-1]].End) {
			sequential = false
			break
		}
	}
	root := trace.RootSpan
	extendEdges := sequential && root != nil && !root.StartTime.IsZero() && root.EndTime.After(root.StartTime)

	for k, idx := range order {
		w := windows[idx]
		start, end := w.Start, w.End
		upStart, upEnd := w.UpstreamStart, w.UpstreamEnd
		isFirst, isLast := k == 0, k == len(order)-1
		if extendEdges {
			if isFirst {
				start, upStart = root.StartTime, 0
			}
			if isLast {
				end = root.EndTime
				if rootUpstreamOK {
					upEnd = time.Duration(rootUpstreamMs * float64(time.Millisecond))
				}
			}
		}
		latency := end.Sub(start)
		if latency < 0 {
			latency = 0
		}
		upstream := upEnd - upStart
		if upstream < 0 {
			upstream = 0
		}
		if upstream > latency {
			upstream = latency
		}
		row := rowLatency{
			latencyMs:  durationMs(latency),
			upstreamMs: durationMs(upstream),
			overheadMs: durationMs(latency - upstream),
		}
		if sequential {
			// Only the last row keeps the root span: its attributes carry the stream and
			// worker-handoff timings, which belong to the attempt that answered.
			sub := windowTrace(trace, start, end, isLast)
			if isLast && sub.RootSpan != nil {
				sub.RootSpan = withoutEarlierStreamWork(sub.RootSpan, w.StreamBefore)
			}
			buckets, measuredMs, isStreaming := overhead.Compute(sub, row.overheadMs, true, row.upstreamMs, true)
			if isStreaming {
				// Same rule as a single-row stream: overhead is the measured Bifrost CPU and
				// the off-CPU relay wait between chunks is folded into upstream.
				measured := min(max(measuredMs, 0), row.overheadMs)
				row.upstreamMs = row.latencyMs - measured
				row.overheadMs = measured
			}
			row.breakdown = buckets
		}
		out[idx] = row
	}
	return out
}

// applyLaterAttemptWindow sets a non-streaming row's provisional numbers from its own
// attempt window when it is not the request's first attempt. A later attempt's
// ExtraFields carry request-wide totals, and rows written without a trace never
// reach Inject's per-row split. The first attempt keeps its ExtraFields, which already
// cover the request from its start.
func applyLaterAttemptWindow(entry *logstore.Log, w schemas.AttemptWindow) {
	if entry.Stream || w.Index <= 1 {
		return
	}
	latency := w.End.Sub(w.Start)
	upstream := min(w.Upstream(), latency)
	latencyMs, upstreamMs, overheadMs := durationMs(latency), durationMs(upstream), durationMs(latency-upstream)
	entry.Latency = &latencyMs
	entry.UpstreamLatency = &upstreamMs
	entry.OverheadLatency = &overheadMs
}

// allWindowsMeasured reports whether every one of n parked rows has an attempt window.
func allWindowsMeasured(windows []rowWindow, n int) bool {
	if len(windows) != n {
		return false
	}
	for _, w := range windows {
		if !w.ok {
			return false
		}
	}
	return true
}

// windowTrace returns a view of trace holding only the spans that start in [start, end],
// each clipped to end so time after the row's window never reaches its breakdown.
func windowTrace(trace *schemas.Trace, start, end time.Time, keepRoot bool) *schemas.Trace {
	spans := make([]*schemas.Span, 0, len(trace.Spans))
	for _, s := range trace.Spans {
		if s == nil || s.StartTime.Before(start) || s.StartTime.After(end) {
			continue
		}
		if s.EndTime.After(end) {
			s = clipSpanEnd(s, end)
		}
		spans = append(spans, s)
	}
	sub := &schemas.Trace{Spans: spans}
	if keepRoot {
		sub.RootSpan = trace.RootSpan
	}
	return sub
}

// clipSpanEnd copies s with EndTime set to end; the trace's own span is left untouched.
// Field-by-field because Span holds a mutex.
func clipSpanEnd(s *schemas.Span, end time.Time) *schemas.Span {
	return &schemas.Span{
		SpanID:     s.SpanID,
		ParentID:   s.ParentID,
		TraceID:    s.TraceID,
		Name:       s.Name,
		Kind:       s.Kind,
		StartTime:  s.StartTime,
		EndTime:    end,
		Status:     s.Status,
		StatusMsg:  s.StatusMsg,
		Attributes: s.Attributes,
		LLM:        s.LLM,
		A2A:        s.A2A,
		Enrichment: s.Enrichment,
		Events:     s.Events,
	}
}

// withoutEarlierStreamWork returns root with its request-wide stream totals reduced by what
// earlier attempts recorded, so a fallback's row carries only its own stream work. Earlier
// rows stay unary: they produced no output, and their decode time stays in their residual.
func withoutEarlierStreamWork(root *schemas.Span, before schemas.StreamTotals) *schemas.Span {
	if before == (schemas.StreamTotals{}) || root.Attributes == nil {
		return root
	}
	attrs := make(map[string]any, len(root.Attributes))
	for k, v := range root.Attributes {
		attrs[k] = v
	}
	for key, d := range map[string]time.Duration{
		schemas.AttrBifrostStreamParseMs:        before.Parse,
		schemas.AttrBifrostStreamConvertMs:      before.Convert,
		schemas.AttrBifrostStreamBackpressureMs: before.Backpressure,
	} {
		if ms, ok := overhead.TraceAttrFloatMs(attrs, key); ok && d > 0 {
			attrs[key] = max(ms-durationMs(d), 0)
		}
	}
	clipped := clipSpanEnd(root, root.EndTime)
	clipped.Attributes = attrs
	return clipped
}

func durationMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
