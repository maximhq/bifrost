package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/stretchr/testify/require"
)

func TestAddProviderKeysToSemanticCacheConfig_DirectOnlyMode(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": 1,
			"ttl":       "5m",
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.NoError(t, err)

	configMap, ok := pluginConfig.Config.(map[string]interface{})
	require.True(t, ok)
	_, hasKeys := configMap["keys"]
	require.False(t, hasKeys, "direct-only mode should not inject provider keys")
}

func TestAddProviderKeysToSemanticCacheConfig_InjectsProviderKeys(t *testing.T) {
	config := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {
				Keys: []schemas.Key{
					{
						Name:   "openai-key",
						Value:  *schemas.NewEnvVar("sk-test"),
						Weight: 1,
					},
				},
			},
		},
	}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":        "openai",
			"embedding_model": "text-embedding-3-small",
			"dimension":       1536,
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.NoError(t, err)

	configMap, ok := pluginConfig.Config.(map[string]interface{})
	require.True(t, ok)
	keys, ok := configMap["keys"].([]schemas.Key)
	require.True(t, ok, "provider-backed mode should inject provider keys")
	require.Len(t, keys, 1)
	require.Equal(t, "openai-key", keys[0].Name)
	require.Equal(t, "openai", configMap["provider"])
}

func TestAddProviderKeysToSemanticCacheConfig_SemanticModeMissingProvider(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": 1536,
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'provider' for semantic mode")
}

func TestAddProviderKeysToSemanticCacheConfig_ProviderBackedModeMissingDimension(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":        "openai",
			"embedding_model": "text-embedding-3-small",
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'dimension' for provider-backed semantic mode")
}

func TestAddProviderKeysToSemanticCacheConfig_ProviderBackedModeDimensionOne(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":        "openai",
			"embedding_model": "text-embedding-3-small",
			"dimension":       1,
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'dimension' > 1")
}

func TestAddProviderKeysToSemanticCacheConfig_ProviderBackedModeMissingEmbeddingModel(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":  "openai",
			"dimension": 1536,
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'embedding_model'")
}

func TestAddProviderKeysToSemanticCacheConfig_InvalidDimensionZero(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": 0,
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'dimension' must be >= 1")
}

func TestAddProviderKeysToSemanticCacheConfig_InvalidDimensionNegative(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": -1,
		},
	}

	err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'dimension' must be >= 1")
}
