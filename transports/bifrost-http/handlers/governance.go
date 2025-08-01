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
	plugin *governance.GovernancePlugin
	db     *gorm.DB
	logger schemas.Logger
}

// NewGovernanceHandler creates a new governance handler instance
func NewGovernanceHandler(plugin *governance.GovernancePlugin, db *gorm.DB, logger schemas.Logger) *GovernanceHandler {
	return &GovernanceHandler{
		plugin: plugin,
		db:     db,
		logger: logger,
	}
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

	r.GET("/api/governance/debug/health", h.GetHealthCheck)
}

// Virtual Key CRUD Operations

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

// GetVirtualKeys handles GET /api/governance/virtual-keys - Get all virtual keys with relationships
func (h *GovernanceHandler) GetVirtualKeys(ctx *fasthttp.RequestCtx) {
	var virtualKeys []governance.VirtualKey

	// Preload all relationships for complete information
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").Find(&virtualKeys).Error; err != nil {
		h.sendError(ctx, 500, "Failed to retrieve virtual keys", err)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"virtual_keys": virtualKeys,
		"count":        len(virtualKeys),
	})
}

// CreateVirtualKey handles POST /api/governance/virtual-keys - Create a new virtual key
func (h *GovernanceHandler) CreateVirtualKey(ctx *fasthttp.RequestCtx) {
	var req CreateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		h.sendError(ctx, 400, "Invalid JSON", err)
		return
	}

	// Validate required fields
	if req.Name == "" {
		h.sendError(ctx, 400, "Virtual key name is required", nil)
		return
	}

	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		h.sendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer", nil)
		return
	}

	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			h.sendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %d", req.Budget.MaxLimit), nil)
			return
		}
		// Validate reset duration format
		if _, err := governance.ParseDuration(req.Budget.ResetDuration); err != nil {
			h.sendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), nil)
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
			h.sendError(ctx, 500, "Failed to create virtual key", err)
			return err
		}

		return nil
	}); err != nil {
		h.sendError(ctx, 500, "Failed to create virtual key", err)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").First(&vk, "id = ?", vk.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for created VK: %w", err))
	}

	h.sendJSON(ctx, 201, map[string]interface{}{
		"message":     "Virtual key created successfully",
		"virtual_key": vk,
	})
}

// GetVirtualKey handles GET /api/governance/virtual-keys/{vk_id} - Get a specific virtual key
func (h *GovernanceHandler) GetVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	var vk governance.VirtualKey
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").First(&vk, "id = ?", vkID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.sendError(ctx, 404, "Virtual key not found", nil)
			return
		}
		h.sendError(ctx, 500, "Failed to retrieve virtual key", err)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"virtual_key": vk,
	})
}

// UpdateVirtualKey handles PUT /api/governance/virtual-keys/{vk_id} - Update a virtual key
func (h *GovernanceHandler) UpdateVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	var req UpdateVirtualKeyRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		h.sendError(ctx, 400, "Invalid JSON", err)
		return
	}

	// Validate mutually exclusive TeamID and CustomerID
	if req.TeamID != nil && req.CustomerID != nil {
		h.sendError(ctx, 400, "VirtualKey cannot be attached to both Team and Customer", nil)
		return
	}

	var vk governance.VirtualKey
	if err := h.db.First(&vk, "id = ?", vkID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.sendError(ctx, 404, "Virtual key not found", nil)
			return
		}
		h.sendError(ctx, 500, "Failed to retrieve virtual key", err)
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
		h.sendError(ctx, 500, "Failed to update virtual key", err)
		return
	}

	// Update in-memory cache for budget and rate limit changes
	if req.Budget != nil && vk.BudgetID != nil {
		if err := h.plugin.UpdateBudgetCache(*vk.BudgetID); err != nil {
			h.logger.Error(fmt.Errorf("failed to update budget cache: %w", err))
		}
	}
	if req.RateLimit != nil && vk.RateLimitID != nil {
		if err := h.plugin.UpdateRateLimitCache(*vk.RateLimitID, vk.Value); err != nil {
			h.logger.Error(fmt.Errorf("failed to update rate limit cache: %w", err))
		}
	}

	// Load relationships for response
	if err := h.db.Preload("Team").Preload("Customer").Preload("Budget").Preload("RateLimit").First(&vk, "id = ?", vk.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for updated VK: %w", err))
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"message":     "Virtual key updated successfully",
		"virtual_key": vk,
	})
}

// DeleteVirtualKey handles DELETE /api/governance/virtual-keys/{vk_id} - Delete a virtual key
func (h *GovernanceHandler) DeleteVirtualKey(ctx *fasthttp.RequestCtx) {
	vkID := ctx.UserValue("vk_id").(string)

	result := h.db.Delete(&governance.VirtualKey{}, "id = ?", vkID)
	if result.Error != nil {
		h.sendError(ctx, 500, "Failed to delete virtual key", result.Error)
		return
	}

	if result.RowsAffected == 0 {
		h.sendError(ctx, 404, "Virtual key not found", nil)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"message": "Virtual key deleted successfully",
	})
}

// Team CRUD Operations

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
		h.sendError(ctx, 500, "Failed to retrieve teams", err)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"teams": teams,
		"count": len(teams),
	})
}

// CreateTeam handles POST /api/governance/teams - Create a new team
func (h *GovernanceHandler) CreateTeam(ctx *fasthttp.RequestCtx) {
	var req CreateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		h.sendError(ctx, 400, "Invalid JSON", err)
		return
	}

	// Validate required fields
	if req.Name == "" {
		h.sendError(ctx, 400, "Team name is required", nil)
		return
	}

	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			h.sendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %d", req.Budget.MaxLimit), nil)
			return
		}
		// Validate reset duration format
		if _, err := governance.ParseDuration(req.Budget.ResetDuration); err != nil {
			h.sendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), nil)
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
		h.sendError(ctx, 500, "Failed to create team", err)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", team.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for created team: %w", err))
	}

	h.sendJSON(ctx, 201, map[string]interface{}{
		"message": "Team created successfully",
		"team":    team,
	})
}

// GetTeam handles GET /api/governance/teams/{team_id} - Get a specific team
func (h *GovernanceHandler) GetTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var team governance.Team
	if err := h.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", teamID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.sendError(ctx, 404, "Team not found", nil)
			return
		}
		h.sendError(ctx, 500, "Failed to retrieve team", err)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"team": team,
	})
}

// UpdateTeam handles PUT /api/governance/teams/{team_id} - Update a team
func (h *GovernanceHandler) UpdateTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	var req UpdateTeamRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		h.sendError(ctx, 400, "Invalid JSON", err)
		return
	}

	var team governance.Team
	if err := h.db.First(&team, "id = ?", teamID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.sendError(ctx, 404, "Team not found", nil)
			return
		}
		h.sendError(ctx, 500, "Failed to retrieve team", err)
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
		h.sendError(ctx, 500, "Failed to update team", err)
		return
	}

	// Update in-memory cache for budget changes
	if req.Budget != nil && team.BudgetID != nil {
		if err := h.plugin.UpdateBudgetCache(*team.BudgetID); err != nil {
			h.logger.Error(fmt.Errorf("failed to update budget cache: %w", err))
		}
	}

	// Load relationships for response
	if err := h.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", team.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for updated team: %w", err))
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"message": "Team updated successfully",
		"team":    team,
	})
}

// DeleteTeam handles DELETE /api/governance/teams/{team_id} - Delete a team
func (h *GovernanceHandler) DeleteTeam(ctx *fasthttp.RequestCtx) {
	teamID := ctx.UserValue("team_id").(string)

	result := h.db.Delete(&governance.Team{}, "id = ?", teamID)
	if result.Error != nil {
		h.sendError(ctx, 500, "Failed to delete team", result.Error)
		return
	}

	if result.RowsAffected == 0 {
		h.sendError(ctx, 404, "Team not found", nil)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"message": "Team deleted successfully",
	})
}

// Customer CRUD Operations

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

// GetCustomers handles GET /api/governance/customers - Get all customers
func (h *GovernanceHandler) GetCustomers(ctx *fasthttp.RequestCtx) {
	var customers []governance.Customer

	// Preload relationships for complete information
	if err := h.db.Preload("Teams").Preload("Budget").Find(&customers).Error; err != nil {
		h.sendError(ctx, 500, "Failed to retrieve customers", err)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"customers": customers,
		"count":     len(customers),
	})
}

// CreateCustomer handles POST /api/governance/customers - Create a new customer
func (h *GovernanceHandler) CreateCustomer(ctx *fasthttp.RequestCtx) {
	var req CreateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		h.sendError(ctx, 400, "Invalid JSON", err)
		return
	}

	// Validate required fields
	if req.Name == "" {
		h.sendError(ctx, 400, "Customer name is required", nil)
		return
	}

	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			h.sendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %d", req.Budget.MaxLimit), nil)
			return
		}
		// Validate reset duration format
		if _, err := governance.ParseDuration(req.Budget.ResetDuration); err != nil {
			h.sendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration), nil)
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
		h.sendError(ctx, 500, "Failed to create customer", err)
		return
	}

	// Load relationships for response
	if err := h.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", customer.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for created customer: %w", err))
	}

	h.sendJSON(ctx, 201, map[string]interface{}{
		"message":  "Customer created successfully",
		"customer": customer,
	})
}

// GetCustomer handles GET /api/governance/customers/{customer_id} - Get a specific customer
func (h *GovernanceHandler) GetCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	var customer governance.Customer
	if err := h.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", customerID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.sendError(ctx, 404, "Customer not found", nil)
			return
		}
		h.sendError(ctx, 500, "Failed to retrieve customer", err)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"customer": customer,
	})
}

// UpdateCustomer handles PUT /api/governance/customers/{customer_id} - Update a customer
func (h *GovernanceHandler) UpdateCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	var req UpdateCustomerRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		h.sendError(ctx, 400, "Invalid JSON", err)
		return
	}

	var customer governance.Customer
	if err := h.db.First(&customer, "id = ?", customerID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.sendError(ctx, 404, "Customer not found", nil)
			return
		}
		h.sendError(ctx, 500, "Failed to retrieve customer", err)
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
		h.sendError(ctx, 500, "Failed to update customer", err)
		return
	}

	// Update in-memory cache for budget changes
	if req.Budget != nil && customer.BudgetID != nil {
		if err := h.plugin.UpdateBudgetCache(*customer.BudgetID); err != nil {
			h.logger.Error(fmt.Errorf("failed to update budget cache: %w", err))
		}
	}

	// Load relationships for response
	if err := h.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", customer.ID).Error; err != nil {
		h.logger.Error(fmt.Errorf("failed to load relationships for updated customer: %w", err))
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"message":  "Customer updated successfully",
		"customer": customer,
	})
}

// DeleteCustomer handles DELETE /api/governance/customers/{customer_id} - Delete a customer
func (h *GovernanceHandler) DeleteCustomer(ctx *fasthttp.RequestCtx) {
	customerID := ctx.UserValue("customer_id").(string)

	result := h.db.Delete(&governance.Customer{}, "id = ?", customerID)
	if result.Error != nil {
		h.sendError(ctx, 500, "Failed to delete customer", result.Error)
		return
	}

	if result.RowsAffected == 0 {
		h.sendError(ctx, 404, "Customer not found", nil)
		return
	}

	h.sendJSON(ctx, 200, map[string]interface{}{
		"message": "Customer deleted successfully",
	})
}

// CreateBudgetRequest represents the request body for creating a budget
type CreateBudgetRequest struct {
	MaxLimit      int64  `json:"max_limit" validate:"required"`      // Maximum budget in cents
	ResetDuration string `json:"reset_duration" validate:"required"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

// UpdateBudgetRequest represents the request body for updating a budget
type UpdateBudgetRequest struct {
	MaxLimit      *int64  `json:"max_limit,omitempty"`
	ResetDuration *string `json:"reset_duration,omitempty"`
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

// GetHealthCheck handles GET /api/governance/debug/health - Health check for governance system
func (h *GovernanceHandler) GetHealthCheck(ctx *fasthttp.RequestCtx) {
	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now(),
		"checks":    make(map[string]interface{}),
	}

	checks := health["checks"].(map[string]interface{})

	// Check database connectivity
	sqlDB, err := h.db.DB()
	if err != nil || sqlDB.Ping() != nil {
		checks["database"] = map[string]interface{}{
			"status": "unhealthy",
			"error":  "Database connection failed",
		}
		health["status"] = "unhealthy"
	} else {
		checks["database"] = map[string]interface{}{
			"status": "healthy",
		}
	}

	statusCode := 200
	if health["status"] == "unhealthy" {
		statusCode = 503
	}

	h.sendJSON(ctx, statusCode, health)
}

// Helper methods

// sendJSON sends a JSON response
func (h *GovernanceHandler) sendJSON(ctx *fasthttp.RequestCtx, statusCode int, data interface{}) {
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(statusCode)

	if err := json.NewEncoder(ctx).Encode(data); err != nil {
		h.logger.Error(fmt.Errorf("failed to encode JSON response: %w", err))
		ctx.SetStatusCode(500)
		ctx.SetBodyString(`{"error": "Internal server error"}`)
	}
}

// sendError sends an error response
func (h *GovernanceHandler) sendError(ctx *fasthttp.RequestCtx, statusCode int, message string, err error) {
	response := map[string]interface{}{
		"error":  message,
		"status": statusCode,
	}

	if err != nil {
		h.logger.Error(fmt.Errorf("%s: %w", message, err))
		response["details"] = err.Error()
	}

	h.sendJSON(ctx, statusCode, response)
}
