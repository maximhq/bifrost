package otel

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"go.opentelemetry.io/otel/attribute"
)

func TestGetStringSliceAttr_AnyPreservesIndex(t *testing.T) {
	got := getStringSliceAttr(map[string]any{"k": []any{"a", 1, "c"}}, "k")
	want := []string{"a", "", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A non-string ID element must not shift the aligned names: the entry with the
// bad ID is dropped along with its name, and remaining ids keep their own names.
func TestEntitySetFromAttrs_MixedAnyKeepsAlignment(t *testing.T) {
	attrs := map[string]any{
		schemas.AttrBifrostTeamIDs:   []any{"a1", 42, "c3"},
		schemas.AttrBifrostTeamNames: []any{"Alpha", "Bogus", "Gamma"},
	}
	ids, names := entitySetFromAttrs(attrs, schemas.AttrBifrostTeamIDs, schemas.AttrBifrostTeamNames, schemas.AttrBifrostTeamID, schemas.AttrBifrostTeamName)
	if ids != "a1,c3" || names != "Alpha,Gamma" {
		t.Errorf("got (%q,%q), want (\"a1,c3\",\"Alpha,Gamma\")", ids, names)
	}
}

// TestBuildSpanAttrs_TypedMatchesAttributeMap pins the typed dimension reads to
// the attribute-map reads they replaced: the same span, expressed either way,
// must produce identical metric dimensions. A field that exists on
// SpanEnrichment but is never emitted as an attribute (or the reverse) shows up
// here rather than as a metric silently losing a dimension.
func TestBuildSpanAttrs_TypedMatchesAttributeMap(t *testing.T) {
	fallbackIdx := 2
	enrichment := &schemas.SpanEnrichment{
		VirtualKeyID: "vk-1", VirtualKeyName: "vk-name",
		SelectedKeyID: "sk-1", SelectedKeyName: "sk-name",
		RoutingRuleID: "rr-1", RoutingRuleName: "rule one",
		TeamIDs: []string{"t1", "t2"}, TeamNames: []string{"team one", "team two"},
		CustomerID: "c1", CustomerName: "cust one",
		BusinessUnitIDs: []string{"bu1"}, BusinessUnitNames: []string{"bu one"},
		ProjectID: "p1", ProjectName: "proj one",
		App:                "Claude Code",
		RoutingEnginesUsed: []string{"routing-rule", "governance"},
		ComplexityTier:     "COMPLEX", ComplexityMechanism: "semantic",
		FallbackIndex: &fallbackIdx,
	}

	// Map-only span: what a span built by a path that does not populate the
	// typed record still looks like.
	mapSpan := &schemas.Span{Name: "chat gpt-4o", Attributes: map[string]any{}}
	enrichment.ApplyToSpan(mapSpan)
	mapSpan.Enrichment = nil // force the fallback path
	mapSpan.Attributes[schemas.AttrProviderName] = "openai"
	mapSpan.Attributes[schemas.AttrRequestModel] = "gpt-4o"
	mapSpan.Attributes[schemas.AttrLegacyRequestType] = "chat_completion"
	// Written by the tracer post-response, not by ApplyToSpan.
	mapSpan.Attributes[schemas.AttrBifrostAlias] = "my-alias"
	mapSpan.Attributes[schemas.AttrBifrostRoutingEngineUsed] = "routing-rule,governance"
	mapSpan.Attributes[schemas.AttrBifrostComplexityTier] = "COMPLEX"
	mapSpan.Attributes[schemas.AttrBifrostComplexityMechanism] = "semantic"

	// Typed span: same data, read off the record.
	typedSpan := &schemas.Span{
		Name:       "chat gpt-4o",
		Attributes: map[string]any{},
		Enrichment: enrichment,
		LLM: &schemas.LLMSpanData{
			Provider:     schemas.OpenAI,
			RequestModel: "gpt-4o",
			RequestType:  schemas.ChatCompletionRequest,
			Alias:        "my-alias",
		},
	}

	fromMap := buildSpanAttrs(mapSpan)
	fromTyped := buildSpanAttrs(typedSpan)

	if len(fromMap) != len(fromTyped) {
		t.Fatalf("dimension count: map = %d, typed = %d", len(fromMap), len(fromTyped))
	}
	mapped := make(map[string]string, len(fromMap))
	for _, kv := range fromMap {
		mapped[string(kv.Key)] = kv.Value.Emit()
	}
	for _, kv := range fromTyped {
		want, ok := mapped[string(kv.Key)]
		if !ok {
			t.Errorf("%s: present in typed output, absent from map output", kv.Key)
			continue
		}
		if got := kv.Value.Emit(); got != want {
			t.Errorf("%s: typed = %q, map = %q", kv.Key, got, want)
		}
	}
}

// TestMetricLabelsMatchEnrichmentRegistry keeps the otel metric labels in parity with
// the registry: every metric-safe dimension is a label on both the span-sourced and
// the context-sourced path, and no other dimension is.
func TestMetricLabelsMatchEnrichmentRegistry(t *testing.T) {
	want := map[string]bool{"bifrost_instance_id": true}
	for _, n := range schemas.MetricSafeEnrichmentDimNames() {
		want[n] = true
	}
	span := &schemas.Span{Name: "chat gpt-4o", Attributes: map[string]any{}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for name, attrs := range map[string][]attribute.KeyValue{
		"span":    buildSpanAttrs(span),
		"context": buildContextAttrs(ctx, nil, nil),
	} {
		got := map[string]bool{}
		for _, kv := range attrs {
			got[string(kv.Key)] = true
		}
		for n := range want {
			if !got[n] {
				t.Errorf("%s: metric-safe dimension %q is not a label", name, n)
			}
		}
		for n := range got {
			if !want[n] {
				t.Errorf("%s: label %q is not a metric-safe dimension", name, n)
			}
		}
	}
}

// The dimensions derived post-response (app, alias, routing, complexity) reach the
// labels on both paths.
func TestMetricLabelsCarryDerivedDimensions(t *testing.T) {
	ua := "claude-cli/2.1.0 (external, cli)"
	app := schemas.DetectAppFromUserAgent(ua)
	if app == "" || app == schemas.UserAgentAppOther {
		t.Fatalf("test user agent %q is not classified (got %q)", ua, app)
	}
	want := map[string]string{
		"app":                  app,
		"alias":                "my-alias",
		"routing_engine_used":  "routing-rule,governance",
		"routing_rule_id":      "rr-1",
		"routing_rule_name":    "rule one",
		"complexity_tier":      "COMPLEX",
		"complexity_mechanism": "semantic",
	}

	span := &schemas.Span{
		Name:       "chat gpt-4o",
		Attributes: map[string]any{},
		Enrichment: &schemas.SpanEnrichment{
			App:                 app,
			RoutingRuleID:       "rr-1",
			RoutingRuleName:     "rule one",
			RoutingEnginesUsed:  []string{"routing-rule", "governance"},
			ComplexityTier:      "COMPLEX",
			ComplexityMechanism: "semantic",
		},
		LLM: &schemas.LLMSpanData{Provider: schemas.OpenAI, RequestModel: "gpt-4o", RequestType: schemas.ChatCompletionRequest, Alias: "my-alias"},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, ua)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceRoutingRuleID, "rr-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceRoutingRuleName, "rule one")
	ctx.SetValue(schemas.BifrostContextKeyRoutingEnginesUsed, []string{"routing-rule", "governance"})
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, "COMPLEX")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, "semantic")
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	resp.ChatResponse.ExtraFields.OriginalModelRequested = "my-alias"
	resp.ChatResponse.ExtraFields.ResolvedModelUsed = "gpt-4o"

	for name, attrs := range map[string][]attribute.KeyValue{
		"span":    buildSpanAttrs(span),
		"context": buildContextAttrs(ctx, resp, nil),
	} {
		got := map[string]string{}
		for _, kv := range attrs {
			got[string(kv.Key)] = kv.Value.Emit()
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: label %s = %q, want %q", name, k, got[k], v)
			}
		}
	}
}

func BenchmarkBuildMetricLabels(b *testing.B) {
	span := &schemas.Span{
		Name:       "chat gpt-4o",
		Attributes: map[string]any{},
		Enrichment: &schemas.SpanEnrichment{VirtualKeyID: "vk-1", VirtualKeyName: "vk", TeamID: "t1", TeamName: "team", App: "Claude Code"},
		LLM:        &schemas.LLMSpanData{Provider: schemas.OpenAI, RequestModel: "gpt-4o", RequestType: schemas.ChatCompletionRequest},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, "claude-cli/2.1.0 (external, cli)")
	b.Run("span", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = buildSpanAttrs(span)
		}
	})
	b.Run("context", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = buildContextAttrs(ctx, nil, nil)
		}
	})
}
