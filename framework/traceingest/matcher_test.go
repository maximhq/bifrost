package traceingest

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatewayLog builds a finished gateway LLM row at an offset from the test's base time.
func gatewayLog(id string, ms int, mutate func(*logstore.Log)) *logstore.Log {
	entry := &logstore.Log{
		ID: id, Timestamp: *at(ms), CreatedAt: *at(ms), Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "success",
		TokenUsageParsed: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}, Cost: new(0.0042), Latency: new(640.0),
	}
	if mutate != nil {
		mutate(entry)
	}
	return entry
}

func (e *testEnv) ingestAndMatch(t *testing.T, spans ...*SpanPart) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, e.service.Ingest(ctx, e.key, e.project, logstore.TraceSourceOTel, spans))
	require.NoError(t, e.service.FlushRollups(ctx))
}

func (e *testEnv) span(t *testing.T, traceID, spanID string) *logstore.TraceSpan {
	t.Helper()
	found, err := e.store.FindTraceSpans(context.Background(), "p1", []logstore.TraceSpanKey{{TraceID: traceID, SpanID: spanID}})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return found[0]
}

// TestMatcherLinksASpanToTheGatewayCallThatNamedIt pins the strongest link: the gateway request
// carried a link header naming the trace and the model span. The span then shows the gateway's
// numbers, the SDK's own numbers for the same call are not counted, and no gateway row is written.
func TestMatcherLinksASpanToTheGatewayCallThatNamedIt(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("log-1", 150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm")
	})))
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t1", SpanID: "root", Name: "agent", Kind: logstore.TraceSpanKindTask, StartTime: at(0), EndTime: at(900)},
		&SpanPart{TraceID: "t1", SpanID: "llm", ParentSpanIDs: []string{"root"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800), PromptTokens: 90, CompletionTokens: 15, Cost: new(9.99)},
	)
	span := env.span(t, "t1", "llm")
	assert.Equal(t, LinkMethodHeader, span.LinkMethod)
	require.NotNil(t, span.LinkSnapshotParsed)
	assert.Equal(t, 120, span.LinkSnapshotParsed.TotalTokens)
	assert.InDelta(t, 0.0042, *span.LinkSnapshotParsed.Cost, 1e-12)
	assert.Equal(t, 105, span.TotalTokens, "the SDK's own numbers stay on the span, uncounted")

	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 120, trace.TotalTokens, "the call is counted once, with the gateway's numbers")
	assert.InDelta(t, 0.0042, *trace.Cost, 1e-12)
	assert.Equal(t, 1, trace.LinkedSpanCount)

	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "log-1", links[0].LogID)

	// A second pass changes nothing: the link is stable.
	require.NoError(t, env.store.MarkTracesDirty(ctx, []logstore.TraceKey{{ProjectID: "p1", TraceID: "t1"}}))
	require.NoError(t, env.service.FlushRollups(ctx))
	links, err = env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, links, 1)
}

// TestMatcherWalksFromAnHTTPSpanOrAnEnclosingSpan pins the two shapes a named parent takes. With
// traceparent the gateway's parent is the SDK's HTTP client span, below the model span. With a
// Braintrust span.export() it is the span the model span sits under.
func TestMatcherWalksFromAnHTTPSpanOrAnEnclosingSpan(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("via-http", 150, func(l *logstore.Log) { l.TraceID, l.ParentSpanID = new("t1"), new("http") })))
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(800)},
		&SpanPart{TraceID: "t1", SpanID: "http", ParentSpanIDs: []string{"llm"}, Name: "POST", Kind: logstore.TraceSpanKindHTTP, StartTime: at(110), EndTime: at(790)},
	)
	assert.NotNil(t, env.span(t, "t1", "llm").LinkSnapshotParsed, "the walk climbs from the HTTP span to the model span")
	assert.Nil(t, env.span(t, "t1", "http").LinkSnapshotParsed)

	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("first", 150, func(l *logstore.Log) { l.TraceID, l.ParentSpanID = new("t2"), new("task") })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("second", 1150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t2"), new("task")
		l.TokenUsageParsed = &schemas.BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}
	})))
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t2", SpanID: "task", Name: "answer", Kind: logstore.TraceSpanKindTask, StartTime: at(0), EndTime: at(2000)},
		&SpanPart{TraceID: "t2", SpanID: "call-1", ParentSpanIDs: []string{"task"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800)},
		&SpanPart{TraceID: "t2", SpanID: "call-2", ParentSpanIDs: []string{"task"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(1100), EndTime: at(1800)},
	)
	assert.Equal(t, 120, env.span(t, "t2", "call-1").LinkSnapshotParsed.TotalTokens, "two calls under one parent are told apart by time")
	assert.Equal(t, 8, env.span(t, "t2", "call-2").LinkSnapshotParsed.TotalTokens)
	assert.Nil(t, env.span(t, "t2", "task").LinkSnapshotParsed, "the enclosing span is not a model call")
}

// TestMatcherCountsAFallbackChainInFull pins the chain rule: every gateway row of the call is
// linked, and the snapshot holds their sum with the answering attempt's model.
func TestMatcherCountsAFallbackChainInFull(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("primary", 150, func(l *logstore.Log) {
		l.ProviderResponseID = new("chatcmpl-1")
		l.Status, l.Cost, l.TokenUsageParsed = "error", nil, nil
	})))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("fallback", 400, func(l *logstore.Log) {
		l.ParentRequestID = new("primary")
		l.Provider, l.Model = "anthropic", "claude-sonnet-5-5"
	})))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(900), ProviderResponseID: "chatcmpl-1"})
	span := env.span(t, "t1", "llm")
	assert.Equal(t, LinkMethodResponseID, span.LinkMethod)
	assert.Equal(t, "claude-sonnet-5-5", span.LinkSnapshotParsed.Model)
	assert.Equal(t, 120, span.LinkSnapshotParsed.TotalTokens)
	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, links, 2, "the failed first attempt and the fallback are both linked")
}

// TestMatcherAcceptsAFingerprintOnlyWhenItIsUnique pins the weakest link: same model, same last
// user message, inside the span's window. One candidate links. Two candidates link nothing, since
// a wrong link would move another call's cost onto this trace.
func TestMatcherAcceptsAFingerprintOnlyWhenItIsUnique(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	prompt := "What is the capital of France?"
	fingerprint := TextFingerprint(prompt)
	input := []any{map[string]any{"role": "user", "content": prompt}}

	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("alone", 150, func(l *logstore.Log) { l.InputFingerprint = &fingerprint })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("other-model", 160, func(l *logstore.Log) { l.InputFingerprint, l.Model = &fingerprint, "gpt-4o-mini" })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("too-late", 60_000, func(l *logstore.Log) { l.InputFingerprint = &fingerprint })))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "openai/gpt-4o", StartTime: at(100), EndTime: at(800), Input: input})
	span := env.span(t, "t1", "llm")
	assert.Equal(t, LinkMethodFingerprint, span.LinkMethod)
	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "alone", links[0].LogID)

	twin := TextFingerprint("ping")
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("twin-a", 5150, func(l *logstore.Log) { l.InputFingerprint = &twin })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("twin-b", 5200, func(l *logstore.Log) { l.InputFingerprint = &twin })))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t2", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(5100), EndTime: at(5800), Input: "ping"})
	assert.Empty(t, env.span(t, "t2", "llm").LinkMethod, "two candidates: the span stays unlinked")
}

// TestMatcherWaitsForAGatewayRowThatIsNotFinal pins the snapshot rule: a gateway row still marked
// processing is linked but not copied, and the trace is booked for another look.
func TestMatcherWaitsForAGatewayRowThatIsNotFinal(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("running", 150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm")
		l.Status, l.Cost, l.TokenUsageParsed = "processing", nil, nil
	})))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(800)})
	assert.Nil(t, env.span(t, "t1", "llm").LinkSnapshotParsed, "numbers that are not final are not copied")
	env.service.mu.Lock()
	_, booked := env.service.retries[logstore.TraceKey{ProjectID: "p1", TraceID: "t1"}]
	env.service.mu.Unlock()
	assert.True(t, booked)

	require.NoError(t, env.store.Update(ctx, "running", map[string]any{"status": "success", "total_tokens": 55, "cost": 0.001}))
	require.NoError(t, env.service.requeueDueRetries(ctx, time.Now().Add(time.Hour)))
	require.NoError(t, env.service.FlushRollups(ctx))
	span := env.span(t, "t1", "llm")
	require.NotNil(t, span.LinkSnapshotParsed)
	assert.Equal(t, 55, span.LinkSnapshotParsed.TotalTokens)

	// When retention removes the gateway row, the span keeps the numbers it copied.
	require.NoError(t, env.store.DeleteLog(ctx, "running"))
	require.NoError(t, env.store.MarkTracesDirty(ctx, []logstore.TraceKey{{ProjectID: "p1", TraceID: "t1"}}))
	require.NoError(t, env.service.FlushRollups(ctx))
	span = env.span(t, "t1", "llm")
	assert.True(t, span.LinkedLogExpired)
	assert.Equal(t, 55, span.LinkSnapshotParsed.TotalTokens)
}

// fakePricer prices gpt-4o on openai only.
type fakePricer struct{}

func (fakePricer) CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, _ schemas.RequestType, _ *modelcatalog.PricingLookupScopes) float64 {
	if provider == schemas.OpenAI && model == "gpt-4o" {
		return float64(usage.TotalTokens) * 0.00001
	}
	return 0
}

func (fakePricer) GetProvidersForModel(model string) []schemas.ModelProvider {
	if model == "gpt-4o" {
		return []schemas.ModelProvider{schemas.OpenAI}
	}
	return nil
}

// TestCostOfAnUnlinkedSpan pins the cost order for a call the gateway did not serve: a cost the SDK
// sent is kept, a span with tokens and a known model is priced from the catalog and marked
// estimated, and a model nothing prices stays unpriced.
func TestCostOfAnUnlinkedSpan(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	env.service.SetPricer(fakePricer{})
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t1", SpanID: "sdk-cost", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(0), EndTime: at(10), PromptTokens: 10, CompletionTokens: 10, Cost: new(0.5)},
		&SpanPart{TraceID: "t1", SpanID: "estimated", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", Provider: "openai.chat", StartTime: at(0), EndTime: at(10), PromptTokens: 600, CompletionTokens: 400},
		&SpanPart{TraceID: "t1", SpanID: "by-catalog", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(0), EndTime: at(10), PromptTokens: 100},
		&SpanPart{TraceID: "t1", SpanID: "unpriced", Kind: logstore.TraceSpanKindLLM, Model: "house-model", StartTime: at(0), EndTime: at(10), PromptTokens: 100},
	)
	sdk := env.span(t, "t1", "sdk-cost")
	assert.InDelta(t, 0.5, *sdk.Cost, 1e-12)
	assert.False(t, sdk.CostEstimated)
	estimated := env.span(t, "t1", "estimated")
	require.NotNil(t, estimated.Cost)
	assert.InDelta(t, 0.01, *estimated.Cost, 1e-12)
	assert.True(t, estimated.CostEstimated)
	byCatalog := env.span(t, "t1", "by-catalog")
	require.NotNil(t, byCatalog.Cost, "the provider is resolved from the catalog by model name")
	assert.Equal(t, "openai", byCatalog.Provider)
	assert.Nil(t, env.span(t, "t1", "unpriced").Cost)

	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.InDelta(t, 0.511, *trace.Cost, 1e-9)
	assert.InDelta(t, 0.011, *trace.EstimatedCost, 1e-9)
	assert.Equal(t, 1, trace.UnpricedSpanCount)

	// A refold keeps the estimate as an estimate.
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t1", SpanID: "estimated", IsMerge: true, Metadata: map[string]any{"k": "v"}}}))
	again := env.span(t, "t1", "estimated")
	require.NotNil(t, again.Cost)
	assert.True(t, again.CostEstimated)
}
