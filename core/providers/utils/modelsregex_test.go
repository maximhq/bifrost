package utils

import (
	"maps"
	"strings"
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

// An alias allowed only through a regex: entry, whose target the upstream list never
// returns (a Bedrock inference profile here), is still listed, like an exact entry.
func TestListModelsPipelineBackfillsRegexAllowedAliases(t *testing.T) {
	p := &ListModelsPipeline{
		AllowedModels:     schemas.WhiteList{"regex:^claude-.*", "claude-haiku"},
		BlacklistedModels: schemas.BlackList{"claude-opus"},
		Aliases: schemas.KeyAliases{
			"claude-sonnet": {ModelID: "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
			"claude-haiku":  {ModelID: "us.anthropic.claude-haiku-4-5-20251001-v1:0"},
			"claude-opus":   {ModelID: "us.anthropic.claude-opus-4-1-20250805-v1:0"},
			"claude-listed": {ModelID: "anthropic.claude-3-haiku-20240307-v1:0"},
			"titan":         {ModelID: "us.amazon.titan-text-express-v1"},
		},
		ProviderKey: schemas.Bedrock,
		MatchFns:    DefaultMatchFns(),
	}
	included := map[string]bool{}
	got := map[string]string{}
	add := func(id, alias string) {
		if _, dup := got[id]; dup {
			t.Fatalf("%s is listed twice", id)
		}
		got[id] = alias
	}
	for _, upstream := range []string{"anthropic.claude-3-haiku-20240307-v1:0", "amazon.titan-text-express-v1"} {
		for _, r := range p.FilterModel(upstream) {
			add("bedrock/"+r.ResolvedID, r.AliasValue)
			included[strings.ToLower(r.ResolvedID)] = true
		}
	}
	for _, m := range p.BackfillModels(included) {
		alias := ""
		if m.Alias != nil {
			alias = *m.Alias
		}
		add(m.ID, alias)
	}
	want := map[string]string{
		"bedrock/claude-listed": "anthropic.claude-3-haiku-20240307-v1:0",
		"bedrock/claude-sonnet": "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"bedrock/claude-haiku":  "us.anthropic.claude-haiku-4-5-20251001-v1:0",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
}
