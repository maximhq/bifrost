package handlers

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ListProviderKeysResponse represents the response for listing keys for a provider.
type ListProviderKeysResponse struct {
	Keys  []schemas.Key `json:"keys"`
	Total int           `json:"total"`
}

func (h *ProviderHandler) listProviderKeys(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	keys, err := h.inMemoryStore.GetProviderKeysRedacted(provider)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider keys: %v", err))
		return
	}

	SendJSON(ctx, ListProviderKeysResponse{Keys: keys, Total: len(keys)})
}

func (h *ProviderHandler) getProviderKey(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	keyID, err := getKeyIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	key, err := h.inMemoryStore.GetProviderKeyRedacted(provider, keyID)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider key: %v", err))
		return
	}

	SendJSON(ctx, key)
}

func (h *ProviderHandler) createProviderKey(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	var key schemas.Key
	if err := sonic.Unmarshal(ctx.PostBody(), &key); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	providerConfig, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
		return
	}

	if providerConfig.CustomProviderConfig != nil && providerConfig.CustomProviderConfig.IsKeyLess {
		SendError(ctx, fasthttp.StatusBadRequest, "Cannot add keys to a keyless provider")
		return
	}

	baseProvider := provider
	if providerConfig.CustomProviderConfig != nil && providerConfig.CustomProviderConfig.BaseProviderType != "" {
		baseProvider = providerConfig.CustomProviderConfig.BaseProviderType
	}

	if !bifrost.CanProviderKeyValueBeEmpty(baseProvider) && key.Value.GetValue() == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Key value must not be empty")
		return
	}

	if err := validateProviderKeyURL(provider, key); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := key.BlacklistedModels.Validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid blacklisted_models: %v", err))
		return
	}

	if key.ID == "" {
		key.ID = uuid.NewString()
	}
	if key.Enabled == nil {
		key.Enabled = bifrost.Ptr(true)
	}

	if err := h.inMemoryStore.AddProviderKey(ctx, provider, key); err != nil {
		logger.Warn("Failed to create key for provider %s: %v", provider, err)
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		if errors.Is(err, lib.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create provider key: %v", err))
		return
	}

	if err := h.attemptModelDiscovery(ctx, provider, providerConfig.CustomProviderConfig); err != nil {
		logger.Warn("Model discovery failed for provider %s after key create: %v", provider, err)
	}

	redactedKey, err := h.inMemoryStore.GetProviderKeyRedacted(provider, key.ID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get created provider key: %v", err))
		return
	}

	SendJSON(ctx, redactedKey)
}

func (h *ProviderHandler) updateProviderKey(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	keyID, err := getKeyIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	var updateKey schemas.Key
	if err := sonic.Unmarshal(ctx.PostBody(), &updateKey); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	providerConfig, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
		return
	}

	if providerConfig.CustomProviderConfig != nil && providerConfig.CustomProviderConfig.IsKeyLess {
		SendError(ctx, fasthttp.StatusBadRequest, "Cannot update keys on a keyless provider")
		return
	}

	oldRawKey, err := h.inMemoryStore.GetProviderKeyRaw(provider, keyID)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider key: %v", err))
		return
	}

	oldRedactedKey, err := h.inMemoryStore.GetProviderKeyRedacted(provider, keyID)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider key: %v", err))
		return
	}

	updateKey.ID = keyID
	mergedKey := h.mergeUpdatedKey(*oldRawKey, *oldRedactedKey, updateKey)

	baseProvider := provider
	if providerConfig.CustomProviderConfig != nil && providerConfig.CustomProviderConfig.BaseProviderType != "" {
		baseProvider = providerConfig.CustomProviderConfig.BaseProviderType
	}

	if !bifrost.CanProviderKeyValueBeEmpty(baseProvider) && mergedKey.Value.GetValue() == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Key value must not be empty")
		return
	}

	if err := mergedKey.BlacklistedModels.Validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid blacklisted_models: %v", err))
		return
	}

	if err := validateProviderKeyURL(provider, mergedKey); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := h.inMemoryStore.UpdateProviderKey(ctx, provider, keyID, mergedKey); err != nil {
		logger.Warn("Failed to update key %s for provider %s: %v", keyID, provider, err)
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update provider key: %v", err))
		return
	}

	if err := h.attemptModelDiscovery(ctx, provider, providerConfig.CustomProviderConfig); err != nil {
		logger.Warn("Model discovery failed for provider %s after key update: %v", provider, err)
	}

	redactedKey, err := h.inMemoryStore.GetProviderKeyRedacted(provider, keyID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get updated provider key: %v", err))
		return
	}

	SendJSON(ctx, redactedKey)
}

func (h *ProviderHandler) deleteProviderKey(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	keyID, err := getKeyIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	providerConfig, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
		return
	}

	if providerConfig.CustomProviderConfig != nil && providerConfig.CustomProviderConfig.IsKeyLess {
		SendError(ctx, fasthttp.StatusBadRequest, "Cannot delete keys on a keyless provider")
		return
	}

	redactedKey, err := h.inMemoryStore.GetProviderKeyRedacted(provider, keyID)
	if err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider key: %v", err))
		return
	}

	if err := h.inMemoryStore.RemoveProviderKey(ctx, provider, keyID); err != nil {
		logger.Warn("Failed to delete key %s for provider %s: %v", keyID, provider, err)
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to delete provider key: %v", err))
		return
	}

	if err := h.attemptModelDiscovery(ctx, provider, providerConfig.CustomProviderConfig); err != nil {
		logger.Warn("Model discovery failed for provider %s after key delete: %v", provider, err)
	}

	SendJSON(ctx, redactedKey)
}

// mergeUpdatedKey merges an updated key with the old raw/redacted versions,
// preserving real values for fields that were sent back in redacted form.
func (h *ProviderHandler) mergeUpdatedKey(oldRawKey, oldRedactedKey, updateKey schemas.Key) schemas.Key {
	mergedKey := updateKey

	if updateKey.Value.IsRedacted() && updateKey.Value.Equals(&oldRedactedKey.Value) {
		mergedKey.Value = oldRawKey.Value
	}

	if updateKey.AzureKeyConfig != nil && oldRedactedKey.AzureKeyConfig != nil && oldRawKey.AzureKeyConfig != nil {
		if updateKey.AzureKeyConfig.Endpoint.IsRedacted() &&
			updateKey.AzureKeyConfig.Endpoint.Equals(&oldRedactedKey.AzureKeyConfig.Endpoint) {
			mergedKey.AzureKeyConfig.Endpoint = oldRawKey.AzureKeyConfig.Endpoint
		}
		if updateKey.AzureKeyConfig.APIVersion != nil &&
			oldRedactedKey.AzureKeyConfig.APIVersion != nil &&
			oldRawKey.AzureKeyConfig != nil &&
			updateKey.AzureKeyConfig.APIVersion.IsRedacted() &&
			updateKey.AzureKeyConfig.APIVersion.Equals(oldRedactedKey.AzureKeyConfig.APIVersion) {
			mergedKey.AzureKeyConfig.APIVersion = oldRawKey.AzureKeyConfig.APIVersion
		}
		if updateKey.AzureKeyConfig.ClientID != nil &&
			oldRedactedKey.AzureKeyConfig.ClientID != nil &&
			oldRawKey.AzureKeyConfig != nil &&
			updateKey.AzureKeyConfig.ClientID.IsRedacted() &&
			updateKey.AzureKeyConfig.ClientID.Equals(oldRedactedKey.AzureKeyConfig.ClientID) {
			mergedKey.AzureKeyConfig.ClientID = oldRawKey.AzureKeyConfig.ClientID
		}
		if updateKey.AzureKeyConfig.ClientSecret != nil &&
			oldRedactedKey.AzureKeyConfig.ClientSecret != nil &&
			oldRawKey.AzureKeyConfig != nil &&
			updateKey.AzureKeyConfig.ClientSecret.IsRedacted() &&
			updateKey.AzureKeyConfig.ClientSecret.Equals(oldRedactedKey.AzureKeyConfig.ClientSecret) {
			mergedKey.AzureKeyConfig.ClientSecret = oldRawKey.AzureKeyConfig.ClientSecret
		}
		if updateKey.AzureKeyConfig.TenantID != nil &&
			oldRedactedKey.AzureKeyConfig.TenantID != nil &&
			oldRawKey.AzureKeyConfig != nil &&
			updateKey.AzureKeyConfig.TenantID.IsRedacted() &&
			updateKey.AzureKeyConfig.TenantID.Equals(oldRedactedKey.AzureKeyConfig.TenantID) {
			mergedKey.AzureKeyConfig.TenantID = oldRawKey.AzureKeyConfig.TenantID
		}
	}

	if updateKey.VertexKeyConfig != nil && oldRedactedKey.VertexKeyConfig != nil && oldRawKey.VertexKeyConfig != nil {
		if updateKey.VertexKeyConfig.ProjectID.IsRedacted() &&
			updateKey.VertexKeyConfig.ProjectID.Equals(&oldRedactedKey.VertexKeyConfig.ProjectID) {
			mergedKey.VertexKeyConfig.ProjectID = oldRawKey.VertexKeyConfig.ProjectID
		}
		if updateKey.VertexKeyConfig.ProjectNumber.IsRedacted() &&
			updateKey.VertexKeyConfig.ProjectNumber.Equals(&oldRedactedKey.VertexKeyConfig.ProjectNumber) {
			mergedKey.VertexKeyConfig.ProjectNumber = oldRawKey.VertexKeyConfig.ProjectNumber
		}
		if updateKey.VertexKeyConfig.Region.IsRedacted() &&
			updateKey.VertexKeyConfig.Region.Equals(&oldRedactedKey.VertexKeyConfig.Region) {
			mergedKey.VertexKeyConfig.Region = oldRawKey.VertexKeyConfig.Region
		}
		if updateKey.VertexKeyConfig.AuthCredentials.IsRedacted() &&
			updateKey.VertexKeyConfig.AuthCredentials.Equals(&oldRedactedKey.VertexKeyConfig.AuthCredentials) {
			mergedKey.VertexKeyConfig.AuthCredentials = oldRawKey.VertexKeyConfig.AuthCredentials
		}
	}

	if updateKey.BedrockKeyConfig != nil && oldRedactedKey.BedrockKeyConfig != nil && oldRawKey.BedrockKeyConfig != nil {
		if updateKey.BedrockKeyConfig.AccessKey.IsRedacted() &&
			updateKey.BedrockKeyConfig.AccessKey.Equals(&oldRedactedKey.BedrockKeyConfig.AccessKey) {
			mergedKey.BedrockKeyConfig.AccessKey = oldRawKey.BedrockKeyConfig.AccessKey
		}
		if updateKey.BedrockKeyConfig.SecretKey.IsRedacted() &&
			updateKey.BedrockKeyConfig.SecretKey.Equals(&oldRedactedKey.BedrockKeyConfig.SecretKey) {
			mergedKey.BedrockKeyConfig.SecretKey = oldRawKey.BedrockKeyConfig.SecretKey
		}
		if updateKey.BedrockKeyConfig.SessionToken != nil &&
			oldRedactedKey.BedrockKeyConfig.SessionToken != nil &&
			oldRawKey.BedrockKeyConfig != nil &&
			updateKey.BedrockKeyConfig.SessionToken.IsRedacted() &&
			updateKey.BedrockKeyConfig.SessionToken.Equals(oldRedactedKey.BedrockKeyConfig.SessionToken) {
			mergedKey.BedrockKeyConfig.SessionToken = oldRawKey.BedrockKeyConfig.SessionToken
		}
		if updateKey.BedrockKeyConfig.Region != nil &&
			oldRedactedKey.BedrockKeyConfig.Region != nil &&
			oldRawKey.BedrockKeyConfig != nil &&
			updateKey.BedrockKeyConfig.Region.IsRedacted() &&
			updateKey.BedrockKeyConfig.Region.Equals(oldRedactedKey.BedrockKeyConfig.Region) {
			mergedKey.BedrockKeyConfig.Region = oldRawKey.BedrockKeyConfig.Region
		}
		if updateKey.BedrockKeyConfig.ARN != nil &&
			oldRedactedKey.BedrockKeyConfig.ARN != nil &&
			oldRawKey.BedrockKeyConfig != nil &&
			updateKey.BedrockKeyConfig.ARN.IsRedacted() &&
			updateKey.BedrockKeyConfig.ARN.Equals(oldRedactedKey.BedrockKeyConfig.ARN) {
			mergedKey.BedrockKeyConfig.ARN = oldRawKey.BedrockKeyConfig.ARN
		}
		if updateKey.BedrockKeyConfig.RoleARN != nil &&
			oldRedactedKey.BedrockKeyConfig.RoleARN != nil &&
			oldRawKey.BedrockKeyConfig != nil &&
			updateKey.BedrockKeyConfig.RoleARN.IsRedacted() &&
			updateKey.BedrockKeyConfig.RoleARN.Equals(oldRedactedKey.BedrockKeyConfig.RoleARN) {
			mergedKey.BedrockKeyConfig.RoleARN = oldRawKey.BedrockKeyConfig.RoleARN
		}
		if updateKey.BedrockKeyConfig.ExternalID != nil &&
			oldRedactedKey.BedrockKeyConfig.ExternalID != nil &&
			oldRawKey.BedrockKeyConfig != nil &&
			updateKey.BedrockKeyConfig.ExternalID.IsRedacted() &&
			updateKey.BedrockKeyConfig.ExternalID.Equals(oldRedactedKey.BedrockKeyConfig.ExternalID) {
			mergedKey.BedrockKeyConfig.ExternalID = oldRawKey.BedrockKeyConfig.ExternalID
		}
		if updateKey.BedrockKeyConfig.RoleSessionName != nil &&
			oldRedactedKey.BedrockKeyConfig.RoleSessionName != nil &&
			oldRawKey.BedrockKeyConfig != nil &&
			updateKey.BedrockKeyConfig.RoleSessionName.IsRedacted() &&
			updateKey.BedrockKeyConfig.RoleSessionName.Equals(oldRedactedKey.BedrockKeyConfig.RoleSessionName) {
			mergedKey.BedrockKeyConfig.RoleSessionName = oldRawKey.BedrockKeyConfig.RoleSessionName
		}
	}

	if updateKey.VLLMKeyConfig != nil && oldRedactedKey.VLLMKeyConfig != nil && oldRawKey.VLLMKeyConfig != nil {
		if updateKey.VLLMKeyConfig.URL.IsRedacted() &&
			updateKey.VLLMKeyConfig.URL.Equals(&oldRedactedKey.VLLMKeyConfig.URL) {
			mergedKey.VLLMKeyConfig.URL = oldRawKey.VLLMKeyConfig.URL
		}
	}

	if updateKey.OllamaKeyConfig != nil && oldRedactedKey.OllamaKeyConfig != nil && oldRawKey.OllamaKeyConfig != nil {
		if updateKey.OllamaKeyConfig.URL.IsRedacted() &&
			updateKey.OllamaKeyConfig.URL.Equals(&oldRedactedKey.OllamaKeyConfig.URL) {
			mergedKey.OllamaKeyConfig.URL = oldRawKey.OllamaKeyConfig.URL
		}
	}

	if updateKey.SGLKeyConfig != nil && oldRedactedKey.SGLKeyConfig != nil && oldRawKey.SGLKeyConfig != nil {
		if updateKey.SGLKeyConfig.URL.IsRedacted() &&
			updateKey.SGLKeyConfig.URL.Equals(&oldRedactedKey.SGLKeyConfig.URL) {
			mergedKey.SGLKeyConfig.URL = oldRawKey.SGLKeyConfig.URL
		}
	}

	mergedKey.ConfigHash = oldRawKey.ConfigHash
	mergedKey.Status = oldRawKey.Status

	return mergedKey
}

func getKeyIDFromCtx(ctx *fasthttp.RequestCtx) (string, error) {
	keyValue := ctx.UserValue("key_id")
	if keyValue == nil {
		return "", fmt.Errorf("missing key_id parameter")
	}

	keyID, ok := keyValue.(string)
	if !ok || keyID == "" {
		return "", fmt.Errorf("invalid key_id parameter")
	}

	decoded, err := url.PathUnescape(keyID)
	if err != nil {
		return "", fmt.Errorf("invalid key_id parameter encoding: %v", err)
	}

	return decoded, nil
}

// validateProviderKeyURL checks that Ollama/SGL keys have a server URL configured.
func validateProviderKeyURL(provider schemas.ModelProvider, key schemas.Key) error {
	switch provider {
	case schemas.Ollama:
		if key.OllamaKeyConfig == nil || !key.OllamaKeyConfig.URL.IsDefined() {
			return fmt.Errorf("ollama_key_config.url is required for Ollama keys")
		}
	case schemas.SGL:
		if key.SGLKeyConfig == nil || !key.SGLKeyConfig.URL.IsDefined() {
			return fmt.Errorf("sgl_key_config.url is required for SGL keys")
		}
	}
	return nil
}
