package modelcatalog

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
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
		{"bare no match (deny)", "gpt-4o", schemas.WhiteList{"claude", "gemini"}, false},
		{"empty allowlist denies", "gpt-4o", schemas.WhiteList{}, false},
		{"prefixed match", "gpt-4o", schemas.WhiteList{"openai/gpt-4o"}, true},
		{"prefixed present but wrong model", "gpt-4o-mini", schemas.WhiteList{"openai/gpt-4o"}, false},
		{"match after a prefixed miss (ordering)", "gpt-4o", schemas.WhiteList{"openai/other", "openai/gpt-4o"}, true},
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

// TestIsModelAllowedForProvider_ExactOnly pins that an explicit allowlist is exact: a
// regex-looking entry is an ordinary name that matches only itself. Patterns live in their
// own field and are evaluated by the permit, not by this catalog check.
func TestIsModelAllowedForProvider_ExactOnly(t *testing.T) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o"}),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	provider := schemas.OpenAI
	mc.UpsertLive(provider, "k1", false, []string{"openai/gpt-4o", "gpt-4o"})

	cases := []struct {
		name    string
		model   string
		allowed schemas.WhiteList
		want    bool
	}{
		{"regex-looking entry does not admit the family", "gpt-4o-mini", schemas.WhiteList{"regex:^gpt-4.*"}, false},
		{"regex-looking entry matches itself", "regex:^gpt-4.*", schemas.WhiteList{"regex:^gpt-4.*"}, true},
		{"bare pattern syntax is a literal", "gpt-4o", schemas.WhiteList{"^gpt-4.*"}, false},
		{"literal entries match case-insensitively", "GPT-4o", schemas.WhiteList{"gpt-4o"}, true},
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

// TestKeyPatternsInCatalog pins how a key's pattern twins reach the catalog: the models a
// pattern admits are listed, a block pattern removes them, the pattern string itself is never
// surfaced as a model, and key selection honours both sides.
func TestKeyPatternsInCatalog(t *testing.T) {
	mc := &ModelCatalog{
		datasheet: datasheet.NewTestStore(map[string]string{"gpt-4o": "gpt-4o", "gpt-4o-mini": "gpt-4o-mini", "gpt-4o-preview": "gpt-4o-preview", "claude-3": "claude-3"}),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	provider := schemas.OpenAI
	mc.keyconf.SetProvider(provider, []schemas.Key{{
		ID:                        "k1",
		Models:                    schemas.WhiteList{"claude-3"},
		ModelsPatterns:            schemas.ModelPatternList{"^gpt-4.*"},
		BlacklistedModelsPatterns: schemas.ModelPatternList{".*-preview$"},
	}})
	// The live list is what the provider's list-models pipeline already gated by
	// the key's rule; deprecated datasheet rows are reconciled on top through the
	// same rule, which is where a pattern must admit or block a name here.
	mc.UpsertLive(provider, "k1", false, []string{"gpt-4o", "gpt-4o-mini"})

	listed := mc.GetModelsForProvider(provider)
	want := map[string]bool{"gpt-4o": true, "gpt-4o-mini": true, "claude-3": true}
	for _, m := range listed {
		if m == "^gpt-4.*" || m == ".*-preview$" {
			t.Errorf("GetModelsForProvider surfaced the pattern %q as a model", m)
		}
		if m == "gpt-4o-preview" {
			t.Errorf("GetModelsForProvider listed a model the block pattern removes")
		}
		delete(want, m)
	}
	if len(want) > 0 {
		t.Errorf("GetModelsForProvider missed %v in %v", want, listed)
	}

	if !mc.keyconf.IsAllowed(provider, "gpt-4o") || !mc.keyconf.IsAllowed(provider, "GPT-4-TURBO") {
		t.Errorf("a pattern-admitted model should be allowed on the provider")
	}
	if mc.keyconf.IsAllowed(provider, "gpt-4o-preview") {
		t.Errorf("the block pattern should win over the allow pattern")
	}
	if mc.keyconf.IsAllowed(provider, "o3") {
		t.Errorf("a model no list or pattern admits should be denied")
	}
	if keys := mc.keyconf.KeysAllowingModel(provider, "gpt-4o"); len(keys) != 1 || keys[0] != "k1" {
		t.Errorf("KeysAllowingModel(gpt-4o) = %v, want [k1]", keys)
	}
	if keys := mc.keyconf.KeysAllowingModel(provider, "gpt-4o-preview"); len(keys) != 0 {
		t.Errorf("KeysAllowingModel(gpt-4o-preview) = %v, want none", keys)
	}
	if got := mc.AllowedModelsPatternsForProvider(provider); len(got) != 1 || got[0] != "^gpt-4.*" {
		t.Errorf("AllowedModelsPatternsForProvider = %v", got)
	}
	if got := mc.BlacklistedModelsPatternsForProvider(provider); len(got) != 1 || got[0] != ".*-preview$" {
		t.Errorf("BlacklistedModelsPatternsForProvider = %v", got)
	}

	// A block pattern only blocks provider-wide when every enabled key carries it.
	mc.keyconf.SetProvider(provider, []schemas.Key{
		{ID: "k1", Models: schemas.WhiteList{"*"}, BlacklistedModelsPatterns: schemas.ModelPatternList{".*-preview$"}},
		{ID: "k2", Models: schemas.WhiteList{"*"}},
	})
	if got := mc.BlacklistedModelsPatternsForProvider(provider); len(got) != 0 {
		t.Errorf("a pattern on one of two keys must not block provider-wide, got %v", got)
	}
	if !mc.keyconf.IsAllowed(provider, "gpt-4o-preview") {
		t.Errorf("k2 can still serve the model k1 blocks by pattern")
	}
}
