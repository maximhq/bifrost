// Package overhead splits per-request overhead latency into per-phase buckets from a
// completed trace's spans. Shared by logging (stores the full breakdown per log row) and
// the metrics plugins (export a folded per-component histogram) so all three agree.
package overhead

import (
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// spanWall is a span's wall-clock duration, guarding against unfinished spans
// (zero or non-monotonic EndTime) which would otherwise read as huge negatives.
func spanWall(s *schemas.Span) time.Duration {
	if s.EndTime.IsZero() || !s.EndTime.After(s.StartTime) {
		return 0
	}
	return s.EndTime.Sub(s.StartTime)
}

// spanOverlap is the duration of child that falls inside parent's time window. For a
// genuinely nested child this equals the child's wall duration, so self-time is
// unchanged; for a child re-parented by ID but running outside the parent (a
// sequential sibling in wall-clock terms) it is zero. Guards unfinished spans.
func spanOverlap(parent, child *schemas.Span) time.Duration {
	if parent.EndTime.IsZero() || !parent.EndTime.After(parent.StartTime) {
		return 0
	}
	if child.EndTime.IsZero() || !child.EndTime.After(child.StartTime) {
		return 0
	}
	start := child.StartTime
	if parent.StartTime.After(start) {
		start = parent.StartTime
	}
	end := child.EndTime
	if parent.EndTime.Before(end) {
		end = parent.EndTime
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start)
}

// isOverheadSpanKind reports whether a span's self-time counts as attributable
// Bifrost overhead. Only spans that tightly bracket Bifrost's own code qualify:
// plugin hooks and internal operations (key.selection, etc). Deliberately excluded:
//   - llm.call/retry/fallback and the media provider kinds: the upstream side.
//   - the root http.request span: its self-time is the glue between child spans,
//     which for streaming also contains response-body socket reads that happen
//     outside any child span. That time is real upstream (and is already in the
//     upstream accumulator), so counting it here would double-report it as overhead.
//
// Excluded spans still subtract from their parent's self-time via childDur, so their
// time is removed from any bucket rather than mislabeled. The gap between the summed
// buckets and the stamped overhead number is surfaced as the reconciliation line.
func isOverheadSpanKind(kind schemas.SpanKind) bool {
	switch kind {
	case schemas.SpanKindPlugin, schemas.SpanKindInternal:
		return true
	default:
		return false
	}
}

// overheadBucketName maps an overhead-side span to its breakdown bucket.
func overheadBucketName(s *schemas.Span) string {
	if s.Kind == schemas.SpanKindPlugin {
		// plugin.<name>.<phase> -> plugin.<name>, collapsing the hook phases.
		n := strings.TrimPrefix(s.Name, "plugin.")
		if i := strings.LastIndex(n, "."); i > 0 {
			n = n[:i]
		}
		return "plugin." + n
	}
	return s.Name // key.selection and other internal spans keep their name
}

// Compute decomposes Bifrost overhead across spans by self-time:
// each span's own wall duration minus the wall duration of its direct children.
// Self-times across the tree are non-overlapping and sum to the root duration, so
// summing the overhead-side buckets is an independent measure of overhead that does
// not depend on the upstream socket accumulator. Only overhead-side spans produce a
// bucket; provider/upstream spans still subtract from their parent's self-time.
//
// The remaining overhead (the stamped total, minus the measured plugin/internal
// self-time) is attributed to a residual "scheduling" bucket: the goroutine-scheduling
// latency between phases plus any glue no phase span has captured yet. Now that
// request/response conversion, marshal, and parse each have their own phase span, this
// residual is small. It is derived from overheadMs (which already excludes upstream),
// not from the root span's self-time, so it never picks up streaming socket reads.
// Buckets are returned with microsecond values, measured spans first (chronological)
// then the scheduling residual.
//
// Compute returns the per-phase buckets, the measured Bifrost-CPU total in ms (the sum
// of those buckets), and whether this was a streaming request. For streams the caller
// uses measuredMs as the overhead (see the logging plugin's Inject): total-upstream
// over-counts stream overhead because it includes off-CPU relay/scheduler wait between
// chunks, which is not Bifrost work.
func Compute(trace *schemas.Trace, overheadMs float64, overheadOK bool, upstreamMs float64, upstreamOK bool) ([]logstore.OverheadBucket, float64, bool) {
	if trace == nil || len(trace.Spans) == 0 {
		return nil, 0, false
	}
	// Sum direct-children time per parent, over ALL spans (upstream ones too), so
	// excluded child spans are still removed from their parent's self-time. Only the
	// portion of a child that temporally OVERLAPS its parent counts: a child
	// re-parented for trace-hierarchy reasons but running outside the parent's window
	// (e.g. llm.call is linked under key.selection but starts after it ends) then
	// correctly subtracts nothing, instead of driving the parent's self-time negative.
	spanByID := make(map[string]*schemas.Span, len(trace.Spans))
	for _, s := range trace.Spans {
		if s != nil && s.SpanID != "" {
			spanByID[s.SpanID] = s
		}
	}
	childDur := make(map[string]time.Duration, len(trace.Spans))
	for _, s := range trace.Spans {
		if s == nil || s.ParentID == "" {
			continue
		}
		parent := spanByID[s.ParentID]
		if parent == nil {
			continue
		}
		childDur[s.ParentID] += spanOverlap(parent, s)
	}

	type agg struct {
		dur   time.Duration
		kind  schemas.SpanKind
		first time.Time
	}
	buckets := make(map[string]*agg)
	for _, s := range trace.Spans {
		if s == nil || !isOverheadSpanKind(s.Kind) {
			continue
		}
		self := spanWall(s) - childDur[s.SpanID]
		if self <= 0 {
			continue
		}
		name := overheadBucketName(s)
		b := buckets[name]
		if b == nil {
			b = &agg{kind: s.Kind, first: s.StartTime}
			buckets[name] = b
		}
		b.dur += self
		if s.StartTime.Before(b.first) {
			b.first = s.StartTime
		}
	}

	// Streaming runs no per-chunk spans: the relay loop's JSON decode, struct->unified
	// mapping, and downstream-backpressure stall are stamped as root-span attributes at
	// stream end. Fold them into the same buckets as their unary equivalents (decode ->
	// response-parse/Serialization, mapping -> convertor/Convertor) so a stream's numbers
	// read like a unary request's. Backpressure has no unary twin and is not Bifrost CPU,
	// so it gets its own bucket. Seeding the map here means measuredNs and core pick them
	// up on the existing path, with no separate bookkeeping.
	if trace.RootSpan != nil {
		attrs := trace.RootSpan.Attributes
		addStreamBucketMs := func(name string, ms float64) {
			if ms <= 0 {
				return
			}
			b := buckets[name]
			if b == nil {
				b = &agg{kind: schemas.SpanKindInternal, first: trace.RootSpan.StartTime}
				buckets[name] = b
			}
			b.dur += time.Duration(ms * float64(time.Millisecond))
		}
		if ms, ok := TraceAttrFloatMs(attrs, schemas.AttrBifrostStreamParseMs); ok {
			addStreamBucketMs("response-parse", ms)
		}
		// Inbound per-chunk mapping (provider->Bifrost) is conversion work: it belongs
		// in the Convertor category, as its own member so the stream split is visible.
		if ms, ok := TraceAttrFloatMs(attrs, schemas.AttrBifrostStreamConvertMs); ok {
			addStreamBucketMs("convertor.stream-in", ms)
		}
		// Backpressure is the provider-side downstream wait that IS in the overhead
		// total. Split it into (A) client-write vs (B) transport CPU using the transport
		// goroutine's concurrent measurements as weights (those run in parallel and are
		// not themselves in the total, so they weight rather than add). The (B) share is
		// the outbound per-chunk mapping (Bifrost->client) -- also conversion work, so it
		// joins the Convertor category as the outbound member. The (A) share is the client
		// socket write and stays its own bucket. No transport timing (raw passthrough, or a
		// client that disconnected) falls back to a single undifferentiated bucket.
		if bp, ok := TraceAttrFloatMs(attrs, schemas.AttrBifrostStreamBackpressureMs); ok && bp > 0 {
			cpuMs, _ := TraceAttrFloatMs(attrs, schemas.AttrBifrostStreamTransportCPUMs)
			writeMs, _ := TraceAttrFloatMs(attrs, schemas.AttrBifrostStreamClientWriteMs)
			if total := cpuMs + writeMs; total > 0 {
				addStreamBucketMs("stream-client-write", bp*writeMs/total)
				addStreamBucketMs("convertor.stream-out", bp*cpuMs/total)
			}
		}
		// Worker->caller goroutine-hop latency (unary path): scheduling wall-time
		// inside the overhead window that sits on no span. Carve it into its own
		// "worker-handoff" bucket. The reverse hop is the queue-wait span.
		if ms, ok := TraceAttrFloatMs(attrs, schemas.AttrBifrostWorkerHandoffMs); ok {
			addStreamBucketMs("worker-handoff", ms)
		}
	}

	// Provider-agnostic catch-all. Every provider call runs inside an llm.call span
	// (SpanKindLLMCall), which is not itself a bucket but envelops the upstream network
	// call plus ALL provider-side glue. The hot pieces (request conversion/marshal/signing,
	// response read/decompress/parse) now have their own phase spans; its self-time (wall
	// minus the child phase spans) is therefore upstream plus whatever provider work no
	// phase span captured. Subtracting the measured upstream leaves that uncaptured
	// remainder, surfaced as "provider-internal" so a brand-new provider (or an unspanned
	// step in an existing one) lands here, and its size tells us a provider needs finer
	// spans. Summed across attempts: retries create
	// one llm.call span each, and upstream latency likewise accumulates across them.
	//
	// STREAMING IS EXCLUDED. For a streamed response the llm.call span is DEFERRED — it
	// covers the entire stream (ended on the final chunk), not just setup — while upstream
	// is only time-to-first-byte. So llm.call self - upstream would capture the whole
	// per-chunk relay, which is instead decomposed by the stream phases above
	// (response-parse / convertor / backpressure via the stream accumulator). Computing
	// provider-internal there would double-count that work and mislabel it. Detect
	// streaming by the presence of any stream-overhead attribute on the root span.
	isStreaming := false
	if trace.RootSpan != nil && trace.RootSpan.Attributes != nil {
		a := trace.RootSpan.Attributes
		for _, k := range []string{schemas.AttrBifrostStreamParseMs, schemas.AttrBifrostStreamConvertMs, schemas.AttrBifrostStreamBackpressureMs} {
			if _, ok := a[k]; ok {
				isStreaming = true
				break
			}
		}
	}
	if upstreamOK && !isStreaming {
		var llmSelfNs int64
		var firstLLM time.Time
		for _, s := range trace.Spans {
			if s == nil || s.Kind != schemas.SpanKindLLMCall {
				continue
			}
			if self := spanWall(s) - childDur[s.SpanID]; self > 0 {
				llmSelfNs += self.Nanoseconds()
			}
			if firstLLM.IsZero() || s.StartTime.Before(firstLLM) {
				firstLLM = s.StartTime
			}
		}
		// llmSelfNs and upstream are both provider-side wall time; the difference is the
		// uninstrumented glue. Guard on a small floor so measurement skew (upstream
		// stamped slightly larger than the enveloping span) never emits a noise bucket.
		providerInternalUs := float64(llmSelfNs)/1000.0 - upstreamMs*1000.0
		if providerInternalUs > 0.5 {
			b := buckets["provider-internal"]
			if b == nil {
				b = &agg{kind: schemas.SpanKindInternal, first: firstLLM}
				buckets["provider-internal"] = b
			}
			b.dur += time.Duration(providerInternalUs * float64(time.Microsecond))
		}
	}

	out := make([]logstore.OverheadBucket, 0, len(buckets)+1)
	var measuredNs int64
	for name, b := range buckets {
		measuredNs += b.dur.Nanoseconds()
		out = append(out, logstore.OverheadBucket{
			Name:       name,
			Kind:       string(b.kind),
			DurationUs: float64(b.dur.Nanoseconds()) / 1000.0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return buckets[out[i].Name].first.Before(buckets[out[j].Name].first)
	})

	measuredMs := float64(measuredNs) / float64(time.Millisecond)

	// Unary requests: whatever overhead is left over after every instrumented phase is
	// the residual between phases — goroutine-scheduling latency (the request hops across
	// the HTTP, core-pipeline and provider-worker goroutines) plus any not-yet-spanned
	// transport edge.
	//
	// STREAMING IS EXCLUDED. For a stream, total-upstream is NOT Bifrost overhead: it
	// includes the off-CPU relay/scheduler wait the request goroutine spends parked
	// between provider chunks (confirmed ~2% CPU under load). All actual Bifrost CPU is
	// already measured in the buckets above (parse/convert accumulators, aggregated
	// per-chunk plugin timing, transport marshal/write).
	if overheadOK && !isStreaming {
		schedulingUs := overheadMs*1000.0 - float64(measuredNs)/1000.0
		if schedulingUs > 0.5 {
			out = append(out, logstore.OverheadBucket{Name: "scheduling", Kind: "scheduling", DurationUs: schedulingUs})
		}
	}
	if len(out) == 0 {
		return nil, measuredMs, isStreaming
	}
	return out, measuredMs, isStreaming
}

// TraceAttrFloatMs reads a millisecond span attribute, tolerating int/int64/float64.
func TraceAttrFloatMs(attrs map[string]any, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}

// overhead_component values emitted by ComputeForMetrics: the UI's top-level overhead
// categories, so the metric and the log-detail view share one vocabulary. Keep in sync
// with overheadCategoryKey / OVERHEAD_BUCKET_CATEGORY in
// ui/app/workspace/logs/sheets/logDetailView.tsx (paired Go/TS definitions).
const (
	CategorySerialization = "serialization"
	CategoryConversion    = "conversion"
	CategoryPlugins       = "plugins"
	CategoryMiddleware    = "middleware"
	CategoryRouting       = "routing"
	CategoryProcessing    = "processing"
	CategoryNetworking    = "networking"
	CategoryStreaming     = "streaming"
	// CategoryMiscellaneous: overhead not worth its own span. Folds the core "miscellaneous"
	// glue span and the "scheduling" residual (overhead minus the measured phases).
	CategoryMiscellaneous = "miscellaneous"
	CategoryOther         = "other"
)

// bucketCategory maps internal phase-span names to their category. Anything unmapped and
// not matched by a prefix rule falls to "other" (the signal to add a home here and in the UI).
var bucketCategory = map[string]string{
	"key-pool":                   CategoryRouting,
	"key.selection":              CategoryRouting,
	"handle-setup":               CategoryProcessing,
	"pipeline-pre":               CategoryProcessing,
	"pipeline-post":              CategoryProcessing,
	"worker-setup":               CategoryProcessing,
	"worker-handoff":             CategoryProcessing,
	"queue-wait":                 CategoryProcessing,
	"attribute-population":       CategoryProcessing,
	"miscellaneous":              CategoryMiscellaneous,
	"provider-internal":          CategoryNetworking,
	"transport-context":          CategoryNetworking,
	"transport-response-headers": CategoryNetworking,
	"response-finalize":          CategoryNetworking,
	"request-sign":               CategoryNetworking,
	"credentials-fetch":          CategoryNetworking,
	"stream-backpressure":        CategoryStreaming,
	"stream-client-write":        CategoryStreaming,
	"scheduling":                 CategoryMiscellaneous,
}

// MetricComponent maps one bucket to its category, mirroring the UI's overheadCategoryKey:
// serialization phases, middleware.*, convertor(.*) and plugin.* by rule, else bucketCategory,
// else "other" (or "plugins" for an unmatched plugin-kind span).
func MetricComponent(b logstore.OverheadBucket) string {
	switch b.Name {
	case "request-unmarshal", "request-marshal", "response-parse", "response-marshal":
		return CategorySerialization
	}
	if strings.HasPrefix(b.Name, "middleware.") {
		return CategoryMiddleware
	}
	if b.Name == "convertor" || strings.HasPrefix(b.Name, "convertor.") {
		return CategoryConversion
	}
	if strings.HasPrefix(b.Name, "plugin.") {
		return CategoryPlugins
	}
	if c, ok := bucketCategory[b.Name]; ok {
		return c
	}
	if b.Kind == string(schemas.SpanKindPlugin) {
		return CategoryPlugins
	}
	return CategoryOther
}

// ComputeForMetrics rolls the breakdown up for metric export: overhead_component -> microseconds.
// Reads overhead/upstream durations off the root span, so callers pass only the trace.
// Returns nil when there's nothing to record, so callers can skip observation.
func ComputeForMetrics(trace *schemas.Trace) map[string]float64 {
	if trace == nil {
		return nil
	}
	var upstreamMs, overheadMs float64
	var upOK, ovOK bool
	if trace.RootSpan != nil && trace.RootSpan.Attributes != nil {
		upstreamMs, upOK = TraceAttrFloatMs(trace.RootSpan.Attributes, schemas.AttrBifrostUpstreamDurationMs)
		overheadMs, ovOK = TraceAttrFloatMs(trace.RootSpan.Attributes, schemas.AttrBifrostOverheadDurationMs)
	}
	buckets, _, _ := Compute(trace, overheadMs, ovOK, upstreamMs, upOK)
	if len(buckets) == 0 {
		return nil
	}
	out := make(map[string]float64, len(buckets))
	for _, b := range buckets {
		out[MetricComponent(b)] += b.DurationUs
	}
	return out
}
