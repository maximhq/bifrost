// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all provider management functionality including CRUD operations.
package handlers

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ProviderHandler manages HTTP requests for provider operations
type ProviderHandler struct {
	store  *lib.ConfigStore
	client *bifrost.Bifrost
	logger schemas.Logger
}

// NewProviderHandler creates a new provider handler instance
func NewProviderHandler(store *lib.ConfigStore, client *bifrost.Bifrost, logger schemas.Logger) *ProviderHandler {
	return &ProviderHandler{
		store:  store,
		client: client,
		logger: logger,
	}
}

// AddProviderRequest represents the request body for adding a new provider
type AddProviderRequest struct {
	Provider                 schemas.ModelProvider             `json:"provider"`
	Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	MetaConfig               *map[string]interface{}           `json:"meta_config,omitempty"`                 // Provider-specific metadata
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`                // Proxy configuration
}

// UpdateProviderRequest represents the request body for updating a provider
type UpdateProviderRequest struct {
	Keys                     []schemas.Key                    `json:"keys"`                        // API keys for the provider
	NetworkConfig            schemas.NetworkConfig            `json:"network_config"`              // Network-related settings
	MetaConfig               *map[string]interface{}          `json:"meta_config,omitempty"`       // Provider-specific metadata
	ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"` // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig             `json:"proxy_config,omitempty"`      // Proxy configuration
}

// ProviderResponse represents the response for provider operations
type ProviderResponse struct {
	Name                     schemas.ModelProvider            `json:"name"`
	Keys                     []schemas.Key                    `json:"keys"`                        // API keys for the provider
	NetworkConfig            schemas.NetworkConfig            `json:"network_config"`              // Network-related settings
	MetaConfig               *schemas.MetaConfig              `json:"meta_config"`                 // Provider-specific metadata
	ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"` // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig             `json:"proxy_config"`                // Proxy configuration
}

// ListProvidersResponse represents the response for listing all providers
type ListProvidersResponse struct {
	Providers []ProviderResponse `json:"providers"`
	Total     int                `json:"total"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// RegisterRoutes registers all provider management routes
func (h *ProviderHandler) RegisterRoutes(r *router.Router) {
	// Provider CRUD operations
	r.GET("/api/providers", h.ListProviders)
	r.GET("/api/providers/{provider}", h.GetProvider)
	r.POST("/api/providers", h.AddProvider)
	r.PUT("/api/providers/{provider}", h.UpdateProvider)
	r.DELETE("/api/providers/{provider}", h.DeleteProvider)
}

// ListProviders handles GET /api/providers - List all providers
func (h *ProviderHandler) ListProviders(ctx *fasthttp.RequestCtx) {
	providers, err := h.store.GetAllProviders()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers: %v", err), h.logger)
		return
	}

	var providerResponses []ProviderResponse

	// Sort providers alphabetically
	sort.Slice(providers, func(i, j int) bool {
		return string(providers[i]) < string(providers[j])
	})

	for _, provider := range providers {
		config, err := h.store.GetProviderConfigRedacted(provider)
		if err != nil {
			h.logger.Warn(fmt.Sprintf("Failed to get config for provider %s: %v", provider, err))
			// Include provider even if config fetch fails
			providerResponses = append(providerResponses, ProviderResponse{
				Name: provider,
			})
			continue
		}

		providerResponses = append(providerResponses, h.getProviderResponseFromConfig(provider, *config))
	}

	response := ListProvidersResponse{
		Providers: providerResponses,
		Total:     len(providerResponses),
	}

	SendJSON(ctx, response, h.logger)
}

// GetProvider handles GET /api/providers/{provider} - Get specific provider
func (h *ProviderHandler) GetProvider(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err), h.logger)
		return
	}

	config, err := h.store.GetProviderConfigRedacted(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	response := h.getProviderResponseFromConfig(provider, *config)

	SendJSON(ctx, response, h.logger)
}

// AddProvider handles POST /api/providers - Add a new provider
func (h *ProviderHandler) AddProvider(ctx *fasthttp.RequestCtx) {
	var req AddProviderRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err), h.logger)
		return
	}

	// Validate provider
	if req.Provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Missing provider", h.logger)
		return
	}

	// Validate required keys
	if len(req.Keys) == 0 && req.Provider != schemas.Ollama && req.Provider != schemas.SGL {
		SendError(ctx, fasthttp.StatusBadRequest, "At least one API key is required", h.logger)
		return
	}

	if req.ConcurrencyAndBufferSize != nil {
		if req.ConcurrencyAndBufferSize.Concurrency == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be greater than 0", h.logger)
			return
		}
		if req.ConcurrencyAndBufferSize.BufferSize == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Buffer size must be greater than 0", h.logger)
			return
		}
	}

	// Check if provider already exists
	if _, err := h.store.GetProviderConfigRedacted(req.Provider); err == nil {
		SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("Provider %s already exists", req.Provider), h.logger)
		return
	}

	// Construct ProviderConfig from individual fields
	config := lib.ProviderConfig{
		Keys:                     req.Keys,
		NetworkConfig:            req.NetworkConfig,
		ConcurrencyAndBufferSize: req.ConcurrencyAndBufferSize,
	}

	// Handle meta config if provided
	if req.MetaConfig != nil && len(*req.MetaConfig) > 0 {
		// Convert to appropriate meta config type based on provider
		metaConfig, err := h.convertToProviderMetaConfig(req.Provider, *req.MetaConfig)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid meta config: %v", err), h.logger)
			return
		}
		config.MetaConfig = metaConfig
	}

	// Add provider to store (env vars will be processed by store)
	if err := h.store.AddProvider(req.Provider, config); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to add provider %s: %v", req.Provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add provider: %v", err), h.logger)
		return
	}

	if err := h.store.SaveConfig(); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to save configuration: %v", err), h.logger)
		return
	}

	h.logger.Info(fmt.Sprintf("Provider %s added successfully", req.Provider))

	response := h.getProviderResponseFromConfig(req.Provider, config)

	SendJSON(ctx, response, h.logger)
}

// UpdateProvider handles PUT /api/providers/{provider} - Update provider config
// NOTE: This endpoint expects ALL fields to be provided in the request body,
// including both edited and non-edited fields. Partial updates are not supported.
// The frontend should send the complete provider configuration.
func (h *ProviderHandler) UpdateProvider(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err), h.logger)
		return
	}

	var req UpdateProviderRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err), h.logger)
		return
	}

	// Get the raw config to access actual values for merging with redacted request values
	oldConfigRaw, err := h.store.GetProviderConfigRaw(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	oldConfigRedacted, err := h.store.GetProviderConfigRedacted(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	// Construct ProviderConfig from individual fields
	config := lib.ProviderConfig{
		Keys:                     oldConfigRaw.Keys,
		NetworkConfig:            oldConfigRaw.NetworkConfig,
		ConcurrencyAndBufferSize: oldConfigRaw.ConcurrencyAndBufferSize,
		ProxyConfig:              oldConfigRaw.ProxyConfig,
	}

	// Environment variable cleanup is now handled automatically by mergeKeys function

	var keysToAdd []schemas.Key
	var keysToUpdate []schemas.Key

	for _, key := range req.Keys {
		if !slices.ContainsFunc(oldConfigRaw.Keys, func(k schemas.Key) bool {
			return k.ID == key.ID
		}) {
			keysToAdd = append(keysToAdd, key)
		} else {
			keysToUpdate = append(keysToUpdate, key)
		}
	}

	var keysToDelete []schemas.Key
	for _, key := range oldConfigRaw.Keys {
		if !slices.ContainsFunc(req.Keys, func(k schemas.Key) bool {
			return k.ID == key.ID
		}) {
			keysToDelete = append(keysToDelete, key)
		}
	}

	keys, err := h.mergeKeys(provider, oldConfigRaw.Keys, oldConfigRedacted.Keys, keysToAdd, keysToDelete, keysToUpdate)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid keys: %v", err), h.logger)
		return
	}
	config.Keys = keys

	// Handle meta config if provided
	if req.MetaConfig != nil && len(*req.MetaConfig) > 0 {
		// Merge new meta config with old, preserving redacted values
		metaConfig, err := h.mergeMetaConfig(provider, oldConfigRaw.MetaConfig, *req.MetaConfig)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid meta config: %v", err), h.logger)
			return
		}
		config.MetaConfig = metaConfig
	}

	if req.ConcurrencyAndBufferSize.Concurrency == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be greater than 0", h.logger)
		return
	}
	if req.ConcurrencyAndBufferSize.BufferSize == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "Buffer size must be greater than 0", h.logger)
		return
	}

	if req.ConcurrencyAndBufferSize.Concurrency > req.ConcurrencyAndBufferSize.BufferSize {
		SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be less than or equal to buffer size", h.logger)
		return
	}

	config.ConcurrencyAndBufferSize = &req.ConcurrencyAndBufferSize
	config.NetworkConfig = &req.NetworkConfig
	config.ProxyConfig = req.ProxyConfig

	// Update provider config in store (env vars will be processed by store)
	if err := h.store.UpdateProviderConfig(provider, config); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to update provider %s: %v", provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update provider: %v", err), h.logger)
		return
	}

	if err := h.store.SaveConfig(); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to save configuration: %v", err), h.logger)
		return
	}

	if config.ConcurrencyAndBufferSize.Concurrency != oldConfigRaw.ConcurrencyAndBufferSize.Concurrency ||
		config.ConcurrencyAndBufferSize.BufferSize != oldConfigRaw.ConcurrencyAndBufferSize.BufferSize {
		// Update concurrency and queue configuration in Bifrost
		if err := h.client.UpdateProviderConcurrency(provider); err != nil {
			// Note: Store update succeeded, continue but log the concurrency update failure
			h.logger.Warn(fmt.Sprintf("Failed to update concurrency for provider %s: %v", provider, err))
		}
	}

	response := h.getProviderResponseFromConfig(provider, config)

	SendJSON(ctx, response, h.logger)
}

// DeleteProvider handles DELETE /api/providers/{provider} - Remove provider
func (h *ProviderHandler) DeleteProvider(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err), h.logger)
		return
	}

	// Check if provider exists
	if _, err := h.store.GetProviderConfigRedacted(provider); err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	// Remove provider from store
	if err := h.store.RemoveProvider(provider); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to remove provider %s: %v", provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to remove provider: %v", err), h.logger)
		return
	}

	if err := h.store.SaveConfig(); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to save configuration: %v", err), h.logger)
		return
	}

	h.logger.Info(fmt.Sprintf("Provider %s removed successfully", provider))

	response := ProviderResponse{
		Name: provider,
	}

	SendJSON(ctx, response, h.logger)
}

// convertToProviderMetaConfig converts a generic map to the appropriate provider-specific meta config
func (h *ProviderHandler) convertToProviderMetaConfig(provider schemas.ModelProvider, metaConfigMap map[string]interface{}) (*schemas.MetaConfig, error) {
	if len(metaConfigMap) == 0 {
		return nil, nil
	}

	// Convert map to JSON and then to specific meta config type
	metaConfigJSON, err := json.Marshal(metaConfigMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal meta config: %w", err)
	}

	switch provider {
	case schemas.Bedrock:
		var bedrockMetaConfig meta.BedrockMetaConfig
		if err := json.Unmarshal(metaConfigJSON, &bedrockMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Bedrock meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &bedrockMetaConfig
		return &metaConfig, nil

	default:
		// For providers that don't support meta config, return nil
		return nil, nil
	}
}

// mergeKeys merges new keys with old, preserving values that are redacted in the new config
func (h *ProviderHandler) mergeKeys(provider schemas.ModelProvider, oldRawKeys []schemas.Key, oldRedactedKeys []schemas.Key, keysToAdd []schemas.Key, keysToDelete []schemas.Key, keysToUpdate []schemas.Key) ([]schemas.Key, error) {
	// Clean up environment variables for deleted and updated keys
	h.store.CleanupEnvKeysForKeys(string(provider), keysToDelete)
	h.store.CleanupEnvKeysForUpdatedKeys(string(provider), keysToUpdate)
	// Create a map of indices to delete
	toDelete := make(map[int]bool)
	for _, key := range keysToDelete {
		for i, oldKey := range oldRawKeys {
			if oldKey.ID == key.ID {
				toDelete[i] = true
				break
			}
		}
	}

	// Create a map of updates by ID for quick lookup
	updates := make(map[string]schemas.Key)
	for _, key := range keysToUpdate {
		updates[key.ID] = key
	}

	// Process existing keys (handle updates and deletions)
	var resultKeys []schemas.Key
	for i, oldRawKey := range oldRawKeys {
		// Skip if this key should be deleted
		if toDelete[i] {
			continue
		}

		// Check if this key should be updated
		if updateKey, exists := updates[oldRawKey.ID]; exists {
			mergedKey := updateKey

			// Handle redacted values
			if lib.IsRedacted(updateKey.Value) &&
				(!strings.HasPrefix(updateKey.Value, "env.") ||
					!strings.EqualFold(updateKey.Value, oldRedactedKeys[i].Value)) {
				mergedKey.Value = oldRawKey.Value
			}

			// Handle Azure config redacted values
			if updateKey.AzureKeyConfig != nil && oldRedactedKeys[i].AzureKeyConfig != nil {
				if lib.IsRedacted(updateKey.AzureKeyConfig.Endpoint) &&
					(!strings.HasPrefix(updateKey.AzureKeyConfig.Endpoint, "env.") ||
						!strings.EqualFold(updateKey.AzureKeyConfig.Endpoint, oldRedactedKeys[i].AzureKeyConfig.Endpoint)) {
					mergedKey.AzureKeyConfig.Endpoint = oldRawKey.AzureKeyConfig.Endpoint
				}
				if updateKey.AzureKeyConfig.APIVersion != nil {
					if lib.IsRedacted(*updateKey.AzureKeyConfig.APIVersion) &&
						(!strings.HasPrefix(*updateKey.AzureKeyConfig.APIVersion, "env.") ||
							!strings.EqualFold(*updateKey.AzureKeyConfig.APIVersion, *oldRedactedKeys[i].AzureKeyConfig.APIVersion)) {
						mergedKey.AzureKeyConfig.APIVersion = oldRawKey.AzureKeyConfig.APIVersion
					}
				}
			}

			// Handle Vertex config redacted values
			if updateKey.VertexKeyConfig != nil && oldRedactedKeys[i].VertexKeyConfig != nil {
				if lib.IsRedacted(updateKey.VertexKeyConfig.ProjectID) &&
					(!strings.HasPrefix(updateKey.VertexKeyConfig.ProjectID, "env.") ||
						!strings.EqualFold(updateKey.VertexKeyConfig.ProjectID, oldRedactedKeys[i].VertexKeyConfig.ProjectID)) {
					mergedKey.VertexKeyConfig.ProjectID = oldRawKey.VertexKeyConfig.ProjectID
				}
				if lib.IsRedacted(updateKey.VertexKeyConfig.Region) &&
					(!strings.HasPrefix(updateKey.VertexKeyConfig.Region, "env.") ||
						!strings.EqualFold(updateKey.VertexKeyConfig.Region, oldRedactedKeys[i].VertexKeyConfig.Region)) {
					mergedKey.VertexKeyConfig.Region = oldRawKey.VertexKeyConfig.Region
				}
				if lib.IsRedacted(updateKey.VertexKeyConfig.AuthCredentials) &&
					(!strings.HasPrefix(updateKey.VertexKeyConfig.AuthCredentials, "env.") ||
						!strings.EqualFold(updateKey.VertexKeyConfig.AuthCredentials, oldRedactedKeys[i].VertexKeyConfig.AuthCredentials)) {
					mergedKey.VertexKeyConfig.AuthCredentials = oldRawKey.VertexKeyConfig.AuthCredentials
				}
			}

			resultKeys = append(resultKeys, mergedKey)
		} else {
			// Keep unchanged key
			resultKeys = append(resultKeys, oldRawKey)
		}
	}

	// Add new keys
	resultKeys = append(resultKeys, keysToAdd...)

	return resultKeys, nil
}

// mergeMetaConfig merges new meta config with old, preserving values that are redacted in the new config
func (h *ProviderHandler) mergeMetaConfig(provider schemas.ModelProvider, oldConfig *schemas.MetaConfig, newConfigMap map[string]interface{}) (*schemas.MetaConfig, error) {
	if oldConfig == nil || len(newConfigMap) == 0 {
		return h.convertToProviderMetaConfig(provider, newConfigMap)
	}

	switch provider {
	case schemas.Bedrock:
		var newBedrockConfig meta.BedrockMetaConfig
		newConfigJSON, _ := json.Marshal(newConfigMap)
		if err := json.Unmarshal(newConfigJSON, &newBedrockConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal new Bedrock meta config: %w", err)
		}

		oldBedrockConfig, ok := (*oldConfig).(*meta.BedrockMetaConfig)
		if !ok {
			return nil, fmt.Errorf("existing meta config type mismatch: expected BedrockMetaConfig")
		}

		// Preserve old values if new ones are redacted
		if lib.IsRedacted(newBedrockConfig.SecretAccessKey) {
			newBedrockConfig.SecretAccessKey = oldBedrockConfig.SecretAccessKey
		}
		if newBedrockConfig.Region != nil && oldBedrockConfig.Region != nil && lib.IsRedacted(*newBedrockConfig.Region) {
			newBedrockConfig.Region = oldBedrockConfig.Region
		}
		if newBedrockConfig.SessionToken != nil && oldBedrockConfig.SessionToken != nil && lib.IsRedacted(*newBedrockConfig.SessionToken) {
			newBedrockConfig.SessionToken = oldBedrockConfig.SessionToken
		}
		if newBedrockConfig.ARN != nil && oldBedrockConfig.ARN != nil && lib.IsRedacted(*newBedrockConfig.ARN) {
			newBedrockConfig.ARN = oldBedrockConfig.ARN
		}

		var metaConfig schemas.MetaConfig = &newBedrockConfig
		return &metaConfig, nil
	default:
		return nil, nil
	}
}

func (h *ProviderHandler) getProviderResponseFromConfig(provider schemas.ModelProvider, config lib.ProviderConfig) ProviderResponse {
	if config.NetworkConfig == nil {
		config.NetworkConfig = &schemas.DefaultNetworkConfig
	}
	if config.ConcurrencyAndBufferSize == nil {
		config.ConcurrencyAndBufferSize = &schemas.DefaultConcurrencyAndBufferSize
	}

	return ProviderResponse{
		Name:                     provider,
		Keys:                     config.Keys,
		NetworkConfig:            *config.NetworkConfig,
		MetaConfig:               config.MetaConfig,
		ConcurrencyAndBufferSize: *config.ConcurrencyAndBufferSize,
		ProxyConfig:              config.ProxyConfig,
	}
}

func getProviderFromCtx(ctx *fasthttp.RequestCtx) (schemas.ModelProvider, error) {
	providerValue := ctx.UserValue("provider")
	if providerValue == nil {
		return "", fmt.Errorf("missing provider parameter")
	}
	providerStr, ok := providerValue.(string)
	if !ok {
		return "", fmt.Errorf("invalid provider parameter type")
	}

	return schemas.ModelProvider(providerStr), nil
}
