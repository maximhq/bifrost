package traceingest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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

	// A key bound to virtual keys only matches that key's gateway traffic.
	bound := TextFingerprint("scoped")
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("someone-else", 9150, func(l *logstore.Log) { l.InputFingerprint, l.VirtualKeyID = &bound, new("vk-other") })))
	env.config.addKey("bf-tr-bound", &tables.TableTracingKey{Name: "bound", ProjectIDs: []string{"p1"}, VirtualKeyIDs: []string{"vk-mine"}, LogContent: true, Enabled: true})
	principal, err := env.service.Authenticate(ctx, "bf-tr-bound")
	require.NoError(t, err)
	require.NoError(t, env.service.Ingest(ctx, principal, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t3", SpanID: "llm", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(9100), EndTime: at(9800), Input: "scoped"}}))
	require.NoError(t, env.service.FlushRollups(ctx))
	assert.Empty(t, env.span(t, "t3", "llm").LinkMethod)
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
	env := newTestEnv(t, Config{Pricer: fakePricer{}})
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

	// A model named with the same provider prefix the span names is priced without the prefix.
	env.ingestAndMatch(t, &SpanPart{TraceID: "t2", SpanID: "prefixed", Kind: logstore.TraceSpanKindLLM, Model: "openai/gpt-4o", Provider: "openai.chat", StartTime: at(0), EndTime: at(10), PromptTokens: 600, CompletionTokens: 400})
	prefixed := env.span(t, "t2", "prefixed")
	require.NotNil(t, prefixed.Cost, "openai/gpt-4o from openai.chat is priced as openai gpt-4o")
	assert.InDelta(t, 0.01, *prefixed.Cost, 1e-12)
	assert.Equal(t, "openai.chat", prefixed.Provider, "the provider the SDK sent is kept")
}

// An estimate prices the usage it was computed from. A later part that changes the usage drops the
// stale estimate, and the next pass prices the span again from the new numbers.
func TestMatcherRepricesAnEstimateWhenTheUsageChanges(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{Pricer: fakePricer{}})
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(0), EndTime: at(10), PromptTokens: 10})
	first := env.span(t, "t1", "llm")
	require.NotNil(t, first.Cost)
	require.InDelta(t, 0.0001, *first.Cost, 1e-12)

	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t1", SpanID: "llm", IsMerge: true, PromptTokens: 100}}))
	require.NoError(t, env.service.FlushRollups(ctx))
	span := env.span(t, "t1", "llm")
	require.Equal(t, 100, span.TotalTokens)
	require.NotNil(t, span.Cost)
	assert.InDelta(t, 0.001, *span.Cost, 1e-12, "the estimate follows the new token count")
	assert.True(t, span.CostEstimated)
	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.InDelta(t, 0.001, *trace.Cost, 1e-12)
}

// kindPricer prices gpt-4o as a chat call at 1.0 and as an embedding at 0.1, on openai.
type kindPricer struct{}

func (kindPricer) CalculateCostForUsage(_ *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, _ *modelcatalog.PricingLookupScopes) float64 {
	if provider != schemas.OpenAI || model != "gpt-4o" {
		return 0
	}
	if requestType == schemas.EmbeddingRequest {
		return 0.1
	}
	return 1.0
}

func (kindPricer) GetProvidersForModel(string) []schemas.ModelProvider {
	return []schemas.ModelProvider{schemas.OpenAI}
}

// An estimate is priced for the span's kind. A part that turns an llm span into an embedding with
// the same model and tokens drops the chat estimate, and the next pass prices it as an embedding.
func TestMatcherRepricesAnEstimateWhenTheKindChanges(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{Pricer: kindPricer{}})
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(0), EndTime: at(10), PromptTokens: 10})
	first := env.span(t, "t1", "llm")
	require.NotNil(t, first.Cost)
	require.InDelta(t, 1.0, *first.Cost, 1e-12)

	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t1", SpanID: "llm", IsMerge: true, Kind: logstore.TraceSpanKindEmbedding}}))
	require.NoError(t, env.service.FlushRollups(ctx))
	span := env.span(t, "t1", "llm")
	require.Equal(t, logstore.TraceSpanKindEmbedding, span.Kind)
	require.NotNil(t, span.Cost)
	assert.InDelta(t, 0.1, *span.Cost, 1e-12, "the estimate follows the new kind")
}

// The fingerprint lookup returns at most a fixed number of rows. A full page cannot show that a
// fit is unique, since another fit may sit past it, so it links nothing.
func TestMatcherTreatsAFullFingerprintPageAsAmbiguous(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	prompt := "What is the capital of France?"
	fingerprint := TextFingerprint(prompt)
	for i := range logstore.MaxFingerprintCandidates - 1 {
		require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog(fmt.Sprintf("other-%02d", i), 110+i, func(l *logstore.Log) { l.InputFingerprint, l.Model = &fingerprint, "gpt-4o-mini" })))
	}
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("fit-a", 300, func(l *logstore.Log) { l.InputFingerprint = &fingerprint })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("fit-b", 400, func(l *logstore.Log) { l.InputFingerprint = &fingerprint })))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800), Input: prompt})
	assert.Nil(t, env.span(t, "t1", "llm").LinkSnapshotParsed, "two rows fit, though only one is on the first page")
}

// A fallback attempt belongs to the call that started its chain. A span whose response id is the
// fallback's must not take it again when another span already holds the chain.
func TestMatcherCountsAFallbackRowOnce(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("primary", 150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("outer")
		l.Status, l.Cost, l.TokenUsageParsed = "error", nil, nil
	})))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("fallback", 400, func(l *logstore.Log) {
		l.ParentRequestID, l.ProviderResponseID = new("primary"), new("chatcmpl-fb")
	})))
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t1", SpanID: "outer", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(900)},
		&SpanPart{TraceID: "t1", SpanID: "inner", ParentSpanIDs: []string{"outer"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(120), EndTime: at(880), ProviderResponseID: "chatcmpl-fb", PromptTokens: 100, CompletionTokens: 20, Cost: new(0.009)},
	)
	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	holders := 0
	for _, link := range links {
		if link.LogID == "fallback" {
			holders++
		}
	}
	assert.Equal(t, 1, holders, "the fallback row is linked to one span")

	// The inner span reports the fallback's response id, which outer's snapshot already counts.
	require.Equal(t, 120, env.span(t, "t1", "inner").TotalTokens)
	trace, err := env.store.RecomputeTraceRollup(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 120, trace.TotalTokens, "the fallback call is counted once")
	require.NotNil(t, trace.Cost)
	assert.InDelta(t, 0.0042, *trace.Cost, 1e-12)
}

// When retention removes part of a linked chain, the span keeps the snapshot it took of the whole
// chain, marked expired, instead of shrinking to the rows that are left.
func TestMatcherKeepsTheSnapshotWhenPartOfAChainExpires(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("primary", 150, func(l *logstore.Log) { l.ProviderResponseID = new("chatcmpl-1") })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("fallback", 400, func(l *logstore.Log) { l.ParentRequestID = new("primary") })))
	part := &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(900), ProviderResponseID: "chatcmpl-1"}
	env.ingestAndMatch(t, part)
	require.Equal(t, 240, env.span(t, "t1", "llm").LinkSnapshotParsed.TotalTokens)

	require.NoError(t, env.store.DeleteLog(ctx, "primary"))
	again := *part
	again.Name = "chat again" // a new part, so the trace is folded and matched again
	env.ingestAndMatch(t, &again)
	span := env.span(t, "t1", "llm")
	assert.Equal(t, 240, span.LinkSnapshotParsed.TotalTokens, "the snapshot of the whole chain stays")
	assert.True(t, span.LinkedLogExpired)
}

// A chain whose stored first attempt expired keeps its snapshot even when a later attempt that
// names the expired row appears, so the lookup returns as many rows as were linked.
func TestMatcherKeepsTheSnapshotWhenAnExpiredRowIsReplacedInTheChain(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("primary", 150, func(l *logstore.Log) { l.ProviderResponseID = new("chatcmpl-1") })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("fallback", 400, func(l *logstore.Log) { l.ParentRequestID = new("primary") })))
	part := &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(900), ProviderResponseID: "chatcmpl-1"}
	env.ingestAndMatch(t, part)
	before := env.span(t, "t1", "llm").LinkSnapshotParsed
	require.NotNil(t, before)
	require.Equal(t, 240, before.TotalTokens)

	require.NoError(t, env.store.DeleteLog(ctx, "primary"))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("late", 600, func(l *logstore.Log) {
		l.ParentRequestID = new("primary")
		l.TokenUsageParsed = &schemas.BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}
	})))
	again := *part
	again.Name = "chat again"
	env.ingestAndMatch(t, &again)
	span := env.span(t, "t1", "llm")
	assert.Equal(t, before, span.LinkSnapshotParsed, "the snapshot of the whole chain stays")
	assert.True(t, span.LinkedLogExpired)
}

// A gateway call that names a parent with one model span under it still has to fall inside that
// span's time window. Otherwise the first call to arrive takes whichever span came first, and the
// links of two calls in a row come out swapped.
func TestMatcherChecksTheWindowOfASingleChild(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("first", 150, func(l *logstore.Log) { l.TraceID, l.ParentSpanID = new("t1"), new("task") })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("second", 31_150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("task")
		l.TokenUsageParsed = &schemas.BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}
	})))
	// Only the second call's span has arrived.
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t1", SpanID: "task", Name: "answer", Kind: logstore.TraceSpanKindTask, StartTime: at(0), EndTime: at(40_000)},
		&SpanPart{TraceID: "t1", SpanID: "call-2", ParentSpanIDs: []string{"task"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(31_100), EndTime: at(31_800)},
	)
	require.NotNil(t, env.span(t, "t1", "call-2").LinkSnapshotParsed)
	assert.Equal(t, 8, env.span(t, "t1", "call-2").LinkSnapshotParsed.TotalTokens, "the call inside the span's window links to it")
}

// A link that is final on its first pass still gets the last pass the retry schedule promises, so
// a cost the gateway fills in after the row finished (a pricing recalculation) reaches the span.
func TestMatcherRefreshesAFinalLinkOnceMore(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("priced-late", 150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm")
		l.Cost = nil
	})))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(800)})
	require.NotNil(t, env.span(t, "t1", "llm").LinkSnapshotParsed)
	require.Nil(t, env.span(t, "t1", "llm").LinkSnapshotParsed.Cost, "no cost yet")

	require.NoError(t, env.store.Update(ctx, "priced-late", map[string]any{"cost": 0.003}))
	require.NoError(t, env.service.requeueDueRetries(ctx, time.Now().Add(time.Hour)))
	require.NoError(t, env.service.FlushRollups(ctx))
	span := env.span(t, "t1", "llm")
	require.NotNil(t, span.LinkSnapshotParsed.Cost, "the last pass copies the late cost")
	assert.InDelta(t, 0.003, *span.LinkSnapshotParsed.Cost, 1e-12)

	env.service.mu.Lock()
	_, booked := env.service.retries[logstore.TraceKey{ProjectID: "p1", TraceID: "t1"}]
	env.service.mu.Unlock()
	assert.False(t, booked, "after the last pass the trace is forgotten")
}

// A fingerprint link is the weakest evidence. When a gateway call that named the span arrives
// later, it replaces the fingerprint link instead of being turned away.
func TestMatcherReplacesAFingerprintLinkWithANamedCall(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	prompt := "What is the capital of France?"
	fingerprint := TextFingerprint(prompt)
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("by-fingerprint", 150, func(l *logstore.Log) { l.InputFingerprint = &fingerprint })))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800), Input: prompt})
	require.Equal(t, LinkMethodFingerprint, env.span(t, "t1", "llm").LinkMethod)

	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("named", 200, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm")
		l.TokenUsageParsed = &schemas.BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}
	})))
	require.NoError(t, env.store.MarkTracesDirty(ctx, []logstore.TraceKey{{ProjectID: "p1", TraceID: "t1"}}))
	require.NoError(t, env.service.FlushRollups(ctx))

	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, links, 1, "the fingerprint link is dropped")
	assert.Equal(t, "named", links[0].LogID)
	span := env.span(t, "t1", "llm")
	assert.Equal(t, LinkMethodHeader, span.LinkMethod)
	require.NotNil(t, span.LinkSnapshotParsed)
	assert.Equal(t, 8, span.LinkSnapshotParsed.TotalTokens, "the snapshot is the named call's")
}

// A named call that is still running takes over a fingerprint link at once, and the span drops the
// fingerprint row's numbers in the same step: until the named call is final the span counts its own
// SDK usage, never the numbers of a row it no longer links.
func TestMatcherClearsAFingerprintSnapshotWhenARunningNamedCallReplacesIt(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	prompt := "What is the capital of France?"
	fingerprint := TextFingerprint(prompt)
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("by-fingerprint", 150, func(l *logstore.Log) {
		l.InputFingerprint = &fingerprint
		l.TokenUsageParsed = &schemas.BifrostLLMUsage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100}
	})))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800), Input: prompt, Cost: new(0.5)})
	require.Equal(t, LinkMethodFingerprint, env.span(t, "t1", "llm").LinkMethod)
	require.Equal(t, 100, env.span(t, "t1", "llm").LinkSnapshotParsed.TotalTokens)

	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("named", 200, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm")
		l.Status, l.Cost, l.TokenUsageParsed = "processing", nil, nil
	})))
	require.NoError(t, env.store.MarkTracesDirty(ctx, []logstore.TraceKey{{ProjectID: "p1", TraceID: "t1"}}))
	require.NoError(t, env.service.FlushRollups(ctx))

	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "named", links[0].LogID)
	span := env.span(t, "t1", "llm")
	assert.Nil(t, span.LinkSnapshotParsed, "the fingerprint row's numbers are dropped")
	assert.Empty(t, span.LinkMethod)
	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Zero(t, trace.TotalTokens, "the span counts its own SDK usage until the named call is final")
	require.NotNil(t, trace.Cost)
	assert.InDelta(t, 0.5, *trace.Cost, 1e-12, "the SDK's own cost, not the fingerprint row's")
}

// A fingerprint link made while the call that names another span had not arrived yet gives way
// when that span arrives: the named span takes the row, and the span that held it by fingerprint
// is left with no link and no snapshot.
func TestMatcherMovesAFingerprintRowToTheSpanItNames(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	fingerprint := TextFingerprint("ping")
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("call", 150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("b")
		l.InputFingerprint = &fingerprint
	})))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "a", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800), Input: "ping"})
	require.Equal(t, LinkMethodFingerprint, env.span(t, "t1", "a").LinkMethod)

	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "b", Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(110), EndTime: at(790)})
	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "b", links[0].SpanID, "the row moves to the span it names")
	b := env.span(t, "t1", "b")
	require.NotNil(t, b.LinkSnapshotParsed)
	assert.Equal(t, 120, b.LinkSnapshotParsed.TotalTokens)
	assert.Equal(t, LinkMethodHeader, b.LinkMethod)
	a := env.span(t, "t1", "a")
	assert.Nil(t, a.LinkSnapshotParsed, "the span that held the row by fingerprint lets it go")
	assert.Empty(t, a.LinkMethod)
	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 120, trace.TotalTokens, "the call is counted once")
}

// A call that names a model span directly wins over a call that names the span above it, whatever
// order the two arrived in.
func TestMatcherPrefersACallThatNamesTheSpanItself(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("names-task", 150, func(l *logstore.Log) { l.TraceID, l.ParentSpanID = new("t1"), new("task") })))
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("names-llm", 200, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm")
		l.TokenUsageParsed = &schemas.BifrostLLMUsage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}
	})))
	env.ingestAndMatch(t,
		&SpanPart{TraceID: "t1", SpanID: "task", Name: "answer", Kind: logstore.TraceSpanKindTask, StartTime: at(0), EndTime: at(900)},
		&SpanPart{TraceID: "t1", SpanID: "llm", ParentSpanIDs: []string{"task"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: at(100), EndTime: at(800)},
	)
	links, err := env.store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "names-llm", links[0].LogID)
	assert.Equal(t, 8, env.span(t, "t1", "llm").LinkSnapshotParsed.TotalTokens)
}

// A trace waiting for its final refresh can still gain a call that is not linked yet. The refresh
// then must not end the schedule: the new call keeps getting retries until its row is final, since
// a gateway row finishing does not mark the trace dirty by itself.
func TestMatcherKeepsRetryingANewCallAfterAFinalLink(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	key := logstore.TraceKey{ProjectID: "p1", TraceID: "t1"}
	booked := func() bool {
		env.service.mu.Lock()
		defer env.service.mu.Unlock()
		_, ok := env.service.retries[key]
		return ok
	}
	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("first", 150, func(l *logstore.Log) { l.TraceID, l.ParentSpanID = new("t1"), new("llm-1") })))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm-1", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(800)})
	require.NotNil(t, env.span(t, "t1", "llm-1").LinkSnapshotParsed)
	require.True(t, booked(), "the final refresh is booked")

	require.NoError(t, env.store.CreateIfNotExists(ctx, gatewayLog("second", 1150, func(l *logstore.Log) {
		l.TraceID, l.ParentSpanID = new("t1"), new("llm-2")
		l.Status, l.Cost, l.TokenUsageParsed = "processing", nil, nil
	})))
	env.ingestAndMatch(t, &SpanPart{TraceID: "t1", SpanID: "llm-2", Kind: logstore.TraceSpanKindLLM, StartTime: at(1100), EndTime: at(1800)})
	require.NoError(t, env.service.requeueDueRetries(ctx, time.Now().Add(time.Hour)))
	require.NoError(t, env.service.FlushRollups(ctx))
	assert.True(t, booked(), "the call still running keeps the trace booked")

	require.NoError(t, env.store.Update(ctx, "second", map[string]any{"status": "success", "total_tokens": 55, "cost": 0.001}))
	require.NoError(t, env.service.requeueDueRetries(ctx, time.Now().Add(time.Hour)))
	require.NoError(t, env.service.FlushRollups(ctx))
	span := env.span(t, "t1", "llm-2")
	require.NotNil(t, span.LinkSnapshotParsed)
	assert.Equal(t, 55, span.LinkSnapshotParsed.TotalTokens)
}

// memoryRollupStore serves one dirty trace with one unpriced model span from memory. It makes no
// cgo calls, which the race detector treats as a global sync point, so a race between the rollup
// worker and the goroutine that built the service stays visible.
type memoryRollupStore struct {
	logstore.TraceStore
	cleared atomic.Bool
	cost    atomic.Pointer[float64]
}

func (m *memoryRollupStore) ListDirtyTraces(context.Context, int) ([]logstore.DirtyTrace, error) {
	if m.cleared.Load() {
		return nil, nil
	}
	return []logstore.DirtyTrace{{TraceKey: logstore.TraceKey{ProjectID: "p1", TraceID: "t1"}}}, nil
}

func (m *memoryRollupStore) ListTraceSpans(context.Context, string, string) ([]*logstore.TraceSpan, error) {
	return []*logstore.TraceSpan{{ProjectID: "p1", TraceID: "t1", SpanID: "llm", Kind: logstore.TraceSpanKindLLM, Model: "gpt-4o", StartTime: *at(0), EndTime: at(10), TotalTokens: 100}}, nil
}

func (m *memoryRollupStore) ListTraceSpanLinks(context.Context, string, string) ([]*logstore.TraceSpanLink, error) {
	return nil, nil
}

func (m *memoryRollupStore) FindGatewayCallsByTraceID(context.Context, string) ([]*logstore.GatewayCall, error) {
	return nil, nil
}

func (m *memoryRollupStore) FindGatewayCallsByProviderResponseIDs(context.Context, []string) ([]*logstore.GatewayCall, error) {
	return nil, nil
}

func (m *memoryRollupStore) UpdateTraceSpanEnrichment(_ context.Context, _, _, _ string, enrichment logstore.TraceSpanEnrichment) error {
	if enrichment.Cost != nil {
		m.cost.Store(enrichment.Cost)
	}
	return nil
}

func (m *memoryRollupStore) RecomputeTraceRollup(context.Context, string, string) (*logstore.Trace, error) {
	return &logstore.Trace{}, nil
}

func (m *memoryRollupStore) ClearTraceDirty(context.Context, logstore.DirtyTrace) error {
	m.cleared.Store(true)
	return nil
}

func (m *memoryRollupStore) MarkTracesDirty(context.Context, []logstore.TraceKey) error { return nil }

// The pricer is in place before the rollup worker starts, so a trace that is already dirty when the
// service starts is priced on the worker's first pass, and the worker never reads the pricer while
// it is being set.
func TestPricerIsSetBeforeTheRollupWorkerRuns(t *testing.T) {
	store := &memoryRollupStore{}
	service := NewService(store, newFakeConfig(), Config{RollupInterval: time.Millisecond, Pricer: fakePricer{}}, testLogger{})
	t.Cleanup(service.Close)
	require.Eventually(t, func() bool { return store.cost.Load() != nil }, 5*time.Second, time.Millisecond)
}
