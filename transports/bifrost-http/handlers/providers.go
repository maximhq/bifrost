// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all provider management functionality including CRUD operations.
package handlers

import (
	"encoding/json"
	"fmt"

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
}

// UpdateProviderRequest represents the request body for updating a provider
type UpdateProviderRequest struct {
	Keys                     *[]schemas.Key                    `json:"keys,omitempty"`                        // API keys for the provider
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	MetaConfig               *map[string]interface{}           `json:"meta_config,omitempty"`                 // Provider-specific metadata
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
}

// ProviderResponse represents the response for provider operations
type ProviderResponse struct {
	Provider schemas.ModelProvider `json:"provider"`
	Config   *lib.ProviderConfig   `json:"config,omitempty"`
	Status   string                `json:"status"`
	Message  string                `json:"message,omitempty"`
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
	r.GET("/providers", h.ListProviders)
	r.GET("/providers/{provider}", h.GetProvider)
	r.POST("/providers", h.AddProvider)
	r.PUT("/providers/{provider}", h.UpdateProvider)
	r.DELETE("/providers/{provider}", h.DeleteProvider)

	// Configuration persistence
	r.POST("/config/save", h.SaveConfig)
}

// ListProviders handles GET /providers - List all providers
func (h *ProviderHandler) ListProviders(ctx *fasthttp.RequestCtx) {
	providers, err := h.store.GetAllProviders()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get providers: %v", err), h.logger)
		return
	}

	var providerResponses []ProviderResponse
	for _, provider := range providers {
		config, err := h.store.GetProviderConfig(provider)
		if err != nil {
			h.logger.Warn(fmt.Sprintf("Failed to get config for provider %s: %v", provider, err))
			// Include provider even if config fetch fails
			providerResponses = append(providerResponses, ProviderResponse{
				Provider: provider,
				Status:   "error",
				Message:  fmt.Sprintf("Failed to get config: %v", err),
			})
			continue
		}

		providerResponses = append(providerResponses, ProviderResponse{
			Provider: provider,
			Config:   config,
			Status:   "active",
		})
	}

	response := ListProvidersResponse{
		Providers: providerResponses,
		Total:     len(providerResponses),
	}

	SendJSON(ctx, response, h.logger)
}

// GetProvider handles GET /providers/{provider} - Get specific provider
func (h *ProviderHandler) GetProvider(ctx *fasthttp.RequestCtx) {
	providerStr := ctx.UserValue("provider").(string)
	provider := schemas.ModelProvider(providerStr)

	config, err := h.store.GetProviderConfig(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	response := ProviderResponse{
		Provider: provider,
		Config:   config,
		Status:   "active",
	}

	SendJSON(ctx, response, h.logger)
}

// AddProvider handles POST /providers - Add a new provider
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
	if len(req.Keys) == 0 {
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
	if _, err := h.store.GetProviderConfig(req.Provider); err == nil {
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

	h.logger.Info(fmt.Sprintf("Provider %s added successfully", req.Provider))

	response := ProviderResponse{
		Provider: req.Provider,
		Config:   &config,
		Status:   "added",
		Message:  fmt.Sprintf("Provider %s added successfully", req.Provider),
	}

	SendJSON(ctx, response, h.logger)
}

// UpdateProvider handles PUT /providers/{provider} - Update provider config
// NOTE: This endpoint expects ALL fields to be provided in the request body,
// including both edited and non-edited fields. Partial updates are not supported.
// The frontend should send the complete provider configuration.
func (h *ProviderHandler) UpdateProvider(ctx *fasthttp.RequestCtx) {
	providerStr := ctx.UserValue("provider").(string)
	provider := schemas.ModelProvider(providerStr)

	var req UpdateProviderRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err), h.logger)
		return
	}

	// Check if provider exists
	oldConfig, err := h.store.GetProviderConfig(provider)
	if err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	// Construct ProviderConfig from individual fields
	config := lib.ProviderConfig{
		Keys:                     oldConfig.Keys,
		NetworkConfig:            oldConfig.NetworkConfig,
		ConcurrencyAndBufferSize: oldConfig.ConcurrencyAndBufferSize,
	}

	// Validate required keys (at least one key must be provided)
	if req.Keys != nil {
		if len(*req.Keys) == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "At least one API key is required", h.logger)
			return
		}
		config.Keys = *req.Keys
	}

	// Handle meta config if provided
	if req.MetaConfig != nil && len(*req.MetaConfig) > 0 {
		// Convert to appropriate meta config type based on provider
		metaConfig, err := h.convertToProviderMetaConfig(provider, *req.MetaConfig)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid meta config: %v", err), h.logger)
			return
		}
		config.MetaConfig = metaConfig
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
		config.ConcurrencyAndBufferSize = req.ConcurrencyAndBufferSize
	}

	// Update provider config in store (env vars will be processed by store)
	if err := h.store.UpdateProviderConfig(provider, config); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to update provider %s: %v", provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update provider: %v", err), h.logger)
		return
	}

	// Update concurrency and queue configuration in Bifrost
	if err := h.client.UpdateProviderConcurrency(provider); err != nil {
		// Note: Store update succeeded, continue but log the concurrency update failure
		h.logger.Warn(fmt.Sprintf("Failed to update concurrency for provider %s: %v", provider, err))
	}

	h.logger.Info(fmt.Sprintf("Provider %s updated successfully", provider))

	response := ProviderResponse{
		Provider: provider,
		Config:   &config,
		Status:   "updated",
		Message:  fmt.Sprintf("Provider %s updated successfully", provider),
	}

	SendJSON(ctx, response, h.logger)
}

// DeleteProvider handles DELETE /providers/{provider} - Remove provider
func (h *ProviderHandler) DeleteProvider(ctx *fasthttp.RequestCtx) {
	providerStr := ctx.UserValue("provider").(string)
	provider := schemas.ModelProvider(providerStr)

	// Check if provider exists
	if _, err := h.store.GetProviderConfig(provider); err != nil {
		SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("Provider not found: %v", err), h.logger)
		return
	}

	// Remove provider from store
	if err := h.store.RemoveProvider(provider); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to remove provider %s: %v", provider, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to remove provider: %v", err), h.logger)
		return
	}

	h.logger.Info(fmt.Sprintf("Provider %s removed successfully", provider))

	response := ProviderResponse{
		Provider: provider,
		Status:   "removed",
		Message:  fmt.Sprintf("Provider %s removed successfully", provider),
	}

	SendJSON(ctx, response, h.logger)
}

// SaveConfig handles POST /config/save - Persist current configuration to JSON file
func (h *ProviderHandler) SaveConfig(ctx *fasthttp.RequestCtx) {
	// Save current configuration back to the original JSON file
	if err := h.store.SaveConfig(); err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to save configuration: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to save configuration: %v", err), h.logger)
		return
	}

	h.logger.Info("Configuration saved successfully")

	response := map[string]interface{}{
		"status":  "success",
		"message": "Configuration saved successfully",
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
	case schemas.Azure:
		var azureMetaConfig meta.AzureMetaConfig
		if err := json.Unmarshal(metaConfigJSON, &azureMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Azure meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &azureMetaConfig
		return &metaConfig, nil

	case schemas.Bedrock:
		var bedrockMetaConfig meta.BedrockMetaConfig
		if err := json.Unmarshal(metaConfigJSON, &bedrockMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Bedrock meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &bedrockMetaConfig
		return &metaConfig, nil

	case schemas.Vertex:
		var vertexMetaConfig meta.VertexMetaConfig
		if err := json.Unmarshal(metaConfigJSON, &vertexMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Vertex meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &vertexMetaConfig
		return &metaConfig, nil

	default:
		// For providers that don't support meta config, return nil
		return nil, nil
	}
}
