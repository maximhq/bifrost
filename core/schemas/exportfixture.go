package schemas

import (
	"fmt"
	"strings"
	"time"
)

// Trace fixture and assertions shared by every observability connector.
//
// Not a _test.go file: connectors live in other modules (and the enterprise
// repo) and cannot import another package's test code. Per-connector fixtures
// are what let three of them drift to a five-key content list.

// ExportFixtureSecret marks every content-bearing field in the fixture. It must
// not appear in a connector's output when content logging is disabled.
const ExportFixtureSecret = "BIFROST-CONTENT-SENTINEL"

// ExportFixtureMetadata marks fields that must survive stripping, separating
// "stripped correctly" from "dropped everything".
const ExportFixtureMetadata = "bifrost-metadata-survives"

// ExportFixtureCost is the cost breakdown the fixture carries. Values are chosen
// so every category is distinct and non-zero, and so the sides sum to the total
// and each side's categories sum to that side — a connector's output can be
// checked against exact numbers rather than just key presence.
func ExportFixtureCost() *BifrostCost {
	return &BifrostCost{
		TotalCost:      1.00,
		InputCost:      0.30,
		OutputCost:     0.50,
		AdditionalCost: 0.20,
		InputCostDetails: &InputCostDetails{
			TextCost: 0.10, AudioCost: 0.05, ImageCost: 0.05,
			CachedReadCost: 0.04, CachedWriteCost: 0.03, RequestCost: 0.03,
		},
		OutputCostDetails: &OutputCostDetails{
			TextCost: 0.20, AudioCost: 0.10, ImageCost: 0.05,
			ReasoningCost: 0.10, CitationCost: 0.03, SearchQueriesCost: 0.02,
		},
		AdditionalCostDetails: &AdditionalCostDetails{
			GuardrailCost: 0.08, MCPCost: 0.06, SemanticCacheCost: 0.04, RoutingCost: 0.02,
		},
	}
}

// ExportFixtureOptions shapes the fixture trace.
type ExportFixtureOptions struct {
	// PluginNames get a prehook/posthook span pair each.
	// Defaults to governance, semanticcache, logging.
	PluginNames []string
	// IncludeOverheadSpans adds internal phase spans, which only an
	// OverheadSpanConsumer should receive.
	IncludeOverheadSpans bool
}

// NewExportFixtureTrace builds a trace covering every connector leak path:
// content on the root span, on a child LLM span, inside a span event, on the
// typed payload, plus filterable plugin spans.
//
// Every key in AllContentAttributeKeys carries ExportFixtureSecret, so tests
// assert one string is absent rather than enumerating keys — which keeps them
// correct as content attributes are added upstream.
func NewExportFixtureTrace(opts ExportFixtureOptions) *Trace {
	base := time.Unix(1700000000, 0).UTC()
	pluginNames := opts.PluginNames
	if len(pluginNames) == 0 {
		pluginNames = []string{"governance", "semanticcache", "logging"}
	}

	root := &Span{
		SpanID:     "span-root",
		TraceID:    "trace-fixture",
		Name:       "POST /v1/chat/completions",
		Kind:       SpanKindHTTPRequest,
		StartTime:  base,
		EndTime:    base.Add(250 * time.Millisecond),
		Status:     SpanStatusOk,
		Attributes: exportFixtureAttrs(),
	}

	llm := &Span{
		SpanID:     "span-llm",
		ParentID:   root.SpanID,
		TraceID:    "trace-fixture",
		Name:       "chat gpt-4o",
		Kind:       SpanKindLLMCall,
		StartTime:  base.Add(10 * time.Millisecond),
		EndTime:    base.Add(240 * time.Millisecond),
		Status:     SpanStatusOk,
		Attributes: exportFixtureAttrs(),
		// Content also rides the typed payload, which is json:"-" so a
		// whole-trace marshal cannot leak it.
		LLM: &LLMSpanData{
			Provider:     ModelProvider("openai"),
			RequestType:  ChatCompletionRequest,
			RequestModel: "gpt-4o",
			InputMessages: []MessageSummary{{
				Role:    "user",
				Content: ExportFixtureSecret,
				ToolCalls: []ToolCallSummary{{
					Name: "lookup", Args: ExportFixtureSecret,
				}},
				Attachments: []AttachmentSummary{{
					Kind: AttachmentImage, MediaType: "image/png", Data: ExportFixtureSecret,
				}},
			}},
			OutputMessages: []MessageSummary{{Role: "assistant", Content: ExportFixtureSecret}},
			ReasoningText:  ExportFixtureSecret,
			Cost:           ExportFixtureCost(),
		},
		Events: []SpanEvent{{
			Name:       "gen_ai.content.completion",
			Timestamp:  base.Add(200 * time.Millisecond),
			Attributes: exportFixtureAttrs(),
		}},
	}

	// Cost lives on the LLM span, where the tracer writes it.
	llm.SetAttributes(CostAttributes(ExportFixtureCost()))

	spans := []*Span{root, llm}
	for i, name := range pluginNames {
		offset := time.Duration(i+1) * time.Millisecond
		for _, stage := range []string{"prehook", "posthook"} {
			spans = append(spans, &Span{
				SpanID:    "span-plugin-" + name + "-" + stage,
				ParentID:  root.SpanID,
				TraceID:   "trace-fixture",
				Name:      "plugin." + SanitizePluginSpanName(name) + "." + stage,
				Kind:      SpanKindPlugin,
				StartTime: base.Add(offset),
				EndTime:   base.Add(offset + time.Millisecond),
				Status:    SpanStatusOk,
				Attributes: map[string]any{
					AttrRequestModel: ExportFixtureMetadata,
				},
			})
		}
	}

	if opts.IncludeOverheadSpans {
		for i, name := range []string{"queue-wait", "request-marshal", "response-parse"} {
			offset := time.Duration(i+20) * time.Millisecond
			spans = append(spans, &Span{
				SpanID:     "span-overhead-" + name,
				ParentID:   root.SpanID,
				TraceID:    "trace-fixture",
				Name:       name,
				Kind:       SpanKindInternal,
				StartTime:  base.Add(offset),
				EndTime:    base.Add(offset + time.Millisecond),
				Status:     SpanStatusOk,
				Attributes: map[string]any{AttrRequestModel: ExportFixtureMetadata},
			})
		}
	}

	return &Trace{
		RequestID:  "req-fixture",
		TraceID:    "trace-fixture",
		InternalID: "internal-fixture",
		RootSpan:   root,
		Spans:      spans,
		StartTime:  base,
		EndTime:    base.Add(250 * time.Millisecond),
		Attributes: map[string]any{TraceAttrSessionID: ExportFixtureMetadata},
	}
}

// exportFixtureAttrs sets every content key to the sentinel, plus metadata that
// must survive stripping.
func exportFixtureAttrs() map[string]any {
	attrs := make(map[string]any, len(AllContentAttributeKeys())+4)
	for _, key := range AllContentAttributeKeys() {
		attrs[key] = ExportFixtureSecret
	}
	attrs[AttrRequestModel] = ExportFixtureMetadata
	attrs[AttrProviderName] = ExportFixtureMetadata
	attrs[AttrInputTokens] = 250
	attrs[AttrOutputTokens] = 80
	return attrs
}

// ExportFixturePluginSpanNames returns the span names the fixture emits for a
// plugin.
func ExportFixturePluginSpanNames(pluginName string) []string {
	sanitized := SanitizePluginSpanName(pluginName)
	return []string{
		"plugin." + sanitized + ".prehook",
		"plugin." + sanitized + ".posthook",
	}
}

// AssertNoContentLeak reports content leaks in a connector's output; empty means
// clean. Takes serialized bytes, not a trace, because that is what reaches the
// backend — the typed-payload leak was only visible after marshalling.
func AssertNoContentLeak(payload []byte) []string {
	var problems []string
	body := string(payload)
	if strings.Contains(body, ExportFixtureSecret) {
		problems = append(problems, fmt.Sprintf(
			"content sentinel %q appears %d time(s) in the exported payload",
			ExportFixtureSecret, strings.Count(body, ExportFixtureSecret)))
	}
	if !strings.Contains(body, ExportFixtureMetadata) {
		problems = append(problems,
			"metadata sentinel is absent: stripping removed more than content")
	}
	return problems
}

// AssertSpanFilterApplied reports filter violations: dropped names that appear,
// kept names that do not. Matches span names as substrings of the payload, which
// is what a leak looks like regardless of wire format.
func AssertSpanFilterApplied(payload []byte, wantDropped, wantKept []string) []string {
	var problems []string
	body := string(payload)
	for _, name := range wantDropped {
		if strings.Contains(body, name) {
			problems = append(problems, fmt.Sprintf("filtered span %q leaked into the payload", name))
		}
	}
	for _, name := range wantKept {
		if !strings.Contains(body, name) {
			problems = append(problems, fmt.Sprintf("span %q should have been exported but is absent", name))
		}
	}
	return problems
}

// CostLookup reads one cost value out of a connector's decoded output. Connectors
// name these fields differently — BigQuery columns, Splunk event fields, Datadog
// LLM-Obs metrics, raw span attributes — so each supplies its own lookup and the
// reconciliation rules below stay in one place.
//
// found is false when the connector does not carry that value at all, which the
// assertion reports rather than treating as zero.
type CostLookup func(category CostCategory) (value float64, found bool)

// CostCategory names one value in the breakdown, independent of how a connector
// spells it.
type CostCategory string

const (
	CostTotal      CostCategory = "total"
	CostInput      CostCategory = "input"
	CostOutput     CostCategory = "output"
	CostAdditional CostCategory = "additional"

	CostInputText        CostCategory = "input.text"
	CostInputAudio       CostCategory = "input.audio"
	CostInputImage       CostCategory = "input.image"
	CostInputCachedRead  CostCategory = "input.cached_read"
	CostInputCachedWrite CostCategory = "input.cached_write"
	CostInputRequest     CostCategory = "input.request"

	CostOutputText      CostCategory = "output.text"
	CostOutputAudio     CostCategory = "output.audio"
	CostOutputImage     CostCategory = "output.image"
	CostOutputReasoning CostCategory = "output.reasoning"
	CostOutputCitation  CostCategory = "output.citation"
	CostOutputSearch    CostCategory = "output.search_queries"

	CostGuardrail     CostCategory = "additional.guardrail"
	CostMCP           CostCategory = "additional.mcp"
	CostSemanticCache CostCategory = "additional.semantic_cache"
	CostRouting       CostCategory = "additional.routing"
)

// costSides maps each side to the categories that must sum to it.
var costSides = map[CostCategory][]CostCategory{
	CostInput: {CostInputText, CostInputAudio, CostInputImage,
		CostInputCachedRead, CostInputCachedWrite, CostInputRequest},
	CostOutput: {CostOutputText, CostOutputAudio, CostOutputImage,
		CostOutputReasoning, CostOutputCitation, CostOutputSearch},
	CostAdditional: {CostGuardrail, CostMCP, CostSemanticCache, CostRouting},
}

// AssertCostBreakdown reports every discrepancy between a connector's exported
// cost values and ExportFixtureCost: missing categories, wrong values, and sums
// that do not reconcile. An empty result means the connector exported the
// breakdown faithfully.
//
// Reconciliation is the point. Key presence alone would pass a connector that
// wired a category to the wrong attribute, which is the mistake this class of
// code keeps making (see BigQuery's pickDetail passing one key twice).
func AssertCostBreakdown(lookup CostLookup) []string {
	const eps = 1e-9
	var problems []string

	near := func(got, want float64) bool {
		diff := got - want
		return diff <= eps && diff >= -eps
	}

	want := map[CostCategory]float64{
		CostTotal: 1.00, CostInput: 0.30, CostOutput: 0.50, CostAdditional: 0.20,
		CostInputText: 0.10, CostInputAudio: 0.05, CostInputImage: 0.05,
		CostInputCachedRead: 0.04, CostInputCachedWrite: 0.03, CostInputRequest: 0.03,
		CostOutputText: 0.20, CostOutputAudio: 0.10, CostOutputImage: 0.05,
		CostOutputReasoning: 0.10, CostOutputCitation: 0.03, CostOutputSearch: 0.02,
		CostGuardrail: 0.08, CostMCP: 0.06, CostSemanticCache: 0.04, CostRouting: 0.02,
	}

	get := func(c CostCategory) (float64, bool) {
		v, ok := lookup(c)
		if !ok {
			problems = append(problems, fmt.Sprintf("cost category %q was not exported", c))
		}
		return v, ok
	}

	for category, expected := range want {
		v, ok := get(category)
		if !ok {
			continue
		}
		if !near(v, expected) {
			problems = append(problems, fmt.Sprintf("cost %q = %v, want %v", category, v, expected))
		}
	}

	// Sides must sum to the total, and each side's categories to that side.
	if total, ok := lookup(CostTotal); ok {
		var sides float64
		for _, side := range []CostCategory{CostInput, CostOutput, CostAdditional} {
			if v, found := lookup(side); found {
				sides += v
			}
		}
		if !near(sides, total) {
			problems = append(problems, fmt.Sprintf(
				"input+output+additional = %v, want total %v", sides, total))
		}
	}
	for side, categories := range costSides {
		sideValue, ok := lookup(side)
		if !ok {
			continue
		}
		var sum float64
		for _, c := range categories {
			if v, found := lookup(c); found {
				sum += v
			}
		}
		if !near(sum, sideValue) {
			problems = append(problems, fmt.Sprintf(
				"%q categories sum to %v, want %v", side, sum, sideValue))
		}
	}
	return problems
}

// CostAttributeLookup is a CostLookup over raw span attributes, for connectors
// that publish them verbatim (OTEL, Kafka, Pub/Sub).
func CostAttributeLookup(attrs map[string]any) CostLookup {
	keys := map[CostCategory]string{
		CostTotal: AttrUsageCost, CostInput: AttrBifrostCostInput,
		CostOutput: AttrBifrostCostOutput, CostAdditional: AttrBifrostCostAdditional,
		CostInputText: AttrBifrostCostInputText, CostInputAudio: AttrBifrostCostInputAudio,
		CostInputImage:       AttrBifrostCostInputImage,
		CostInputCachedRead:  AttrBifrostCostInputCachedRead,
		CostInputCachedWrite: AttrBifrostCostInputCachedWrite,
		CostInputRequest:     AttrBifrostCostInputRequest,
		CostOutputText:       AttrBifrostCostOutputText, CostOutputAudio: AttrBifrostCostOutputAudio,
		CostOutputImage:     AttrBifrostCostOutputImage,
		CostOutputReasoning: AttrBifrostCostOutputReasoning,
		CostOutputCitation:  AttrBifrostCostOutputCitation,
		CostOutputSearch:    AttrBifrostCostOutputSearch,
		CostGuardrail:       AttrBifrostCostGuardrail, CostMCP: AttrBifrostCostMCP,
		CostSemanticCache: AttrBifrostCostSemanticCache, CostRouting: AttrBifrostCostRouting,
	}
	return func(category CostCategory) (float64, bool) {
		key, ok := keys[category]
		if !ok {
			return 0, false
		}
		v, present := attrs[key]
		if !present {
			return 0, false
		}
		f, isFloat := v.(float64)
		return f, isFloat
	}
}
