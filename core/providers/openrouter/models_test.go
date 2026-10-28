package openrouter

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestNormalizeOpenRouterDisplayNames pins that OpenRouter's wire `name`
// (decoded onto Model.Name) is moved to the upstream-label field DisplayName,
// leaving Name for datasheet-derived values (#8914).
func TestNormalizeOpenRouterDisplayNames(t *testing.T) {
	t.Parallel()

	models := []schemas.Model{
		{ID: "openai/gpt-4o-mini", Name: schemas.Ptr("OpenAI: GPT-4o-mini")},
		{ID: "anthropic/claude-haiku-4-5", Name: schemas.Ptr("Claude Haiku 4.5"), DisplayName: schemas.Ptr("Already labeled")},
		{ID: "bare/model"},
	}
	normalizeOpenRouterDisplayNames(models)

	if models[0].Name != nil {
		t.Fatalf("Name should be cleared, got %q", *models[0].Name)
	}
	if models[0].DisplayName == nil || *models[0].DisplayName != "OpenAI: GPT-4o-mini" {
		t.Fatalf("DisplayName should carry the upstream label, got %+v", models[0].DisplayName)
	}
	if models[1].DisplayName == nil || *models[1].DisplayName != "Already labeled" {
		t.Fatalf("an existing DisplayName must be preserved, got %+v", models[1].DisplayName)
	}
	if models[1].Name == nil {
		t.Fatal("an existing DisplayName must stop Name from being moved")
	}
	if models[2].DisplayName != nil || models[2].Name != nil {
		t.Fatal("a model with no label must stay unlabeled")
	}
}
