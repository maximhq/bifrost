package utils

import (
	"sort"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// runListModelsPipeline mirrors the per-provider ToBifrostListModelsResponse driver
// (see providers/openai/models.go): filter every API model, then backfill configured
// entries not surfaced during filtering. It returns "id" / "id(alias=value)" strings.
func runListModelsPipeline(p *ListModelsPipeline, apiIDs []string) []string {
	if p.ShouldEarlyExit() {
		return []string{}
	}
	included := make(map[string]bool)
	out := make([]string, 0, len(apiIDs))
	emit := func(resolvedID, aliasValue string) {
		s := string(p.ProviderKey) + "/" + resolvedID
		if aliasValue != "" {
			s += "(alias=" + aliasValue + ")"
		}
		out = append(out, s)
		included[strings.ToLower(resolvedID)] = true
	}
	for _, id := range apiIDs {
		for _, r := range p.FilterModel(id) {
			emit(r.ResolvedID, r.AliasValue)
		}
	}
	for _, m := range p.BackfillModels(included) {
		id := strings.TrimPrefix(m.ID, string(p.ProviderKey)+"/")
		alias := ""
		if m.Alias != nil {
			alias = *m.Alias
		}
		if alias != "" {
			out = append(out, string(p.ProviderKey)+"/"+id+"(alias="+alias+")")
		} else {
			out = append(out, string(p.ProviderKey)+"/"+id)
		}
	}
	sort.Strings(out)
	return out
}

func newTestPipeline(models schemas.WhiteList, blacklist schemas.BlackList, aliases schemas.KeyAliases) *ListModelsPipeline {
	return &ListModelsPipeline{
		AllowedModels:     models,
		BlacklistedModels: blacklist,
		Aliases:           aliases,
		ProviderKey:       schemas.OpenAI,
		MatchFns:          DefaultMatchFns(),
	}
}

// TestListModels_WildcardWithAlias_AddsAliasKeepsModels is the baseline: an unrestricted
// key lists every provider model AND the configured alias (alias is additive).
func TestListModels_WildcardWithAlias_AddsAliasKeepsModels(t *testing.T) {
	p := newTestPipeline(schemas.WhiteList{"*"}, nil, schemas.KeyAliases{"best-model": {ModelID: "gpt-4o"}})
	got := runListModelsPipeline(p, []string{"gpt-4o", "gpt-4", "gpt-3.5-turbo"})
	assert.Equal(t, []string{
		"openai/best-model(alias=gpt-4o)",
		"openai/gpt-3.5-turbo",
		"openai/gpt-4",
		"openai/gpt-4o",
	}, got)
}

// TestListModels_RestrictedAllowlistKeepsConfiguredAlias is the #4170 regression: a key
// whose allowlist restricts the provider's own models must still surface configured
// aliases (previously the alias was dropped because its key was not in the allowlist).
func TestListModels_RestrictedAllowlistKeepsConfiguredAlias(t *testing.T) {
	p := newTestPipeline(schemas.WhiteList{"gpt-4o"}, nil, schemas.KeyAliases{"my-alias": {ModelID: "gpt-4o"}})
	got := runListModelsPipeline(p, []string{"gpt-4o", "gpt-4", "gpt-3.5-turbo"})
	// gpt-4o is allowlisted (kept); gpt-4/gpt-3.5 are filtered out; the alias is always surfaced.
	assert.Equal(t, []string{
		"openai/gpt-4o",
		"openai/my-alias(alias=gpt-4o)",
	}, got)
}

// TestListModels_EmptyAllowlistWithAliasSurfacesAlias: a deny-all allowlist filters every
// provider model, but explicitly-configured aliases must still appear (previously empty).
func TestListModels_EmptyAllowlistWithAliasSurfacesAlias(t *testing.T) {
	p := newTestPipeline(schemas.WhiteList{}, nil, schemas.KeyAliases{"best-model": {ModelID: "gpt-4o"}})
	got := runListModelsPipeline(p, []string{"gpt-4o", "gpt-4"})
	assert.Equal(t, []string{"openai/best-model(alias=gpt-4o)"}, got)
}

// TestListModels_EmptyAllowlistNoAliasIsEmpty: deny-all with no aliases still returns nothing
// (guards the early-exit path is unchanged).
func TestListModels_EmptyAllowlistNoAliasIsEmpty(t *testing.T) {
	p := newTestPipeline(schemas.WhiteList{}, nil, nil)
	assert.True(t, p.ShouldEarlyExit())
	got := runListModelsPipeline(p, []string{"gpt-4o", "gpt-4"})
	assert.Empty(t, got)
}

// TestListModels_AliasBackfilledWhenTargetNotReturned: an alias whose target model is not in
// the API response is still listed via backfill.
func TestListModels_AliasBackfilledWhenTargetNotReturned(t *testing.T) {
	p := newTestPipeline(schemas.WhiteList{"*"}, nil, schemas.KeyAliases{"ft-alias": {ModelID: "ft:custom-model"}})
	got := runListModelsPipeline(p, []string{"gpt-4o"})
	assert.Equal(t, []string{
		"openai/ft-alias(alias=ft:custom-model)",
		"openai/gpt-4o",
	}, got)
}

// TestListModels_BlacklistBlocksAlias: the blacklist still wins over an alias, whether the
// alias is matched during filtering or backfilled.
func TestListModels_BlacklistBlocksAlias(t *testing.T) {
	p := newTestPipeline(
		schemas.WhiteList{"*"},
		schemas.BlackList{"blocked-alias"},
		schemas.KeyAliases{"blocked-alias": {ModelID: "gpt-4o"}, "ok-alias": {ModelID: "gpt-4o"}},
	)
	got := runListModelsPipeline(p, []string{"gpt-4o"})
	assert.Equal(t, []string{
		"openai/gpt-4o",
		"openai/ok-alias(alias=gpt-4o)",
	}, got)
}

// TestListModels_NoDuplicateWhenAliasKeyAlsoAllowlisted: when a restricted allowlist contains
// both a real model and an alias key that maps to it, no entry is duplicated.
func TestListModels_NoDuplicateWhenAliasKeyAlsoAllowlisted(t *testing.T) {
	p := newTestPipeline(
		schemas.WhiteList{"best-model", "gpt-4o"},
		nil,
		schemas.KeyAliases{"best-model": {ModelID: "gpt-4o"}},
	)
	got := runListModelsPipeline(p, []string{"gpt-4o", "gpt-4"})
	assert.Equal(t, []string{
		"openai/best-model(alias=gpt-4o)",
		"openai/gpt-4o",
	}, got)
}

// TestListModels_UnfilteredSkipsAllowlistAndBackfill: unfiltered listings skip allowlist
// filtering and alias backfill, but still resolve aliases for models the API returned.
func TestListModels_UnfilteredSkipsAllowlistAndBackfill(t *testing.T) {
	p := newTestPipeline(schemas.WhiteList{"gpt-4o"}, nil, schemas.KeyAliases{"best-model": {ModelID: "gpt-4o"}})
	p.Unfiltered = true
	got := runListModelsPipeline(p, []string{"gpt-4o", "gpt-4"})
	// gpt-4 is not filtered out (allowlist ignored); best-model resolves from gpt-4o; no backfill.
	assert.Equal(t, []string{
		"openai/best-model(alias=gpt-4o)",
		"openai/gpt-4",
		"openai/gpt-4o",
	}, got)
}
