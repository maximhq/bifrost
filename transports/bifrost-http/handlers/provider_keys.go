package handlers

import (
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

	SendJSON(ctx, redactedKey)
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

	if mergedKey.GigaChatKeyConfig != nil {
		var credentials, user, password, accessToken *schemas.SecretVar
		var certFile, keyFile, caBundleFile string
		if oldRawKey.GigaChatKeyConfig != nil {
			credentials = oldRawKey.GigaChatKeyConfig.Credentials
			user = oldRawKey.GigaChatKeyConfig.User
			password = oldRawKey.GigaChatKeyConfig.Password
			accessToken = oldRawKey.GigaChatKeyConfig.AccessToken
			certFile = oldRawKey.GigaChatKeyConfig.CertFile
			keyFile = oldRawKey.GigaChatKeyConfig.KeyFile
			caBundleFile = oldRawKey.GigaChatKeyConfig.CABundleFile
		}
		for _, item := range []struct {
			incoming *schemas.SecretVar
			stored   *schemas.SecretVar
			field    string
		}{
			{mergedKey.GigaChatKeyConfig.Credentials, credentials, "gigachat_key_config.credentials"},
			{mergedKey.GigaChatKeyConfig.User, user, "gigachat_key_config.user"},
			{mergedKey.GigaChatKeyConfig.Password, password, "gigachat_key_config.password"},
			{mergedKey.GigaChatKeyConfig.AccessToken, accessToken, "gigachat_key_config.access_token"},
		} {
			if err := preserve(item.incoming, item.stored, item.field); err != nil {
				return schemas.Key{}, err
			}
		}
		for _, item := range []struct {
			incoming *string
			stored   string
			field    string
		}{
			{&mergedKey.GigaChatKeyConfig.CertFile, certFile, "gigachat_key_config.cert_file"},
			{&mergedKey.GigaChatKeyConfig.KeyFile, keyFile, "gigachat_key_config.key_file"},
			{&mergedKey.GigaChatKeyConfig.CABundleFile, caBundleFile, "gigachat_key_config.ca_bundle_file"},
		} {
			if *item.incoming != "<REDACTED>" {
				continue
			}
			if strings.TrimSpace(item.stored) == "" {
				return schemas.Key{}, fmt.Errorf("masked preview cannot be used for %s without a stored value", item.field)
			}
			*item.incoming = item.stored
		}
	}

	mergedKey.ConfigHash = oldRawKey.ConfigHash
	mergedKey.Status = oldRawKey.Status

	return mergedKey, nil
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
	case schemas.GigaChat:
		if key.GigaChatKeyConfig != nil {
			if err := key.GigaChatKeyConfig.Validate(); err != nil {
				return err
			}
		}
		if !key.Value.IsSet() && (key.GigaChatKeyConfig == nil || (!key.GigaChatKeyConfig.HasAuthMaterial() && !key.GigaChatKeyConfig.HasClientCertificateMaterial())) {
			return fmt.Errorf("gigachat key requires value access token, gigachat_key_config bearer auth material, or gigachat_key_config mTLS client certificate material")
		}
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
	}
	return nil
}
