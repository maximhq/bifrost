package modelcatalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initDatasheetOverrideCatalog boots a catalog on a shared sqlite store with
// local datasheet files and MCP library sync disabled.
func initDatasheetOverrideCatalog(t *testing.T, cs configstore.ConfigStore, dir string) *ModelCatalog {
	t.Helper()
	pricingURL := "file://" + filepath.Join(dir, "pricing.json")
	paramsURL := "file://" + filepath.Join(dir, "params.json")
	disabled := int64(0)
	mc, err := Init(context.Background(), &Config{
		PricingURL:             &pricingURL,
		ModelParametersURL:     &paramsURL,
		MCPLibrarySyncInterval: &disabled,
	}, cs, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mc.Cleanup() })
	return mc
}

// A saved override applies on the node that wrote it, fires the peer reload
// hook, survives a forced re-sync, and reaches a peer through ReloadFromDB.
func TestSaveDatasheetOverride_AppliesSurvivesSyncAndReachesPeers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pricing.json"), []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","context_length":128000,"input_cost_per_token":0.0000025,"output_cost_per_token":0.00001}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "params.json"), []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","max_output_tokens":16384}}`), 0o600))
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(ctx) })

	leader := initDatasheetOverrideCatalog(t, cs, dir)
	peer := initDatasheetOverrideCatalog(t, cs, dir)
	hookCalls := 0
	leader.SetAfterSyncHook(func(context.Context) { hookCalls++ })

	row := &configstoreTables.TableDatasheetOverride{
		Provider:     "openai",
		Model:        "gpt-4o",
		Mode:         "chat",
		PricingPatch: json.RawMessage(`{"context_length":64000}`),
		ParamsPatch:  json.RawMessage(`{"max_output_tokens":4096}`),
	}
	require.NoError(t, leader.SaveDatasheetOverride(ctx, row))
	require.NotEmpty(t, row.ID)
	assert.Equal(t, 1, hookCalls)
	assert.JSONEq(t, `{"pricing":{"context_length":128000},"params":{"max_output_tokens":16384}}`, string(row.Baseline))

	assert.Equal(t, 64000, *leader.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength)
	assert.Equal(t, 4096, *leader.GetModelCapabilities(schemas.OpenAI, "gpt-4o").MaxOutputTokens)

	require.NoError(t, leader.ForceReloadPricing(ctx))
	assert.Equal(t, 64000, *leader.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength, "a forced sync keeps the override")
	assert.Equal(t, 4096, *leader.GetModelCapabilities(schemas.OpenAI, "gpt-4o").MaxOutputTokens)

	assert.Equal(t, 128000, *peer.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength, "peer has not reloaded yet")
	require.NoError(t, peer.ReloadFromDB(ctx))
	assert.Equal(t, 64000, *peer.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength)

	require.NoError(t, leader.DeleteDatasheetOverride(ctx, row.ID))
	assert.Equal(t, 128000, *leader.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength)
	assert.Equal(t, 16384, *leader.GetModelCapabilities(schemas.OpenAI, "gpt-4o").MaxOutputTokens)
	assert.Equal(t, 3, hookCalls, "save, forced sync and delete each ask peers to reload")
}

func TestSaveDatasheetOverride_RejectsInvalidPatch(t *testing.T) {
	err := ValidateDatasheetOverride(&configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "gpt-4o", PricingPatch: json.RawMessage(`{"not_a_field":1}`),
	})
	require.ErrorContains(t, err, `unknown field "not_a_field"`)
}
