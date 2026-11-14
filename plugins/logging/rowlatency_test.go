package logging

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

func bucketUs(buckets []logstore.OverheadBucket, name string) float64 {
	for _, b := range buckets {
		if b.Name == name {
			return b.DurationUs
		}
	}
	return 0
}

func pluginSpan(id, name string, start time.Time, d time.Duration) *schemas.Span {
	return &schemas.Span{SpanID: id, Name: name, Kind: schemas.SpanKindPlugin, StartTime: start, EndTime: start.Add(d)}
}

// Each sequential row's breakdown holds only the spans inside its own window; a span in
// the gap between rows (here a plugin hook during tool execution) belongs to neither.
func TestSplitTraceLatency_BreakdownFollowsRowWindows(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	root := &schemas.Span{SpanID: "root", StartTime: at(0), EndTime: at(300)}
	trace := &schemas.Trace{
		RootSpan: root,
		Spans: []*schemas.Span{
			root,
			pluginSpan("routing", "plugin.routing.pre", at(2), 4*time.Millisecond),
			pluginSpan("gap", "plugin.toolhook.pre", at(130), 7*time.Millisecond),
			pluginSpan("governance", "plugin.governance.pre", at(160), 3*time.Millisecond),
		},
	}
	windows := []schemas.AttemptWindow{
		{Index: 1, Start: at(10), End: at(120), UpstreamStart: 0, UpstreamEnd: 100 * time.Millisecond},
		{Index: 2, Start: at(150), End: at(290), UpstreamStart: 110 * time.Millisecond, UpstreamEnd: 230 * time.Millisecond},
	}
	rows := splitTraceLatency(trace, windows, 240, true)

	if rows[0].latencyMs != 120 || rows[0].upstreamMs != 100 {
		t.Errorf("row 1 latency/upstream = %v/%v, want 120/100 (from the request start)", rows[0].latencyMs, rows[0].upstreamMs)
	}
	if rows[1].latencyMs != 150 || rows[1].upstreamMs != 130 {
		t.Errorf("row 2 latency/upstream = %v/%v, want 150/130 (to the request end)", rows[1].latencyMs, rows[1].upstreamMs)
	}
	if got := bucketUs(rows[0].breakdown, "plugin.routing"); got != 4000 {
		t.Errorf("row 1 plugin.routing = %vus, want 4000", got)
	}
	if got := bucketUs(rows[1].breakdown, "plugin.governance"); got != 3000 {
		t.Errorf("row 2 plugin.governance = %vus, want 3000", got)
	}
	for i, row := range rows {
		if got := bucketUs(row.breakdown, "plugin.toolhook"); got != 0 {
			t.Errorf("row %d holds %vus of a span from the gap between rows", i+1, got)
		}
	}
	if got := bucketUs(rows[1].breakdown, "plugin.routing"); got != 0 {
		t.Errorf("row 2 holds row 1's routing span (%vus)", got)
	}
}

// A non-final row's window ends inside logging's PostLLMHook, while the enclosing
// post-hook span runs on. Its breakdown must count that span only up to the window end.
func TestSplitTraceLatency_BreakdownClippedToRowWindow(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	root := &schemas.Span{SpanID: "root", StartTime: at(0), EndTime: at(300)}
	post := pluginSpan("post", "plugin.slowguard.post", at(115), 50*time.Millisecond)
	trace := &schemas.Trace{RootSpan: root, Spans: []*schemas.Span{root, post}}
	windows := []schemas.AttemptWindow{
		{Index: 1, Start: at(10), End: at(120), UpstreamStart: 0, UpstreamEnd: 100 * time.Millisecond},
		{Index: 2, Start: at(170), End: at(290), UpstreamStart: 110 * time.Millisecond, UpstreamEnd: 230 * time.Millisecond},
	}
	rows := splitTraceLatency(trace, windows, 240, true)

	if got := bucketUs(rows[0].breakdown, "plugin.slowguard"); got != 5000 {
		t.Errorf("row 1 plugin.slowguard = %vus, want 5000 (clipped at the window end)", got)
	}
	var sumUs float64
	for _, b := range rows[0].breakdown {
		sumUs += b.DurationUs
	}
	if sumUs > rows[0].overheadMs*1000 {
		t.Errorf("row 1 breakdown sums to %vus, more than its %vms overhead", sumUs, rows[0].overheadMs)
	}
	if !post.EndTime.Equal(at(165)) {
		t.Errorf("clipping mutated the shared span: EndTime = %v", post.EndTime)
	}
}

// A stream attempt can decode events and fail before any output, then fall back. The root
// span's stream totals cover both attempts; the fallback's row keeps only its own share.
func TestSplitTraceLatency_LastStreamRowExcludesEarlierAttempts(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	root := &schemas.Span{SpanID: "root", StartTime: at(0), EndTime: at(300), Attributes: map[string]any{
		schemas.AttrBifrostStreamParseMs:   3.0,
		schemas.AttrBifrostStreamConvertMs: 1.5,
	}}
	hook := pluginSpan("hook", "plugin.governance.pre", at(155), 2*time.Millisecond)
	trace := &schemas.Trace{RootSpan: root, Spans: []*schemas.Span{root, hook}}
	windows := []schemas.AttemptWindow{
		{Index: 1, Start: at(10), End: at(120), UpstreamEnd: 100 * time.Millisecond},
		{Index: 2, Start: at(150), End: at(290), UpstreamStart: 100 * time.Millisecond, UpstreamEnd: 230 * time.Millisecond,
			StreamBefore: schemas.StreamTotals{Parse: 2 * time.Millisecond, Convert: 1500 * time.Microsecond}},
	}
	rows := splitTraceLatency(trace, windows, 240, true)

	if got := bucketUs(rows[1].breakdown, "response-parse"); got != 1000 {
		t.Errorf("last row response-parse = %vus, want 1000 (3ms total minus the 2ms the earlier attempt decoded)", got)
	}
	if got := bucketUs(rows[1].breakdown, "convertor.stream-in"); got != 0 {
		t.Errorf("last row convertor.stream-in = %vus, want 0 (all of it was the earlier attempt's)", got)
	}
	if got := root.Attributes[schemas.AttrBifrostStreamParseMs]; got != 3.0 {
		t.Errorf("the trace's root span was mutated: parse = %v", got)
	}
}

// Overlapping rows (a concurrent list_models fan-out) keep their own windows, with no
// edge extension and no breakdown, since their spans interleave.
func TestSplitTraceLatency_OverlappingRowsKeepOwnWindows(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	trace := &schemas.Trace{RootSpan: &schemas.Span{StartTime: at(0), EndTime: at(500)}}
	windows := []schemas.AttemptWindow{
		{Index: 1, Start: at(5), End: at(205), UpstreamEnd: 180 * time.Millisecond},
		{Index: 1, Start: at(6), End: at(106), UpstreamEnd: 90 * time.Millisecond},
	}
	rows := splitTraceLatency(trace, windows, 400, true)
	want := []struct{ latency, upstream float64 }{{200, 180}, {100, 90}}
	for i, row := range rows {
		if row.latencyMs != want[i].latency || row.upstreamMs != want[i].upstream || row.overheadMs != want[i].latency-want[i].upstream {
			t.Errorf("row %d = latency %v upstream %v overhead %v, want %v/%v", i+1, row.latencyMs, row.upstreamMs, row.overheadMs, want[i].latency, want[i].upstream)
		}
		if row.breakdown != nil {
			t.Errorf("row %d has a breakdown; overlapping rows cannot be split", i+1)
		}
	}
}

// Without a trace (Go SDK), rows skip Inject: a later attempt's row must still carry its
// own numbers instead of the request-wide totals in its ExtraFields, and the first
// attempt's row keeps what it already had.
func TestStoreOrEnqueueEntry_LaterAttemptWithoutTraceUsesOwnWindow(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.ResetUpstreamLatency()

	ctx.MarkAttemptStart()
	schemas.AddUpstreamLatency(ctx, 40*time.Millisecond)
	firstUpstream := 40.0
	first := &logstore.Log{ID: "first", UpstreamLatency: &firstUpstream}
	plugin.storeOrEnqueueEntry(ctx, first, nil)

	ctx.MarkAttemptStart()
	time.Sleep(30 * time.Millisecond)
	schemas.AddUpstreamLatency(ctx, 25*time.Millisecond)
	cumulative := 65.0
	second := &logstore.Log{ID: "second", UpstreamLatency: &cumulative}
	plugin.storeOrEnqueueEntry(ctx, second, nil)

	if *first.UpstreamLatency != 40 {
		t.Errorf("first attempt upstream = %v, want its ExtraFields value 40 untouched", *first.UpstreamLatency)
	}
	if second.UpstreamLatency == nil || *second.UpstreamLatency != 25 {
		t.Errorf("second attempt upstream = %v, want 25 (its own window)", second.UpstreamLatency)
	}
	if second.Latency == nil || *second.Latency < 30 || second.OverheadLatency == nil {
		t.Errorf("second attempt latency = %v overhead = %v, want its own window (>= 30ms)", second.Latency, second.OverheadLatency)
	}
}

// A traced stream row parks with the stream counters its attempt started from; a unary
// row never reads them.
func TestStoreOrEnqueueEntry_StreamRowCarriesStreamSnapshot(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-stream-snapshot")
	ctx.ResetUpstreamLatency()
	ctx.ResetStreamOverhead()
	schemas.AddStreamParse(ctx, 2*time.Millisecond)
	ctx.MarkAttemptStart()
	ctx.MarkStreamAttemptStart()
	schemas.AddStreamParse(ctx, time.Millisecond)

	plugin.storeOrEnqueueEntry(ctx, &logstore.Log{ID: "stream", Stream: true}, nil)
	plugin.storeOrEnqueueEntry(ctx, &logstore.Log{ID: "unary"}, nil)

	val, ok := plugin.pendingLogsToInject.Load("trace-stream-snapshot")
	if !ok {
		t.Fatal("rows were not parked for Inject")
	}
	windows := val.(*pendingInjectEntries).windows
	if len(windows) != 2 {
		t.Fatalf("parked %d windows, want 2", len(windows))
	}
	if got := windows[0].window.StreamBefore.Parse; got != 2*time.Millisecond {
		t.Errorf("stream row StreamBefore.Parse = %v, want 2ms (the counters when its attempt began)", got)
	}
	if got := windows[1].window.StreamBefore; got != (schemas.StreamTotals{}) {
		t.Errorf("unary row read the stream snapshot: %+v", got)
	}
}
