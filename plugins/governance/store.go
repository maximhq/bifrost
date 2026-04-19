// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/routing"
	"gorm.io/gorm"
)

type EntityWiseBudgets map[string][]*configstoreTables.TableBudget
type EntityWiseRateLimits map[string][]*configstoreTables.TableRateLimit

// LocalGovernanceStore provides in-memory cache for governance data with fast, non-blocking access
type LocalGovernanceStore struct {
	// Core data maps using sync.Map for lock-free reads
	virtualKeys  sync.Map // string -> *VirtualKey (VK value -> VirtualKey with preloaded relationships)
	teams        sync.Map // string -> *Team (Team ID -> Team)
	customers    sync.Map // string -> *Customer (Customer ID -> Customer)
	budgets      sync.Map // string -> *Budget (Budget ID -> Budget)
	rateLimits   sync.Map // string -> *RateLimit (RateLimit ID -> RateLimit)
	modelConfigs sync.Map // string -> *ModelConfig (key: "modelName" or "modelName:provider" -> ModelConfig)
	providers    sync.Map // string -> *Provider (Provider name -> Provider with preloaded relationships)
	routingRules sync.Map // string -> []*TableRoutingRule (key: "scope:scopeID" -> rules, scopeID="" for global)

	// Last DB usages for budgets and rate limits
	LastDBUsagesBudgetsMu            sync.RWMutex       // Last DB usages for budgets
	LastDBUsagesRateLimitsRequestsMu sync.RWMutex       // Mutex for last DB usages for rate limits requests
	LastDBUsagesRateLimitsTokensMu   sync.RWMutex       // Mutex for last DB usages for rate limits tokens
	LastDBUsagesBudgets              map[string]float64 // Map for last DB usages for budgets
	LastDBUsagesRequestsRateLimits   map[string]int64   // Map for last DB usages for rate limits requests
	LastDBUsagesTokensRateLimits     map[string]int64   // Map for last DB usages for rate limits tokens

	// CEL caching layer for routing rules
	compiledRoutingPrograms sync.Map // string -> cel.Program (key: ruleID -> compiled CEL program)
	routingCELEnv           *cel.Env // Singleton CEL environment reused for all compilations

	// Config store for refresh operations
	configStore configstore.ConfigStore

	// Model catalog for cross-provider model matching (optional)
	modelCatalog *modelcatalog.ModelCatalog

	// Logger
	logger schemas.Logger
}

type GovernanceData struct {
	VirtualKeys  map[string]*configstoreTables.TableVirtualKey  `json:"virtual_keys"`
	Teams        map[string]*configstoreTables.TableTeam        `json:"teams"`
	Customers    map[string]*configstoreTables.TableCustomer    `json:"customers"`
	Users        map[string]*UserGovernance                     `json:"users"` // User-level governance (enterprise-only)
	Budgets      map[string]*configstoreTables.TableBudget      `json:"budgets"`
	RateLimits   map[string]*configstoreTables.TableRateLimit   `json:"rate_limits"`
	RoutingRules map[string]*configstoreTables.TableRoutingRule `json:"routing_rules"`
	ModelConfigs []*configstoreTables.TableModelConfig          `json:"model_configs"`
	Providers    []*configstoreTables.TableProvider             `json:"providers"`
}

// BusinessUnitGovernance holds in-memory budget and rate limit data for a business unit
type BusinessUnitGovernance struct {
	BudgetID    *string
	RateLimitID *string
}

// UserGovernance holds governance data for a user (enterprise-only)
type UserGovernance struct {
	BudgetID    *string `json:"budget_id,omitempty"`
	RateLimitID *string `json:"rate_limit_id,omitempty"`
}

// BudgetAndRateLimitStatus represents the current budget and rate limit usage state
// Exhaustion is determined by percent_used >= 100
type BudgetAndRateLimitStatus struct {
	BudgetPercentUsed           float64 `json:"budget_percent_used"`             // 0-100, >100 means exhausted
	RateLimitTokenPercentUsed   float64 `json:"rate_limit_token_percent_used"`   // 0-100, >100 means exhausted
	RateLimitRequestPercentUsed float64 `json:"rate_limit_request_percent_used"` // 0-100, >100 means exhausted
}

// GovernanceStore defines the interface for governance data access and policy evaluation.
//
// Error semantics contract:
//   - CheckRateLimit and CheckBudget return a non-nil error to indicate a governance/policy
//     violation (not an infrastructure/operational failure).
//   - Callers must treat any non-nil error from these methods as an explicit denial/violation
//     decision rather than a retryable infrastructure error.
//   - This contract ensures consistent behavior across implementations (e.g., in-memory,
//     DB-backed) and prevents retry loops on policy violations.
type GovernanceStore interface {
	GetGovernanceData(ctx context.Context) *GovernanceData
	GetVirtualKey(ctx context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool)
	// Budget crud
	LoadBudget(ctx context.Context, budgetID string) *configstoreTables.TableBudget
	StoreBudget(ctx context.Context, budgetID string, budget *configstoreTables.TableBudget)
	DeleteBudget(ctx context.Context, budgetID string)
	// Rate limit crud
	LoadRateLimit(ctx context.Context, rateLimitID string) *configstoreTables.TableRateLimit
	StoreRateLimit(ctx context.Context, rateLimitID string, rateLimit *configstoreTables.TableRateLimit)
	DeleteRateLimit(ctx context.Context, rateLimitID string)
	// Provider-level governance checks
	CheckProviderBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckProviderRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// Model-level governance checks
	CheckModelBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckModelRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// VK-level governance checks
	CheckVirtualKeyBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckVirtualKeyRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// In-memory usage updates (for VK-level)
	UpdateVirtualKeyBudgetUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, cost float64) error
	UpdateVirtualKeyRateLimitUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// In-memory reset checks (return items that need DB sync)
	ResetExpiredRateLimitsInMemory(ctx context.Context) []*configstoreTables.TableRateLimit
	ResetExpiredBudgetsInMemory(ctx context.Context) []*configstoreTables.TableBudget
	// DB sync for expired items
	ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) error
	ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error
	// Provider and model-level usage updates (combined)
	UpdateProviderAndModelBudgetUsageInMemory(ctx context.Context, model string, provider schemas.ModelProvider, cost float64) error
	UpdateProviderAndModelRateLimitUsageInMemory(ctx context.Context, model string, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// Dump operations
	DumpRateLimits(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) error
	DumpBudgets(ctx context.Context, baselines map[string]float64) error
	// In-memory CRUD operations
	CreateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey)
	UpdateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64)
	DeleteVirtualKeyInMemory(ctx context.Context, vkID string)
	CreateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam)
	UpdateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam, budgetBaselines map[string]float64)
	DeleteTeamInMemory(ctx context.Context, teamID string)
	// Customer information
	CreateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer)
	UpdateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer, budgetBaselines map[string]float64)
	DeleteCustomerInMemory(ctx context.Context, customerID string)
	// Team level CheckUserBudget
	CheckTeamBudget(ctx context.Context, teamID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckTeamRateLimit(ctx context.Context, teamID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// Customer-level governance checks
	CheckCustomerBudget(ctx context.Context, customerID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckCustomerRateLimit(ctx context.Context, customerID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// User governance in-memory operations (enterprise-only, but interface defined here for compatibility)
	GetUserGovernance(ctx context.Context, userID string) (*UserGovernance, bool)
	CreateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	UpdateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	DeleteUserGovernanceInMemory(ctx context.Context, userID string)
	// User-level governance checks (enterprise-only)
	CheckUserBudget(ctx context.Context, userID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckUserRateLimit(ctx context.Context, userID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	UpdateUserBudgetUsageInMemory(ctx context.Context, userID string, cost float64) error
	UpdateUserRateLimitUsageInMemory(ctx context.Context, userID string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// Model config in-memory operations
	UpdateModelConfigInMemory(ctx context.Context, mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig
	DeleteModelConfigInMemory(ctx context.Context, mcID string)
	// Provider in-memory operations
	UpdateProviderInMemory(ctx context.Context, provider *configstoreTables.TableProvider) *configstoreTables.TableProvider
	DeleteProviderInMemory(ctx context.Context, providerName string)
	// Routing Rules CEL caching
	GetRoutingProgram(ctx context.Context, rule *configstoreTables.TableRoutingRule) (cel.Program, error)
	// Budget and rate limit status queries for routing with baseline support
	GetBudgetAndRateLimitStatus(ctx context.Context, model string, provider schemas.ModelProvider, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus
	// Routing Rules CRUD
	HasRoutingRules(ctx context.Context) bool
	GetAllRoutingRules(ctx context.Context) []*configstoreTables.TableRoutingRule
	GetScopedRoutingRules(ctx context.Context, scope string, scopeID string) []*configstoreTables.TableRoutingRule
	UpdateRoutingRuleInMemory(ctx context.Context, rule *configstoreTables.TableRoutingRule) error
	DeleteRoutingRuleInMemory(ctx context.Context, id string) error
}

// NewLocalGovernanceStore creates a new in-memory governance store
// The modelCatalog parameter is optional (can be nil) and enables cross-provider model matching
// for governance lookups (e.g., "openai/gpt-4o" matching config for "gpt-4o").
func NewLocalGovernanceStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore, governanceConfig *configstore.GovernanceConfig, modelCatalog *modelcatalog.ModelCatalog) (*LocalGovernanceStore, error) {
	// Create singleton CEL environment once for all routing rule compilations
	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	store := &LocalGovernanceStore{
		configStore:                    configStore,
		logger:                         logger,
		routingCELEnv:                  env,
		modelCatalog:                   modelCatalog,
		LastDBUsagesBudgets:            make(map[string]float64),
		LastDBUsagesRequestsRateLimits: make(map[string]int64),
		LastDBUsagesTokensRateLimits:   make(map[string]int64),
	}

	if configStore != nil {
		// Load initial data from database
		if err := store.loadFromDatabase(ctx); err != nil {
			return nil, fmt.Errorf("failed to load initial data: %w", err)
		}
	} else {
		if err := store.loadFromConfigMemory(ctx, governanceConfig); err != nil {
			return nil, fmt.Errorf("failed to load governance data from config memory: %w", err)
		}
	}

	store.logger.Info("governance store initialized successfully")
	return store, nil
}

// LoadBudget loads a budget by its ID from the local store.
func (gs *LocalGovernanceStore) LoadBudget(ctx context.Context, budgetID string) *configstoreTables.TableBudget {
	if budget, ok := gs.budgets.Load(budgetID); ok {
		if b, ok := budget.(*configstoreTables.TableBudget); ok {
			return b
		}
	}
	return nil
}

// StoreBudget stores a budget in the local store.
func (gs *LocalGovernanceStore) StoreBudget(ctx context.Context, budgetID string, budget *configstoreTables.TableBudget) {
	gs.budgets.Store(budgetID, budget)
}

// DeleteBudget deletes a budget from the local store.
func (gs *LocalGovernanceStore) DeleteBudget(ctx context.Context, budgetID string) {
	gs.budgets.Delete(budgetID)
}

// LoadRateLimit loads a rate limit by its ID from the local store.
func (gs *LocalGovernanceStore) LoadRateLimit(ctx context.Context, rateLimitID string) *configstoreTables.TableRateLimit {
	if rateLimit, ok := gs.rateLimits.Load(rateLimitID); ok {
		if rl, ok := rateLimit.(*configstoreTables.TableRateLimit); ok {
			return rl
		}
	}
	return nil
}

// StoreRateLimit stores a rate limit in the local store.
func (gs *LocalGovernanceStore) StoreRateLimit(ctx context.Context, rateLimitID string, rateLimit *configstoreTables.TableRateLimit) {
	gs.rateLimits.Store(rateLimitID, rateLimit)
}

// DeleteRateLimit deletes a rate limit from the local store.
func (gs *LocalGovernanceStore) DeleteRateLimit(ctx context.Context, rateLimitID string) {
	gs.rateLimits.Delete(rateLimitID)
}

// GetGovernanceData returns a snapshot of the current governance data.
func (gs *LocalGovernanceStore) GetGovernanceData(ctx context.Context) *GovernanceData {
	refreshVKAssociations := func(vk *configstoreTables.TableVirtualKey) {
		if vk == nil {
			return
		}
		// Cross-reference live budget/rate limit from standalone maps
		// (usage updates clone into budgets/rateLimits maps, so embedded pointers go stale)
		// Hydrate multi-budgets from live sync.Map
		if len(vk.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(vk.Budgets))
			for _, b := range vk.Budgets {
				if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			vk.Budgets = liveBudgets
		}
		if vk.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*vk.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					vk.RateLimit = rl
				}
			}
		}
		if len(vk.ProviderConfigs) > 0 {
			configs := make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
			copy(configs, vk.ProviderConfigs)
			for i := range configs {
				// Hydrate provider config multi-budgets
				if len(configs[i].Budgets) > 0 {
					liveBudgets := make([]configstoreTables.TableBudget, 0, len(configs[i].Budgets))
					for _, b := range configs[i].Budgets {
						if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
							if budget, ok := lb.(*configstoreTables.TableBudget); ok {
								liveBudgets = append(liveBudgets, *budget)
							}
						}
					}
					configs[i].Budgets = liveBudgets
				}
				if configs[i].RateLimitID != nil {
					if liveRL, exists := gs.rateLimits.Load(*configs[i].RateLimitID); exists && liveRL != nil {
						if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
							configs[i].RateLimit = rl
						}
					}
				}
			}
			vk.ProviderConfigs = configs
		}
	}

	refreshTeamAssociations := func(team *configstoreTables.TableTeam) {
		if team == nil {
			return
		}
		// Allocate a fresh slice — shallow-copying `team` (via `clone := *team` at
		// the caller) reuses the backing array, so in-place writes would mutate
		// the live gs.teams entry under concurrent reads. Mirrors the VK pattern
		// above. Budgets missing from gs.budgets are dropped rather than kept stale.
		if len(team.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(team.Budgets))
			for _, b := range team.Budgets {
				if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			team.Budgets = liveBudgets
		}
		if team.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*team.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					team.RateLimit = rl
				}
			}
		}
	}
	virtualKeys := make(map[string]*configstoreTables.TableVirtualKey)
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		clone := *vk
		refreshVKAssociations(&clone)
		virtualKeys[key.(string)] = &clone
		return true // continue iteration
	})
	teams := make(map[string]*configstoreTables.TableTeam)
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		clone := *team
		refreshTeamAssociations(&clone)
		// Reset to 0 — will be recomputed from live VKs below to stay accurate
		// after creates/updates/deletes that don't trigger a full ReloadTeam.
		clone.VirtualKeyCount = 0
		teams[key.(string)] = &clone
		return true // continue iteration
	})
	customers := make(map[string]*configstoreTables.TableCustomer)
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		clone := *customer
		clone.Teams = make([]configstoreTables.TableTeam, 0)
		clone.VirtualKeys = make([]configstoreTables.TableVirtualKey, 0)
		if clone.BudgetID != nil {
			if liveBudget, exists := gs.budgets.Load(*clone.BudgetID); exists && liveBudget != nil {
				if b, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					clone.Budget = b
				}
			}
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		customers[key.(string)] = &clone
		return true // continue iteration
	})
	// virtualKeys level data
	for _, vk := range virtualKeys {
		if vk == nil {
			continue
		}
		if vk.TeamID != nil {
			if team, exists := teams[*vk.TeamID]; exists && team != nil {
				vk.Team = team
				team.VirtualKeyCount++
			}
		}
		if vk.CustomerID != nil {
			if customer, exists := customers[*vk.CustomerID]; exists && customer != nil {
				vk.Customer = customer

				nestedVK := *vk
				nestedVK.Customer = nil
				customer.VirtualKeys = append(customer.VirtualKeys, nestedVK)
			}
		}
	}
	// Team level data
	for _, team := range teams {
		if team == nil {
			continue
		}
		if team.CustomerID != nil {
			if customer, exists := customers[*team.CustomerID]; exists && customer != nil {
				team.Customer = customer

				nestedTeam := *team
				nestedTeam.Customer = nil
				customer.Teams = append(customer.Teams, nestedTeam)
			}
		}
	}
	// Customer level data
	for _, customer := range customers {
		if customer == nil {
			continue
		}
		sort.Slice(customer.Teams, func(i, j int) bool {
			if customer.Teams[i].CreatedAt.Equal(customer.Teams[j].CreatedAt) {
				return customer.Teams[i].ID < customer.Teams[j].ID
			}
			return customer.Teams[i].CreatedAt.Before(customer.Teams[j].CreatedAt)
		})
		sort.Slice(customer.VirtualKeys, func(i, j int) bool {
			if customer.VirtualKeys[i].CreatedAt.Equal(customer.VirtualKeys[j].CreatedAt) {
				return customer.VirtualKeys[i].ID < customer.VirtualKeys[j].ID
			}
			return customer.VirtualKeys[i].CreatedAt.Before(customer.VirtualKeys[j].CreatedAt)
		})
	}
	budgets := make(map[string]*configstoreTables.TableBudget)
	gs.budgets.Range(func(key, value interface{}) bool {
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return true // continue
		}
		budgets[key.(string)] = budget
		return true // continue iteration
	})
	rateLimits := make(map[string]*configstoreTables.TableRateLimit)
	gs.rateLimits.Range(func(key, value interface{}) bool {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true // continue
		}
		rateLimits[key.(string)] = rateLimit
		return true // continue iteration
	})
	routingRules := make(map[string]*configstoreTables.TableRoutingRule)
	gs.routingRules.Range(func(key, value any) bool {
		rules, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok || rules == nil {
			return true // continue
		}
		// Flatten the rules array (stored as []*TableRoutingRule by scope:scopeID)
		for _, rule := range rules {
			if rule != nil {
				routingRules[rule.ID] = rule
			}
		}
		return true // continue iteration
	})
	var modelConfigsList []*configstoreTables.TableModelConfig
	gs.modelConfigs.Range(func(key, value any) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true // continue
		}
		// Cross-reference live budget/rate limit from standalone maps
		// (usage updates clone into budgets/rateLimits maps, so embedded pointers go stale)
		clone := *mc
		if clone.BudgetID != nil {
			if liveBudget, exists := gs.budgets.Load(*clone.BudgetID); exists && liveBudget != nil {
				if b, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					clone.Budget = b
				}
			}
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		modelConfigsList = append(modelConfigsList, &clone)
		return true // continue iteration
	})
	var providersList []*configstoreTables.TableProvider
	gs.providers.Range(func(key, value interface{}) bool {
		p, ok := value.(*configstoreTables.TableProvider)
		if !ok || p == nil {
			return true // continue
		}
		// Cross-reference live budget/rate limit from standalone maps
		clone := *p
		if clone.BudgetID != nil {
			if liveBudget, exists := gs.budgets.Load(*clone.BudgetID); exists && liveBudget != nil {
				if b, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					clone.Budget = b
				}
			}
		}
		if clone.RateLimitID != nil {
			if liveRL, exists := gs.rateLimits.Load(*clone.RateLimitID); exists && liveRL != nil {
				if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
					clone.RateLimit = rl
				}
			}
		}
		providersList = append(providersList, &clone)
		return true // continue iteration
	})
	// Sort slice fields by CreatedAt so responses are sent in consistent order
	sort.Slice(modelConfigsList, func(i, j int) bool {
		return modelConfigsList[i].CreatedAt.Before(modelConfigsList[j].CreatedAt)
	})
	sort.Slice(providersList, func(i, j int) bool {
		return providersList[i].CreatedAt.Before(providersList[j].CreatedAt)
	})
	return &GovernanceData{
		VirtualKeys:  virtualKeys,
		Teams:        teams,
		Customers:    customers,
		Budgets:      budgets,
		RateLimits:   rateLimits,
		RoutingRules: routingRules,
		ModelConfigs: modelConfigsList,
		Providers:    providersList,
	}
}

// GetVirtualKey retrieves a virtual key by its value (lock-free) with all relationships preloaded
func (gs *LocalGovernanceStore) GetVirtualKey(ctx context.Context, vkValue string) (*configstoreTables.TableVirtualKey, bool) {
	value, exists := gs.virtualKeys.Load(vkValue)
	if !exists || value == nil {
		return nil, false
	}
	vk, ok := value.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		return nil, false
	}
	return vk, true
}

// CheckRateLimit checks rate limits for tokens and requests across categories
func (gs *LocalGovernanceStore) CheckRateLimit(ctx context.Context, entityWiseRateLimits EntityWiseRateLimits, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	for entity, rateLimits := range entityWiseRateLimits {
		for _, rateLimit := range rateLimits {
			var violations []string
			// Check if rate limit needs reset (in-memory check)
			// Track which limits are expired so we can skip only those specific checks
			tokenLimitExpired := false
			if rateLimit.TokenResetDuration != nil {
				if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
					if time.Since(rateLimit.TokenLastReset) >= duration {
						// Token rate limit expired but hasn't been reset yet - skip token check only
						tokenLimitExpired = true
					}
				}
			}
			requestLimitExpired := false
			if rateLimit.RequestResetDuration != nil {
				if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
					if time.Since(rateLimit.RequestLastReset) >= duration {
						// Request rate limit expired but hasn't been reset yet - skip request check only
						requestLimitExpired = true
					}
				}
			}

			tokensBaseline, exists := tokensBaselines[rateLimit.ID]
			if !exists {
				tokensBaseline = 0
			}
			requestsBaseline, exists := requestsBaselines[rateLimit.ID]
			if !exists {
				requestsBaseline = 0
			}

			// Token limits - check if total usage (local + remote baseline) exceeds limit
			// Skip this check if token limit has expired
			if !tokenLimitExpired && rateLimit.TokenMaxLimit != nil && rateLimit.TokenCurrentUsage+tokensBaseline >= *rateLimit.TokenMaxLimit {
				duration := "unknown"
				if rateLimit.TokenResetDuration != nil {
					duration = *rateLimit.TokenResetDuration
				}
				violations = append(violations, fmt.Sprintf("token limit exceeded (%d/%d, resets every %s)",
					rateLimit.TokenCurrentUsage+tokensBaseline, *rateLimit.TokenMaxLimit, duration))
			}

			// Request limits - check if total usage (local + remote baseline) exceeds limit
			// Skip this check if request limit has expired
			if !requestLimitExpired && rateLimit.RequestMaxLimit != nil && rateLimit.RequestCurrentUsage+requestsBaseline >= *rateLimit.RequestMaxLimit {
				duration := "unknown"
				if rateLimit.RequestResetDuration != nil {
					duration = *rateLimit.RequestResetDuration
				}
				violations = append(violations, fmt.Sprintf("request limit exceeded (%d/%d, resets every %s)",
					rateLimit.RequestCurrentUsage+requestsBaseline, *rateLimit.RequestMaxLimit, duration))
			}

			if len(violations) > 0 {
				// Determine specific violation type
				decision := DecisionRateLimited // Default to general rate limited decision
				if len(violations) == 1 {
					if strings.Contains(violations[0], "token") {
						decision = DecisionTokenLimited // More specific violation type
					} else if strings.Contains(violations[0], "request") {
						decision = DecisionRequestLimited // More specific violation type
					}
				}
				return decision, fmt.Errorf("rate limit violated for %s: %s", entity, violations)
			}
		}
	}
	return DecisionAllow, nil
}

// Generic check budget method
// The idea is to keep this as a common method for checking all budgets. The entire business logic resides in here
func (gs *LocalGovernanceStore) CheckBudget(ctx context.Context, entityWiseBudgets EntityWiseBudgets, baselines map[string]float64) (Decision, error) {
	// Check each budget in hierarchy order using in-memory data
	for entity, budgets := range entityWiseBudgets {
		for _, budget := range budgets { // Check if budget needs reset (in-memory check)
			if budget.ResetDuration != "" {
				if duration, err := configstoreTables.ParseDuration(budget.ResetDuration); err == nil {
					if time.Since(budget.LastReset) >= duration {
						// Budget expired but hasn't been reset yet - treat as reset
						// Note: actual reset will happen in post-hook via AtomicBudgetUpdate
						gs.logger.Debug("LocalStore CheckBudget: Budget %s (%s) expired, skipping check", budget.ID, entity)
						continue // Skip budget check for expired budgets
					}
				}
			}
			baseline, exists := baselines[budget.ID]
			if !exists {
				baseline = 0
			}
			gs.logger.Debug("LocalStore CheckBudget: Checking %s budget %s: local=%.4f, remote=%.4f, total=%.4f, limit=%.4f",
				entity, budget.ID, budget.CurrentUsage, baseline, budget.CurrentUsage+baseline, budget.MaxLimit)
			// Check if current usage (local + remote baseline) exceeds budget limit
			if budget.CurrentUsage+baseline >= budget.MaxLimit {
				gs.logger.Debug("LocalStore CheckBudget: Budget %s EXCEEDED", budget.ID)
				return DecisionBudgetExceeded, fmt.Errorf("%s budget exceeded: %.4f >= %.4f dollars",
					entity, budget.CurrentUsage+baseline, budget.MaxLimit)
			}
		}
	}
	return DecisionAllow, nil
}

// CheckVirtualKeyBudget performs virtual key level budget checking using in-memory store data (lock-free for high performance)
func (gs *LocalGovernanceStore) CheckVirtualKeyBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	if vk == nil {
		return DecisionVirtualKeyNotFound, fmt.Errorf("virtual key cannot be nil")
	}
	// This is to prevent nil pointer dereference
	if baselines == nil {
		baselines = map[string]float64{}
	}
	// Extract provider from request
	var provider schemas.ModelProvider
	if request != nil {
		provider = request.Provider
	}
	// Use helper to collect budgets and their names (lock-free)
	budgetsWithCategories := gs.collectBudgetsFromHierarchy(ctx, vk, provider)
	gs.logger.Debug("LocalStore CheckBudget: Received %d baselines from remote nodes", len(baselines))
	for budgetID, baseline := range baselines {
		gs.logger.Debug("  - Baseline for budget %s: %.4f", budgetID, baseline)
	}
	return gs.CheckBudget(ctx, budgetsWithCategories, baselines)
}

// CheckProviderBudget performs budget checking for provider-level configs (lock-free for high performance)
func (gs *LocalGovernanceStore) CheckProviderBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	// This is to prevent nil pointer dereference
	if baselines == nil {
		baselines = map[string]float64{}
	}
	// Extract provider from request
	var provider schemas.ModelProvider
	if request != nil {
		provider = request.Provider
	}
	// Get provider config
	providerKey := string(provider)
	value, exists := gs.providers.Load(providerKey)
	if !exists || value == nil {
		// No provider config found, allow request
		return DecisionAllow, nil
	}
	providerTable, ok := value.(*configstoreTables.TableProvider)
	if !ok || providerTable == nil || providerTable.BudgetID == nil {
		// No budget configured for provider, allow request
		return DecisionAllow, nil
	}
	// Read from budgets map to get the latest updated budget (same source as UpdateProviderBudgetUsage)
	budget := gs.LoadBudget(ctx, *providerTable.BudgetID)
	if budget == nil {
		return DecisionAllow, nil
	}
	return gs.CheckBudget(ctx, map[string][]*configstoreTables.TableBudget{providerKey: {budget}}, baselines)
}

// CheckProviderRateLimit checks provider-level rate limits and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckProviderRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	// Extract provider from request
	var provider schemas.ModelProvider
	if request != nil {
		provider = request.Provider
	}
	// Get provider config
	providerKey := string(provider)
	value, exists := gs.providers.Load(providerKey)
	if !exists || value == nil {
		// No provider config found, allow request
		return DecisionAllow, nil
	}
	providerTable, ok := value.(*configstoreTables.TableProvider)
	if !ok || providerTable == nil || providerTable.RateLimitID == nil {
		// No rate limit configured for provider, allow request
		return DecisionAllow, nil
	}
	// Read from rateLimits map to get the latest updated rate limit (same source as UpdateProviderRateLimitUsage)
	rateLimit := gs.LoadRateLimit(ctx, *providerTable.RateLimitID)
	if rateLimit == nil {
		return DecisionAllow, nil
	}
	return gs.CheckRateLimit(ctx, EntityWiseRateLimits{providerKey: []*configstoreTables.TableRateLimit{rateLimit}}, tokensBaselines, requestsBaselines)
}

// findModelOnlyConfig looks up a model-only config (no provider) with cross-provider model name normalization.
// Returns the matching config and the display name for error messages.
func (gs *LocalGovernanceStore) findModelOnlyConfig(ctx context.Context, model string) (*configstoreTables.TableModelConfig, string) {
	// If modelMatcher is available, try normalized base model name first (cross-provider matching)
	if gs.modelCatalog != nil {
		baseName := gs.modelCatalog.GetBaseModelName(model)
		if baseName != model {
			if value, exists := gs.modelConfigs.Load(baseName); exists && value != nil {
				if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil {
					return mc, baseName
				}
			}
		}
	}
	// Always try direct lookup by original model name as fallback
	if value, exists := gs.modelConfigs.Load(model); exists && value != nil {
		if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil {
			return mc, model
		}
	}
	return nil, ""
}

// CheckModelBudget performs budget checking for model-level configs (lock-free for high performance)
func (gs *LocalGovernanceStore) CheckModelBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	// This is to prevent nil pointer dereference
	if baselines == nil {
		baselines = map[string]float64{}
	}
	// Extract model and provider from request
	var model string
	var provider *schemas.ModelProvider
	if request != nil {
		model = request.Model
		if request.Provider != "" {
			provider = &request.Provider
		}
	}
	// Collect model configs to check: model+provider (if exists) AND model-only (if exists)
	entityWiseBudgets := EntityWiseBudgets{}
	// Check model+provider config first (more specific) - if provider is provided
	if provider != nil {
		key := fmt.Sprintf("%s:%s", model, string(*provider))
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil && mc.Budget != nil {
				budget := gs.LoadBudget(ctx, *mc.BudgetID)
				if budget != nil {
					key := fmt.Sprintf("Model:%s:Provider:%s", mc.ModelName, *provider)
					entityWiseBudgets[key] = []*configstoreTables.TableBudget{budget}
				}
			}
		}
	}
	// Always check model-only config (if exists) - regardless of whether model+provider config exists
	// Uses findModelOnlyConfig for cross-provider model name normalization
	if mc, _ := gs.findModelOnlyConfig(ctx, model); mc != nil && mc.Budget != nil {
		budget := gs.LoadBudget(ctx, *mc.BudgetID)
		if budget != nil {
			key := fmt.Sprintf("Model:%s", mc.ModelName)
			entityWiseBudgets[key] = []*configstoreTables.TableBudget{budget}
		}
	}
	return gs.CheckBudget(ctx, entityWiseBudgets, baselines)
}

// CheckTeamBudget checks team-level budget and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckTeamBudget(ctx context.Context, teamID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	if teamID == "" {
		return DecisionAllow, nil
	}
	if baselines == nil {
		baselines = map[string]float64{}
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return DecisionAllow, nil
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || len(team.Budgets) == 0 {
		return DecisionAllow, nil
	}
	list := make([]*configstoreTables.TableBudget, 0, len(team.Budgets))
	for _, b := range team.Budgets {
		if hot := gs.LoadBudget(ctx, b.ID); hot != nil {
			list = append(list, hot)
		}
	}
	if len(list) == 0 {
		return DecisionAllow, nil
	}
	key := fmt.Sprintf("Team:%s", teamID)
	return gs.CheckBudget(ctx, EntityWiseBudgets{key: list}, baselines)
}

// CheckTeamRateLimit checks team-level rate limit and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckTeamRateLimit(ctx context.Context, teamID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return DecisionAllow, nil
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team.RateLimitID == nil {
		return DecisionAllow, nil
	}
	teamRateLimit := gs.LoadRateLimit(ctx, *team.RateLimitID)
	if teamRateLimit == nil {
		return DecisionAllow, nil
	}
	key := fmt.Sprintf("Team:%s", teamID)
	entityWiseRateLimits := EntityWiseRateLimits{key: {teamRateLimit}}
	return gs.CheckRateLimit(ctx, entityWiseRateLimits, tokensBaselines, requestsBaselines)
}

// CheckCustomerBudget checks customer-level budget and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckCustomerBudget(ctx context.Context, customerID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	if customerID == "" {
		return DecisionAllow, nil
	}
	if baselines == nil {
		baselines = map[string]float64{}
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return DecisionAllow, nil
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer.BudgetID == nil {
		return DecisionAllow, nil
	}
	customerBudget := gs.LoadBudget(ctx, *customer.BudgetID)
	if customerBudget == nil {
		return DecisionAllow, nil
	}
	key := fmt.Sprintf("Customer:%s", customerID)
	entityWiseBudgets := EntityWiseBudgets{key: {customerBudget}}
	return gs.CheckBudget(ctx, entityWiseBudgets, baselines)
}

// CheckCustomerRateLimit checks customer-level rate limit and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckCustomerRateLimit(ctx context.Context, customerID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	if customerID == "" {
		return DecisionAllow, nil
	}
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return DecisionAllow, nil
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer.RateLimitID == nil {
		return DecisionAllow, nil
	}
	customerRateLimit := gs.LoadRateLimit(ctx, *customer.RateLimitID)
	if customerRateLimit == nil {
		return DecisionAllow, nil
	}
	key := fmt.Sprintf("Customer:%s", customerID)
	entityWiseRateLimits := EntityWiseRateLimits{key: {customerRateLimit}}
	return gs.CheckRateLimit(ctx, entityWiseRateLimits, tokensBaselines, requestsBaselines)
}

// CheckUserBudget checks if user's budget allows the request (enterprise-only)
// Community build: silent no-op so user-governance absence never silently denies requests.
func (gs *LocalGovernanceStore) CheckUserBudget(ctx context.Context, userID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	return DecisionAllow, nil
}

// CheckModelRateLimit checks model-level rate limits and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckModelRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	// This is to prevent nil pointer dereference
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}
	// Extract model and provider from request
	var model string
	var provider *schemas.ModelProvider
	if request != nil {
		model = request.Model
		if request.Provider != "" {
			provider = &request.Provider
		}
	}
	// Collect model configs to check: model+provider (if exists) AND model-only (if exists)
	entityWiseRateLimits := make(EntityWiseRateLimits)
	// Check model+provider config first (more specific) - if provider is provided
	if provider != nil {
		key := fmt.Sprintf("%s:%s", model, string(*provider))
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil && mc.RateLimitID != nil {
				rateLimit := gs.LoadRateLimit(ctx, *mc.RateLimitID)
				if rateLimit != nil {
					entityWiseRateLimits[fmt.Sprintf("Model:%s:Provider:%s", model, string(*provider))] = []*configstoreTables.TableRateLimit{rateLimit}
				}
			}
		}
	}
	// Always check model-only config (if exists) - regardless of whether model+provider config exists
	// Uses findModelOnlyConfig for cross-provider model name normalization
	if mc, configKey := gs.findModelOnlyConfig(ctx, model); mc != nil && mc.RateLimitID != nil {
		rateLimit := gs.LoadRateLimit(ctx, *mc.RateLimitID)
		if rateLimit != nil {
			entityWiseRateLimits[fmt.Sprintf("Model:%s", configKey)] = []*configstoreTables.TableRateLimit{rateLimit}
		}
	}
	return gs.CheckRateLimit(ctx, entityWiseRateLimits, tokensBaselines, requestsBaselines)
}

// CheckUserRateLimit checks if user's rate limit allows the request (enterprise-only)
// Community build: silent no-op so user-governance absence never silently denies requests.
func (gs *LocalGovernanceStore) CheckUserRateLimit(ctx context.Context, userID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	return DecisionAllow, nil
}

// CheckVirtualKeyRateLimit checks a virtual key  rate limit and returns evaluation result if violated (true if violated, false if not)
func (gs *LocalGovernanceStore) CheckVirtualKeyRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	// Extract provider from request
	var provider schemas.ModelProvider
	if request != nil {
		provider = request.Provider
	}
	// Collect rate limits and their names from the hierarchy
	entityWiseRateLimits := gs.collectRateLimitsFromHierarchy(ctx, vk, provider)
	// This is to prevent nil pointer dereference
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}
	return gs.CheckRateLimit(ctx, entityWiseRateLimits, tokensBaselines, requestsBaselines)
}

// UpdateVirtualKeyBudgetUsageInMemory performs atomic budget updates across the hierarchy (both in memory and in database)
func (gs *LocalGovernanceStore) UpdateVirtualKeyBudgetUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, cost float64) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}

	// Collect budget IDs using fast in-memory lookup instead of DB queries
	budgetIDs := gs.collectBudgetIDsFromMemory(ctx, vk, provider)
	now := time.Now()
	for _, budgetID := range budgetIDs {
		// Update in-memory cache for next read (lock-free)
		if cachedBudgetValue, exists := gs.budgets.Load(budgetID); exists && cachedBudgetValue != nil {
			if cachedBudget, ok := cachedBudgetValue.(*configstoreTables.TableBudget); ok && cachedBudget != nil {
				// Clone FIRST to avoid race conditions
				clone := *cachedBudget
				oldUsage := clone.CurrentUsage

				// Check if budget needs reset (in-memory check) - operate on clone
				if clone.ResetDuration != "" {
					if duration, err := configstoreTables.ParseDuration(clone.ResetDuration); err == nil {
						if now.Sub(clone.LastReset) >= duration {
							clone.CurrentUsage = 0
							clone.LastReset = now
							gs.logger.Debug("UpdateVirtualKeyBudgetUsageInMemory: Budget %s was reset (expired, duration: %v)", budgetID, duration)
						}
					}
				}

				// Update the clone
				clone.CurrentUsage += cost
				gs.budgets.Store(budgetID, &clone)
				gs.logger.Debug("UpdateVirtualKeyBudgetUsageInMemory: Updated budget %s: %.4f -> %.4f (added %.4f)",
					budgetID, oldUsage, clone.CurrentUsage, cost)
			}
		} else {
			gs.logger.Warn("UpdateVirtualKeyBudgetUsageInMemory: Budget %s not found in local store", budgetID)
		}
	}
	return nil
}

// UpdateProviderAndModelBudgetUsageInMemory performs atomic budget updates for both provider-level and model-level configs (in memory)
func (gs *LocalGovernanceStore) UpdateProviderAndModelBudgetUsageInMemory(ctx context.Context, model string, provider schemas.ModelProvider, cost float64) error {
	now := time.Now()

	// Helper function to update a budget by ID
	updateBudget := func(budgetID string) {
		if cachedBudgetValue, exists := gs.budgets.Load(budgetID); exists && cachedBudgetValue != nil {
			if cachedBudget, ok := cachedBudgetValue.(*configstoreTables.TableBudget); ok && cachedBudget != nil {
				// Clone FIRST to avoid race conditions
				clone := *cachedBudget
				// Check if budget needs reset (in-memory check) - operate on clone
				if clone.ResetDuration != "" {
					if duration, err := configstoreTables.ParseDuration(clone.ResetDuration); err == nil {
						if now.Sub(clone.LastReset) >= duration {
							clone.CurrentUsage = 0
							clone.LastReset = now
						}
					}
				}
				// Update the clone
				clone.CurrentUsage += cost
				gs.budgets.Store(budgetID, &clone)
			}
		}
	}

	// 1. Update provider-level budget (if provider is set)
	if provider != "" {
		providerKey := string(provider)
		if value, exists := gs.providers.Load(providerKey); exists && value != nil {
			if providerTable, ok := value.(*configstoreTables.TableProvider); ok && providerTable != nil && providerTable.BudgetID != nil {
				updateBudget(*providerTable.BudgetID)
			}
		}
	}

	// 2. Update model-level budgets
	// Check model+provider config first (more specific) - if provider is provided
	if provider != "" {
		key := fmt.Sprintf("%s:%s", model, string(provider))
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil && mc.BudgetID != nil {
				updateBudget(*mc.BudgetID)
			}
		}
	}

	// Always check model-only config (if exists) - regardless of whether model+provider config exists
	// Uses findModelOnlyConfig for cross-provider model name normalization
	if mc, _ := gs.findModelOnlyConfig(ctx, model); mc != nil && mc.BudgetID != nil {
		updateBudget(*mc.BudgetID)
	}

	return nil
}

// UpdateUserBudgetUsageInMemory updates user's budget usage in memory (enterprise-only)
// Community build: silent no-op to avoid per-request error spam when a userID is set.
func (gs *LocalGovernanceStore) UpdateUserBudgetUsageInMemory(ctx context.Context, userID string, cost float64) error {
	return nil
}

// UpdateProviderAndModelRateLimitUsageInMemory updates rate limit counters for both provider-level and model-level rate limits (lock-free)
func (gs *LocalGovernanceStore) UpdateProviderAndModelRateLimitUsageInMemory(ctx context.Context, model string, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	now := time.Now()

	// Helper function to update a rate limit by ID
	updateRateLimit := func(rateLimitID string) {
		if cachedRateLimitValue, exists := gs.rateLimits.Load(rateLimitID); exists && cachedRateLimitValue != nil {
			if cachedRateLimit, ok := cachedRateLimitValue.(*configstoreTables.TableRateLimit); ok && cachedRateLimit != nil {
				// Clone FIRST to avoid race conditions
				clone := *cachedRateLimit
				// Check if rate limit needs reset (in-memory check) - operate on clone
				if clone.TokenResetDuration != nil {
					if duration, err := configstoreTables.ParseDuration(*clone.TokenResetDuration); err == nil {
						if now.Sub(clone.TokenLastReset) >= duration {
							clone.TokenCurrentUsage = 0
							clone.TokenLastReset = now
						}
					}
				}
				if clone.RequestResetDuration != nil {
					if duration, err := configstoreTables.ParseDuration(*clone.RequestResetDuration); err == nil {
						if now.Sub(clone.RequestLastReset) >= duration {
							clone.RequestCurrentUsage = 0
							clone.RequestLastReset = now
						}
					}
				}
				// Update the clone
				if shouldUpdateTokens {
					clone.TokenCurrentUsage += tokensUsed
				}
				if shouldUpdateRequests {
					clone.RequestCurrentUsage += 1
				}
				gs.rateLimits.Store(rateLimitID, &clone)
			}
		}
	}

	// 1. Update provider-level rate limit (if provider is set)
	if provider != "" {
		providerKey := string(provider)
		if value, exists := gs.providers.Load(providerKey); exists && value != nil {
			if providerTable, ok := value.(*configstoreTables.TableProvider); ok && providerTable != nil && providerTable.RateLimitID != nil {
				updateRateLimit(*providerTable.RateLimitID)
			}
		}
	}

	// 2. Update model-level rate limits
	// Check model+provider config first (more specific) - if provider is provided
	if provider != "" {
		key := fmt.Sprintf("%s:%s", model, string(provider))
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil && mc.RateLimitID != nil {
				updateRateLimit(*mc.RateLimitID)
			}
		}
	}

	// Always check model-only config (if exists) - regardless of whether model+provider config exists
	// Uses findModelOnlyConfig for cross-provider model name normalization
	if mc, _ := gs.findModelOnlyConfig(ctx, model); mc != nil && mc.RateLimitID != nil {
		updateRateLimit(*mc.RateLimitID)
	}

	return nil
}

// UpdateVirtualKeyRateLimitUsageInMemory updates rate limit counters for VK-level rate limits (lock-free)
func (gs *LocalGovernanceStore) UpdateVirtualKeyRateLimitUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}
	// Collect rate limit IDs using fast in-memory lookup instead of DB queries
	rateLimitIDs := gs.collectRateLimitIDsFromMemory(ctx, vk, provider)
	now := time.Now()
	for _, rateLimitID := range rateLimitIDs {
		// Update in-memory cache for next read (lock-free)
		if cachedRateLimitValue, exists := gs.rateLimits.Load(rateLimitID); exists && cachedRateLimitValue != nil {
			if cachedRateLimit, ok := cachedRateLimitValue.(*configstoreTables.TableRateLimit); ok && cachedRateLimit != nil {
				// Clone FIRST to avoid race conditions
				clone := *cachedRateLimit

				// Check if rate limit needs reset (in-memory check) - operate on clone
				if clone.TokenResetDuration != nil {
					if duration, err := configstoreTables.ParseDuration(*clone.TokenResetDuration); err == nil {
						if now.Sub(clone.TokenLastReset) >= duration {
							clone.TokenCurrentUsage = 0
							clone.TokenLastReset = now
							gs.logger.Debug("UpdateRateLimitUsage: Rate limit %s was reset (expired, duration: %v)", rateLimitID, duration)
						}
					}
				}
				if clone.RequestResetDuration != nil {
					if duration, err := configstoreTables.ParseDuration(*clone.RequestResetDuration); err == nil {
						if now.Sub(clone.RequestLastReset) >= duration {
							clone.RequestCurrentUsage = 0
							clone.RequestLastReset = now
							gs.logger.Debug("UpdateRateLimitUsage: Rate limit %s was reset (expired, duration: %v)", rateLimitID, duration)
						}
					}
				}
				// Update the clone
				if shouldUpdateTokens {
					clone.TokenCurrentUsage += tokensUsed
				}
				if shouldUpdateRequests {
					clone.RequestCurrentUsage += 1
				}
				gs.rateLimits.Store(rateLimitID, &clone)
			}
		}
	}
	return nil
}

// UpdateUserRateLimitUsageInMemory updates user's rate limit usage in memory (enterprise-only)
// Community build: silent no-op to avoid per-request error spam when a userID is set.
func (gs *LocalGovernanceStore) UpdateUserRateLimitUsageInMemory(ctx context.Context, userID string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	return nil
}

// ResetExpiredBudgetsInMemory checks and resets budgets that have exceeded their reset duration (lock-free)
func (gs *LocalGovernanceStore) ResetExpiredBudgetsInMemory(ctx context.Context) []*configstoreTables.TableBudget {
	now := time.Now()
	var resetBudgets []*configstoreTables.TableBudget
	// We reset all budgets
	gs.budgets.Range(func(key, value any) bool {
		// Type-safe conversion
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return true // continue
		}
		// Determine whether the budget needs resetting
		var shouldReset bool
		var newLastReset time.Time
		// Any budget and rate limit can be calendar aligned
		if budget.CalendarAligned {
			// Calendar-aligned: reset when we've entered a genuinely new calendar period.
			currentPeriodStart := configstoreTables.GetCalendarPeriodStart(budget.ResetDuration, now)
			if currentPeriodStart.After(budget.LastReset) {
				shouldReset = true
				newLastReset = currentPeriodStart
			}
		} else {
			// Rolling duration: reset after the configured duration has elapsed
			duration, err := configstoreTables.ParseDuration(budget.ResetDuration)
			if err != nil {
				gs.logger.Error("invalid budget reset duration %s: %v", budget.ResetDuration, err)
				return true // continue
			}
			if now.Sub(budget.LastReset) >= duration {
				shouldReset = true
				newLastReset = now
			}
		}
		if shouldReset {
			// Create a copy to avoid data race (sync.Map is concurrent-safe for reads/writes but not mutations)
			copiedBudget := *budget
			oldUsage := copiedBudget.CurrentUsage
			copiedBudget.CurrentUsage = 0
			copiedBudget.LastReset = newLastReset
			gs.LastDBUsagesBudgetsMu.Lock()
			gs.LastDBUsagesBudgets[copiedBudget.ID] = 0
			gs.LastDBUsagesBudgetsMu.Unlock()
			// Atomically replace the entry using the original key
			gs.budgets.Store(key, &copiedBudget)
			resetBudgets = append(resetBudgets, &copiedBudget)
			// Update all VKs, teams, customers, and provider configs that reference this budget
			gs.updateBudgetReferences(ctx, &copiedBudget)
			gs.logger.Debug(fmt.Sprintf("Reset budget %s (was %.2f, reset to 0)",
				copiedBudget.ID, oldUsage))
		}
		return true // continue
	})
	return resetBudgets
}

// ResetExpiredRateLimitsInMemory performs background reset of expired rate limits for both provider-level and VK-level (lock-free)
func (gs *LocalGovernanceStore) ResetExpiredRateLimitsInMemory(ctx context.Context) []*configstoreTables.TableRateLimit {
	now := time.Now()
	var resetRateLimits []*configstoreTables.TableRateLimit
	gs.rateLimits.Range(func(key, value any) bool {
		// Type-safe conversion
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true // continue
		}
		tokenNeedsReset := false
		requestNeedsReset := false
		// Any budget and rate limit can be calendar aligned
		if rateLimit.CalendarAligned {
			// Calendar-aligned: reset when we've entered a genuinely new calendar period.
			if rateLimit.TokenResetDuration != nil {
				currentPeriodStart := configstoreTables.GetCalendarPeriodStart(*rateLimit.TokenResetDuration, now)
				if currentPeriodStart.After(rateLimit.TokenLastReset) {
					tokenNeedsReset = true
				}
			}
			if rateLimit.RequestResetDuration != nil {
				currentPeriodStart := configstoreTables.GetCalendarPeriodStart(*rateLimit.RequestResetDuration, now)
				if currentPeriodStart.After(rateLimit.RequestLastReset) {
					requestNeedsReset = true
				}
			}
		} else {
			// Rolling duration: reset after the configured duration has elapsed
			if rateLimit.TokenResetDuration != nil {
				duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration)
				if err != nil {
					gs.logger.Error("invalid budget reset duration %s: %v", *rateLimit.TokenResetDuration, err)
					return true // continue
				}
				if now.Sub(rateLimit.TokenLastReset) >= duration {
					tokenNeedsReset = true
				}
			}
			if rateLimit.RequestResetDuration != nil {
				duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration)
				if err != nil {
					gs.logger.Error("invalid budget reset duration %s: %v", *rateLimit.RequestResetDuration, err)
					return true // continue
				}
				if now.Sub(rateLimit.RequestLastReset) >= duration {
					requestNeedsReset = true
				}
			}
		}
		// Create a copy to avoid data race (sync.Map is concurrent-safe for reads/writes but not mutations)
		copiedRateLimit := *rateLimit
		// Reset token limits if expired
		if tokenNeedsReset && copiedRateLimit.TokenResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*copiedRateLimit.TokenResetDuration); err == nil {
				if now.Sub(copiedRateLimit.TokenLastReset) >= duration {
					copiedRateLimit.TokenCurrentUsage = 0
					copiedRateLimit.TokenLastReset = now
					gs.LastDBUsagesRateLimitsTokensMu.Lock()
					gs.LastDBUsagesTokensRateLimits[copiedRateLimit.ID] = 0
					gs.LastDBUsagesRateLimitsTokensMu.Unlock()
				}
			}
		}
		// Reset request limits if expired
		if requestNeedsReset && copiedRateLimit.RequestResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*copiedRateLimit.RequestResetDuration); err == nil {
				if now.Sub(copiedRateLimit.RequestLastReset) >= duration {
					copiedRateLimit.RequestCurrentUsage = 0
					copiedRateLimit.RequestLastReset = now
					gs.LastDBUsagesRateLimitsRequestsMu.Lock()
					gs.LastDBUsagesRequestsRateLimits[copiedRateLimit.ID] = 0
					gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
				}
			}
		}
		// Only commit the copy + emit for DB reset + rescan references when something
		// actually expired. Without this guard the 10-second tick would always call
		// gs.updateRateLimitReferences (which scans every VK + provider-config) and
		// return every rate limit to the caller for a redundant DB update. Mirrors
		// the `if shouldReset { ... }` guard in ResetExpiredBudgetsInMemory above.
		if tokenNeedsReset || requestNeedsReset {
			// Atomically replace the entry using the original key
			gs.rateLimits.Store(key, &copiedRateLimit)
			resetRateLimits = append(resetRateLimits, &copiedRateLimit)
			// Update all VKs and provider configs that reference this rate limit
			gs.updateRateLimitReferences(ctx, &copiedRateLimit)
		}
		return true // continue
	})
	return resetRateLimits
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration in database
func (gs *LocalGovernanceStore) ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error {
	// Persist to database if any resets occurred using direct UPDATE to avoid overwriting config fields
	if len(resetBudgets) > 0 && gs.configStore != nil {
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			for _, budget := range resetBudgets {
				// Direct UPDATE only resets current_usage and last_reset
				// This prevents overwriting max_limit or reset_duration that may have been changed by other nodes/requests
				result := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableBudget{}).
					Where("id = ?", budget.ID).
					Updates(map[string]interface{}{
						"current_usage": budget.CurrentUsage,
						"last_reset":    budget.LastReset,
					})

				if result.Error != nil {
					return fmt.Errorf("failed to reset budget %s: %w", budget.ID, result.Error)
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("failed to persist budget resets to database: %w", err)
		}
	}

	return nil
}

// ResetExpiredRateLimits performs background reset of expired rate limits for both provider-level and VK-level in database
func (gs *LocalGovernanceStore) ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) error {
	if len(resetRateLimits) > 0 && gs.configStore != nil {
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			for _, rateLimit := range resetRateLimits {
				// Build update map with only the fields that were reset
				updates := make(map[string]interface{})

				// Check which fields were reset by comparing with current values
				if rateLimit.TokenCurrentUsage == 0 && rateLimit.TokenResetDuration != nil {
					updates["token_current_usage"] = 0
					updates["token_last_reset"] = rateLimit.TokenLastReset
				}
				if rateLimit.RequestCurrentUsage == 0 && rateLimit.RequestResetDuration != nil {
					updates["request_current_usage"] = 0
					updates["request_last_reset"] = rateLimit.RequestLastReset
				}

				if len(updates) > 0 {
					// Direct UPDATE only resets usage and last_reset fields
					// This prevents overwriting max_limit or reset_duration that may have been changed by other nodes/requests
					result := tx.WithContext(ctx).
						Session(&gorm.Session{SkipHooks: true}).
						Model(&configstoreTables.TableRateLimit{}).
						Where("id = ?", rateLimit.ID).
						Updates(updates)

					if result.Error != nil {
						return fmt.Errorf("failed to reset rate limit %s: %w", rateLimit.ID, result.Error)
					}
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("failed to persist rate limit resets to database: %w", err)
		}
	}
	return nil
}

// DumpRateLimits dumps all rate limits to the database
func (gs *LocalGovernanceStore) DumpRateLimits(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) error {
	if gs.configStore == nil {
		return nil
	}
	// This is to prevent nil pointer dereference
	if tokenBaselines == nil {
		tokenBaselines = map[string]int64{}
	}
	if requestBaselines == nil {
		requestBaselines = map[string]int64{}
	}
	// Collect unique rate limit IDs from virtual keys, model configs, and providers
	rateLimitIDs := make(map[string]bool)
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		if vk.RateLimitID != nil {
			rateLimitIDs[*vk.RateLimitID] = true
		}
		if vk.ProviderConfigs != nil {
			for _, pc := range vk.ProviderConfigs {
				if pc.RateLimitID != nil {
					rateLimitIDs[*pc.RateLimitID] = true
				}
			}
		}
		return true // continue
	})
	// Collect rate limit IDs from model configs
	gs.modelConfigs.Range(func(key, value interface{}) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true // continue
		}
		if mc.RateLimitID != nil {
			rateLimitIDs[*mc.RateLimitID] = true
		}
		return true // continue
	})
	// Collect rate limit IDs from providers
	gs.providers.Range(func(key, value interface{}) bool {
		provider, ok := value.(*configstoreTables.TableProvider)
		if !ok || provider == nil {
			return true // continue
		}
		if provider.RateLimitID != nil {
			rateLimitIDs[*provider.RateLimitID] = true
		}
		return true // continue
	})

	// Collect rate limit IDs from teams
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		if team.RateLimitID != nil {
			rateLimitIDs[*team.RateLimitID] = true
		}
		return true // continue
	})

	// Collect rate limit IDs from customers
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		if customer.RateLimitID != nil {
			rateLimitIDs[*customer.RateLimitID] = true
		}
		return true // continue
	})

	// Prepare rate limit usage updates with baselines
	type rateLimitUpdate struct {
		ID                  string
		TokenCurrentUsage   int64
		RequestCurrentUsage int64
	}
	var rateLimitUpdates []rateLimitUpdate
	for rateLimitID := range rateLimitIDs {
		if rateLimitValue, exists := gs.rateLimits.Load(rateLimitID); exists && rateLimitValue != nil {
			if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
				update := rateLimitUpdate{
					ID:                  rateLimit.ID,
					TokenCurrentUsage:   rateLimit.TokenCurrentUsage,
					RequestCurrentUsage: rateLimit.RequestCurrentUsage,
				}
				if tokenBaseline, exists := tokenBaselines[rateLimit.ID]; exists {
					update.TokenCurrentUsage += tokenBaseline
				}
				if requestBaseline, exists := requestBaselines[rateLimit.ID]; exists {
					update.RequestCurrentUsage += requestBaseline
				}
				rateLimitUpdates = append(rateLimitUpdates, update)
			}
		}
	}

	// Save all updated rate limits to database using direct UPDATE to avoid overwriting config fields
	if len(rateLimitUpdates) > 0 && gs.configStore != nil {
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			for _, update := range rateLimitUpdates {
				// Direct UPDATE only updates usage fields
				// This prevents overwriting max_limit or reset_duration that may have been changed by other nodes/requests
				result := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableRateLimit{}).
					Where("id = ?", update.ID).
					Updates(map[string]interface{}{
						"token_current_usage":   update.TokenCurrentUsage,
						"request_current_usage": update.RequestCurrentUsage,
					})

				if result.Error != nil {
					return fmt.Errorf("failed to dump rate limit %s: %w", update.ID, result.Error)
				}
			}
			return nil
		}); err != nil {
			// Check if error is a deadlock (SQLSTATE 40P01 for PostgreSQL, 1213 for MySQL)
			errStr := err.Error()
			isDeadlock := strings.Contains(errStr, "deadlock") ||
				strings.Contains(errStr, "40P01") ||
				strings.Contains(errStr, "1213")

			if isDeadlock {
				// Deadlock means another node is updating the same rows - this is fine!
				// Our usage data will be synced via gossip and written in the next dump cycle
				gs.logger.Debug("Rate limit dump encountered deadlock (another node is updating) - will retry next cycle")
				return nil // Not a real error in multi-node setup
			}
			return fmt.Errorf("failed to dump rate limits to database: %w", err)
		}
	}
	return nil
}

// DumpBudgets dumps all budgets to the database
func (gs *LocalGovernanceStore) DumpBudgets(ctx context.Context, baselines map[string]float64) error {
	if gs.configStore == nil {
		return nil
	}
	// This is to prevent nil pointer dereference
	if baselines == nil {
		baselines = map[string]float64{}
	}
	budgets := make(map[string]*configstoreTables.TableBudget)
	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		keyStr, keyOk := key.(string)
		budget, budgetOk := value.(*configstoreTables.TableBudget)

		if keyOk && budgetOk && budget != nil {
			budgets[keyStr] = budget // Store budget by ID
		}
		return true // continue iteration
	})
	if len(budgets) > 0 && gs.configStore != nil {
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			// Update each budget atomically using direct UPDATE to avoid deadlocks
			// (SELECT + Save pattern causes deadlocks when multiple instances run concurrently)
			for _, inMemoryBudget := range budgets {
				// Calculate the new usage value
				newUsage := inMemoryBudget.CurrentUsage
				if baseline, exists := baselines[inMemoryBudget.ID]; exists {
					newUsage += baseline
				}

				// Direct UPDATE avoids read-then-write lock escalation that causes deadlocks
				// Use Session with SkipHooks to avoid triggering BeforeSave hook validation
				result := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableBudget{}).
					Where("id = ?", inMemoryBudget.ID).
					Update("current_usage", newUsage)

				if result.Error != nil {
					return fmt.Errorf("failed to update budget %s: %w", inMemoryBudget.ID, result.Error)
				}
			}
			return nil
		}); err != nil {
			// Check if error is a deadlock (SQLSTATE 40P01 for PostgreSQL, 1213 for MySQL)
			errStr := err.Error()
			isDeadlock := strings.Contains(errStr, "deadlock") ||
				strings.Contains(errStr, "40P01") ||
				strings.Contains(errStr, "1213")

			if isDeadlock {
				// Deadlock means another node is updating the same rows - this is fine!
				// Our usage data will be synced via gossip and written in the next dump cycle
				gs.logger.Debug("Budget dump encountered deadlock (another node is updating) - will retry next cycle")
				return nil // Not a real error in multi-node setup
			}
			return fmt.Errorf("failed to dump budgets to database: %w", err)
		}
	}
	return nil
}

// DATABASE METHODS

// loadFromDatabase loads all governance data from the database into memory
func (gs *LocalGovernanceStore) loadFromDatabase(ctx context.Context) error {
	// Load customers with their budgets
	customers, err := gs.configStore.GetCustomers(ctx)
	if err != nil {
		return fmt.Errorf("failed to load customers: %w", err)
	}

	// Load teams with their budgets
	teams, err := gs.configStore.GetTeams(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to load teams: %w", err)
	}

	// Load virtual keys with all relationships
	virtualKeys, err := gs.configStore.GetVirtualKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to load virtual keys: %w", err)
	}

	// Load budgets
	budgets, err := gs.configStore.GetBudgets(ctx)
	if err != nil {
		return fmt.Errorf("failed to load budgets: %w", err)
	}

	// Load rate limits
	rateLimits, err := gs.configStore.GetRateLimits(ctx)
	if err != nil {
		return fmt.Errorf("failed to load rate limits: %w", err)
	}

	// Load model configs
	modelConfigs, err := gs.configStore.GetModelConfigs(ctx)
	if err != nil {
		return fmt.Errorf("failed to load model configs: %w", err)
	}

	// Load providers with governance relationships (similar to GetModelConfigs)
	providers, err := gs.configStore.GetProviders(ctx)
	if err != nil {
		return fmt.Errorf("failed to load providers: %w", err)
	}

	// Load routing rules
	routingRules, err := gs.configStore.GetRoutingRules(ctx)
	if err != nil {
		return fmt.Errorf("failed to load routing rules: %w", err)
	}

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits, modelConfigs, providers, routingRules)

	return nil
}

// loadFromConfigMemory loads all governance data from the config's memory into store's memory
func (gs *LocalGovernanceStore) loadFromConfigMemory(ctx context.Context, config *configstore.GovernanceConfig) error {
	if config == nil {
		return fmt.Errorf("governance config is nil")
	}

	// Load customers with their budgets
	customers := config.Customers

	// Load teams with their budgets
	teams := config.Teams

	// Load budgets
	budgets := config.Budgets

	// Load virtual keys with all relationships
	virtualKeys := config.VirtualKeys

	// Load rate limits
	rateLimits := config.RateLimits

	// Load model configs
	modelConfigs := config.ModelConfigs

	// Load providers
	providers := config.Providers

	// Load routing rules
	routingRules := config.RoutingRules

	// Populate model configs with their relationships (Budget and RateLimit)
	for i := range modelConfigs {
		mc := &modelConfigs[i]

		// Populate budget
		if mc.BudgetID != nil {
			for j := range budgets {
				if budgets[j].ID == *mc.BudgetID {
					mc.Budget = &budgets[j]
					break
				}
			}
		}

		// Populate rate limit
		if mc.RateLimitID != nil {
			for j := range rateLimits {
				if rateLimits[j].ID == *mc.RateLimitID {
					mc.RateLimit = &rateLimits[j]
					break
				}
			}
		}

		modelConfigs[i] = *mc
	}

	// Populate providers with their relationships (Budget and RateLimit)
	for i := range providers {
		provider := &providers[i]

		// Populate budget
		if provider.BudgetID != nil {
			for j := range budgets {
				if budgets[j].ID == *provider.BudgetID {
					provider.Budget = &budgets[j]
					break
				}
			}
		}

		// Populate rate limit
		if provider.RateLimitID != nil {
			for j := range rateLimits {
				if rateLimits[j].ID == *provider.RateLimitID {
					provider.RateLimit = &rateLimits[j]
					break
				}
			}
		}

		providers[i] = *provider
	}

	// Populate virtual keys with their relationships
	for i := range virtualKeys {
		vk := &virtualKeys[i]

		for i := range teams {
			if vk.TeamID != nil && teams[i].ID == *vk.TeamID {
				vk.Team = &teams[i]
			}
		}

		for i := range customers {
			if vk.CustomerID != nil && customers[i].ID == *vk.CustomerID {
				vk.Customer = &customers[i]
			}
		}

		for i := range rateLimits {
			if vk.RateLimitID != nil && rateLimits[i].ID == *vk.RateLimitID {
				vk.RateLimit = &rateLimits[i]
			}
		}

		// Populate provider config relationships with rate limits
		if vk.ProviderConfigs != nil {
			for j := range vk.ProviderConfigs {
				pc := &vk.ProviderConfigs[j]

				// Populate rate limit
				if pc.RateLimitID != nil {
					for k := range rateLimits {
						if rateLimits[k].ID == *pc.RateLimitID {
							pc.RateLimit = &rateLimits[k]
							break
						}
					}
				}
			}
		}

		virtualKeys[i] = *vk
	}

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits, modelConfigs, providers, routingRules)

	return nil
}

// rebuildInMemoryStructures rebuilds all in-memory data structures (lock-free)
func (gs *LocalGovernanceStore) rebuildInMemoryStructures(ctx context.Context, customers []configstoreTables.TableCustomer, teams []configstoreTables.TableTeam, virtualKeys []configstoreTables.TableVirtualKey, budgets []configstoreTables.TableBudget, rateLimits []configstoreTables.TableRateLimit, modelConfigs []configstoreTables.TableModelConfig, providers []configstoreTables.TableProvider, routingRules []configstoreTables.TableRoutingRule) {
	// Clear existing data by creating new sync.Maps
	gs.virtualKeys = sync.Map{}
	gs.teams = sync.Map{}
	gs.customers = sync.Map{}
	gs.budgets = sync.Map{}
	gs.rateLimits = sync.Map{}
	gs.modelConfigs = sync.Map{}
	gs.providers = sync.Map{}
	gs.routingRules = sync.Map{}

	// Build customers map
	for i := range customers {
		customer := &customers[i]
		gs.customers.Store(customer.ID, customer)
	}

	// Build teams map
	for i := range teams {
		team := &teams[i]
		gs.teams.Store(team.ID, team)
	}

	// Build budgets map
	for i := range budgets {
		budget := &budgets[i]
		gs.budgets.Store(budget.ID, budget)
	}

	// Build rate limits map
	for i := range rateLimits {
		rateLimit := &rateLimits[i]
		gs.rateLimits.Store(rateLimit.ID, rateLimit)
	}

	// Build virtual keys map and track active VKs
	for i := range virtualKeys {
		vk := &virtualKeys[i]
		gs.virtualKeys.Store(vk.Value, vk)
	}

	// Build model configs map
	// Key format: "modelName" for global configs, "modelName:provider" for provider-specific configs
	// Model names are normalized using GetBaseModelName to prevent duplicate config leakage
	// (e.g., "openai/gpt-4o" and "gpt-4o" both store under key "gpt-4o")
	for i := range modelConfigs {
		mc := &modelConfigs[i]
		if mc.Provider != nil {
			// Store under provider-specific key
			key := fmt.Sprintf("%s:%s", mc.ModelName, *mc.Provider)
			gs.modelConfigs.Store(key, mc)
		} else {
			// Global config (applies to all providers) - store under normalized model name
			key := mc.ModelName
			if gs.modelCatalog != nil {
				key = gs.modelCatalog.GetBaseModelName(mc.ModelName)
			}
			gs.modelConfigs.Store(key, mc)
		}
	}

	// Build providers map
	// Key format: provider name (e.g., "openai", "anthropic")
	for i := range providers {
		provider := &providers[i]
		gs.providers.Store(provider.Name, provider)
	}

	// Build routing rules map - O(n) single pass
	// Key format: "scope:scopeID" (scopeID empty string for global)
	rulesMap := make(map[string][]*configstoreTables.TableRoutingRule)

	for i := range routingRules {
		rule := &routingRules[i]

		// Build key
		key := rule.Scope + ":"
		if rule.ScopeID != nil {
			key += *rule.ScopeID
		}

		// Group rules by key
		rulesMap[key] = append(rulesMap[key], rule)
	}

	// Sort each group by priority ASC (0 is highest priority, higher numbers are lower priority)
	for key, rules := range rulesMap {
		sort.Slice(rules, func(i, j int) bool {
			return rules[i].Priority < rules[j].Priority
		})
		gs.routingRules.Store(key, rules)
	}

	// Pre-compile all routing rule programs to avoid first-request latency
	gs.routingRules.Range(func(key, value interface{}) bool {
		if rules, ok := value.([]*configstoreTables.TableRoutingRule); ok {
			for _, rule := range rules {
				if _, err := gs.GetRoutingProgram(ctx, rule); err != nil {
					gs.logger.Warn("Failed to pre-compile routing program for rule %s: %v", rule.Name, err)
				}
			}
		}
		return true
	})

	// Load last DB usages from database entities (assign and populate inside mutexes to avoid race with ResetExpired*InMemory)
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets = make(map[string]float64)
	for i := range budgets {
		budget := &budgets[i]
		gs.LastDBUsagesBudgets[budget.ID] = budget.CurrentUsage
	}
	gs.LastDBUsagesBudgetsMu.Unlock()

	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	gs.LastDBUsagesRequestsRateLimits = make(map[string]int64)
	gs.LastDBUsagesTokensRateLimits = make(map[string]int64)
	for i := range rateLimits {
		rateLimit := &rateLimits[i]
		gs.LastDBUsagesRequestsRateLimits[rateLimit.ID] = rateLimit.RequestCurrentUsage
		gs.LastDBUsagesTokensRateLimits[rateLimit.ID] = rateLimit.TokenCurrentUsage
	}
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
}

// collectRateLimitsFromHierarchy collects rate limits and their metadata from the hierarchy (Provider Configs → VK → Team → Customer)
func (gs *LocalGovernanceStore) collectRateLimitsFromHierarchy(ctx context.Context, vk *configstoreTables.TableVirtualKey, requestedProvider schemas.ModelProvider) map[string][]*configstoreTables.TableRateLimit {
	if vk == nil {
		return nil
	}

	rateLimitsWithCategories := map[string][]*configstoreTables.TableRateLimit{}
	seen := map[string]bool{}

	for _, pc := range vk.ProviderConfigs {
		if pc.RateLimitID != nil && pc.Provider == string(requestedProvider) {
			if rateLimitValue, exists := gs.rateLimits.Load(*pc.RateLimitID); exists && rateLimitValue != nil {
				if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
					if categoryRateLimits := rateLimitsWithCategories[pc.Provider]; categoryRateLimits == nil {
						rateLimitsWithCategories[pc.Provider] = []*configstoreTables.TableRateLimit{}
					}
					rateLimitsWithCategories[pc.Provider] = append(rateLimitsWithCategories[pc.Provider], rateLimit)
					seen[rateLimit.ID] = true
				}
			}
		}
	}

	if vk.RateLimitID != nil {
		if rateLimitValue, exists := gs.rateLimits.Load(*vk.RateLimitID); exists && rateLimitValue != nil {
			if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
				if categoryRateLimits := rateLimitsWithCategories["VK"]; categoryRateLimits == nil {
					rateLimitsWithCategories["VK"] = []*configstoreTables.TableRateLimit{}
				}
				rateLimitsWithCategories["VK"] = append(rateLimitsWithCategories["VK"], rateLimit)
				seen[rateLimit.ID] = true
			}
		}
	}

	// Check Team rate limit if VK belongs to a team
	var teamCustomerID string
	if vk.TeamID != nil {
		if teamValue, exists := gs.teams.Load(*vk.TeamID); exists && teamValue != nil {
			if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
				if team.RateLimitID != nil {
					if rateLimitValue, exists := gs.rateLimits.Load(*team.RateLimitID); exists && rateLimitValue != nil {
						if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
							if categoryRateLimits := rateLimitsWithCategories["Team"]; categoryRateLimits == nil {
								rateLimitsWithCategories["Team"] = []*configstoreTables.TableRateLimit{}
							}
							rateLimitsWithCategories["Team"] = append(rateLimitsWithCategories["Team"], rateLimit)
							seen[rateLimit.ID] = true
						}
					}
				}

				// Check if team belongs to a customer
				if team.CustomerID != nil {
					teamCustomerID = *team.CustomerID
					if customerValue, exists := gs.customers.Load(*team.CustomerID); exists && customerValue != nil {
						if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
							if customer.RateLimitID != nil {
								if rateLimitValue, exists := gs.rateLimits.Load(*customer.RateLimitID); exists && rateLimitValue != nil {
									if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
										if categoryRateLimits := rateLimitsWithCategories["Customer"]; categoryRateLimits == nil {
											rateLimitsWithCategories["Customer"] = []*configstoreTables.TableRateLimit{}
										}
										rateLimitsWithCategories["Customer"] = append(rateLimitsWithCategories["Customer"], rateLimit)
										seen[rateLimit.ID] = true
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Check Customer rate limit if VK directly belongs to a customer (skip if already collected via team)
	if vk.CustomerID != nil && (teamCustomerID == "" || *vk.CustomerID != teamCustomerID) {
		if customerValue, exists := gs.customers.Load(*vk.CustomerID); exists && customerValue != nil {
			if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
				if customer.RateLimitID != nil {
					if rateLimitValue, exists := gs.rateLimits.Load(*customer.RateLimitID); exists && rateLimitValue != nil {
						if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
							if categoryRateLimits := rateLimitsWithCategories["Customer"]; categoryRateLimits == nil {
								rateLimitsWithCategories["Customer"] = []*configstoreTables.TableRateLimit{}
							}
							rateLimitsWithCategories["Customer"] = append(rateLimitsWithCategories["Customer"], rateLimit)
							seen[rateLimit.ID] = true
						}
					}
				}
			}
		}
	}
	return rateLimitsWithCategories
}

// collectBudgetsFromHierarchy collects budgets and their metadata from the hierarchy (Provider Configs → VK → Customer -> User -> Team → BusinessUnit)
func (gs *LocalGovernanceStore) collectBudgetsFromHierarchy(_ context.Context, vk *configstoreTables.TableVirtualKey, requestedProvider schemas.ModelProvider) EntityWiseBudgets {
	if vk == nil {
		return nil
	}
	entityWiseBudgets := make(EntityWiseBudgets)
	// Collect all budgets in hierarchy order using lock-free sync.Map access (Provider Configs → VK → Team → Customer)
	seen := make(map[string]bool)
	for _, pc := range vk.ProviderConfigs {
		if pc.Provider != string(requestedProvider) {
			continue
		}
		// Multi-budgets
		for _, b := range pc.Budgets {
			if seen[b.ID] {
				continue
			}
			if budgetValue, exists := gs.budgets.Load(b.ID); exists && budgetValue != nil {
				if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
					if categoryBudgets := entityWiseBudgets[pc.Provider]; categoryBudgets == nil {
						entityWiseBudgets[pc.Provider] = []*configstoreTables.TableBudget{}
					}
					entityWiseBudgets[pc.Provider] = append(entityWiseBudgets[pc.Provider], budget)
					seen[budget.ID] = true
				}
			}
		}
	}
	// VK-level multi-budgets
	for _, b := range vk.Budgets {
		if seen[b.ID] {
			continue
		}
		if budgetValue, exists := gs.budgets.Load(b.ID); exists && budgetValue != nil {
			if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
				if categoryBudgets := entityWiseBudgets["VK"]; categoryBudgets == nil {
					entityWiseBudgets["VK"] = []*configstoreTables.TableBudget{}
				}
				entityWiseBudgets["VK"] = append(entityWiseBudgets["VK"], budget)
				seen[budget.ID] = true
			}
		}
	}
	var teamCustomerID string
	if vk.TeamID != nil {
		if teamValue, exists := gs.teams.Load(*vk.TeamID); exists && teamValue != nil {
			if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
				for _, tb := range team.Budgets {
					if seen[tb.ID] {
						continue
					}
					if budgetValue, exists := gs.budgets.Load(tb.ID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
							if categoryBudgets := entityWiseBudgets["Team"]; categoryBudgets == nil {
								entityWiseBudgets["Team"] = []*configstoreTables.TableBudget{}
							}
							entityWiseBudgets["Team"] = append(entityWiseBudgets["Team"], budget)
							seen[budget.ID] = true
						}
					}
				}

				// Check if team belongs to a customer
				if team.CustomerID != nil {
					teamCustomerID = *team.CustomerID
					if customerValue, exists := gs.customers.Load(*team.CustomerID); exists && customerValue != nil {
						if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
							if customer.BudgetID != nil {
								if budgetValue, exists := gs.budgets.Load(*customer.BudgetID); exists && budgetValue != nil {
									if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
										if categoryBudgets := entityWiseBudgets["Customer"]; categoryBudgets == nil {
											entityWiseBudgets["Customer"] = []*configstoreTables.TableBudget{}
										}
										entityWiseBudgets["Customer"] = append(entityWiseBudgets["Customer"], budget)
										seen[budget.ID] = true
									}
								}
							}
						}
					}
				}
			}
		}
	}
	// Check Customer budget if VK directly belongs to a customer (skip if already collected via team)
	if vk.CustomerID != nil && (teamCustomerID == "" || *vk.CustomerID != teamCustomerID) {
		if customerValue, exists := gs.customers.Load(*vk.CustomerID); exists && customerValue != nil {
			if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
				if customer.BudgetID != nil {
					if budgetValue, exists := gs.budgets.Load(*customer.BudgetID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
							if categoryBudgets := entityWiseBudgets["Customer"]; categoryBudgets == nil {
								entityWiseBudgets["Customer"] = []*configstoreTables.TableBudget{}
							}
							entityWiseBudgets["Customer"] = append(entityWiseBudgets["Customer"], budget)
							seen[budget.ID] = true
						}
					}
				}
			}
		}
	}
	return entityWiseBudgets
}

// collectBudgetIDsFromMemory collects budget IDs from in-memory store data (lock-free)
func (gs *LocalGovernanceStore) collectBudgetIDsFromMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider) []string {
	budgetsWithCategory := gs.collectBudgetsFromHierarchy(ctx, vk, provider)
	budgetIDs := []string{}
	for _, budgets := range budgetsWithCategory {
		for _, budget := range budgets {
			budgetIDs = append(budgetIDs, budget.ID)
		}
	}
	return budgetIDs
}

// collectRateLimitIDsFromMemory collects rate limit IDs from in-memory store data (lock-free)
func (gs *LocalGovernanceStore) collectRateLimitIDsFromMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider) []string {
	rateLimitsWithCategories := gs.collectRateLimitsFromHierarchy(ctx, vk, provider)
	rateLimitIDs := []string{}
	for _, rateLimits := range rateLimitsWithCategories {
		for _, rateLimit := range rateLimits {
			rateLimitIDs = append(rateLimitIDs, rateLimit.ID)
		}
	}
	return rateLimitIDs
}

// PUBLIC API METHODS

// CreateVirtualKeyInMemory adds a new virtual key to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey) {
	if vk == nil {
		return // Nothing to create
	}

	// Store budgets
	for i := range vk.Budgets {
		gs.budgets.Store(vk.Budgets[i].ID, &vk.Budgets[i])
	}

	// Create associated rate limit if exists
	if vk.RateLimit != nil {
		gs.rateLimits.Store(vk.RateLimit.ID, vk.RateLimit)
	}

	// Create provider config budgets and rate limits if they exist
	if vk.ProviderConfigs != nil {
		for _, pc := range vk.ProviderConfigs {
			for i := range pc.Budgets {
				gs.budgets.Store(pc.Budgets[i].ID, &pc.Budgets[i])
			}
			if pc.RateLimit != nil {
				gs.rateLimits.Store(pc.RateLimit.ID, pc.RateLimit)
			}
		}
	}

	gs.virtualKeys.Store(vk.Value, vk)
}

// UpdateVirtualKeyInMemory updates an existing virtual key in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64) {
	if vk == nil {
		return // Nothing to update
	}

	// Do not update the current usage of the rate limit, as it will be updated by the usage tracker.
	// But update if max limit or reset duration changes.
	if existingVKValue, exists := gs.virtualKeys.Load(vk.Value); exists && existingVKValue != nil {
		existingVK, ok := existingVKValue.(*configstoreTables.TableVirtualKey)
		if !ok || existingVK == nil {
			return // Nothing to update
		}

		// Create clone to avoid modifying the original
		clone := *vk

		// Collect all incoming budget IDs across VK + provider configs to avoid
		// deleting a budget that was moved between VK-level and PC-level in one update.
		allNewBudgetIDs := make(map[string]bool)
		for i := range clone.Budgets {
			allNewBudgetIDs[clone.Budgets[i].ID] = true
		}
		for i := range clone.ProviderConfigs {
			for j := range clone.ProviderConfigs[i].Budgets {
				allNewBudgetIDs[clone.ProviderConfigs[i].Budgets[j].ID] = true
			}
		}

		// Update multi-budgets for VK
		for i := range clone.Budgets {
			// Preserve existing usage from memory
			if existingBudgetValue, exists := gs.budgets.Load(clone.Budgets[i].ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					clone.Budgets[i].CurrentUsage = existingBudget.CurrentUsage
					clone.Budgets[i].LastReset = existingBudget.LastReset
				}
			}
			gs.budgets.Store(clone.Budgets[i].ID, &clone.Budgets[i])
		}
		// Delete removed multi-budgets
		for _, oldBudget := range existingVK.Budgets {
			if !allNewBudgetIDs[oldBudget.ID] {
				gs.budgets.Delete(oldBudget.ID)
			}
		}

		if clone.RateLimit != nil {
			// Preserve existing usage from memory when updating rate limit config
			// The usage tracker maintains current usage in memory, and we only want to update
			// the configuration fields (max_limit, reset_duration) from the database
			if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
				if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
					// Preserve current usage and last reset times from existing in-memory rate limit
					clone.RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
					clone.RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
					clone.RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
					clone.RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
				}
			}
			// Update the rate limit in the main rateLimits sync.Map
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
		} else if existingVK.RateLimit != nil {
			// Rate limit was removed from the virtual key, delete it from memory
			gs.rateLimits.Delete(existingVK.RateLimit.ID)
		}
		if clone.ProviderConfigs != nil {
			// Create a map of existing provider configs by ID for fast lookup
			existingProviderConfigs := make(map[uint]configstoreTables.TableVirtualKeyProviderConfig)
			if existingVK.ProviderConfigs != nil {
				for _, existingPC := range existingVK.ProviderConfigs {
					existingProviderConfigs[existingPC.ID] = existingPC
				}
			}

			// Process each new/updated provider config
			for i, pc := range clone.ProviderConfigs {
				if pc.RateLimit != nil {
					// Preserve existing usage from memory when updating provider config rate limit
					if existingRateLimitValue, exists := gs.rateLimits.Load(pc.RateLimit.ID); exists && existingRateLimitValue != nil {
						if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
							// Preserve current usage and last reset times from existing in-memory rate limit
							clone.ProviderConfigs[i].RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
							clone.ProviderConfigs[i].RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
							clone.ProviderConfigs[i].RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
							clone.ProviderConfigs[i].RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
						}
					}
					gs.rateLimits.Store(clone.ProviderConfigs[i].RateLimit.ID, clone.ProviderConfigs[i].RateLimit)
				} else {
					// Rate limit was removed from provider config, delete it from memory if it existed
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.RateLimit != nil {
						gs.rateLimits.Delete(existingPC.RateLimit.ID)
						clone.ProviderConfigs[i].RateLimit = nil
					}
				}
				// Update multi-budgets for provider config
				for j := range clone.ProviderConfigs[i].Budgets {
					b := &clone.ProviderConfigs[i].Budgets[j]
					if existingBudgetValue, exists := gs.budgets.Load(b.ID); exists && existingBudgetValue != nil {
						if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
							b.CurrentUsage = existingBudget.CurrentUsage
							b.LastReset = existingBudget.LastReset
						}
					}
					gs.budgets.Store(b.ID, b)
				}
				// Delete removed multi-budgets for this provider config
				if existingPC, exists := existingProviderConfigs[pc.ID]; exists {
					for _, oldBudget := range existingPC.Budgets {
						if !allNewBudgetIDs[oldBudget.ID] {
							gs.budgets.Delete(oldBudget.ID)
						}
					}
				}
			}
		}
		gs.virtualKeys.Store(vk.Value, &clone)
	} else {
		gs.CreateVirtualKeyInMemory(ctx, vk)
	}
}

// DeleteVirtualKeyInMemory removes a virtual key from the in-memory store
func (gs *LocalGovernanceStore) DeleteVirtualKeyInMemory(ctx context.Context, vkID string) {
	if vkID == "" {
		return // Nothing to delete
	}

	// Find and delete the VK by ID (lock-free)
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue iteration
		}

		if vk.ID == vkID {
			// Delete budgets
			for _, b := range vk.Budgets {
				gs.budgets.Delete(b.ID)
			}

			// Delete associated rate limit if exists
			if vk.RateLimitID != nil {
				gs.rateLimits.Delete(*vk.RateLimitID)
			}

			// Delete provider config budgets and rate limits
			if vk.ProviderConfigs != nil {
				for _, pc := range vk.ProviderConfigs {
					for _, b := range pc.Budgets {
						gs.budgets.Delete(b.ID)
					}
					if pc.RateLimitID != nil {
						gs.rateLimits.Delete(*pc.RateLimitID)
					}
				}
			}

			gs.virtualKeys.Delete(key)
			return false // stop iteration
		}
		return true // continue iteration
	})
}

// CreateTeamInMemory adds a new team to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam) {
	if team == nil {
		return // Nothing to create
	}

	// Create associated budgets if they exist
	for i := range team.Budgets {
		b := team.Budgets[i]
		gs.budgets.Store(b.ID, &b)
	}

	// Create associated rate limit if exists
	if team.RateLimit != nil {
		gs.rateLimits.Store(team.RateLimit.ID, team.RateLimit)
	}

	gs.teams.Store(team.ID, team)
}

// UpdateTeamInMemory updates an existing team in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam, budgetBaselines map[string]float64) {
	if team == nil {
		return // Nothing to update
	}

	// Check if there's an existing team to get current budget state
	if existingTeamValue, exists := gs.teams.Load(team.ID); exists && existingTeamValue != nil {
		existingTeam, ok := existingTeamValue.(*configstoreTables.TableTeam)
		if !ok || existingTeam == nil {
			return // Nothing to update
		}

		// Create clone to avoid modifying the original
		clone := *team

		// Reconcile multi-budget slice by ID: preserve live usage on matches,
		// evict budgets that disappeared from the team (owned-FK semantics —
		// a team's budgets are team-scoped, so dropping the association means
		// the budget no longer exists for anyone).
		existingBudgetIDs := map[string]struct{}{}
		for _, b := range existingTeam.Budgets {
			existingBudgetIDs[b.ID] = struct{}{}
		}
		nextBudgetIDs := map[string]struct{}{}
		for i := range clone.Budgets {
			b := &clone.Budgets[i]
			nextBudgetIDs[b.ID] = struct{}{}
			if live, exists := gs.budgets.Load(b.ID); exists && live != nil {
				if lb, ok := live.(*configstoreTables.TableBudget); ok && lb != nil {
					b.CurrentUsage = lb.CurrentUsage
					b.LastReset = lb.LastReset
				}
			}
			gs.budgets.Store(b.ID, b)
		}
		for id := range existingBudgetIDs {
			if _, stillThere := nextBudgetIDs[id]; !stillThere {
				gs.budgets.Delete(id)
			}
		}

		// Handle rate limit updates with consistent logic
		if clone.RateLimit != nil {
			// Preserve existing usage from memory when updating team rate limit config
			if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
				if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
					// Preserve current usage and last reset time from existing in-memory rate limit
					clone.RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
					clone.RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
					clone.RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
					clone.RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
				}
			}
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
		} else if existingTeam.RateLimit != nil {
			// Rate limit was removed from the team, delete it from memory
			gs.rateLimits.Delete(existingTeam.RateLimit.ID)
		}

		gs.teams.Store(team.ID, &clone)
	} else {
		gs.CreateTeamInMemory(ctx, team)
	}
}

// DeleteTeamInMemory removes a team from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteTeamInMemory(ctx context.Context, teamID string) {
	if teamID == "" {
		return // Nothing to delete
	}

	// Get team to check for associated budgets and rate limit
	if teamValue, exists := gs.teams.Load(teamID); exists && teamValue != nil {
		if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
			// Delete all associated budgets
			for _, b := range team.Budgets {
				gs.budgets.Delete(b.ID)
			}
			// Delete associated rate limit if exists
			if team.RateLimitID != nil {
				gs.rateLimits.Delete(*team.RateLimitID)
			}
		}
	}

	// Set team_id to null for all virtual keys associated with the team
	// Iterate through all VKs since team.VirtualKeys may not be populated
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		if vk.TeamID != nil && *vk.TeamID == teamID {
			clone := *vk
			clone.TeamID = nil
			clone.Team = nil
			gs.virtualKeys.Store(key, &clone)
		}
		return true // continue iteration
	})

	gs.teams.Delete(teamID)
}

// CreateCustomerInMemory adds a new customer to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer) {
	if customer == nil {
		return // Nothing to create
	}
	// Create associated budget if exists
	if customer.Budget != nil {
		gs.budgets.Store(customer.Budget.ID, customer.Budget)
	}
	// Create associated rate limit if exists
	if customer.RateLimit != nil {
		gs.rateLimits.Store(customer.RateLimit.ID, customer.RateLimit)
	}
	gs.customers.Store(customer.ID, customer)
}

// UpdateCustomerInMemory updates an existing customer in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateCustomerInMemory(ctx context.Context, customer *configstoreTables.TableCustomer, budgetBaselines map[string]float64) {
	if customer == nil {
		return // Nothing to update
	}
	// Check if there's an existing customer to get current budget state
	if existingCustomerValue, exists := gs.customers.Load(customer.ID); exists && existingCustomerValue != nil {
		existingCustomer, ok := existingCustomerValue.(*configstoreTables.TableCustomer)
		if !ok || existingCustomer == nil {
			return // Nothing to update
		}
		// Create clone to avoid modifying the original
		clone := *customer

		// Handle budget updates with consistent logic
		if clone.Budget != nil {
			// Preserve existing usage from memory when updating customer budget config
			if existingBudgetValue, exists := gs.budgets.Load(clone.Budget.ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					// Preserve current usage and last reset time from existing in-memory budget
					clone.Budget.CurrentUsage = existingBudget.CurrentUsage
					clone.Budget.LastReset = existingBudget.LastReset
				}
			}
			gs.budgets.Store(clone.Budget.ID, clone.Budget)
		} else if existingCustomer.Budget != nil {
			// Budget was removed from the customer, delete it from memory
			gs.budgets.Delete(existingCustomer.Budget.ID)
		}

		// Handle rate limit updates with consistent logic
		if clone.RateLimit != nil {
			// Preserve existing usage from memory when updating customer rate limit config
			if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
				if existingRateLimit, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && existingRateLimit != nil {
					// Preserve current usage and last reset time from existing in-memory rate limit
					clone.RateLimit.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
					clone.RateLimit.TokenLastReset = existingRateLimit.TokenLastReset
					clone.RateLimit.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
					clone.RateLimit.RequestLastReset = existingRateLimit.RequestLastReset
				}
			}
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
		} else if existingCustomer.RateLimit != nil {
			// Rate limit was removed from the customer, delete it from memory
			gs.rateLimits.Delete(existingCustomer.RateLimit.ID)
		}

		gs.customers.Store(customer.ID, &clone)
	} else {
		gs.CreateCustomerInMemory(ctx, customer)
	}
}

// DeleteCustomerInMemory removes a customer from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteCustomerInMemory(ctx context.Context, customerID string) {
	if customerID == "" {
		return // Nothing to delete
	}
	// Get customer to check for associated budget and rate limit
	if customerValue, exists := gs.customers.Load(customerID); exists && customerValue != nil {
		if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
			// Delete associated budget if exists
			if customer.BudgetID != nil {
				gs.budgets.Delete(*customer.BudgetID)
			}
			// Delete associated rate limit if exists
			if customer.RateLimitID != nil {
				gs.rateLimits.Delete(*customer.RateLimitID)
			}
		}
	}
	// Set customer_id to null for all virtual keys associated with the customer
	// Iterate through all VKs since customer.VirtualKeys may not be populated
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		if vk.CustomerID != nil && *vk.CustomerID == customerID {
			clone := *vk
			clone.CustomerID = nil
			clone.Customer = nil
			gs.virtualKeys.Store(key, &clone)
		}
		return true // continue iteration
	})
	// Set customer_id to null for all teams associated with the customer
	// Iterate through all teams since customer.Teams may not be populated
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		if team.CustomerID != nil && *team.CustomerID == customerID {
			clone := *team
			clone.CustomerID = nil
			clone.Customer = nil
			gs.teams.Store(key, &clone)
		}
		return true // continue iteration
	})
	gs.customers.Delete(customerID)
}

// GetUserGovernance retrieves user governance data by user ID (enterprise-only, lock-free)
func (gs *LocalGovernanceStore) GetUserGovernance(ctx context.Context, userID string) (*UserGovernance, bool) {
	// User governance is part of enterprise
	return nil, false
}

// CreateUserGovernanceInMemory adds user governance data to the in-memory store (enterprise-only)
func (gs *LocalGovernanceStore) CreateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) {
	// NoOp
	// Available in enterprise
}

// UpdateUserGovernanceInMemory updates user governance data in the in-memory store (enterprise-only)
func (gs *LocalGovernanceStore) UpdateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit) {
	// NoOp
	// Available in enterprise
}

// DeleteUserGovernanceInMemory removes user governance data from the in-memory store (enterprise-only)
func (gs *LocalGovernanceStore) DeleteUserGovernanceInMemory(ctx context.Context, userID string) {
	// NoOp
	// Available in enterprise
}

// UpdateModelConfigInMemory adds or updates a model config in the in-memory store (lock-free)
// Preserves existing usage values when updating budgets and rate limits
// Returns the updated model config with potentially modified usage values
func (gs *LocalGovernanceStore) UpdateModelConfigInMemory(ctx context.Context, mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig {
	if mc == nil {
		return nil // Nothing to update
	}

	// Clone to avoid modifying the original
	clone := *mc

	// Store associated budget if exists, preserving existing in-memory usage
	if clone.Budget != nil {
		if existingBudgetValue, exists := gs.budgets.Load(clone.Budget.ID); exists && existingBudgetValue != nil {
			if eb, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && eb != nil {
				clone.Budget.CurrentUsage = eb.CurrentUsage
			}
		}
		gs.budgets.Store(clone.Budget.ID, clone.Budget)
	}

	// Store associated rate limit if exists, preserving existing in-memory usage
	if clone.RateLimit != nil {
		if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
			if erl, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && erl != nil {
				clone.RateLimit.TokenCurrentUsage = erl.TokenCurrentUsage
				clone.RateLimit.RequestCurrentUsage = erl.RequestCurrentUsage
			}
		}
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Determine the key based on whether provider is specified
	// Key format: "modelName" for global configs, "modelName:provider" for provider-specific configs
	if clone.Provider != nil {
		key := fmt.Sprintf("%s:%s", clone.ModelName, *clone.Provider)
		gs.modelConfigs.Store(key, &clone)
	} else {
		key := clone.ModelName
		if gs.modelCatalog != nil {
			key = gs.modelCatalog.GetBaseModelName(clone.ModelName)
		}
		gs.modelConfigs.Store(key, &clone)
	}

	return &clone
}

// DeleteModelConfigInMemory removes a model config from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteModelConfigInMemory(ctx context.Context, mcID string) {
	if mcID == "" {
		return // Nothing to delete
	}

	// Find and delete the model config by ID
	gs.modelConfigs.Range(func(key, value interface{}) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true // continue iteration
		}

		if mc.ID == mcID {
			// Delete associated budget if exists
			if mc.BudgetID != nil {
				gs.budgets.Delete(*mc.BudgetID)
			}

			// Delete associated rate limit if exists
			if mc.RateLimitID != nil {
				gs.rateLimits.Delete(*mc.RateLimitID)
			}

			gs.modelConfigs.Delete(key)
			return false // stop iteration
		}
		return true // continue iteration
	})
}

// UpdateProviderInMemory adds or updates a provider in the in-memory store (lock-free)
// Preserves existing usage values when updating budgets and rate limits
// Returns the updated provider with potentially modified usage values
func (gs *LocalGovernanceStore) UpdateProviderInMemory(ctx context.Context, provider *configstoreTables.TableProvider) *configstoreTables.TableProvider {
	if provider == nil {
		return nil // Nothing to update
	}

	// Clone to avoid modifying the original
	clone := *provider

	// Store associated budget if exists, preserving existing in-memory usage
	if clone.Budget != nil {
		if existingBudgetValue, exists := gs.budgets.Load(clone.Budget.ID); exists && existingBudgetValue != nil {
			if eb, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && eb != nil {
				clone.Budget.CurrentUsage = eb.CurrentUsage
			}
		}
		gs.budgets.Store(clone.Budget.ID, clone.Budget)
	}

	// Store associated rate limit if exists, preserving existing in-memory usage
	if clone.RateLimit != nil {
		if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
			if erl, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && erl != nil {
				clone.RateLimit.TokenCurrentUsage = erl.TokenCurrentUsage
				clone.RateLimit.RequestCurrentUsage = erl.RequestCurrentUsage
			}
		}
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Store under provider name
	gs.providers.Store(clone.Name, &clone)

	return &clone
}

// DeleteProviderInMemory removes a provider from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteProviderInMemory(ctx context.Context, providerName string) {
	if providerName == "" {
		return // Nothing to delete
	}
	// Get provider to check for associated budget/rate limit
	if providerValue, exists := gs.providers.Load(providerName); exists && providerValue != nil {
		if provider, ok := providerValue.(*configstoreTables.TableProvider); ok && provider != nil {
			// Delete associated budget if exists
			if provider.BudgetID != nil {
				gs.budgets.Delete(*provider.BudgetID)
			}

			// Delete associated rate limit if exists
			if provider.RateLimitID != nil {
				gs.rateLimits.Delete(*provider.RateLimitID)
			}
		}
	}
	gs.providers.Delete(providerName)
}

// Helper functions

// updateBudgetReferences updates all VKs, teams, customers, and provider configs that reference a reset budget
func (gs *LocalGovernanceStore) updateBudgetReferences(ctx context.Context, resetBudget *configstoreTables.TableBudget) {
	budgetID := resetBudget.ID
	// Update VKs that reference this budget
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		needsUpdate := false
		clone := *vk

		// Check VK-level budgets
		for i, b := range clone.Budgets {
			if b.ID == budgetID {
				clone.Budgets[i] = *resetBudget
				needsUpdate = true
			}
		}
		// Check provider config budgets
		if vk.ProviderConfigs != nil {
			for i := range clone.ProviderConfigs {
				for j, b := range clone.ProviderConfigs[i].Budgets {
					if b.ID == budgetID {
						clone.ProviderConfigs[i].Budgets[j] = *resetBudget
						needsUpdate = true
					}
				}
			}
		}
		if needsUpdate {
			gs.virtualKeys.Store(key, &clone)
		}
		return true // continue
	})
	// Update teams that reference this budget
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		for i := range team.Budgets {
			if team.Budgets[i].ID == budgetID {
				clone := *team
				clone.Budgets = append([]configstoreTables.TableBudget(nil), team.Budgets...)
				clone.Budgets[i] = *resetBudget
				gs.teams.Store(key, &clone)
				break
			}
		}
		return true // continue
	})
	// Update customers that reference this budget
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		if customer.BudgetID != nil && *customer.BudgetID == budgetID {
			clone := *customer
			clone.Budget = resetBudget
			gs.customers.Store(key, &clone)
		}
		return true // continue
	})
}

// updateRateLimitReferences updates all VKs, teams, customers, users and provider configs that reference a reset rate limit
func (gs *LocalGovernanceStore) updateRateLimitReferences(ctx context.Context, resetRateLimit *configstoreTables.TableRateLimit) {
	rateLimitID := resetRateLimit.ID
	// Update VKs that reference this rate limit
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		needsUpdate := false
		clone := *vk

		// Check VK-level rate limit
		if vk.RateLimitID != nil && *vk.RateLimitID == rateLimitID {
			clone.RateLimit = resetRateLimit
			needsUpdate = true
		}

		// Check provider config rate limits
		if vk.ProviderConfigs != nil {
			for i, pc := range clone.ProviderConfigs {
				if pc.RateLimitID != nil && *pc.RateLimitID == rateLimitID {
					clone.ProviderConfigs[i].RateLimit = resetRateLimit
					needsUpdate = true
				}
			}
		}

		if needsUpdate {
			gs.virtualKeys.Store(key, &clone)
		}
		return true // continue
	})
	// Update teams that reference this rate limit
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		if team.RateLimitID != nil && *team.RateLimitID == rateLimitID {
			clone := *team
			clone.RateLimit = resetRateLimit
			gs.teams.Store(key, &clone)
		}
		return true // continue
	})
	// Update customers that reference this rate limit
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		if customer.RateLimitID != nil && *customer.RateLimitID == rateLimitID {
			clone := *customer
			clone.RateLimit = resetRateLimit
			gs.customers.Store(key, &clone)
		}
		return true // continue
	})
}

// HasRoutingRules checks if there are any routing rules configured
// Quick check to determine if we need to run routing evaluation at all
func (gs *LocalGovernanceStore) HasRoutingRules(ctx context.Context) bool {
	hasAny := false
	gs.routingRules.Range(func(_, _ interface{}) bool {
		hasAny = true
		return false // stop after first entry
	})
	return hasAny
}

// GetAllRoutingRules gets all routing rules from in-memory cache
func (gs *LocalGovernanceStore) GetAllRoutingRules(ctx context.Context) []*configstoreTables.TableRoutingRule {
	var result []*configstoreTables.TableRoutingRule

	// Iterate through all cached rules
	gs.routingRules.Range(func(_, value interface{}) bool {
		rules, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok {
			return true
		}
		result = append(result, rules...)
		return true
	})

	// Sort by priority ASC (0 is highest priority, higher numbers are lower priority), then created_at ASC
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result
}

// GetScopedRoutingRules retrieves routing rules by scope and scope ID (from in-memory cache)
// Rules are already sorted by priority ASC (0 is highest priority)
func (gs *LocalGovernanceStore) GetScopedRoutingRules(ctx context.Context, scope string, scopeID string) []*configstoreTables.TableRoutingRule {
	// Build cache key: "scope:scopeID" (scopeID empty string for global)
	var key string
	if scope == "global" {
		key = "global:"
	} else {
		key = fmt.Sprintf("%s:%s", scope, scopeID)
	}

	// Load from in-memory sync.Map
	rules, ok := gs.routingRules.Load(key)
	if !ok {
		return nil
	}

	rulesList, ok := rules.([]*configstoreTables.TableRoutingRule)
	if !ok {
		return nil
	}

	// Filter by enabled and return
	var enabledRules []*configstoreTables.TableRoutingRule
	for _, rule := range rulesList {
		if rule.Enabled {
			enabledRules = append(enabledRules, rule)
		}
	}

	return enabledRules
}

// GetRoutingProgram compiles a CEL expression and caches the resulting program
// Uses the singleton CEL environment for efficiency
// Returns error if compilation fails
func (gs *LocalGovernanceStore) GetRoutingProgram(ctx context.Context, rule *configstoreTables.TableRoutingRule) (cel.Program, error) {
	if rule == nil {
		return nil, fmt.Errorf("routing rule cannot be nil")
	}

	// Check cache first to avoid recompilation
	if prog, ok := gs.compiledRoutingPrograms.Load(rule.ID); ok {
		if celProg, ok := prog.(cel.Program); ok {
			return celProg, nil
		}
	}

	// Get CEL expression, default to "true" if empty
	expr := rule.CelExpression
	if expr == "" {
		expr = "true"
	}

	// Normalize header and param keys to lowercase so CEL expressions match normalized map keys
	expr = routing.NormalizeMapKeysInCEL(expr)

	// Validate expression format
	if err := routing.ValidateCELExpression(expr); err != nil {
		return nil, fmt.Errorf("invalid CEL expression: %w", err)
	}

	// Compile using singleton environment
	ast, issues := gs.routingCELEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %s", issues.Err().Error())
	}

	// Create program
	program, err := gs.routingCELEnv.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program creation error: %w", err)
	}

	// Cache the compiled program
	gs.compiledRoutingPrograms.Store(rule.ID, program)

	return program, nil
}

// GetBudgetAndRateLimitStatus returns the current budget and rate limit status for provider and model combination
// Accounts for baseline usage from remote nodes when calculating percentages
func (gs *LocalGovernanceStore) GetBudgetAndRateLimitStatus(ctx context.Context, model string, provider schemas.ModelProvider, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus {
	// Prevent nil pointer dereferences
	if budgetBaselines == nil {
		budgetBaselines = map[string]float64{}
	}
	if tokenBaselines == nil {
		tokenBaselines = map[string]int64{}
	}
	if requestBaselines == nil {
		requestBaselines = map[string]int64{}
	}

	result := &BudgetAndRateLimitStatus{
		BudgetPercentUsed:           0,
		RateLimitTokenPercentUsed:   0,
		RateLimitRequestPercentUsed: 0,
	}

	// Check model-specific rate limits and budgets (takes precedence)
	if model != "" {
		// Check model+provider config first (most specific)
		key := fmt.Sprintf("%s:%s", model, string(provider))
		if modelValue, ok := gs.modelConfigs.Load(key); ok && modelValue != nil {
			if modelConfig, ok := modelValue.(*configstoreTables.TableModelConfig); ok && modelConfig != nil {
				// Get rate limit status
				if modelConfig.RateLimitID != nil {
					if rateLimitValue, ok := gs.rateLimits.Load(*modelConfig.RateLimitID); ok && rateLimitValue != nil {
						if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
							tokensBaseline, exists := tokenBaselines[rateLimit.ID]
							if !exists {
								tokensBaseline = 0
							}
							requestsBaseline, exists := requestBaselines[rateLimit.ID]
							if !exists {
								requestsBaseline = 0
							}
							// Calculate token percent used
							if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
								tokenPercent := float64(rateLimit.TokenCurrentUsage+tokensBaseline) / float64(*rateLimit.TokenMaxLimit) * 100
								if tokenPercent > result.RateLimitTokenPercentUsed {
									result.RateLimitTokenPercentUsed = tokenPercent
								}
							}
							// Calculate request percent used
							if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
								requestPercent := float64(rateLimit.RequestCurrentUsage+requestsBaseline) / float64(*rateLimit.RequestMaxLimit) * 100
								if requestPercent > result.RateLimitRequestPercentUsed {
									result.RateLimitRequestPercentUsed = requestPercent
								}
							}
						}
					}
				}
				// Get budget status
				if modelConfig.BudgetID != nil {
					if budgetValue, ok := gs.budgets.Load(*modelConfig.BudgetID); ok && budgetValue != nil {
						if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
							baseline, exists := budgetBaselines[budget.ID]
							if !exists {
								baseline = 0
							}
							if budget.MaxLimit > 0 {
								budgetPercent := float64(budget.CurrentUsage+baseline) / budget.MaxLimit * 100
								if budgetPercent > result.BudgetPercentUsed {
									result.BudgetPercentUsed = budgetPercent
								}
							}
						}
					}
				}
			}
		}

		// Fall back to model-only config (if exists)
		// Uses findModelOnlyConfig for cross-provider model name normalization
		if modelConfig, _ := gs.findModelOnlyConfig(ctx, model); modelConfig != nil {
			// Get rate limit status
			if modelConfig.RateLimitID != nil {
				if rateLimitValue, ok := gs.rateLimits.Load(*modelConfig.RateLimitID); ok && rateLimitValue != nil {
					if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
						// Calculate token percent used
						tokensBaseline, exists := tokenBaselines[rateLimit.ID]
						if !exists {
							tokensBaseline = 0
						}
						requestsBaseline, exists := requestBaselines[rateLimit.ID]
						if !exists {
							requestsBaseline = 0
						}
						if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
							tokenPercent := float64(rateLimit.TokenCurrentUsage+tokensBaseline) / float64(*rateLimit.TokenMaxLimit) * 100
							if tokenPercent > result.RateLimitTokenPercentUsed {
								result.RateLimitTokenPercentUsed = tokenPercent
							}
						}
						// Calculate request percent used
						if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
							requestPercent := float64(rateLimit.RequestCurrentUsage+requestsBaseline) / float64(*rateLimit.RequestMaxLimit) * 100
							if requestPercent > result.RateLimitRequestPercentUsed {
								result.RateLimitRequestPercentUsed = requestPercent
							}
						}
					}
				}
			}
			// Get budget status
			if modelConfig.BudgetID != nil {
				if budgetValue, ok := gs.budgets.Load(*modelConfig.BudgetID); ok && budgetValue != nil {
					if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
						baseline, exists := budgetBaselines[budget.ID]
						if !exists {
							baseline = 0
						}
						if budget.MaxLimit > 0 {
							budgetPercent := float64(budget.CurrentUsage+baseline) / budget.MaxLimit * 100
							if budgetPercent > result.BudgetPercentUsed {
								result.BudgetPercentUsed = budgetPercent
							}
						}
					}
				}
			}
		}
	}

	// Check global provider-specific rate limits and budgets
	providerValue, ok := gs.providers.Load(string(provider))
	if ok && providerValue != nil {
		if providerTable, ok := providerValue.(*configstoreTables.TableProvider); ok && providerTable != nil {
			// Get rate limit status
			if providerTable.RateLimitID != nil {
				if rateLimitValue, ok := gs.rateLimits.Load(*providerTable.RateLimitID); ok && rateLimitValue != nil {
					if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
						tokensBaseline, exists := tokenBaselines[rateLimit.ID]
						if !exists {
							tokensBaseline = 0
						}
						requestsBaseline, exists := requestBaselines[rateLimit.ID]
						if !exists {
							requestsBaseline = 0
						}
						// Calculate token percent used
						if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
							tokenPercent := float64(rateLimit.TokenCurrentUsage+tokensBaseline) / float64(*rateLimit.TokenMaxLimit) * 100
							if tokenPercent > result.RateLimitTokenPercentUsed {
								result.RateLimitTokenPercentUsed = tokenPercent
							}
						}
						// Calculate request percent used
						if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
							requestPercent := float64(rateLimit.RequestCurrentUsage+requestsBaseline) / float64(*rateLimit.RequestMaxLimit) * 100
							if requestPercent > result.RateLimitRequestPercentUsed {
								result.RateLimitRequestPercentUsed = requestPercent
							}
						}
					}
				}
			}
			// Get budget status
			if providerTable.BudgetID != nil {
				if budgetValue, ok := gs.budgets.Load(*providerTable.BudgetID); ok && budgetValue != nil {
					if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
						baseline, exists := budgetBaselines[budget.ID]
						if !exists {
							baseline = 0
						}
						if budget.MaxLimit > 0 {
							budgetPercent := float64(budget.CurrentUsage+baseline) / budget.MaxLimit * 100
							if budgetPercent > result.BudgetPercentUsed {
								result.BudgetPercentUsed = budgetPercent
							}
						}
					}
				}
			}
		}
	}

	// Check virtual key level provider-specific rate limits and budgets
	if vk != nil {
		if vk.ProviderConfigs != nil {
			for _, pc := range vk.ProviderConfigs {
				if pc.Provider == string(provider) {
					// Get rate limit status
					if pc.RateLimit != nil {
						// Look up canonical rate limit from gs.rateLimits
						if rateLimitValue, ok := gs.rateLimits.Load(pc.RateLimit.ID); ok && rateLimitValue != nil {
							if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
								tokensBaseline, exists := tokenBaselines[rateLimit.ID]
								if !exists {
									tokensBaseline = 0
								}
								requestsBaseline, exists := requestBaselines[rateLimit.ID]
								if !exists {
									requestsBaseline = 0
								}
								// Calculate token percent used
								if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
									tokenPercent := float64(rateLimit.TokenCurrentUsage+tokensBaseline) / float64(*rateLimit.TokenMaxLimit) * 100
									if tokenPercent > result.RateLimitTokenPercentUsed {
										result.RateLimitTokenPercentUsed = tokenPercent
									}
								}
								// Calculate request percent used
								if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
									requestPercent := float64(rateLimit.RequestCurrentUsage+requestsBaseline) / float64(*rateLimit.RequestMaxLimit) * 100
									if requestPercent > result.RateLimitRequestPercentUsed {
										result.RateLimitRequestPercentUsed = requestPercent
									}
								}
							}
						}
					}
					// Get budget status from multi-budgets
					for _, b := range pc.Budgets {
						if budgetValue, ok := gs.budgets.Load(b.ID); ok && budgetValue != nil {
							if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
								baseline, exists := budgetBaselines[budget.ID]
								if !exists {
									baseline = 0
								}
								if budget.MaxLimit > 0 {
									budgetPercent := float64(budget.CurrentUsage+baseline) / budget.MaxLimit * 100
									if budgetPercent > result.BudgetPercentUsed {
										result.BudgetPercentUsed = budgetPercent
									}
								}
							}
						}
					}
					break
				}
			}
		}
	}
	return result
}

// UpdateRoutingRuleInMemory updates a routing rule in the in-memory cache
func (gs *LocalGovernanceStore) UpdateRoutingRuleInMemory(ctx context.Context, rule *configstoreTables.TableRoutingRule) error {
	if rule == nil {
		return fmt.Errorf("routing rule cannot be nil")
	}
	// First, remove the rule from ALL scopes (in case it was moved from one scope to another)
	gs.routingRules.Range(func(key, value interface{}) bool {
		rules, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok {
			return true
		}

		// Filter out the rule if it exists in this scope
		newRules := make([]*configstoreTables.TableRoutingRule, 0, len(rules))
		for _, r := range rules {
			if r.ID != rule.ID {
				newRules = append(newRules, r)
			}
		}

		// Update the scope with the filtered rules
		if len(newRules) != len(rules) {
			if len(newRules) == 0 {
				gs.routingRules.Delete(key)
			} else {
				gs.routingRules.Store(key, newRules)
			}
		}
		return true
	})
	// Build cache key for the new scope
	var key string
	if rule.Scope == "global" {
		key = "global:"
	} else {
		scopeID := ""
		if rule.ScopeID != nil {
			scopeID = *rule.ScopeID
		}
		key = fmt.Sprintf("%s:%s", rule.Scope, scopeID)
	}
	// Load existing rules for this scope
	var rules []*configstoreTables.TableRoutingRule
	if value, ok := gs.routingRules.Load(key); ok {
		if existing, ok := value.([]*configstoreTables.TableRoutingRule); ok {
			rules = existing
		}
	}
	// Add the rule to the new scope
	rules = append(rules, rule)
	// Sort by priority ASC (0 is highest priority, higher numbers are lower priority)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})
	// Store back in cache
	gs.routingRules.Store(key, rules)
	// Invalidate compiled program cache for this rule (expression may have changed)
	gs.compiledRoutingPrograms.Delete(rule.ID)
	// Recompile the program immediately to update cache with fresh compilation
	if _, err := gs.GetRoutingProgram(ctx, rule); err != nil {
		gs.logger.Warn("Failed to recompile routing program for rule %s: %v", rule.Name, err)
	}
	return nil
}

// DeleteRoutingRuleInMemory removes a routing rule from the in-memory cache
func (gs *LocalGovernanceStore) DeleteRoutingRuleInMemory(ctx context.Context, id string) error {
	// Loop over all rules and delete the one with the matching id
	gs.routingRules.Range(func(key, value interface{}) bool {
		rules, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok {
			return true
		}
		// Find and filter out the rule with matching ID
		var filteredRules []*configstoreTables.TableRoutingRule
		for _, r := range rules {
			if r.ID != id {
				filteredRules = append(filteredRules, r)
			}
		}
		// Update or delete the key
		if len(filteredRules) == 0 {
			gs.routingRules.Delete(key)
		} else {
			gs.routingRules.Store(key, filteredRules)
		}
		return true
	})
	// Invalidate compiled program cache for this rule
	gs.compiledRoutingPrograms.Delete(id)
	return nil
}
