package modelcatalog

import (
	"fmt"
	"slices"
	"strings"
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
		{"defence in depth: names differing only in case that map elsewhere must all be allowed (Validate rejects this config)", "Claude-Sonnet", withAliases(schemas.KeyAliases{"claude-sonnet": {ModelID: canonical}, "CLAUDE-SONNET": {ModelID: "us.anthropic.claude-opus-4-6"}}), schemas.WhiteList{canonical}, false},
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

// A key the request's key restriction excludes is never selected, so its missing alias does not veto
// the name, and a restriction that allows no key admits no alias (#6620 review: the virtual key's
// own key_ids were ignored by the every-key rule).
func TestKeyAliasesResolveToAllowed_KeyIDs(t *testing.T) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(nil),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	const canonical = "us.anthropic.claude-sonnet-4-6"
	anyModel := schemas.WhiteList{"*"}
	sonnet := schemas.KeyAliases{"claude-sonnet": {ModelID: canonical}}
	keys := []schemas.Key{
		{ID: "aliased", Aliases: sonnet, Models: anyModel},
		{ID: "plain", Models: anyModel},
	}
	allowed := schemas.WhiteList{canonical}
	for _, tc := range []struct {
		name    string
		keyIDs  schemas.WhiteList
		allowed schemas.WhiteList
		want    bool
	}{
		{"every key: the plain key would send the name as written", allKeyIDs, allowed, false},
		{"restricted to the aliased key: admitted", schemas.WhiteList{"aliased"}, allowed, true},
		{"restricted to the plain key: refused", schemas.WhiteList{"plain"}, allowed, false},
		{"restricted to no key: nothing is admitted", schemas.WhiteList{}, allowed, false},
		{"restricted to an unknown key: nothing is admitted", schemas.WhiteList{"gone"}, allowed, false},
		{"a wildcard allowlist is not decided here", schemas.WhiteList{"aliased"}, schemas.WhiteList{"*"}, false},
		{"an empty allowlist allows nothing", schemas.WhiteList{"aliased"}, schemas.WhiteList{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mc.KeyAliasesResolveToAllowed(schemas.Bedrock, "claude-sonnet", keys, tc.keyIDs, tc.allowed); got != tc.want {
				t.Errorf("KeyAliasesResolveToAllowed(keyIDs=%v) = %v, want %v", tc.keyIDs, got, tc.want)
			}
		})
	}
	// The whole-provider check keeps its every-key rule.
	if mc.IsModelAllowedForProvider(schemas.Bedrock, "claude-sonnet", &configstore.ProviderConfig{Keys: keys}, allowed) {
		t.Error("IsModelAllowedForProvider admitted an alias a selectable key lacks")
	}

	// KeyAliasTargets reads only the keys the restriction allows.
	other := []schemas.Key{
		{ID: "a", Aliases: sonnet, Models: anyModel},
		{ID: "b", Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: "us.anthropic.claude-opus-4-6"}}, Models: anyModel},
	}
	for _, tc := range []struct {
		keyIDs schemas.WhiteList
		want   []string
	}{
		{allKeyIDs, []string{canonical, "us.anthropic.claude-opus-4-6"}},
		{schemas.WhiteList{"a"}, []string{canonical}},
		{schemas.WhiteList{"b"}, []string{"us.anthropic.claude-opus-4-6"}},
		{schemas.WhiteList{}, nil},
	} {
		var got []string
		KeyAliasTargets("claude-sonnet", other, tc.keyIDs, func(target string) { got = append(got, target) })
		if !slices.Equal(got, tc.want) {
			t.Errorf("KeyAliasTargets(keyIDs=%v) = %v, want %v", tc.keyIDs, got, tc.want)
		}
	}
}

// foldName must agree with strings.EqualFold, or the alias-name index would reject a name that alias
// resolution accepts: the long s and the Kelvin sign fold with ASCII letters, sigma has three forms,
// the dotted and dotless i of Turkish fold with nothing ASCII, and invalid bytes decode alike.
func TestFoldNameAgreesWithEqualFold(t *testing.T) {
	names := []string{
		"sonnet", "SONNET", "Sonnet", "\u017fonnet", "\u017fONNET", "kelvin", "\u212aelvin", "KELVIN",
		"\u03a3\u03c3\u03c2", "\u03c3\u03c3\u03c3", "\u03c2\u03a3\u03c3", "\u0130stanbul", "istanbul", "\u0131stanbul", "Istanbul",
		"claude-sonnet-4.6", "Claude-Sonnet-4.6", "a\xffb", "a\xfeb", "", "\u00df", "SS", "\u1e9e",
	}
	for _, a := range names {
		for _, b := range names {
			if got, want := foldName(a) == foldName(b), strings.EqualFold(a, b); got != want {
				t.Errorf("foldName(%q) == foldName(%q) is %v, strings.EqualFold is %v", a, b, got, want)
			}
		}
	}
	if s := "claude-sonnet-4.6"; foldName(s) != s {
		t.Errorf("an already-folded name is returned as is")
	}
}

// MayBeKeyAlias answers from an index of the provider's alias names, rebuilt when the provider's
// configuration publishes a new keys slice, and never rejects a name some key would resolve.
func TestMayBeKeyAlias(t *testing.T) {
	const provider = schemas.ModelProvider("alias-index-test")
	first := []schemas.Key{
		{ID: "a", Aliases: schemas.KeyAliases{"Claude-Sonnet": {ModelID: "x"}}},
		{ID: "b"},
	}
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"Claude-Sonnet", true},
		{"claude-sonnet", true}, // resolution is case-insensitive
		{"CLAUDE-SONNET", true},
		{"claude-opus", false},
		{"claude-sonnet-4-6", false},
	} {
		if got := MayBeKeyAlias(provider, tc.model, first); got != tc.want {
			t.Errorf("MayBeKeyAlias(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
	// An edited key publishes a new slice: the index follows it, and the old names are gone.
	second := []schemas.Key{{ID: "a", Aliases: schemas.KeyAliases{"claude-opus": {ModelID: "y"}}}, {ID: "b"}}
	if !MayBeKeyAlias(provider, "claude-opus", second) || MayBeKeyAlias(provider, "claude-sonnet", second) {
		t.Error("the index did not follow the new keys slice")
	}
	// So does a slice of another length sharing the first one's array.
	if MayBeKeyAlias(provider, "claude-opus", second[1:]) {
		t.Error("a narrowed slice was answered from the whole slice's index")
	}
	if MayBeKeyAlias(provider, "claude-opus", nil) {
		t.Error("no keys carry no alias")
	}
	// Every name an alias scan resolves passes the index.
	for _, model := range []string{"Claude-Sonnet", "claude-sonnet", "CLAUDE-SONNET"} {
		resolved := false
		KeyAliasTargets(model, []schemas.Key{{ID: "a", Models: schemas.WhiteList{"*"}, Aliases: first[0].Aliases}}, allKeyIDs, func(string) { resolved = true })
		if resolved && !MayBeKeyAlias(provider, model, first) {
			t.Errorf("%q resolves but the index rejects it", model)
		}
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

func TestKeyAliasTargets(t *testing.T) {
	anyModel := schemas.WhiteList{"*"}
	off := false
	collect := func(model string, keys []schemas.Key) []string {
		var got []string
		KeyAliasTargets(model, keys, allKeyIDs, func(target string) { got = append(got, target) })
		return got
	}
	sonnet := schemas.KeyAliases{"claude-sonnet": {ModelID: "us.anthropic.claude-sonnet-4-6"}}
	other := schemas.KeyAliases{"claude-sonnet": {ModelID: "us.anthropic.claude-opus-4-6"}}

	cases := []struct {
		name  string
		model string
		keys  []schemas.Key
		want  []string
	}{
		{"one key maps the name", "claude-sonnet", []schemas.Key{{Aliases: sonnet, Models: anyModel}}, []string{"us.anthropic.claude-sonnet-4-6"}},
		{"case-insensitive like alias resolution", "Claude-Sonnet", []schemas.Key{{Aliases: sonnet, Models: anyModel}}, []string{"us.anthropic.claude-sonnet-4-6"}},
		{"keys that disagree yield every target", "claude-sonnet", []schemas.Key{{Aliases: sonnet, Models: anyModel}, {Aliases: other, Models: anyModel}}, []string{"us.anthropic.claude-sonnet-4-6", "us.anthropic.claude-opus-4-6"}},
		{"a disabled key is ignored", "claude-sonnet", []schemas.Key{{Aliases: sonnet, Models: anyModel, Enabled: &off}}, nil},
		{"a key restricted to other models is ignored", "claude-sonnet", []schemas.Key{{Aliases: sonnet, Models: schemas.WhiteList{"titan-*"}}}, nil},
		{"a key that blacklists the name is ignored", "claude-sonnet", []schemas.Key{{Aliases: sonnet, Models: anyModel, BlacklistedModels: schemas.BlackList{"claude-sonnet"}}}, nil},
		{"an empty target is skipped", "claude-sonnet", []schemas.Key{{Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: ""}}, Models: anyModel}}, nil},
		{"a target equal to the name is skipped", "claude-sonnet", []schemas.Key{{Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: "claude-sonnet"}}, Models: anyModel}}, nil},
		{"an unknown name has no targets", "claude-opus", []schemas.Key{{Aliases: sonnet, Models: anyModel}}, nil},
		{"an exact-name hit suppresses the case-insensitive scan", "claude-sonnet", []schemas.Key{{Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: "x"}, "Claude-Sonnet": {ModelID: "y"}}, Models: anyModel}}, []string{"x"}},
		{"differently-cased matches all visit", "CLAUDE-SONNET", []schemas.Key{{Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: "x"}, "Claude-Sonnet": {ModelID: "y"}}, Models: anyModel}}, []string{"x", "y"}},
		{"keys without aliases have no targets", "claude-sonnet", []schemas.Key{{Models: anyModel}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(tc.model, tc.keys)
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if len(got) != len(want) || !slices.Equal(got, want) {
				t.Errorf("KeyAliasTargets(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}

	if HasKeyAliases([]schemas.Key{{Models: anyModel}}) {
		t.Error("HasKeyAliases = true for keys without aliases")
	}
	if !HasKeyAliases([]schemas.Key{{Models: anyModel}, {Aliases: sonnet, Models: anyModel}}) {
		t.Error("HasKeyAliases = false when a key has an alias")
	}
}
