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

// TestIsModelAllowedForProvider_Regex pins regex entries (schemas.ModelRegexPrefix) in an
// explicit allowlist: bare patterns match the model name, provider-qualified patterns match
// "provider/model", a pattern is a full match, and literal entries match case-insensitively
// like every other evaluation path.
func TestIsModelAllowedForProvider_Regex(t *testing.T) {
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
		{"bare pattern matches the family", "gpt-4o-mini", schemas.WhiteList{"regex:^gpt-4.*"}, true},
		{"bare pattern is a full match", "gpt-4o", schemas.WhiteList{"regex:gpt-4"}, false},
		{"bare pattern is case-insensitive", "GPT-4O", schemas.WhiteList{"regex:^gpt-4.*"}, true},
		{"pattern does not admit another family", "claude-3-opus", schemas.WhiteList{"regex:^gpt-4.*"}, false},
		{"provider-qualified pattern matches provider/model", "gpt-4o", schemas.WhiteList{"regex:openai/gpt-.*"}, true},
		{"provider-qualified pattern for another provider misses", "gpt-4o", schemas.WhiteList{"regex:anthropic/.*"}, false},
		{"pattern alongside a literal", "gpt-4o", schemas.WhiteList{"claude", "regex:^o3.*"}, false},
		{"literal alongside a pattern", "claude", schemas.WhiteList{"claude", "regex:^o3.*"}, true},
		{"literal entries match case-insensitively", "GPT-4o", schemas.WhiteList{"gpt-4o"}, true},
		{"pattern with a slash does not need the catalog", "gpt-4o", schemas.WhiteList{"regex:(openai|azure)/gpt-4o"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mc.IsModelAllowedForProvider(provider, tc.model, nil, tc.allowed)
			if got != tc.want {
				t.Errorf("IsModelAllowedForProvider(%q, %v) = %v, want %v", tc.model, tc.allowed, got, tc.want)
			}
		})
	}

	// A regex allow entry is a pattern, not a model: listing must not surface it as a name,
	// but must include the catalog models it admits.
	mc.keyconf.SetProvider(provider, []schemas.Key{{ID: "k1", Models: schemas.WhiteList{"regex:^gpt-4.*"}}})
	for _, m := range mc.GetModelsForProvider(provider) {
		if schemas.IsRegexEntry(m) {
			t.Errorf("GetModelsForProvider surfaced the pattern %q as a model", m)
		}
	}
}
