// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all provider management functionality including CRUD operations.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ModelsManager defines the interface for managing provider models
type ModelsManager interface {
	RefetchModelsForProvider(ctx context.Context, provider schemas.ModelProvider) error
	DeleteModelsForProvider(ctx context.Context, provider schemas.ModelProvider) error
	GetModelsForProvider(provider schemas.ModelProvider) []string
}

// ProviderHandler manages HTTP requests for provider operations
type ProviderHandler struct {
	store         *lib.Config
	client        *bifrost.Bifrost
	modelsManager ModelsManager
}

// NewProviderHandler creates a new provider handler instance
func NewProviderHandler(modelsManager ModelsManager, store *lib.Config, client *bifrost.Bifrost) *ProviderHandler {
	return &ProviderHandler{
		store:         store,
		client:        client,
		modelsManager: modelsManager,
	}
}

type ProviderStatus = string

const (
	ProviderStatusActive  ProviderStatus = "active"  // Provider is active and working
	ProviderStatusError   ProviderStatus = "error"   // Provider failed to initialize
	ProviderStatusDeleted ProviderStatus = "deleted" // Provider is deleted from the store
)

// ProviderResponse represents the response for provider operations
type ProviderResponse struct {
	Name                     schemas.ModelProvider            `json:"name"`
	Keys                     []schemas.Key                    `json:"keys"`                             // API keys for the provider
	NetworkConfig            schemas.NetworkConfig            `json:"network_config"`                   // Network-related settings
	ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`      // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig             `json:"proxy_config"`                     // Proxy configuration
	SendBackRawRequest       bool                             `json:"send_back_raw_request"`            // Include raw request in BifrostResponse
	SendBackRawResponse      bool                             `json:"send_back_raw_response"`           // Include raw response in BifrostResponse
	CustomProviderConfig     *schemas.CustomProviderConfig    `json:"custom_provider_config,omitempty"` // Custom provider configuration
	Status                   ProviderStatus                   `json:"status"`                           // Status of the provider
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
func (h *ProviderHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Provider CRUD operations
	r.GET("/api/providers", lib.ChainMiddlewares(h.listProviders, middlewares...))
	r.GET("/api/providers/{provider}", lib.ChainMiddlewares(h.getProvider, middlewares...))
	r.POST("/api/providers", lib.ChainMiddlewares(h.addProvider, middlewares...))
	r.PUT("/api/providers/{provider}", lib.ChainMiddlewares(h.updateProvider, middlewares...))
	r.DELETE("/api/providers/{provider}", lib.ChainMiddlewares(h.deleteProvider, middlewares...))
	r.GET("/api/keys", lib.ChainMiddlewares(h.listKeys, middlewares...))
	r.GET("/api/models", lib.ChainMiddlewares(h.listModels, middlewares...))
}

// listProviders handles GET /api/providers - List all providers
func (h *ProviderHandler) listProviders(ctx *fasthttp.RequestCtx) {
	providers, err := h.store.GetAllProviders()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers: %v", err))
		return
	}

	providersInClient, err := h.client.GetConfiguredProviders()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers from client: %v", err))
		return
	}

	providerResponses := []ProviderResponse{}

	// Sort providers alphabetically
	sort.Slice(providers, func(i, j int) bool {
		return string(providers[i]) < string(providers[j])
	})

	for _, provider := range providers {
		config, err := h.store.GetProviderConfigRedacted(provider)
		if err != nil {
			logger.Warn(fmt.Sprintf("Failed to get config for provider %s: %v", provider, err))
			// Include provider even if config fetch fails
			providerResponses = append(providerResponses, ProviderResponse{
				Name:   provider,
				Status: ProviderStatusError,
			})
			continue
		}

		providerStatus := ProviderStatusError
		if slices.Contains(providersInClient, provider) {
			providerStatus = ProviderStatusActive
		}

		providerResponses = append(providerResponses, h.getProviderResponseFromConfig(provider, *config, providerStatus))
	}

	response := ListProvidersResponse{
		Providers: providerResponses,
		Total:     len(providerResponses),
	}

	SendJSON(ctx, response)
}

// getProvider handles GET /api/providers/{provider} - Get specific provider
func (h *ProviderHandler) getProvider(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	providersInClient, err := h.client.GetConfiguredProviders()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers from client: %v", err))
		return
	}

	config, err := h.store.GetProviderConfigRedacted(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
		return
	}

	providerStatus := ProviderStatusError
	if slices.Contains(providersInClient, provider) {
		providerStatus = ProviderStatusActive
	}

	response := h.getProviderResponseFromConfig(provider, *config, providerStatus)

	SendJSON(ctx, response)
}

// addProvider handles POST /api/providers - Add a new provider
func (h *ProviderHandler) addProvider(ctx *fasthttp.RequestCtx) {
	// Payload structure
	var payload = struct {
		Provider                 schemas.ModelProvider             `json:"provider"`
		Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider
		NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
		ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
		ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`                // Proxy configuration
		SendBackRawRequest       *bool                             `json:"send_back_raw_request,omitempty"`       // Include raw request in BifrostResponse
		SendBackRawResponse      *bool                             `json:"send_back_raw_response,omitempty"`      // Include raw response in BifrostResponse
		CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"`      // Custom provider configuration
	}{}

	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	// Validate provider
	if payload.Provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Missing provider")
		return
	}

	if payload.CustomProviderConfig != nil {
		// custom provider key should not be same as standard provider names
		if bifrost.IsStandardProvider(payload.Provider) {
			SendError(ctx, fasthttp.StatusBadRequest, "Custom provider cannot be same as a standard provider")
			return
		}

		if payload.CustomProviderConfig.BaseProviderType == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "BaseProviderType is required when CustomProviderConfig is provided")
			return
		}

		// check if base provider is a supported base provider
		if !bifrost.IsSupportedBaseProvider(payload.CustomProviderConfig.BaseProviderType) {
			SendError(ctx, fasthttp.StatusBadRequest, "BaseProviderType must be a standard provider")
			return
		}
	}

	if payload.ConcurrencyAndBufferSize != nil {
		if payload.ConcurrencyAndBufferSize.Concurrency == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be greater than 0")
			return
		}
		if payload.ConcurrencyAndBufferSize.BufferSize == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Buffer size must be greater than 0")
			return
		}

		if payload.ConcurrencyAndBufferSize.Concurrency > payload.ConcurrencyAndBufferSize.BufferSize {
			SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be less than or equal to buffer size")
			return
		}
	}

	// Validate retry backoff values if NetworkConfig is provided
	if payload.NetworkConfig != nil {
		if err := validateRetryBackoff(payload.NetworkConfig); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid retry backoff: %v", err))
			return
		}
	}

	// Check if provider already exists
	if _, err := h.store.GetProviderConfigRedacted(payload.Provider); err == nil {
		SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("Provider %s already exists", payload.Provider))
		return
	}

	// Construct ProviderConfig from individual fields
	config := configstore.ProviderConfig{
		Keys:                     payload.Keys,
		NetworkConfig:            payload.NetworkConfig,
		ProxyConfig:              payload.ProxyConfig,
		ConcurrencyAndBufferSize: payload.ConcurrencyAndBufferSize,
		SendBackRawRequest:       payload.SendBackRawRequest != nil && *payload.SendBackRawRequest,
		SendBackRawResponse:      payload.SendBackRawResponse != nil && *payload.SendBackRawResponse,
		CustomProviderConfig:     payload.CustomProviderConfig,
	}

	// Validate custom provider configuration before persisting
	if err := lib.ValidateCustomProvider(config, payload.Provider); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid custom provider config: %v", err))
		return
	}

	// Add provider to store (env vars will be processed by store)
	if err := h.store.AddProvider(ctx, payload.Provider, config); err != nil {
		logger.Warn(fmt.Sprintf("Failed to add provider %s: %v", payload.Provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add provider: %v", err))
		return
	}

	logger.Info(fmt.Sprintf("Provider %s added successfully", payload.Provider))

	// Get redacted config for response
	redactedConfig, err := h.store.GetProviderConfigRedacted(payload.Provider)
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to get redacted config for provider %s: %v", payload.Provider, err))
		// Fall back to the raw config (no keys)
		response := h.getProviderResponseFromConfig(payload.Provider, configstore.ProviderConfig{
			NetworkConfig:            config.NetworkConfig,
			ConcurrencyAndBufferSize: config.ConcurrencyAndBufferSize,
			ProxyConfig:              config.ProxyConfig,
			SendBackRawRequest:       config.SendBackRawRequest,
			SendBackRawResponse:      config.SendBackRawResponse,
			CustomProviderConfig:     config.CustomProviderConfig,
		}, ProviderStatusActive)
		SendJSON(ctx, response)
		return
	}

	if payload.CustomProviderConfig == nil ||
		!payload.CustomProviderConfig.IsKeyLess ||
		(payload.CustomProviderConfig.AllowedRequests != nil && payload.CustomProviderConfig.AllowedRequests.ListModels) {
		go func() {
			if err := h.modelsManager.RefetchModelsForProvider(context.Background(), payload.Provider); err != nil {
				logger.Warn(fmt.Sprintf("Failed to refetch models for provider %s: %v", payload.Provider, err))
			}
		}()
	}

	response := h.getProviderResponseFromConfig(payload.Provider, *redactedConfig, ProviderStatusActive)

	SendJSON(ctx, response)
}

// updateProvider handles PUT /api/providers/{provider} - Update provider config
// NOTE: This endpoint expects ALL fields to be provided in the request body,
// including both edited and non-edited fields. Partial updates are not supported.
// The frontend should send the complete provider configuration.
// This flow upserts the config
func (h *ProviderHandler) updateProvider(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		// If not found, then first we create and then update
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	var payload = struct {
		Keys                     []schemas.Key                    `json:"keys"`                             // API keys for the provider
		NetworkConfig            schemas.NetworkConfig            `json:"network_config"`                   // Network-related settings
		ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`      // Concurrency settings
		ProxyConfig              *schemas.ProxyConfig             `json:"proxy_config,omitempty"`           // Proxy configuration
		SendBackRawRequest       *bool                            `json:"send_back_raw_request,omitempty"`  // Include raw request in BifrostResponse
		SendBackRawResponse      *bool                            `json:"send_back_raw_response,omitempty"` // Include raw response in BifrostResponse
		CustomProviderConfig     *schemas.CustomProviderConfig    `json:"custom_provider_config,omitempty"` // Custom provider configuration
	}{}

	if err := json.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	// Get the raw config to access actual values for merging with redacted request values
	oldConfigRaw, err := h.store.GetProviderConfigRaw(provider)
	if err != nil {
		if !errors.Is(err, lib.ErrNotFound) {
			logger.Warn(fmt.Sprintf("Failed to get old config for provider %s: %v", provider, err))
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return
		}
	}

	if oldConfigRaw == nil {
		oldConfigRaw = &configstore.ProviderConfig{}
	}

	oldConfigRedacted, err := h.store.GetProviderConfigRedacted(provider)
	if err != nil {
		if !errors.Is(err, lib.ErrNotFound) {
			logger.Warn(fmt.Sprintf("Failed to get old redacted config for provider %s: %v", provider, err))
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return
		}
	}

	if oldConfigRedacted == nil {
		oldConfigRedacted = &configstore.ProviderConfig{}
	}

	// Construct ProviderConfig from individual fields
	config := configstore.ProviderConfig{
		Keys:                     oldConfigRaw.Keys,
		NetworkConfig:            oldConfigRaw.NetworkConfig,
		ConcurrencyAndBufferSize: oldConfigRaw.ConcurrencyAndBufferSize,
		ProxyConfig:              oldConfigRaw.ProxyConfig,
		CustomProviderConfig:     oldConfigRaw.CustomProviderConfig,
	}

	// Environment variable cleanup is now handled automatically by mergeKeys function

	var keysToAdd []schemas.Key
	var keysToUpdate []schemas.Key

	for _, key := range payload.Keys {
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
		if !slices.ContainsFunc(payload.Keys, func(k schemas.Key) bool {
			return k.ID == key.ID
		}) {
			keysToDelete = append(keysToDelete, key)
		}
	}

	keys, err := h.mergeKeys(provider, oldConfigRaw.Keys, oldConfigRedacted.Keys, keysToAdd, keysToDelete, keysToUpdate)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid keys: %v", err))
		return
	}
	config.Keys = keys

	if payload.ConcurrencyAndBufferSize.Concurrency == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be greater than 0")
		return
	}
	if payload.ConcurrencyAndBufferSize.BufferSize == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "Buffer size must be greater than 0")
		return
	}

	if payload.ConcurrencyAndBufferSize.Concurrency > payload.ConcurrencyAndBufferSize.BufferSize {
		SendError(ctx, fasthttp.StatusBadRequest, "Concurrency must be less than or equal to buffer size")
		return
	}

	// Build a prospective config with the requested CustomProviderConfig (including nil)
	prospective := config
	prospective.CustomProviderConfig = payload.CustomProviderConfig
	if err := lib.ValidateCustomProviderUpdate(prospective, *oldConfigRaw, provider); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid custom provider config: %v", err))
		return
	}

	nc := payload.NetworkConfig

	// Validate retry backoff values
	if err := validateRetryBackoff(&nc); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid retry backoff: %v", err))
		return
	}

	config.ConcurrencyAndBufferSize = &payload.ConcurrencyAndBufferSize
	config.NetworkConfig = &nc
	config.ProxyConfig = payload.ProxyConfig
	config.CustomProviderConfig = payload.CustomProviderConfig
	if payload.SendBackRawRequest != nil {
		config.SendBackRawRequest = *payload.SendBackRawRequest
	}
	if payload.SendBackRawResponse != nil {
		config.SendBackRawResponse = *payload.SendBackRawResponse
	}

	// Update provider config in store (env vars will be processed by store)
	if err := h.store.UpdateProviderConfig(ctx, provider, config); err != nil {
		if !errors.Is(err, lib.ErrNotFound) {
			logger.Warn(fmt.Sprintf("Failed to update provider %s: %v", provider, err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update provider: %v", err))
			return
		}
		// Creating provider instance with current config
		if addErr := h.store.AddProvider(ctx, provider, config); addErr != nil {
			logger.Warn(fmt.Sprintf("Failed to add provider %s: %v", provider, addErr))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to upsert provider: %v", addErr))
			return
		}
	}

	// Get redacted config for response
	redactedConfig, err := h.store.GetProviderConfigRedacted(provider)
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to get redacted config for provider %s: %v", provider, err))
		// Fall back to sanitized config (no keys)
		response := h.getProviderResponseFromConfig(provider, configstore.ProviderConfig{
			NetworkConfig:            config.NetworkConfig,
			ConcurrencyAndBufferSize: config.ConcurrencyAndBufferSize,
			ProxyConfig:              config.ProxyConfig,
			SendBackRawRequest:       config.SendBackRawRequest,
			SendBackRawResponse:      config.SendBackRawResponse,
			CustomProviderConfig:     config.CustomProviderConfig,
		}, ProviderStatusActive)
		SendJSON(ctx, response)
		return
	}

	if len(redactedConfig.Keys) > 0 &&
		(payload.CustomProviderConfig == nil ||
			!payload.CustomProviderConfig.IsKeyLess ||
			(payload.CustomProviderConfig.AllowedRequests != nil && payload.CustomProviderConfig.AllowedRequests.ListModels)) {
		go func() {
			if err := h.modelsManager.RefetchModelsForProvider(context.Background(), provider); err != nil {
				logger.Warn(fmt.Sprintf("Failed to refetch models for provider %s: %v", provider, err))
			}
		}()
	} else {
		if err := h.modelsManager.DeleteModelsForProvider(ctx, provider); err != nil {
			logger.Warn(fmt.Sprintf("Failed to delete models for provider %s: %v", provider, err))
		}
	}

	response := h.getProviderResponseFromConfig(provider, *redactedConfig, ProviderStatusActive)

	SendJSON(ctx, response)
}

// deleteProvider handles DELETE /api/providers/{provider} - Remove provider
func (h *ProviderHandler) deleteProvider(ctx *fasthttp.RequestCtx) {
	provider, err := getProviderFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid provider: %v", err))
		return
	}

	// Check if provider exists
	if _, err := h.store.GetProviderConfigRedacted(provider); err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
		return
	}

	// Remove provider from store
	if err := h.store.RemoveProvider(ctx, provider); err != nil {
		logger.Warn(fmt.Sprintf("Failed to remove provider %s: %v", provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to remove provider: %v", err))
		return
	}

	logger.Info(fmt.Sprintf("Provider %s removed successfully", provider))

	if err := h.modelsManager.DeleteModelsForProvider(ctx, provider); err != nil {
		logger.Warn(fmt.Sprintf("Failed to delete models for provider %s: %v", provider, err))
	}

	response := ProviderResponse{
		Name: provider,
	}

	SendJSON(ctx, response)
}

// listKeys handles GET /api/keys - List all keys
func (h *ProviderHandler) listKeys(ctx *fasthttp.RequestCtx) {
	keys, err := h.store.GetAllKeys()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get keys: %v", err))
		return
	}

	SendJSON(ctx, keys)
}

// ModelResponse represents a single model in the response
type ModelResponse struct {
	Name             string   `json:"name"`
	Provider         string   `json:"provider"`
	AccessibleByKeys []string `json:"accessible_by_keys,omitempty"`
}

// ListModelsResponse represents the response for listing models
type ListModelsResponse struct {
	Models []ModelResponse `json:"models"`
	Total  int             `json:"total"`
}

// listModels handles GET /api/models - List models with filtering
// Query parameters:
//   - query: Filter models by name (case-insensitive partial match)
//   - provider: Filter by specific provider name
//   - keys: Comma-separated list of key IDs to filter models accessible by those keys
//   - limit: Maximum number of results to return (default: 5)
func (h *ProviderHandler) listModels(ctx *fasthttp.RequestCtx) {
	// Parse query parameters
	queryParam := string(ctx.QueryArgs().Peek("query"))
	providerParam := string(ctx.QueryArgs().Peek("provider"))
	keysParam := string(ctx.QueryArgs().Peek("keys"))
	limitParam := string(ctx.QueryArgs().Peek("limit"))

	// Parse limit with default
	limit := 5
	if limitParam != "" {
		if n, err := ctx.QueryArgs().GetUint("limit"); err == nil {
			limit = n
		}
	}

	var allModels []ModelResponse

	// If provider is specified, get models for that provider only
	if providerParam != "" {
		provider := schemas.ModelProvider(providerParam)
		models := h.modelsManager.GetModelsForProvider(provider)

		// Filter by keys if specified
		if keysParam != "" {
			keyIDs := strings.Split(keysParam, ",")
			models = h.filterModelsByKeys(provider, models, keyIDs)
		}

		for _, model := range models {
			allModels = append(allModels, ModelResponse{
				Name:     model,
				Provider: string(provider),
			})
		}
	} else {
		// Get all providers
		providers, err := h.store.GetAllProviders()
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers: %v", err))
			return
		}

		// Collect models from all providers
		for _, provider := range providers {
			models := h.modelsManager.GetModelsForProvider(provider)

			// Filter by keys if specified
			if keysParam != "" {
				keyIDs := strings.Split(keysParam, ",")
				models = h.filterModelsByKeys(provider, models, keyIDs)
			}

			for _, model := range models {
				allModels = append(allModels, ModelResponse{
					Name:     model,
					Provider: string(provider),
				})
			}
		}
	}

	// Apply query filter if provided (fuzzy search)
	// We are currently doing it in memory to later make use of in memory model pools
	if queryParam != "" {
		filtered := []ModelResponse{}
		queryLower := strings.ToLower(queryParam)
		// Remove common separators for more flexible matching
		queryNormalized := strings.ReplaceAll(strings.ReplaceAll(queryLower, "-", ""), "_", "")

		for _, model := range allModels {
			modelLower := strings.ToLower(model.Name)
			modelNormalized := strings.ReplaceAll(strings.ReplaceAll(modelLower, "-", ""), "_", "")

			// Match if:
			// 1. Direct substring match
			// 2. Normalized substring match (ignoring - and _)
			// 3. All query characters appear in order (fuzzy match)
			if strings.Contains(modelLower, queryLower) ||
				strings.Contains(modelNormalized, queryNormalized) ||
				fuzzyMatch(modelLower, queryLower) {
				filtered = append(filtered, model)
			}
		}
		allModels = filtered
	}

	// Apply limit
	total := len(allModels)
	if limit > 0 && limit < len(allModels) {
		allModels = allModels[:limit]
	}

	response := ListModelsResponse{
		Models: allModels,
		Total:  total,
	}

	SendJSON(ctx, response)
}

// filterModelsByKeys filters models based on key-level model restrictions
func (h *ProviderHandler) filterModelsByKeys(provider schemas.ModelProvider, models []string, keyIDs []string) []string {
	// Get provider config to access keys
	config, err := h.store.GetProviderConfigRaw(provider)
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to get config for provider %s: %v", provider, err))
		return models
	}
	// Build a set of allowed models from the specified keys
	// Track whether we have any unrestricted keys (which grant access to all models)
	// and whether we have any restricted keys (which limit to specific models)
	allowedModels := make(map[string]bool)
	hasRestrictedKey := false
	hasUnrestrictedKey := false
	for _, keyID := range keyIDs {
		for _, key := range config.Keys {
			if key.ID == keyID {
				if len(key.Models) > 0 {
					// Key has model restrictions - add them to allowedModels
					hasRestrictedKey = true
					for _, model := range key.Models {
						allowedModels[model] = true
					}
				} else {
					// Key has no model restrictions - grants access to all models
					hasUnrestrictedKey = true
				}
				break
			}
		}
	}
	// If any key is unrestricted, return all models (union of "all" and restricted subsets is "all")
	if hasUnrestrictedKey {
		return models
	}
	// If no keys have model restrictions (e.g., unknown key IDs), return all models
	if !hasRestrictedKey {
		return models
	}
	// Filter models based on restrictions from restricted keys only
	filtered := []string{}
	for _, model := range models {
		if allowedModels[model] {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// mergeKeys merges new keys with old, preserving values that are redacted in the new config
func (h *ProviderHandler) mergeKeys(provider schemas.ModelProvider, oldRawKeys []schemas.Key, oldRedactedKeys []schemas.Key, keysToAdd []schemas.Key, keysToDelete []schemas.Key, keysToUpdate []schemas.Key) ([]schemas.Key, error) {
	// Clean up environment variables for deleted keys only
	// Updated keys will be cleaned up after merge to avoid premature cleanup
	h.store.CleanupEnvKeysForKeys(provider, keysToDelete)
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

	// Map old redacted keys by ID for reliable lookup
	redactedByID := make(map[string]schemas.Key)
	for _, rk := range oldRedactedKeys {
		redactedByID[rk.ID] = rk
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
			oldRedactedKey, ok := redactedByID[oldRawKey.ID]
			if !ok {
				oldRedactedKey = schemas.Key{}
			}
			mergedKey := updateKey

			// Handle redacted values - preserve old value if new value is redacted/env var AND it's the same as old redacted value
			if lib.IsRedacted(updateKey.Value) &&
				strings.EqualFold(updateKey.Value, oldRedactedKey.Value) {
				mergedKey.Value = oldRawKey.Value
			}

			// Handle Azure config redacted values
			if updateKey.AzureKeyConfig != nil && oldRedactedKey.AzureKeyConfig != nil && oldRawKey.AzureKeyConfig != nil {
				if lib.IsRedacted(updateKey.AzureKeyConfig.Endpoint) &&
					strings.EqualFold(updateKey.AzureKeyConfig.Endpoint, oldRedactedKey.AzureKeyConfig.Endpoint) {
					mergedKey.AzureKeyConfig.Endpoint = oldRawKey.AzureKeyConfig.Endpoint
				}
				if updateKey.AzureKeyConfig.APIVersion != nil &&
					oldRedactedKey.AzureKeyConfig.APIVersion != nil &&
					oldRawKey.AzureKeyConfig != nil {
					if lib.IsRedacted(*updateKey.AzureKeyConfig.APIVersion) &&
						strings.EqualFold(*updateKey.AzureKeyConfig.APIVersion, *oldRedactedKey.AzureKeyConfig.APIVersion) {
						mergedKey.AzureKeyConfig.APIVersion = oldRawKey.AzureKeyConfig.APIVersion
					}
				}
				// handle client id and secret and tenant id
				if updateKey.AzureKeyConfig.ClientID != nil &&
					oldRedactedKey.AzureKeyConfig.ClientID != nil &&
					oldRawKey.AzureKeyConfig != nil {
					if lib.IsRedacted(*updateKey.AzureKeyConfig.ClientID) &&
						strings.EqualFold(*updateKey.AzureKeyConfig.ClientID, *oldRedactedKey.AzureKeyConfig.ClientID) {
						mergedKey.AzureKeyConfig.ClientID = oldRawKey.AzureKeyConfig.ClientID
					}
				}
				if updateKey.AzureKeyConfig.ClientSecret != nil &&
					oldRedactedKey.AzureKeyConfig.ClientSecret != nil &&
					oldRawKey.AzureKeyConfig != nil {
					if lib.IsRedacted(*updateKey.AzureKeyConfig.ClientSecret) &&
						strings.EqualFold(*updateKey.AzureKeyConfig.ClientSecret, *oldRedactedKey.AzureKeyConfig.ClientSecret) {
						mergedKey.AzureKeyConfig.ClientSecret = oldRawKey.AzureKeyConfig.ClientSecret
					}
				}
				if updateKey.AzureKeyConfig.TenantID != nil &&
					oldRedactedKey.AzureKeyConfig.TenantID != nil &&
					oldRawKey.AzureKeyConfig != nil {
					if lib.IsRedacted(*updateKey.AzureKeyConfig.TenantID) &&
						strings.EqualFold(*updateKey.AzureKeyConfig.TenantID, *oldRedactedKey.AzureKeyConfig.TenantID) {
						mergedKey.AzureKeyConfig.TenantID = oldRawKey.AzureKeyConfig.TenantID
					}
				}
			}

			// Handle Vertex config redacted values
			if updateKey.VertexKeyConfig != nil && oldRedactedKey.VertexKeyConfig != nil && oldRawKey.VertexKeyConfig != nil {
				if lib.IsRedacted(updateKey.VertexKeyConfig.ProjectID) &&
					strings.EqualFold(updateKey.VertexKeyConfig.ProjectID, oldRedactedKey.VertexKeyConfig.ProjectID) {
					mergedKey.VertexKeyConfig.ProjectID = oldRawKey.VertexKeyConfig.ProjectID
				}
				if lib.IsRedacted(updateKey.VertexKeyConfig.ProjectNumber) &&
					strings.EqualFold(updateKey.VertexKeyConfig.ProjectNumber, oldRedactedKey.VertexKeyConfig.ProjectNumber) {
					mergedKey.VertexKeyConfig.ProjectNumber = oldRawKey.VertexKeyConfig.ProjectNumber
				}
				if lib.IsRedacted(updateKey.VertexKeyConfig.Region) &&
					strings.EqualFold(updateKey.VertexKeyConfig.Region, oldRedactedKey.VertexKeyConfig.Region) {
					mergedKey.VertexKeyConfig.Region = oldRawKey.VertexKeyConfig.Region
				}
				if lib.IsRedacted(updateKey.VertexKeyConfig.AuthCredentials) &&
					strings.EqualFold(updateKey.VertexKeyConfig.AuthCredentials, oldRedactedKey.VertexKeyConfig.AuthCredentials) {
					mergedKey.VertexKeyConfig.AuthCredentials = oldRawKey.VertexKeyConfig.AuthCredentials
				}
			}

			// Handle Bedrock config redacted values
			if updateKey.BedrockKeyConfig != nil && oldRedactedKey.BedrockKeyConfig != nil && oldRawKey.BedrockKeyConfig != nil {
				if lib.IsRedacted(updateKey.BedrockKeyConfig.AccessKey) &&
					strings.EqualFold(updateKey.BedrockKeyConfig.AccessKey, oldRedactedKey.BedrockKeyConfig.AccessKey) {
					mergedKey.BedrockKeyConfig.AccessKey = oldRawKey.BedrockKeyConfig.AccessKey
				}
				if lib.IsRedacted(updateKey.BedrockKeyConfig.SecretKey) &&
					strings.EqualFold(updateKey.BedrockKeyConfig.SecretKey, oldRedactedKey.BedrockKeyConfig.SecretKey) {
					mergedKey.BedrockKeyConfig.SecretKey = oldRawKey.BedrockKeyConfig.SecretKey
				}
				if updateKey.BedrockKeyConfig.SessionToken != nil &&
					oldRedactedKey.BedrockKeyConfig.SessionToken != nil &&
					oldRawKey.BedrockKeyConfig != nil {
					if lib.IsRedacted(*updateKey.BedrockKeyConfig.SessionToken) &&
						strings.EqualFold(*updateKey.BedrockKeyConfig.SessionToken, *oldRedactedKey.BedrockKeyConfig.SessionToken) {
						mergedKey.BedrockKeyConfig.SessionToken = oldRawKey.BedrockKeyConfig.SessionToken
					}
				}
				if updateKey.BedrockKeyConfig.Region != nil {
					if lib.IsRedacted(*updateKey.BedrockKeyConfig.Region) &&
						(!strings.HasPrefix(*updateKey.BedrockKeyConfig.Region, "env.") ||
							(oldRedactedKey.BedrockKeyConfig.Region != nil &&
								!strings.EqualFold(*updateKey.BedrockKeyConfig.Region, *oldRedactedKey.BedrockKeyConfig.Region))) {
						mergedKey.BedrockKeyConfig.Region = oldRawKey.BedrockKeyConfig.Region
					}
				}
				if updateKey.BedrockKeyConfig.ARN != nil {
					if lib.IsRedacted(*updateKey.BedrockKeyConfig.ARN) &&
						(!strings.HasPrefix(*updateKey.BedrockKeyConfig.ARN, "env.") ||
							(oldRedactedKey.BedrockKeyConfig.ARN != nil &&
								!strings.EqualFold(*updateKey.BedrockKeyConfig.ARN, *oldRedactedKey.BedrockKeyConfig.ARN))) {
						mergedKey.BedrockKeyConfig.ARN = oldRawKey.BedrockKeyConfig.ARN
					}
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

	// Clean up environment variables for updated keys after merge
	// This allows us to compare the final merged values with the original values
	h.store.CleanupEnvKeysForUpdatedKeys(provider, keysToUpdate, oldRawKeys, resultKeys)

	return resultKeys, nil
}

func (h *ProviderHandler) getProviderResponseFromConfig(provider schemas.ModelProvider, config configstore.ProviderConfig, status ProviderStatus) ProviderResponse {
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
		ConcurrencyAndBufferSize: *config.ConcurrencyAndBufferSize,
		ProxyConfig:              config.ProxyConfig,
		SendBackRawRequest:       config.SendBackRawRequest,
		SendBackRawResponse:      config.SendBackRawResponse,
		CustomProviderConfig:     config.CustomProviderConfig,
		Status:                   status,
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

	decoded, err := url.PathUnescape(providerStr)
	if err != nil {
		return "", fmt.Errorf("invalid provider parameter encoding: %v", err)
	}

	return schemas.ModelProvider(decoded), nil
}

func validateRetryBackoff(networkConfig *schemas.NetworkConfig) error {
	if networkConfig != nil {
		if networkConfig.RetryBackoffInitial > 0 {
			if networkConfig.RetryBackoffInitial < lib.MinRetryBackoff {
				return fmt.Errorf("retry backoff initial must be at least %v", lib.MinRetryBackoff)
			}
			if networkConfig.RetryBackoffInitial > lib.MaxRetryBackoff {
				return fmt.Errorf("retry backoff initial must be at most %v", lib.MaxRetryBackoff)
			}
		}
		if networkConfig.RetryBackoffMax > 0 {
			if networkConfig.RetryBackoffMax < lib.MinRetryBackoff {
				return fmt.Errorf("retry backoff max must be at least %v", lib.MinRetryBackoff)
			}
			if networkConfig.RetryBackoffMax > lib.MaxRetryBackoff {
				return fmt.Errorf("retry backoff max must be at most %v", lib.MaxRetryBackoff)
			}
		}
		if networkConfig.RetryBackoffInitial > 0 && networkConfig.RetryBackoffMax > 0 {
			if networkConfig.RetryBackoffInitial > networkConfig.RetryBackoffMax {
				return fmt.Errorf("retry backoff initial must be less than or equal to retry backoff max")
			}
		}
	}
	return nil
}
