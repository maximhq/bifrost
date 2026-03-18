// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all provider management functionality including CRUD operations.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// ModelsManager defines the interface for managing provider models
type ModelsManager interface {
	ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error)
	RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error
	GetModelsForProvider(provider schemas.ModelProvider) []string
	GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string
}

// ProviderHandler manages HTTP requests for provider operations
type ProviderHandler struct {
	dbStore       configstore.ConfigStore
	inMemoryStore *lib.Config
	client        *bifrost.Bifrost
	modelsManager ModelsManager
}

// NewProviderHandler creates a new provider handler instance
func NewProviderHandler(modelsManager ModelsManager, inMemoryStore *lib.Config, client *bifrost.Bifrost) *ProviderHandler {
	return &ProviderHandler{
		dbStore:       inMemoryStore.ConfigStore,
		inMemoryStore: inMemoryStore,
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
	Name                     schemas.ModelProvider             `json:"name"`
	NetworkConfig            schemas.NetworkConfig             `json:"network_config"`                   // Network-related settings
	ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize  `json:"concurrency_and_buffer_size"`      // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config"`                     // Proxy configuration
	SendBackRawRequest       bool                              `json:"send_back_raw_request"`            // Include raw request in BifrostResponse
	SendBackRawResponse      bool                              `json:"send_back_raw_response"`           // Include raw response in BifrostResponse
	StoreRawRequestResponse  bool                              `json:"store_raw_request_response"`       // Capture raw request/response for internal logging only
	CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"` // Custom provider configuration
	ProviderStatus           ProviderStatus                    `json:"provider_status"`                  // Health/initialization status of the provider
	Status                   string                            `json:"status,omitempty"`                 // Operational status (e.g., list_models_failed)
	Description              string                            `json:"description,omitempty"`            // Error/status description
	ConfigHash               string                            `json:"config_hash,omitempty"`            // Hash of config.json version, used for change detection
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

type providerCreatePayload struct {
	Provider                 schemas.ModelProvider             `json:"provider"`
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"`
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`
	SendBackRawRequest       *bool                             `json:"send_back_raw_request,omitempty"`
	SendBackRawResponse      *bool                             `json:"send_back_raw_response,omitempty"`
	StoreRawRequestResponse  *bool                             `json:"store_raw_request_response,omitempty"`
	CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"`
}

type providerUpdatePayload struct {
	NetworkConfig            schemas.NetworkConfig             `json:"network_config"`
	ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize  `json:"concurrency_and_buffer_size"`
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`
	SendBackRawRequest       *bool                             `json:"send_back_raw_request,omitempty"`
	SendBackRawResponse      *bool                             `json:"send_back_raw_response,omitempty"`
	StoreRawRequestResponse  *bool                             `json:"store_raw_request_response,omitempty"`
	CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"`
}

// RegisterRoutes registers all provider management routes
func (h *ProviderHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Provider CRUD operations
	r.GET("/api/providers", lib.ChainMiddlewares(h.listProviders, middlewares...))
	r.GET("/api/providers/{provider}", lib.ChainMiddlewares(h.getProvider, middlewares...))
	r.GET("/api/providers/{provider}/keys", lib.ChainMiddlewares(h.listProviderKeys, middlewares...))
	r.GET("/api/providers/{provider}/keys/{key_id}", lib.ChainMiddlewares(h.getProviderKey, middlewares...))
	r.POST("/api/providers", lib.ChainMiddlewares(h.addProvider, middlewares...))
	r.POST("/api/providers/{provider}/keys", lib.ChainMiddlewares(h.createProviderKey, middlewares...))
	r.PUT("/api/providers/{provider}", lib.ChainMiddlewares(h.updateProvider, middlewares...))
	r.PUT("/api/providers/{provider}/keys/{key_id}", lib.ChainMiddlewares(h.updateProviderKey, middlewares...))
	r.DELETE("/api/providers/{provider}", lib.ChainMiddlewares(h.deleteProvider, middlewares...))
	r.DELETE("/api/providers/{provider}/keys/{key_id}", lib.ChainMiddlewares(h.deleteProviderKey, middlewares...))
	r.GET("/api/keys", lib.ChainMiddlewares(h.listKeys, middlewares...))
	r.GET("/api/models", lib.ChainMiddlewares(h.listModels, middlewares...))
	r.GET("/api/models/parameters", lib.ChainMiddlewares(h.getModelParameters, middlewares...))
	r.GET("/api/models/base", lib.ChainMiddlewares(h.listBaseModels, middlewares...))
}

// listProviders handles GET /api/providers - List all providers
func (h *ProviderHandler) listProviders(ctx *fasthttp.RequestCtx) {
	// Fetching providers from database or in-memory store
	var providers map[schemas.ModelProvider]configstore.ProviderConfig
	if h.dbStore != nil {
		var err error
		providers, err = h.dbStore.GetProvidersConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers: %v", err))
			return
		}
	} else {
		h.inMemoryStore.Mu.RLock()
		providers = h.inMemoryStore.Providers
		h.inMemoryStore.Mu.RUnlock()
	}
	providersInClient, err := h.client.GetConfiguredProviders()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers from client: %v", err))
		return
	}
	providerResponses := []ProviderResponse{}

	for providerName, provider := range providers {
		config := provider.Redacted()

		providerStatus := ProviderStatusError
		if slices.Contains(providersInClient, providerName) {
			providerStatus = ProviderStatusActive
		}
		providerResponses = append(providerResponses, h.getProviderResponseFromConfig(providerName, *config, providerStatus))
	}
	// Sort providers alphabetically
	sort.Slice(providerResponses, func(i, j int) bool {
		return providerResponses[i].Name < providerResponses[j].Name
	})
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

	var config *configstore.ProviderConfig
	if h.dbStore != nil {
		config, err = h.dbStore.GetProviderConfig(ctx, provider)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
				return
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
			return
		}
	} else {
		config, err = h.inMemoryStore.GetProviderConfigRaw(provider)
		if err != nil {
			if errors.Is(err, lib.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err))
				return
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider config: %v", err))
			return
		}
	}
	redactedConfig := config.Redacted()

	providerStatus := ProviderStatusError
	if slices.Contains(providersInClient, provider) {
		providerStatus = ProviderStatusActive
	}

	response := h.getProviderResponseFromConfig(provider, *redactedConfig, providerStatus)

	SendJSON(ctx, response)
}

// addProvider handles POST /api/providers - Add a new provider
// NOTE: This only gets called when a new custom provider is added
func (h *ProviderHandler) addProvider(ctx *fasthttp.RequestCtx) {
	var payload providerCreatePayload
	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
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
	if _, err := h.inMemoryStore.GetProviderConfigRedacted(payload.Provider); err != nil {
		if !errors.Is(err, lib.ErrNotFound) {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to check provider config: %v", err))
			return
		}
	} else {
		SendError(ctx, fasthttp.StatusConflict, fmt.Sprintf("Provider %s already exists", payload.Provider))
		return
	}

	// Construct ProviderConfig from individual fields
	config := configstore.ProviderConfig{
		NetworkConfig:            payload.NetworkConfig,
		ProxyConfig:              payload.ProxyConfig,
		ConcurrencyAndBufferSize: payload.ConcurrencyAndBufferSize,
		SendBackRawRequest:       payload.SendBackRawRequest != nil && *payload.SendBackRawRequest,
		SendBackRawResponse:      payload.SendBackRawResponse != nil && *payload.SendBackRawResponse,
		StoreRawRequestResponse:  payload.StoreRawRequestResponse != nil && *payload.StoreRawRequestResponse,
		CustomProviderConfig:     payload.CustomProviderConfig,
	}
	// Validate custom provider configuration before persisting
	if err := lib.ValidateCustomProvider(config, payload.Provider); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid custom provider config: %v", err))
		return
	}
	// Add provider to store (env vars will be processed by store)
	if err := h.inMemoryStore.AddProvider(ctx, payload.Provider, config); err != nil {
		logger.Warn("Failed to add provider %s: %v", payload.Provider, err)
		if errors.Is(err, lib.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add provider: %v", err))
		return
	}
	logger.Info("Provider %s added successfully", payload.Provider)

	// Attempt model discovery
	if err := h.attemptModelDiscovery(ctx, payload.Provider, payload.CustomProviderConfig); err != nil {
		logger.Warn("Model discovery failed for provider %s: %v", payload.Provider, err)
	}

	// Get redacted config for response (in-memory store is now updated by updateKeyStatus)
	redactedConfig, err := h.inMemoryStore.GetProviderConfigRedacted(payload.Provider)
	if err != nil {
		logger.Warn("Failed to get redacted config for provider %s: %v", payload.Provider, err)
		// Fall back to the raw config (no keys)
		response := h.getProviderResponseFromConfig(payload.Provider, configstore.ProviderConfig{
			NetworkConfig:            config.NetworkConfig,
			ConcurrencyAndBufferSize: config.ConcurrencyAndBufferSize,
			ProxyConfig:              config.ProxyConfig,
			SendBackRawRequest:       config.SendBackRawRequest,
			SendBackRawResponse:      config.SendBackRawResponse,
			StoreRawRequestResponse:  config.StoreRawRequestResponse,
			CustomProviderConfig:     config.CustomProviderConfig,
			Status:                   config.Status,
			Description:              config.Description,
		}, ProviderStatusActive)
		SendJSON(ctx, response)
		return
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

	var payload providerUpdatePayload

	if err := sonic.Unmarshal(ctx.PostBody(), &payload); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	// Get the raw config to access actual values for merging with redacted request values
	oldConfigRaw, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil {
		if !errors.Is(err, lib.ErrNotFound) {
			logger.Warn("Failed to get old config for provider %s: %v", provider, err)
			SendError(ctx, fasthttp.StatusInternalServerError, err.Error())
			return
		}
	}

	if oldConfigRaw == nil {
		oldConfigRaw = &configstore.ProviderConfig{}
	}

	// Construct ProviderConfig from individual fields (keys are managed separately via /keys endpoints)
	config := configstore.ProviderConfig{
		Keys:                     oldConfigRaw.Keys,
		NetworkConfig:            oldConfigRaw.NetworkConfig,
		ConcurrencyAndBufferSize: oldConfigRaw.ConcurrencyAndBufferSize,
		ProxyConfig:              oldConfigRaw.ProxyConfig,
		CustomProviderConfig:     oldConfigRaw.CustomProviderConfig,
		StoreRawRequestResponse:  oldConfigRaw.StoreRawRequestResponse,
		Status:                   oldConfigRaw.Status,
		Description:              oldConfigRaw.Description,
	}

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
	// Merge network config - restore ca_cert_pem if the redacted placeholder was sent back
	if oldConfigRaw.NetworkConfig != nil && (nc.CACertPEM == "<REDACTED>" || nc.CACertPEM == "********") {
		nc.CACertPEM = oldConfigRaw.NetworkConfig.CACertPEM
	}
	config.NetworkConfig = &nc
	// Merge proxy config - preserve secrets if redacted values were sent back
	if payload.ProxyConfig != nil && oldConfigRaw.ProxyConfig != nil {
		if payload.ProxyConfig.IsRedactedValue(payload.ProxyConfig.Password) {
			payload.ProxyConfig.Password = oldConfigRaw.ProxyConfig.Password
		}
		if payload.ProxyConfig.IsRedactedValue(payload.ProxyConfig.CACertPEM) {
			payload.ProxyConfig.CACertPEM = oldConfigRaw.ProxyConfig.CACertPEM
		}
	}

	config.ProxyConfig = payload.ProxyConfig
	config.CustomProviderConfig = payload.CustomProviderConfig
	if payload.SendBackRawRequest != nil {
		config.SendBackRawRequest = *payload.SendBackRawRequest
	}
	if payload.SendBackRawResponse != nil {
		config.SendBackRawResponse = *payload.SendBackRawResponse
	}
	if payload.StoreRawRequestResponse != nil {
		config.StoreRawRequestResponse = *payload.StoreRawRequestResponse
	}

	// Add provider to store if it doesn't exist (upsert behavior)
	if _, err := h.inMemoryStore.GetProviderConfigRaw(provider); err != nil {
		if !errors.Is(err, lib.ErrNotFound) {
			logger.Warn("Failed to get provider %s: %v", provider, err)
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get provider: %v", err))
			return
		}
		// Adding the provider to store
		if err := h.inMemoryStore.AddProvider(ctx, provider, config); err != nil {
			// In an upsert flow, "already exists" is not fatal — the provider may have been
			// added concurrently or exist in the DB from a previous failed attempt.
			if !errors.Is(err, lib.ErrAlreadyExists) {
				logger.Warn("Failed to add provider %s: %v", provider, err)
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to add provider: %v", err))
				return
			}
			logger.Info("Provider %s already exists during upsert, proceeding with update", provider)
		}
	}

	// Update provider config in store (env vars will be processed by store)
	if err := h.inMemoryStore.UpdateProviderConfig(ctx, provider, config); err != nil {
		logger.Warn("Failed to update provider %s: %v", provider, err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update provider: %v", err))
		return
	}
	// Attempt model discovery
	err = h.attemptModelDiscovery(ctx, provider, payload.CustomProviderConfig)

	if err != nil {
		logger.Warn("Model discovery failed for provider %s: %v", provider, err)
	}

	// Get redacted config for response (in-memory store is now updated by updateKeyStatus)
	redactedConfig, err := h.inMemoryStore.GetProviderConfigRedacted(provider)
	if err != nil {
		logger.Warn("Failed to get redacted config for provider %s: %v", provider, err)
		// Fall back to sanitized config (no keys)
		response := h.getProviderResponseFromConfig(provider, configstore.ProviderConfig{
			NetworkConfig:            config.NetworkConfig,
			ConcurrencyAndBufferSize: config.ConcurrencyAndBufferSize,
			ProxyConfig:              config.ProxyConfig,
			SendBackRawRequest:       config.SendBackRawRequest,
			SendBackRawResponse:      config.SendBackRawResponse,
			StoreRawRequestResponse:  config.StoreRawRequestResponse,
			CustomProviderConfig:     config.CustomProviderConfig,
			Status:                   config.Status,
			Description:              config.Description,
		}, ProviderStatusActive)
		SendJSON(ctx, response)
		return
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
	if _, err := h.inMemoryStore.GetProviderConfigRedacted(provider); err != nil && !errors.Is(err, lib.ErrNotFound) {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to get provider: %v", err))
		return
	}

	if err := h.modelsManager.RemoveProvider(ctx, provider); err != nil {
		logger.Warn("Failed to delete models for provider %s: %v", provider, err)
	}

	response := ProviderResponse{
		Name: provider,
	}

	SendJSON(ctx, response)
}

// listKeys handles GET /api/keys - List all keys
func (h *ProviderHandler) listKeys(ctx *fasthttp.RequestCtx) {
	keys, err := h.inMemoryStore.GetAllKeys()
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
	unfilteredParam := string(ctx.QueryArgs().Peek("unfiltered"))

	unfiltered := unfilteredParam == "true"

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
		var models []string
		if unfiltered {
			models = h.modelsManager.GetUnfilteredModelsForProvider(provider)
		} else {
			models = h.modelsManager.GetModelsForProvider(provider)
			// Filter by keys if specified
			if keysParam != "" {
				keyIDs := strings.Split(keysParam, ",")
				models = h.filterModelsByKeys(provider, models, keyIDs)
			}
		}
		for _, model := range models {
			allModels = append(allModels, ModelResponse{
				Name:     model,
				Provider: string(provider),
			})
		}
	} else {
		// Get all providers
		providers, err := h.inMemoryStore.GetAllProviders()
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers: %v", err))
			return
		}

		// Collect models from all providers
		for _, provider := range providers {
			var models []string
			if unfiltered {
				models = h.modelsManager.GetUnfilteredModelsForProvider(provider)
			} else {
				models = h.modelsManager.GetModelsForProvider(provider)
				// Filter by keys if specified
				if keysParam != "" {
					keyIDs := strings.Split(keysParam, ",")
					models = h.filterModelsByKeys(provider, models, keyIDs)
				}

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
	// "*" is treated as a wildcard meaning "no filter" — return all models
	if queryParam != "" && queryParam != "*" {
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

// getModelParameters handles GET /api/models/parameters - Get model parameters for a specific model
// Query parameters:
//   - model: The model name to get parameters for (required)
func (h *ProviderHandler) getModelParameters(ctx *fasthttp.RequestCtx) {
	modelParam := string(ctx.QueryArgs().Peek("model"))
	if modelParam == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model query parameter is required")
		return
	}

	if h.dbStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "database store not available")
		return
	}

	params, err := h.dbStore.GetModelParameters(ctx, modelParam)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("no parameters found for model %s", modelParam))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get model parameters: %v", err))
		return
	}

	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBodyString(params.Data)
}

// filterModelsByKeys filters models based on key-level model restrictions
func (h *ProviderHandler) filterModelsByKeys(provider schemas.ModelProvider, models []string, keyIDs []string) []string {
	// Get provider config to access keys
	config, err := h.inMemoryStore.GetProviderConfigRaw(provider)
	if err != nil {
		logger.Warn("Failed to get config for provider %s: %v", provider, err)
		return models
	}
	// Build a set of allowed models from the specified keys
	// Track whether we have any unrestricted keys (which grant access to all models)
	// and whether we have any restricted keys (which limit to specific models)
	allowedModels := make(map[string]bool)
	hasRestrictedKey := false
	hasUnrestrictedKey := false
	hasDenyAllKey := false
	for _, keyID := range keyIDs {
		for _, key := range config.Keys {
			if key.ID == keyID {
				if key.Models.IsUnrestricted() {
					// Key allows all models (wildcard)
					hasUnrestrictedKey = true
				} else if !key.Models.IsEmpty() {
					// Key has specific model restrictions - add them to allowedModels
					hasRestrictedKey = true
					for _, model := range key.Models {
						allowedModels[model] = true
					}
				} else {
					// Empty Models = explicit deny-all for this key
					hasDenyAllKey = true
				}
				break
			}
		}
	}
	// If any key is unrestricted, return all models (union of "all" and restricted subsets is "all")
	if hasUnrestrictedKey {
		return models
	}
	// If no keys were matched or restricted, but at least one key explicitly denies all, return nothing
	if !hasRestrictedKey && hasDenyAllKey {
		return []string{}
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

// ListBaseModelsResponse represents the response for listing base models
type ListBaseModelsResponse struct {
	Models []string `json:"models"`
	Total  int      `json:"total"`
}

// listBaseModels handles GET /api/models/base - List distinct base model names from the catalog
// Query parameters:
//   - query: Filter base models by name (case-insensitive partial match)
//   - limit: Maximum number of results to return (default: 20)
func (h *ProviderHandler) listBaseModels(ctx *fasthttp.RequestCtx) {
	queryParam := string(ctx.QueryArgs().Peek("query"))
	limitParam := string(ctx.QueryArgs().Peek("limit"))

	limit := 20
	if limitParam != "" {
		if n, err := ctx.QueryArgs().GetUint("limit"); err == nil {
			limit = n
		}
	}

	modelCatalog := h.inMemoryStore.ModelCatalog
	if modelCatalog == nil {
		SendJSON(ctx, ListBaseModelsResponse{Models: []string{}, Total: 0})
		return
	}

	baseModels := modelCatalog.GetDistinctBaseModelNames()
	sort.Strings(baseModels)

	// Apply query filter if provided
	if queryParam != "" {
		filtered := []string{}
		queryLower := strings.ToLower(queryParam)
		queryNormalized := strings.ReplaceAll(strings.ReplaceAll(queryLower, "-", ""), "_", "")

		for _, model := range baseModels {
			modelLower := strings.ToLower(model)
			modelNormalized := strings.ReplaceAll(strings.ReplaceAll(modelLower, "-", ""), "_", "")

			if strings.Contains(modelLower, queryLower) ||
				strings.Contains(modelNormalized, queryNormalized) ||
				fuzzyMatch(modelLower, queryLower) {
				filtered = append(filtered, model)
			}
		}
		baseModels = filtered
	}

	total := len(baseModels)
	if limit > 0 && limit < len(baseModels) {
		baseModels = baseModels[:limit]
	}

	SendJSON(ctx, ListBaseModelsResponse{Models: baseModels, Total: total})
}

// attemptModelDiscovery performs model discovery with timeout
func (h *ProviderHandler) attemptModelDiscovery(ctx *fasthttp.RequestCtx, provider schemas.ModelProvider, customProviderConfig *schemas.CustomProviderConfig) error {
	// Determine if we should attempt model discovery
	shouldDiscoverModels := customProviderConfig == nil ||
		!customProviderConfig.IsKeyLess

	if !shouldDiscoverModels {
		return nil
	}

	// Attempt model discovery with reasonable timeout
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	_, err := h.modelsManager.ReloadProvider(ctxWithTimeout, provider)

	if err != nil {
		return err
	}

	return nil
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
		NetworkConfig:            *config.NetworkConfig,
		ConcurrencyAndBufferSize: *config.ConcurrencyAndBufferSize,
		ProxyConfig:              config.ProxyConfig,
		SendBackRawRequest:       config.SendBackRawRequest,
		SendBackRawResponse:      config.SendBackRawResponse,
		StoreRawRequestResponse:  config.StoreRawRequestResponse,
		CustomProviderConfig:     config.CustomProviderConfig,
		ProviderStatus:           status,
		Status:                   config.Status,
		Description:              config.Description,
		ConfigHash:               config.ConfigHash,
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
