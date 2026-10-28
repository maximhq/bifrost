package modelcatalog

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// TestIsModelAllowedForProvider_ExplicitList pins the explicit-allowlist branch
// after the option-1 lazy-defer rewrite: bare-name and provider-prefixed
// matching must behave exactly as before.
func TestIsModelAllowedForProvider_ExplicitList(t *testing.T) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	// Give OpenAI a live catalog carrying a provider-prefixed entry, so the
	// prefixed branch has something to match (ParseModelString only strips
	// recognized provider prefixes, so this must be a real provider).
	provider := schemas.OpenAI
	mc.UpsertLive(provider, "k1", false, []string{"openai/gpt-4o", "gpt-4o"})

	cases := []struct {
		name    string
		model   string
		allowed schemas.WhiteList
		want    bool
	}{
		{"bare direct match", "gpt-4o", schemas.WhiteList{"gpt-4o", "claude"}, true},
		{"bare match is case-insensitive like every other list check", "gpt-4o", schemas.WhiteList{"GPT-4O"}, true},
		{"bare no match (deny)", "gpt-4o", schemas.WhiteList{"claude", "gemini"}, false},
		{"empty allowlist denies", "gpt-4o", schemas.WhiteList{}, false},
		{"prefixed match", "gpt-4o", schemas.WhiteList{"openai/gpt-4o"}, true},
		{"prefixed present but wrong model", "gpt-4o-mini", schemas.WhiteList{"openai/gpt-4o"}, false},
		{"match after a prefixed miss (ordering)", "gpt-4o", schemas.WhiteList{"openai/other", "openai/gpt-4o"}, true},
		{"regex entry matches the bare name", "gpt-4o-mini", schemas.WhiteList{"regex:^gpt-4.*"}, true},
		{"regex entry is matched against the bare name only", "gpt-4o", schemas.WhiteList{"regex:^openai/gpt-4o$"}, false},
		{"regex entry miss", "gpt-3.5-turbo", schemas.WhiteList{"regex:^gpt-4.*"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mc.IsModelAllowedForProvider(provider, tc.model, nil, tc.allowed)
			if got != tc.want {
				t.Errorf("IsModelAllowedForProvider(%q, %v) = %v, want %v", tc.model, tc.allowed, got, tc.want)
			}
		})
	}
}

// TestIsModelAllowedForProvider_KeyAliases pins #6620: a request names a model by a key alias
// ("claude-sonnet"), and an allowlist that names the model the alias resolves to
// ("us.anthropic.claude-sonnet-4-6") must permit it. Aliases only ever widen a name to the
// model it stands for, so the allowlist still decides which models can be reached.
func TestIsModelAllowedForProvider_KeyAliases(t *testing.T) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(nil),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	provider := schemas.Bedrock
	const canonical = "us.anthropic.claude-sonnet-4-6"
	// Real keys carry a models list; an empty one allows nothing, so core never selects such a key.
	anyModel := schemas.WhiteList{"*"}
	withAliases := func(aliases ...schemas.KeyAliases) *configstore.ProviderConfig {
		cfg := &configstore.ProviderConfig{}
		for _, a := range aliases {
			cfg.Keys = append(cfg.Keys, schemas.Key{Aliases: a, Models: anyModel})
		}
		return cfg
	}
	withExtraKey := func(cfg *configstore.ProviderConfig, extra schemas.Key) *configstore.ProviderConfig {
		cfg.Keys = append(cfg.Keys, extra)
		return cfg
	}
	withDisabled := func(cfg *configstore.ProviderConfig, extra schemas.Key) *configstore.ProviderConfig {
		off := false
		extra.Enabled = &off
		cfg.Keys = append(cfg.Keys, extra)
		return cfg
	}
	sonnet := schemas.KeyAliases{"claude-sonnet": {ModelID: canonical}}

	cases := []struct {
		name    string
		model   string
		cfg     *configstore.ProviderConfig
		allowed schemas.WhiteList
		want    bool
	}{
		{"alias of an allowed canonical id is allowed", "claude-sonnet", withAliases(sonnet), schemas.WhiteList{canonical}, true},
		{"alias match is case-insensitive like alias resolution", "Claude-Sonnet", withAliases(sonnet), schemas.WhiteList{canonical}, true},
		{"every key maps the alias", "claude-sonnet", withAliases(sonnet, sonnet), schemas.WhiteList{canonical}, true},
		{"a key without the alias would send the name through as written", "claude-sonnet", withAliases(nil, sonnet), schemas.WhiteList{canonical}, false},
		{"another key maps the alias to a model that is not allowed", "claude-sonnet", withAliases(sonnet, schemas.KeyAliases{"claude-sonnet": {ModelID: "us.anthropic.claude-opus-4-6"}}), schemas.WhiteList{canonical}, false},
		{"a disabled key is never selected, so it is ignored", "claude-sonnet", withDisabled(withAliases(sonnet), schemas.Key{}), schemas.WhiteList{canonical}, true},
		{"names differing only in case that map elsewhere must all be allowed", "Claude-Sonnet", withAliases(schemas.KeyAliases{"claude-sonnet": {ModelID: canonical}, "CLAUDE-SONNET": {ModelID: "us.anthropic.claude-opus-4-6"}}), schemas.WhiteList{canonical}, false},
		{"an empty alias target allows nothing", "claude-sonnet", withAliases(schemas.KeyAliases{"claude-sonnet": {ModelID: ""}}), schemas.WhiteList{canonical}, false},
		{"alias allowed by a regex entry on its target", "claude-sonnet", withAliases(sonnet), schemas.WhiteList{"regex:^us\\.anthropic\\..*"}, true},
		{"alias whose target is not allowed stays blocked", "claude-sonnet", withAliases(sonnet), schemas.WhiteList{"us.anthropic.claude-haiku-4-5"}, false},
		{"unknown name stays blocked", "claude-opus", withAliases(sonnet), schemas.WhiteList{canonical}, false},
		{"no provider config means no aliases", "claude-sonnet", nil, schemas.WhiteList{canonical}, false},
		{"keys without aliases change nothing", "claude-sonnet", withAliases(nil), schemas.WhiteList{canonical}, false},
		{"the canonical id itself is still allowed", canonical, withAliases(sonnet), schemas.WhiteList{canonical}, true},
		{"an alias in the list is still matched by name", "claude-sonnet", withAliases(sonnet), schemas.WhiteList{"claude-sonnet"}, true},
		{"the canonical id is not opened by an alias-only allowlist", canonical, withAliases(sonnet), schemas.WhiteList{"claude-sonnet"}, false},
		// Key selection drops a key whose models or blacklisted_models list excludes the requested
		// name, so such a key can never serve it and its missing alias must not veto the request.
		{"a key restricted to other models cannot serve the name, so it does not veto", "claude-sonnet", withExtraKey(withAliases(sonnet), schemas.Key{Models: schemas.WhiteList{"titan-*"}}), schemas.WhiteList{canonical}, true},
		{"a key that blacklists the name cannot serve it, so it does not veto", "claude-sonnet", withExtraKey(withAliases(sonnet), schemas.Key{Models: anyModel, BlacklistedModels: schemas.BlackList{"claude-sonnet"}}), schemas.WhiteList{canonical}, true},
		{"a key that lists the name without the alias could be selected, so it still vetoes", "claude-sonnet", withExtraKey(withAliases(sonnet), schemas.Key{Models: schemas.WhiteList{"claude-sonnet"}}), schemas.WhiteList{canonical}, false},
		{"when key selection would drop every key nothing is admitted", "claude-sonnet", withExtraKey(&configstore.ProviderConfig{}, schemas.Key{Aliases: sonnet, Models: schemas.WhiteList{"titan-*"}}), schemas.WhiteList{canonical}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mc.IsModelAllowedForProvider(provider, tc.model, tc.cfg, tc.allowed)
			if got != tc.want {
				t.Errorf("IsModelAllowedForProvider(%q, %v) = %v, want %v", tc.model, tc.allowed, got, tc.want)
			}
		})
	}
}

// BenchmarkIsModelAllowedForProvider_ExplicitList is the per-request model check against an
// explicit allowlist. "direct" is the common case and must not change; "alias" and "blocked" are
// the paths the key-alias lookup adds work to, on a provider with 3 keys of 20 aliases each.
func BenchmarkIsModelAllowedForProvider_ExplicitList(b *testing.B) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(nil),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	cfg := &configstore.ProviderConfig{}
	for k := 0; k < 3; k++ {
		aliases := schemas.KeyAliases{}
		for a := 0; a < 20; a++ {
			aliases[fmt.Sprintf("alias-%d", a)] = schemas.AliasConfig{ModelID: fmt.Sprintf("us.vendor.model-%d", a)}
		}
		cfg.Keys = append(cfg.Keys, schemas.Key{Aliases: aliases, Models: schemas.WhiteList{"*"}})
	}
	allowed := schemas.WhiteList{"us.vendor.model-19", "us.vendor.other"}
	for _, tc := range []struct {
		name, model string
		want        bool
	}{
		{"direct", "us.vendor.other", true},
		{"alias", "alias-19", true},
		{"blocked", "not-a-model", false},
	} {
		// Fail instead of timing a path that returns early with the wrong verdict.
		if got := mc.IsModelAllowedForProvider(schemas.Bedrock, tc.model, cfg, allowed); got != tc.want {
			b.Fatalf("%s: IsModelAllowedForProvider(%q) = %v, want %v", tc.name, tc.model, got, tc.want)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = mc.IsModelAllowedForProvider(schemas.Bedrock, tc.model, cfg, allowed)
			}
		})
	}
}
