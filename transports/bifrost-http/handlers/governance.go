// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all governance management functionality including CRUD operations for VKs, Rules, and configs.
package handlers

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// GovernanceHandler manages HTTP requests for governance operations
type GovernanceHandler struct {
	plugin      *governance.GovernancePlugin
	pluginStore *governance.GovernanceStore
	configStore configstore.ConfigStore
	logger      schemas.Logger
}

// NewGovernanceHandler creates a new governance handler instance
func NewGovernanceHandler(plugin *governance.GovernancePlugin, configStore configstore.ConfigStore, logger schemas.Logger) (*GovernanceHandler, error) {
	return &GovernanceHandler{
		plugin:      plugin,
		pluginStore: plugin.GetGovernanceStore(),
		configStore: configStore,
		logger:      logger,
	}, nil
}

// CreateVirtualKeyRequest represents the request body for creating a virtual key
type CreateVirtualKeyRequest struct {
	Name             string                  `json:"name" validate:"required"`
	Description      string                  `json:"description,omitempty"`
	AllowedModels    []string                `json:"allowed_models,omitempty"`    // Empty means all models allowed
	AllowedProviders []string                `json:"allowed_providers,omitempty"` // Empty means all providers allowed
	TeamID           *string                 `json:"team_id,omitempty"`           // Mutually exclusive with CustomerID
	CustomerID       *string                 `json:"customer_id,omitempty"`       // Mutually exclusive with TeamID
	Budget           *CreateBudgetRequest    `json:"budget,omitempty"`
	RateLimit        *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	KeyIDs           []string                `json:"key_ids,omitempty"` // List of DBKey UUIDs to associate with this VirtualKey
	IsActive         *bool                   `json:"is_active,omitempty"`
}

// UpdateVirtualKeyRequest represents the request body for updating a virtual key
type UpdateVirtualKeyRequest struct {
	Description      *string                 `json:"description,omitempty"`
	AllowedModels    *[]string               `json:"allowed_models,omitempty"`
	AllowedProviders *[]string               `json:"allowed_providers,omitempty"`
	TeamID           *string                 `json:"team_id,omitempty"`
	CustomerID       *string                 `json:"customer_id,omitempty"`
	Budget           *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit        *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	KeyIDs           *[]string               `json:"key_ids,omitempty"` // List of DBKey UUIDs to associate with this VirtualKey
	IsActive         *bool                   `json:"is_active,omitempty"`
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
func (h *GovernanceHandler) RegisterRoutes(r *router.Router) {
	// Virtual Key CRUD operations
	r.GET("/api/governance/virtual-keys", h.getVirtualKeys)
	r.POST("/api/governance/virtual-keys", h.createVirtualKey)
	r.GET("/api/governance/virtual-keys/{vk_id}", h.getVirtualKey)
	r.PUT("/api/governance/virtual-keys/{vk_id}", h.updateVirtualKey)
	r.DELETE("/api/governance/virtual-keys/{vk_id}", h.deleteVirtualKey)

	// Team CRUD operations
	r.GET("/api/governance/teams", h.getTeams)
	r.POST("/api/governance/teams", h.createTeam)
	r.GET("/api/governance/teams/{team_id}", h.getTeam)
	r.PUT("/api/governance/teams/{team_id}", h.updateTeam)
	r.DELETE("/api/governance/teams/{team_id}", h.deleteTeam)

	// Customer CRUD operations
	r.GET("/api/governance/customers", h.getCustomers)
	r.POST("/api/governance/customers", h.createCustomer)
	r.GET("/api/governance/customers/{customer_id}", h.getCustomer)
	r.PUT("/api/governance/customers/{customer_id}", h.updateCustomer)
	r.DELETE("/api/governance/customers/{customer_id}", h.deleteCustomer)
}

// Virtual Key CRUD Operations

// getVirtualKeys handles GET /api/governance/virtual-keys - Get all virtual keys with relationships
func (h *GovernanceHandler) getVirtualKeys(ctx *fasthttp.RequestCtx) {
	// Preload all relationships for complete information
	virtualKeys, err := h.configStore.GetVirtualKeys()
	if err != nil {
		h.logger.Error("failed to retrieve virtual keys: %v", err)
		SendError(ctx, 500, "Failed to retrieve virtual keys", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"virtual_keys": virtualKeys,
		"count":        len(virtualKeys),
	}, h.logger)
}

// createVirtualKey handles POST /api/governance/virtual-keys - Create a new virtual key
func (h *GovernanceHandler) createVirtualKey(ctx *fasthttp.RequestCtx) {
	var req CreateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Virtual key name is required", h.logger)
		return
	}

	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		SendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer", h.logger)
		return
	}

	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit), h.logger)
			return
		}
		// Validate reset duration format
		if _, err := configstore.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), h.logger)
			return
		}
	}

	// Set defaults
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	var vk configstore.TableVirtualKey
	if err := h.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
		// Get the keys if DBKeyIDs are provided
		var keys []configstore.TableKey
		if len(req.KeyIDs) > 0 {
			var err error
			keys, err = h.configStore.GetKeysByIDs(req.KeyIDs)
			if err != nil {
				return fmt.Errorf("failed to get keys by IDs: %w", err)
			}
			if len(keys) != len(req.KeyIDs) {
				return fmt.Errorf("some keys not found: expected %d, found %d", len(req.KeyIDs), len(keys))
			}
		}

		vk = configstore.TableVirtualKey{
			ID:               uuid.NewString(),
			Name:             req.Name,
			Value:            uuid.NewString(),
			Description:      req.Description,
			AllowedModels:    req.AllowedModels,
			AllowedProviders: req.AllowedProviders,
			TeamID:           req.TeamID,
			CustomerID:       req.CustomerID,
			IsActive:         isActive,
			Keys:             keys, // Set the keys for the many-to-many relationship
		}

		if req.Budget != nil {
			budget := configstore.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := h.configStore.CreateBudget(&budget, tx); err != nil {
				return err
			}
			vk.BudgetID = &budget.ID
		}

		if req.RateLimit != nil {
			rateLimit := configstore.TableRateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
				TokenResetDuration:   req.RateLimit.TokenResetDuration,
				RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
				RequestResetDuration: req.RateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := h.configStore.CreateRateLimit(&rateLimit, tx); err != nil {
				return err
			}
			vk.RateLimitID = &rateLimit.ID
		}

		if err := h.configStore.CreateVirtualKey(&vk, tx); err != nil {
			return err
		}

		return nil
	}); err != nil {
		SendError(ctx, 500, err.Error(), h.logger)
		return
	}

	// Load relationships for response
	preloadedVk, err := h.configStore.GetVirtualKey(vk.ID)
	if err != nil {
		h.logger.Error("failed to load relationships for created VK: %v", err)
		// If we can't load the full VK, use the basic one we just created
		preloadedVk = &vk
	}

	// Add to in-memory store
	h.pluginStore.CreateVirtualKeyInMemory(preloadedVk)

	// If budget was created, add it to in-memory store
	if vk.BudgetID != nil && preloadedVk.Budget != nil {
		h.pluginStore.CreateBudgetInMemory(preloadedVk.Budget)
	}

	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key created successfully",
		"virtual_key": preloadedVk,
	}, h.logger)
}

// getVirtualKey handles GET /api/governance/virtual-keys/{vk_id} - Get a specific virtual key
func (h *GovernanceHandler) getVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	vk, err := h.configStore.GetVirtualKey(vkID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Virtual key not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"virtual_key": vk,
	}, h.logger)
}

// updateVirtualKey handles PUT /api/governance/virtual-keys/{vk_id} - Update a virtual key
func (h *GovernanceHandler) updateVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	var req UpdateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		SendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer", h.logger)
		return
	}

	vk, err := h.configStore.GetVirtualKey(vkID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Virtual key not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key", h.logger)
		return
	}

	if err := h.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
		// Update fields if provided
		if req.Description != nil {
			vk.Description = *req.Description
		}
		if req.AllowedModels != nil {
			vk.AllowedModels = *req.AllowedModels
		}
		if req.AllowedProviders != nil {
			vk.AllowedProviders = *req.AllowedProviders
		}
		if req.TeamID != nil {
			vk.TeamID = req.TeamID
			vk.CustomerID = nil // Clear CustomerID if setting TeamID
		}
		if req.CustomerID != nil {
			vk.CustomerID = req.CustomerID
			vk.TeamID = nil // Clear TeamID if setting CustomerID
		}
		if req.IsActive != nil {
			vk.IsActive = *req.IsActive
		}

		// Handle budget updates
		if req.Budget != nil {
			if vk.BudgetID != nil {
				// Update existing budget
				budget := configstore.TableBudget{}
				if err := tx.First(&budget, "id = ?", *vk.BudgetID).Error; err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := h.configStore.UpdateBudget(&budget, tx); err != nil {
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
				if _, err := configstore.ParseDuration(*req.Budget.ResetDuration); err != nil {
					return fmt.Errorf("invalid reset duration format: %s", *req.Budget.ResetDuration)
				}
				// Storing now
				budget := configstore.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := h.configStore.CreateBudget(&budget, tx); err != nil {
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
				rateLimit := configstore.TableRateLimit{}
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

				if err := h.configStore.UpdateRateLimit(&rateLimit, tx); err != nil {
					return err
				}
			} else {
				// Create new rate limit
				rateLimit := configstore.TableRateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := h.configStore.CreateRateLimit(&rateLimit, tx); err != nil {
					return err
				}
				vk.RateLimitID = &rateLimit.ID
			}
		}

		// Handle DBKey associations if provided
		if req.KeyIDs != nil {
			// Get the keys if DBKeyIDs are provided
			var keys []configstore.TableKey
			if len(*req.KeyIDs) > 0 {
				var err error
				keys, err = h.configStore.GetKeysByIDs(*req.KeyIDs)
				if err != nil {
					return fmt.Errorf("failed to get keys by IDs: %w", err)
				}
				if len(keys) != len(*req.KeyIDs) {
					return fmt.Errorf("some keys not found: expected %d, found %d", len(*req.KeyIDs), len(keys))
				}
			}

			// Set the keys for the many-to-many relationship
			vk.Keys = keys
		}

		if err := h.configStore.UpdateVirtualKey(vk, tx); err != nil {
			return err
		}

		return nil
	}); err != nil {
		h.logger.Error("failed to update virtual key: %v", err)
		SendError(ctx, 500, "Failed to update virtual key", h.logger)
		return
	}

	// Load relationships for response
	preloadedVk, err := h.configStore.GetVirtualKey(vk.ID)
	if err != nil {
		h.logger.Error("failed to load relationships for updated VK: %v", err)
		preloadedVk = vk
	}

	// Update in-memory cache for budget and rate limit changes
	if req.Budget != nil && preloadedVk.BudgetID != nil {
		if err := h.pluginStore.UpdateBudgetInMemory(preloadedVk.Budget); err != nil {
			h.logger.Error("failed to update budget cache: %v", err)
		}
	}

	// Update in-memory store
	h.pluginStore.UpdateVirtualKeyInMemory(preloadedVk)

	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key updated successfully",
		"virtual_key": preloadedVk,
	}, h.logger)
}

// deleteVirtualKey handles DELETE /api/governance/virtual-keys/{vk_id} - Delete a virtual key
func (h *GovernanceHandler) deleteVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	// Fetch the virtual key from the database to get the budget and rate limit
	vk, err := h.configStore.GetVirtualKey(vkID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Virtual key not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key", h.logger)
		return
	}

	budgetID := vk.BudgetID

	if err := h.configStore.DeleteVirtualKey(vkID); err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Virtual key not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to delete virtual key", h.logger)
		return
	}

	// Remove from in-memory store
	h.pluginStore.DeleteVirtualKeyInMemory(vkID)

	// Remove Budget from in-memory store
	if budgetID != nil {
		h.pluginStore.DeleteBudgetInMemory(*budgetID)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Virtual key deleted successfully",
	}, h.logger)
}

// Team CRUD Operations

// getTeams handles GET /api/governance/teams - Get all teams
func (h *GovernanceHandler) getTeams(ctx *fasthttp.RequestCtx) {
	customerID := string(ctx.QueryArgs().Peek("customer_id"))

	// Preload relationships for complete information
	teams, err := h.configStore.GetTeams(customerID)
	if err != nil {
		h.logger.Error("failed to retrieve teams: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to retrieve teams: %v", err), h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"teams": teams,
		"count": len(teams),
	}, h.logger)
}

// createTeam handles POST /api/governance/teams - Create a new team
func (h *GovernanceHandler) createTeam(ctx *fasthttp.RequestCtx) {
	var req CreateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Team name is required", h.logger)
		return
	}

	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit), h.logger)
			return
		}
		// Validate reset duration format
		if _, err := configstore.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), h.logger)
			return
		}
	}

	var team configstore.TableTeam
	if err := h.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
		team = configstore.TableTeam{
			ID:         uuid.NewString(),
			Name:       req.Name,
			CustomerID: req.CustomerID,
		}

		if req.Budget != nil {
			budget := configstore.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := h.configStore.CreateBudget(&budget, tx); err != nil {
				return err
			}
			team.BudgetID = &budget.ID
		}

		if err := h.configStore.CreateTeam(&team, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		h.logger.Error("failed to create team: %v", err)
		SendError(ctx, 500, "failed to create team", h.logger)
		return
	}

	// Load relationships for response
	preloadedTeam, err := h.configStore.GetTeam(team.ID)
	if err != nil {
		h.logger.Error("failed to load relationships for created team: %v", err)
		preloadedTeam = &team
	}

	// Add to in-memory store
	h.pluginStore.CreateTeamInMemory(preloadedTeam)

	// If budget was created, add it to in-memory store
	if preloadedTeam.BudgetID != nil {
		h.pluginStore.CreateBudgetInMemory(preloadedTeam.Budget)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Team created successfully",
		"team":    preloadedTeam,
	}, h.logger)
}

// getTeam handles GET /api/governance/teams/{team_id} - Get a specific team
func (h *GovernanceHandler) getTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	team, err := h.configStore.GetTeam(teamID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Team not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve team", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"team": team,
	}, h.logger)
}

// updateTeam handles PUT /api/governance/teams/{team_id} - Update a team
func (h *GovernanceHandler) updateTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var req UpdateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	team, err := h.configStore.GetTeam(teamID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Team not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve team", h.logger)
		return
	}

	if err := h.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
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
				budget, err := h.configStore.GetBudget(*team.BudgetID, tx)
				if err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := h.configStore.UpdateBudget(budget, tx); err != nil {
					return err
				}
				team.Budget = budget
			} else {
				// Create new budget
				budget := configstore.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := h.configStore.CreateBudget(&budget, tx); err != nil {
					return err
				}
				team.BudgetID = &budget.ID
				team.Budget = &budget
			}
		}

		if err := h.configStore.UpdateTeam(team, tx); err != nil {
			return err
		}

		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to update team", h.logger)
		return
	}

	// Update in-memory cache for budget changes
	if req.Budget != nil && team.BudgetID != nil {
		if err := h.pluginStore.UpdateBudgetInMemory(team.Budget); err != nil {
			h.logger.Error("failed to update budget cache: %v", err)
		}
	}

	// Load relationships for response
	preloadedTeam, err := h.configStore.GetTeam(team.ID)
	if err != nil {
		h.logger.Error("failed to load relationships for updated team: %v", err)
		preloadedTeam = team
	}

	// Update in-memory store
	h.pluginStore.UpdateTeamInMemory(preloadedTeam)

	SendJSON(ctx, map[string]interface{}{
		"message": "Team updated successfully",
		"team":    preloadedTeam,
	}, h.logger)
}

// deleteTeam handles DELETE /api/governance/teams/{team_id} - Delete a team
func (h *GovernanceHandler) deleteTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	team, err := h.configStore.GetTeam(teamID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Team not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve team", h.logger)
		return
	}

	budgetID := team.BudgetID

	if err := h.configStore.DeleteTeam(teamID); err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Team not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to delete team", h.logger)
		return
	}

	// Remove from in-memory store
	h.pluginStore.DeleteTeamInMemory(teamID)

	// Remove Budget from in-memory store
	if budgetID != nil {
		h.pluginStore.DeleteBudgetInMemory(*budgetID)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Team deleted successfully",
	}, h.logger)
}

// Customer CRUD Operations

// getCustomers handles GET /api/governance/customers - Get all customers
func (h *GovernanceHandler) getCustomers(ctx *fasthttp.RequestCtx) {
	customers, err := h.configStore.GetCustomers()
	if err != nil {
		h.logger.Error("failed to retrieve customers: %v", err)
		SendError(ctx, 500, "failed to retrieve customers", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"customers": customers,
		"count":     len(customers),
	}, h.logger)
}

// createCustomer handles POST /api/governance/customers - Create a new customer
func (h *GovernanceHandler) createCustomer(ctx *fasthttp.RequestCtx) {
	var req CreateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "Customer name is required", h.logger)
		return
	}

	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit), h.logger)
			return
		}
		// Validate reset duration format
		if _, err := configstore.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), h.logger)
			return
		}
	}

	var customer configstore.TableCustomer
	if err := h.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
		customer = configstore.TableCustomer{
			ID:   uuid.NewString(),
			Name: req.Name,
		}

		if req.Budget != nil {
			budget := configstore.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := h.configStore.CreateBudget(&budget, tx); err != nil {
				return err
			}
			customer.BudgetID = &budget.ID
		}

		if err := h.configStore.CreateCustomer(&customer, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		SendError(ctx, 500, "failed to create customer", h.logger)
		return
	}

	// Load relationships for response
	preloadedCustomer, err := h.configStore.GetCustomer(customer.ID)
	if err != nil {
		h.logger.Error("failed to load relationships for created customer: %v", err)
		preloadedCustomer = &customer
	}

	// Add to in-memory store
	h.pluginStore.CreateCustomerInMemory(preloadedCustomer)

	// If budget was created, add it to in-memory store
	if preloadedCustomer.BudgetID != nil {
		h.pluginStore.CreateBudgetInMemory(preloadedCustomer.Budget)
	}

	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer created successfully",
		"customer": preloadedCustomer,
	}, h.logger)
}

// getCustomer handles GET /api/governance/customers/{customer_id} - Get a specific customer
func (h *GovernanceHandler) getCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	customer, err := h.configStore.GetCustomer(customerID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Customer not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"customer": customer,
	}, h.logger)
}

// updateCustomer handles PUT /api/governance/customers/{customer_id} - Update a customer
func (h *GovernanceHandler) updateCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	var req UpdateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	customer, err := h.configStore.GetCustomer(customerID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Customer not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer", h.logger)
		return
	}

	if err := h.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
		// Update fields if provided
		if req.Name != nil {
			customer.Name = *req.Name
		}

		// Handle budget updates
		if req.Budget != nil {
			if customer.BudgetID != nil {
				// Update existing budget
				budget, err := h.configStore.GetBudget(*customer.BudgetID, tx)
				if err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := h.configStore.UpdateBudget(budget, tx); err != nil {
					return err
				}
				customer.Budget = budget
			} else {
				// Create new budget
				budget := configstore.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := h.configStore.CreateBudget(&budget, tx); err != nil {
					return err
				}
				customer.BudgetID = &budget.ID
				customer.Budget = &budget
			}
		}

		if err := h.configStore.UpdateCustomer(customer, tx); err != nil {
			return err
		}

		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to update customer", h.logger)
		return
	}

	// Update in-memory cache for budget changes
	if req.Budget != nil && customer.BudgetID != nil {
		if err := h.pluginStore.UpdateBudgetInMemory(customer.Budget); err != nil {
			h.logger.Error("failed to update budget cache: %v", err)
		}
	}

	// Load relationships for response
	preloadedCustomer, err := h.configStore.GetCustomer(customer.ID)
	if err != nil {
		h.logger.Error("failed to load relationships for updated customer: %v", err)
		preloadedCustomer = customer
	}

	// Update in-memory store
	h.pluginStore.UpdateCustomerInMemory(preloadedCustomer)

	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer updated successfully",
		"customer": preloadedCustomer,
	}, h.logger)
}

// deleteCustomer handles DELETE /api/governance/customers/{customer_id} - Delete a customer
func (h *GovernanceHandler) deleteCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	customer, err := h.configStore.GetCustomer(customerID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Customer not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer", h.logger)
		return
	}

	budgetID := customer.BudgetID

	if err := h.configStore.DeleteCustomer(customerID); err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Customer not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to delete customer", h.logger)
		return
	}

	// Remove from in-memory store
	h.pluginStore.DeleteCustomerInMemory(customerID)

	// Remove Budget from in-memory store
	if budgetID != nil {
		h.pluginStore.DeleteBudgetInMemory(*budgetID)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Customer deleted successfully",
	}, h.logger)
}
