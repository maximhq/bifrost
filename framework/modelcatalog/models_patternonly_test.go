package modelcatalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// patternOnlyCatalog builds a catalog whose OpenAI datasheet holds two current
// models (one the allow pattern admits, one it does not) and two deprecated
// ones split the same way, with a single key that allows by pattern alone -
// no exact Models entry, which is what leaves the aggregated allow list nil.
func patternOnlyCatalog(t *testing.T) *ModelCatalog {
	t.Helper()
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"gpt-4o": {"provider": "openai", "mode": "chat", "base_model": "gpt-4o"},
		"o3": {"provider": "openai", "mode": "chat", "base_model": "o3"},
		"gpt-4-legacy": {"provider": "openai", "mode": "chat", "base_model": "gpt-4-legacy", "is_deprecated": true},
		"o3-legacy": {"provider": "openai", "mode": "chat", "base_model": "o3-legacy", "is_deprecated": true}
	}`)
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing testdata: %v", err)
	}
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing testdata: %v", err)
	}
	mc := &ModelCatalog{
		datasheet: ds,
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
	mc.initCaches()
	mc.keyconf.SetProvider(schemas.OpenAI, []schemas.Key{{
		ID:             "k1",
		ModelsPatterns: schemas.ModelPatternList{"^gpt-4.*"},
	}})
	return mc
}

// TestPatternOnlyKeyRestrictsDatasheetView pins that a key allowing models by
// pattern alone still restricts the datasheet-only listing to what the pattern
// admits: the aggregated exact allow list is nil there, which must not read as
// "no restriction" (every model listed) or as "nothing configured" (none).
func TestPatternOnlyKeyRestrictsDatasheetView(t *testing.T) {
	mc := patternOnlyCatalog(t)

	listed := mc.GetModelsForProvider(schemas.OpenAI)
	for _, m := range []string{"gpt-4o", "gpt-4-legacy"} {
		if !slices.Contains(listed, m) {
			t.Errorf("GetModelsForProvider missed %q, the allow pattern admits it: got %v", m, listed)
		}
	}
	for _, m := range []string{"o3", "o3-legacy"} {
		if slices.Contains(listed, m) {
			t.Errorf("GetModelsForProvider listed %q, which no allow rule admits: got %v", m, listed)
		}
	}
}

// TestPatternOnlyKeyRestrictsDatasheetAppend pins the same rule on the live
// path, where the deprecated datasheet rows are reconciled on top of the live
// list: only the ones the pattern admits may be appended.
func TestPatternOnlyKeyRestrictsDatasheetAppend(t *testing.T) {
	mc := patternOnlyCatalog(t)
	mc.UpsertLive(schemas.OpenAI, "k1", false, []string{"gpt-4o"})

	listed := mc.GetModelsForProvider(schemas.OpenAI)
	if !slices.Contains(listed, "gpt-4-legacy") {
		t.Errorf("GetModelsForProvider dropped the deprecated model the pattern admits: got %v", listed)
	}
	if slices.Contains(listed, "o3-legacy") {
		t.Errorf("GetModelsForProvider appended %q, which no allow rule admits: got %v", "o3-legacy", listed)
	}
}
