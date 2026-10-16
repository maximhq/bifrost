package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestListModelsPipelineRegexEntries(t *testing.T) {
	p := &ListModelsPipeline{
		AllowedModels:     schemas.WhiteList{"regex:^gpt-4.*", "o1-mini"},
		BlacklistedModels: schemas.BlackList{"regex:.*-preview$"},
		ProviderKey:       schemas.OpenAI,
		MatchFns:          DefaultMatchFns(),
	}
	if p.ShouldEarlyExit() {
		t.Fatal("a regex allow list is a restricted list, not an empty one")
	}
	for model, want := range map[string]int{
		"gpt-4o":         1,
		"GPT-4o-mini":    1,
		"gpt-4o-preview": 0,
		"gpt-3.5-turbo":  0,
		"o1-mini":        1,
	} {
		if got := len(p.FilterModel(model)); got != want {
			t.Errorf("FilterModel(%q) returned %d results, want %d", model, got, want)
		}
	}
	backfilled := p.BackfillModels(map[string]bool{})
	if len(backfilled) != 1 || backfilled[0].ID != "openai/o1-mini" {
		t.Fatalf("only the exact entry is backfilled, got %+v", backfilled)
	}
}
