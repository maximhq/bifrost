package handlers

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strings"

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

// complexitySemanticRearmer is implemented by the governance plugin. Kept as a
// local interface so these handlers do not take a dependency on the plugin
// package for one notification.
type complexitySemanticRearmer interface {
	RearmComplexitySemanticClassifier(provider schemas.ModelProvider)
}

// rearmComplexitySemanticClassifier tells the complexity router that this
// provider's configuration changed. Its semantic classifier only ever starts
// warmup on a write to the complexity configuration, so without this a
// classifier that failed because the provider could not serve — every key
// disabled, say — stays failed after the provider is fixed, and the operator
// has no way to restart it that does not involve editing a configuration that
// was already correct.
//
// Deliberately unconditional and cheap on this side: the classifier ignores
// providers it does not embed through, and ignores changes while it is healthy.
func (h *ProviderHandler) rearmComplexitySemanticClassifier(provider schemas.ModelProvider) {
	basePlugins := h.inMemoryStore.BasePlugins.Load()
	if basePlugins == nil {
		return
	}
	for _, plugin := range *basePlugins {
		if rearmer, ok := plugin.(complexitySemanticRearmer); ok {
			rearmer.RearmComplexitySemanticClassifier(provider)
			return
		}
	}
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
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
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

	if err := validateProviderKeyURL(baseProvider, key); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := key.BlacklistedModels.Validate(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid blacklisted_models: %v", err))
		return
	}

	if err := key.Aliases.Validate(baseProvider); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid aliases: %v", err))
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
			SendError(ctx, fasthttp.StatusConflict, "API key names must be unique across providers. Choose a different name")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create provider key: %v", err))
		return
	}

	if providerConfig.CustomProviderConfig == nil || !providerConfig.CustomProviderConfig.IsKeyLess {
		if err := h.modelsManager.OnKeyAdded(ctx, provider, key); err != nil {
			logger.Warn("Catalog refresh failed for provider %s after key create: %v", provider, err)
		}
	}
	h.rearmComplexitySemanticClassifier(provider)

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
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
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

	updateKey.ID = keyID
	mergedKey, err := h.mergeUpdatedKey(*oldRawKey, updateKey)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

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

	if err := mergedKey.Aliases.Validate(baseProvider); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid aliases: %v", err))
		return
	}

	if err := validateProviderKeyURL(baseProvider, mergedKey); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := h.inMemoryStore.UpdateProviderKey(ctx, provider, keyID, mergedKey); err != nil {
		logger.Warn("Failed to update key %s for provider %s: %v", keyID, provider, err)
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		if errors.Is(err, lib.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "API key names must be unique across providers. Choose a different name")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update provider key: %v", err))
		return
	}

	if providerConfig.CustomProviderConfig == nil || !providerConfig.CustomProviderConfig.IsKeyLess {
		if err := h.modelsManager.OnKeyUpdated(ctx, provider, mergedKey); err != nil {
			logger.Warn("Catalog refresh failed for provider %s after key update: %v", provider, err)
		}
	}
	h.rearmComplexitySemanticClassifier(provider)

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

	if err := h.modelsManager.OnKeyDeleted(ctx, provider, keyID); err != nil {
		logger.Warn("Catalog refresh failed for provider %s after key delete: %v", provider, err)
	}
	h.rearmComplexitySemanticClassifier(provider)

	SendJSON(ctx, redactedKey)
}

// refreshProviderModels handles POST /api/providers/{provider}/refresh-models.
// Re-runs list-models across every enabled key of the provider and returns the
// provider's keys with their freshly resolved discovery status.
func (h *ProviderHandler) refreshProviderModels(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	if _, err := h.inMemoryStore.GetProviderConfigRaw(provider); err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
		return
	}

	if err := h.modelsManager.RefreshLiveModelsForAllKeys(ctx, provider); err != nil {
		if errors.Is(err, ErrRefreshInProgress) {
			SendError(ctx, fasthttp.StatusConflict, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to refresh models: %v", err))
		return
	}

	// Read back after the refresh so the caller sees the statuses this pass
	// produced rather than the ones it started from.
	keys, err := h.inMemoryStore.GetProviderKeysRedacted(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider keys: %v", err))
		return
	}

	SendJSON(ctx, ListProviderKeysResponse{Keys: keys, Total: len(keys)})
}

// refreshProviderKeyModels handles
// POST /api/providers/{provider}/keys/{key_id}/refresh-models. Re-runs
// list-models for one key and returns it with its freshly resolved status.
func (h *ProviderHandler) refreshProviderKeyModels(ctx *fasthttp.RequestCtx) {
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

	if _, err := h.inMemoryStore.GetProviderKeyRedacted(provider, keyID); err != nil {
		if errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider key not found: %v", err))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider key: %v", err))
		return
	}

	if err := h.modelsManager.RefreshLiveModelsForKey(ctx, provider, keyID); err != nil {
		if errors.Is(err, ErrRefreshInProgress) {
			SendError(ctx, fasthttp.StatusConflict, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to refresh models: %v", err))
		return
	}

	refreshedKey, err := h.inMemoryStore.GetProviderKeyRedacted(provider, keyID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider key: %v", err))
		return
	}

	SendJSON(ctx, refreshedKey)
}

// mergeUpdatedKey merges an updated key with the old raw version, preserving
// stored values for masked placeholders. A placeholder without a stored
// counterpart is rejected so it can never reach persistence.
func (h *ProviderHandler) mergeUpdatedKey(oldRawKey, updateKey schemas.Key) (schemas.Key, error) {
	mergedKey := updateKey
	preserve := func(incoming, stored *schemas.SecretVar, field string) error {
		if !incoming.IsMaskedPlaceholder() {
			return nil
		}
		if stored == nil || !stored.IsSet() {
			return fmt.Errorf("masked preview cannot be used for %s without a stored value", field)
		}
		*incoming = *stored
		return nil
	}

	if err := preserve(&mergedKey.Value, &oldRawKey.Value, "value"); err != nil {
		return schemas.Key{}, err
	}

	if mergedKey.AzureKeyConfig != nil {
		var endpoint, clientID, clientSecret, tenantID *schemas.SecretVar
		if oldRawKey.AzureKeyConfig != nil {
			endpoint = &oldRawKey.AzureKeyConfig.Endpoint
			clientID = oldRawKey.AzureKeyConfig.ClientID
			clientSecret = oldRawKey.AzureKeyConfig.ClientSecret
			tenantID = oldRawKey.AzureKeyConfig.TenantID
		}
		for _, item := range []struct {
			incoming *schemas.SecretVar
			stored   *schemas.SecretVar
			field    string
		}{
			{&mergedKey.AzureKeyConfig.Endpoint, endpoint, "azure_key_config.endpoint"},
			{mergedKey.AzureKeyConfig.ClientID, clientID, "azure_key_config.client_id"},
			{mergedKey.AzureKeyConfig.ClientSecret, clientSecret, "azure_key_config.client_secret"},
			{mergedKey.AzureKeyConfig.TenantID, tenantID, "azure_key_config.tenant_id"},
		} {
			if err := preserve(item.incoming, item.stored, item.field); err != nil {
				return schemas.Key{}, err
			}
		}
	}

	if mergedKey.VertexKeyConfig != nil {
		var projectID, projectNumber, region, authCredentials *schemas.SecretVar
		if oldRawKey.VertexKeyConfig != nil {
			projectID = &oldRawKey.VertexKeyConfig.ProjectID
			projectNumber = &oldRawKey.VertexKeyConfig.ProjectNumber
			region = &oldRawKey.VertexKeyConfig.Region
			authCredentials = &oldRawKey.VertexKeyConfig.AuthCredentials
		}
		for _, item := range []struct {
			incoming *schemas.SecretVar
			stored   *schemas.SecretVar
			field    string
		}{
			{&mergedKey.VertexKeyConfig.ProjectID, projectID, "vertex_key_config.project_id"},
			{&mergedKey.VertexKeyConfig.ProjectNumber, projectNumber, "vertex_key_config.project_number"},
			{&mergedKey.VertexKeyConfig.Region, region, "vertex_key_config.region"},
			{&mergedKey.VertexKeyConfig.AuthCredentials, authCredentials, "vertex_key_config.auth_credentials"},
		} {
			if err := preserve(item.incoming, item.stored, item.field); err != nil {
				return schemas.Key{}, err
			}
		}
	}

	if mergedKey.BedrockKeyConfig != nil {
		var accessKey, secretKey, sessionToken, region, arn, roleARN, externalID, sessionName, batchRoleARN *schemas.SecretVar
		if oldRawKey.BedrockKeyConfig != nil {
			accessKey = &oldRawKey.BedrockKeyConfig.AccessKey
			secretKey = &oldRawKey.BedrockKeyConfig.SecretKey
			sessionToken = oldRawKey.BedrockKeyConfig.SessionToken
			region = oldRawKey.BedrockKeyConfig.Region
			arn = oldRawKey.BedrockKeyConfig.ARN
			roleARN = oldRawKey.BedrockKeyConfig.RoleARN
			externalID = oldRawKey.BedrockKeyConfig.ExternalID
			sessionName = oldRawKey.BedrockKeyConfig.RoleSessionName
			batchRoleARN = oldRawKey.BedrockKeyConfig.BatchRoleARN
		}
		for _, item := range []struct {
			incoming *schemas.SecretVar
			stored   *schemas.SecretVar
			field    string
		}{
			{&mergedKey.BedrockKeyConfig.AccessKey, accessKey, "bedrock_key_config.access_key"},
			{&mergedKey.BedrockKeyConfig.SecretKey, secretKey, "bedrock_key_config.secret_key"},
			{mergedKey.BedrockKeyConfig.SessionToken, sessionToken, "bedrock_key_config.session_token"},
			{mergedKey.BedrockKeyConfig.Region, region, "bedrock_key_config.region"},
			{mergedKey.BedrockKeyConfig.ARN, arn, "bedrock_key_config.arn"},
			{mergedKey.BedrockKeyConfig.RoleARN, roleARN, "bedrock_key_config.role_arn"},
			{mergedKey.BedrockKeyConfig.ExternalID, externalID, "bedrock_key_config.external_id"},
			{mergedKey.BedrockKeyConfig.RoleSessionName, sessionName, "bedrock_key_config.session_name"},
			{mergedKey.BedrockKeyConfig.BatchRoleARN, batchRoleARN, "bedrock_key_config.batch_role_arn"},
		} {
			if err := preserve(item.incoming, item.stored, item.field); err != nil {
				return schemas.Key{}, err
			}
		}
	}

	if mergedKey.BedrockMantleKeyConfig != nil {
		var accessKey, secretKey, sessionToken, region, roleARN, externalID, sessionName *schemas.SecretVar
		if oldRawKey.BedrockMantleKeyConfig != nil {
			accessKey = &oldRawKey.BedrockMantleKeyConfig.AccessKey
			secretKey = &oldRawKey.BedrockMantleKeyConfig.SecretKey
			sessionToken = oldRawKey.BedrockMantleKeyConfig.SessionToken
			region = oldRawKey.BedrockMantleKeyConfig.Region
			roleARN = oldRawKey.BedrockMantleKeyConfig.RoleARN
			externalID = oldRawKey.BedrockMantleKeyConfig.ExternalID
			sessionName = oldRawKey.BedrockMantleKeyConfig.RoleSessionName
		}
		for _, item := range []struct {
			incoming *schemas.SecretVar
			stored   *schemas.SecretVar
			field    string
		}{
			{&mergedKey.BedrockMantleKeyConfig.AccessKey, accessKey, "bedrock_mantle_key_config.access_key"},
			{&mergedKey.BedrockMantleKeyConfig.SecretKey, secretKey, "bedrock_mantle_key_config.secret_key"},
			{mergedKey.BedrockMantleKeyConfig.SessionToken, sessionToken, "bedrock_mantle_key_config.session_token"},
			{mergedKey.BedrockMantleKeyConfig.Region, region, "bedrock_mantle_key_config.region"},
			{mergedKey.BedrockMantleKeyConfig.RoleARN, roleARN, "bedrock_mantle_key_config.role_arn"},
			{mergedKey.BedrockMantleKeyConfig.ExternalID, externalID, "bedrock_mantle_key_config.external_id"},
			{mergedKey.BedrockMantleKeyConfig.RoleSessionName, sessionName, "bedrock_mantle_key_config.session_name"},
		} {
			if err := preserve(item.incoming, item.stored, item.field); err != nil {
				return schemas.Key{}, err
			}
		}
	}

	if mergedKey.VLLMKeyConfig != nil {
		var stored *schemas.SecretVar
		if oldRawKey.VLLMKeyConfig != nil {
			stored = &oldRawKey.VLLMKeyConfig.URL
		}
		if err := preserve(&mergedKey.VLLMKeyConfig.URL, stored, "vllm_key_config.url"); err != nil {
			return schemas.Key{}, err
		}
	}

	// ReplicateKeyConfig has no sensitive fields — pass through as-is
	if updateKey.ReplicateKeyConfig == nil && oldRawKey.ReplicateKeyConfig != nil {
		mergedKey.ReplicateKeyConfig = oldRawKey.ReplicateKeyConfig
	}

	if mergedKey.OllamaKeyConfig != nil {
		var stored *schemas.SecretVar
		if oldRawKey.OllamaKeyConfig != nil {
			stored = &oldRawKey.OllamaKeyConfig.URL
		}
		if err := preserve(&mergedKey.OllamaKeyConfig.URL, stored, "ollama_key_config.url"); err != nil {
			return schemas.Key{}, err
		}
	}

	if mergedKey.SGLKeyConfig != nil {
		var stored *schemas.SecretVar
		if oldRawKey.SGLKeyConfig != nil {
			stored = &oldRawKey.SGLKeyConfig.URL
		}
		if err := preserve(&mergedKey.SGLKeyConfig.URL, stored, "sgl_key_config.url"); err != nil {
			return schemas.Key{}, err
		}
	}

	if mergedKey.DatabricksKeyConfig != nil {
		var workspaceURL, clientID, clientSecret *schemas.SecretVar
		if oldRawKey.DatabricksKeyConfig != nil {
			workspaceURL = &oldRawKey.DatabricksKeyConfig.WorkspaceURL
			clientID = oldRawKey.DatabricksKeyConfig.ClientID
			clientSecret = oldRawKey.DatabricksKeyConfig.ClientSecret
		}
		for _, item := range []struct {
			incoming *schemas.SecretVar
			stored   *schemas.SecretVar
			field    string
		}{
			{&mergedKey.DatabricksKeyConfig.WorkspaceURL, workspaceURL, "databricks_key_config.workspace_url"},
			{mergedKey.DatabricksKeyConfig.ClientID, clientID, "databricks_key_config.client_id"},
			{mergedKey.DatabricksKeyConfig.ClientSecret, clientSecret, "databricks_key_config.client_secret"},
		} {
			if err := preserve(item.incoming, item.stored, item.field); err != nil {
				return schemas.Key{}, err
			}
		}
	}
	if mergedKey.GithubCopilotKeyConfig != nil {
		var old *schemas.GithubCopilotKeyConfig
		if oldRawKey.GithubCopilotKeyConfig != nil {
			old = oldRawKey.GithubCopilotKeyConfig
		}
		for _, field := range []struct {
			name    string
			updated *schemas.SecretVar
			stored  *schemas.SecretVar
		}{
			{"app_id", &mergedKey.GithubCopilotKeyConfig.AppID, fieldOrNil(old, func(c *schemas.GithubCopilotKeyConfig) *schemas.SecretVar { return &c.AppID })},
			{"installation_id", &mergedKey.GithubCopilotKeyConfig.InstallationID, fieldOrNil(old, func(c *schemas.GithubCopilotKeyConfig) *schemas.SecretVar { return &c.InstallationID })},
			{"repository_id", &mergedKey.GithubCopilotKeyConfig.RepositoryID, fieldOrNil(old, func(c *schemas.GithubCopilotKeyConfig) *schemas.SecretVar { return &c.RepositoryID })},
			{"private_key", &mergedKey.GithubCopilotKeyConfig.PrivateKey, fieldOrNil(old, func(c *schemas.GithubCopilotKeyConfig) *schemas.SecretVar { return &c.PrivateKey })},
			{"github_domain", &mergedKey.GithubCopilotKeyConfig.GithubDomain, fieldOrNil(old, func(c *schemas.GithubCopilotKeyConfig) *schemas.SecretVar { return &c.GithubDomain })},
		} {
			if err := preserve(field.updated, field.stored, "github_copilot_key_config."+field.name); err != nil {

				return schemas.Key{}, err
			}
		}
	}

	mergedKey.ConfigHash = oldRawKey.ConfigHash
	mergedKey.Status = oldRawKey.Status

	// An update that omits name must not clear it: schemas.Key.Name is a plain
	// string, so an omitted field and an explicit "" are indistinguishable here.
	// Treating that as "clear the name" persists an empty name, and config_keys.name
	// carries a global unique index — the first cleared key claims "" and every
	// later update that also omits name then fails with a confusing 409.
	if mergedKey.Name == "" {
		mergedKey.Name = oldRawKey.Name
	}

	return mergedKey, nil
}

// validateGithubCopilotLiteral applies a format check to a credential field, but only when
// the operator supplied a literal. A secret reference resolves elsewhere and at another
// time, so its shape cannot be judged here.
func validateGithubCopilotLiteral(name string, value *schemas.SecretVar, ok func(string) bool) error {
	if value.IsFromSecret() {
		return nil
	}
	if !ok(strings.TrimSpace(value.GetValue())) {
		return fmt.Errorf("github_copilot_key_config.%s is not valid for GitHub Copilot keys", name)
	}
	return nil
}

// isDigits reports whether s is a non-empty run of ASCII digits. GitHub installation and
// repository IDs are numeric, and the installation ID is interpolated into an
// api.github.com path, so a non-numeric value is a path-injection shape as well as a typo.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isPEMPrivateKey reports whether s is an RSA private key Bifrost can actually sign an App
// JWT with. GitHub issues App keys as PKCS#1 ("RSA PRIVATE KEY"); openssl can convert them
// to PKCS#8 ("PRIVATE KEY").
//
// The DER payload is parsed, not just the envelope. A envelope-only check accepts an EC key,
// a passphrase-encrypted key and a block of random base64 alike, none of which can produce
// an RS256 signature, so each would pass setup and fail at the first inference call.
//
// Literal backslash-n is repaired first, because that is how a PEM most often arrives from a
// JSON config or an environment variable.
func isPEMPrivateKey(s string) bool {
	if !strings.Contains(s, "\n") && strings.Contains(s, `\n`) {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}

	block, rest := pem.Decode([]byte(strings.TrimSpace(s)))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return false
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		_, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		return err == nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return false
		}
		// PKCS#8 is a container: it can hold an EC or Ed25519 key just as happily.
		_, isRSA := key.(*rsa.PrivateKey)
		return isRSA
	default:
		return false
	}
}

// fieldOrNil selects a field from a possibly-nil stored config, so a masked update against
// a key that never had this section behaves the same as one against a missing field.
func fieldOrNil(config *schemas.GithubCopilotKeyConfig, pick func(*schemas.GithubCopilotKeyConfig) *schemas.SecretVar) *schemas.SecretVar {
	if config == nil {
		return nil
	}
	return pick(config)
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

// validateProviderKeyURL checks that provider keys carry the nested fields
// config.schema.json marks as required, so a create or merge can never persist
// a key missing them (a masked update against a stored key lacking the section
// would otherwise only surface later as a downstream 500).
func validateProviderKeyURL(provider schemas.ModelProvider, key schemas.Key) error {
	switch provider {
	case schemas.Ollama:
		if key.OllamaKeyConfig == nil || !key.OllamaKeyConfig.URL.IsSet() {
			return fmt.Errorf("ollama_key_config.url is required for Ollama keys")
		}
	case schemas.SGL:
		if key.SGLKeyConfig == nil || !key.SGLKeyConfig.URL.IsSet() {
			return fmt.Errorf("sgl_key_config.url is required for SGL keys")
		}
	case schemas.GithubCopilot:
		// A Copilot API token in value is a valid alternative to the GitHub App bundle. But a
		// supplied App config is checked either way: a half-filled block sitting behind a
		// token persists silently and only surfaces later, when the token expires or is
		// removed and the key falls back to credentials that were never valid.
		if key.GithubCopilotKeyConfig == nil {
			if key.Value.IsSet() {
				return nil
			}
			return fmt.Errorf("github_copilot_key_config is required for GitHub Copilot keys without a value")
		}
		for _, field := range []struct {
			name  string
			value schemas.SecretVar
		}{
			{"app_id", key.GithubCopilotKeyConfig.AppID},
			{"installation_id", key.GithubCopilotKeyConfig.InstallationID},
			{"repository_id", key.GithubCopilotKeyConfig.RepositoryID},
			{"private_key", key.GithubCopilotKeyConfig.PrivateKey},
		} {
			if !field.value.IsSet() {
				return fmt.Errorf("github_copilot_key_config.%s is required for GitHub Copilot keys", field.name)
			}
		}
		// Check the shape of literal values too, not just their presence. A non-numeric
		// installation ID or a mangled PEM otherwise persists and fails at the first
		// inference call, where it reads as a runtime fault rather than a typo.
		//
		// Environment and vault references are exempt: their values are not resolvable
		// here, so checking them would reject every legitimate headless configuration.
		if err := validateGithubCopilotLiteral("installation_id", &key.GithubCopilotKeyConfig.InstallationID, isDigits); err != nil {
			return err
		}
		if err := validateGithubCopilotLiteral("repository_id", &key.GithubCopilotKeyConfig.RepositoryID, isDigits); err != nil {
			return err
		}
		if err := validateGithubCopilotLiteral("private_key", &key.GithubCopilotKeyConfig.PrivateKey, isPEMPrivateKey); err != nil {
			return err
		}
		// app_id is deliberately not digit-checked. GitHub documents the App JWT issuer as
		// "the client ID or application ID" and says "use of the client ID is recommended",
		// and client IDs look like Iv1.b507a08c87ecfe98. A digits-only rule here would reject
		// the configuration GitHub itself recommends.
		// https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
	case schemas.Azure:
		if key.AzureKeyConfig == nil || !key.AzureKeyConfig.Endpoint.IsSet() {
			return fmt.Errorf("azure_key_config.endpoint is required for Azure keys")
		}
	case schemas.Bedrock:
		if key.BedrockKeyConfig == nil || key.BedrockKeyConfig.Region == nil || !key.BedrockKeyConfig.Region.IsSet() {
			return fmt.Errorf("bedrock_key_config.region is required for Bedrock keys")
		}
	case schemas.BedrockMantle:
		if key.BedrockMantleKeyConfig == nil || key.BedrockMantleKeyConfig.Region == nil || !key.BedrockMantleKeyConfig.Region.IsSet() {
			return fmt.Errorf("bedrock_mantle_key_config.region is required for Bedrock Mantle keys")
		}
	case schemas.VLLM:
		if key.VLLMKeyConfig == nil || !key.VLLMKeyConfig.URL.IsSet() {
			return fmt.Errorf("vllm_key_config.url is required for VLLM keys")
		}
		if key.VLLMKeyConfig.ModelName == "" {
			return fmt.Errorf("vllm_key_config.model_name is required for VLLM keys")
		}
	case schemas.Databricks:
		if key.DatabricksKeyConfig == nil || !key.DatabricksKeyConfig.WorkspaceURL.IsSet() {
			return fmt.Errorf("databricks_key_config.workspace_url is required for Databricks keys")
		}
		switch key.DatabricksKeyConfig.APIFormat {
		case "", schemas.DatabricksAPIFormatAuto, schemas.DatabricksAPIFormatModelServing, schemas.DatabricksAPIFormatAIGateway:
		default:
			return fmt.Errorf("databricks_key_config.api_format must be one of auto, model_serving, ai_gateway")
		}
		// Either a personal access token (the key value) or a full OAuth M2M service
		// principal pair is required; a half-configured service principal cannot authenticate.
		hasClientID := key.DatabricksKeyConfig.ClientID != nil && key.DatabricksKeyConfig.ClientID.IsSet()
		hasClientSecret := key.DatabricksKeyConfig.ClientSecret != nil && key.DatabricksKeyConfig.ClientSecret.IsSet()
		if !key.Value.IsSet() && !(hasClientID && hasClientSecret) {
			return fmt.Errorf("databricks keys require either a personal access token as the key value, or both databricks_key_config.client_id and databricks_key_config.client_secret for OAuth M2M")
		}
		if hasClientID != hasClientSecret {
			return fmt.Errorf("databricks_key_config.client_id and databricks_key_config.client_secret must be set together")
		}
	}
	return nil
}
