package tracing

// Deterministic before/after benchmark for fix C1: batching the per-request
// LLM-call span attribute writes. The old path issued ~30 individual
// tracer.SetAttribute calls, each re-resolving the span (GetTrace + linear
// GetSpan scan over all spans in the trace) and taking the span lock. The new
// path (SetAttributes) resolves the span once and writes under a single lock.
//
// Run:
//
//	go test ./framework/tracing/ -run '^$' -bench 'FixC1' -benchmem

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// benchC1Setup builds a trace with a realistic number of sibling spans (so the
// per-call GetSpan linear scan is exercised) and returns the tracer plus a
// handle to the last span (worst-case scan position).
func benchC1Setup(nSpans int) (*Tracer, schemas.SpanHandle) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx, target := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	for i := 0; i < nSpans; i++ {
		_, h := tracer.StartSpan(ctx, "span"+strconv.Itoa(i), schemas.SpanKindPlugin)
		target = h
	}
	return tracer, target
}

// 30 attribute key/value pairs, matching the count set on the LLM-call span.
var (
	benchC1Keys = func() []string {
		k := make([]string, 30)
		for i := range k {
			k[i] = "attr" + strconv.Itoa(i)
		}
		return k
	}()
	benchC1Values = func() []any {
		v := make([]any, 30)
		for i := range v {
			v[i] = "value" + strconv.Itoa(i)
		}
		return v
	}()
)

// Old path: one SetAttribute per attribute (re-resolves span + locks each time).
func BenchmarkFixC1_PerAttribute(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	b.ReportAllocs()
	for b.Loop() {
		for i, k := range benchC1Keys {
			tracer.SetAttribute(h, k, benchC1Values[i])
		}
	}
}

// New path: resolve the span once (SpanFromHandle), then write directly. One
// lookup instead of ~30, no extra allocation.
func BenchmarkFixC1_ResolveOnce(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	b.ReportAllocs()
	for b.Loop() {
		span := tracer.SpanFromHandle(h)
		for i, k := range benchC1Keys {
			span.SetAttribute(k, benchC1Values[i])
		}
	}
}

// Fix C7: StartSpanID avoids the per-span context.WithValue (valueCtx) node that
// StartSpan allocates. The plugin-hook pipeline only needs the span ID (it
// mirrors it into the BifrostContext), so the valueCtx was pure waste. Both
// benchmarks create a span per iteration; the delta is the valueCtx allocation.
func BenchmarkFixC7_StartSpan(b *testing.B) {
	store := NewTraceStore(time.Hour, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = tracer.StartSpan(ctx, "s", schemas.SpanKindPlugin)
	}
}

func BenchmarkFixC7_StartSpanID(b *testing.B) {
	store := NewTraceStore(time.Hour, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = tracer.StartSpanID(ctx, "s", schemas.SpanKindPlugin)
	}
}

// Fix C2: the Populate*Attributes output map already exists (it's reused for
// root-span propagation), so the win is writing it into the span under ONE lock
// instead of a range + SetAttribute loop (one lock per entry). Span resolved once.
var benchC2Attrs = map[string]any{
	"gen_ai.provider.name": "openai", "bifrost.provider.name": "openai",
	"gen_ai.request.model": "gpt-4o", "gen_ai.operation.name": "chat",
	"gen_ai.request.temperature": 0.7, "gen_ai.request.max_tokens": 512,
	"gen_ai.request.top_p": 0.9, "gen_ai.input.messages": "hello",
	"gen_ai.request.stream": false, "bifrost.request.tools_count": 1,
}

func BenchmarkFixC2_PerEntryLoop(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	span := tracer.SpanFromHandle(h)
	b.ReportAllocs()
	for b.Loop() {
		for k, v := range benchC2Attrs {
			span.SetAttribute(k, v)
		}
	}
}

func BenchmarkFixC2_BulkSetAttributes(b *testing.B) {
	tracer, h := benchC1Setup(15)
	defer tracer.Stop()
	span := tracer.SpanFromHandle(h)
	b.ReportAllocs()
	for b.Loop() {
		span.SetAttributes(benchC2Attrs)
	}
}

// Per-request cost of the trace lifecycle, measured with and without a connector
// attached. The gateway benchmark ran with logs_store and telemetry disabled,
// i.e. zero observability plugins, and still paid for the export snapshot on
// every request.

type benchObsPlugin struct{ name string }

func (p *benchObsPlugin) GetName() string { return p.name }
func (p *benchObsPlugin) Inject(_ context.Context, _ *schemas.Trace) error {
	return nil
}
func (p *benchObsPlugin) Cleanup() error { return nil }

// benchLifecycle drives one request's worth of spans through the tracer.
func benchLifecycle(b *testing.B, tracer *Tracer) {
	req, resp := benchChatRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		traceID := tracer.CreateTrace("")
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
		ctx, root := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
		// A request creates roughly a dozen plugin/internal spans before the
		// LLM call; approximate that so the snapshot walks a realistic tree.
		for j := 0; j < 12; j++ {
			_, h := tracer.StartSpan(ctx, "plugin.hook", schemas.SpanKindPlugin)
			tracer.EndSpan(h, schemas.SpanStatusOk, "")
		}
		_, llm := tracer.StartSpan(ctx, "chat gpt-4o", schemas.SpanKindLLMCall)
		tracer.PopulateLLMRequestAttributes(llm, req)
		tracer.PopulateLLMResponseAttributes(schemas.NewBifrostContext(ctx, time.Now()), llm, resp, nil)
		tracer.EndSpan(llm, schemas.SpanStatusOk, "")
		tracer.EndSpan(root, schemas.SpanStatusOk, "")
		tracer.CompleteAndFlushTrace(traceID)
		// Join the export goroutine: without this the connector case measures
		// only the synchronous half and is not comparable with the no-connector
		// baseline, which has no goroutine to wait for.
		tracer.waitForFlushes(flushStopTimeout)
	}
	b.StopTimer()
}

func BenchmarkTraceLifecycle_NoConnector(b *testing.B) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	tracer.SetObservabilityPlugins(nil, nil)
	benchLifecycle(b, tracer)
}

func BenchmarkTraceLifecycle_OneConnector(b *testing.B) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	tracer.SetObservabilityPlugins(
		[]schemas.ObservabilityPlugin{&benchObsPlugin{name: "bench"}}, nil)
	benchLifecycle(b, tracer)
}

// benchChatRequest builds a request/response pair of realistic size: a short
// system prompt plus a multi-turn conversation, which is what makes the content
// marshal worth gating.
func benchChatRequest() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	msgs := make([]schemas.ChatMessage, 0, 8)
	for i := 0; i < 8; i++ {
		text := "turn " + strconv.Itoa(i) + ": " + strings.Repeat("some conversational content ", 8)
		role := schemas.ChatMessageRoleUser
		if i%2 == 1 {
			role = schemas.ChatMessageRoleAssistant
		}
		msgs = append(msgs, schemas.ChatMessage{
			Role:    role,
			Content: &schemas.ChatMessageContent{ContentStr: &text},
		})
	}
	maxTok, temp := 512, 0.7
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI, Model: "gpt-4o", Input: msgs,
			Params: &schemas.ChatParameters{MaxCompletionTokens: &maxTok, Temperature: &temp},
		},
	}
	out := strings.Repeat("the generated answer ", 20)
	reason := "stop"
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "chatcmpl-bench", Model: "gpt-4o", Object: "chat.completion", Created: 1700000000,
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &reason,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: &out},
					},
				},
			}},
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 250, CompletionTokens: 80, TotalTokens: 330},
		},
	}
	return req, resp
}

// benchMetricsOnlyPlugin declines plugin spans, standing in for a connector that
// reads only LLM spans (Splunk derives metrics that way).
type benchMetricsOnlyPlugin struct{ name string }

func (p *benchMetricsOnlyPlugin) GetName() string                                  { return p.name }
func (p *benchMetricsOnlyPlugin) Inject(_ context.Context, _ *schemas.Trace) error { return nil }
func (p *benchMetricsOnlyPlugin) Cleanup() error                                   { return nil }
func (p *benchMetricsOnlyPlugin) ConsumesPluginSpans() bool                        { return false }

func BenchmarkTraceLifecycle_ConnectorWithoutPluginSpans(b *testing.B) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()
	tracer.SetObservabilityPlugins(
		[]schemas.ObservabilityPlugin{&benchMetricsOnlyPlugin{name: "bench-metrics"}}, nil)
	benchLifecycle(b, tracer)
}
