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
	"gorm.io/gorm"
)

// ModelMatcher provides cross-provider model name matching.
// This is satisfied by *modelcatalog.ModelCatalog.
type ModelMatcher interface {
	GetBaseModelName(model string) string
	IsSameModel(model1, model2 string) bool
}

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

	// Model matcher for cross-provider model matching (optional)
	modelMatcher ModelMatcher

	// Logger
	logger schemas.Logger
}

type GovernanceData struct {
	VirtualKeys  map[string]*configstoreTables.TableVirtualKey  `json:"virtual_keys"`
	Teams        map[string]*configstoreTables.TableTeam        `json:"teams"`
	Customers    map[string]*configstoreTables.TableCustomer    `json:"customers"`
	Budgets      map[string]*configstoreTables.TableBudget      `json:"budgets"`
	RateLimits   map[string]*configstoreTables.TableRateLimit   `json:"rate_limits"`
	RoutingRules map[string]*configstoreTables.TableRoutingRule `json:"routing_rules"`
	ModelConfigs []*configstoreTables.TableModelConfig          `json:"model_configs"`
	Providers    []*configstoreTables.TableProvider             `json:"providers"`
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
	GetGovernanceData() *GovernanceData
	GetVirtualKey(vkValue string) (*configstoreTables.TableVirtualKey, bool)
	// Provider-level governance checks
	CheckProviderBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) error
	CheckProviderRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (error, Decision)
	// Model-level governance checks
	CheckModelBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) error
	CheckModelRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (error, Decision)
	// VK-level governance checks
	CheckBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, baselines map[string]float64) error
	CheckRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
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
	CreateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey)
	UpdateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64)
	DeleteVirtualKeyInMemory(vkID string)
	CreateTeamInMemory(team *configstoreTables.TableTeam)
	UpdateTeamInMemory(team *configstoreTables.TableTeam, budgetBaselines map[string]float64)
	DeleteTeamInMemory(teamID string)
	CreateCustomerInMemory(customer *configstoreTables.TableCustomer)
	UpdateCustomerInMemory(customer *configstoreTables.TableCustomer, budgetBaselines map[string]float64)
	DeleteCustomerInMemory(customerID string)
	// Model config in-memory operations
	UpdateModelConfigInMemory(mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig
	DeleteModelConfigInMemory(mcID string)
	// Provider in-memory operations
	UpdateProviderInMemory(provider *configstoreTables.TableProvider) *configstoreTables.TableProvider
	DeleteProviderInMemory(providerName string)
	// Routing Rules CEL caching
	GetRoutingProgram(rule *configstoreTables.TableRoutingRule) (cel.Program, error)
	// Budget and rate limit status queries for routing with baseline support
	GetBudgetAndRateLimitStatus(ctx context.Context, model string, provider schemas.ModelProvider, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus
	// Routing Rules CRUD
	HasRoutingRules(ctx context.Context) bool
	GetAllRoutingRules() []*configstoreTables.TableRoutingRule
	GetScopedRoutingRules(scope string, scopeID string) []*configstoreTables.TableRoutingRule
	UpdateRoutingRuleInMemory(rule *configstoreTables.TableRoutingRule) error
	DeleteRoutingRuleInMemory(id string) error
}

// NewLocalGovernanceStore creates a new in-memory governance store
// The modelMatcher parameter is optional (can be nil) and enables cross-provider model matching
// for governance lookups (e.g., "openai/gpt-4o" matching config for "gpt-4o").
func NewLocalGovernanceStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore, governanceConfig *configstore.GovernanceConfig, modelMatcher ModelMatcher) (*LocalGovernanceStore, error) {
	// Create singleton CEL environment once for all routing rule compilations
	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	store := &LocalGovernanceStore{
		configStore:                    configStore,
		logger:                         logger,
		routingCELEnv:                  env,
		modelMatcher:                   modelMatcher,
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

func (gs *LocalGovernanceStore) GetGovernanceData() *GovernanceData {
	virtualKeys := make(map[string]*configstoreTables.TableVirtualKey)
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		// Cross-reference live budget/rate limit from standalone maps
		// (usage updates clone into budgets/rateLimits maps, so embedded pointers go stale)
		clone := *vk
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
		// Also fix embedded ProviderConfigs
		if len(clone.ProviderConfigs) > 0 {
			configs := make([]configstoreTables.TableVirtualKeyProviderConfig, len(clone.ProviderConfigs))
			copy(configs, clone.ProviderConfigs)
			for i := range configs {
				if configs[i].BudgetID != nil {
					if liveBudget, exists := gs.budgets.Load(*configs[i].BudgetID); exists && liveBudget != nil {
						if b, ok := liveBudget.(*configstoreTables.TableBudget); ok {
							configs[i].Budget = b
						}
					}
				}
				if configs[i].RateLimitID != nil {
					if liveRL, exists := gs.rateLimits.Load(*configs[i].RateLimitID); exists && liveRL != nil {
						if rl, ok := liveRL.(*configstoreTables.TableRateLimit); ok {
							configs[i].RateLimit = rl
						}
					}
				}
			}
			clone.ProviderConfigs = configs
		}
		virtualKeys[key.(string)] = &clone
		return true // continue iteration
	})
	teams := make(map[string]*configstoreTables.TableTeam)
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		teams[key.(string)] = team
		return true // continue iteration
	})
	customers := make(map[string]*configstoreTables.TableCustomer)
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		customers[key.(string)] = customer
		return true // continue iteration
	})
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
	gs.routingRules.Range(func(key, value interface{}) bool {
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
	gs.modelConfigs.Range(func(key, value interface{}) bool {
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
func (gs *LocalGovernanceStore) GetVirtualKey(vkValue string) (*configstoreTables.TableVirtualKey, bool) {
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

// CheckBudget performs budget checking using in-memory store data (lock-free for high performance)
func (gs *LocalGovernanceStore) CheckBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, baselines map[string]float64) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
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
	budgetsToCheck, budgetNames := gs.collectBudgetsFromHierarchy(vk, provider)

	gs.logger.Debug("LocalStore CheckBudget: Received %d baselines from remote nodes", len(baselines))
	for budgetID, baseline := range baselines {
		gs.logger.Debug("  - Baseline for budget %s: %.4f", budgetID, baseline)
	}

	// Check each budget in hierarchy order using in-memory data
	for i, budget := range budgetsToCheck {
		// Check if budget needs reset (in-memory check)
		if budget.ResetDuration != "" {
			if duration, err := configstoreTables.ParseDuration(budget.ResetDuration); err == nil {
				if time.Since(budget.LastReset) >= duration {
					// Budget expired but hasn't been reset yet - treat as reset
					// Note: actual reset will happen in post-hook via AtomicBudgetUpdate
					gs.logger.Debug("LocalStore CheckBudget: Budget %s (%s) expired, skipping check", budget.ID, budgetNames[i])
					continue // Skip budget check for expired budgets
				}
			}
		}

		baseline, exists := baselines[budget.ID]
		if !exists {
			baseline = 0
		}

		gs.logger.Debug("LocalStore CheckBudget: Checking %s budget %s: local=%.4f, remote=%.4f, total=%.4f, limit=%.4f",
			budgetNames[i], budget.ID, budget.CurrentUsage, baseline, budget.CurrentUsage+baseline, budget.MaxLimit)

		// Check if current usage (local + remote baseline) exceeds budget limit
		if budget.CurrentUsage+baseline >= budget.MaxLimit {
			gs.logger.Debug("LocalStore CheckBudget: Budget %s EXCEEDED", budget.ID)
			return fmt.Errorf("%s budget exceeded: %.4f >= %.4f dollars",
				budgetNames[i], budget.CurrentUsage+baseline, budget.MaxLimit)
		}
	}

	gs.logger.Debug("LocalStore CheckBudget: All budgets passed")

	return nil
}

// CheckProviderBudget performs budget checking for provider-level configs (lock-free for high performance)
func (gs *LocalGovernanceStore) CheckProviderBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) error {
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
		return nil
	}

	providerTable, ok := value.(*configstoreTables.TableProvider)
	if !ok || providerTable == nil || providerTable.BudgetID == nil {
		// No budget configured for provider, allow request
		return nil
	}

	// Read from budgets map to get the latest updated budget (same source as UpdateProviderBudgetUsage)
	budgetValue, exists := gs.budgets.Load(*providerTable.BudgetID)
	if !exists || budgetValue == nil {
		// Budget not found in cache, allow request
		return nil
	}

	budget, ok := budgetValue.(*configstoreTables.TableBudget)
	if !ok || budget == nil {
		// Invalid budget type, allow request
		return nil
	}

	// Check if budget needs reset (in-memory check)
	if budget.ResetDuration != "" {
		if duration, err := configstoreTables.ParseDuration(budget.ResetDuration); err == nil {
			if time.Since(budget.LastReset) >= duration {
				// Budget expired but hasn't been reset yet - treat as reset
				return nil // Skip budget check for expired budgets
			}
		}
	}

	baseline, exists := baselines[budget.ID]
	if !exists {
		baseline = 0
	}

	// Check if current usage (local + remote baseline) exceeds budget limit
	if budget.CurrentUsage+baseline >= budget.MaxLimit {
		return fmt.Errorf("%s budget exceeded: %.4f >= %.4f dollars",
			providerKey, budget.CurrentUsage+baseline, budget.MaxLimit)
	}

	return nil
}

// CheckProviderRateLimit checks provider-level rate limits and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckProviderRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (error, Decision) {
	var violations []string

	// This is to prevent nil pointer dereference
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
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
		return nil, DecisionAllow
	}

	providerTable, ok := value.(*configstoreTables.TableProvider)
	if !ok || providerTable == nil || providerTable.RateLimitID == nil {
		// No rate limit configured for provider, allow request
		return nil, DecisionAllow
	}

	// Read from rateLimits map to get the latest updated rate limit (same source as UpdateProviderRateLimitUsage)
	rateLimitValue, exists := gs.rateLimits.Load(*providerTable.RateLimitID)
	if !exists || rateLimitValue == nil {
		// Rate limit not found in cache, allow request
		return nil, DecisionAllow
	}

	rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit)
	if !ok || rateLimit == nil {
		// Invalid rate limit type, allow request
		return nil, DecisionAllow
	}

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
		return fmt.Errorf("rate limit violated for %s: %s", providerKey, violations), decision
	}

	return nil, DecisionAllow // No rate limit violations
}

// findModelOnlyConfig looks up a model-only config (no provider) with cross-provider model name normalization.
// Returns the matching config and the display name for error messages.
func (gs *LocalGovernanceStore) findModelOnlyConfig(model string) (*configstoreTables.TableModelConfig, string) {
	// If modelMatcher is available, try normalized base model name first (cross-provider matching)
	if gs.modelMatcher != nil {
		baseName := gs.modelMatcher.GetBaseModelName(model)
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
func (gs *LocalGovernanceStore) CheckModelBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) error {
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
	var modelConfigsToCheck []*configstoreTables.TableModelConfig
	var budgetNames []string

	// Check model+provider config first (more specific) - if provider is provided
	if provider != nil {
		key := fmt.Sprintf("%s:%s", model, string(*provider))
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil && mc.Budget != nil {
				modelConfigsToCheck = append(modelConfigsToCheck, mc)
				budgetNames = append(budgetNames, fmt.Sprintf("Model:%s:Provider:%s", model, string(*provider)))
			}
		}
	}

	// Always check model-only config (if exists) - regardless of whether model+provider config exists
	// Uses findModelOnlyConfig for cross-provider model name normalization
	if mc, configKey := gs.findModelOnlyConfig(model); mc != nil && mc.Budget != nil {
		modelConfigsToCheck = append(modelConfigsToCheck, mc)
		budgetNames = append(budgetNames, fmt.Sprintf("Model:%s", configKey))
	}

	// Check each model budget
	for i, mc := range modelConfigsToCheck {
		if mc.BudgetID == nil {
			continue
		}

		// Read from budgets map to get the latest updated budget (same source as UpdateModelBudgetUsage)
		budgetValue, exists := gs.budgets.Load(*mc.BudgetID)
		if !exists || budgetValue == nil {
			// Budget not found in cache, skip check
			continue
		}

		budget, ok := budgetValue.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			// Invalid budget type, skip check
			continue
		}

		// Check if budget needs reset (in-memory check)
		if budget.ResetDuration != "" {
			if duration, err := configstoreTables.ParseDuration(budget.ResetDuration); err == nil {
				if time.Since(budget.LastReset) >= duration {
					// Budget expired but hasn't been reset yet - treat as reset
					continue // Skip budget check for expired budgets
				}
			}
		}

		baseline, exists := baselines[budget.ID]
		if !exists {
			baseline = 0
		}

		// Check if current usage (local + remote baseline) exceeds budget limit
		if budget.CurrentUsage+baseline >= budget.MaxLimit {
			return fmt.Errorf("%s budget exceeded: %.4f >= %.4f dollars",
				budgetNames[i], budget.CurrentUsage+baseline, budget.MaxLimit)
		}
	}

	return nil
}

// CheckModelRateLimit checks model-level rate limits and returns evaluation result if violated
func (gs *LocalGovernanceStore) CheckModelRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (error, Decision) {
	var violations []string

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
	var modelConfigsToCheck []*configstoreTables.TableModelConfig
	var rateLimitNames []string

	// Check model+provider config first (more specific) - if provider is provided
	if provider != nil {
		key := fmt.Sprintf("%s:%s", model, string(*provider))
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil && mc.RateLimitID != nil {
				modelConfigsToCheck = append(modelConfigsToCheck, mc)
				rateLimitNames = append(rateLimitNames, fmt.Sprintf("Model:%s:Provider:%s", model, string(*provider)))
			}
		}
	}

	// Always check model-only config (if exists) - regardless of whether model+provider config exists
	// Uses findModelOnlyConfig for cross-provider model name normalization
	if mc, configKey := gs.findModelOnlyConfig(model); mc != nil && mc.RateLimitID != nil {
		modelConfigsToCheck = append(modelConfigsToCheck, mc)
		rateLimitNames = append(rateLimitNames, fmt.Sprintf("Model:%s", configKey))
	}

	// Check each model rate limit
	for i, mc := range modelConfigsToCheck {
		if mc.RateLimitID == nil {
			continue
		}

		// Read from rateLimits map to get the latest updated rate limit (same source as UpdateModelRateLimitUsage)
		rateLimitValue, exists := gs.rateLimits.Load(*mc.RateLimitID)
		if !exists || rateLimitValue == nil {
			// Rate limit not found in cache, skip check
			continue
		}

		rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			// Invalid rate limit type, skip check
			continue
		}

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
			return fmt.Errorf("rate limit violated for %s: %s", rateLimitNames[i], violations), decision
		}
	}

	return nil, DecisionAllow // No rate limit violations
}

// CheckRateLimit checks a single rate limit and returns evaluation result if violated (true if violated, false if not)
func (gs *LocalGovernanceStore) CheckRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	var violations []string

	// Extract provider from request
	var provider schemas.ModelProvider
	if request != nil {
		provider = request.Provider
	}

	// Collect rate limits and their names from the hierarchy
	rateLimits, rateLimitNames := gs.collectRateLimitsFromHierarchy(vk, provider)

	// This is to prevent nil pointer dereference
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}

	for i, rateLimit := range rateLimits {
		// Determine token and request expiration independently
		tokenExpired := false
		requestExpired := false

		// Check if token reset duration is expired
		if rateLimit.TokenResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
				if time.Since(rateLimit.TokenLastReset) >= duration {
					// Token rate limit expired but hasn't been reset yet - skip token checks
					// Note: actual reset will happen in post-hook via AtomicRateLimitUpdate
					tokenExpired = true
				}
			}
		}

		// Check if request reset duration is expired
		if rateLimit.RequestResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
				if time.Since(rateLimit.RequestLastReset) >= duration {
					// Request rate limit expired but hasn't been reset yet - skip request checks
					// Note: actual reset will happen in post-hook via AtomicRateLimitUpdate
					requestExpired = true
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
		// Only check if token limit is not expired
		if !tokenExpired && rateLimit.TokenMaxLimit != nil && rateLimit.TokenCurrentUsage+tokensBaseline >= *rateLimit.TokenMaxLimit {
			duration := "unknown"
			if rateLimit.TokenResetDuration != nil {
				duration = *rateLimit.TokenResetDuration
			}
			violations = append(violations, fmt.Sprintf("token limit exceeded (%d/%d, resets every %s)",
				rateLimit.TokenCurrentUsage+tokensBaseline, *rateLimit.TokenMaxLimit, duration))
		}

		// Request limits - check if total usage (local + remote baseline) exceeds limit
		// Only check if request limit is not expired
		if !requestExpired && rateLimit.RequestMaxLimit != nil && rateLimit.RequestCurrentUsage+requestsBaseline >= *rateLimit.RequestMaxLimit {
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
			msg := strings.Join(violations, "; ")
			return decision, fmt.Errorf("rate limit violated for %s: %s", rateLimitNames[i], msg)
		}
	}

	return DecisionAllow, nil // No rate limit violations
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
	if mc, _ := gs.findModelOnlyConfig(model); mc != nil && mc.BudgetID != nil {
		updateBudget(*mc.BudgetID)
	}

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
	if mc, _ := gs.findModelOnlyConfig(model); mc != nil && mc.RateLimitID != nil {
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
	rateLimitIDs := gs.collectRateLimitIDsFromMemory(vk, provider)
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

// ResetExpiredBudgetsInMemory checks and resets budgets that have exceeded their reset duration (lock-free)
func (gs *LocalGovernanceStore) ResetExpiredBudgetsInMemory(ctx context.Context) []*configstoreTables.TableBudget {
	now := time.Now()
	var resetBudgets []*configstoreTables.TableBudget

	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return true // continue
		}

		duration, err := configstoreTables.ParseDuration(budget.ResetDuration)
		if err != nil {
			gs.logger.Error("invalid budget reset duration %s: %v", budget.ResetDuration, err)
			return true // continue
		}

		if now.Sub(budget.LastReset) >= duration {
			// Create a copy to avoid data race (sync.Map is concurrent-safe for reads/writes but not mutations)
			copiedBudget := *budget
			oldUsage := copiedBudget.CurrentUsage
			copiedBudget.CurrentUsage = 0
			copiedBudget.LastReset = now
			gs.LastDBUsagesBudgetsMu.Lock()
			gs.LastDBUsagesBudgets[copiedBudget.ID] = 0
			gs.LastDBUsagesBudgetsMu.Unlock()

			// Atomically replace the entry using the original key
			gs.budgets.Store(key, &copiedBudget)
			resetBudgets = append(resetBudgets, &copiedBudget)

			// Update all VKs, teams, customers, and provider configs that reference this budget
			gs.updateBudgetReferences(&copiedBudget)

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

	gs.rateLimits.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true // continue
		}

		needsReset := false
		// Check if token reset is needed
		if rateLimit.TokenResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
				if now.Sub(rateLimit.TokenLastReset) >= duration {
					needsReset = true
				}
			}
		}
		// Check if request reset is needed
		if rateLimit.RequestResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
				if now.Sub(rateLimit.RequestLastReset) >= duration {
					needsReset = true
				}
			}
		}

		if needsReset {
			// Create a copy to avoid data race (sync.Map is concurrent-safe for reads/writes but not mutations)
			copiedRateLimit := *rateLimit

			// Reset token limits if expired
			if copiedRateLimit.TokenResetDuration != nil {
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
			if copiedRateLimit.RequestResetDuration != nil {
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

			// Atomically replace the entry using the original key
			gs.rateLimits.Store(key, &copiedRateLimit)
			resetRateLimits = append(resetRateLimits, &copiedRateLimit)

			// Update all VKs and provider configs that reference this rate limit
			gs.updateRateLimitReferences(&copiedRateLimit)
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

		for i := range budgets {
			if vk.BudgetID != nil && budgets[i].ID == *vk.BudgetID {
				vk.Budget = &budgets[i]
			}
		}

		for i := range rateLimits {
			if vk.RateLimitID != nil && rateLimits[i].ID == *vk.RateLimitID {
				vk.RateLimit = &rateLimits[i]
			}
		}

		// Populate provider config relationships with budgets and rate limits
		if vk.ProviderConfigs != nil {
			for j := range vk.ProviderConfigs {
				pc := &vk.ProviderConfigs[j]

				// Populate budget
				if pc.BudgetID != nil {
					for k := range budgets {
						if budgets[k].ID == *pc.BudgetID {
							pc.Budget = &budgets[k]
							break
						}
					}
				}

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
			if gs.modelMatcher != nil {
				key = gs.modelMatcher.GetBaseModelName(mc.ModelName)
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
				if _, err := gs.GetRoutingProgram(rule); err != nil {
					gs.logger.Warn("Failed to pre-compile routing program for rule %s: %v", rule.ID, err)
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

// UTILITY FUNCTIONS

// collectRateLimitsFromHierarchy collects rate limits and their metadata from the hierarchy (Provider Configs → VK)
func (gs *LocalGovernanceStore) collectRateLimitsFromHierarchy(vk *configstoreTables.TableVirtualKey, requestedProvider schemas.ModelProvider) ([]*configstoreTables.TableRateLimit, []string) {
	if vk == nil {
		return nil, nil
	}

	var rateLimits []*configstoreTables.TableRateLimit
	var rateLimitNames []string

	for _, pc := range vk.ProviderConfigs {
		if pc.RateLimitID != nil && pc.Provider == string(requestedProvider) {
			if rateLimitValue, exists := gs.rateLimits.Load(*pc.RateLimitID); exists && rateLimitValue != nil {
				if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
					rateLimits = append(rateLimits, rateLimit)
					rateLimitNames = append(rateLimitNames, pc.Provider)
				}
			}
		}
	}

	if vk.RateLimitID != nil {
		if rateLimitValue, exists := gs.rateLimits.Load(*vk.RateLimitID); exists && rateLimitValue != nil {
			if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
				rateLimits = append(rateLimits, rateLimit)
				rateLimitNames = append(rateLimitNames, "VK")
			}
		}
	}

	return rateLimits, rateLimitNames
}

// collectBudgetsFromHierarchy collects budgets and their metadata from the hierarchy (Provider Configs → VK → Team → Customer)
func (gs *LocalGovernanceStore) collectBudgetsFromHierarchy(vk *configstoreTables.TableVirtualKey, requestedProvider schemas.ModelProvider) ([]*configstoreTables.TableBudget, []string) {
	if vk == nil {
		return nil, nil
	}

	var budgets []*configstoreTables.TableBudget
	var budgetNames []string

	// Collect all budgets in hierarchy order using lock-free sync.Map access (Provider Configs → VK → Team → Customer)
	for _, pc := range vk.ProviderConfigs {
		if pc.BudgetID != nil && pc.Provider == string(requestedProvider) {
			if budgetValue, exists := gs.budgets.Load(*pc.BudgetID); exists && budgetValue != nil {
				if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
					budgets = append(budgets, budget)
					budgetNames = append(budgetNames, pc.Provider)
				}
			}
		}
	}

	if vk.BudgetID != nil {
		if budgetValue, exists := gs.budgets.Load(*vk.BudgetID); exists && budgetValue != nil {
			if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
				budgets = append(budgets, budget)
				budgetNames = append(budgetNames, "VK")
			}
		}
	}

	if vk.TeamID != nil {
		if teamValue, exists := gs.teams.Load(*vk.TeamID); exists && teamValue != nil {
			if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
				if team.BudgetID != nil {
					if budgetValue, exists := gs.budgets.Load(*team.BudgetID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
							budgets = append(budgets, budget)
							budgetNames = append(budgetNames, "Team")
						}
					}
				}

				// Check if team belongs to a customer
				if team.CustomerID != nil {
					if customerValue, exists := gs.customers.Load(*team.CustomerID); exists && customerValue != nil {
						if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
							if customer.BudgetID != nil {
								if budgetValue, exists := gs.budgets.Load(*customer.BudgetID); exists && budgetValue != nil {
									if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
										budgets = append(budgets, budget)
										budgetNames = append(budgetNames, "Customer")
									}
								}
							}
						}
					}
				}
			}
		}
	}

	if vk.CustomerID != nil {
		if customerValue, exists := gs.customers.Load(*vk.CustomerID); exists && customerValue != nil {
			if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
				if customer.BudgetID != nil {
					if budgetValue, exists := gs.budgets.Load(*customer.BudgetID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
							budgets = append(budgets, budget)
							budgetNames = append(budgetNames, "Customer")
						}
					}
				}
			}
		}
	}

	return budgets, budgetNames
}

// collectBudgetIDsFromMemory collects budget IDs from in-memory store data (lock-free)
func (gs *LocalGovernanceStore) collectBudgetIDsFromMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider) []string {
	budgets, _ := gs.collectBudgetsFromHierarchy(vk, provider)

	budgetIDs := make([]string, len(budgets))
	for i, budget := range budgets {
		budgetIDs[i] = budget.ID
	}

	return budgetIDs
}

// collectRateLimitIDsFromMemory collects rate limit IDs from in-memory store data (lock-free)
func (gs *LocalGovernanceStore) collectRateLimitIDsFromMemory(vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider) []string {
	rateLimits, _ := gs.collectRateLimitsFromHierarchy(vk, provider)

	rateLimitIDs := make([]string, len(rateLimits))
	for i, rateLimit := range rateLimits {
		rateLimitIDs[i] = rateLimit.ID
	}

	return rateLimitIDs
}

// PUBLIC API METHODS

// CreateVirtualKeyInMemory adds a new virtual key to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey) {
	if vk == nil {
		return // Nothing to create
	}

	// Create associated budget if exists
	if vk.Budget != nil {
		gs.budgets.Store(vk.Budget.ID, vk.Budget)
	}

	// Create associated rate limit if exists
	if vk.RateLimit != nil {
		gs.rateLimits.Store(vk.RateLimit.ID, vk.RateLimit)
	}

	// Create provider config budgets and rate limits if they exist
	if vk.ProviderConfigs != nil {
		for _, pc := range vk.ProviderConfigs {
			if pc.Budget != nil {
				gs.budgets.Store(pc.Budget.ID, pc.Budget)
			}
			if pc.RateLimit != nil {
				gs.rateLimits.Store(pc.RateLimit.ID, pc.RateLimit)
			}
		}
	}

	gs.virtualKeys.Store(vk.Value, vk)
}

// UpdateVirtualKeyInMemory updates an existing virtual key in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64) {
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
		// Update Budget for VK in memory store
		if clone.Budget != nil {
			// Preserve existing usage from memory when updating budget config
			// The usage tracker maintains current usage in memory, and we only want to update
			// the configuration fields (max_limit, reset_duration) from the database
			if existingBudgetValue, exists := gs.budgets.Load(clone.Budget.ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					// Preserve current usage and last reset time from existing in-memory budget
					clone.Budget.CurrentUsage = existingBudget.CurrentUsage
					clone.Budget.LastReset = existingBudget.LastReset
				}
			}
			gs.budgets.Store(clone.Budget.ID, clone.Budget)
		} else if existingVK.Budget != nil {
			// Budget was removed from the virtual key, delete it from memory
			gs.budgets.Delete(existingVK.Budget.ID)
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
				// Update Budget for provider config in memory store
				if pc.Budget != nil {
					// Preserve existing usage from memory when updating provider config budget
					if existingBudgetValue, exists := gs.budgets.Load(pc.Budget.ID); exists && existingBudgetValue != nil {
						if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
							// Preserve current usage and last reset time from existing in-memory budget
							clone.ProviderConfigs[i].Budget.CurrentUsage = existingBudget.CurrentUsage
							clone.ProviderConfigs[i].Budget.LastReset = existingBudget.LastReset
						}
					}
					gs.budgets.Store(clone.ProviderConfigs[i].Budget.ID, clone.ProviderConfigs[i].Budget)
				} else {
					// Budget was removed from provider config, delete it from memory if it existed
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.Budget != nil {
						gs.budgets.Delete(existingPC.Budget.ID)
						clone.ProviderConfigs[i].Budget = nil
					}
				}
			}
		}
		gs.virtualKeys.Store(vk.Value, &clone)
	} else {
		gs.CreateVirtualKeyInMemory(vk)
	}
}

// DeleteVirtualKeyInMemory removes a virtual key from the in-memory store
func (gs *LocalGovernanceStore) DeleteVirtualKeyInMemory(vkID string) {
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
			// Delete associated budget if exists
			if vk.BudgetID != nil {
				gs.budgets.Delete(*vk.BudgetID)
			}

			// Delete associated rate limit if exists
			if vk.RateLimitID != nil {
				gs.rateLimits.Delete(*vk.RateLimitID)
			}

			// Delete provider config budgets and rate limits
			if vk.ProviderConfigs != nil {
				for _, pc := range vk.ProviderConfigs {
					if pc.BudgetID != nil {
						gs.budgets.Delete(*pc.BudgetID)
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
func (gs *LocalGovernanceStore) CreateTeamInMemory(team *configstoreTables.TableTeam) {
	if team == nil {
		return // Nothing to create
	}

	// Create associated budget if exists
	if team.Budget != nil {
		gs.budgets.Store(team.Budget.ID, team.Budget)
	}

	gs.teams.Store(team.ID, team)
}

// UpdateTeamInMemory updates an existing team in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateTeamInMemory(team *configstoreTables.TableTeam, budgetBaselines map[string]float64) {
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

		// Handle budget updates with consistent logic
		if clone.Budget != nil {
			// Preserve existing usage from memory when updating team budget config
			if existingBudgetValue, exists := gs.budgets.Load(clone.Budget.ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					// Preserve current usage and last reset time from existing in-memory budget
					clone.Budget.CurrentUsage = existingBudget.CurrentUsage
					clone.Budget.LastReset = existingBudget.LastReset
				}
			}
			gs.budgets.Store(clone.Budget.ID, clone.Budget)
		} else if existingTeam.Budget != nil {
			// Budget was removed from the team, delete it from memory
			gs.budgets.Delete(existingTeam.Budget.ID)
		}

		gs.teams.Store(team.ID, &clone)
	} else {
		gs.CreateTeamInMemory(team)
	}
}

// DeleteTeamInMemory removes a team from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteTeamInMemory(teamID string) {
	if teamID == "" {
		return // Nothing to delete
	}

	// Get team to check for associated budget
	if teamValue, exists := gs.teams.Load(teamID); exists && teamValue != nil {
		if team, ok := teamValue.(*configstoreTables.TableTeam); ok && team != nil {
			// Delete associated budget if exists
			if team.BudgetID != nil {
				gs.budgets.Delete(*team.BudgetID)
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
func (gs *LocalGovernanceStore) CreateCustomerInMemory(customer *configstoreTables.TableCustomer) {
	if customer == nil {
		return // Nothing to create
	}

	// Create associated budget if exists
	if customer.Budget != nil {
		gs.budgets.Store(customer.Budget.ID, customer.Budget)
	}

	gs.customers.Store(customer.ID, customer)
}

// UpdateCustomerInMemory updates an existing customer in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateCustomerInMemory(customer *configstoreTables.TableCustomer, budgetBaselines map[string]float64) {
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

		gs.customers.Store(customer.ID, &clone)
	} else {
		gs.CreateCustomerInMemory(customer)
	}
}

// DeleteCustomerInMemory removes a customer from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteCustomerInMemory(customerID string) {
	if customerID == "" {
		return // Nothing to delete
	}

	// Get customer to check for associated budget
	if customerValue, exists := gs.customers.Load(customerID); exists && customerValue != nil {
		if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
			// Delete associated budget if exists
			if customer.BudgetID != nil {
				gs.budgets.Delete(*customer.BudgetID)
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

// UpdateModelConfigInMemory adds or updates a model config in the in-memory store (lock-free)
// Preserves existing usage values when updating budgets and rate limits
// Returns the updated model config with potentially modified usage values
func (gs *LocalGovernanceStore) UpdateModelConfigInMemory(mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig {
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
		if gs.modelMatcher != nil {
			key = gs.modelMatcher.GetBaseModelName(clone.ModelName)
		}
		gs.modelConfigs.Store(key, &clone)
	}

	return &clone
}

// DeleteModelConfigInMemory removes a model config from the in-memory store (lock-free)
func (gs *LocalGovernanceStore) DeleteModelConfigInMemory(mcID string) {
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
func (gs *LocalGovernanceStore) UpdateProviderInMemory(provider *configstoreTables.TableProvider) *configstoreTables.TableProvider {
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
func (gs *LocalGovernanceStore) DeleteProviderInMemory(providerName string) {
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
func (gs *LocalGovernanceStore) updateBudgetReferences(resetBudget *configstoreTables.TableBudget) {
	budgetID := resetBudget.ID
	// Update VKs that reference this budget
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		needsUpdate := false
		clone := *vk

		// Check VK-level budget
		if vk.BudgetID != nil && *vk.BudgetID == budgetID {
			clone.Budget = resetBudget
			needsUpdate = true
		}

		// Check provider config budgets
		if vk.ProviderConfigs != nil {
			for i, pc := range clone.ProviderConfigs {
				if pc.BudgetID != nil && *pc.BudgetID == budgetID {
					clone.ProviderConfigs[i].Budget = resetBudget
					needsUpdate = true
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
		if team.BudgetID != nil && *team.BudgetID == budgetID {
			clone := *team
			clone.Budget = resetBudget
			gs.teams.Store(key, &clone)
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

// updateRateLimitReferences updates all VKs and provider configs that reference a reset rate limit
func (gs *LocalGovernanceStore) updateRateLimitReferences(resetRateLimit *configstoreTables.TableRateLimit) {
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
func (gs *LocalGovernanceStore) GetAllRoutingRules() []*configstoreTables.TableRoutingRule {
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
func (gs *LocalGovernanceStore) GetScopedRoutingRules(scope string, scopeID string) []*configstoreTables.TableRoutingRule {
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
func (gs *LocalGovernanceStore) GetRoutingProgram(rule *configstoreTables.TableRoutingRule) (cel.Program, error) {
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

	// Validate expression format
	if err := validateCELExpression(expr); err != nil {
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
		if modelConfig, _ := gs.findModelOnlyConfig(model); modelConfig != nil {
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
					// Get budget status
					if pc.BudgetID != nil {
						if budgetValue, ok := gs.budgets.Load(*pc.BudgetID); ok && budgetValue != nil {
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
func (gs *LocalGovernanceStore) UpdateRoutingRuleInMemory(rule *configstoreTables.TableRoutingRule) error {
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
	if _, err := gs.GetRoutingProgram(rule); err != nil {
		gs.logger.Warn("Failed to recompile routing program for rule %s: %v", rule.ID, err)
	}

	return nil
}

// DeleteRoutingRuleInMemory removes a routing rule from the in-memory cache
func (gs *LocalGovernanceStore) DeleteRoutingRuleInMemory(id string) error {
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
