package lib

import (
	"fmt"
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
						Value:  *schemas.NewSecretVar("sk-test"),
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

// A plugin update merges its config over the stored one, so a field is cleared by sending null.
// A null field is the same as an absent one.
func TestValidateSemanticCacheConfig_NullFieldsCountAsAbsent(t *testing.T) {
	config := &Config{}
	pluginConfig := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":                       nil,
			"embedding_model":                nil,
			"threshold":                      nil,
			"default_cache_key":              nil,
			"conversation_history_threshold": nil,
			"cache_by_model":                 nil,
			"disable_identity_scoping":       nil,
			"dimension":                      1,
			"ttl":                            "10m",
		},
	}

	require.NoError(t, config.ValidateSemanticCacheConfig(pluginConfig))

	semanticWithoutDimension := &schemas.PluginConfig{
		Name: semanticcache.PluginName,
		Config: map[string]interface{}{
			"provider":        "openai",
			"embedding_model": "text-embedding-3-small",
			"dimension":       nil,
		},
	}
	err := config.ValidateSemanticCacheConfig(semanticWithoutDimension)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'dimension' for provider-backed semantic mode")
}

func TestValidateSemanticCacheConfig_InvalidTTL(t *testing.T) {
	cases := []struct {
		ttl  any
		want string
	}{
		{ttl: "abc", want: "failed to parse TTL duration string 'abc'"},
		{ttl: "-5s", want: "TTL must be non-negative"},
		{ttl: -5, want: "TTL must be non-negative"},
		{ttl: true, want: "unsupported TTL value"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.ttl), func(t *testing.T) {
			config := &Config{}
			pluginConfig := &schemas.PluginConfig{
				Name:   semanticcache.PluginName,
				Config: map[string]interface{}{"dimension": 1, "ttl": tc.ttl},
			}

			err := config.ValidateSemanticCacheConfig(pluginConfig)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}
