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
