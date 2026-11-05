package modelcatalog

import (
	"slices"
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

// TestVLLMModelNameRoutesWithoutLiveModels pins that a vLLM key with no cached
// list-models result routes as if list-models returned its ModelName, without
// surfacing ModelName in the list-models views.
func TestVLLMModelNameRoutesWithoutLiveModels(t *testing.T) {
	vllmKey := func(id, modelName string, aliases schemas.KeyAliases) schemas.Key {
		return schemas.Key{
			ID: id, Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, Aliases: aliases,
			VLLMKeyConfig: &schemas.VLLMKeyConfig{ModelName: modelName},
		}
	}
	newCatalog := func(keys ...schemas.Key) *ModelCatalog {
		kc := keyconfig.New(nil)
		kc.Replace(map[schemas.ModelProvider][]schemas.Key{schemas.VLLM: keys})
		mc := &ModelCatalog{datasheet: datasheet.NewTestStore(map[string]string{}), live: live.New(nil), keyconf: kc, done: make(chan struct{})}
		mc.initCaches()
		return mc
	}
	assertAllowed := func(t *testing.T, mc *ModelCatalog, want map[string]bool) {
		t.Helper()
		for model, w := range want {
			if got := mc.IsModelAllowedForProvider(schemas.VLLM, model, nil, schemas.WhiteList{"*"}); got != w {
				t.Errorf("IsModelAllowedForProvider(vllm, %q) = %v, want %v", model, got, w)
			}
		}
	}

	t.Run("no cached results", func(t *testing.T) {
		mc := newCatalog(
			vllmKey("k20b", "gpt-oss-20b", schemas.KeyAliases{"gpt-oss-20b-1": {ModelID: "gpt-oss-20b"}}),
			vllmKey("k120b", "gpt-oss-120b", nil),
		)
		assertAllowed(t, mc, map[string]bool{"gpt-oss-20b": true, "gpt-oss-20b-1": true, "gpt-oss-120b": true, "gpt-4o": false})
		for _, models := range [][]string{mc.GetModelsForProvider(schemas.VLLM), mc.GetUnfilteredModelsForProvider(schemas.VLLM)} {
			if slices.Contains(models, "gpt-oss-20b") || slices.Contains(models, "gpt-oss-120b") {
				t.Errorf("ModelName leaked into list-models view: %v", models)
			}
		}
	})

	t.Run("cached result wins for that key", func(t *testing.T) {
		mc := newCatalog(vllmKey("k20b", "gpt-oss-20b", nil), vllmKey("k120b", "gpt-oss-120b", nil))
		mc.UpsertLive(schemas.VLLM, "k20b", false, []string{"served-20b"})
		mc.UpsertLive(schemas.VLLM, "k20b", true, []string{"served-20b"})
		assertAllowed(t, mc, map[string]bool{"served-20b": true, "gpt-oss-120b": true, "gpt-oss-20b": false, "gpt-4o": false})
	})

	t.Run("key without ModelName keeps unrestricted fallback", func(t *testing.T) {
		assertAllowed(t, newCatalog(vllmKey("kany", "", nil)), map[string]bool{"gpt-4o": true})
	})

	t.Run("mixed configured and unconfigured keys", func(t *testing.T) {
		unconfigured := vllmKey("kany", "", nil)
		unconfigured.BlacklistedModels = schemas.BlackList{"blocked-model"}
		disabled := vllmKey("kdisabled", "", nil)
		disabled.Enabled = ptrBool(false)
		mc := newCatalog(vllmKey("k20b", "gpt-oss-20b", nil), unconfigured, disabled)
		assertAllowed(t, mc, map[string]bool{"gpt-oss-20b": true, "gpt-4o": true, "blocked-model": false})
	})
}
