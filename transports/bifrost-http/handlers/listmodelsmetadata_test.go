package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func ptr[T any](v T) *T { return &v }

// TestDropModelsWithoutMetadata pins the rule that a bare identifier is not
// worth advertising: a caller given only an ID has no context length to size
// against, no pricing to budget with, and no capabilities to branch on.
func TestDropModelsWithoutMetadata(t *testing.T) {
	resp := &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openai/gpt-4o", ContextLength: ptr(128000)},
		{ID: "openai/ghost-model"}, // nothing known: dropped
		{ID: "openai/o1", OwnedBy: ptr("openai")},
		{ID: "openai/legacy", IsDeprecated: true},
		{ID: "openai/another-ghost"}, // dropped
	}}

	dropModelsWithoutMetadata(resp)

	got := make([]string, len(resp.Data))
	for i, m := range resp.Data {
		got[i] = m.ID
	}
	want := []string{"openai/gpt-4o", "openai/o1", "openai/legacy"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kept %v, want %v", got, want)
		}
	}
}

// Any single populated field is enough. Providers and the datasheet describe
// models unevenly, so demanding a particular field would discard usable models.
func TestHasModelMetadata_AnyFieldCounts(t *testing.T) {
	if hasModelMetadata(schemas.Model{ID: "bare"}) {
		t.Error("an ID-only model must count as having no metadata")
	}

	populated := []schemas.Model{
		{ID: "a", Name: ptr("A")},
		{ID: "b", ContextLength: ptr(1)},
		{ID: "c", Pricing: &schemas.Pricing{}},
		{ID: "d", SupportedParameters: []string{"tools"}},
		{ID: "e", IsDeprecated: true},
		{ID: "f", Alias: ptr("deployment-1")},
		{ID: "g", AdditionalAttributes: map[string]string{"tier": "preview"}},
		{ID: "h", Architecture: &schemas.Architecture{}},
	}
	for _, m := range populated {
		if !hasModelMetadata(m) {
			t.Errorf("model %q has a populated field and must be kept", m.ID)
		}
	}
}

func TestDropModelsWithoutMetadata_NilAndEmptyAreSafe(t *testing.T) {
	dropModelsWithoutMetadata(nil)
	empty := &schemas.BifrostListModelsResponse{}
	dropModelsWithoutMetadata(empty)
	if len(empty.Data) != 0 {
		t.Error("expected an empty response to stay empty")
	}
}
