package otel

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestConvertSpanToOTELSpan_ForwardRawEnabled(t *testing.T) {
	p := &OtelPlugin{forwardRawRequestResponse: true}
	span := &schemas.Span{
		SpanID:    "abc123",
		Name:      "llm.call",
		Kind:      schemas.SpanKindLLMCall,
		Status:    schemas.SpanStatusOk,
		StartTime: time.Now(),
		EndTime:   time.Now(),
		Attributes: map[string]any{
			schemas.AttrRawRequest:  `{"model":"gpt-4"}`,
			schemas.AttrRawResponse: `{"choices":[]}`,
			schemas.AttrRequestModel: "gpt-4",
		},
	}

	otelSpan := p.convertSpanToOTELSpan("aabbccdd", span)

	found := map[string]bool{
		schemas.AttrRawRequest:   false,
		schemas.AttrRawResponse:  false,
		schemas.AttrRequestModel: false,
	}
	for _, kv := range otelSpan.Attributes {
		if _, ok := found[kv.Key]; ok {
			found[kv.Key] = true
		}
	}
	for key, present := range found {
		if !present {
			t.Errorf("expected attribute %q to be present when forwarding is enabled", key)
		}
	}
}

func TestConvertSpanToOTELSpan_ForwardRawDisabled(t *testing.T) {
	p := &OtelPlugin{forwardRawRequestResponse: false}
	span := &schemas.Span{
		SpanID:    "abc123",
		Name:      "llm.call",
		Kind:      schemas.SpanKindLLMCall,
		Status:    schemas.SpanStatusOk,
		StartTime: time.Now(),
		EndTime:   time.Now(),
		Attributes: map[string]any{
			schemas.AttrRawRequest:   `{"model":"gpt-4"}`,
			schemas.AttrRawResponse:  `{"choices":[]}`,
			schemas.AttrRequestModel: "gpt-4",
		},
	}

	otelSpan := p.convertSpanToOTELSpan("aabbccdd", span)

	for _, kv := range otelSpan.Attributes {
		if kv.Key == schemas.AttrRawRequest || kv.Key == schemas.AttrRawResponse {
			t.Errorf("attribute %q should be stripped when forwarding is disabled", kv.Key)
		}
	}

	// Other attributes should still be present
	foundModel := false
	for _, kv := range otelSpan.Attributes {
		if kv.Key == schemas.AttrRequestModel {
			foundModel = true
		}
	}
	if !foundModel {
		t.Error("non-raw attributes should be preserved when forwarding is disabled")
	}
}

func TestConvertSpanToOTELSpan_NoRawAttrs(t *testing.T) {
	p := &OtelPlugin{forwardRawRequestResponse: false}
	span := &schemas.Span{
		SpanID:    "abc123",
		Name:      "llm.call",
		Kind:      schemas.SpanKindLLMCall,
		Status:    schemas.SpanStatusOk,
		StartTime: time.Now(),
		EndTime:   time.Now(),
		Attributes: map[string]any{
			schemas.AttrRequestModel: "gpt-4",
		},
	}

	otelSpan := p.convertSpanToOTELSpan("aabbccdd", span)

	foundModel := false
	for _, kv := range otelSpan.Attributes {
		if kv.Key == schemas.AttrRequestModel {
			foundModel = true
		}
	}
	if !foundModel {
		t.Error("attributes should be unaffected when no raw attrs are present")
	}
}
