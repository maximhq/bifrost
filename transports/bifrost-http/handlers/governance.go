// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all governance management functionality including CRUD operations for VKs, Rules, and configs.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// GovernanceManager is the interface for the governance manager
type GovernanceManager interface {
	GetGovernanceData() *governance.GovernanceData
	ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error)
	RemoveVirtualKey(ctx context.Context, id string) error
	ReloadTeam(ctx context.Context, id string) (*configstoreTables.TableTeam, error)
	RemoveTeam(ctx context.Context, id string) error
	ReloadCustomer(ctx context.Context, id string) (*configstoreTables.TableCustomer, error)
	RemoveCustomer(ctx context.Context, id string) error
}

// GovernanceHandler manages HTTP requests for governance operations
type GovernanceHandler struct {
	configStore       configstore.ConfigStore
	governanceManager GovernanceManager
}

// NewGovernanceHandler creates a new governance handler instance
func NewGovernanceHandler(manager GovernanceManager, configStore configstore.ConfigStore) (*GovernanceHandler, error) {
	if manager == nil {
		return nil, fmt.Errorf("governance manager is required")
	}
	if configStore == nil {
		return nil, fmt.Errorf("config store is required")
	}
	return &GovernanceHandler{
		governanceManager: manager,
		configStore:       configStore,
	}, nil
}

// CreateVirtualKeyRequest represents the request body for creating a virtual key
type CreateVirtualKeyRequest struct {
	Name            string `json:"name" validate:"required"`
	Description     string `json:"description,omitempty"`
	ProviderConfigs []struct {
		Provider      string                  `json:"provider" validate:"required"`
		Weight        float64                 `json:"weight,omitempty"`
		AllowedModels []string                `json:"allowed_models,omitempty"` // Empty means all models allowed
		Budget        *CreateBudgetRequest    `json:"budget,omitempty"`         // Provider-level budget
		RateLimit     *CreateRateLimitRequest `json:"rate_limit,omitempty"`     // Provider-level rate limit
		KeyIDs        []string                `json:"key_ids,omitempty"`        // List of DBKey UUIDs to associate with this provider config
	} `json:"provider_configs,omitempty"` // Empty means all providers allowed
	MCPConfigs []struct {
		MCPClientName  string   `json:"mcp_client_name" validate:"required"`
		ToolsToExecute []string `json:"tools_to_execute,omitempty"`
	} `json:"mcp_configs,omitempty"` // Empty means all MCP clients allowed
	TeamID     *string                 `json:"team_id,omitempty"`     // Mutually exclusive with CustomerID
	CustomerID *string                 `json:"customer_id,omitempty"` // Mutually exclusive with TeamID
	Budget     *CreateBudgetRequest    `json:"budget,omitempty"`
	RateLimit  *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	IsActive   *bool                   `json:"is_active,omitempty"`
}

// UpdateVirtualKeyRequest represents the request body for updating a virtual key
type UpdateVirtualKeyRequest struct {
	Name            *string `json:"name,omitempty"`
	Description     *string `json:"description,omitempty"`
	ProviderConfigs []struct {
		ID            *uint                   `json:"id,omitempty"` // null for new entries
		Provider      string                  `json:"provider" validate:"required"`
		Weight        float64                 `json:"weight,omitempty"`
		AllowedModels []string                `json:"allowed_models,omitempty"` // Empty means all models allowed
		Budget        *UpdateBudgetRequest    `json:"budget,omitempty"`         // Provider-level budget
		RateLimit     *UpdateRateLimitRequest `json:"rate_limit,omitempty"`     // Provider-level rate limit
		KeyIDs        []string                `json:"key_ids,omitempty"`        // List of DBKey UUIDs to associate with this provider config
	} `json:"provider_configs,omitempty"`
	MCPConfigs []struct {
		ID             *uint    `json:"id,omitempty"` // null for new entries
		MCPClientName  string   `json:"mcp_client_name" validate:"required"`
		ToolsToExecute []string `json:"tools_to_execute,omitempty"`
	} `json:"mcp_configs,omitempty"`
	TeamID     *string                 `json:"team_id,omitempty"`
	CustomerID *string                 `json:"customer_id,omitempty"`
	Budget     *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit  *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	IsActive   *bool                   `json:"is_active,omitempty"`
}

// CreateBudgetRequest represents the request body for creating a budget
type CreateBudgetRequest struct {
	MaxLimit      float64 `json:"max_limit" validate:"required"`      // Maximum budget in dollars
	ResetDuration string  `json:"reset_duration" validate:"required"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

// UpdateBudgetRequest represents the request body for updating a budget
type UpdateBudgetRequest struct {
	MaxLimit      *float64 `json:"max_limit,omitempty"`
	ResetDuration *string  `json:"reset_duration,omitempty"`
}

// CreateRateLimitRequest represents the request body for creating a rate limit using flexible approach
type CreateRateLimitRequest struct {
	TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`        // Maximum tokens allowed
	TokenResetDuration   *string `json:"token_reset_duration,omitempty"`   // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`      // Maximum requests allowed
	RequestResetDuration *string `json:"request_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

// UpdateRateLimitRequest represents the request body for updating a rate limit using flexible approach
type UpdateRateLimitRequest struct {
	TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`        // Maximum tokens allowed
	TokenResetDuration   *string `json:"token_reset_duration,omitempty"`   // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`      // Maximum requests allowed
	RequestResetDuration *string `json:"request_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

// CreateTeamRequest represents the request body for creating a team
type CreateTeamRequest struct {
	Name       string               `json:"name" validate:"required"`
	CustomerID *string              `json:"customer_id,omitempty"` // Team can belong to a customer
	Budget     *CreateBudgetRequest `json:"budget,omitempty"`      // Team can have its own budget
}

// UpdateTeamRequest represents the request body for updating a team
type UpdateTeamRequest struct {
	Name       *string              `json:"name,omitempty"`
	CustomerID *string              `json:"customer_id,omitempty"`
	Budget     *UpdateBudgetRequest `json:"budget,omitempty"`
}

// CreateCustomerRequest represents the request body for creating a customer
type CreateCustomerRequest struct {
	Name   string               `json:"name" validate:"required"`
	Budget *CreateBudgetRequest `json:"budget,omitempty"`
}

// UpdateCustomerRequest represents the request body for updating a customer
type UpdateCustomerRequest struct {
	Name   *string              `json:"name,omitempty"`
	Budget *UpdateBudgetRequest `json:"budget,omitempty"`
}

// RegisterRoutes registers all governance-related routes for the new hierarchical system
func (h *GovernanceHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Virtual Key CRUD operations
	r.GET("/api/governance/virtual-keys", lib.ChainMiddlewares(h.getVirtualKeys, middlewares...))
	r.POST("/api/governance/virtual-keys", lib.ChainMiddlewares(h.createVirtualKey, middlewares...))
	r.GET("/api/governance/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.getVirtualKey, middlewares...))
	r.PUT("/api/governance/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.updateVirtualKey, middlewares...))
	r.DELETE("/api/governance/virtual-keys/{vk_id}", lib.ChainMiddlewares(h.deleteVirtualKey, middlewares...))

	// Team CRUD operations
	r.GET("/api/governance/teams", lib.ChainMiddlewares(h.getTeams, middlewares...))
	r.POST("/api/governance/teams", lib.ChainMiddlewares(h.createTeam, middlewares...))
	r.GET("/api/governance/teams/{team_id}", lib.ChainMiddlewares(h.getTeam, middlewares...))
	r.PUT("/api/governance/teams/{team_id}", lib.ChainMiddlewares(h.updateTeam, middlewares...))
	r.DELETE("/api/governance/teams/{team_id}", lib.ChainMiddlewares(h.deleteTeam, middlewares...))

	// Customer CRUD operations
	r.GET("/api/governance/customers", lib.ChainMiddlewares(h.getCustomers, middlewares...))
	r.POST("/api/governance/customers", lib.ChainMiddlewares(h.createCustomer, middlewares...))
	r.GET("/api/governance/customers/{customer_id}", lib.ChainMiddlewares(h.getCustomer, middlewares...))
	r.PUT("/api/governance/customers/{customer_id}", lib.ChainMiddlewares(h.updateCustomer, middlewares...))
	r.DELETE("/api/governance/customers/{customer_id}", lib.ChainMiddlewares(h.deleteCustomer, middlewares...))

	// Budget and Rate Limit GET operations
	r.GET("/api/governance/budgets", lib.ChainMiddlewares(h.getBudgets, middlewares...))
	r.GET("/api/governance/rate-limits", lib.ChainMiddlewares(h.getRateLimits, middlewares...))
}

// Virtual Key CRUD Operations

// getVirtualKeys handles GET /api/governance/virtual-keys - Get all virtual keys with relationships
func (h *GovernanceHandler) getVirtualKeys(ctx *fasthttp.RequestCtx) {
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"virtual_keys": data.VirtualKeys,
			"count":        len(data.VirtualKeys),
		})
		return
	}
	// Preload all relationships for complete information
	virtualKeys, err := h.configStore.GetVirtualKeys(ctx)
	if err != nil {
		logger.Error("failed to retrieve virtual keys: %v", err)
		SendError(ctx, 500, "Failed to retrieve virtual keys")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"virtual_keys": virtualKeys,
		"count":        len(virtualKeys),
	})
}

// createVirtualKey handles POST /api/governance/virtual-keys - Create a new virtual key
func (h *GovernanceHandler) createVirtualKey(ctx *fasthttp.RequestCtx) {
	var req CreateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Virtual key name is required")
		return
	}
	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		SendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer")
		return
	}
	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit))
			return
		}
		// Validate reset duration format
		if _, err := configstoreTables.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration))
			return
		}
	}
	// Set defaults
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	var vk configstoreTables.TableVirtualKey
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		vk = configstoreTables.TableVirtualKey{
			ID:          uuid.NewString(),
			Name:        req.Name,
			Value:       governance.VirtualKeyPrefix + uuid.NewString(),
			Description: req.Description,
			TeamID:      req.TeamID,
			CustomerID:  req.CustomerID,
			IsActive:    isActive,
		}
		if req.Budget != nil {
			budget := configstoreTables.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			vk.BudgetID = &budget.ID
		}
		if req.RateLimit != nil {
			rateLimit := configstoreTables.TableRateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
				TokenResetDuration:   req.RateLimit.TokenResetDuration,
				RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
				RequestResetDuration: req.RateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := validateRateLimit(&rateLimit); err != nil {
				return err
			}
			if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return err
			}
			vk.RateLimitID = &rateLimit.ID
		}
		if err := h.configStore.CreateVirtualKey(ctx, &vk, tx); err != nil {
			return err
		}
		if req.ProviderConfigs != nil {
			for _, pc := range req.ProviderConfigs {
				// Validate budget if provided
				if pc.Budget != nil {
					if pc.Budget.MaxLimit < 0 {
						return fmt.Errorf("provider config budget max_limit cannot be negative: %.2f", pc.Budget.MaxLimit)
					}
					// Validate reset duration format
					if _, err := configstoreTables.ParseDuration(pc.Budget.ResetDuration); err != nil {
						return fmt.Errorf("invalid provider config budget reset duration format: %s", pc.Budget.ResetDuration)
					}
				}

				// Get keys for this provider config if specified
				var keys []configstoreTables.TableKey
				if len(pc.KeyIDs) > 0 {
					var err error
					keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
					if err != nil {
						return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
					}
					if len(keys) != len(pc.KeyIDs) {
						return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
					}
				}

				providerConfig := &configstoreTables.TableVirtualKeyProviderConfig{
					VirtualKeyID:  vk.ID,
					Provider:      pc.Provider,
					Weight:        &pc.Weight,
					AllowedModels: pc.AllowedModels,
					Keys:          keys,
				}

				// Create budget for provider config if provided
				if pc.Budget != nil {
					budget := configstoreTables.TableBudget{
						ID:            uuid.NewString(),
						MaxLimit:      pc.Budget.MaxLimit,
						ResetDuration: pc.Budget.ResetDuration,
						LastReset:     time.Now(),
						CurrentUsage:  0,
					}
					if err := validateBudget(&budget); err != nil {
						return err
					}
					if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
						return err
					}
					providerConfig.BudgetID = &budget.ID
				}
				// Create rate limit for provider config if provided
				if pc.RateLimit != nil {
					rateLimit := configstoreTables.TableRateLimit{
						ID:                   uuid.NewString(),
						TokenMaxLimit:        pc.RateLimit.TokenMaxLimit,
						TokenResetDuration:   pc.RateLimit.TokenResetDuration,
						RequestMaxLimit:      pc.RateLimit.RequestMaxLimit,
						RequestResetDuration: pc.RateLimit.RequestResetDuration,
						TokenLastReset:       time.Now(),
						RequestLastReset:     time.Now(),
					}
					if err := validateRateLimit(&rateLimit); err != nil {
						return err
					}
					if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
						return err
					}
					providerConfig.RateLimitID = &rateLimit.ID
				}

				if err := h.configStore.CreateVirtualKeyProviderConfig(ctx, providerConfig, tx); err != nil {
					return err
				}
			}
		}
		if req.MCPConfigs != nil {
			// Check for duplicate MCPClientName values before processing
			seenMCPClientNames := make(map[string]bool)
			for _, mc := range req.MCPConfigs {
				if seenMCPClientNames[mc.MCPClientName] {
					return fmt.Errorf("duplicate mcp_client_name: %s", mc.MCPClientName)
				}
				seenMCPClientNames[mc.MCPClientName] = true
			}

			for _, mc := range req.MCPConfigs {
				mcpClient, err := h.configStore.GetMCPClientByName(ctx, mc.MCPClientName)
				if err != nil {
					return fmt.Errorf("failed to get MCP client: %w", err)
				}
				if err := h.configStore.CreateVirtualKeyMCPConfig(ctx, &configstoreTables.TableVirtualKeyMCPConfig{
					VirtualKeyID:   vk.ID,
					MCPClientID:    mcpClient.ID,
					ToolsToExecute: mc.ToolsToExecute,
				}, tx); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		// Check if this is a duplicate MCPClientName error and return 400 instead of 500
		if strings.Contains(err.Error(), "duplicate mcp_client_name:") {
			SendError(ctx, 400, err.Error())
			return
		}
		SendError(ctx, 500, err.Error())
		return
	}
	preloadedVk, err := h.governanceManager.ReloadVirtualKey(ctx, vk.ID)
	if err != nil {
		logger.Error("failed to reload virtual key: %v", err)
		preloadedVk = &vk
	}

	SendJSON(ctx, map[string]any{
		"message":     "Virtual key created successfully",
		"virtual_key": preloadedVk,
	})
}

// getVirtualKey handles GET /api/governance/virtual-keys/{vk_id} - Get a specific virtual key
func (h *GovernanceHandler) getVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		for _, vk := range data.VirtualKeys {
			if vk.ID == vkID {
				SendJSON(ctx, map[string]interface{}{
					"virtual_key": vk,
				})
				return
			}
		}
		SendError(ctx, 404, "Virtual key not found")
		return
	}
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"virtual_key": vk,
	})
}

// updateVirtualKey handles PUT /api/governance/virtual-keys/{vk_id} - Update a virtual key
func (h *GovernanceHandler) updateVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	var req UpdateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		SendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer")
		return
	}
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Update fields if provided
		if req.Name != nil {
			vk.Name = *req.Name
		}
		if req.Description != nil {
			vk.Description = *req.Description
		}
		if req.TeamID != nil {
			vk.TeamID = req.TeamID
			vk.CustomerID = nil // Clear CustomerID if setting TeamID
		}
		if req.CustomerID != nil {
			vk.CustomerID = req.CustomerID
			vk.TeamID = nil // Clear TeamID if setting CustomerID
		}
		// When both TeamID and CustomerID are nil
		if req.TeamID == nil && req.CustomerID == nil {
			vk.TeamID = nil
			vk.CustomerID = nil
		}
		if req.IsActive != nil {
			vk.IsActive = *req.IsActive
		}
		// Handle budget updates
		if req.Budget != nil {
			if vk.BudgetID != nil {
				// Update existing budget
				budget := configstoreTables.TableBudget{}
				if err := tx.First(&budget, "id = ?", *vk.BudgetID).Error; err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.UpdateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				vk.Budget = &budget
			} else {
				// Create new budget
				if req.Budget.MaxLimit == nil || req.Budget.ResetDuration == nil {
					return fmt.Errorf("both max_limit and reset_duration are required when creating a new budget")
				}
				if *req.Budget.MaxLimit < 0 {
					return fmt.Errorf("budget max_limit cannot be negative: %.2f", *req.Budget.MaxLimit)
				}
				if _, err := configstoreTables.ParseDuration(*req.Budget.ResetDuration); err != nil {
					return fmt.Errorf("invalid reset duration format: %s", *req.Budget.ResetDuration)
				}
				// Storing now
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				vk.BudgetID = &budget.ID
				vk.Budget = &budget
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			if vk.RateLimitID != nil {
				// Update existing rate limit
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *vk.RateLimitID).Error; err != nil {
					return err
				}

				if req.RateLimit.TokenMaxLimit != nil {
					rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				}
				if req.RateLimit.TokenResetDuration != nil {
					rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				}
				if req.RateLimit.RequestMaxLimit != nil {
					rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				}
				if req.RateLimit.RequestResetDuration != nil {
					rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				}

				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
			} else {
				// Create new rate limit
				rateLimit := configstoreTables.TableRateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				vk.RateLimitID = &rateLimit.ID
			}
		}

		if err := h.configStore.UpdateVirtualKey(ctx, vk, tx); err != nil {
			return err
		}
		if req.ProviderConfigs != nil {
			// Get existing provider configs for comparison
			var existingConfigs []configstoreTables.TableVirtualKeyProviderConfig
			if err := tx.Where("virtual_key_id = ?", vk.ID).Find(&existingConfigs).Error; err != nil {
				return err
			}
			// Create maps for easier lookup
			existingConfigsMap := make(map[uint]configstoreTables.TableVirtualKeyProviderConfig)
			for _, config := range existingConfigs {
				existingConfigsMap[config.ID] = config
			}
			requestConfigsMap := make(map[uint]bool)
			// Process new configs: create new ones and update existing ones
			for _, pc := range req.ProviderConfigs {
				if pc.ID == nil {
					// Validate budget if provided for new provider config
					if pc.Budget != nil {
						if pc.Budget.MaxLimit != nil && *pc.Budget.MaxLimit < 0 {
							return fmt.Errorf("provider config budget max_limit cannot be negative: %.2f", *pc.Budget.MaxLimit)
						}
						if pc.Budget.ResetDuration != nil {
							if _, err := configstoreTables.ParseDuration(*pc.Budget.ResetDuration); err != nil {
								return fmt.Errorf("invalid provider config budget reset duration format: %s", *pc.Budget.ResetDuration)
							}
						}
						// Both fields are required when creating new budget
						if pc.Budget.MaxLimit == nil || pc.Budget.ResetDuration == nil {
							return fmt.Errorf("both max_limit and reset_duration are required when creating a new provider budget")
						}
					}
			// Get keys for this provider config if specified
			var keys []configstoreTables.TableKey
			if len(pc.KeyIDs) > 0 {
				var err error
				keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
				if err != nil {
					return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
				}
				if len(keys) != len(pc.KeyIDs) {
					return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
				}
			}

				// Create new provider config
				providerConfig := &configstoreTables.TableVirtualKeyProviderConfig{
					VirtualKeyID:  vk.ID,
					Provider:      pc.Provider,
					Weight:        &pc.Weight,
					AllowedModels: pc.AllowedModels,
					Keys:          keys,
				}
				// Create budget for provider config if provided
				if pc.Budget != nil {
						budget := configstoreTables.TableBudget{
							ID:            uuid.NewString(),
							MaxLimit:      *pc.Budget.MaxLimit,
							ResetDuration: *pc.Budget.ResetDuration,
							LastReset:     time.Now(),
							CurrentUsage:  0,
						}
						if err := validateBudget(&budget); err != nil {
							return err
						}
						if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
							return err
						}
						providerConfig.BudgetID = &budget.ID
					}
					// Create rate limit for provider config if provided
					if pc.RateLimit != nil {
						rateLimit := configstoreTables.TableRateLimit{
							ID:                   uuid.NewString(),
							TokenMaxLimit:        pc.RateLimit.TokenMaxLimit,
							TokenResetDuration:   pc.RateLimit.TokenResetDuration,
							RequestMaxLimit:      pc.RateLimit.RequestMaxLimit,
							RequestResetDuration: pc.RateLimit.RequestResetDuration,
							TokenLastReset:       time.Now(),
							RequestLastReset:     time.Now(),
						}
						if err := validateRateLimit(&rateLimit); err != nil {
							return err
						}
						if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
							return err
						}
						providerConfig.RateLimitID = &rateLimit.ID
					}
					if err := h.configStore.CreateVirtualKeyProviderConfig(ctx, providerConfig, tx); err != nil {
						return err
					}
			} else {
			// Update existing provider config
			existing, ok := existingConfigsMap[*pc.ID]
			if !ok {
				return fmt.Errorf("provider config %d does not belong to this virtual key", *pc.ID)
			}
			requestConfigsMap[*pc.ID] = true
			existing.Provider = pc.Provider
			existing.Weight = &pc.Weight
			existing.AllowedModels = pc.AllowedModels

				// Get keys for this provider config if specified
				var keys []configstoreTables.TableKey
				if len(pc.KeyIDs) > 0 {
					var err error
					keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
					if err != nil {
						return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
					}
					if len(keys) != len(pc.KeyIDs) {
						return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
					}
				}
				existing.Keys = keys

				// Handle budget updates for provider config
					if pc.Budget != nil {
						if existing.BudgetID != nil {
							// Update existing budget
							budget := configstoreTables.TableBudget{}
							if err := tx.First(&budget, "id = ?", *existing.BudgetID).Error; err != nil {
								return err
							}
							if pc.Budget.MaxLimit != nil {
								budget.MaxLimit = *pc.Budget.MaxLimit
							}
							if pc.Budget.ResetDuration != nil {
								budget.ResetDuration = *pc.Budget.ResetDuration
							}
							if err := validateBudget(&budget); err != nil {
								return err
							}
							if err := h.configStore.UpdateBudget(ctx, &budget, tx); err != nil {
								return err
							}
						} else {
							// Create new budget for existing provider config
							if pc.Budget.MaxLimit == nil || pc.Budget.ResetDuration == nil {
								return fmt.Errorf("both max_limit and reset_duration are required when creating a new provider budget")
							}
							if *pc.Budget.MaxLimit < 0 {
								return fmt.Errorf("provider config budget max_limit cannot be negative: %.2f", *pc.Budget.MaxLimit)
							}
							if _, err := configstoreTables.ParseDuration(*pc.Budget.ResetDuration); err != nil {
								return fmt.Errorf("invalid provider config budget reset duration format: %s", *pc.Budget.ResetDuration)
							}
							budget := configstoreTables.TableBudget{
								ID:            uuid.NewString(),
								MaxLimit:      *pc.Budget.MaxLimit,
								ResetDuration: *pc.Budget.ResetDuration,
								LastReset:     time.Now(),
								CurrentUsage:  0,
							}
							if err := validateBudget(&budget); err != nil {
								return err
							}
							if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
								return err
							}
							existing.BudgetID = &budget.ID
						}
					}
					// Handle rate limit updates for provider config
					if pc.RateLimit != nil {
						if existing.RateLimitID != nil {
							// Update existing rate limit
							rateLimit := configstoreTables.TableRateLimit{}
							if err := tx.First(&rateLimit, "id = ?", *existing.RateLimitID).Error; err != nil {
								return err
							}
							if pc.RateLimit.TokenMaxLimit != nil {
								rateLimit.TokenMaxLimit = pc.RateLimit.TokenMaxLimit
							}
							if pc.RateLimit.TokenResetDuration != nil {
								rateLimit.TokenResetDuration = pc.RateLimit.TokenResetDuration
							}
							if pc.RateLimit.RequestMaxLimit != nil {
								rateLimit.RequestMaxLimit = pc.RateLimit.RequestMaxLimit
							}
							if pc.RateLimit.RequestResetDuration != nil {
								rateLimit.RequestResetDuration = pc.RateLimit.RequestResetDuration
							}
							if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
								return err
							}
						} else {
							// Create new rate limit for existing provider config
							rateLimit := configstoreTables.TableRateLimit{
								ID:                   uuid.NewString(),
								TokenMaxLimit:        pc.RateLimit.TokenMaxLimit,
								TokenResetDuration:   pc.RateLimit.TokenResetDuration,
								RequestMaxLimit:      pc.RateLimit.RequestMaxLimit,
								RequestResetDuration: pc.RateLimit.RequestResetDuration,
								TokenLastReset:       time.Now(),
								RequestLastReset:     time.Now(),
							}
							if err := validateRateLimit(&rateLimit); err != nil {
								return err
							}
							if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
								return err
							}
							existing.RateLimitID = &rateLimit.ID
						}
					}
					if err := h.configStore.UpdateVirtualKeyProviderConfig(ctx, &existing, tx); err != nil {
						return err
					}
				}
			}
			// Delete provider configs that are not in the request
			for id := range existingConfigsMap {
				if !requestConfigsMap[id] {
					if err := h.configStore.DeleteVirtualKeyProviderConfig(ctx, id, tx); err != nil {
						return err
					}
				}
			}
		}
		if req.MCPConfigs != nil {
			// Check for duplicate MCPClientName values among all configs before processing
			seenMCPClientNames := make(map[string]bool)
			for _, mc := range req.MCPConfigs {
				if seenMCPClientNames[mc.MCPClientName] {
					return fmt.Errorf("duplicate mcp_client_name: %s", mc.MCPClientName)
				}
				seenMCPClientNames[mc.MCPClientName] = true
			}
			// Get existing MCP configs for comparison
			var existingMCPConfigs []configstoreTables.TableVirtualKeyMCPConfig
			if err := tx.Where("virtual_key_id = ?", vk.ID).Find(&existingMCPConfigs).Error; err != nil {
				return err
			}
			// Create maps for easier lookup
			existingMCPConfigsMap := make(map[uint]configstoreTables.TableVirtualKeyMCPConfig)
			for _, config := range existingMCPConfigs {
				existingMCPConfigsMap[config.ID] = config
			}
			requestMCPConfigsMap := make(map[uint]bool)
			// Process new configs: create new ones and update existing ones
			for _, mc := range req.MCPConfigs {
				if mc.ID == nil {
					mcpClient, err := h.configStore.GetMCPClientByName(ctx, mc.MCPClientName)
					if err != nil {
						return fmt.Errorf("failed to get MCP client: %w", err)
					}
					// Create new MCP config
					if err := h.configStore.CreateVirtualKeyMCPConfig(ctx, &configstoreTables.TableVirtualKeyMCPConfig{
						VirtualKeyID:   vk.ID,
						MCPClientID:    mcpClient.ID,
						ToolsToExecute: mc.ToolsToExecute,
					}, tx); err != nil {
						return err
					}
				} else {
					// Update existing MCP config
					existing, ok := existingMCPConfigsMap[*mc.ID]
					if !ok {
						return fmt.Errorf("MCP config %d does not belong to this virtual key", *mc.ID)
					}
					requestMCPConfigsMap[*mc.ID] = true
					existing.ToolsToExecute = mc.ToolsToExecute
					if err := h.configStore.UpdateVirtualKeyMCPConfig(ctx, &existing, tx); err != nil {
						return err
					}
				}
			}
			// Delete MCP configs that are not in the request
			for id := range existingMCPConfigsMap {
				if !requestMCPConfigsMap[id] {
					if err := h.configStore.DeleteVirtualKeyMCPConfig(ctx, id, tx); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		errMsg := err.Error()
		// Check if this is a duplicate MCPClientName error and return 400 instead of 500
		if strings.Contains(errMsg, "duplicate mcp_client_name:") ||
			strings.Contains(errMsg, "already exists'") ||
			strings.Contains(errMsg, "duplicate key") {
			SendError(ctx, 400, fmt.Sprintf("Failed to update virtual key: %v", err))
			return
		}
		SendError(ctx, 500, fmt.Sprintf("Failed to update virtual key: %v", err))
		return
	}
	// Load relationships for response
	preloadedVk, err := h.configStore.GetVirtualKey(ctx, vk.ID)
	if err != nil {
		logger.Error("failed to load relationships for updated VK: %v", err)
		preloadedVk = vk
	}
	h.governanceManager.ReloadVirtualKey(ctx, vk.ID)
	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key updated successfully",
		"virtual_key": preloadedVk,
	})
}

// deleteVirtualKey handles DELETE /api/governance/virtual-keys/{vk_id} - Delete a virtual key
func (h *GovernanceHandler) deleteVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)
	// Fetch the virtual key from the database to get the budget and rate limit
	vk, err := h.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}
	// Removing key from in-memory store
	err = h.governanceManager.RemoveVirtualKey(ctx, vk.ID)
	if err != nil {
		// But we ignore this error because its not
		logger.Error("failed to remove virtual key: %v", err)
	}
	// Deleting key from database
	if err := h.configStore.DeleteVirtualKey(ctx, vkID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Virtual key not found")
			return
		}
		logger.Error("failed to delete virtual key: %v", err)
		SendError(ctx, 500, "Failed to delete virtual key")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Virtual key deleted successfully",
	})
}

// Team CRUD Operations

// getTeams handles GET /api/governance/teams - Get all teams
func (h *GovernanceHandler) getTeams(ctx *fasthttp.RequestCtx) {
	customerID := string(ctx.QueryArgs().Peek("customer_id"))
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		if customerID != "" {
			teams := make(map[string]*configstoreTables.TableTeam)
			for _, team := range data.Teams {
				if team.CustomerID != nil && *team.CustomerID == customerID {
					teams[team.ID] = team
				}
			}
			SendJSON(ctx, map[string]interface{}{
				"teams": teams,
				"count": len(teams),
			})
		} else {
			SendJSON(ctx, map[string]interface{}{
				"teams": data.Teams,
				"count": len(data.Teams),
			})
		}
		return
	}
	// Preload relationships for complete information
	teams, err := h.configStore.GetTeams(ctx, customerID)
	if err != nil {
		logger.Error("failed to retrieve teams: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to retrieve teams: %v", err))
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"teams": teams,
		"count": len(teams),
	})
}

// createTeam handles POST /api/governance/teams - Create a new team
func (h *GovernanceHandler) createTeam(ctx *fasthttp.RequestCtx) {
	var req CreateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Team name is required")
		return
	}
	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit))
			return
		}
		// Validate reset duration format
		if _, err := configstoreTables.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration))
			return
		}
	}
	// Creating team in database
	var team configstoreTables.TableTeam
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		team = configstoreTables.TableTeam{
			ID:         uuid.NewString(),
			Name:       req.Name,
			CustomerID: req.CustomerID,
		}
		if req.Budget != nil {
			budget := configstoreTables.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			team.BudgetID = &budget.ID
		}
		if err := h.configStore.CreateTeam(ctx, &team, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		logger.Error("failed to create team: %v", err)
		SendError(ctx, 500, "failed to create team")
		return
	}
	// Reloading team from in-memory store
	preloadedTeam, err := h.governanceManager.ReloadTeam(ctx, team.ID)
	if err != nil {
		logger.Error("failed to reload team: %v", err)
		preloadedTeam = &team
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Team created successfully",
		"team":    preloadedTeam,
	})
}

// getTeam handles GET /api/governance/teams/{team_id} - Get a specific team
func (h *GovernanceHandler) getTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		team, ok := data.Teams[teamID]
		if !ok {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"team": team,
		})
		return
	}
	team, err := h.configStore.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve team")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"team": team,
	})
}

// updateTeam handles PUT /api/governance/teams/{team_id} - Update a team
func (h *GovernanceHandler) updateTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var req UpdateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Fetching team from database
	team, err := h.configStore.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve team")
		return
	}
	// Updating team in database
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Update fields if provided
		if req.Name != nil {
			team.Name = *req.Name
		}
		if req.CustomerID != nil {
			team.CustomerID = req.CustomerID
		}
		// Handle budget updates
		if req.Budget != nil {
			if team.BudgetID != nil {
				// Update existing budget
				budget, err := h.configStore.GetBudget(ctx, *team.BudgetID, tx)
				if err != nil {
					return err
				}
				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := h.configStore.UpdateBudget(ctx, budget, tx); err != nil {
					return err
				}
				team.Budget = budget
			} else {
				// Create new budget
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				team.BudgetID = &budget.ID
				team.Budget = &budget
			}
		}
		if err := h.configStore.UpdateTeam(ctx, team, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to update team")
		return
	}
	// Reloading team from in-memory store
	preloadedTeam, err := h.governanceManager.ReloadTeam(ctx, team.ID)
	if err != nil {
		logger.Error("failed to reload team: %v", err)
		preloadedTeam = team
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Team updated successfully",
		"team":    preloadedTeam,
	})
}

// deleteTeam handles DELETE /api/governance/teams/{team_id} - Delete a team
func (h *GovernanceHandler) deleteTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)
	team, err := h.configStore.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve team")
		return
	}
	// Removing team from in-memory store
	err = h.governanceManager.RemoveTeam(ctx, team.ID)
	if err != nil {
		// But we ignore this error because its not
		logger.Error("failed to remove team: %v", err)
	}
	if err := h.configStore.DeleteTeam(ctx, teamID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Team not found")
			return
		}
		SendError(ctx, 500, "Failed to delete team")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Team deleted successfully",
	})
}

// Customer CRUD Operations

// getCustomers handles GET /api/governance/customers - Get all customers
func (h *GovernanceHandler) getCustomers(ctx *fasthttp.RequestCtx) {
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"customers": data.Customers,
			"count":     len(data.Customers),
		})
		return
	}
	customers, err := h.configStore.GetCustomers(ctx)
	if err != nil {
		logger.Error("failed to retrieve customers: %v", err)
		SendError(ctx, 500, "failed to retrieve customers")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"customers": customers,
		"count":     len(customers),
	})
}

// createCustomer handles POST /api/governance/customers - Create a new customer
func (h *GovernanceHandler) createCustomer(ctx *fasthttp.RequestCtx) {
	var req CreateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Customer name is required")
		return
	}
	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit))
			return
		}
		// Validate reset duration format
		if _, err := configstoreTables.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration))
			return
		}
	}
	var customer configstoreTables.TableCustomer
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		customer = configstoreTables.TableCustomer{
			ID:   uuid.NewString(),
			Name: req.Name,
		}

		if req.Budget != nil {
			budget := configstoreTables.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			customer.BudgetID = &budget.ID
		}
		if err := h.configStore.CreateCustomer(ctx, &customer, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		SendError(ctx, 500, "failed to create customer")
		return
	}
	preloadedCustomer, err := h.governanceManager.ReloadCustomer(ctx, customer.ID)
	if err != nil {
		logger.Error("failed to reload customer: %v", err)
		preloadedCustomer = &customer
	}
	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer created successfully",
		"customer": preloadedCustomer,
	})
}

// getCustomer handles GET /api/governance/customers/{customer_id} - Get a specific customer
func (h *GovernanceHandler) getCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		customer, ok := data.Customers[customerID]
		if !ok {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"customer": customer,
		})
		return
	}
	customer, err := h.configStore.GetCustomer(ctx, customerID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"customer": customer,
	})
}

// updateCustomer handles PUT /api/governance/customers/{customer_id} - Update a customer
func (h *GovernanceHandler) updateCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)
	var req UpdateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Fetching customer from database
	customer, err := h.configStore.GetCustomer(ctx, customerID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer")
		return
	}
	// Updating customer in database
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Update fields if provided
		if req.Name != nil {
			customer.Name = *req.Name
		}
		// Handle budget updates
		if req.Budget != nil {
			if customer.BudgetID != nil {
				// Update existing budget
				budget, err := h.configStore.GetBudget(ctx, *customer.BudgetID, tx)
				if err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := h.configStore.UpdateBudget(ctx, budget, tx); err != nil {
					return err
				}
				customer.Budget = budget
			} else {
				// Create new budget
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				customer.BudgetID = &budget.ID
				customer.Budget = &budget
			}
		}
		if err := h.configStore.UpdateCustomer(ctx, customer, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to update customer")
		return
	}

	preloadedCustomer, err := h.governanceManager.ReloadCustomer(ctx, customer.ID)
	if err != nil {
		logger.Error("failed to reload customer: %v", err)
		preloadedCustomer = customer
	}

	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer updated successfully",
		"customer": preloadedCustomer,
	})
}

// deleteCustomer handles DELETE /api/governance/customers/{customer_id} - Delete a customer
func (h *GovernanceHandler) deleteCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	customer, err := h.configStore.GetCustomer(ctx, customerID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer")
		return
	}
	err = h.governanceManager.RemoveCustomer(ctx, customer.ID)
	if err != nil {
		// But we ignore this error because its not
		logger.Error("failed to remove customer: %v", err)
	}
	if err := h.configStore.DeleteCustomer(ctx, customerID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Customer not found")
			return
		}
		SendError(ctx, 500, "Failed to delete customer")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Customer deleted successfully",
	})
}

// Budget and Rate Limit GET operations

// getBudgets handles GET /api/governance/budgets - Get all budgets
func (h *GovernanceHandler) getBudgets(ctx *fasthttp.RequestCtx) {
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"budgets": data.Budgets,
			"count":   len(data.Budgets),
		})
		return
	}
	budgets, err := h.configStore.GetBudgets(ctx)
	if err != nil {
		logger.Error("failed to retrieve budgets: %v", err)
		SendError(ctx, 500, "failed to retrieve budgets")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"budgets": budgets,
		"count":   len(budgets),
	})
}

// getRateLimits handles GET /api/governance/rate-limits - Get all rate limits
func (h *GovernanceHandler) getRateLimits(ctx *fasthttp.RequestCtx) {
	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"rate_limits": data.RateLimits,
			"count":       len(data.RateLimits),
		})
		return
	}
	rateLimits, err := h.configStore.GetRateLimits(ctx)
	if err != nil {
		logger.Error("failed to retrieve rate limits: %v", err)
		SendError(ctx, 500, "failed to retrieve rate limits")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"rate_limits": rateLimits,
		"count":       len(rateLimits),
	})
}

// validateRateLimit validates the rate limit
func validateRateLimit(rateLimit *configstoreTables.TableRateLimit) error {
	if rateLimit.TokenMaxLimit != nil && (*rateLimit.TokenMaxLimit < 0 || *rateLimit.TokenMaxLimit == 0) {
		return fmt.Errorf("rate limit token max limit cannot be negative or zero: %d", *rateLimit.TokenMaxLimit)
	}
	// Only require token reset duration if token limit is set
	if rateLimit.TokenMaxLimit != nil {
		if rateLimit.TokenResetDuration == nil {
			return fmt.Errorf("rate limit token reset duration is required")
		}
		if _, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err != nil {
			return fmt.Errorf("invalid rate limit token reset duration format: %s", *rateLimit.TokenResetDuration)
		}
	}
	if rateLimit.RequestMaxLimit != nil && (*rateLimit.RequestMaxLimit < 0 || *rateLimit.RequestMaxLimit == 0) {
		return fmt.Errorf("rate limit request max limit cannot be negative or zero: %d", *rateLimit.RequestMaxLimit)
	}
	// Only require request reset duration if request limit is set
	if rateLimit.RequestMaxLimit != nil {
		if rateLimit.RequestResetDuration == nil {
			return fmt.Errorf("rate limit request reset duration is required")
		}
		if _, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err != nil {
			return fmt.Errorf("invalid rate limit request reset duration format: %s", *rateLimit.RequestResetDuration)
		}
	}
	return nil
}

// validateBudget validates the budget
func validateBudget(budget *configstoreTables.TableBudget) error {
	if budget.MaxLimit < 0 || budget.MaxLimit == 0 {
		return fmt.Errorf("budget max limit cannot be negative or zero: %.2f", budget.MaxLimit)
	}
	if budget.ResetDuration == "" {
		return fmt.Errorf("budget reset duration is required")
	}
	if _, err := configstoreTables.ParseDuration(budget.ResetDuration); err != nil {
		return fmt.Errorf("invalid budget reset duration format: %s", budget.ResetDuration)
	}
	return nil
}
