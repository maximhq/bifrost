package otel

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
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
		TeamIDs: []string{"t1", "t2"}, TeamNames: []string{"team one", "team two"},
		CustomerID: "c1", CustomerName: "cust one",
		BusinessUnitIDs: []string{"bu1"}, BusinessUnitNames: []string{"bu one"},
		ProjectID: "p1", ProjectName: "proj one",
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

	// Typed span: same data, read off the record.
	typedSpan := &schemas.Span{
		Name:       "chat gpt-4o",
		Attributes: map[string]any{},
		Enrichment: enrichment,
		LLM: &schemas.LLMSpanData{
			Provider:     schemas.OpenAI,
			RequestModel: "gpt-4o",
			RequestType:  schemas.ChatCompletionRequest,
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
