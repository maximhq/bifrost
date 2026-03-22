package configstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateProvidersConfig_OpenRouterKeyConfigRoundTrip(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		schemas.OpenRouter: {
			Keys: []schemas.Key{{
				ID:     "openrouter-key-1",
				Name:   "openrouter-primary",
				Value:  *schemas.NewEnvVar("sk-openrouter"),
				Weight: 1.0,
				OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
					Provider: json.RawMessage(`{"order":["openai","anthropic"],"allow_fallbacks":false}`),
				},
			}},
		},
	}

	require.NoError(t, store.UpdateProvidersConfig(ctx, providers))

	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	require.Contains(t, result, schemas.OpenRouter)
	require.Len(t, result[schemas.OpenRouter].Keys, 1)
	require.NotNil(t, result[schemas.OpenRouter].Keys[0].OpenRouterKeyConfig)
	assert.JSONEq(
		t,
		`{"order":["openai","anthropic"],"allow_fallbacks":false}`,
		string(result[schemas.OpenRouter].Keys[0].OpenRouterKeyConfig.Provider),
	)
}

func TestGenerateKeyHash_OpenRouterKeyConfigAffectsHash(t *testing.T) {
	baseKey := schemas.Key{
		ID:     "openrouter-key-1",
		Name:   "openrouter-primary",
		Value:  *schemas.NewEnvVar("sk-openrouter"),
		Weight: 1.0,
	}

	hashWithoutProvider, err := GenerateKeyHash(baseKey)
	require.NoError(t, err)

	keyWithProvider := baseKey
	keyWithProvider.OpenRouterKeyConfig = &schemas.OpenRouterKeyConfig{
		Provider: json.RawMessage(`{"order":["openai"]}`),
	}
	hashWithProvider, err := GenerateKeyHash(keyWithProvider)
	require.NoError(t, err)

	assert.NotEqual(t, hashWithoutProvider, hashWithProvider)

	keyWithDifferentProvider := baseKey
	keyWithDifferentProvider.OpenRouterKeyConfig = &schemas.OpenRouterKeyConfig{
		Provider: json.RawMessage(`{"order":["anthropic"]}`),
	}
	hashWithDifferentProvider, err := GenerateKeyHash(keyWithDifferentProvider)
	require.NoError(t, err)

	assert.NotEqual(t, hashWithProvider, hashWithDifferentProvider)
}

func TestGenerateKeyHash_OpenRouterKeyConfigIgnoresWhitespaceAndKeyOrder(t *testing.T) {
	baseKey := schemas.Key{
		ID:     "openrouter-key-1",
		Name:   "openrouter-primary",
		Value:  *schemas.NewEnvVar("sk-openrouter"),
		Weight: 1.0,
	}

	keyA := baseKey
	keyA.OpenRouterKeyConfig = &schemas.OpenRouterKeyConfig{
		Provider: json.RawMessage(`{
			"order": ["openai", "anthropic"],
			"allow_fallbacks": false,
			"require_parameters": true
		}`),
	}

	keyB := baseKey
	keyB.OpenRouterKeyConfig = &schemas.OpenRouterKeyConfig{
		Provider: json.RawMessage(`{"require_parameters":true,"allow_fallbacks":false,"order":["openai","anthropic"]}`),
	}

	hashA, err := GenerateKeyHash(keyA)
	require.NoError(t, err)

	hashB, err := GenerateKeyHash(keyB)
	require.NoError(t, err)

	assert.Equal(t, hashA, hashB)
}

func TestUpdateProvidersConfig_OpenRouterKeyConfigCanBeCleared(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	initialProviders := map[schemas.ModelProvider]ProviderConfig{
		schemas.OpenRouter: {
			Keys: []schemas.Key{{
				ID:     "openrouter-key-1",
				Name:   "openrouter-primary",
				Value:  *schemas.NewEnvVar("sk-openrouter"),
				Weight: 1.0,
				OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
					Provider: json.RawMessage(`{"order":["openai","anthropic"],"allow_fallbacks":false}`),
				},
			}},
		},
	}

	require.NoError(t, store.UpdateProvidersConfig(ctx, initialProviders))

	updatedProviders := map[schemas.ModelProvider]ProviderConfig{
		schemas.OpenRouter: {
			Keys: []schemas.Key{{
				ID:     "openrouter-key-1",
				Name:   "openrouter-primary",
				Value:  *schemas.NewEnvVar("sk-openrouter"),
				Weight: 1.0,
			}},
		},
	}

	require.NoError(t, store.UpdateProvidersConfig(ctx, updatedProviders))

	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	require.Contains(t, result, schemas.OpenRouter)
	require.Len(t, result[schemas.OpenRouter].Keys, 1)
	assert.Nil(t, result[schemas.OpenRouter].Keys[0].OpenRouterKeyConfig)
}

func TestUpdateProvidersConfig_OpenRouterKeyConfigRejectsNonObjectJSON(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		schemas.OpenRouter: {
			Keys: []schemas.Key{{
				ID:     "openrouter-key-1",
				Name:   "openrouter-primary",
				Value:  *schemas.NewEnvVar("sk-openrouter"),
				Weight: 1.0,
				OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
					Provider: json.RawMessage(`[]`),
				},
			}},
		},
	}

	err := store.UpdateProvidersConfig(ctx, providers)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider must be a valid JSON object")
}

func TestCreateVirtualKeyProviderConfig_WithOpenRouterKeyRetainsOpenRouterKeyConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		schemas.OpenRouter: {
			Keys: []schemas.Key{{
				ID:     "openrouter-key-1",
				Name:   "openrouter-primary",
				Value:  *schemas.NewEnvVar("sk-openrouter"),
				Weight: 1.0,
				OpenRouterKeyConfig: &schemas.OpenRouterKeyConfig{
					Provider: json.RawMessage(`{"order":["openai"],"allow_fallbacks":false}`),
				},
			}},
		},
	}

	require.NoError(t, store.UpdateProvidersConfig(ctx, providers))

	vk := &tables.TableVirtualKey{
		ID:       "vk-openrouter-keys",
		Name:     "VK OpenRouter Keys",
		Value:    "vk-openrouter-value",
		IsActive: true,
	}
	require.NoError(t, store.CreateVirtualKey(ctx, vk))

	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: vk.ID,
		Provider:     string(schemas.OpenRouter),
		Weight:       &weight,
		Keys: []tables.TableKey{{
			Name: "openrouter-primary",
		}},
	}
	require.NoError(t, store.CreateVirtualKeyProviderConfig(ctx, pc))

	var configWithKeys tables.TableVirtualKeyProviderConfig
	err := store.db.Preload("Keys").First(&configWithKeys, "id = ?", pc.ID).Error
	require.NoError(t, err)
	require.Len(t, configWithKeys.Keys, 1)
	require.NotNil(t, configWithKeys.Keys[0].OpenRouterKeyConfig)
	assert.JSONEq(
		t,
		`{"order":["openai"],"allow_fallbacks":false}`,
		string(configWithKeys.Keys[0].OpenRouterKeyConfig.Provider),
	)
	assert.Nil(t, configWithKeys.Keys[0].OpenRouterProviderJSON)
}
