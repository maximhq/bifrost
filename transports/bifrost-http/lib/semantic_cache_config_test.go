package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/stretchr/testify/require"
)

func TestValidateSemanticCacheConfig_DirectOnlyMode(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": 1,
			"ttl":       "5m",
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.NoError(t, err)

	configMap, ok := pluginConfig.Config.(map[string]interface{})
	require.True(t, ok)
	_, hasKeys := configMap["keys"]
	require.False(t, hasKeys, "direct-only mode should not inject provider keys")
}

func TestValidateSemanticCacheConfig_DirectOnlyModeRemovesStaleProviderBackedFields(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension":       1,
			"keys":            []schemas.Key{{Name: "stale-key"}},
			"embedding_model": "text-embedding-3-small",
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.NoError(t, err)

	configMap, ok := pluginConfig.Config.(map[string]interface{})
	require.True(t, ok)
	_, hasEmbeddingModel := configMap["embedding_model"]
	require.False(t, hasEmbeddingModel, "direct-only mode should remove stale embedding_model")
}

func TestValidateSemanticCacheConfig_ProviderBackedModeValidationPasses(t *testing.T) {
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

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.NoError(t, err)

	configMap, ok := pluginConfig.Config.(map[string]interface{})
	require.True(t, ok)
	_, hasKeys := configMap["keys"]
	require.False(t, hasKeys, "keys are inherited from global client; they must not be injected into the plugin config")
	require.Equal(t, "openai", configMap["provider"])
}

func TestValidateSemanticCacheConfig_SemanticModeMissingProvider(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": 1536,
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'provider' for semantic mode")
}

func TestValidateSemanticCacheConfig_ProviderBackedModeMissingDimension(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":        "openai",
			"embedding_model": "text-embedding-3-small",
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'dimension' for provider-backed semantic mode")
}

func TestValidateSemanticCacheConfig_ProviderBackedModeDimensionOne(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":        "openai",
			"embedding_model": "text-embedding-3-small",
			"dimension":       1,
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'dimension' > 1")
}

func TestValidateSemanticCacheConfig_ProviderBackedModeMissingEmbeddingModel(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":  "openai",
			"dimension": 1536,
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'embedding_model'")
}

func TestValidateSemanticCacheConfig_InvalidDimensionZero(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": 0,
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'dimension' must be >= 1")
}

func TestValidateSemanticCacheConfig_InvalidDimensionNegative(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"dimension": -1,
		},
	}

	err := config.ValidateSemanticCacheConfig(pluginConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'dimension' must be >= 1")
}
