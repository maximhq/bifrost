package otel

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// makeSpan creates a test span with the given name and kind.
func makeSpan(name string, kind schemas.SpanKind) *schemas.Span {
	now := time.Now()
	return &schemas.Span{
		SpanID:    "abcdef1234567890",
		Name:      name,
		Kind:      kind,
		StartTime: now,
		EndTime:   now.Add(10 * time.Millisecond),
		Status:    schemas.SpanStatusOk,
	}
}

// TestConvertTraceToResourceSpan_LLMSpansOnlyFalse verifies all spans are exported when llmSpansOnly is disabled.
func TestConvertTraceToResourceSpan_LLMSpansOnlyFalse(t *testing.T) {
	p := &OtelPlugin{
		serviceName:  "test",
		llmSpansOnly: false,
	}
	trace := &schemas.Trace{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
		Spans: []*schemas.Span{
			makeSpan("http.request", schemas.SpanKindHTTPRequest),
			makeSpan("llm.call", schemas.SpanKindLLMCall),
			makeSpan("plugin.otel.prehook", schemas.SpanKindPlugin),
			makeSpan("plugin.telemetry.posthook", schemas.SpanKindPlugin),
			makeSpan("key.selection", schemas.SpanKindInternal),
		},
	}

	result := p.convertTraceToResourceSpan(trace)
	if result == nil {
		t.Fatal("expected non-nil ResourceSpan, got nil")
	}
	spans := result.ScopeSpans[0].Spans
	if len(spans) != 5 {
		t.Errorf("expected 5 spans (all exported), got %d", len(spans))
	}
}

// TestConvertTraceToResourceSpan_LLMSpansOnlyTrue verifies only LLM operation spans are exported when llmSpansOnly is enabled.
func TestConvertTraceToResourceSpan_LLMSpansOnlyTrue(t *testing.T) {
	p := &OtelPlugin{
		serviceName:  "test",
		llmSpansOnly: true,
	}
	trace := &schemas.Trace{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
		Spans: []*schemas.Span{
			makeSpan("/v1/chat/completions", schemas.SpanKindHTTPRequest),
			makeSpan("llm.call", schemas.SpanKindLLMCall),
			makeSpan("plugin.otel.prehook", schemas.SpanKindPlugin),
			makeSpan("plugin.telemetry.posthook", schemas.SpanKindPlugin),
			makeSpan("key.selection", schemas.SpanKindInternal),
			makeSpan("retry.attempt.1", schemas.SpanKindRetry),
			makeSpan("fallback.openai.gpt-4", schemas.SpanKindFallback),
			makeSpan("embedding.ada-002", schemas.SpanKindEmbedding),
			makeSpan("speech.tts-1", schemas.SpanKindSpeech),
			makeSpan("transcription.whisper-1", schemas.SpanKindTranscription),
			makeSpan("mcp.tool.search", schemas.SpanKindMCPTool),
		},
	}

	result := p.convertTraceToResourceSpan(trace)
	if result == nil {
		t.Fatal("expected non-nil ResourceSpan, got nil")
	}
	spans := result.ScopeSpans[0].Spans

	// Only LLM operation spans should be kept (all 6 kinds)
	if len(spans) != 6 {
		t.Errorf("expected 6 spans (LLM operations only), got %d", len(spans))
		for _, s := range spans {
			t.Logf("  span: %s", s.Name)
		}
	}

	expectedNames := map[string]bool{
		"llm.call":               true,
		"retry.attempt.1":        true,
		"fallback.openai.gpt-4": true,
		"embedding.ada-002":      true,
		"speech.tts-1":           true,
		"transcription.whisper-1": true,
	}
	for _, s := range spans {
		if !expectedNames[s.Name] {
			t.Errorf("unexpected span exported: %s", s.Name)
		}
	}
}

// TestConvertTraceToResourceSpan_LLMSpansOnlyNoLLMSpans verifies nil is returned when no LLM spans exist after filtering.
func TestConvertTraceToResourceSpan_LLMSpansOnlyNoLLMSpans(t *testing.T) {
	p := &OtelPlugin{
		serviceName:  "test",
		llmSpansOnly: true,
	}
	trace := &schemas.Trace{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
		Spans: []*schemas.Span{
			makeSpan("/v1/chat/completions", schemas.SpanKindHTTPRequest),
			makeSpan("plugin.otel.prehook", schemas.SpanKindPlugin),
			makeSpan("plugin.telemetry.posthook", schemas.SpanKindPlugin),
			makeSpan("key.selection", schemas.SpanKindInternal),
		},
	}

	result := p.convertTraceToResourceSpan(trace)
	if result != nil {
		t.Errorf("expected nil ResourceSpan when no LLM spans exist, got non-nil with %d scope spans", len(result.ScopeSpans))
	}
}

// TestIsLLMOperationSpan validates classification of all span kinds as LLM or infrastructure.
func TestIsLLMOperationSpan(t *testing.T) {
	llmKinds := []schemas.SpanKind{
		schemas.SpanKindLLMCall,
		schemas.SpanKindRetry,
		schemas.SpanKindFallback,
		schemas.SpanKindEmbedding,
		schemas.SpanKindSpeech,
		schemas.SpanKindTranscription,
	}
	for _, kind := range llmKinds {
		if !isLLMOperationSpan(kind) {
			t.Errorf("expected %s to be an LLM operation span", kind)
		}
	}

	infraKinds := []schemas.SpanKind{
		schemas.SpanKindHTTPRequest,
		schemas.SpanKindPlugin,
		schemas.SpanKindInternal,
		schemas.SpanKindMCPTool,
		schemas.SpanKindUnspecified,
	}
	for _, kind := range infraKinds {
		if isLLMOperationSpan(kind) {
			t.Errorf("expected %s to NOT be an LLM operation span", kind)
		}
	}
}
