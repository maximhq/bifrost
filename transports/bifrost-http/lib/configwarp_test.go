package lib

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// warpFile decodes a warp section the way LoadConfig does.
func warpFile(t *testing.T, section string) *WarpFileConfig {
	t.Helper()
	var file WarpFileConfig
	require.NoError(t, json.Unmarshal([]byte(section), &file))
	return &file
}

// warpDeclaration names a setting from every part of the settings page, topics included. Warp is
// left disabled so the save needs no vector store.
const warpDeclaration = `{
	"provider": "openai",
	"model": "gpt-4o",
	"api_key_id": "warp-key",
	"additional_models": [{"provider": "anthropic", "model": "claude-sonnet-5-5"}],
	"max_iterations": 12,
	"request_timeout_seconds": 300,
	"history_retention_days": 14,
	"system_prompt_suffix": "prod keys start with prod-",
	"temperature": 0,
	"reasoning_effort": "low",
	"embedding_provider": "openai",
	"embedding_model": "text-embedding-3-small",
	"embedding_dimension": 1536,
	"semantic_search_threshold": 0.6,
	"semantic_search_limit": 15,
	"topic_discovery_min_requests": 250
}`

// bootWarp boots on the file in tempDir, applies its warp section the way the server does, and
// returns the stored row. dashboardEdit, when set, is then saved through the service the way the
// settings page saves it.
func bootWarp(t *testing.T, tempDir string, dashboardEdit string) *configstoreTables.TableWarpConfig {
	t.Helper()
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config.Close(ctx)
	service := warp.NewService(config.ConfigStore)
	config.ApplyWarpFileConfig(ctx, service)
	store, ok := config.ConfigStore.(configstore.WarpStore)
	require.True(t, ok)
	row, err := store.GetWarpConfig(ctx)
	require.NoError(t, err)
	if dashboardEdit != "" {
		var input warp.ConfigInput
		require.NoError(t, json.Unmarshal([]byte(dashboardEdit), &input))
		_, err := service.SaveConfig(ctx, &input)
		require.NoError(t, err)
	}
	return row
}

// TestApplyWarpFileConfig_AppliesTheDeclaration: every settings-page field declared in config.json,
// the topic discovery threshold included, is stored as a dashboard save would store it.
func TestApplyWarpFileConfig_AppliesTheDeclaration(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Warp = warpFile(t, warpDeclaration)
	createConfigFile(t, tempDir, configData)

	row := bootWarp(t, tempDir, "")
	require.NotNil(t, row)
	assert.False(t, row.Enabled)
	assert.Equal(t, "openai", row.Provider)
	assert.Equal(t, "gpt-4o", row.Model)
	assert.Equal(t, "warp-key", row.APIKeyID)
	require.NotNil(t, row.AdditionalModels)
	assert.JSONEq(t, `[{"provider":"anthropic","model":"claude-sonnet-5-5"}]`, *row.AdditionalModels)
	assert.Equal(t, 12, row.MaxIterations)
	assert.Equal(t, 300, row.RequestTimeoutSeconds)
	assert.Equal(t, 14, row.HistoryRetentionDays)
	require.NotNil(t, row.SystemPromptSuffix)
	assert.Equal(t, "prod keys start with prod-", *row.SystemPromptSuffix)
	require.NotNil(t, row.Temperature, "an explicit 0 is a temperature, not an omission")
	assert.Equal(t, 0.0, *row.Temperature)
	assert.Equal(t, "low", row.ReasoningEffort)
	assert.Equal(t, "openai", row.EmbeddingProvider)
	assert.Equal(t, "text-embedding-3-small", row.EmbeddingModel)
	assert.Equal(t, 1536, row.EmbeddingDimension)
	assert.Equal(t, "BifrostWarpLogs", row.LogVectorStoreNamespace)
	assert.Equal(t, 0.6, row.SemanticSearchThreshold)
	assert.Equal(t, 15, row.SemanticSearchLimit)
	assert.Equal(t, 250, row.TopicDiscoveryMinRequests)
}

// TestApplyWarpFileConfig_SplitKeepsADashboardEditUntilTheFileChanges: the hash lives in its own row,
// so a settings-page save does not wipe it, and an unchanged file leaves the edit alone.
func TestApplyWarpFileConfig_SplitKeepsADashboardEditUntilTheFileChanges(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Warp = warpFile(t, `{"provider": "openai", "model": "gpt-4o", "topic_discovery_min_requests": 250}`)
	createConfigFile(t, tempDir, configData)

	bootWarp(t, tempDir, `{"provider": "openai", "model": "gpt-4o-mini", "topic_discovery_min_requests": 500}`)

	row := bootWarp(t, tempDir, "")
	assert.Equal(t, "gpt-4o-mini", row.Model, "an unchanged file keeps the dashboard edit")
	assert.Equal(t, 500, row.TopicDiscoveryMinRequests)

	configData.Warp = warpFile(t, `{"provider": "openai", "model": "gpt-4o", "topic_discovery_min_requests": 1000}`)
	createConfigFile(t, tempDir, configData)
	row = bootWarp(t, tempDir, "")
	assert.Equal(t, "gpt-4o", row.Model, "a changed file is applied")
	assert.Equal(t, 1000, row.TopicDiscoveryMinRequests)
}

// TestApplyWarpFileConfig_SourceOfTruthAppliesTheFileEveryBoot: under source_of_truth "config.json"
// a dashboard edit lasts only until the next boot.
func TestApplyWarpFileConfig_SourceOfTruthAppliesTheFileEveryBoot(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.SourceOfTruth = SourceOfTruthConfigJSON
	configData.Warp = warpFile(t, `{"provider": "openai", "model": "gpt-4o", "topic_discovery_min_requests": 250}`)
	createConfigFile(t, tempDir, configData)

	bootWarp(t, tempDir, `{"provider": "openai", "model": "gpt-4o-mini", "topic_discovery_min_requests": 500}`)

	row := bootWarp(t, tempDir, "")
	assert.Equal(t, "gpt-4o", row.Model)
	assert.Equal(t, 250, row.TopicDiscoveryMinRequests)
}

// TestApplyWarpFileConfig_OmittedSearchAndTopicFieldsKeepTheStoredValues: the file follows the
// settings API, where an omitted embedding, search or topic field keeps what is stored. Without that
// a file that never mentions the namespace would move a deployment off its custom one.
func TestApplyWarpFileConfig_OmittedSearchAndTopicFieldsKeepTheStoredValues(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	createConfigFile(t, tempDir, configData)

	bootWarp(t, tempDir, `{
		"provider": "openai", "model": "gpt-4o",
		"embedding_provider": "openai", "embedding_model": "text-embedding-3-small", "embedding_dimension": 1536,
		"log_vector_store_namespace": "CustomWarpLogs", "semantic_search_limit": 20, "topic_discovery_min_requests": 750
	}`)

	configData.Warp = warpFile(t, `{"provider": "anthropic", "model": "claude-sonnet-5-5"}`)
	createConfigFile(t, tempDir, configData)
	row := bootWarp(t, tempDir, "")
	assert.Equal(t, "anthropic", row.Provider)
	assert.Equal(t, "claude-sonnet-5-5", row.Model)
	assert.Equal(t, "CustomWarpLogs", row.LogVectorStoreNamespace)
	assert.Equal(t, "text-embedding-3-small", row.EmbeddingModel)
	assert.Equal(t, 20, row.SemanticSearchLimit)
	assert.Equal(t, 750, row.TopicDiscoveryMinRequests)
}

// TestApplyWarpFileConfig_AbsentOrInvalidLeavesTheStoredConfig: no warp section, or one the settings
// page would refuse, leaves what the dashboard stored, and a refused one is retried next boot.
func TestApplyWarpFileConfig_AbsentOrInvalidLeavesTheStoredConfig(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	createConfigFile(t, tempDir, configData)

	row := bootWarp(t, tempDir, `{"provider": "openai", "model": "gpt-4o-mini", "topic_discovery_min_requests": 500}`)
	assert.Nil(t, row, "nothing is stored without a warp section")

	row = bootWarp(t, tempDir, "")
	assert.Equal(t, "gpt-4o-mini", row.Model, "an absent section leaves the stored config")

	for name, section := range map[string]string{
		"topic threshold below the minimum": `{"provider": "openai", "model": "gpt-4o", "topic_discovery_min_requests": 5}`,
		"one iteration":                     `{"provider": "openai", "model": "gpt-4o", "max_iterations": 1}`,
		"enabled without a vector store":    `{"enabled": true, "provider": "openai", "model": "gpt-4o", "embedding_provider": "openai", "embedding_model": "text-embedding-3-small", "embedding_dimension": 1536}`,
	} {
		configData.Warp = warpFile(t, section)
		createConfigFile(t, tempDir, configData)
		row = bootWarp(t, tempDir, "")
		assert.Equal(t, "gpt-4o-mini", row.Model, name)
		assert.Equal(t, 500, row.TopicDiscoveryMinRequests, name)
	}

	config, err := LoadConfig(context.Background(), tempDir)
	require.NoError(t, err)
	defer config.Close(context.Background())
	hash, err := config.ConfigStore.GetConfig(context.Background(), configstoreTables.ConfigWarpHashKey)
	if err == nil {
		assert.Empty(t, hash.Value, "a refused declaration records no hash, so the next boot tries it again")
	} else {
		assert.ErrorIs(t, err, configstore.ErrNotFound)
	}
}

// TestLoadConfig_IgnoresAMalformedWarpSection: a section that cannot be a Warp config write is
// warned about and skipped at apply time, like one the settings page would refuse. The rest of
// config.json still loads, the stored Warp settings are kept, and no hash is recorded.
func TestLoadConfig_IgnoresAMalformedWarpSection(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	createConfigFile(t, tempDir, configData)
	row := bootWarp(t, tempDir, `{"provider": "openai", "model": "gpt-4o-mini", "topic_discovery_min_requests": 500}`)
	assert.Nil(t, row, "nothing is stored without a warp section")

	raw, err := os.ReadFile(filepath.Join(tempDir, "config.json"))
	require.NoError(t, err)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["warp"] = json.RawMessage(`{"topic_discovery_min_requests": "lots"}`)
	raw, err = json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "config.json"), raw, 0644))

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err, "a malformed warp section does not stop the rest of config.json loading")
	defer config.Close(ctx)
	assert.Equal(t, 10, config.ClientConfig.InitialPoolSize, "the other sections still load")

	config.ApplyWarpFileConfig(ctx, warp.NewService(config.ConfigStore))
	store, ok := config.ConfigStore.(configstore.WarpStore)
	require.True(t, ok)
	row, err = store.GetWarpConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o-mini", row.Model, "the stored settings are kept")
	assert.Equal(t, 500, row.TopicDiscoveryMinRequests)
	hash, err := config.ConfigStore.GetConfig(ctx, configstoreTables.ConfigWarpHashKey)
	if err == nil {
		assert.Empty(t, hash.Value, "an ignored section records no hash")
	} else {
		assert.ErrorIs(t, err, configstore.ErrNotFound)
	}
}

// warp.vector_store is boot-time infrastructure, not a setting the page saves: it is taken out
// of the section before the settings are applied or hashed, so declaring it does not re-apply
// the rest of the section over dashboard edits, and it is written back on marshal.
func TestWarpFileConfig_VectorStoreIsTakenOutOfTheSettings(t *testing.T) {
	plain := warpFile(t, `{"max_iterations": 12}`)
	withStore := warpFile(t, `{"max_iterations": 12, "vector_store": {"enabled": true, "type": "chromem"}}`)

	require.NoError(t, withStore.invalid)
	require.NoError(t, withStore.vectorStoreInvalid)
	require.NotNil(t, withStore.vectorStore)
	assert.Equal(t, vectorstore.VectorStoreTypeChromem, withStore.vectorStore.Type)
	assert.Equal(t, string(plain.raw), string(withStore.raw), "the settings and their sync hash are unchanged by the store")

	out, err := json.Marshal(withStore)
	require.NoError(t, err)
	assert.JSONEq(t, `{"max_iterations": 12, "vector_store": {"enabled": true, "type": "chromem"}}`, string(out))
}

// A section that only gives Warp its store declares no settings. Its empty remainder must not be
// applied: a save of {} stores enabled=false and switches Warp off.
func TestWarpFileConfig_OnlyAVectorStoreAppliesNoSettings(t *testing.T) {
	file := warpFile(t, `{"vector_store": {"enabled": true, "type": "chromem"}}`)
	require.NoError(t, file.invalid)
	require.NotNil(t, file.vectorStore)
	assert.Empty(t, file.raw)

	config := &Config{warpFileConfig: file}
	saver := &countingWarpSaver{}
	config.ApplyWarpFileConfig(context.Background(), saver)
	assert.Zero(t, saver.saves, "nothing is saved for a section with no settings")
}

type countingWarpSaver struct{ saves int }

func (s *countingWarpSaver) SaveConfig(context.Context, *warp.ConfigInput) (warp.ConfigView, error) {
	s.saves++
	return warp.ConfigView{}, nil
}

// initWarpVectorStore never fails the boot. A store that is invalid or cannot be built leaves
// Warp unavailable (dedicated and failed, no store) instead of falling back to the shared one; a
// disabled declaration, or one at the shared store's own location, leaves Warp on the shared store.
func TestInitWarpVectorStore(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	load := func(t *testing.T, shared *vectorstore.Config, section string) *Config {
		t.Helper()
		config := &Config{}
		initWarpVectorStore(ctx, config, &ConfigData{VectorStoreConfig: shared, Warp: warpFile(t, section)})
		t.Cleanup(func() { config.Close(ctx) })
		return config
	}

	t.Run("connected", func(t *testing.T) {
		config := load(t, nil, `{"vector_store": {"enabled": true, "type": "chromem"}}`)
		require.NotNil(t, config.WarpVectorStore)
		source := config.WarpVectorStoreSource
		assert.True(t, source.Dedicated)
		assert.False(t, source.Failed)
		assert.Equal(t, "chromem", source.Type)
		assert.NotEmpty(t, source.Fingerprint)
	})
	t.Run("invalid declaration", func(t *testing.T) {
		config := load(t, nil, `{"vector_store": {"enabled": true, "type": "not-a-store"}}`)
		assert.Nil(t, config.WarpVectorStore)
		assert.Equal(t, warp.VectorStoreSource{Dedicated: true, Failed: true}, config.WarpVectorStoreSource)
	})
	t.Run("store cannot be built", func(t *testing.T) {
		config := load(t, nil, `{"vector_store": {"enabled": true, "type": "weaviate", "config": {"scheme": "http"}}}`)
		assert.Nil(t, config.WarpVectorStore)
		source := config.WarpVectorStoreSource
		assert.True(t, source.Dedicated)
		assert.True(t, source.Failed)
		assert.Equal(t, "weaviate", source.Type)
	})
	t.Run("disabled", func(t *testing.T) {
		config := load(t, nil, `{"vector_store": {"enabled": false, "type": "chromem"}}`)
		assert.Nil(t, config.WarpVectorStore)
		assert.Equal(t, warp.VectorStoreSource{}, config.WarpVectorStoreSource)
	})
	t.Run("same location as the shared store", func(t *testing.T) {
		shared := &vectorstore.Config{Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{Path: "/data/vectors"}}
		config := load(t, shared, `{"vector_store": {"enabled": true, "type": "chromem", "config": {"path": "/data/vectors"}}}`)
		assert.Nil(t, config.WarpVectorStore)
		assert.Equal(t, warp.VectorStoreSource{}, config.WarpVectorStoreSource)
	})
	t.Run("no declaration", func(t *testing.T) {
		config := load(t, nil, `{"max_iterations": 12}`)
		assert.Nil(t, config.WarpVectorStore)
		assert.Equal(t, warp.VectorStoreSource{}, config.WarpVectorStoreSource)
	})
}

// A config.json whose warp section declares a store passes the schema and boots with Warp on it.
func TestLoadConfig_WarpVectorStore(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.Warp = warpFile(t, `{"max_iterations": 12, "vector_store": {"enabled": true, "type": "chromem"}}`)
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config.Close(ctx)
	require.NotNil(t, config.WarpVectorStore)
	assert.True(t, config.WarpVectorStoreSource.Dedicated)
	assert.Nil(t, config.VectorStore, "the shared store is untouched")
}
