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
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/governance"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// GovernanceHandler manages HTTP requests for governance operations
type GovernanceHandler struct {
	plugin      *governance.GovernancePlugin
	pluginStore *governance.GovernanceStore
	db          *gorm.DB
	logger      schemas.Logger
}

// NewGovernanceHandler creates a new governance handler instance
func NewGovernanceHandler(plugin *governance.GovernancePlugin, db *gorm.DB, logger schemas.Logger) *GovernanceHandler {
	return &GovernanceHandler{
		plugin:      plugin,
		pluginStore: plugin.GetGovernanceStore(),
		db:          db,
		logger:      logger,
	}
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
	r.GET("/api/governance/virtual-keys", h.GetVirtualKeys)
	r.POST("/api/governance/virtual-keys", h.CreateVirtualKey)
	r.GET("/api/governance/virtual-keys/{vk_id}", h.GetVirtualKey)
	r.PUT("/api/governance/virtual-keys/{vk_id}", h.UpdateVirtualKey)
	r.DELETE("/api/governance/virtual-keys/{vk_id}", h.DeleteVirtualKey)

	// Team CRUD operations
	r.GET("/api/governance/teams", h.GetTeams)
	r.POST("/api/governance/teams", h.CreateTeam)
	r.GET("/api/governance/teams/{team_id}", h.GetTeam)
	r.PUT("/api/governance/teams/{team_id}", h.UpdateTeam)
	r.DELETE("/api/governance/teams/{team_id}", h.DeleteTeam)

	// Customer CRUD operations
	r.GET("/api/governance/customers", h.GetCustomers)
	r.POST("/api/governance/customers", h.CreateCustomer)
	r.GET("/api/governance/customers/{customer_id}", h.GetCustomer)
	r.PUT("/api/governance/customers/{customer_id}", h.UpdateCustomer)
	r.DELETE("/api/governance/customers/{customer_id}", h.DeleteCustomer)
}

// Virtual Key CRUD Operations

// GetVirtualKeys handles GET /api/governance/virtual-keys - Get all virtual keys with relationships
func (h *GovernanceHandler) GetVirtualKeys(ctx *fasthttp.RequestCtx) {
	var virtualKeys []governance.VirtualKey

	// Preload all relationships for complete information
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").Find(&virtualKeys).Error; err != nil {
		SendError(ctx, 500, "Failed to retrieve virtual keys", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"virtual_keys": virtualKeys,
		"count":        len(virtualKeys),
	}, h.logger)
}

// CreateVirtualKey handles POST /api/governance/virtual-keys - Create a new virtual key
func (h *GovernanceHandler) CreateVirtualKey(ctx *fasthttp.RequestCtx) {
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
		if _, err := governance.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), h.logger)
			return
		}
	}

	// Set defaults
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	var vk governance.VirtualKey
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		vk = governance.VirtualKey{
			ID:               uuid.NewString(),
			Name:             req.Name,
			Value:            uuid.NewString(),
			Description:      req.Description,
			AllowedModels:    req.AllowedModels,
			AllowedProviders: req.AllowedProviders,
			TeamID:           req.TeamID,
			CustomerID:       req.CustomerID,
			IsActive:         isActive,
		}

		if req.Budget != nil {
			budget := governance.Budget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := tx.Create(&budget).Error; err != nil {
				return err
			}
			vk.BudgetID = &budget.ID
		}

		if req.RateLimit != nil {
			rateLimit := governance.RateLimit{
				ID:                   uuid.NewString(),
				TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
				TokenResetDuration:   req.RateLimit.TokenResetDuration,
				RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
				RequestResetDuration: req.RateLimit.RequestResetDuration,
				TokenLastReset:       time.Now(),
				RequestLastReset:     time.Now(),
			}
			if err := tx.Create(&rateLimit).Error; err != nil {
				return err
			}
			vk.RateLimitID = &rateLimit.ID
		}

		if err := tx.Create(&vk).Error; err != nil {
			SendError(ctx, 500, "Failed to create virtual key", h.logger)
			return err
		}

		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to create virtual key", h.logger)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").First(&vk, "id = ?", vk.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for created VK: %w", err))
	}

	// Add to in-memory store
	h.pluginStore.CreateVirtualKeyInMemory(&vk)

	// If budget was created, add it to in-memory store
	if vk.BudgetID != nil {
		h.pluginStore.CreateBudgetInMemory(vk.Budget)
	}

	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key created successfully",
		"virtual_key": vk,
	}, h.logger)
}

// GetVirtualKey handles GET /api/governance/virtual-keys/{vk_id} - Get a specific virtual key
func (h *GovernanceHandler) GetVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	var vk governance.VirtualKey
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").First(&vk, "id = ?", vkID).Error; err != nil {
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

// UpdateVirtualKey handles PUT /api/governance/virtual-keys/{vk_id} - Update a virtual key
func (h *GovernanceHandler) UpdateVirtualKey(ctx *fasthttp.RequestCtx) {
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

	var vk governance.VirtualKey
	if err := h.db.First(&vk, "id = ?", vkID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Virtual key not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key", h.logger)
		return
	}

	if err := h.db.Transaction(func(tx *gorm.DB) error {
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
				budget := governance.Budget{}
				if err := tx.First(&budget, "id = ?", *vk.BudgetID).Error; err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := tx.Save(&budget).Error; err != nil {
					return err
				}
			} else {
				// Create new budget
				budget := governance.Budget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := tx.Create(&budget).Error; err != nil {
					return err
				}
				vk.BudgetID = &budget.ID
			}
		}

		// Handle rate limit updates
		if req.RateLimit != nil {
			if vk.RateLimitID != nil {
				// Update existing rate limit
				rateLimit := governance.RateLimit{}
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

				if err := tx.Save(&rateLimit).Error; err != nil {
					return err
				}
			} else {
				// Create new rate limit
				rateLimit := governance.RateLimit{
					ID:                   uuid.NewString(),
					TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
					TokenResetDuration:   req.RateLimit.TokenResetDuration,
					RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
					RequestResetDuration: req.RateLimit.RequestResetDuration,
					TokenLastReset:       time.Now(),
					RequestLastReset:     time.Now(),
				}
				if err := tx.Create(&rateLimit).Error; err != nil {
					return err
				}
				vk.RateLimitID = &rateLimit.ID
			}
		}

		if err := tx.Save(&vk).Error; err != nil {
			return err
		}

		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to update virtual key", h.logger)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").First(&vk, "id = ?", vk.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for updated VK: %w", err))
	}

	// Update in-memory cache for budget and rate limit changes
	if req.Budget != nil && vk.BudgetID != nil {
		if err := h.pluginStore.UpdateBudgetInMemory(vk.Budget); err != nil {
			h.logger.Error(fmt.Errorf("failed to update budget cache: %w", err))
		}
	}

	// Update in-memory store
	h.pluginStore.UpdateVirtualKeyInMemory(&vk)

	SendJSON(ctx, map[string]interface{}{
		"message":     "Virtual key updated successfully",
		"virtual_key": vk,
	}, h.logger)
}

// DeleteVirtualKey handles DELETE /api/governance/virtual-keys/{vk_id} - Delete a virtual key
func (h *GovernanceHandler) DeleteVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	// Fetch the virtual key from the database to get the budget and rate limit
	var vk governance.VirtualKey
	if err := h.db.First(&vk, "id = ?", vkID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Virtual key not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key", h.logger)
		return
	}

	budgetID := vk.BudgetID

	result := h.db.Delete(&governance.VirtualKey{}, "id = ?", vkID)
	if result.Error != nil {
		SendError(ctx, 500, "Failed to delete virtual key", h.logger)
		return
	}

	if result.RowsAffected == 0 {
		SendError(ctx, 404, "Virtual key not found", h.logger)
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

// GetTeams handles GET /api/governance/teams - Get all teams
func (h *GovernanceHandler) GetTeams(ctx *fasthttp.RequestCtx) {
	var teams []governance.Team

	// Preload relationships for complete information
	query := h.db.Preload("Customer").Preload("Budget")

	// Optional filtering by customer
	if customerID := string(ctx.QueryArgs().Peek("customer_id")); customerID != "" {
		query = query.Where("customer_id = ?", customerID)
	}

	if err := query.Find(&teams).Error; err != nil {
		SendError(ctx, 500, "Failed to retrieve teams", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"teams": teams,
		"count": len(teams),
	}, h.logger)
}

// CreateTeam handles POST /api/governance/teams - Create a new team
func (h *GovernanceHandler) CreateTeam(ctx *fasthttp.RequestCtx) {
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
		if _, err := governance.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), h.logger)
			return
		}
	}

	var team governance.Team
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		team = governance.Team{
			ID:         uuid.NewString(),
			Name:       req.Name,
			CustomerID: req.CustomerID,
		}

		if req.Budget != nil {
			budget := governance.Budget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := tx.Create(&budget).Error; err != nil {
				return err
			}
			team.BudgetID = &budget.ID
		}

		if err := tx.Create(&team).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to create team", h.logger)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", team.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for created team: %w", err))
	}

	// Add to in-memory store
	h.pluginStore.CreateTeamInMemory(&team)

	// If budget was created, add it to in-memory store
	if team.BudgetID != nil {
		h.pluginStore.CreateBudgetInMemory(team.Budget)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Team created successfully",
		"team":    team,
	}, h.logger)
}

// GetTeam handles GET /api/governance/teams/{team_id} - Get a specific team
func (h *GovernanceHandler) GetTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var team governance.Team
	if err := h.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", teamID).Error; err != nil {
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

// UpdateTeam handles PUT /api/governance/teams/{team_id} - Update a team
func (h *GovernanceHandler) UpdateTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var req UpdateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	var team governance.Team
	if err := h.db.First(&team, "id = ?", teamID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Team not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve team", h.logger)
		return
	}

	if err := h.db.Transaction(func(tx *gorm.DB) error {
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
				budget := governance.Budget{}
				if err := tx.First(&budget, "id = ?", *team.BudgetID).Error; err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := tx.Save(&budget).Error; err != nil {
					return err
				}
			} else {
				// Create new budget
				budget := governance.Budget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := tx.Create(&budget).Error; err != nil {
					return err
				}
				team.BudgetID = &budget.ID
			}
		}

		if err := tx.Save(&team).Error; err != nil {
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
			h.logger.Error(fmt.Errorf("failed to update budget cache: %w", err))
		}
	}

	// Load relationships for response
	if err := h.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", team.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for updated team: %w", err))
	}

	// Update in-memory store
	h.pluginStore.UpdateTeamInMemory(&team)

	SendJSON(ctx, map[string]interface{}{
		"message": "Team updated successfully",
		"team":    team,
	}, h.logger)
}

// DeleteTeam handles DELETE /api/governance/teams/{team_id} - Delete a team
func (h *GovernanceHandler) DeleteTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var team governance.Team
	if err := h.db.First(&team, "id = ?", teamID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Team not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve team", h.logger)
		return
	}

	budgetID := team.BudgetID

	result := h.db.Delete(&governance.Team{}, "id = ?", teamID)
	if result.Error != nil {
		SendError(ctx, 500, "Failed to delete team", h.logger)
		return
	}

	if result.RowsAffected == 0 {
		SendError(ctx, 404, "Team not found", h.logger)
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

// GetCustomers handles GET /api/governance/customers - Get all customers
func (h *GovernanceHandler) GetCustomers(ctx *fasthttp.RequestCtx) {
	var customers []governance.Customer

	// Preload relationships for complete information
	if err := h.db.Preload("Teams").Preload("Budget").Find(&customers).Error; err != nil {
		SendError(ctx, 500, "Failed to retrieve customers", h.logger)
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"customers": customers,
		"count":     len(customers),
	}, h.logger)
}

// CreateCustomer handles POST /api/governance/customers - Create a new customer
func (h *GovernanceHandler) CreateCustomer(ctx *fasthttp.RequestCtx) {
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
		if _, err := governance.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), h.logger)
			return
		}
	}

	var customer governance.Customer
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		customer = governance.Customer{
			ID:   uuid.NewString(),
			Name: req.Name,
		}

		if req.Budget != nil {
			budget := governance.Budget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     time.Now(),
				CurrentUsage:  0,
			}
			if err := tx.Create(&budget).Error; err != nil {
				return err
			}
			customer.BudgetID = &budget.ID
		}

		if err := tx.Create(&customer).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		SendError(ctx, 500, "Failed to create customer", h.logger)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", customer.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for created customer: %w", err))
	}

	// Add to in-memory store
	h.pluginStore.CreateCustomerInMemory(&customer)

	// If budget was created, add it to in-memory store
	if customer.BudgetID != nil {
		h.pluginStore.CreateBudgetInMemory(customer.Budget)
	}

	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer created successfully",
		"customer": customer,
	}, h.logger)
}

// GetCustomer handles GET /api/governance/customers/{customer_id} - Get a specific customer
func (h *GovernanceHandler) GetCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	var customer governance.Customer
	if err := h.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", customerID).Error; err != nil {
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

// UpdateCustomer handles PUT /api/governance/customers/{customer_id} - Update a customer
func (h *GovernanceHandler) UpdateCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	var req UpdateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON", h.logger)
		return
	}

	var customer governance.Customer
	if err := h.db.First(&customer, "id = ?", customerID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Customer not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer", h.logger)
		return
	}

	if err := h.db.Transaction(func(tx *gorm.DB) error {
		// Update fields if provided
		if req.Name != nil {
			customer.Name = *req.Name
		}

		// Handle budget updates
		if req.Budget != nil {
			if customer.BudgetID != nil {
				// Update existing budget
				budget := governance.Budget{}
				if err := tx.First(&budget, "id = ?", *customer.BudgetID).Error; err != nil {
					return err
				}

				if req.Budget.MaxLimit != nil {
					budget.MaxLimit = *req.Budget.MaxLimit
				}
				if req.Budget.ResetDuration != nil {
					budget.ResetDuration = *req.Budget.ResetDuration
				}

				if err := tx.Save(&budget).Error; err != nil {
					return err
				}
			} else {
				// Create new budget
				budget := governance.Budget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     time.Now(),
					CurrentUsage:  0,
				}
				if err := tx.Create(&budget).Error; err != nil {
					return err
				}
				customer.BudgetID = &budget.ID
			}
		}

		if err := tx.Save(&customer).Error; err != nil {
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
			h.logger.Error(fmt.Errorf("failed to update budget cache: %w", err))
		}
	}

	// Load relationships for response
	if err := h.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", customer.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for updated customer: %w", err))
	}

	// Update in-memory store
	h.pluginStore.UpdateCustomerInMemory(&customer)

	SendJSON(ctx, map[string]interface{}{
		"message":  "Customer updated successfully",
		"customer": customer,
	}, h.logger)
}

// DeleteCustomer handles DELETE /api/governance/customers/{customer_id} - Delete a customer
func (h *GovernanceHandler) DeleteCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	var customer governance.Customer
	if err := h.db.First(&customer, "id = ?", customerID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			SendError(ctx, 404, "Customer not found", h.logger)
			return
		}
		SendError(ctx, 500, "Failed to retrieve customer", h.logger)
		return
	}

	budgetID := customer.BudgetID

	result := h.db.Delete(&governance.Customer{}, "id = ?", customerID)
	if result.Error != nil {
		SendError(ctx, 500, "Failed to delete customer", h.logger)
		return
	}

	if result.RowsAffected == 0 {
		SendError(ctx, 404, "Customer not found", h.logger)
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
