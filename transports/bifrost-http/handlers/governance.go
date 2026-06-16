// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains all governance management functionality including CRUD operations for VKs, Rules, and configs.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
	ReloadModelConfig(ctx context.Context, id string) (*configstoreTables.TableModelConfig, error)
	RemoveModelConfig(ctx context.Context, id string) error
	ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*configstoreTables.TableProvider, error)
	RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error
	ReloadRoutingRule(ctx context.Context, id string) error
	RemoveRoutingRule(ctx context.Context, id string) error
	UpsertPricingOverride(ctx context.Context, override *configstoreTables.TablePricingOverride) error
	DeletePricingOverride(ctx context.Context, id string) error
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
		Weight        *float64                `json:"weight,omitempty"`
		AllowedModels schemas.WhiteList       `json:"allowed_models,omitempty"` // ["*"] allows all models; empty denies all
		Budgets       []CreateBudgetRequest   `json:"budgets,omitempty"`        // Multi-budget for provider config
		RateLimit     *CreateRateLimitRequest `json:"rate_limit,omitempty"`     // Provider-level rate limit
		KeyIDs        schemas.WhiteList       `json:"key_ids,omitempty"`        // List of DBKey UUIDs to associate with this provider config
	} `json:"provider_configs,omitempty"` // Empty means no providers allowed (deny-by-default)
	MCPConfigs []struct {
		MCPClientName  string            `json:"mcp_client_name" validate:"required"`
		ToolsToExecute schemas.WhiteList `json:"tools_to_execute,omitempty"`
	} `json:"mcp_configs,omitempty"` // Empty means no MCP clients allowed (deny-by-default)
	TeamID          *string                 `json:"team_id,omitempty"`     // Mutually exclusive with CustomerID
	CustomerID      *string                 `json:"customer_id,omitempty"` // Mutually exclusive with TeamID
	Budgets         []CreateBudgetRequest   `json:"budgets,omitempty"`     // Multi-budget: each must have a unique reset_duration
	RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	IsActive        *bool                   `json:"is_active,omitempty"`
	CalendarAligned bool                    `json:"calendar_aligned,omitempty"` // When true, all budgets reset at clean calendar boundaries
}

// UpdateVirtualKeyRequest represents the request body for updating a virtual key
type UpdateVirtualKeyRequest struct {
	Name            *string `json:"name,omitempty"`
	Description     *string `json:"description,omitempty"`
	ProviderConfigs []struct {
		ID            *uint                   `json:"id,omitempty"` // null for new entries
		Provider      string                  `json:"provider" validate:"required"`
		Weight        *float64                `json:"weight,omitempty"`
		AllowedModels schemas.WhiteList       `json:"allowed_models,omitempty"` // ["*"] allows all models; empty denies all
		Budgets       []CreateBudgetRequest   `json:"budgets,omitempty"`        // Multi-budget for provider config
		RateLimit     *UpdateRateLimitRequest `json:"rate_limit,omitempty"`     // Provider-level rate limit
		KeyIDs        schemas.WhiteList       `json:"key_ids,omitempty"`        // List of DBKey UUIDs to associate with this provider config
	} `json:"provider_configs,omitempty"`
	MCPConfigs []struct {
		ID             *uint             `json:"id,omitempty"` // null for new entries
		MCPClientName  string            `json:"mcp_client_name" validate:"required"`
		ToolsToExecute schemas.WhiteList `json:"tools_to_execute,omitempty"`
	} `json:"mcp_configs,omitempty"`
	TeamID          *string                 `json:"team_id,omitempty"`
	CustomerID      *string                 `json:"customer_id,omitempty"`
	Budgets         []CreateBudgetRequest   `json:"budgets,omitempty"` // Multi-budget: replaces all VK-level budgets
	RateLimit       *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
	IsActive        *bool                   `json:"is_active,omitempty"`
	CalendarAligned *bool                   `json:"calendar_aligned,omitempty"` // When true, all budgets reset at clean calendar boundaries
}

// CreateBudgetRequest represents the request body for creating a budget
type CreateBudgetRequest struct {
	MaxLimit        float64 `json:"max_limit" validate:"required"`      // Maximum budget in dollars
	ResetDuration   string  `json:"reset_duration" validate:"required"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	CalendarAligned bool    `json:"calendar_aligned,omitempty"`         // Snap resets to calendar boundaries (day/week/month/year)
}

// UpdateBudgetRequest represents the request body for updating a budget
type UpdateBudgetRequest struct {
	MaxLimit        *float64 `json:"max_limit,omitempty"`
	ResetDuration   *string  `json:"reset_duration,omitempty"`
	CalendarAligned *bool    `json:"calendar_aligned,omitempty"` // When switching to true, current usage is reset to 0
}

// RoutingTarget represents a single weighted routing target within a rule.
// All fields except Weight are optional; nil means "use the incoming request's value".
// Weights across all targets in a rule must sum to 1 (e.g. 0.7 + 0.3 = 1.0).
type RoutingTarget struct {
	Provider *string `json:"provider,omitempty"` // nil = use incoming provider
	Model    *string `json:"model,omitempty"`    // nil = use incoming model
	KeyID    *string `json:"key_id,omitempty"`   // nil = no key pin
	Weight   float64 `json:"weight"`             // must be > 0; all weights must sum to 1
}

// CreateRoutingRuleRequest represents the request body for creating a routing rule
type CreateRoutingRuleRequest struct {
	Name          string          `json:"name" validate:"required"`
	Description   string          `json:"description,omitempty"`
	Enabled       *bool           `json:"enabled,omitempty"`    // nil = use DB default (true)
	ChainRule     *bool           `json:"chain_rule,omitempty"` // nil = use DB default (false)
	CelExpression string          `json:"cel_expression"`
	Targets       []RoutingTarget `json:"targets"` // Required; weights must sum to 1
	Fallbacks     []string        `json:"fallbacks,omitempty"`
	Scope         string          `json:"scope,omitempty"` // Defaults to "global" if not provided
	ScopeID       *string         `json:"scope_id,omitempty"`
	Query         map[string]any  `json:"query,omitempty"`
	Priority      int             `json:"priority,omitempty"` // Defaults to 0 if not provided
}

// UpdateRoutingRuleRequest represents the request body for updating a routing rule
type UpdateRoutingRuleRequest struct {
	Name          *string         `json:"name,omitempty"`
	Description   *string         `json:"description,omitempty"`
	Enabled       *bool           `json:"enabled,omitempty"`
	ChainRule     *bool           `json:"chain_rule,omitempty"`
	CelExpression *string         `json:"cel_expression,omitempty"`
	Targets       []RoutingTarget `json:"targets,omitempty"` // If provided, replaces all existing targets; weights must sum to 1
	Fallbacks     []string        `json:"fallbacks,omitempty"`
	Query         map[string]any  `json:"query,omitempty"`
	Priority      *int            `json:"priority,omitempty"`
	Scope         *string         `json:"scope,omitempty"`
	ScopeID       *string         `json:"scope_id,omitempty"`
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

func isBudgetRemovalRequest(req *UpdateBudgetRequest) bool {
	return req != nil && req.MaxLimit == nil && req.ResetDuration == nil
}

// budgetLastReset returns the appropriate LastReset for a new budget.
// When calendarAligned is true it snaps to the start of the current calendar period
// (e.g. midnight on the 1st of the month for "1M"), otherwise it returns time.Now().
func budgetLastReset(calendarAligned bool, resetDuration string) time.Time {
	if calendarAligned {
		return configstoreTables.GetCalendarPeriodStart(resetDuration, time.Now())
	}
	return time.Now()
}

func isRateLimitRemovalRequest(req *UpdateRateLimitRequest) bool {
	return req != nil && req.TokenMaxLimit == nil && req.RequestMaxLimit == nil &&
		req.TokenResetDuration == nil && req.RequestResetDuration == nil
}

func collectProviderConfigDeleteIDs(
	config configstoreTables.TableVirtualKeyProviderConfig,
	budgetIDs []string,
	rateLimitIDs []string,
) ([]string, []string) {
	for _, b := range config.Budgets {
		budgetIDs = append(budgetIDs, b.ID)
	}
	if config.RateLimitID != nil {
		rateLimitIDs = append(rateLimitIDs, *config.RateLimitID)
	}
	return budgetIDs, rateLimitIDs
}

// CreateTeamRequest represents the request body for creating a team
type CreateTeamRequest struct {
	Name       string                  `json:"name" validate:"required"`
	CustomerID *string                 `json:"customer_id,omitempty"` // Team can belong to a customer
	Budget     *CreateBudgetRequest    `json:"budget,omitempty"`      // Team can have its own budget
	RateLimit  *CreateRateLimitRequest `json:"rate_limit,omitempty"`  // Team can have its own rate limit
}

// UpdateTeamRequest represents the request body for updating a team
type UpdateTeamRequest struct {
	Name       *string                 `json:"name,omitempty"`
	CustomerID *string                 `json:"customer_id,omitempty"`
	Budget     *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit  *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
}

// CreateCustomerRequest represents the request body for creating a customer
type CreateCustomerRequest struct {
	Name      string                  `json:"name" validate:"required"`
	Budget    *CreateBudgetRequest    `json:"budget,omitempty"`
	RateLimit *CreateRateLimitRequest `json:"rate_limit,omitempty"` // Customer can have its own rate limit
}

// UpdateCustomerRequest represents the request body for updating a customer
type UpdateCustomerRequest struct {
	Name      *string                 `json:"name,omitempty"`
	Budget    *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
}

// CreateModelConfigRequest represents the request body for creating a model config
type CreateModelConfigRequest struct {
	ModelName string                  `json:"model_name" validate:"required"`
	Provider  *string                 `json:"provider,omitempty"` // Optional provider, nil means all providers
	Budget    *CreateBudgetRequest    `json:"budget,omitempty"`
	RateLimit *CreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// UpdateModelConfigRequest represents the request body for updating a model config
type UpdateModelConfigRequest struct {
	ModelName *string                 `json:"model_name,omitempty"`
	Provider  *string                 `json:"provider,omitempty"` // Optional provider, nil means no change
	Budget    *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
}

// UpdateProviderGovernanceRequest represents the request body for updating provider governance
type UpdateProviderGovernanceRequest struct {
	Budget    *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit *UpdateRateLimitRequest `json:"rate_limit,omitempty"`
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

	// Routing Rules CRUD operations
	r.GET("/api/governance/routing-rules", lib.ChainMiddlewares(h.getRoutingRules, middlewares...))
	r.POST("/api/governance/routing-rules", lib.ChainMiddlewares(h.createRoutingRule, middlewares...))
	r.GET("/api/governance/routing-rules/{rule_id}", lib.ChainMiddlewares(h.getRoutingRule, middlewares...))
	r.PUT("/api/governance/routing-rules/{rule_id}", lib.ChainMiddlewares(h.updateRoutingRule, middlewares...))
	r.DELETE("/api/governance/routing-rules/{rule_id}", lib.ChainMiddlewares(h.deleteRoutingRule, middlewares...))

	// Model Config CRUD operations
	r.GET("/api/governance/model-configs", lib.ChainMiddlewares(h.getModelConfigs, middlewares...))
	r.POST("/api/governance/model-configs", lib.ChainMiddlewares(h.createModelConfig, middlewares...))
	r.GET("/api/governance/model-configs/{mc_id}", lib.ChainMiddlewares(h.getModelConfig, middlewares...))
	r.PUT("/api/governance/model-configs/{mc_id}", lib.ChainMiddlewares(h.updateModelConfig, middlewares...))
	r.DELETE("/api/governance/model-configs/{mc_id}", lib.ChainMiddlewares(h.deleteModelConfig, middlewares...))

	// Provider Governance operations
	r.GET("/api/governance/providers", lib.ChainMiddlewares(h.getProviderGovernance, middlewares...))
	r.PUT("/api/governance/providers/{provider_name}", lib.ChainMiddlewares(h.updateProviderGovernance, middlewares...))
	r.DELETE("/api/governance/providers/{provider_name}", lib.ChainMiddlewares(h.deleteProviderGovernance, middlewares...))

	// Pricing override operations
	r.GET("/api/governance/pricing-overrides", lib.ChainMiddlewares(h.getPricingOverrides, middlewares...))
	r.POST("/api/governance/pricing-overrides", lib.ChainMiddlewares(h.createPricingOverride, middlewares...))
	r.PUT("/api/governance/pricing-overrides/{id}", lib.ChainMiddlewares(h.updatePricingOverride, middlewares...))
	r.DELETE("/api/governance/pricing-overrides/{id}", lib.ChainMiddlewares(h.deletePricingOverride, middlewares...))

	// Self-service endpoint — no admin auth, VK in header is the credential.
	// Registered without admin middlewares; only common middlewares (telemetry) are applied.
	r.GET("/api/governance/virtual-keys/quota", h.getVirtualKeyQuota)
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
		// Convert map to slice to match the non-memory response format (array)
		virtualKeys := make([]*configstoreTables.TableVirtualKey, 0, len(data.VirtualKeys))
		for _, vk := range data.VirtualKeys {
			virtualKeys = append(virtualKeys, vk)
		}
		sort.Slice(virtualKeys, func(i, j int) bool {
			return virtualKeys[i].CreatedAt.Before(virtualKeys[j].CreatedAt)
		})
		SendJSON(ctx, map[string]interface{}{
			"virtual_keys": virtualKeys,
			"count":        len(virtualKeys),
			"total_count":  len(virtualKeys),
			"limit":        len(virtualKeys),
			"offset":       0,
		})
		return
	}
	// Check for pagination/filter parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))
	customerID := string(ctx.QueryArgs().Peek("customer_id"))
	teamID := string(ctx.QueryArgs().Peek("team_id"))
	sortBy := string(ctx.QueryArgs().Peek("sort_by"))
	order := string(ctx.QueryArgs().Peek("order"))
	isExport := string(ctx.QueryArgs().Peek("export")) == "true"

	if limitStr != "" || offsetStr != "" || search != "" || customerID != "" || teamID != "" || sortBy != "" || isExport {
		// Paginated/filtered path
		params := configstore.VirtualKeyQueryParams{
			Search:     search,
			CustomerID: customerID,
			TeamID:     teamID,
			SortBy:     sortBy,
			Order:      order,
			Export:     isExport,
		}
		if limitStr != "" {
			n, err := strconv.Atoi(limitStr)
			if err != nil {
				SendError(ctx, 400, "Invalid limit parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
				return
			}
			params.Limit = n
		}
		if offsetStr != "" {
			n, err := strconv.Atoi(offsetStr)
			if err != nil {
				SendError(ctx, 400, "Invalid offset parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
				return
			}
			params.Offset = n
		}

		params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)
		virtualKeys, totalCount, err := h.configStore.GetVirtualKeysPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve virtual keys: %v", err)
			SendError(ctx, 500, "Failed to retrieve virtual keys")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"virtual_keys": virtualKeys,
			"count":        len(virtualKeys),
			"total_count":  totalCount,
			"limit":        params.Limit,
			"offset":       params.Offset,
		})
		return
	}

	// Non-paginated path: return all virtual keys
	virtualKeys, err := h.configStore.GetVirtualKeys(ctx)
	if err != nil {
		logger.Error("failed to retrieve virtual keys: %v", err)
		SendError(ctx, 500, "Failed to retrieve virtual keys")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"virtual_keys": virtualKeys,
		"count":        len(virtualKeys),
		"total_count":  len(virtualKeys),
		"limit":        len(virtualKeys),
		"offset":       0,
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
	// Validate budgets if provided
	if len(req.Budgets) > 0 {
		seenDurations := make(map[string]bool)
		for _, b := range req.Budgets {
			if b.MaxLimit < 0 {
				SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", b.MaxLimit))
				return
			}
			if _, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil {
				SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", b.ResetDuration))
				return
			}
			if seenDurations[b.ResetDuration] {
				SendError(ctx, 400, fmt.Sprintf("Duplicate reset_duration in budgets: %s", b.ResetDuration))
				return
			}
			seenDurations[b.ResetDuration] = true
		}
	}
	// Set defaults
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	// Fetch providers from DB to ensure up-to-date data in cluster mode.
	providerSet := map[schemas.ModelProvider]struct{}{}
	if req.ProviderConfigs != nil {
		var err error
		providerSet, err = h.getConfiguredProviderSet(ctx)
		if err != nil {
			SendError(ctx, 500, fmt.Sprintf("Failed to load providers: %v", err))
			return
		}
	}
	var vk configstoreTables.TableVirtualKey
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		vk = configstoreTables.TableVirtualKey{
			ID:              uuid.NewString(),
			Name:            req.Name,
			Value:           governance.GenerateVirtualKey(),
			Description:     req.Description,
			TeamID:          req.TeamID,
			CustomerID:      req.CustomerID,
			IsActive:        isActive,
			CalendarAligned: req.CalendarAligned,
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
		// Create multi-budgets for VK
		if len(req.Budgets) > 0 {
			for _, b := range req.Budgets {
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      b.MaxLimit,
					ResetDuration: b.ResetDuration,
					LastReset:     budgetLastReset(vk.CalendarAligned, b.ResetDuration),
					CurrentUsage:  0,
					VirtualKeyID:  &vk.ID,
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
			}
		}
		if req.ProviderConfigs != nil {
			for _, pc := range req.ProviderConfigs {
				providerName := schemas.ModelProvider(strings.TrimSpace(pc.Provider))
				if providerName == "" {
					return &badRequestError{err: fmt.Errorf("provider name is required")}
				}
				if _, ok := providerSet[providerName]; !ok {
					return &badRequestError{err: fmt.Errorf("invalid provider name: %s", pc.Provider)}
				}
				if err := pc.AllowedModels.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid allowed_models for provider %s: %w", pc.Provider, err)}
				}
				if err := pc.KeyIDs.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid key_ids for provider %s: %w", pc.Provider, err)}
				}

				// Get keys for this provider config if specified
				var keys []configstoreTables.TableKey
				allowAllKeys := false
				if pc.KeyIDs.IsUnrestricted() {
					allowAllKeys = true
				} else if !pc.KeyIDs.IsEmpty() {
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
					Provider:      string(providerName),
					Weight:        pc.Weight,
					AllowedModels: pc.AllowedModels,
					AllowAllKeys:  allowAllKeys,
					Keys:          keys,
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
				// Create multi-budgets for provider config
				if len(pc.Budgets) > 0 {
					seenDurations := make(map[string]bool)
					for _, b := range pc.Budgets {
						if seenDurations[b.ResetDuration] {
							return &badRequestError{err: fmt.Errorf("duplicate reset_duration in provider config budgets: %s", b.ResetDuration)}
						}
						seenDurations[b.ResetDuration] = true
						budget := configstoreTables.TableBudget{
							ID:               uuid.NewString(),
							MaxLimit:         b.MaxLimit,
							ResetDuration:    b.ResetDuration,
							LastReset:        budgetLastReset(vk.CalendarAligned, b.ResetDuration),
							CurrentUsage:     0,
							ProviderConfigID: &providerConfig.ID,
						}
						if err := validateBudget(&budget); err != nil {
							return err
						}
						if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
							return err
						}
					}
				}
			}
		}
		if req.MCPConfigs != nil {
			// Check for duplicate MCPClientName values before processing
			seenMCPClientNames := make(map[string]bool)
			for _, mc := range req.MCPConfigs {
				if seenMCPClientNames[mc.MCPClientName] {
					return &badRequestError{err: fmt.Errorf("duplicate mcp_client_name: %s", mc.MCPClientName)}
				}
				seenMCPClientNames[mc.MCPClientName] = true
			}

			for _, mc := range req.MCPConfigs {
				if err := mc.ToolsToExecute.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid tools_to_execute for mcp client %s: %w", mc.MCPClientName, err)}
				}
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
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) {
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
	providerSet := map[schemas.ModelProvider]struct{}{}
	if len(req.ProviderConfigs) > 0 {
		providerSet, err = h.getConfiguredProviderSet(ctx)
		if err != nil {
			SendError(ctx, 500, fmt.Sprintf("Failed to load providers: %v", err))
			return
		}
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		var rateLimitIDToDelete string
		var providerBudgetIDsToDelete []string
		var providerRateLimitIDsToDelete []string

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
		if req.CalendarAligned != nil {
			vk.CalendarAligned = *req.CalendarAligned
		}
		// Handle multi-budget updates
		if req.Budgets != nil {
			// Validate multi-budgets
			seenDurations := make(map[string]bool)
			for _, b := range req.Budgets {
				if b.MaxLimit < 0 {
					return &badRequestError{err: fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)}
				}
				if _, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid reset duration format: %s", b.ResetDuration)}
				}
				if seenDurations[b.ResetDuration] {
					return &badRequestError{err: fmt.Errorf("duplicate reset_duration in budgets: %s", b.ResetDuration)}
				}
				seenDurations[b.ResetDuration] = true
			}

			// Build map of existing budgets by reset_duration for matching
			existingByDuration := make(map[string]configstoreTables.TableBudget)
			for _, existing := range vk.Budgets {
				existingByDuration[existing.ResetDuration] = existing
			}

			// Reconcile: preserve existing budgets where possible, create new ones where needed
			var reconciledBudgets []configstoreTables.TableBudget
			matchedIDs := make(map[string]bool)
			for _, b := range req.Budgets {
				if existing, found := existingByDuration[b.ResetDuration]; found {
					// Budget with same duration exists — update max_limit, preserve usage
					existing.MaxLimit = b.MaxLimit
					if err := validateBudget(&existing); err != nil {
						return err
					}
					if err := h.configStore.UpdateBudget(ctx, &existing, tx); err != nil {
						return err
					}
					reconciledBudgets = append(reconciledBudgets, existing)
					matchedIDs[existing.ID] = true
				} else {
					// New budget duration — create fresh
					budget := configstoreTables.TableBudget{
						ID:            uuid.NewString(),
						MaxLimit:      b.MaxLimit,
						ResetDuration: b.ResetDuration,
						LastReset:     budgetLastReset(vk.CalendarAligned, b.ResetDuration),
						CurrentUsage:  0,
						VirtualKeyID:  &vk.ID,
					}
					if err := validateBudget(&budget); err != nil {
						return err
					}
					if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
						return err
					}
					reconciledBudgets = append(reconciledBudgets, budget)
				}
			}
			// Delete budgets that are no longer present
			for _, existing := range vk.Budgets {
				if !matchedIDs[existing.ID] {
					if err := h.configStore.DeleteBudget(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to delete removed VK budget: %w", err)
					}
				}
			}
			vk.Budgets = reconciledBudgets
		}

		// Handle rate limit updates
		if req.RateLimit != nil {
			if isRateLimitRemovalRequest(req.RateLimit) {
				if vk.RateLimitID != nil {
					rateLimitIDToDelete = *vk.RateLimitID
					vk.RateLimitID = nil
					vk.RateLimit = nil
				}
			} else if vk.RateLimitID != nil {
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
			if err := tx.Where("virtual_key_id = ?", vk.ID).
				Preload("Budgets").
				Find(&existingConfigs).Error; err != nil {
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
				providerName := schemas.ModelProvider(strings.TrimSpace(pc.Provider))
				if providerName == "" {
					return &badRequestError{err: fmt.Errorf("provider name is required")}
				}
				if _, ok := providerSet[providerName]; !ok {
					return &badRequestError{err: fmt.Errorf("invalid provider name: %s", pc.Provider)}
				}
				if pc.ID == nil {
					if err := pc.AllowedModels.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid allowed_models for provider %s: %w", pc.Provider, err)}
					}
					if err := pc.KeyIDs.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid key_ids for provider %s: %w", pc.Provider, err)}
					}

					// Get keys for this provider config if specified
					var keys []configstoreTables.TableKey
					allowAllKeys := false
					if pc.KeyIDs.IsUnrestricted() {
						allowAllKeys = true
					} else if !pc.KeyIDs.IsEmpty() {
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
						Provider:      string(providerName),
						Weight:        pc.Weight,
						AllowedModels: pc.AllowedModels,
						AllowAllKeys:  allowAllKeys,
						Keys:          keys,
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
					// Create multi-budgets for new provider config in update
					if len(pc.Budgets) > 0 {
						seenDurations := make(map[string]bool)
						for _, b := range pc.Budgets {
							if seenDurations[b.ResetDuration] {
								return &badRequestError{err: fmt.Errorf("duplicate reset_duration in provider config budgets: %s", b.ResetDuration)}
							}
							seenDurations[b.ResetDuration] = true
							budget := configstoreTables.TableBudget{
								ID:               uuid.NewString(),
								MaxLimit:         b.MaxLimit,
								ResetDuration:    b.ResetDuration,
								LastReset:        budgetLastReset(vk.CalendarAligned, b.ResetDuration),
								CurrentUsage:     0,
								ProviderConfigID: &providerConfig.ID,
							}
							if err := validateBudget(&budget); err != nil {
								return err
							}
							if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
								return err
							}
						}
					}
				} else {
					// Update existing provider config
					existing, ok := existingConfigsMap[*pc.ID]
					if !ok {
						return fmt.Errorf("provider config %d does not belong to this virtual key", *pc.ID)
					}
					requestConfigsMap[*pc.ID] = true
					if err := pc.AllowedModels.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid allowed_models for provider %s: %w", pc.Provider, err)}
					}
					if err := pc.KeyIDs.Validate(); err != nil {
						return &badRequestError{err: fmt.Errorf("invalid key_ids for provider %s: %w", pc.Provider, err)}
					}
					existing.Provider = string(providerName)
					existing.Weight = pc.Weight
					existing.AllowedModels = pc.AllowedModels

					// Get keys for this provider config if specified
					var keys []configstoreTables.TableKey
					allowAllKeys := false
					if pc.KeyIDs.IsUnrestricted() {
						allowAllKeys = true
					} else if !pc.KeyIDs.IsEmpty() {
						var err error
						keys, err = h.configStore.GetKeysByIDs(ctx, pc.KeyIDs)
						if err != nil {
							return fmt.Errorf("failed to get keys by IDs for provider %s: %w", pc.Provider, err)
						}
						if len(keys) != len(pc.KeyIDs) {
							return fmt.Errorf("some keys not found for provider %s: expected %d, found %d", pc.Provider, len(pc.KeyIDs), len(keys))
						}
					}
					existing.AllowAllKeys = allowAllKeys
					existing.Keys = keys

					// Handle multi-budget updates for existing provider config
					if pc.Budgets != nil {
						// Validate
						seenDurations := make(map[string]bool)
						for _, b := range pc.Budgets {
							if b.MaxLimit < 0 {
								return &badRequestError{err: fmt.Errorf("provider config budget max_limit cannot be negative: %.2f", b.MaxLimit)}
							}
							if _, err := configstoreTables.ParseDuration(b.ResetDuration); err != nil {
								return &badRequestError{err: fmt.Errorf("invalid provider config budget reset duration format: %s", b.ResetDuration)}
							}
							if seenDurations[b.ResetDuration] {
								return &badRequestError{err: fmt.Errorf("duplicate reset_duration in provider config budgets: %s", b.ResetDuration)}
							}
							seenDurations[b.ResetDuration] = true
						}

						// Build map of existing budgets by reset_duration for matching
						pcExistingByDuration := make(map[string]configstoreTables.TableBudget)
						for _, eb := range existing.Budgets {
							pcExistingByDuration[eb.ResetDuration] = eb
						}

						// Reconcile: preserve existing budgets where possible
						var pcReconciledBudgets []configstoreTables.TableBudget
						pcMatchedIDs := make(map[string]bool)
						for _, b := range pc.Budgets {
							if eb, found := pcExistingByDuration[b.ResetDuration]; found {
								// Budget with same duration exists — update max_limit, preserve usage
								eb.MaxLimit = b.MaxLimit
								if err := validateBudget(&eb); err != nil {
									return err
								}
								if err := h.configStore.UpdateBudget(ctx, &eb, tx); err != nil {
									return err
								}
								pcReconciledBudgets = append(pcReconciledBudgets, eb)
								pcMatchedIDs[eb.ID] = true
							} else {
								// New budget duration — create fresh
								budget := configstoreTables.TableBudget{
									ID:               uuid.NewString(),
									MaxLimit:         b.MaxLimit,
									ResetDuration:    b.ResetDuration,
									LastReset:        budgetLastReset(vk.CalendarAligned, b.ResetDuration),
									CurrentUsage:     0,
									ProviderConfigID: &existing.ID,
								}
								if err := validateBudget(&budget); err != nil {
									return err
								}
								if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
									return err
								}
								pcReconciledBudgets = append(pcReconciledBudgets, budget)
							}
						}
						// Delete budgets that are no longer present
						for _, eb := range existing.Budgets {
							if !pcMatchedIDs[eb.ID] {
								if err := h.configStore.DeleteBudget(ctx, eb.ID, tx); err != nil {
									return fmt.Errorf("failed to delete removed provider config budget: %w", err)
								}
							}
						}
						existing.Budgets = pcReconciledBudgets
					}
					// Handle rate limit updates for provider config
					if pc.RateLimit != nil {
						if isRateLimitRemovalRequest(pc.RateLimit) {
							if existing.RateLimitID != nil {
								providerRateLimitIDsToDelete = append(providerRateLimitIDsToDelete, *existing.RateLimitID)
								existing.RateLimitID = nil
								existing.RateLimit = nil
							}
						} else if existing.RateLimitID != nil {
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
					providerBudgetIDsToDelete, providerRateLimitIDsToDelete = collectProviderConfigDeleteIDs(
						existingConfigsMap[id],
						providerBudgetIDsToDelete,
						providerRateLimitIDsToDelete,
					)
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
					return &badRequestError{err: fmt.Errorf("duplicate mcp_client_name: %s", mc.MCPClientName)}
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
				if err := mc.ToolsToExecute.Validate(); err != nil {
					return &badRequestError{err: fmt.Errorf("invalid tools_to_execute for mcp client %s: %w", mc.MCPClientName, err)}
				}
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

		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}
		for _, id := range providerBudgetIDsToDelete {
			if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", id).Error; err != nil {
				return err
			}
		}
		for _, id := range providerRateLimitIDsToDelete {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", id).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		var badReqErr *badRequestError
		if errors.As(err, &badReqErr) ||
			strings.Contains(err.Error(), "already exists") ||
			strings.Contains(err.Error(), "duplicate key") {
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
	if _, err := h.governanceManager.ReloadVirtualKey(ctx, vk.ID); err != nil {
		// Should never happen but just in case
		logger.Error("failed to reload virtual key after update: %v", err)
		SendError(ctx, 500, "Virtual key updated in database but failed to reload in-memory state")
		return
	}
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
				"teams":       teams,
				"count":       len(teams),
				"total_count": len(teams),
				"limit":       len(teams),
				"offset":      0,
			})
		} else {
			SendJSON(ctx, map[string]interface{}{
				"teams":       data.Teams,
				"count":       len(data.Teams),
				"total_count": len(data.Teams),
				"limit":       len(data.Teams),
				"offset":      0,
			})
		}
		return
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		limit, _ := strconv.Atoi(limitStr)
		offset, _ := strconv.Atoi(offsetStr)
		limit, offset = ClampPaginationParams(limit, offset)
		teams, totalCount, err := h.configStore.GetTeamsPaginated(ctx, configstore.TeamsQueryParams{
			Limit:      limit,
			Offset:     offset,
			Search:     search,
			CustomerID: customerID,
		})
		if err != nil {
			logger.Error("failed to retrieve teams: %v", err)
			SendError(ctx, 500, fmt.Sprintf("Failed to retrieve teams: %v", err))
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"teams":       teams,
			"count":       len(teams),
			"total_count": totalCount,
			"limit":       limit,
			"offset":      offset,
		})
		return
	}

	// Non-paginated path: return all teams
	teams, err := h.configStore.GetTeams(ctx, customerID)
	if err != nil {
		logger.Error("failed to retrieve teams: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to retrieve teams: %v", err))
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"teams":       teams,
		"count":       len(teams),
		"total_count": len(teams),
		"limit":       len(teams),
		"offset":      0,
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
	// Validate rate limit if provided
	if req.RateLimit != nil {
		rateLimit := configstoreTables.TableRateLimit{
			TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
			TokenResetDuration:   req.RateLimit.TokenResetDuration,
			RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
			RequestResetDuration: req.RateLimit.RequestResetDuration,
		}
		if err := validateRateLimit(&rateLimit); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid rate limit: %s", err.Error()))
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
				LastReset:     budgetLastReset(false, req.Budget.ResetDuration),
				CurrentUsage:  0,
			}
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			team.BudgetID = &budget.ID
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
			if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return err
			}
			team.RateLimitID = &rateLimit.ID
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
		// Track IDs to delete after updating the team (to avoid FK constraint)
		var budgetIDToDelete, rateLimitIDToDelete string

		// Update fields if provided
		if req.Name != nil {
			team.Name = *req.Name
		}
		if req.CustomerID != nil {
			if *req.CustomerID == "" {
				team.CustomerID = nil
			} else {
				team.CustomerID = req.CustomerID
			}
		}
		// Handle budget updates
		if req.Budget != nil {
			// Check if budget removal is requested (all fields nil)
			budgetIsEmpty := isBudgetRemovalRequest(req.Budget)
			if budgetIsEmpty {
				// Mark budget for deletion after FK is removed
				if team.BudgetID != nil {
					budgetIDToDelete = *team.BudgetID
					team.BudgetID = nil
					team.Budget = nil
				}
			} else if team.BudgetID != nil {
				// Update existing budget — all fields are optional (partial update)
				budget := configstoreTables.TableBudget{}
				if err := tx.First(&budget, "id = ?", *team.BudgetID).Error; err != nil {
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
				team.Budget = &budget
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
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     budgetLastReset(false, *req.Budget.ResetDuration),
					CurrentUsage:  0,
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				team.BudgetID = &budget.ID
				team.Budget = &budget
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if team.RateLimitID != nil {
					rateLimitIDToDelete = *team.RateLimitID
					team.RateLimitID = nil
					team.RateLimit = nil
				}
			} else if team.RateLimitID != nil {
				// Update existing rate limit
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *team.RateLimitID).Error; err != nil {
					return err
				}
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				team.RateLimit = &rateLimit
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
				team.RateLimitID = &rateLimit.ID
				team.RateLimit = &rateLimit
			}
		}
		if err := h.configStore.UpdateTeam(ctx, team, tx); err != nil {
			return err
		}

		// Now that FK references are removed, delete the orphaned budget/rate limit
		if budgetIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", budgetIDToDelete).Error; err != nil {
				return err
			}
		}
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
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
			"customers":   data.Customers,
			"count":       len(data.Customers),
			"total_count": len(data.Customers),
			"limit":       len(data.Customers),
			"offset":      0,
		})
		return
	}
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		limit, _ := strconv.Atoi(limitStr)
		offset, _ := strconv.Atoi(offsetStr)
		limit, offset = ClampPaginationParams(limit, offset)
		customers, totalCount, err := h.configStore.GetCustomersPaginated(ctx, configstore.CustomersQueryParams{
			Limit:  limit,
			Offset: offset,
			Search: search,
		})
		if err != nil {
			logger.Error("failed to retrieve customers: %v", err)
			SendError(ctx, 500, "failed to retrieve customers")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"customers":   customers,
			"count":       len(customers),
			"total_count": totalCount,
			"limit":       limit,
			"offset":      offset,
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
		"customers":   customers,
		"count":       len(customers),
		"total_count": len(customers),
		"limit":       len(customers),
		"offset":      0,
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
	// Validate rate limit if provided
	if req.RateLimit != nil {
		rateLimit := configstoreTables.TableRateLimit{
			TokenMaxLimit:        req.RateLimit.TokenMaxLimit,
			TokenResetDuration:   req.RateLimit.TokenResetDuration,
			RequestMaxLimit:      req.RateLimit.RequestMaxLimit,
			RequestResetDuration: req.RateLimit.RequestResetDuration,
		}
		if err := validateRateLimit(&rateLimit); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid rate limit: %s", err.Error()))
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
				LastReset:     budgetLastReset(false, req.Budget.ResetDuration),
				CurrentUsage:  0,
			}
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			customer.BudgetID = &budget.ID
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
			if err := h.configStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return err
			}
			customer.RateLimitID = &rateLimit.ID
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
		// Track IDs to delete after updating the customer (to avoid FK constraint)
		var budgetIDToDelete, rateLimitIDToDelete string

		// Update fields if provided
		if req.Name != nil {
			customer.Name = *req.Name
		}
		// Handle budget updates
		if req.Budget != nil {
			// Check if budget removal is requested (all fields nil)
			budgetIsEmpty := isBudgetRemovalRequest(req.Budget)
			if budgetIsEmpty {
				// Mark budget for deletion after FK is removed
				if customer.BudgetID != nil {
					budgetIDToDelete = *customer.BudgetID
					customer.BudgetID = nil
					customer.Budget = nil
				}
			} else if customer.BudgetID != nil {
				// Update existing budget — all fields are optional (partial update)
				budget := configstoreTables.TableBudget{}
				if err := tx.First(&budget, "id = ?", *customer.BudgetID).Error; err != nil {
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
				customer.Budget = &budget
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
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     budgetLastReset(false, *req.Budget.ResetDuration),
					CurrentUsage:  0,
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				customer.BudgetID = &budget.ID
				customer.Budget = &budget
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if customer.RateLimitID != nil {
					rateLimitIDToDelete = *customer.RateLimitID
					customer.RateLimitID = nil
					customer.RateLimit = nil
				}
			} else if customer.RateLimitID != nil {
				// Update existing rate limit
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *customer.RateLimitID).Error; err != nil {
					return err
				}
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				customer.RateLimit = &rateLimit
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
				customer.RateLimitID = &rateLimit.ID
				customer.RateLimit = &rateLimit
			}
		}
		if err := h.configStore.UpdateCustomer(ctx, customer, tx); err != nil {
			return err
		}

		// Now that FK references are removed, delete the orphaned budget/rate limit
		if budgetIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", budgetIDToDelete).Error; err != nil {
				return err
			}
		}
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
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

func (h *GovernanceHandler) getConfiguredProviderSet(ctx context.Context) (map[schemas.ModelProvider]struct{}, error) {
	providers, err := h.configStore.GetProviders(ctx)
	if err != nil {
		return nil, err
	}
	providerSet := make(map[schemas.ModelProvider]struct{}, len(providers))
	for _, provider := range providers {
		providerName := schemas.ModelProvider(strings.TrimSpace(provider.Name))
		if providerName == "" {
			continue
		}
		providerSet[providerName] = struct{}{}
	}
	return providerSet, nil
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

// Model Config CRUD Operations

// getModelConfigs handles GET /api/governance/model-configs - Get all model configs
func (h *GovernanceHandler) getModelConfigs(ctx *fasthttp.RequestCtx) {
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		SendJSON(ctx, map[string]any{
			"model_configs": data.ModelConfigs,
			"count":         len(data.ModelConfigs),
			"total_count":   len(data.ModelConfigs),
			"limit":         len(data.ModelConfigs),
			"offset":        0,
		})
		return
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		// Paginated path
		params := configstore.ModelConfigsQueryParams{
			Search: search,
		}
		if limitStr != "" {
			n, err := strconv.Atoi(limitStr)
			if err != nil {
				SendError(ctx, 400, "Invalid limit parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
				return
			}
			params.Limit = n
		}
		if offsetStr != "" {
			n, err := strconv.Atoi(offsetStr)
			if err != nil {
				SendError(ctx, 400, "Invalid offset parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
				return
			}
			params.Offset = n
		}

		params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)
		modelConfigs, totalCount, err := h.configStore.GetModelConfigsPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve model configs: %v", err)
			SendError(ctx, 500, "Failed to retrieve model configs")
			return
		}
		SendJSON(ctx, map[string]any{
			"model_configs": modelConfigs,
			"count":         len(modelConfigs),
			"total_count":   totalCount,
			"limit":         params.Limit,
			"offset":        params.Offset,
		})
		return
	}

	// Non-paginated path: return all model configs
	modelConfigs, err := h.configStore.GetModelConfigs(ctx)
	if err != nil {
		logger.Error("failed to retrieve model configs: %v", err)
		SendError(ctx, 500, "Failed to retrieve model configs")
		return
	}
	SendJSON(ctx, map[string]any{
		"model_configs": modelConfigs,
		"count":         len(modelConfigs),
		"total_count":   len(modelConfigs),
		"limit":         len(modelConfigs),
		"offset":        0,
	})
}

// getModelConfig handles GET /api/governance/model-configs/{mc_id} - Get a specific model config
func (h *GovernanceHandler) getModelConfig(ctx *fasthttp.RequestCtx) {
	mcID := ctx.UserValue("mc_id").(string)
	mc, err := h.configStore.GetModelConfigByID(ctx, mcID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve model config")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"model_config": mc,
	})
}

// createModelConfig handles POST /api/governance/model-configs - Create a new model config
func (h *GovernanceHandler) createModelConfig(ctx *fasthttp.RequestCtx) {
	var req CreateModelConfigRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Validate required fields
	if req.ModelName == "" {
		SendError(ctx, 400, "Model name is required")
		return
	}
	// Check if model config with same (model_name, provider) already exists
	existing, err := h.configStore.GetModelConfig(ctx, req.ModelName, req.Provider)
	if err != nil && err != configstore.ErrNotFound {
		logger.Error("failed to check existing model config: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to check existing model config: %v", err))
		return
	}
	if existing != nil {
		if req.Provider != nil {
			SendError(ctx, 409, fmt.Sprintf("Model config for model '%s' with provider '%s' already exists", req.ModelName, *req.Provider))
		} else {
			SendError(ctx, 409, fmt.Sprintf("Model config for model '%s' (global) already exists", req.ModelName))
		}
		return
	}
	// Validate budget if provided
	if req.Budget != nil {
		if req.Budget.MaxLimit < 0 {
			SendError(ctx, 400, fmt.Sprintf("Budget max_limit cannot be negative: %.2f", req.Budget.MaxLimit))
			return
		}
		if _, err := configstoreTables.ParseDuration(req.Budget.ResetDuration); err != nil {
			SendError(ctx, 400, fmt.Sprintf("Invalid reset duration format: %s", req.Budget.ResetDuration))
			return
		}
	}
	var mc configstoreTables.TableModelConfig
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		mc = configstoreTables.TableModelConfig{
			ID:        uuid.NewString(),
			ModelName: req.ModelName,
			Provider:  req.Provider,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		// Create budget if provided
		if req.Budget != nil {
			budget := configstoreTables.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      req.Budget.MaxLimit,
				ResetDuration: req.Budget.ResetDuration,
				LastReset:     budgetLastReset(false, req.Budget.ResetDuration),
				CurrentUsage:  0,
			}
			if err := validateBudget(&budget); err != nil {
				return err
			}
			if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
				return err
			}
			mc.BudgetID = &budget.ID
			mc.Budget = &budget
		}
		// Create rate limit if provided
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
			mc.RateLimitID = &rateLimit.ID
			mc.RateLimit = &rateLimit
		}
		if err := h.configStore.CreateModelConfig(ctx, &mc, tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		logger.Error("failed to create model config: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to create model config: %v", err))
		return
	}
	// Reload model config in memory
	preloadedMC, err := h.governanceManager.ReloadModelConfig(ctx, mc.ID)
	if err != nil {
		logger.Error("failed to reload model config in memory: %v", err)
		preloadedMC = &mc
	}
	SendJSON(ctx, map[string]interface{}{
		"message":      "Model config created successfully",
		"model_config": preloadedMC,
	})
}

// updateModelConfig handles PUT /api/governance/model-configs/{mc_id} - Update a model config
func (h *GovernanceHandler) updateModelConfig(ctx *fasthttp.RequestCtx) {
	mcID := ctx.UserValue("mc_id").(string)
	var req UpdateModelConfigRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	mc, err := h.configStore.GetModelConfigByID(ctx, mcID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve model config")
		return
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Track IDs to delete after updating the model config (to avoid FK constraint)
		var budgetIDToDelete, rateLimitIDToDelete string

		// Update fields if provided
		if req.ModelName != nil {
			mc.ModelName = *req.ModelName
		}
		// Update provider if provided in request
		if req.Provider != nil {
			mc.Provider = req.Provider
		}
		// Handle budget updates
		if req.Budget != nil {
			// Check if budget removal is requested (all fields nil)
			budgetIsEmpty := isBudgetRemovalRequest(req.Budget)
			if budgetIsEmpty {
				// Mark budget for deletion after FK is removed
				if mc.BudgetID != nil {
					budgetIDToDelete = *mc.BudgetID
					mc.BudgetID = nil
					mc.Budget = nil
				}
			} else if mc.BudgetID != nil {
				// Update existing budget — all fields are optional (partial update)
				budget := configstoreTables.TableBudget{}
				if err := tx.First(&budget, "id = ?", *mc.BudgetID).Error; err != nil {
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
				mc.Budget = &budget
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
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     budgetLastReset(false, *req.Budget.ResetDuration),
					CurrentUsage:  0,
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				mc.BudgetID = &budget.ID
				mc.Budget = &budget
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if mc.RateLimitID != nil {
					rateLimitIDToDelete = *mc.RateLimitID
					mc.RateLimitID = nil
					mc.RateLimit = nil
				}
			} else if mc.RateLimitID != nil {
				// Update existing rate limit - set ALL fields from request (nil means clear)
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *mc.RateLimitID).Error; err != nil {
					return err
				}
				// Set all fields from request - nil values will clear the field
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				mc.RateLimit = &rateLimit
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
				mc.RateLimitID = &rateLimit.ID
				mc.RateLimit = &rateLimit
			}
		}
		mc.UpdatedAt = time.Now()
		if err := h.configStore.UpdateModelConfig(ctx, mc, tx); err != nil {
			return err
		}

		// Now that FK references are removed, delete the orphaned budget/rate limit
		if budgetIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", budgetIDToDelete).Error; err != nil {
				return err
			}
		}
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		logger.Error("failed to update model config: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to update model config: %v", err))
		return
	}
	// Reload model config in memory (also reloads from DB to get full relationships)
	updatedMC, err := h.governanceManager.ReloadModelConfig(ctx, mc.ID)
	if err != nil {
		logger.Error("failed to reload model config in memory: %v", err)
		updatedMC = mc
	}
	SendJSON(ctx, map[string]interface{}{
		"message":      "Model config updated successfully",
		"model_config": updatedMC,
	})
}

// deleteModelConfig handles DELETE /api/governance/model-configs/{mc_id} - Delete a model config
func (h *GovernanceHandler) deleteModelConfig(ctx *fasthttp.RequestCtx) {
	mcID := ctx.UserValue("mc_id").(string)
	// Check if model config exists
	_, err := h.configStore.GetModelConfigByID(ctx, mcID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve model config")
		return
	}
	// Delete the model config
	if err := h.configStore.DeleteModelConfig(ctx, mcID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Model config not found")
			return
		}
		logger.Error("failed to delete model config: %v", err)
		SendError(ctx, 500, "Failed to delete model config")
		return
	}
	// Remove model config from in-memory store
	if err := h.governanceManager.RemoveModelConfig(ctx, mcID); err != nil {
		logger.Error("failed to remove model config from memory: %v", err)
		// Continue anyway, the config is deleted from DB
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Model config deleted successfully",
	})
}

// Provider Governance Operations

// ProviderGovernanceResponse represents a provider with its governance settings
type ProviderGovernanceResponse struct {
	Provider  string                            `json:"provider"`
	Budget    *configstoreTables.TableBudget    `json:"budget,omitempty"`
	RateLimit *configstoreTables.TableRateLimit `json:"rate_limit,omitempty"`
}

// getProviderGovernance handles GET /api/governance/providers - Get all providers with governance settings
func (h *GovernanceHandler) getProviderGovernance(ctx *fasthttp.RequestCtx) {
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		data := h.governanceManager.GetGovernanceData()
		if data == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		var result []ProviderGovernanceResponse
		for _, p := range data.Providers {
			if p.Budget != nil || p.RateLimit != nil {
				result = append(result, ProviderGovernanceResponse{
					Provider:  p.Name,
					Budget:    p.Budget,
					RateLimit: p.RateLimit,
				})
			}
		}
		SendJSON(ctx, map[string]interface{}{
			"providers": result,
			"count":     len(result),
		})
		return
	}
	providers, err := h.configStore.GetProviders(ctx)
	if err != nil {
		logger.Error("failed to retrieve providers: %v", err)
		SendError(ctx, 500, "Failed to retrieve providers")
		return
	}
	// Transform to governance response format
	var result []ProviderGovernanceResponse
	for _, p := range providers {
		if p.Budget != nil || p.RateLimit != nil {
			result = append(result, ProviderGovernanceResponse{
				Provider:  p.Name,
				Budget:    p.Budget,
				RateLimit: p.RateLimit,
			})
		}
	}
	SendJSON(ctx, map[string]interface{}{
		"providers": result,
		"count":     len(result),
	})
}

// updateProviderGovernance handles PUT /api/governance/providers/{provider_name} - Update provider governance
func (h *GovernanceHandler) updateProviderGovernance(ctx *fasthttp.RequestCtx) {
	providerName := ctx.UserValue("provider_name").(string)
	var req UpdateProviderGovernanceRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}
	// Get all providers and find the one we need
	providers, err := h.configStore.GetProviders(ctx)
	if err != nil {
		SendError(ctx, 500, "Failed to retrieve providers")
		return
	}
	var provider *configstoreTables.TableProvider
	for i := range providers {
		if providers[i].Name == providerName {
			provider = &providers[i]
			break
		}
	}
	if provider == nil {
		SendError(ctx, 404, "Provider not found")
		return
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Track IDs to delete after updating the provider (to avoid FK constraint)
		var budgetIDToDelete, rateLimitIDToDelete string

		// Handle budget updates
		if req.Budget != nil {
			// Check if budget removal is requested (all fields nil)
			budgetIsEmpty := isBudgetRemovalRequest(req.Budget)
			if budgetIsEmpty {
				// Mark budget for deletion after FK is removed
				if provider.BudgetID != nil {
					budgetIDToDelete = *provider.BudgetID
					provider.BudgetID = nil
					provider.Budget = nil
				}
			} else if provider.BudgetID != nil {
				// Update existing budget — all fields are optional (partial update)
				budget := configstoreTables.TableBudget{}
				if err := tx.First(&budget, "id = ?", *provider.BudgetID).Error; err != nil {
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
				provider.Budget = &budget
			} else {
				// Create new budget
				if req.Budget.MaxLimit == nil || req.Budget.ResetDuration == nil {
					return fmt.Errorf("both max_limit and reset_duration are required when creating a new budget")
				}
				budget := configstoreTables.TableBudget{
					ID:            uuid.NewString(),
					MaxLimit:      *req.Budget.MaxLimit,
					ResetDuration: *req.Budget.ResetDuration,
					LastReset:     budgetLastReset(false, *req.Budget.ResetDuration),
					CurrentUsage:  0,
				}
				if err := validateBudget(&budget); err != nil {
					return err
				}
				if err := h.configStore.CreateBudget(ctx, &budget, tx); err != nil {
					return err
				}
				provider.BudgetID = &budget.ID
				provider.Budget = &budget
			}
		}
		// Handle rate limit updates
		if req.RateLimit != nil {
			// Check if rate limit values are empty - means remove rate limit (reset durations don't matter)
			rateLimitIsEmpty := req.RateLimit.TokenMaxLimit == nil && req.RateLimit.RequestMaxLimit == nil
			if rateLimitIsEmpty {
				// Mark rate limit for deletion after FK is removed
				if provider.RateLimitID != nil {
					rateLimitIDToDelete = *provider.RateLimitID
					provider.RateLimitID = nil
					provider.RateLimit = nil
				}
			} else if provider.RateLimitID != nil {
				// Update existing rate limit - set ALL fields from request (nil means clear)
				rateLimit := configstoreTables.TableRateLimit{}
				if err := tx.First(&rateLimit, "id = ?", *provider.RateLimitID).Error; err != nil {
					return err
				}
				// Set all fields from request - nil values will clear the field
				rateLimit.TokenMaxLimit = req.RateLimit.TokenMaxLimit
				rateLimit.TokenResetDuration = req.RateLimit.TokenResetDuration
				rateLimit.RequestMaxLimit = req.RateLimit.RequestMaxLimit
				rateLimit.RequestResetDuration = req.RateLimit.RequestResetDuration
				if err := validateRateLimit(&rateLimit); err != nil {
					return err
				}
				if err := h.configStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
					return err
				}
				provider.RateLimit = &rateLimit
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
				provider.RateLimitID = &rateLimit.ID
				provider.RateLimit = &rateLimit
			}
		}
		// Update only budget/rate limit FK references (avoid overwriting encrypted fields)
		if err := tx.Model(provider).Select("budget_id", "rate_limit_id").Updates(provider).Error; err != nil {
			return err
		}

		// Now that FK references are removed, delete the orphaned budget/rate limit
		if budgetIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", budgetIDToDelete).Error; err != nil {
				return err
			}
		}
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		logger.Error("failed to update provider governance: %v", err)
		SendError(ctx, 500, fmt.Sprintf("Failed to update provider governance: %v", err))
		return
	}
	// Reload provider in memory
	updatedProvider, err := h.governanceManager.ReloadProvider(ctx, schemas.ModelProvider(providerName))
	if err != nil {
		logger.Error("failed to reload provider in memory: %v", err)
		// Use the local provider object if reload fails
	} else {
		provider = updatedProvider
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Provider governance updated successfully",
		"provider": ProviderGovernanceResponse{
			Provider:  provider.Name,
			Budget:    provider.Budget,
			RateLimit: provider.RateLimit,
		},
	})
}

// deleteProviderGovernance handles DELETE /api/governance/providers/{provider_name} - Remove governance from provider
func (h *GovernanceHandler) deleteProviderGovernance(ctx *fasthttp.RequestCtx) {
	providerName := ctx.UserValue("provider_name").(string)
	// Get all providers and find the one we need
	providers, err := h.configStore.GetProviders(ctx)
	if err != nil {
		SendError(ctx, 500, "Failed to retrieve providers")
		return
	}
	var provider *configstoreTables.TableProvider
	for i := range providers {
		if providers[i].Name == providerName {
			provider = &providers[i]
			break
		}
	}
	if provider == nil {
		SendError(ctx, 404, "Provider not found")
		return
	}
	if err := h.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Store IDs to delete after removing FK references
		var budgetIDToDelete, rateLimitIDToDelete string

		if provider.BudgetID != nil {
			budgetIDToDelete = *provider.BudgetID
			provider.BudgetID = nil
			provider.Budget = nil
		}
		if provider.RateLimitID != nil {
			rateLimitIDToDelete = *provider.RateLimitID
			provider.RateLimitID = nil
			provider.RateLimit = nil
		}

		// Update only budget/rate limit FK references (avoid overwriting encrypted fields)
		if err := tx.Model(provider).Select("budget_id", "rate_limit_id").Updates(provider).Error; err != nil {
			return err
		}

		// Now delete the orphaned budget/rate limit
		if budgetIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableBudget{}, "id = ?", budgetIDToDelete).Error; err != nil {
				return err
			}
		}
		if rateLimitIDToDelete != "" {
			if err := tx.Delete(&configstoreTables.TableRateLimit{}, "id = ?", rateLimitIDToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		logger.Error("failed to delete provider governance: %v", err)
		SendError(ctx, 500, "Failed to delete provider governance")
		return
	}
	// Reload provider in memory (to clear the budget/rate limit)
	if _, err := h.governanceManager.ReloadProvider(ctx, schemas.ModelProvider(providerName)); err != nil {
		logger.Error("failed to reload provider in memory: %v", err)
		// Continue anyway, the governance is deleted from DB
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Provider governance deleted successfully",
	})
}

// Routing Rules CRUD Operations

// getRoutingRules retrieves all routing rules with optional filtering from database
func (h *GovernanceHandler) getRoutingRules(ctx *fasthttp.RequestCtx) {
	// Get query parameters for filtering
	scope := string(ctx.QueryArgs().Peek("scope"))
	scopeID := string(ctx.QueryArgs().Peek("scope_id"))

	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		gd := h.governanceManager.GetGovernanceData()
		if gd == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		inMemoryRules := gd.RoutingRules

		// Filter rules by scope and scopeID
		var rules []configstoreTables.TableRoutingRule
		for _, rule := range inMemoryRules {
			if scope != "" && rule.Scope != scope {
				continue
			}
			if scopeID != "" {
				ruleScope := ""
				if rule.ScopeID != nil {
					ruleScope = *rule.ScopeID
				}
				if ruleScope != scopeID {
					continue
				}
			}
			rules = append(rules, *rule)
		}

		SendJSON(ctx, map[string]interface{}{
			"rules":       rules,
			"count":       len(rules),
			"total_count": len(rules),
			"limit":       len(rules),
			"offset":      0,
		})
		return
	}

	// If scope/scopeID filters are specified, use the existing non-paginated path
	if scope != "" || scopeID != "" {
		rules, err := h.configStore.GetRoutingRulesByScope(ctx, scope, scopeID)
		if err != nil {
			SendError(ctx, 500, "Failed to get routing rules")
			return
		}
		response := make([]configstoreTables.TableRoutingRule, 0, len(rules))
		for _, rule := range rules {
			response = append(response, rule)
		}
		SendJSON(ctx, map[string]interface{}{
			"rules":       response,
			"count":       len(response),
			"total_count": len(response),
			"limit":       len(response),
			"offset":      0,
		})
		return
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		// Paginated path
		params := configstore.RoutingRulesQueryParams{
			Search: search,
		}
		if limitStr != "" {
			n, err := strconv.Atoi(limitStr)
			if err != nil {
				SendError(ctx, 400, "Invalid limit parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
				return
			}
			params.Limit = n
		}
		if offsetStr != "" {
			n, err := strconv.Atoi(offsetStr)
			if err != nil {
				SendError(ctx, 400, "Invalid offset parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
				return
			}
			params.Offset = n
		}

		params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)
		rules, totalCount, err := h.configStore.GetRoutingRulesPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve routing rules: %v", err)
			SendError(ctx, 500, "Failed to retrieve routing rules")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"rules":       rules,
			"count":       len(rules),
			"total_count": totalCount,
			"limit":       params.Limit,
			"offset":      params.Offset,
		})
		return
	}

	// Non-paginated path: return all routing rules
	rules, err := h.configStore.GetRoutingRules(ctx)
	if err != nil {
		logger.Error("failed to retrieve routing rules: %v", err)
		SendError(ctx, 500, "Failed to retrieve routing rules")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"rules":       rules,
		"count":       len(rules),
		"total_count": len(rules),
		"limit":       len(rules),
		"offset":      0,
	})
}

// getRoutingRule retrieves a single routing rule by ID from database
func (h *GovernanceHandler) getRoutingRule(ctx *fasthttp.RequestCtx) {
	ruleID := ctx.UserValue("rule_id").(string)

	var rule *configstoreTables.TableRoutingRule
	var err error

	// Check if "from_memory" query parameter is set to true
	fromMemory := string(ctx.QueryArgs().Peek("from_memory")) == "true"
	if fromMemory {
		gd := h.governanceManager.GetGovernanceData()
		if gd == nil {
			SendError(ctx, 500, "Governance data is not available")
			return
		}
		inMemoryRules := gd.RoutingRules

		// Find rule by ID in memory
		for _, r := range inMemoryRules {
			if r.ID == ruleID {
				rule = r
				break
			}
		}
		if rule == nil {
			SendError(ctx, 404, "Routing rule not found")
			return
		}
	} else {
		rule, err = h.configStore.GetRoutingRule(ctx, ruleID)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, 404, "Routing rule not found")
				return
			}
			logger.Error("failed to get routing rule: %v", err)
			SendError(ctx, 500, "Failed to retrieve routing rule")
			return
		}
	}

	SendJSON(ctx, map[string]interface{}{
		"rule": rule,
	})
}

// createRoutingRule creates a new routing rule
func (h *GovernanceHandler) createRoutingRule(ctx *fasthttp.RequestCtx) {
	// Parse request body
	var req CreateRoutingRuleRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}

	// Validate required fields
	if req.Name == "" {
		SendError(ctx, 400, "name field is required")
		return
	}

	// Validate targets
	if len(req.Targets) == 0 {
		SendError(ctx, 400, "at least one target is required")
		return
	}
	if err := validateRoutingTargets(req.Targets); err != nil {
		SendError(ctx, 400, err.Error())
		return
	}

	// Set defaults and normalize scope/scope_id
	scope := req.Scope
	if scope == "" {
		scope = "global"
	}

	// Validate scope value before normalization
	if err := validateRoutingScope(scope); err != nil {
		SendError(ctx, 400, err.Error())
		return
	}

	// Validate: scope_id required for non-global scopes; must be nil/empty for global
	if scope == "global" {
		req.ScopeID = nil // normalize: global rules must not have scope_id
	} else if req.ScopeID == nil || *req.ScopeID == "" {
		SendError(ctx, 400, "scope_id field is required when scope is not global")
		return
	}

	// Build targets
	ruleID := uuid.NewString()
	targets := make([]configstoreTables.TableRoutingTarget, 0, len(req.Targets))
	for _, t := range req.Targets {
		targets = append(targets, configstoreTables.TableRoutingTarget{
			Provider: t.Provider,
			Model:    t.Model,
			KeyID:    t.KeyID,
			Weight:   t.Weight,
		})
	}

	// Create routing rule
	// Handle Enabled/ChainRule: nil means use DB default (true/false), otherwise use provided value
	enabled := true // DB default
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	chainRule := false // DB default
	if req.ChainRule != nil {
		chainRule = *req.ChainRule
	}
	rule := &configstoreTables.TableRoutingRule{
		ID:              ruleID,
		Name:            req.Name,
		Description:     req.Description,
		Enabled:         enabled,
		ChainRule:       chainRule,
		CelExpression:   req.CelExpression,
		Targets:         targets,
		Scope:           scope,
		ScopeID:         req.ScopeID,
		Priority:        req.Priority,
		ParsedFallbacks: req.Fallbacks,
		ParsedQuery:     req.Query,
	}

	// Create in database
	if err := h.configStore.CreateRoutingRule(ctx, rule); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to create routing rule: %v", err))
		return
	}

	// Update in-memory store via manager callback
	if err := h.governanceManager.ReloadRoutingRule(ctx, rule.ID); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to reload routing rule in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule created successfully",
		"rule":    rule,
	})
}

// updateRoutingRule updates an existing routing rule
func (h *GovernanceHandler) updateRoutingRule(ctx *fasthttp.RequestCtx) {
	ruleID := ctx.UserValue("rule_id").(string)

	// Parse request body
	var req UpdateRoutingRuleRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, 400, "Invalid JSON")
		return
	}

	rule, err := h.configStore.GetRoutingRule(ctx, ruleID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Routing rule not found")
			return
		}
		logger.Error("failed to get routing rule: %v", err)
		SendError(ctx, 500, "Failed to retrieve routing rule")
		return
	}

	// Update fields if provided
	if req.Name != nil && *req.Name != "" {
		rule.Name = *req.Name
	}
	if req.Description != nil {
		rule.Description = *req.Description
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.ChainRule != nil {
		rule.ChainRule = *req.ChainRule
	}
	if req.CelExpression != nil {
		rule.CelExpression = *req.CelExpression
	}
	if req.Targets != nil {
		if len(req.Targets) == 0 {
			SendError(ctx, 400, "at least one routing target is required")
			return
		}
		if err := validateRoutingTargets(req.Targets); err != nil {
			SendError(ctx, 400, err.Error())
			return
		}
		newTargets := make([]configstoreTables.TableRoutingTarget, 0, len(req.Targets))
		for _, t := range req.Targets {
			newTargets = append(newTargets, configstoreTables.TableRoutingTarget{
				Provider: t.Provider,
				Model:    t.Model,
				KeyID:    t.KeyID,
				Weight:   t.Weight,
			})
		}
		rule.Targets = newTargets
	}
	if req.Priority != nil {
		rule.Priority = *req.Priority
	}
	if req.Query != nil {
		rule.ParsedQuery = req.Query
	}
	if req.Fallbacks != nil {
		rule.ParsedFallbacks = req.Fallbacks
	}
	if req.Scope != nil && *req.Scope != "" {
		// Validate scope value before updating
		if err := validateRoutingScope(*req.Scope); err != nil {
			SendError(ctx, 400, err.Error())
			return
		}
		rule.Scope = *req.Scope
	}
	if req.ScopeID != nil {
		rule.ScopeID = req.ScopeID
	}

	// If scope is global, ensure scope_id is nil
	if rule.Scope == "global" {
		rule.ScopeID = nil
	} else if rule.ScopeID == nil || *rule.ScopeID == "" {
		SendError(ctx, 400, "scope_id field is required when scope is not global")
		return
	}

	// Update in database
	if err := h.configStore.UpdateRoutingRule(ctx, rule); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to update routing rule in database: %v", err))
		return
	}

	// Update in-memory store via manager callback
	if err := h.governanceManager.ReloadRoutingRule(ctx, rule.ID); err != nil {
		SendError(ctx, 500, fmt.Sprintf("Failed to reload routing rule in memory: %v, please restart bifrost to sync with the database", err))
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule updated successfully",
		"rule":    rule,
	})
}

// deleteRoutingRule deletes a routing rule
func (h *GovernanceHandler) deleteRoutingRule(ctx *fasthttp.RequestCtx) {
	ruleID := ctx.UserValue("rule_id").(string)

	// Delete from database
	if err := h.configStore.DeleteRoutingRule(ctx, ruleID); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 404, "Routing rule not found")
			return
		}
		SendError(ctx, 500, fmt.Sprintf("Failed to delete routing rule from database: %v", err))
		return
	}

	// Remove from in-memory store via manager callback (non-fatal: DB already updated)
	if err := h.governanceManager.RemoveRoutingRule(ctx, ruleID); err != nil {
		logger.Error("failed to remove routing rule from memory: %v", err)
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Routing rule deleted successfully",
	})
}

// ---------------------------------------------------------------------------
// Pricing Override Operations
// ---------------------------------------------------------------------------

// CreatePricingOverrideRequest is the request payload for creating a governance
// pricing override.
type CreatePricingOverrideRequest struct {
	Name          string                      `json:"name"`
	ScopeKind     modelcatalog.ScopeKind      `json:"scope_kind"`
	VirtualKeyID  *string                     `json:"virtual_key_id,omitempty"`
	ProviderID    *string                     `json:"provider_id,omitempty"`
	ProviderKeyID *string                     `json:"provider_key_id,omitempty"`
	MatchType     modelcatalog.MatchType      `json:"match_type"`
	Pattern       string                      `json:"pattern"`
	RequestTypes  []schemas.RequestType       `json:"request_types,omitempty"`
	Patch         modelcatalog.PricingOptions `json:"patch,omitempty"`
}

// nullableString tracks whether a JSON string field was explicitly present in
// the request body (even as null), so the merge logic can distinguish "omitted"
// (leave existing value) from "set to null" (clear the value).
type nullableString struct {
	Value *string
	Set   bool
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if string(b) == "null" {
		n.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// UpdatePricingOverrideRequest is the request payload for updating a governance
// pricing override. All fields except Patch are optional — omitted fields are
// merged from the existing record. Patch is always replaced in full.
type UpdatePricingOverrideRequest struct {
	Name          *string                      `json:"name,omitempty"`
	ScopeKind     *modelcatalog.ScopeKind      `json:"scope_kind,omitempty"`
	VirtualKeyID  nullableString               `json:"virtual_key_id"`
	ProviderID    nullableString               `json:"provider_id"`
	ProviderKeyID nullableString               `json:"provider_key_id"`
	MatchType     *modelcatalog.MatchType      `json:"match_type,omitempty"`
	Pattern       *string                      `json:"pattern,omitempty"`
	RequestTypes  []schemas.RequestType        `json:"request_types,omitempty"`
	Patch         *modelcatalog.PricingOptions `json:"patch,omitempty"`
}

func (h *GovernanceHandler) getPricingOverrides(ctx *fasthttp.RequestCtx) {
	// Parse filter parameters
	var scopeKind, virtualKeyID, providerID, providerKeyID *string
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("scope_kind"))); v != "" {
		scopeKind = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("virtual_key_id"))); v != "" {
		virtualKeyID = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider_id"))); v != "" {
		providerID = &v
	}
	if v := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider_key_id"))); v != "" {
		providerKeyID = &v
	}

	// Check for pagination parameters
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	search := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || search != "" {
		params := configstore.PricingOverridesQueryParams{
			Search:        search,
			ScopeKind:     scopeKind,
			VirtualKeyID:  virtualKeyID,
			ProviderID:    providerID,
			ProviderKeyID: providerKeyID,
		}
		if limitStr != "" {
			n, err := strconv.Atoi(limitStr)
			if err != nil {
				SendError(ctx, 400, "Invalid limit parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
				return
			}
			params.Limit = n
		}
		if offsetStr != "" {
			n, err := strconv.Atoi(offsetStr)
			if err != nil {
				SendError(ctx, 400, "Invalid offset parameter: must be a number")
				return
			}
			if n < 0 {
				SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
				return
			}
			params.Offset = n
		}

		params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)
		overrides, totalCount, err := h.configStore.GetPricingOverridesPaginated(ctx, params)
		if err != nil {
			logger.Error("failed to retrieve pricing overrides: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve pricing overrides")
			return
		}
		SendJSON(ctx, map[string]interface{}{
			"pricing_overrides": overrides,
			"count":             len(overrides),
			"total_count":       totalCount,
			"limit":             params.Limit,
			"offset":            params.Offset,
		})
		return
	}

	// Non-paginated path: return all matching overrides (backward compatible)
	filters := configstore.PricingOverrideFilters{
		ScopeKind:     scopeKind,
		VirtualKeyID:  virtualKeyID,
		ProviderID:    providerID,
		ProviderKeyID: providerKeyID,
	}
	overrides, err := h.configStore.GetPricingOverrides(ctx, filters)
	if err != nil {
		logger.Error("failed to retrieve pricing overrides: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve pricing overrides")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"pricing_overrides": overrides,
		"count":             len(overrides),
		"total_count":       len(overrides),
		"limit":             len(overrides),
		"offset":            0,
	})
}

func (h *GovernanceHandler) createPricingOverride(ctx *fasthttp.RequestCtx) {
	var req CreatePricingOverrideRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}

	name, err := normalizeAndValidatePricingOverrideName(req.Name)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	shape := modelcatalog.PricingOverride{
		ScopeKind:     req.ScopeKind,
		VirtualKeyID:  req.VirtualKeyID,
		ProviderID:    req.ProviderID,
		ProviderKeyID: req.ProviderKeyID,
		MatchType:     req.MatchType,
		Pattern:       req.Pattern,
		RequestTypes:  req.RequestTypes,
	}
	if err := shape.IsValid(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	patchJSON, err := sonic.Marshal(req.Patch)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid patch")
		return
	}

	now := time.Now()
	override := configstoreTables.TablePricingOverride{
		ID:               uuid.NewString(),
		Name:             name,
		ScopeKind:        string(req.ScopeKind),
		VirtualKeyID:     normalizeOptionalString(req.VirtualKeyID),
		ProviderID:       normalizeOptionalString(req.ProviderID),
		ProviderKeyID:    normalizeOptionalString(req.ProviderKeyID),
		MatchType:        string(req.MatchType),
		Pattern:          strings.TrimSpace(req.Pattern),
		RequestTypes:     req.RequestTypes,
		PricingPatchJSON: string(patchJSON),
		ConfigHash:       "",
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := h.configStore.CreatePricingOverride(ctx, &override); err != nil {
		logger.Error("failed to create pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create pricing override")
		return
	}

	if err := h.governanceManager.UpsertPricingOverride(ctx, &override); err != nil {
		logger.Error("failed to upsert pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to upsert pricing override")
		return
	}
	SendJSONWithStatus(ctx, map[string]interface{}{
		"message":          "Pricing override created successfully",
		"pricing_override": override,
	}, fasthttp.StatusCreated)
}

func (h *GovernanceHandler) updatePricingOverride(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)

	var req UpdatePricingOverrideRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}

	existing, err := h.configStore.GetPricingOverrideByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Pricing override not found")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to retrieve pricing override: %v", err))
		return
	}

	// Merge request fields onto the existing record; omitted fields keep their current values.
	merged := modelcatalog.PricingOverride{
		ScopeKind:     modelcatalog.ScopeKind(existing.ScopeKind),
		VirtualKeyID:  existing.VirtualKeyID,
		ProviderID:    existing.ProviderID,
		ProviderKeyID: existing.ProviderKeyID,
		MatchType:     modelcatalog.MatchType(existing.MatchType),
		Pattern:       existing.Pattern,
		RequestTypes:  existing.RequestTypes,
	}
	if req.ScopeKind != nil {
		merged.ScopeKind = *req.ScopeKind
		// Changing scope_kind resets all scope IDs; only what the request
		// explicitly provides will be kept.
		merged.VirtualKeyID = nil
		merged.ProviderID = nil
		merged.ProviderKeyID = nil
	}
	if req.VirtualKeyID.Set {
		merged.VirtualKeyID = req.VirtualKeyID.Value
	}
	if req.ProviderID.Set {
		merged.ProviderID = req.ProviderID.Value
	}
	if req.ProviderKeyID.Set {
		merged.ProviderKeyID = req.ProviderKeyID.Value
	}
	if req.MatchType != nil {
		merged.MatchType = *req.MatchType
	}
	if req.Pattern != nil {
		merged.Pattern = *req.Pattern
	}
	if req.RequestTypes != nil {
		merged.RequestTypes = req.RequestTypes
	}

	if err := merged.IsValid(); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Resolve name: use provided value or fall back to existing.
	nameStr := existing.Name
	if req.Name != nil {
		nameStr, err = normalizeAndValidatePricingOverrideName(*req.Name)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
	}

	// Patch JSON: always replace in full with whatever is provided (or keep existing if omitted).
	pricingPatchJSON := existing.PricingPatchJSON
	if req.Patch != nil {
		b, err := sonic.Marshal(req.Patch)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid patch")
			return
		}
		pricingPatchJSON = string(b)
	}

	override := configstoreTables.TablePricingOverride{
		ID:               id,
		Name:             nameStr,
		ScopeKind:        string(merged.ScopeKind),
		VirtualKeyID:     normalizeOptionalString(merged.VirtualKeyID),
		ProviderID:       normalizeOptionalString(merged.ProviderID),
		ProviderKeyID:    normalizeOptionalString(merged.ProviderKeyID),
		MatchType:        string(merged.MatchType),
		Pattern:          strings.TrimSpace(merged.Pattern),
		RequestTypes:     merged.RequestTypes,
		PricingPatchJSON: pricingPatchJSON,
		ConfigHash:       existing.ConfigHash,
		CreatedAt:        existing.CreatedAt,
		UpdatedAt:        time.Now(),
	}

	if err := h.configStore.UpdatePricingOverride(ctx, &override); err != nil {
		logger.Error("failed to update pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to update pricing override")
		return
	}

	if err := h.governanceManager.UpsertPricingOverride(ctx, &override); err != nil {
		logger.Error("failed to upsert pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to upsert pricing override")
		return
	}
	SendJSON(ctx, map[string]interface{}{
		"message":          "Pricing override updated successfully",
		"pricing_override": override,
	})
}

func (h *GovernanceHandler) deletePricingOverride(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)
	if err := h.configStore.DeletePricingOverride(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Pricing override not found")
			return
		}
		logger.Error("failed to delete pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete pricing override")
		return
	}

	if err := h.governanceManager.DeletePricingOverride(ctx, id); err != nil {
		logger.Warn("failed to delete pricing override from memory: %v", err)
	}
	SendJSON(ctx, map[string]interface{}{
		"message": "Pricing override deleted successfully",
	})
}

func normalizeAndValidatePricingOverrideName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("name is required")
	}
	return trimmed, nil
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// validRoutingScopes contains the allowed scope values for routing rules
var validRoutingScopes = map[string]bool{
	"global":      true,
	"team":        true,
	"customer":    true,
	"virtual_key": true,
}

// validateRoutingScope validates that the scope value is one of the allowed values
func validateRoutingScope(scope string) error {
	if scope == "" {
		return nil // Empty scope will default to "global" later
	}
	if !validRoutingScopes[scope] {
		return fmt.Errorf("invalid scope %q: must be one of: global, team, customer, virtual_key", scope)
	}
	return nil
}

// validateRoutingTargets checks that all weights are positive, that no two
// targets share the same (provider, model, key_id) identity, and that all
// weights sum to 1.
func validateRoutingTargets(targets []RoutingTarget) error {
	seen := make(map[string]struct{}, len(targets))
	total := 0.0
	for _, t := range targets {
		if t.Weight < 0 {
			return fmt.Errorf("each target weight must be positive")
		}
		if t.KeyID != nil && *t.KeyID != "" && (t.Provider == nil || *t.Provider == "") {
			return fmt.Errorf("key_id requires provider to be set")
		}

		// Canonicalise identity: lowercase provider/model, treat nil == "".
		provider := ""
		if t.Provider != nil {
			provider = strings.ToLower(*t.Provider)
		}
		model := ""
		if t.Model != nil {
			model = strings.ToLower(*t.Model)
		}
		keyID := ""
		if t.KeyID != nil {
			keyID = *t.KeyID
		}
		key := provider + "|" + model + "|" + keyID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate target entry: provider=%q model=%q key_id=%q", provider, model, keyID)
		}
		seen[key] = struct{}{}

		total += t.Weight
	}
	if math.Abs(total-1.0) > 0.001 {
		return fmt.Errorf("target weights must sum to 1, got %.4f", total)
	}
	return nil
}

// getVirtualKeyQuota handles GET /api/governance/virtual-keys/quota
// This is a self-service endpoint — no admin auth required. The VK value in the header is the credential.
func (h *GovernanceHandler) getVirtualKeyQuota(ctx *fasthttp.RequestCtx) {
	// Extract virtual key using the same logic as the inference path (lib/ctx.go):
	// x-bf-vk accepts any value; other headers require the sk-bf- prefix.
	var vkValue string
	if v := string(ctx.Request.Header.Peek("x-bf-vk")); v != "" {
		vkValue = v
	} else if v := governance.ParseVirtualKeyFromFastHTTPRequest(ctx); v != nil {
		vkValue = *v
	}
	if vkValue == "" {
		SendError(ctx, 401, "Missing virtual key. Provide it via x-bf-vk header, Authorization Bearer, x-api-key, or x-goog-api-key header.")
		return
	}

	vk, err := h.configStore.GetVirtualKeyQuotaByValue(ctx, vkValue)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, 401, "Virtual key not found")
			return
		}
		SendError(ctx, 500, "Failed to retrieve virtual key")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"virtual_key_name": vk.Name,
		"is_active":        vk.IsActive,
		"budgets":          vk.Budgets,
		"rate_limit":       vk.RateLimit,
	})
}
