package lib

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// datasheetOverrideTestConfig boots a catalog on sqlite with a local
// datasheet listing gpt-4o at a 128000 context window.
func datasheetOverrideTestConfig(t *testing.T, withStore bool) *Config {
	t.Helper()
	SetLogger(&testLogger{})
	ctx := context.Background()
	dir := t.TempDir()
	pricingPath := filepath.Join(dir, "pricing.json")
	paramsPath := filepath.Join(dir, "params.json")
	require.NoError(t, os.WriteFile(pricingPath, []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","context_length":128000,"input_cost_per_token":0.0000025}}`), 0o600))
	require.NoError(t, os.WriteFile(paramsPath, []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","max_output_tokens":16384}}`), 0o600))
	var cs configstore.ConfigStore
	if withStore {
		var err error
		cs, err = configstore.NewConfigStore(ctx, &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config:  &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")},
		}, bifrost.NewNoOpLogger())
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close(ctx) })
	}
	pricingURL, paramsURL := "file://"+pricingPath, "file://"+paramsPath
	disabled := int64(0)
	mc, err := modelcatalog.Init(ctx, &modelcatalog.Config{PricingURL: &pricingURL, ModelParametersURL: &paramsURL, MCPLibrarySyncInterval: &disabled}, cs, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mc.Cleanup() })
	return &Config{ConfigStore: cs, ModelCatalog: mc}
}

func gpt4oContextLength(config *Config) int {
	return *config.ModelCatalog.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength
}

func parseDatasheetConfig(t *testing.T, body string) *ConfigData {
	t.Helper()
	var cd ConfigData
	require.NoError(t, json.Unmarshal([]byte(body), &cd))
	return &cd
}

func TestGenerateDatasheetOverrideHash(t *testing.T) {
	a := DatasheetOverrideFileConfig{Provider: "openai", Model: "gpt-4o", Mode: "chat", PricingPatch: json.RawMessage(`{"a":1,"b":2}`)}
	b := a
	b.PricingPatch = json.RawMessage(`{ "b": 2, "a": 1 }`)
	c := a
	c.PricingPatch = json.RawMessage(`{"a":1,"b":3}`)
	hashA, err := generateDatasheetOverrideHash(a.row())
	require.NoError(t, err)
	hashB, err := generateDatasheetOverrideHash(b.row())
	require.NoError(t, err)
	hashC, err := generateDatasheetOverrideHash(c.row())
	require.NoError(t, err)
	assert.Equal(t, hashA, hashB, "key order and whitespace are not a change")
	assert.NotEqual(t, hashA, hashC)
}

// config.json entries apply at boot; a UI edit of the same target stands while
// the file entry is unchanged, and a changed file entry wins again.
func TestLoadDatasheetOverridesFromFile_HashGatedWithStore(t *testing.T) {
	ctx := context.Background()
	config := datasheetOverrideTestConfig(t, true)
	file := parseDatasheetConfig(t, `{"datasheet_overrides":[{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"context_length":64000}}]}`)

	loadDatasheetOverridesFromFile(ctx, config, file)
	assert.Equal(t, 64000, gpt4oContextLength(config))

	// A UI edit keeps the stored config hash (the handler carries it over).
	stored := config.ModelCatalog.FindDatasheetOverride("openai", "gpt-4o", "chat")
	require.NotNil(t, stored)
	uiEdit := *stored
	uiEdit.PricingPatch = json.RawMessage(`{"context_length":32000}`)
	require.NoError(t, config.ModelCatalog.SaveDatasheetOverride(ctx, &uiEdit))

	loadDatasheetOverridesFromFile(ctx, config, file)
	assert.Equal(t, 32000, gpt4oContextLength(config), "an unchanged file entry does not clobber the UI edit")

	changed := parseDatasheetConfig(t, `{"datasheet_overrides":[{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"context_length":100000}}]}`)
	loadDatasheetOverridesFromFile(ctx, config, changed)
	assert.Equal(t, 100000, gpt4oContextLength(config), "a changed file entry wins")
	rows, err := config.ConfigStore.GetDatasheetOverrides(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

// In config.json source-of-truth mode, stored overrides the file no longer
// lists are removed; in split mode they are kept.
func TestLoadDatasheetOverridesFromFile_SourceOfTruthPrunes(t *testing.T) {
	ctx := context.Background()
	config := datasheetOverrideTestConfig(t, true)
	loadDatasheetOverridesFromFile(ctx, config, parseDatasheetConfig(t,
		`{"datasheet_overrides":[{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"context_length":64000}}]}`))
	require.Equal(t, 64000, gpt4oContextLength(config))

	loadDatasheetOverridesFromFile(ctx, config, parseDatasheetConfig(t, `{"datasheet_overrides":[]}`))
	assert.Equal(t, 64000, gpt4oContextLength(config), "split mode keeps stored overrides")

	loadDatasheetOverridesFromFile(ctx, config, parseDatasheetConfig(t, `{"source_of_truth":"config.json","datasheet_overrides":[]}`))
	assert.Equal(t, 128000, gpt4oContextLength(config), "config.json mode removes overrides the file dropped")
}

// Without a config store the entries apply in memory; invalid ones are skipped.
func TestLoadDatasheetOverridesFromFile_NoStore(t *testing.T) {
	ctx := context.Background()
	config := datasheetOverrideTestConfig(t, false)
	loadDatasheetOverridesFromFile(ctx, config, parseDatasheetConfig(t, `{"datasheet_overrides":[
		{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"context_length":64000}},
		{"provider":"openai","model":"bad","pricing_patch":{"not_a_field":1}}
	]}`))
	assert.Equal(t, 64000, gpt4oContextLength(config))
}
