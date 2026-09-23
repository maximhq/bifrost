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
		},
		Events: []SpanEvent{{
			Name:       "gen_ai.content.completion",
			Timestamp:  base.Add(200 * time.Millisecond),
			Attributes: exportFixtureAttrs(),
		}},
	}

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
