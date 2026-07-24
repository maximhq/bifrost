// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"context"
	"errors"
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
	virtualKeys sync.Map // string -> *VirtualKey (VK value -> VirtualKey with preloaded relationships)
	// virtualKeysByID is a secondary index over virtualKeys keyed by VK row ID,
	// giving O(1) by-ID lookups (e.g. the /mcp JWT auth path) without an O(n)
	// scan or a database read. Maintained in lock-step with virtualKeys via
	// storeVirtualKey / deleteVirtualKeyByValue — never write it directly.
	virtualKeysByID sync.Map // string -> *VirtualKey (VK row ID -> VirtualKey)
	teams           sync.Map // string -> *Team (Team ID -> Team)
	customers       sync.Map // string -> *Customer (Customer ID -> Customer)
	budgets         sync.Map // string -> *Budget (Budget ID -> Budget)
	rateLimits      sync.Map // string -> *RateLimit (RateLimit ID -> RateLimit)
	modelConfigs    sync.Map // string -> *ModelConfig (key: "modelName" or "modelName:provider" -> ModelConfig)
	providers       sync.Map // string -> *Provider (Provider name -> Provider with preloaded relationships)
	routingRules    sync.Map // string -> []*TableRoutingRule (key: "scope:scopeID" -> rules, scopeID="" for global)

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

	// Reset hooks allow wrappers to observe request-time local resets.
	resetHooksMu      sync.RWMutex
	onBudgetsReset    func([]*configstoreTables.TableBudget)
	onRateLimitsReset func([]*configstoreTables.TableRateLimit)

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
	// Budget crud.
	// UpsertBudgetConfig preserves in-memory CurrentUsage/LastReset on replacement —
	// use it for every config publish (fresh load or admin edit) so a concurrent
	// BumpBudgetUsage increment is never clobbered.
	LoadBudget(ctx context.Context, budgetID string) *configstoreTables.TableBudget
	UpsertBudgetConfig(ctx context.Context, budgetID string, config *configstoreTables.TableBudget)
	DeleteBudget(ctx context.Context, budgetID string)
	// Rate limit crud. UpsertRateLimitConfig carries in-memory counter state
	// (token + request CurrentUsage/LastReset) forward across replacements —
	// same rationale as UpsertBudgetConfig.
	LoadRateLimit(ctx context.Context, rateLimitID string) *configstoreTables.TableRateLimit
	UpsertRateLimitConfig(ctx context.Context, rateLimitID string, config *configstoreTables.TableRateLimit)
	DeleteRateLimit(ctx context.Context, rateLimitID string)
	// Provider-level governance checks
	CheckProviderBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckProviderRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// Model-level governance checks
	CheckModelBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckModelRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// Scoped model-level governance checks (aggregate with the global model checks above).
	// scope/scopeID identify the owning entity (e.g. "virtual_key" + VK.ID, or any other
	// scope registered via tables.RegisterModelConfigScope). An empty scope or scopeID
	// is a no-op (returns DecisionAllow).
	CheckScopedModelBudget(ctx context.Context, scope, scopeID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckScopedModelRateLimit(ctx context.Context, scope, scopeID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// VK-level governance checks
	CheckVirtualKeyBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckVirtualKeyRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// In-memory usage updates (for VK-level)
	UpdateVirtualKeyBudgetUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, cost float64) error
	UpdateVirtualKeyRateLimitUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// In-memory usage updates for scoped model configs (mirror the global model updates).
	// scope/scopeID identify the owning entity; an empty scope or scopeID is a no-op.
	UpdateScopedModelBudgetUsageInMemory(ctx context.Context, scope, scopeID, model string, provider schemas.ModelProvider, cost float64) error
	UpdateScopedModelRateLimitUsageInMemory(ctx context.Context, scope, scopeID, model string, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// In-memory reset checks (return items that need DB sync)
	ResetExpiredRateLimitsInMemory(ctx context.Context, refreshReferences bool, rateLimitIDs ...string) []*configstoreTables.TableRateLimit
	ResetExpiredBudgetsInMemory(ctx context.Context, refreshReferences bool, budgetIDs ...string) []*configstoreTables.TableBudget
	// DB sync for expired items
	ResetExpiredRateLimits(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) error
	ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error
	// Manual/admin reset of a single budget; returns ErrBudgetNotFound for unknown IDs.
	ResetBudget(ctx context.Context, budgetID string) (*configstoreTables.TableBudget, error)
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
	// Team-level live budget/rate-limit collectors (resolved from the hot maps);
	// used by the enterprise user→team→business-unit hierarchy collector.
	CollectTeamBudgets(ctx context.Context, teamID string) []*configstoreTables.TableBudget
	CollectTeamRateLimits(ctx context.Context, teamID string) []*configstoreTables.TableRateLimit
	// Customer-level governance checks
	CheckCustomerBudget(ctx context.Context, customerID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckCustomerRateLimit(ctx context.Context, customerID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	// User governance in-memory operations (enterprise-only, but interface defined here for compatibility)
	GetUserGovernance(ctx context.Context, userID string) (*UserGovernance, bool)
	CreateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	UpdateUserGovernanceInMemory(ctx context.Context, userID string, budget *configstoreTables.TableBudget, rateLimit *configstoreTables.TableRateLimit)
	DeleteUserGovernanceInMemory(ctx context.Context, userID string)
	CreateUserNameInMemory(ctx context.Context, userID string, userName string)
	// User-level governance checks (enterprise-only)
	CheckUserBudget(ctx context.Context, userID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error)
	CheckUserRateLimit(ctx context.Context, userID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error)
	UpdateUserBudgetUsageInMemory(ctx context.Context, userID string, cost float64) error
	UpdateUserRateLimitUsageInMemory(ctx context.Context, userID string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	// Model config in-memory operations
	UpdateModelConfigInMemory(ctx context.Context, mc *configstoreTables.TableModelConfig) *configstoreTables.TableModelConfig
	DeleteModelConfigInMemory(ctx context.Context, mcID string)
	ScopedModelConfigIDs(scope, scopeID string) []string
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
	// CollectApplicableGovernanceIDs returns every budget and rate-limit ID this node charges for the given (virtualKey, userID, provider, model).
	// The IDs are stamped on the log row so ghost-node reconciliation can re-attribute cost and tokens;
	// missing any ID here means that usage vanishes from cluster baselines when the node ghosts.
	// userID contributes the user-scoped model-config IDs;
	CollectApplicableGovernanceIDs(ctx context.Context, virtualKey string, userID string, provider schemas.ModelProvider, model string) (budgetIDs []string, rateLimitIDs []string)
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

// SetResetHooks configures callbacks that run after successful local resets.
func (gs *LocalGovernanceStore) SetResetHooks(onBudgetsReset func([]*configstoreTables.TableBudget), onRateLimitsReset func([]*configstoreTables.TableRateLimit)) {
	gs.resetHooksMu.Lock()
	defer gs.resetHooksMu.Unlock()
	gs.onBudgetsReset = onBudgetsReset
	gs.onRateLimitsReset = onRateLimitsReset
}

func (gs *LocalGovernanceStore) getBudgetsResetHook() func([]*configstoreTables.TableBudget) {
	gs.resetHooksMu.RLock()
	defer gs.resetHooksMu.RUnlock()
	return gs.onBudgetsReset
}

func (gs *LocalGovernanceStore) getRateLimitsResetHook() func([]*configstoreTables.TableRateLimit) {
	gs.resetHooksMu.RLock()
	defer gs.resetHooksMu.RUnlock()
	return gs.onRateLimitsReset
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

// UpsertBudgetConfig publishes a budget config under budgetID, preserving the
// in-memory CurrentUsage and LastReset from any prior snapshot so a concurrent
// BumpBudgetUsage or ResetBudgetAt is never clobbered by a config replacement.
// First-writes (no prior entry) are handled via sync.Map.LoadOrStore so
// simultaneous first-writers collapse to a single insertion and the late
// arrival re-enters the CAS loop against the winner's snapshot.
//
// This method replaces the former blind StoreBudget: every caller installing
// a budget — whether fresh load or config replacement — should funnel through
// here so counters are never clobbered by an admin edit racing with a usage
// increment.
func (gs *LocalGovernanceStore) UpsertBudgetConfig(ctx context.Context, budgetID string, config *configstoreTables.TableBudget) {
	if config == nil {
		return
	}
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists {
			if _, loaded := gs.budgets.LoadOrStore(budgetID, config); !loaded {
				return
			}
			continue
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			gs.budgets.Store(budgetID, config)
			return
		}
		merged := *config
		merged.CurrentUsage = old.CurrentUsage
		merged.LastReset = old.LastReset
		if gs.budgets.CompareAndSwap(budgetID, raw, &merged) {
			return
		}
	}
}

// DeleteBudget deletes a budget from the local store.
func (gs *LocalGovernanceStore) DeleteBudget(ctx context.Context, budgetID string) {
	gs.budgets.Delete(budgetID)
	// Clean up LastDB baselines so the gossip delta doesn't carry stale entries.
	gs.LastDBUsagesBudgetsMu.Lock()
	delete(gs.LastDBUsagesBudgets, budgetID)
	gs.LastDBUsagesBudgetsMu.Unlock()
}

// SetBudgetDBBaseline records the DB-authoritative usage for a budget so that
// gossip delta calculations (CurrentUsage - LastDBUsage) start from the correct
// base. Must be called whenever a budget with non-zero usage is loaded into
// memory outside of the initial loadFromDatabase path (e.g., access-profile
// propagation that preserves usage under a new ID).
func (gs *LocalGovernanceStore) SetBudgetDBBaseline(budgetID string, currentUsage float64) {
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets[budgetID] = currentUsage
	gs.LastDBUsagesBudgetsMu.Unlock()
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

// UpsertRateLimitConfig publishes a rate-limit config under rateLimitID,
// preserving in-memory token and request counter state (TokenCurrentUsage /
// TokenLastReset / RequestCurrentUsage / RequestLastReset) from any prior
// snapshot. Same CAS-retry contract as UpsertBudgetConfig.
func (gs *LocalGovernanceStore) UpsertRateLimitConfig(ctx context.Context, rateLimitID string, config *configstoreTables.TableRateLimit) {
	if config == nil {
		return
	}
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists {
			if _, loaded := gs.rateLimits.LoadOrStore(rateLimitID, config); !loaded {
				return
			}
			continue
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			gs.rateLimits.Store(rateLimitID, config)
			return
		}
		merged := *config
		merged.TokenCurrentUsage = old.TokenCurrentUsage
		merged.TokenLastReset = old.TokenLastReset
		merged.RequestCurrentUsage = old.RequestCurrentUsage
		merged.RequestLastReset = old.RequestLastReset
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &merged) {
			return
		}
	}
}

// DeleteRateLimit deletes a rate limit from the local store.
func (gs *LocalGovernanceStore) DeleteRateLimit(ctx context.Context, rateLimitID string) {
	gs.rateLimits.Delete(rateLimitID)
	// Clean up LastDB baselines so the gossip delta doesn't carry stale entries.
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	delete(gs.LastDBUsagesTokensRateLimits, rateLimitID)
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	delete(gs.LastDBUsagesRequestsRateLimits, rateLimitID)
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
}

// SetRateLimitDBBaseline records the DB-authoritative usage for a rate limit so
// that gossip delta calculations (TokenCurrentUsage - LastDBTokenUsage) start
// from the correct base. Must be called whenever a rate limit with non-zero
// usage is loaded into memory outside of the initial loadFromDatabase path
// (e.g., access-profile propagation that preserves usage under a new ID).
func (gs *LocalGovernanceStore) SetRateLimitDBBaseline(rateLimitID string, tokenUsage int64, requestUsage int64) {
	gs.LastDBUsagesRateLimitsTokensMu.Lock()
	gs.LastDBUsagesTokensRateLimits[rateLimitID] = tokenUsage
	gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	gs.LastDBUsagesRateLimitsRequestsMu.Lock()
	gs.LastDBUsagesRequestsRateLimits[rateLimitID] = requestUsage
	gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
}

// maxRequestTimeResetAttempts bounds how many times a Bump* usage loop defers
// to the shared reset path before falling back to an inline reset. A converging
// target needs exactly one attempt; the bound exists so a non-converging target
// (issue #4851 class) can never spin a request-path goroutine at 100% CPU.
const maxRequestTimeResetAttempts = 3

// BumpBudgetUsage atomically increments CurrentUsage on the budget identified
// by budgetID and, as a side effect, zeros CurrentUsage / advances LastReset
// when the rolling ResetDuration has elapsed. Uses sync.Map.CompareAndSwap so
// concurrent callers on the same budget never drop increments — a lost CAS
// retries against the winner's snapshot. No-op when the budget is absent.
//
// This is the serialisation point for every usage increment: callers MUST
// funnel through this method (directly or via one of the higher-level
// Update*BudgetUsageInMemory wrappers) rather than doing a plain
// Load → clone → mutate → Store, which races.
func (gs *LocalGovernanceStore) BumpBudgetUsage(ctx context.Context, budgetID string, cost float64) error {
	resetAttempts := 0
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil
		}
		target := gs.budgetResetTarget(old, time.Now())
		if target != nil && resetAttempts < maxRequestTimeResetAttempts {
			resetAttempts++
			gs.ResetExpiredBudgetsInMemory(ctx, false, budgetID)
			continue
		}
		clone := *old
		if target != nil {
			// The reset target is still due after maxRequestTimeResetAttempts
			// shared-path resets, so it is not converging (issue #4851 class:
			// a target function bug or bad duration slipped past validation).
			// Reset inline in the clone so this loop always terminates,
			// zeroing the LastDB baseline exactly like
			// resetExpiredBudgetFromSnapshot so DB delta folding stays
			// consistent. Only the reset hook (DB persistence of LastReset)
			// is skipped on the request path.
			gs.logger.Error("budget %s reset target not converging after %d resets; applying inline reset to avoid request-path spin", budgetID, resetAttempts)
			clone.CurrentUsage = 0
			clone.LastReset = *target
			clone.ConsumeOverrideCycle()
			gs.LastDBUsagesBudgetsMu.Lock()
			gs.LastDBUsagesBudgets[budgetID] = 0
			gs.LastDBUsagesBudgetsMu.Unlock()
		}
		clone.CurrentUsage += cost
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			return nil
		}
	}
}

// BumpRateLimitUsage atomically increments the token and/or request counters on
// the rate limit identified by rateLimitID and, as a side effect, zeros the
// relevant counter / advances its LastReset when the rolling
// TokenResetDuration / RequestResetDuration has elapsed. Same CAS-retry
// contract as BumpBudgetUsage — no increment is ever dropped under
// concurrent callers. No-op when the rate limit is absent.
func (gs *LocalGovernanceStore) BumpRateLimitUsage(ctx context.Context, rateLimitID string, tokensUsed int64, shouldUpdateTokens, shouldUpdateRequests bool) error {
	resetAttempts := 0
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil
		}
		tokenNewLastReset, requestNewLastReset := gs.rateLimitResetTargets(old, time.Now())
		if (tokenNewLastReset != nil || requestNewLastReset != nil) && resetAttempts < maxRequestTimeResetAttempts {
			resetAttempts++
			gs.ResetExpiredRateLimitsInMemory(ctx, false, rateLimitID)
			continue
		}
		clone := *old
		if tokenNewLastReset != nil || requestNewLastReset != nil {
			// The reset target is still due after maxRequestTimeResetAttempts
			// shared-path resets, so it is not converging (issue #4851 class:
			// a target function bug or bad duration slipped past validation).
			// Reset inline in the clone so this loop always terminates,
			// zeroing the LastDB baselines exactly like
			// resetExpiredRateLimitFromSnapshot so DB delta folding stays
			// consistent. Only the reset hook (DB persistence of LastReset)
			// is skipped on the request path.
			gs.logger.Error("rate limit %s reset target not converging after %d resets; applying inline reset to avoid request-path spin", rateLimitID, resetAttempts)
			if tokenNewLastReset != nil {
				clone.TokenCurrentUsage = 0
				clone.TokenLastReset = *tokenNewLastReset
				gs.LastDBUsagesRateLimitsTokensMu.Lock()
				gs.LastDBUsagesTokensRateLimits[rateLimitID] = 0
				gs.LastDBUsagesRateLimitsTokensMu.Unlock()
			}
			if requestNewLastReset != nil {
				clone.RequestCurrentUsage = 0
				clone.RequestLastReset = *requestNewLastReset
				gs.LastDBUsagesRateLimitsRequestsMu.Lock()
				gs.LastDBUsagesRequestsRateLimits[rateLimitID] = 0
				gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
			}
		}
		if shouldUpdateTokens {
			clone.TokenCurrentUsage += tokensUsed
		}
		if shouldUpdateRequests {
			clone.RequestCurrentUsage++
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			return nil
		}
	}
}

// BumpRateLimitUsageBy atomically adds arbitrary token and request deltas to the
// rate limit identified by rateLimitID. Unlike BumpRateLimitUsage (which adds a
// token count and a single request), this adds caller-supplied counts on both
// dimensions — used to fold accumulated usage carried from another rate limit.
// Same CAS-retry contract: no increment is dropped under concurrent callers.
// No window-reset side effect, since carried deltas are not request traffic.
// No-op when the rate limit is absent or both deltas are zero.
func (gs *LocalGovernanceStore) BumpRateLimitUsageBy(ctx context.Context, rateLimitID string, tokenDelta, requestDelta int64) error {
	if tokenDelta == 0 && requestDelta == 0 {
		return nil
	}
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil
		}
		clone := *old
		clone.TokenCurrentUsage += tokenDelta
		clone.RequestCurrentUsage += requestDelta
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			return nil
		}
	}
}

// ResetBudgetAt atomically zeros the budget's CurrentUsage and advances its
// LastReset to newLastReset, provided the currently-stored budget has an
// older LastReset. Returns the reset budget and true when the CAS succeeds;
// (nil, false) if the budget is absent or another writer has already advanced
// LastReset to at least newLastReset. Callers (e.g. ResetExpiredBudgetsInMemory)
// use the false return to skip the DB-persistence and reference-refresh work
// that would otherwise be redundant.
func (gs *LocalGovernanceStore) ResetBudgetAt(ctx context.Context, budgetID string, newLastReset time.Time) (*configstoreTables.TableBudget, bool) {
	for {
		raw, exists := gs.budgets.Load(budgetID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableBudget)
		if !ok || old == nil {
			return nil, false
		}
		if !old.LastReset.Before(newLastReset) {
			// Someone else already advanced LastReset past ours, or the reset
			// window hasn't actually opened relative to the stored snapshot.
			return nil, false
		}
		clone := *old
		clone.CurrentUsage = 0
		clone.LastReset = newLastReset
		clone.ConsumeOverrideCycle()
		if gs.budgets.CompareAndSwap(budgetID, raw, &clone) {
			return &clone, true
		}
	}
}

// ResetRateLimitAt atomically resets one or both rate-limit counters on the
// rate limit identified by rateLimitID. A non-nil tokenNewLastReset resets the
// token counter and advances TokenLastReset; similarly for
// requestNewLastReset. Each reset is conditional on the corresponding
// LastReset currently being strictly older than the supplied target, so
// concurrent resetters collapse into a single successful write. Returns the
// updated snapshot and true when at least one counter was reset; (nil, false)
// otherwise.
func (gs *LocalGovernanceStore) ResetRateLimitAt(ctx context.Context, rateLimitID string, tokenNewLastReset, requestNewLastReset *time.Time) (*configstoreTables.TableRateLimit, bool) {
	if tokenNewLastReset == nil && requestNewLastReset == nil {
		return nil, false
	}
	for {
		raw, exists := gs.rateLimits.Load(rateLimitID)
		if !exists || raw == nil {
			return nil, false
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return nil, false
		}
		clone := *old
		didReset := false
		if tokenNewLastReset != nil && old.TokenLastReset.Before(*tokenNewLastReset) {
			clone.TokenCurrentUsage = 0
			clone.TokenLastReset = *tokenNewLastReset
			didReset = true
		}
		if requestNewLastReset != nil && old.RequestLastReset.Before(*requestNewLastReset) {
			clone.RequestCurrentUsage = 0
			clone.RequestLastReset = *requestNewLastReset
			didReset = true
		}
		if !didReset {
			return nil, false
		}
		if gs.rateLimits.CompareAndSwap(rateLimitID, raw, &clone) {
			return &clone, true
		}
	}
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
		// Refresh each owned budget from the live budget map.
		refreshedBudgets := make([]configstoreTables.TableBudget, 0, len(clone.Budgets))
		for _, b := range clone.Budgets {
			if liveBudget, exists := gs.budgets.Load(b.ID); exists && liveBudget != nil {
				if lb, ok := liveBudget.(*configstoreTables.TableBudget); ok {
					refreshedBudgets = append(refreshedBudgets, *lb)
					continue
				}
			}
			refreshedBudgets = append(refreshedBudgets, b)
		}
		clone.Budgets = refreshedBudgets
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
		// Cross-reference live budgets/rate limit from standalone maps.
		clone := *mc
		if len(clone.Budgets) > 0 {
			liveBudgets := make([]configstoreTables.TableBudget, 0, len(clone.Budgets))
			for _, b := range clone.Budgets {
				if lb, exists := gs.budgets.Load(b.ID); exists && lb != nil {
					if budget, ok := lb.(*configstoreTables.TableBudget); ok {
						liveBudgets = append(liveBudgets, *budget)
					}
				}
			}
			clone.Budgets = liveBudgets
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

// GetVirtualKeyByID retrieves a virtual key by its row ID (lock-free) with all
// relationships preloaded, via the ID-keyed secondary index. Mirrors
// GetVirtualKey (which is keyed by value); used by by-ID hot paths such as /mcp
// JWT auth to avoid a per-request database read.
func (gs *LocalGovernanceStore) GetVirtualKeyByID(ctx context.Context, vkID string) (*configstoreTables.TableVirtualKey, bool) {
	value, exists := gs.virtualKeysByID.Load(vkID)
	if !exists || value == nil {
		return nil, false
	}
	vk, ok := value.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		return nil, false
	}
	return vk, true
}

// storeVirtualKey writes vk into both the value-keyed primary map and the
// ID-keyed secondary index, keeping the two in lock-step. Every writer to
// virtualKeys must go through here so the ID index never diverges.
func (gs *LocalGovernanceStore) storeVirtualKey(value string, vk *configstoreTables.TableVirtualKey) {
	if value == "" {
		if vk != nil {
			gs.logger.Warn("skipping virtual key %s with unresolvable value (env/vault ref could not be resolved)", vk.ID)
		}
		return
	}
	gs.virtualKeys.Store(value, vk)
	if vk != nil && vk.ID != "" {
		gs.virtualKeysByID.Store(vk.ID, vk)
	}
}

// deleteVirtualKeyByValue removes the VK stored under value from both the
// primary map and the ID-keyed secondary index.
func (gs *LocalGovernanceStore) deleteVirtualKeyByValue(value string) {
	if existing, ok := gs.virtualKeys.Load(value); ok {
		if vk, ok := existing.(*configstoreTables.TableVirtualKey); ok && vk != nil && vk.ID != "" {
			gs.virtualKeysByID.Delete(vk.ID)
		}
	}
	gs.virtualKeys.Delete(value)
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
			effectiveMaxLimit := budget.EffectiveMaxLimit()
			gs.logger.Debug("LocalStore CheckBudget: Checking %s budget %s: local=%.4f, remote=%.4f, total=%.4f, limit=%.4f",
				entity, budget.ID, budget.CurrentUsage, baseline, budget.CurrentUsage+baseline, effectiveMaxLimit)
			// Check if current usage (local + remote baseline) exceeds budget limit
			if budget.CurrentUsage+baseline >= effectiveMaxLimit {
				gs.logger.Debug("LocalStore CheckBudget: Budget %s EXCEEDED", budget.ID)
				return DecisionBudgetExceeded, fmt.Errorf("%s budget exceeded: %.4f >= %.4f dollars",
					entity, budget.CurrentUsage+baseline, effectiveMaxLimit)
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

const modelConfigWildcard = configstoreTables.ModelConfigAllModels

// modelConfigStoreKey builds the in-memory cache key for a model config.
func modelConfigStoreKey(scope, scopeID, modelKey string, provider *string) string {
	base := modelKey
	if provider != nil {
		base = fmt.Sprintf("%s:%s", modelKey, *provider)
	}
	if scope == "" || scope == configstoreTables.ModelConfigScopeGlobal {
		return base
	}
	return fmt.Sprintf("%s:%s:%s", scope, scopeID, base)
}

// modelConfigScope is one level of the model-config scope chain (name + target ID).
type modelConfigScope struct {
	name string
	id   string
}

// nonGlobalModelConfigScopeChain returns the non-global scopes that apply to a request
// made with the given virtual key, most specific first. The global scope is intentionally
// excluded because it is enforced separately (and unconditionally) by EvaluateModelAndProviderRequest.
func nonGlobalModelConfigScopeChain(vk *configstoreTables.TableVirtualKey) []modelConfigScope {
	if vk == nil {
		return nil
	}
	return []modelConfigScope{{name: configstoreTables.ModelConfigScopeVirtualKey, id: vk.ID}}
}

// findScopedModelOnlyConfig looks up a model-only config (no provider) within a specific
// scope, preserving cross-provider model-name normalization. scope=="global" reproduces the
// historical global lookup exactly. Returns the matching config and the display name.
func (gs *LocalGovernanceStore) findScopedModelOnlyConfig(ctx context.Context, scope, scopeID, model string) (*configstoreTables.TableModelConfig, string) {
	tryKey := func(modelKey string) (*configstoreTables.TableModelConfig, string) {
		key := modelConfigStoreKey(scope, scopeID, modelKey, nil)
		if value, exists := gs.modelConfigs.Load(key); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok && mc != nil {
				return mc, modelKey
			}
		}
		return nil, ""
	}
	// If modelCatalog is available, try normalized base model name first (cross-provider matching)
	if gs.modelCatalog != nil {
		baseName := gs.modelCatalog.GetBaseModelName(model)
		if baseName != model {
			if mc, name := tryKey(baseName); mc != nil {
				return mc, name
			}
		}
	}
	// Always try direct lookup by original model name as fallback
	return tryKey(model)
}

// extractModelAndProvider extracts the model name and optional provider from a request.
func extractModelAndProvider(request *EvaluationRequest) (string, *string) {
	if request == nil {
		return "", nil
	}
	var provider *string
	if request.Provider != "" {
		p := string(request.Provider)
		provider = &p
	}
	return request.Model, provider
}

// collectModelConfigsFor returns every model config that applies to a request for
// (model, provider) within a single scope, across four tiers (most → least specific),
// deduped by config ID:
//  1. (model, provider)  exact model on this provider
//  2. (model, nil)       exact model on all providers (base-name normalized)
//  3. ("*", provider)    all models on this provider  (provider-level governance)
//  4. ("*", nil)         all models on all providers
//
// This is the single source of truth for "which model configs apply"; every budget /
// rate-limit check and usage-tracking site iterates it so the wildcard tiers are matched
// consistently everywhere.
func (gs *LocalGovernanceStore) collectModelConfigsFor(ctx context.Context, scope, scopeID, model string, provider *string) []*configstoreTables.TableModelConfig {
	var out []*configstoreTables.TableModelConfig
	seen := make(map[string]bool)
	add := func(mc *configstoreTables.TableModelConfig) {
		if mc == nil || seen[mc.ID] {
			return
		}
		seen[mc.ID] = true
		out = append(out, mc)
	}
	loadKey := func(modelKey string, prov *string) *configstoreTables.TableModelConfig {
		if value, exists := gs.modelConfigs.Load(modelConfigStoreKey(scope, scopeID, modelKey, prov)); exists && value != nil {
			if mc, ok := value.(*configstoreTables.TableModelConfig); ok {
				return mc
			}
		}
		return nil
	}
	if provider != nil {
		add(loadKey(model, provider)) // tier 1: exact model + provider
	}
	if mc, _ := gs.findScopedModelOnlyConfig(ctx, scope, scopeID, model); mc != nil {
		add(mc) // tier 2: exact model, all providers (normalized)
	}
	if provider != nil {
		add(loadKey(modelConfigWildcard, provider)) // tier 3: all models on this provider
	}
	add(loadKey(modelConfigWildcard, nil)) // tier 4: all models, all providers
	return out
}

// modelConfigEntityKey builds a stable, unique entity description for a model config
func modelConfigEntityKey(mc *configstoreTables.TableModelConfig) string {
	name := mc.ModelName
	if name == modelConfigWildcard {
		name = "AllModels"
	}
	key := "Model:" + name
	if mc.Provider != nil {
		key += ":Provider:" + *mc.Provider
	}
	if mc.Scope != "" && mc.Scope != configstoreTables.ModelConfigScopeGlobal {
		scopeID := ""
		if mc.ScopeID != nil {
			scopeID = *mc.ScopeID
		}
		key += ":" + mc.Scope + ":" + scopeID
	}
	return key
}

// loadModelConfigBudgets returns the hot in-memory budget rows owned by a model config
func (gs *LocalGovernanceStore) loadModelConfigBudgets(ctx context.Context, mc *configstoreTables.TableModelConfig) []*configstoreTables.TableBudget {
	if mc == nil || len(mc.Budgets) == 0 {
		return nil
	}
	out := make([]*configstoreTables.TableBudget, 0, len(mc.Budgets))
	for i := range mc.Budgets {
		if budget := gs.LoadBudget(ctx, mc.Budgets[i].ID); budget != nil {
			out = append(out, budget)
		}
	}
	return out
}

// CheckModelBudget performs budget checking for global-scope model-level configs, across all
// four tiers (exact model±provider and all-models "*"±provider).
func (gs *LocalGovernanceStore) CheckModelBudget(ctx context.Context, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	// This is to prevent nil pointer dereference
	if baselines == nil {
		baselines = map[string]float64{}
	}
	model, provider := extractModelAndProvider(request)
	entityWiseBudgets := EntityWiseBudgets{}
	for _, mc := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, provider) {
		if budgets := gs.loadModelConfigBudgets(ctx, mc); len(budgets) > 0 {
			entityWiseBudgets[modelConfigEntityKey(mc)] = budgets
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

// CollectTeamBudgets returns the live budget objects configured for a team,
// resolved by ID from the hot budgets map (so usage counters and recent edits
// are reflected). Mirrors the read pattern in CheckTeamBudget. Returns nil when
// the team is unknown or has no budgets. Exported so the enterprise layer can
// fold team budgets into a user→team→business-unit hierarchy collector the same
// way collectBudgetsFromHierarchy folds them into the VK hierarchy.
func (gs *LocalGovernanceStore) CollectTeamBudgets(ctx context.Context, teamID string) []*configstoreTables.TableBudget {
	if teamID == "" {
		return nil
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return nil
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil || len(team.Budgets) == 0 {
		return nil
	}
	list := make([]*configstoreTables.TableBudget, 0, len(team.Budgets))
	for _, b := range team.Budgets {
		if hot := gs.LoadBudget(ctx, b.ID); hot != nil {
			list = append(list, hot)
		}
	}
	if len(list) == 0 {
		return nil
	}
	return list
}

// CollectTeamRateLimits returns the live rate-limit object configured for a team
// (at most one), resolved by ID from the hot rate-limits map. Mirrors the read
// pattern in CheckTeamRateLimit. Returns nil when the team is unknown or has no
// rate limit. Exported for the enterprise user-hierarchy collector.
func (gs *LocalGovernanceStore) CollectTeamRateLimits(ctx context.Context, teamID string) []*configstoreTables.TableRateLimit {
	if teamID == "" {
		return nil
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return nil
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil || team.RateLimitID == nil {
		return nil
	}
	rl := gs.LoadRateLimit(ctx, *team.RateLimitID)
	if rl == nil {
		return nil
	}
	return []*configstoreTables.TableRateLimit{rl}
}

// CollectCustomerBudgets returns the customer's live budgets resolved from the hot budgets map, or nil if the customer is unknown or has none.
func (gs *LocalGovernanceStore) CollectCustomerBudgets(ctx context.Context, customerID string) []*configstoreTables.TableBudget {
	if customerID == "" {
		return nil
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return nil
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer == nil || len(customer.Budgets) == 0 {
		return nil
	}
	list := make([]*configstoreTables.TableBudget, 0, len(customer.Budgets))
	for i := range customer.Budgets {
		if hot := gs.LoadBudget(ctx, customer.Budgets[i].ID); hot != nil {
			list = append(list, hot)
		}
	}
	return list
}

// CollectCustomerRateLimits returns the customer's live rate-limit (at most one) resolved from the hot rate-limits map, or nil if the customer is unknown or has none.
func (gs *LocalGovernanceStore) CollectCustomerRateLimits(ctx context.Context, customerID string) []*configstoreTables.TableRateLimit {
	if customerID == "" {
		return nil
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return nil
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer == nil || customer.RateLimitID == nil {
		return nil
	}
	rl := gs.LoadRateLimit(ctx, *customer.RateLimitID)
	if rl == nil {
		return nil
	}
	return []*configstoreTables.TableRateLimit{rl}
}

// GetTeamCustomerID returns a team's scalar customer id (TableTeam.CustomerID), or "" if the team is unknown or has no scalar customer. The enterprise layer uses it to exclude that customer from its M2M team→customer propagation so the OSS VK→team→customer hierarchy doesn't double-charge it.
func (gs *LocalGovernanceStore) GetTeamCustomerID(ctx context.Context, teamID string) string {
	if teamID == "" {
		return ""
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return ""
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil || team.CustomerID == nil {
		return ""
	}
	return *team.CustomerID
}

// GetTeamName returns a team's display name from the in-memory store, or "" if
// the team is unknown. The enterprise layer uses it as the fallback for log
// stamping when its edge-driven name caches miss (e.g. a team with no user
// members and no business unit).
func (gs *LocalGovernanceStore) GetTeamName(ctx context.Context, teamID string) string {
	if teamID == "" {
		return ""
	}
	teamValue, exists := gs.teams.Load(teamID)
	if !exists || teamValue == nil {
		return ""
	}
	team, ok := teamValue.(*configstoreTables.TableTeam)
	if !ok || team == nil {
		return ""
	}
	return team.Name
}

// GetCustomerName returns a customer's display name from the in-memory store,
// or "" if the customer is unknown. Same fallback role as GetTeamName.
func (gs *LocalGovernanceStore) GetCustomerName(ctx context.Context, customerID string) string {
	if customerID == "" {
		return ""
	}
	customerValue, exists := gs.customers.Load(customerID)
	if !exists || customerValue == nil {
		return ""
	}
	customer, ok := customerValue.(*configstoreTables.TableCustomer)
	if !ok || customer == nil {
		return ""
	}
	return customer.Name
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
	if !ok || len(customer.Budgets) == 0 {
		return DecisionAllow, nil
	}
	key := fmt.Sprintf("Customer:%s", customerID)
	var customerBudgets []*configstoreTables.TableBudget
	for i := range customer.Budgets {
		if b := gs.LoadBudget(ctx, customer.Budgets[i].ID); b != nil {
			customerBudgets = append(customerBudgets, b)
		}
	}
	if len(customerBudgets) == 0 {
		return DecisionAllow, nil
	}
	entityWiseBudgets := EntityWiseBudgets{key: customerBudgets}
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

// CheckModelRateLimit checks global-scope model-level rate limits across all four tiers
func (gs *LocalGovernanceStore) CheckModelRateLimit(ctx context.Context, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	// This is to prevent nil pointer dereference
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}
	model, provider := extractModelAndProvider(request)
	entityWiseRateLimits := make(EntityWiseRateLimits)
	for _, mc := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, provider) {
		if mc.RateLimitID == nil {
			continue
		}
		if rateLimit := gs.LoadRateLimit(ctx, *mc.RateLimitID); rateLimit != nil {
			entityWiseRateLimits[modelConfigEntityKey(mc)] = []*configstoreTables.TableRateLimit{rateLimit}
		}
	}
	return gs.CheckRateLimit(ctx, entityWiseRateLimits, tokensBaselines, requestsBaselines)
}

// CheckScopedModelBudget enforces budgets from model configs scoped to the given
// (scope, scopeID) — e.g. ("virtual_key", vk.ID). Checked in addition to the global
// model budgets; a request must satisfy both. Empty scope or scopeID is a no-op.
func (gs *LocalGovernanceStore) CheckScopedModelBudget(ctx context.Context, scope, scopeID string, request *EvaluationRequest, baselines map[string]float64) (Decision, error) {
	if scope == "" || scopeID == "" {
		return DecisionAllow, nil
	}
	if baselines == nil {
		baselines = map[string]float64{}
	}
	model, provider := extractModelAndProvider(request)
	entityWiseBudgets := EntityWiseBudgets{}
	for _, mc := range gs.collectModelConfigsFor(ctx, scope, scopeID, model, provider) {
		if budgets := gs.loadModelConfigBudgets(ctx, mc); len(budgets) > 0 {
			entityWiseBudgets[modelConfigEntityKey(mc)] = budgets
		}
	}
	return gs.CheckBudget(ctx, entityWiseBudgets, baselines)
}

// CheckScopedModelRateLimit enforces rate limits from model configs scoped to the given
// (scope, scopeID), in addition to the global model rate limits.
func (gs *LocalGovernanceStore) CheckScopedModelRateLimit(ctx context.Context, scope, scopeID string, request *EvaluationRequest, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, error) {
	if scope == "" || scopeID == "" {
		return DecisionAllow, nil
	}
	if tokensBaselines == nil {
		tokensBaselines = map[string]int64{}
	}
	if requestsBaselines == nil {
		requestsBaselines = map[string]int64{}
	}
	model, provider := extractModelAndProvider(request)
	entityWiseRateLimits := make(EntityWiseRateLimits)
	for _, mc := range gs.collectModelConfigsFor(ctx, scope, scopeID, model, provider) {
		if mc.RateLimitID == nil {
			continue
		}
		if rateLimit := gs.LoadRateLimit(ctx, *mc.RateLimitID); rateLimit != nil {
			entityWiseRateLimits[modelConfigEntityKey(mc)] = []*configstoreTables.TableRateLimit{rateLimit}
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
	for _, budgetID := range budgetIDs {
		if err := gs.BumpBudgetUsage(ctx, budgetID, cost); err != nil {
			return err
		}
	}
	return nil
}

// UpdateProviderAndModelBudgetUsageInMemory performs atomic budget updates for both provider-level and model-level configs (in memory)
func (gs *LocalGovernanceStore) UpdateProviderAndModelBudgetUsageInMemory(ctx context.Context, model string, provider schemas.ModelProvider, cost float64) error {
	// 1. Update provider-level budget (if provider is set)
	if provider != "" {
		providerKey := string(provider)
		if value, exists := gs.providers.Load(providerKey); exists && value != nil {
			if providerTable, ok := value.(*configstoreTables.TableProvider); ok && providerTable != nil && providerTable.BudgetID != nil {
				if err := gs.BumpBudgetUsage(ctx, *providerTable.BudgetID, cost); err != nil {
					return err
				}
			}
		}
	}

	// 2. Update global-scope model-level budgets across all four tiers (incl. the
	// all-models "*:provider" tier that now carries provider-level governance).
	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}
	for _, mc := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, providerStr) {
		for i := range mc.Budgets {
			if err := gs.BumpBudgetUsage(ctx, mc.Budgets[i].ID, cost); err != nil {
				return err
			}
		}
	}

	return nil
}

// UpdateUserBudgetUsageInMemory updates user's budget usage in memory (enterprise-only)
// Community build: silent no-op to avoid per-request error spam when a userID is set.
func (gs *LocalGovernanceStore) UpdateUserBudgetUsageInMemory(ctx context.Context, userID string, cost float64) error {
	return nil
}

// UpdateProviderAndModelRateLimitUsageInMemory updates rate limit counters for both provider-level and model-level rate limits.
func (gs *LocalGovernanceStore) UpdateProviderAndModelRateLimitUsageInMemory(ctx context.Context, model string, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	// 1. Update provider-level rate limit (if provider is set)
	if provider != "" {
		providerKey := string(provider)
		if value, exists := gs.providers.Load(providerKey); exists && value != nil {
			if providerTable, ok := value.(*configstoreTables.TableProvider); ok && providerTable != nil && providerTable.RateLimitID != nil {
				if err := gs.BumpRateLimitUsage(ctx, *providerTable.RateLimitID, tokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
					return err
				}
			}
		}
	}

	// 2. Update global-scope model-level rate limits across all four tiers (incl. the
	// all-models "*:provider" tier that now carries provider-level governance).
	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}
	for _, mc := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, providerStr) {
		if mc.RateLimitID == nil {
			continue
		}
		if err := gs.BumpRateLimitUsage(ctx, *mc.RateLimitID, tokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			return err
		}
	}

	return nil
}

// UpdateScopedModelBudgetUsageInMemory bumps budget usage for model configs scoped to the
// given (scope, scopeID). Post-response counterpart to CheckScopedModelBudget — without it,
// scoped budgets never increase and never trip. Empty scope/scopeID/model is a no-op.
func (gs *LocalGovernanceStore) UpdateScopedModelBudgetUsageInMemory(ctx context.Context, scope, scopeID, model string, provider schemas.ModelProvider, cost float64) error {
	if scope == "" || scopeID == "" {
		return nil
	}
	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}
	for _, mc := range gs.collectModelConfigsFor(ctx, scope, scopeID, model, providerStr) {
		for i := range mc.Budgets {
			if err := gs.BumpBudgetUsage(ctx, mc.Budgets[i].ID, cost); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateScopedModelRateLimitUsageInMemory bumps rate limit counters for model configs scoped
// to the given (scope, scopeID). Post-response counterpart to CheckScopedModelRateLimit.
func (gs *LocalGovernanceStore) UpdateScopedModelRateLimitUsageInMemory(ctx context.Context, scope, scopeID, model string, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	if scope == "" || scopeID == "" {
		return nil
	}
	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}
	for _, mc := range gs.collectModelConfigsFor(ctx, scope, scopeID, model, providerStr) {
		if mc.RateLimitID == nil {
			continue
		}
		if err := gs.BumpRateLimitUsage(ctx, *mc.RateLimitID, tokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			return err
		}
	}
	return nil
}

// UpdateVirtualKeyRateLimitUsageInMemory updates rate limit counters for VK-level rate limits.
func (gs *LocalGovernanceStore) UpdateVirtualKeyRateLimitUsageInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}
	// Collect rate limit IDs using fast in-memory lookup instead of DB queries
	rateLimitIDs := gs.collectRateLimitIDsFromMemory(ctx, vk, provider)
	for _, rateLimitID := range rateLimitIDs {
		if err := gs.BumpRateLimitUsage(ctx, rateLimitID, tokensUsed, shouldUpdateTokens, shouldUpdateRequests); err != nil {
			return err
		}
	}
	return nil
}

// UpdateUserRateLimitUsageInMemory updates user's rate limit usage in memory (enterprise-only)
// Community build: silent no-op to avoid per-request error spam when a userID is set.
func (gs *LocalGovernanceStore) UpdateUserRateLimitUsageInMemory(ctx context.Context, userID string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	return nil
}

// budgetResetTarget returns the LastReset value to write when budget is expired.
func (gs *LocalGovernanceStore) budgetResetTarget(budget *configstoreTables.TableBudget, now time.Time) *time.Time {
	if budget == nil || budget.ResetDuration == "" {
		return nil
	}
	// Sub-day durations have no calendar boundary; see rateLimitResetTarget.
	if budget.IsCalendarAligned && configstoreTables.IsCalendarAlignableDuration(budget.ResetDuration) {
		currentPeriodStart := configstoreTables.GetCalendarPeriodStart(budget.ResetDuration, now)
		if currentPeriodStart.After(budget.LastReset) {
			return &currentPeriodStart
		}
		return nil
	}
	duration, err := configstoreTables.ParseDuration(budget.ResetDuration)
	if err != nil {
		gs.logger.Error("invalid budget reset duration %s: %v", budget.ResetDuration, err)
		return nil
	}
	// A non-positive duration would be perpetually due, spinning BumpBudgetUsage
	// forever (issue #4851 class); treat it as invalid, same as unparseable.
	if duration <= 0 {
		gs.logger.Error("non-positive budget reset duration %s: budget will not auto-reset", budget.ResetDuration)
		return nil
	}
	if now.Sub(budget.LastReset) >= duration {
		return &now
	}
	return nil
}

// resetExpiredBudgetFromSnapshot applies the local side effects for an expired budget snapshot.
// Owner reference refresh is NOT done here; ResetExpiredBudgetsInMemory batches it
// once per sweep so a sweep costs O(owners + resets) instead of O(owners x resets).
func (gs *LocalGovernanceStore) resetExpiredBudgetFromSnapshot(ctx context.Context, budget *configstoreTables.TableBudget, now time.Time) *configstoreTables.TableBudget {
	newLastReset := gs.budgetResetTarget(budget, now)
	if newLastReset == nil {
		return nil
	}
	resetBudget, ok := gs.ResetBudgetAt(ctx, budget.ID, *newLastReset)
	if !ok {
		return nil
	}
	oldUsage := budget.CurrentUsage
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets[resetBudget.ID] = 0
	gs.LastDBUsagesBudgetsMu.Unlock()
	gs.logger.Debug(fmt.Sprintf("Reset budget %s (was %.2f, reset to 0)", resetBudget.ID, oldUsage))
	return resetBudget
}

// ResetExpiredBudgetsInMemory checks and resets budgets that have exceeded their reset duration.
// With no budgetIDs it scans every budget; with IDs it only checks those budgets.
// refreshReferences controls whether embedded owner references (VK, team, customer) are updated
// after reset. Background/ticker callers pass true; request-time callers pass false to avoid
// an O(N) scan of all owners on the hot path.
func (gs *LocalGovernanceStore) ResetExpiredBudgetsInMemory(ctx context.Context, refreshReferences bool, budgetIDs ...string) []*configstoreTables.TableBudget {
	now := time.Now()
	var resetBudgets []*configstoreTables.TableBudget
	resetOne := func(value any) {
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return
		}
		if resetBudget := gs.resetExpiredBudgetFromSnapshot(ctx, budget, now); resetBudget != nil {
			resetBudgets = append(resetBudgets, resetBudget)
		}
	}
	if len(budgetIDs) == 0 {
		gs.budgets.Range(func(key, value any) bool {
			resetOne(value)
			return true
		})
	} else {
		for _, budgetID := range budgetIDs {
			if value, ok := gs.budgets.Load(budgetID); ok {
				resetOne(value)
			}
		}
	}
	if refreshReferences {
		gs.updateBudgetReferences(ctx, resetBudgets)
	}
	if len(resetBudgets) > 0 {
		if onBudgetsReset := gs.getBudgetsResetHook(); onBudgetsReset != nil {
			onBudgetsReset(resetBudgets)
		}
	}
	return resetBudgets
}

var ErrBudgetNotFound = errors.New("budget not found")

// ResetBudget forcibly zeroes a budget's usage (manual/admin reset), applying
// the same side effects as an expiry-driven reset.
func (gs *LocalGovernanceStore) ResetBudget(ctx context.Context, budgetID string) (*configstoreTables.TableBudget, error) {
	if gs.LoadBudget(ctx, budgetID) == nil {
		return nil, ErrBudgetNotFound
	}
	// A manual reset must always zero usage. ResetBudgetAt only applies when the
	// target is strictly after the stored LastReset, so advance past a future
	// LastReset to force the reset rather than reporting a no-op as success.
	var resetBudget *configstoreTables.TableBudget
	for {
		current := gs.LoadBudget(ctx, budgetID)
		if current == nil {
			return nil, ErrBudgetNotFound
		}
		if current.CurrentUsage == 0 {
			return current, nil
		}
		target := time.Now()
		if !current.LastReset.Before(target) {
			target = current.LastReset.Add(time.Nanosecond)
		}
		rb, ok := gs.ResetBudgetAt(ctx, budgetID, target)
		if ok {
			resetBudget = rb
			break
		}
	}
	gs.LastDBUsagesBudgetsMu.Lock()
	gs.LastDBUsagesBudgets[resetBudget.ID] = 0
	gs.LastDBUsagesBudgetsMu.Unlock()
	gs.updateBudgetReferences(ctx, resetBudget)
	if onBudgetsReset := gs.getBudgetsResetHook(); onBudgetsReset != nil {
		onBudgetsReset([]*configstoreTables.TableBudget{resetBudget})
	}
	if err := gs.ResetExpiredBudgets(ctx, []*configstoreTables.TableBudget{resetBudget}); err != nil {
		return nil, err
	}
	gs.logger.Info("manually reset budget %s (usage zeroed, last_reset advanced to %s)", resetBudget.ID, resetBudget.LastReset.Format(time.RFC3339))
	return resetBudget, nil
}

// rateLimitResetTarget returns the LastReset value to write when a rate-limit counter is expired.
// Calendar alignment only applies to durations with a calendar boundary (d/w/M/Y);
// sub-day durations fall back to rolling-window semantics even when the owner is
// calendar-aligned, mirroring the handler-side snap logic. Without this guard
// GetCalendarPeriodStart returns now for sub-day durations, making the reset
// target perpetually due and spinning BumpRateLimitUsage forever (issue #4851).
func (gs *LocalGovernanceStore) rateLimitResetTarget(resetDuration *string, calendarAligned bool, lastReset time.Time, now time.Time) *time.Time {
	if resetDuration == nil {
		return nil
	}
	if calendarAligned && configstoreTables.IsCalendarAlignableDuration(*resetDuration) {
		period := configstoreTables.GetCalendarPeriodStart(*resetDuration, now)
		if period.After(lastReset) {
			return &period
		}
		return nil
	}
	duration, err := configstoreTables.ParseDuration(*resetDuration)
	if err != nil {
		gs.logger.Error("invalid rate limit reset duration %s: %v", *resetDuration, err)
		return nil
	}
	// A non-positive duration would be perpetually due, spinning BumpRateLimitUsage
	// forever (issue #4851 class); treat it as invalid, same as unparseable.
	if duration <= 0 {
		gs.logger.Error("non-positive rate limit reset duration %s: counter will not auto-reset", *resetDuration)
		return nil
	}
	if now.Sub(lastReset) >= duration {
		return &now
	}
	return nil
}

// rateLimitResetTargets returns reset targets for the token and request counters.
func (gs *LocalGovernanceStore) rateLimitResetTargets(rateLimit *configstoreTables.TableRateLimit, now time.Time) (*time.Time, *time.Time) {
	if rateLimit == nil {
		return nil, nil
	}
	calendarAligned := rateLimit.IsCalendarAligned
	return gs.rateLimitResetTarget(rateLimit.TokenResetDuration, calendarAligned, rateLimit.TokenLastReset, now), gs.rateLimitResetTarget(rateLimit.RequestResetDuration, calendarAligned, rateLimit.RequestLastReset, now)
}

// resetExpiredRateLimitFromSnapshot applies the local side effects for an expired rate-limit snapshot.
// Owner reference refresh is NOT done here; ResetExpiredRateLimitsInMemory batches it
// once per sweep so a sweep costs O(owners + resets) instead of O(owners x resets).
func (gs *LocalGovernanceStore) resetExpiredRateLimitFromSnapshot(ctx context.Context, rateLimit *configstoreTables.TableRateLimit, now time.Time) *configstoreTables.TableRateLimit {
	tokenNewLastReset, requestNewLastReset := gs.rateLimitResetTargets(rateLimit, now)
	if tokenNewLastReset == nil && requestNewLastReset == nil {
		return nil
	}
	resetRateLimit, ok := gs.ResetRateLimitAt(ctx, rateLimit.ID, tokenNewLastReset, requestNewLastReset)
	if !ok {
		return nil
	}
	if tokenNewLastReset != nil {
		gs.LastDBUsagesRateLimitsTokensMu.Lock()
		gs.LastDBUsagesTokensRateLimits[resetRateLimit.ID] = 0
		gs.LastDBUsagesRateLimitsTokensMu.Unlock()
	}
	if requestNewLastReset != nil {
		gs.LastDBUsagesRateLimitsRequestsMu.Lock()
		gs.LastDBUsagesRequestsRateLimits[resetRateLimit.ID] = 0
		gs.LastDBUsagesRateLimitsRequestsMu.Unlock()
	}
	return resetRateLimit
}

// ResetExpiredRateLimitsInMemory performs reset of expired rate limits for both provider-level and VK-level.
// With no rateLimitIDs it scans every rate limit; with IDs it only checks those rate limits.
// refreshReferences controls whether embedded owner references (VK, team, customer) are updated
// after reset. Background/ticker callers pass true; request-time callers pass false to avoid
// an O(N) scan of all owners on the hot path.
func (gs *LocalGovernanceStore) ResetExpiredRateLimitsInMemory(ctx context.Context, refreshReferences bool, rateLimitIDs ...string) []*configstoreTables.TableRateLimit {
	now := time.Now()
	var resetRateLimits []*configstoreTables.TableRateLimit
	resetOne := func(value any) {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return
		}
		if resetRateLimit := gs.resetExpiredRateLimitFromSnapshot(ctx, rateLimit, now); resetRateLimit != nil {
			resetRateLimits = append(resetRateLimits, resetRateLimit)
		}
	}
	if len(rateLimitIDs) == 0 {
		gs.rateLimits.Range(func(key, value any) bool {
			resetOne(value)
			return true
		})
	} else {
		for _, rateLimitID := range rateLimitIDs {
			if value, ok := gs.rateLimits.Load(rateLimitID); ok {
				resetOne(value)
			}
		}
	}
	if refreshReferences {
		gs.updateRateLimitReferences(ctx, resetRateLimits)
	}
	if len(resetRateLimits) > 0 {
		if onRateLimitsReset := gs.getRateLimitsResetHook(); onRateLimitsReset != nil {
			onRateLimitsReset(resetRateLimits)
		}
	}
	return resetRateLimits
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration in database
func (gs *LocalGovernanceStore) ResetExpiredBudgets(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) error {
	// Persist to database if any resets occurred using direct UPDATE to avoid overwriting config fields
	if len(resetBudgets) > 0 && gs.configStore != nil {
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			for _, budget := range resetBudgets {
				// Persist the finite-override lifecycle first, guarded on last_reset:
				// a snapshot that lost the persistence race to a newer reset must not
				// restore older lifecycle state (extra override cycles would survive a
				// restart otherwise). This must run BEFORE the usage write below, which
				// advances last_reset and would close the guard within this transaction.
				overrideResult := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableBudget{}).
					Where("id = ? AND last_reset < ?", budget.ID, budget.LastReset).
					Updates(map[string]interface{}{
						"override_amount":           budget.OverrideAmount,
						"override_mode":             budget.OverrideMode,
						"override_cycles_remaining": budget.OverrideCyclesRemaining,
					})

				if overrideResult.Error != nil {
					return fmt.Errorf("failed to reset budget override lifecycle %s: %w", budget.ID, overrideResult.Error)
				}
				if overrideResult.RowsAffected == 0 {
					gs.logger.Debug("skipping stale override reset persistence for budget %s: database already holds newer reset state", budget.ID)
				}

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
	// Range over ALL rate limits in memory (mirrors DumpBudgets pattern).
	// This covers rate limits from every source: virtual keys, model configs,
	// providers, teams, customers, AND access profiles — whose IDs were
	// previously missing, causing AP rate-limit usage to never reach the DB.
	type rateLimitUpdate struct {
		ID                  string
		TokenCurrentUsage   int64
		TokenLastReset      time.Time
		RequestCurrentUsage int64
		RequestLastReset    time.Time
	}
	var rateLimitUpdates []rateLimitUpdate
	gs.rateLimits.Range(func(key, value interface{}) bool {
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true
		}
		update := rateLimitUpdate{
			ID:                  rateLimit.ID,
			TokenCurrentUsage:   rateLimit.TokenCurrentUsage,
			TokenLastReset:      rateLimit.TokenLastReset,
			RequestCurrentUsage: rateLimit.RequestCurrentUsage,
			RequestLastReset:    rateLimit.RequestLastReset,
		}
		if tokenBaseline, exists := tokenBaselines[rateLimit.ID]; exists {
			update.TokenCurrentUsage += tokenBaseline
		}
		if requestBaseline, exists := requestBaselines[rateLimit.ID]; exists {
			update.RequestCurrentUsage += requestBaseline
		}
		rateLimitUpdates = append(rateLimitUpdates, update)
		return true
	})
	sort.Slice(rateLimitUpdates, func(i, j int) bool {
		return rateLimitUpdates[i].ID < rateLimitUpdates[j].ID
	})

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
						"token_last_reset":      update.TokenLastReset,
						"request_current_usage": update.RequestCurrentUsage,
						"request_last_reset":    update.RequestLastReset,
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
		budgetIDs := make([]string, 0, len(budgets))
		for id := range budgets {
			budgetIDs = append(budgetIDs, id)
		}
		sort.Strings(budgetIDs)
		if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			// Update each budget atomically using direct UPDATE to avoid deadlocks
			// (SELECT + Save pattern causes deadlocks when multiple instances run concurrently)
			for _, budgetID := range budgetIDs {
				inMemoryBudget := budgets[budgetID]
				// Calculate the new usage value
				newUsage := inMemoryBudget.CurrentUsage
				if baseline, exists := baselines[inMemoryBudget.ID]; exists {
					newUsage += baseline
				}

				// The override trio mutated by ConsumeOverrideCycle is flushed first
				// under a monotonic last_reset guard: it only fires when this node
				// performed a reset the database has not seen, which is exactly when
				// its override snapshot is authoritative. An unguarded write would let
				// a node with a stale in-memory override clobber a newer admin update
				// every dump cycle. This must run BEFORE the usage write below, which
				// advances last_reset and would close the guard within this transaction.
				overrideResult := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableBudget{}).
					Where("id = ? AND last_reset < ?", inMemoryBudget.ID, inMemoryBudget.LastReset).
					Updates(map[string]interface{}{
						"override_amount":           inMemoryBudget.OverrideAmount,
						"override_mode":             inMemoryBudget.OverrideMode,
						"override_cycles_remaining": inMemoryBudget.OverrideCyclesRemaining,
					})

				if overrideResult.Error != nil {
					return fmt.Errorf("failed to update budget override lifecycle %s: %w", inMemoryBudget.ID, overrideResult.Error)
				}

				// Direct UPDATE avoids read-then-write lock escalation that causes deadlocks
				// Use Session with SkipHooks to avoid triggering BeforeSave hook validation
				result := tx.WithContext(ctx).
					Session(&gorm.Session{SkipHooks: true}).
					Model(&configstoreTables.TableBudget{}).
					Where("id = ?", inMemoryBudget.ID).
					Updates(map[string]interface{}{
						"current_usage": newUsage,
						"last_reset":    inMemoryBudget.LastReset,
					})

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
	loadStart := time.Now()
	defer func() {
		gs.logger.Info("[startup-timing] loadFromDatabase total took %v", time.Since(loadStart))
	}()
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
	vkLoadStart := time.Now()
	virtualKeys, err := gs.configStore.GetVirtualKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to load virtual keys: %w", err)
	}
	gs.logger.Info("[startup-timing] loadFromDatabase GetVirtualKeys loaded %d keys in %v", len(virtualKeys), time.Since(vkLoadStart))

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
	rebuildStart := time.Now()
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits, modelConfigs, providers, routingRules)
	gs.logger.Info("[startup-timing] loadFromDatabase rebuildInMemoryStructures took %v", time.Since(rebuildStart))

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

	// Populate model configs with their relationships (Budgets and RateLimit)
	for i := range modelConfigs {
		mc := &modelConfigs[i]

		// Populate multi-budgets owned via TableBudget.ModelConfigID (the active path).
		if len(mc.Budgets) == 0 {
			for j := range budgets {
				if budgets[j].ModelConfigID != nil && *budgets[j].ModelConfigID == mc.ID {
					mc.Budgets = append(mc.Budgets, budgets[j])
				}
			}
		}

		// Legacy single-budget linking (inert; kept for backward-compatible config.json).
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
	gs.virtualKeysByID = sync.Map{}
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
		gs.storeVirtualKey(vk.Value.GetValue(), vk)
	}

	// Build model configs map.
	// Key format (global scope): "modelName" for all-provider configs, "modelName:provider"
	// for provider-specific configs. Non-global scopes (e.g. virtual_key) prefix the key with
	// "<scope>:<scopeID>:" via modelConfigStoreKey so they never collide with global configs.
	// Model names are normalized using GetBaseModelName to prevent duplicate config leakage
	// (e.g., "openai/gpt-4o" and "gpt-4o" both store under base "gpt-4o").
	for i := range modelConfigs {
		mc := &modelConfigs[i]
		// Stamp calendar alignment onto owned budgets and rate limit so the reset path
		// reads the right window (the flat budgets/rate-limits list lacks owner context).
		// Mirrors how VK/team budgets are stamped from their owner.
		for j := range mc.Budgets {
			mc.Budgets[j].IsCalendarAligned = mc.CalendarAligned
			gs.budgets.Store(mc.Budgets[j].ID, &mc.Budgets[j])
		}
		if mc.RateLimit != nil {
			mc.RateLimit.IsCalendarAligned = mc.CalendarAligned
			gs.rateLimits.Store(mc.RateLimit.ID, mc.RateLimit)
		}
		scopeID := ""
		if mc.ScopeID != nil {
			scopeID = *mc.ScopeID
		}
		if mc.Provider != nil {
			// Provider-specific: store under (scope-prefixed) "modelName:provider" key
			key := modelConfigStoreKey(mc.Scope, scopeID, mc.ModelName, mc.Provider)
			gs.modelConfigs.Store(key, mc)
		} else {
			// All-provider config - store under normalized (scope-prefixed) model name.
			// The "*" (all-models) sentinel is never normalized.
			modelKey := mc.ModelName
			if gs.modelCatalog != nil && mc.ModelName != modelConfigWildcard {
				modelKey = gs.modelCatalog.GetBaseModelName(mc.ModelName)
			}
			key := modelConfigStoreKey(mc.Scope, scopeID, modelKey, nil)
			gs.modelConfigs.Store(key, mc)
		}
	}

	// Stamp customer-owned budget and rate-limit entries in the flat caches so
	// calendar-aligned resets survive restarts and reloads. Mirrors the model-config
	// stamping above.
	for i := range customers {
		customer := &customers[i]
		for j := range customer.Budgets {
			customer.Budgets[j].IsCalendarAligned = customer.CalendarAligned
			gs.budgets.Store(customer.Budgets[j].ID, &customer.Budgets[j])
		}
		if customer.RateLimitID != nil {
			if raw, ok := gs.rateLimits.Load(*customer.RateLimitID); ok {
				if rl, ok := raw.(*configstoreTables.TableRateLimit); ok && rl != nil {
					rl.IsCalendarAligned = customer.CalendarAligned
				}
			}
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

	// See collectBudgetsFromHierarchy: when a team-VK request is scoped to a specific
	// customer, only charge the scalar team.CustomerID customer if it is the scoped one.
	scopedCustomerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceScopedCustomerID).(string)

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

				// Check if team belongs to a customer. Skip charging it when the request
				// is scoped to a different customer (header-driven, team-VK path).
				if team.CustomerID != nil {
					teamCustomerID = *team.CustomerID
					chargeTeamCustomer := scopedCustomerID == "" || scopedCustomerID == teamCustomerID
					if customerValue, exists := gs.customers.Load(*team.CustomerID); chargeTeamCustomer && exists && customerValue != nil {
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
func (gs *LocalGovernanceStore) collectBudgetsFromHierarchy(ctx context.Context, vk *configstoreTables.TableVirtualKey, requestedProvider schemas.ModelProvider) EntityWiseBudgets {
	if vk == nil {
		return nil
	}
	// When a team-VK request is scoped to a specific customer (x-bf-customer-id /
	// x-bf-customer-name header, resolved and stamped by the enterprise plugin),
	// the scalar team.CustomerID customer is only charged when it is the scoped one;
	// otherwise the enterprise layer charges the scoped customer instead. Empty key
	// (the common case / pure-OSS) leaves behavior unchanged.
	scopedCustomerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceScopedCustomerID).(string)
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

				// Check if team belongs to a customer. Skip charging it when the request
				// is scoped to a different customer (header-driven, team-VK path).
				if team.CustomerID != nil {
					teamCustomerID = *team.CustomerID
					chargeTeamCustomer := scopedCustomerID == "" || scopedCustomerID == teamCustomerID
					if customerValue, exists := gs.customers.Load(*team.CustomerID); chargeTeamCustomer && exists && customerValue != nil {
						if customer, ok := customerValue.(*configstoreTables.TableCustomer); ok && customer != nil {
							for _, cb := range customer.Budgets {
								if budgetValue, exists := gs.budgets.Load(cb.ID); exists && budgetValue != nil {
									if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
										if entityWiseBudgets["Customer"] == nil {
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
				for _, cb := range customer.Budgets {
					if budgetValue, exists := gs.budgets.Load(cb.ID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
							if entityWiseBudgets["Customer"] == nil {
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

// CollectApplicableGovernanceIDs returns the budget and rate-limit IDs that are
// affected by a request with the given virtual key, provider, and model.
// It combines provider-level, model-level, and VK-hierarchy (team/customer) IDs.
// All lookups are fast in-memory sync.Map reads.
func (gs *LocalGovernanceStore) CollectApplicableGovernanceIDs(ctx context.Context, virtualKey string, userID string, provider schemas.ModelProvider, model string) (budgetIDs []string, rateLimitIDs []string) {
	seenBudgets := map[string]bool{}
	seenRateLimits := map[string]bool{}

	// --- Provider-level ---
	if provider != "" {
		providerKey := string(provider)
		if value, exists := gs.providers.Load(providerKey); exists && value != nil {
			if pt, ok := value.(*configstoreTables.TableProvider); ok && pt != nil {
				if pt.BudgetID != nil && !seenBudgets[*pt.BudgetID] {
					budgetIDs = append(budgetIDs, *pt.BudgetID)
					seenBudgets[*pt.BudgetID] = true
				}
				if pt.RateLimitID != nil && !seenRateLimits[*pt.RateLimitID] {
					rateLimitIDs = append(rateLimitIDs, *pt.RateLimitID)
					seenRateLimits[*pt.RateLimitID] = true
				}
			}
		}
	}

	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}
	// addModelConfigIDs accumulates the (multi-)budget and rate-limit IDs owned by a
	// model config, matching what the enforcement/recording paths count.
	addModelConfigIDs := func(mc *configstoreTables.TableModelConfig) {
		for i := range mc.Budgets {
			if id := mc.Budgets[i].ID; !seenBudgets[id] {
				budgetIDs = append(budgetIDs, id)
				seenBudgets[id] = true
			}
		}
		if mc.RateLimitID != nil && !seenRateLimits[*mc.RateLimitID] {
			rateLimitIDs = append(rateLimitIDs, *mc.RateLimitID)
			seenRateLimits[*mc.RateLimitID] = true
		}
	}

	// --- Model-level (global scope), all four tiers incl. provider/all-models wildcards ---
	if model != "" {
		for _, mc := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, providerStr) {
			addModelConfigIDs(mc)
		}
	}

	// --- User-scoped model configs (user / AP path) ---
	if userID != "" && model != "" {
		for _, mc := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeUser, userID, model, providerStr) {
			addModelConfigIDs(mc)
		}
	}

	// --- VK hierarchy (VK-scoped model configs + team/customer) ---
	if virtualKey != "" {
		if vk, exists := gs.GetVirtualKey(ctx, virtualKey); exists && vk != nil {
			// VK-scoped model configs (provider-level + all-models wildcards).
			if model != "" {
				for _, scope := range nonGlobalModelConfigScopeChain(vk) {
					for _, mc := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, providerStr) {
						addModelConfigIDs(mc)
					}
				}
			}
			for _, id := range gs.collectBudgetIDsFromMemory(ctx, vk, provider) {
				if !seenBudgets[id] {
					budgetIDs = append(budgetIDs, id)
					seenBudgets[id] = true
				}
			}
			for _, id := range gs.collectRateLimitIDsFromMemory(ctx, vk, provider) {
				if !seenRateLimits[id] {
					rateLimitIDs = append(rateLimitIDs, id)
					seenRateLimits[id] = true
				}
			}
		}
	}

	return budgetIDs, rateLimitIDs
}

// PUBLIC API METHODS

// CreateVirtualKeyInMemory adds a new virtual key to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey) {
	if vk == nil {
		return // Nothing to create
	}

	clone := *vk

	// Clone provider configs
	if vk.ProviderConfigs != nil {
		clone.ProviderConfigs = make([]configstoreTables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
		copy(clone.ProviderConfigs, vk.ProviderConfigs)
	}

	// Store budgets
	for i := range clone.Budgets {
		clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
		gs.budgets.Store(clone.Budgets[i].ID, &clone.Budgets[i])
	}

	// Create associated rate limit if exists
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Create provider config budgets and rate limits if they exist
	if clone.ProviderConfigs != nil {
		for i := range clone.ProviderConfigs {
			pc := &clone.ProviderConfigs[i]
			for j := range pc.Budgets {
				pc.Budgets[j].IsCalendarAligned = clone.CalendarAligned
				gs.budgets.Store(pc.Budgets[j].ID, &pc.Budgets[j])
			}
			if pc.RateLimit != nil {
				pc.RateLimit.IsCalendarAligned = clone.CalendarAligned
				gs.rateLimits.Store(pc.RateLimit.ID, pc.RateLimit)
			}
		}
	}

	gs.storeVirtualKey(clone.Value.GetValue(), &clone)
}

// UpdateVirtualKeyInMemory updates an existing virtual key in the in-memory store (lock-free)
func (gs *LocalGovernanceStore) UpdateVirtualKeyInMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey, budgetBaselines map[string]float64, rateLimitTokensBaselines map[string]int64, rateLimitRequestsBaselines map[string]int64) {
	if vk == nil {
		return // Nothing to update
	}

	// Do not update the current usage of the rate limit, as it will be updated by the usage tracker.
	// But update if max limit or reset duration changes.
	existingVKKey := vk.Value.GetValue()
	existingVKValue, exists := gs.virtualKeys.Load(vk.Value.GetValue())
	if exists && existingVKValue != nil {
		if existingVK, ok := existingVKValue.(*configstoreTables.TableVirtualKey); !ok || existingVK == nil || existingVK.ID != vk.ID {
			exists = false
			existingVKValue = nil
		}
	}
	if !exists || existingVKValue == nil {
		gs.virtualKeys.Range(func(key, value interface{}) bool {
			existingVK, ok := value.(*configstoreTables.TableVirtualKey)
			if !ok || existingVK == nil || existingVK.ID != vk.ID {
				return true
			}
			existingVKKey, _ = key.(string)
			existingVKValue = value
			exists = true
			return false
		})
	}
	if exists && existingVKValue != nil {
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
			clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
			gs.budgets.Store(clone.Budgets[i].ID, &clone.Budgets[i])
		}
		// Delete removed multi-budgets
		for _, oldBudget := range existingVK.Budgets {
			if !allNewBudgetIDs[oldBudget.ID] {
				gs.DeleteBudget(ctx, oldBudget.ID)
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
			clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
			// Update the rate limit in the main rateLimits sync.Map
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
			// Clean up old rate limit if ID changed (e.g., after AP propagation
			// creates a fresh UUID). Without this the orphaned entry leaks memory
			// and its stale usage pollutes gossip baselines.
			if existingVK.RateLimit != nil && existingVK.RateLimit.ID != clone.RateLimit.ID {
				gs.DeleteRateLimit(ctx, existingVK.RateLimit.ID)
			}
		} else if existingVK.RateLimit != nil {
			// Rate limit was removed from the virtual key, delete it from memory
			gs.DeleteRateLimit(ctx, existingVK.RateLimit.ID)
		}
		if clone.ProviderConfigs != nil {
			// Create a map of existing provider configs by ID for fast lookup
			existingProviderConfigs := make(map[uint]configstoreTables.TableVirtualKeyProviderConfig)
			if existingVK.ProviderConfigs != nil {
				for _, existingPC := range existingVK.ProviderConfigs {
					existingProviderConfigs[existingPC.ID] = existingPC
				}
			}

			// Collect all new rate limit IDs from new provider configs
			allNewRateLimitIDs := make(map[string]bool)
			if clone.RateLimit != nil {
				allNewRateLimitIDs[clone.RateLimit.ID] = true
			}
			for _, pc := range clone.ProviderConfigs {
				if pc.RateLimit != nil {
					allNewRateLimitIDs[pc.RateLimit.ID] = true
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
					clone.ProviderConfigs[i].RateLimit.IsCalendarAligned = clone.CalendarAligned
					gs.rateLimits.Store(clone.ProviderConfigs[i].RateLimit.ID, clone.ProviderConfigs[i].RateLimit)
				} else {
					// Rate limit was removed from provider config, delete it from memory if it existed
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.RateLimit != nil {
						gs.DeleteRateLimit(ctx, existingPC.RateLimit.ID)
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
					b.IsCalendarAligned = clone.CalendarAligned
					gs.budgets.Store(b.ID, b)
				}
				// Delete removed multi-budgets for this provider config
				if existingPC, exists := existingProviderConfigs[pc.ID]; exists {
					for _, oldBudget := range existingPC.Budgets {
						if !allNewBudgetIDs[oldBudget.ID] {
							gs.DeleteBudget(ctx, oldBudget.ID)
						}
					}
				}
			}
			// Clean up orphaned rate limits and budgets from old provider configs
			// whose IDs changed (e.g., AP propagation replaces all configs with
			// new DB row IDs). Without this, stale entries leak memory and
			// pollute gossip baselines.
			for _, oldPC := range existingVK.ProviderConfigs {
				if oldPC.RateLimit != nil && !allNewRateLimitIDs[oldPC.RateLimit.ID] {
					gs.DeleteRateLimit(ctx, oldPC.RateLimit.ID)
				}
				for _, oldBudget := range oldPC.Budgets {
					if !allNewBudgetIDs[oldBudget.ID] {
						gs.DeleteBudget(ctx, oldBudget.ID)
					}
				}
			}
		}
		if existingVKKey != "" && existingVKKey != vk.Value.GetValue() {
			gs.deleteVirtualKeyByValue(existingVKKey)
		}
		gs.storeVirtualKey(vk.Value.GetValue(), &clone)
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
				gs.DeleteBudget(ctx, b.ID)
			}

			// Delete associated rate limit if exists
			if vk.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *vk.RateLimitID)
			}

			// Delete provider config budgets and rate limits
			if vk.ProviderConfigs != nil {
				for _, pc := range vk.ProviderConfigs {
					for _, b := range pc.Budgets {
						gs.DeleteBudget(ctx, b.ID)
					}
					if pc.RateLimitID != nil {
						gs.DeleteRateLimit(ctx, *pc.RateLimitID)
					}
				}
			}

			gs.deleteVirtualKeyByValue(key.(string))
			return false // stop iteration
		}
		return true // continue iteration
	})

	// Evict any model configs scoped to this virtual key (and their budgets/rate-limits).
	// Mirrors the DB-side cleanup in DeleteVirtualKey and keeps the in-memory store
	// consistent even when the VK entry was already removed.
	gs.DeleteModelConfigsForScopeInMemory(ctx, configstoreTables.ModelConfigScopeVirtualKey, vkID)
}

// DeleteModelConfigsForScopeInMemory evicts every cached model config targeting the
// given scope owner (e.g. scope=virtual_key, scopeID=<vk id>) along with the budgets
// and rate-limits those configs own. It is the in-memory mirror of
// RDBConfigStore.DeleteModelConfigsForScope; every owner-eviction path routes through
// here so the cleanup lives in one place. Exported so out-of-package owner-eviction
// paths (e.g. the enterprise user-deletion flow) reuse it. Owned budgets are released
// from both the active Budgets slice and the legacy single BudgetID column.
func (gs *LocalGovernanceStore) DeleteModelConfigsForScopeInMemory(ctx context.Context, scope, scopeID string) {
	gs.modelConfigs.Range(func(key, value any) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true
		}
		if mc.Scope == scope && mc.ScopeID != nil && *mc.ScopeID == scopeID {
			for i := range mc.Budgets {
				gs.DeleteBudget(ctx, mc.Budgets[i].ID)
			}
			if mc.BudgetID != nil {
				gs.DeleteBudget(ctx, *mc.BudgetID)
			}
			if mc.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *mc.RateLimitID)
			}
			gs.modelConfigs.Delete(key)
		}
		return true
	})
}

// CreateTeamInMemory adds a new team to the in-memory store (lock-free)
func (gs *LocalGovernanceStore) CreateTeamInMemory(ctx context.Context, team *configstoreTables.TableTeam) {
	if team == nil {
		return // Nothing to create
	}

	clone := *team

	// Create associated budgets if they exist
	for i := range clone.Budgets {
		clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
		b := clone.Budgets[i]
		gs.budgets.Store(b.ID, &b)
	}

	// Create associated rate limit if exists
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	gs.teams.Store(clone.ID, &clone)
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
			b.IsCalendarAligned = clone.CalendarAligned
			gs.budgets.Store(b.ID, b)
		}
		for id := range existingBudgetIDs {
			if _, stillThere := nextBudgetIDs[id]; !stillThere {
				gs.DeleteBudget(ctx, id)
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
			clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
			gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
			// Clean up old rate limit if ID changed (e.g., UUID rotation on propagation)
			if existingTeam.RateLimit != nil && existingTeam.RateLimit.ID != clone.RateLimit.ID {
				gs.DeleteRateLimit(ctx, existingTeam.RateLimit.ID)
			}
		} else if existingTeam.RateLimit != nil {
			// Rate limit was removed from the team, delete it from memory
			gs.DeleteRateLimit(ctx, existingTeam.RateLimit.ID)
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
				gs.DeleteBudget(ctx, b.ID)
			}
			// Delete associated rate limit if exists
			if team.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *team.RateLimitID)
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
			gs.storeVirtualKey(key.(string), &clone)
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
	clone := *customer
	for i := range clone.Budgets {
		clone.Budgets[i].IsCalendarAligned = clone.CalendarAligned
		gs.budgets.Store(clone.Budgets[i].ID, &clone.Budgets[i])
	}
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}
	gs.customers.Store(clone.ID, &clone)
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

		// Reconcile budgets: upsert new set, delete any that were removed.
		newBudgetIDs := make(map[string]bool, len(clone.Budgets))
		for i := range clone.Budgets {
			b := &clone.Budgets[i]
			b.IsCalendarAligned = clone.CalendarAligned
			if existingBudgetValue, exists := gs.budgets.Load(b.ID); exists && existingBudgetValue != nil {
				if existingBudget, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && existingBudget != nil {
					b.CurrentUsage = existingBudget.CurrentUsage
					b.LastReset = existingBudget.LastReset
				}
			}
			gs.budgets.Store(b.ID, b)
			newBudgetIDs[b.ID] = true
		}
		for _, existing := range existingCustomer.Budgets {
			if !newBudgetIDs[existing.ID] {
				gs.DeleteBudget(ctx, existing.ID)
			}
		}

		// Handle rate limit updates with consistent logic
		if clone.RateLimit != nil {
			clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
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
			// Clean up old rate limit if ID changed (e.g., UUID rotation on propagation)
			if existingCustomer.RateLimit != nil && existingCustomer.RateLimit.ID != clone.RateLimit.ID {
				gs.DeleteRateLimit(ctx, existingCustomer.RateLimit.ID)
			}
		} else if existingCustomer.RateLimit != nil {
			// Rate limit was removed from the customer, delete it from memory
			gs.DeleteRateLimit(ctx, existingCustomer.RateLimit.ID)
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
			for _, b := range customer.Budgets {
				gs.DeleteBudget(ctx, b.ID)
			}
			// Delete associated rate limit if exists
			if customer.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *customer.RateLimitID)
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
			gs.storeVirtualKey(key.(string), &clone)
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

func (gs *LocalGovernanceStore) CreateUserNameInMemory(ctx context.Context, userID string, userName string) {
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

	// Store associated budgets, preserving existing in-memory usage per budget ID and
	// stamping calendar alignment from the model config (consumed by the reset path).
	for i := range clone.Budgets {
		b := &clone.Budgets[i]
		b.IsCalendarAligned = clone.CalendarAligned
		if existingBudgetValue, exists := gs.budgets.Load(b.ID); exists && existingBudgetValue != nil {
			if eb, ok := existingBudgetValue.(*configstoreTables.TableBudget); ok && eb != nil {
				b.CurrentUsage = eb.CurrentUsage
				b.LastReset = eb.LastReset
			}
		}
		gs.budgets.Store(b.ID, b)
	}

	// Store associated rate limit if exists, preserving existing in-memory usage and
	// stamping calendar alignment from the owning model config.
	if clone.RateLimit != nil {
		clone.RateLimit.IsCalendarAligned = clone.CalendarAligned
		if existingRateLimitValue, exists := gs.rateLimits.Load(clone.RateLimit.ID); exists && existingRateLimitValue != nil {
			if erl, ok := existingRateLimitValue.(*configstoreTables.TableRateLimit); ok && erl != nil {
				clone.RateLimit.TokenCurrentUsage = erl.TokenCurrentUsage
				clone.RateLimit.RequestCurrentUsage = erl.RequestCurrentUsage
			}
		}
		gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
	}

	// Determine the (scope-aware) key. Global scope keeps the historical key format;
	// non-global scopes are namespaced by modelConfigStoreKey. Scope/scope_id are part of
	// a config's identity and do not change on update, so this matches the stored key.
	scopeID := ""
	if clone.ScopeID != nil {
		scopeID = *clone.ScopeID
	}
	if clone.Provider != nil {
		key := modelConfigStoreKey(clone.Scope, scopeID, clone.ModelName, clone.Provider)
		gs.modelConfigs.Store(key, &clone)
	} else {
		modelKey := clone.ModelName
		if gs.modelCatalog != nil && clone.ModelName != modelConfigWildcard {
			modelKey = gs.modelCatalog.GetBaseModelName(clone.ModelName)
		}
		key := modelConfigStoreKey(clone.Scope, scopeID, modelKey, nil)
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
			// Delete associated budgets if any
			for i := range mc.Budgets {
				gs.DeleteBudget(ctx, mc.Budgets[i].ID)
			}

			// Delete associated rate limit if exists
			if mc.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *mc.RateLimitID)
			}

			gs.modelConfigs.Delete(key)
			return false // stop iteration
		}
		return true // continue iteration
	})
}

// ScopedModelConfigIDs returns the IDs of all in-memory model configs for the
// given (scope, scopeID). Callers use this to diff against the DB result and
// evict stale entries via DeleteModelConfigInMemory.
func (gs *LocalGovernanceStore) ScopedModelConfigIDs(scope, scopeID string) []string {
	var ids []string
	gs.modelConfigs.Range(func(key, value interface{}) bool {
		mc, ok := value.(*configstoreTables.TableModelConfig)
		if !ok || mc == nil {
			return true
		}
		mcScopeID := ""
		if mc.ScopeID != nil {
			mcScopeID = *mc.ScopeID
		}
		if mc.Scope == scope && mcScopeID == scopeID {
			ids = append(ids, mc.ID)
		}
		return true
	})
	return ids
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
				gs.DeleteBudget(ctx, *provider.BudgetID)
			}

			// Delete associated rate limit if exists
			if provider.RateLimitID != nil {
				gs.DeleteRateLimit(ctx, *provider.RateLimitID)
			}
		}
	}
	gs.providers.Delete(providerName)
}

// Helper functions

// updateBudgetReferences updates all VKs, teams, customers, and provider configs
// that reference any of the reset budgets. It makes ONE pass over each owner map
// regardless of how many budgets reset, and clones an owner only after a match,
// so a background sweep costs O(owners + resets) instead of O(owners x resets)
// with a clone per owner per reset. Essential at large key counts; the contract
// is pinned by TestBackgroundResetReferenceRefreshScales.
func (gs *LocalGovernanceStore) updateBudgetReferences(ctx context.Context, resetBudgets []*configstoreTables.TableBudget) {
	if len(resetBudgets) == 0 {
		return
	}
	resets := make(map[string]*configstoreTables.TableBudget, len(resetBudgets))
	for _, b := range resetBudgets {
		if b != nil {
			resets[b.ID] = b
		}
	}
	// Update VKs that reference these budgets
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		vkMatch := false
		for i := range vk.Budgets {
			if resets[vk.Budgets[i].ID] != nil {
				vkMatch = true
				break
			}
		}
		pcMatch := false
		for i := range vk.ProviderConfigs {
			for j := range vk.ProviderConfigs[i].Budgets {
				if resets[vk.ProviderConfigs[i].Budgets[j].ID] != nil {
					pcMatch = true
					break
				}
			}
			if pcMatch {
				break
			}
		}
		if !vkMatch && !pcMatch {
			return true // continue
		}
		clone := *vk
		if vkMatch {
			clone.Budgets = append([]configstoreTables.TableBudget(nil), vk.Budgets...)
			for i := range clone.Budgets {
				if b := resets[clone.Budgets[i].ID]; b != nil {
					clone.Budgets[i] = *b
				}
			}
		}
		if pcMatch {
			clone.ProviderConfigs = append([]configstoreTables.TableVirtualKeyProviderConfig(nil), vk.ProviderConfigs...)
			for i := range clone.ProviderConfigs {
				matched := false
				for j := range clone.ProviderConfigs[i].Budgets {
					if resets[clone.ProviderConfigs[i].Budgets[j].ID] != nil {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
				budgets := append([]configstoreTables.TableBudget(nil), clone.ProviderConfigs[i].Budgets...)
				for j := range budgets {
					if b := resets[budgets[j].ID]; b != nil {
						budgets[j] = *b
					}
				}
				clone.ProviderConfigs[i].Budgets = budgets
			}
		}
		gs.storeVirtualKey(key.(string), &clone)
		return true // continue
	})
	// Update teams that reference these budgets
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		matched := false
		for i := range team.Budgets {
			if resets[team.Budgets[i].ID] != nil {
				matched = true
				break
			}
		}
		if !matched {
			return true // continue
		}
		clone := *team
		clone.Budgets = append([]configstoreTables.TableBudget(nil), team.Budgets...)
		for i := range clone.Budgets {
			if b := resets[clone.Budgets[i].ID]; b != nil {
				clone.Budgets[i] = *b
			}
		}
		gs.teams.Store(key, &clone)
		return true // continue
	})
	// Update customers that own these budgets
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		matched := false
		for i := range customer.Budgets {
			if resets[customer.Budgets[i].ID] != nil {
				matched = true
				break
			}
		}
		if !matched {
			return true // continue
		}
		clone := *customer
		clone.Budgets = append([]configstoreTables.TableBudget(nil), customer.Budgets...)
		for i := range clone.Budgets {
			if b := resets[clone.Budgets[i].ID]; b != nil {
				clone.Budgets[i] = *b
			}
		}
		gs.customers.Store(key, &clone)
		return true // continue
	})
}

// updateRateLimitReferences updates all VKs, teams, customers and provider configs
// that reference any of the reset rate limits. It makes ONE pass over each owner
// map regardless of how many rate limits reset, and clones an owner only after a
// match, so a background sweep costs O(owners + resets) instead of
// O(owners x resets) with a clone per owner per reset. Essential at large key
// counts; the contract is pinned by TestBackgroundResetReferenceRefreshScales.
func (gs *LocalGovernanceStore) updateRateLimitReferences(ctx context.Context, resetRateLimits []*configstoreTables.TableRateLimit) {
	if len(resetRateLimits) == 0 {
		return
	}
	resets := make(map[string]*configstoreTables.TableRateLimit, len(resetRateLimits))
	for _, rl := range resetRateLimits {
		if rl != nil {
			resets[rl.ID] = rl
		}
	}
	// Update VKs that reference these rate limits
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		vkMatch := vk.RateLimitID != nil && resets[*vk.RateLimitID] != nil
		pcMatch := false
		for i := range vk.ProviderConfigs {
			if id := vk.ProviderConfigs[i].RateLimitID; id != nil && resets[*id] != nil {
				pcMatch = true
				break
			}
		}
		if !vkMatch && !pcMatch {
			return true // continue
		}
		clone := *vk
		if vkMatch {
			clone.RateLimit = resets[*vk.RateLimitID]
		}
		if pcMatch {
			clone.ProviderConfigs = append([]configstoreTables.TableVirtualKeyProviderConfig(nil), vk.ProviderConfigs...)
			for i := range clone.ProviderConfigs {
				if id := clone.ProviderConfigs[i].RateLimitID; id != nil {
					if rl := resets[*id]; rl != nil {
						clone.ProviderConfigs[i].RateLimit = rl
					}
				}
			}
		}
		gs.storeVirtualKey(key.(string), &clone)
		return true // continue
	})
	// Update teams that reference these rate limits
	gs.teams.Range(func(key, value interface{}) bool {
		team, ok := value.(*configstoreTables.TableTeam)
		if !ok || team == nil {
			return true // continue
		}
		if team.RateLimitID != nil {
			if rl := resets[*team.RateLimitID]; rl != nil {
				clone := *team
				clone.RateLimit = rl
				gs.teams.Store(key, &clone)
			}
		}
		return true // continue
	})
	// Update customers that reference these rate limits
	gs.customers.Range(func(key, value interface{}) bool {
		customer, ok := value.(*configstoreTables.TableCustomer)
		if !ok || customer == nil {
			return true // continue
		}
		if customer.RateLimitID != nil {
			if rl := resets[*customer.RateLimitID]; rl != nil {
				clone := *customer
				clone.RateLimit = rl
				gs.customers.Store(key, &clone)
			}
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
		if rule.EnabledValue() {
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

	// Create program. Partial evaluation is only needed for complexity rules,
	// where routing treats unavailable complexity_tier as unknown instead of
	// leaking an empty-string sentinel.
	var program cel.Program
	var err error
	if celASTReferencesIdentifier(ast, "complexity_tier") {
		program, err = gs.routingCELEnv.Program(ast, cel.EvalOptions(cel.OptPartialEval))
	} else {
		program, err = gs.routingCELEnv.Program(ast)
	}
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

	var providerStr *string
	if provider != "" {
		p := string(provider)
		providerStr = &p
	}

	// applyModelConfig folds a model config's rate-limit and budgets into the running max.
	applyModelConfig := func(modelConfig *configstoreTables.TableModelConfig) {
		if modelConfig.RateLimitID != nil {
			if rateLimitValue, ok := gs.rateLimits.Load(*modelConfig.RateLimitID); ok && rateLimitValue != nil {
				if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
					if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
						tokenPercent := float64(rateLimit.TokenCurrentUsage+tokenBaselines[rateLimit.ID]) / float64(*rateLimit.TokenMaxLimit) * 100
						if tokenPercent > result.RateLimitTokenPercentUsed {
							result.RateLimitTokenPercentUsed = tokenPercent
						}
					}
					// Calculate request percent used
					if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
						requestPercent := float64(rateLimit.RequestCurrentUsage+requestBaselines[rateLimit.ID]) / float64(*rateLimit.RequestMaxLimit) * 100
						if requestPercent > result.RateLimitRequestPercentUsed {
							result.RateLimitRequestPercentUsed = requestPercent
						}
					}
				}
			}
		}
		// Get budget status (max percent across the config's budgets)
		for bi := range modelConfig.Budgets {
			if budgetValue, ok := gs.budgets.Load(modelConfig.Budgets[bi].ID); ok && budgetValue != nil {
				if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
					if effectiveMaxLimit := budget.EffectiveMaxLimit(); effectiveMaxLimit > 0 {
						budgetPercent := float64(budget.CurrentUsage+budgetBaselines[budget.ID]) / effectiveMaxLimit * 100
						if budgetPercent > result.BudgetPercentUsed {
							result.BudgetPercentUsed = budgetPercent
						}
					}
				}
			}
		}
	}

	// Model-level (global scope), all tiers incl. the "*:provider" / "*:nil" wildcards
	// that carry provider-level governance after the provider-governance migration.
	if model != "" {
		for _, modelConfig := range gs.collectModelConfigsFor(ctx, configstoreTables.ModelConfigScopeGlobal, "", model, providerStr) {
			applyModelConfig(modelConfig)
		}
	}

	// VK-scoped model configs. The per-VK provider budget set via the model limits UI now
	// lives here (scope=virtual_key, model="*"); before the provider-governance migration it
	// was read from vk.ProviderConfigs below. Mirror the scope walk enforcement uses.
	if model != "" && vk != nil {
		for _, scope := range nonGlobalModelConfigScopeChain(vk) {
			for _, modelConfig := range gs.collectModelConfigsFor(ctx, scope.name, scope.id, model, providerStr) {
				applyModelConfig(modelConfig)
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
						// Calculate token percent used
						if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
							tokenPercent := float64(rateLimit.TokenCurrentUsage+tokenBaselines[rateLimit.ID]) / float64(*rateLimit.TokenMaxLimit) * 100
							if tokenPercent > result.RateLimitTokenPercentUsed {
								result.RateLimitTokenPercentUsed = tokenPercent
							}
						}
						// Calculate request percent used
						if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
							requestPercent := float64(rateLimit.RequestCurrentUsage+requestBaselines[rateLimit.ID]) / float64(*rateLimit.RequestMaxLimit) * 100
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
						if effectiveMaxLimit := budget.EffectiveMaxLimit(); effectiveMaxLimit > 0 {
							budgetPercent := float64(budget.CurrentUsage+budgetBaselines[budget.ID]) / effectiveMaxLimit * 100
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
	// NO LONGER NEEDED - provider budgets are now handled in model configs under the virtual key scope. Keeping this code here for now, but it can be removed.
	if vk != nil {
		if vk.ProviderConfigs != nil {
			for _, pc := range vk.ProviderConfigs {
				if pc.Provider == string(provider) {
					// Get rate limit status
					if pc.RateLimit != nil {
						// Look up canonical rate limit from gs.rateLimits
						if rateLimitValue, ok := gs.rateLimits.Load(pc.RateLimit.ID); ok && rateLimitValue != nil {
							if rateLimit, ok := rateLimitValue.(*configstoreTables.TableRateLimit); ok && rateLimit != nil {
								// Calculate token percent used
								if rateLimit.TokenMaxLimit != nil && *rateLimit.TokenMaxLimit > 0 {
									tokenPercent := float64(rateLimit.TokenCurrentUsage+tokenBaselines[rateLimit.ID]) / float64(*rateLimit.TokenMaxLimit) * 100
									if tokenPercent > result.RateLimitTokenPercentUsed {
										result.RateLimitTokenPercentUsed = tokenPercent
									}
								}
								// Calculate request percent used
								if rateLimit.RequestMaxLimit != nil && *rateLimit.RequestMaxLimit > 0 {
									requestPercent := float64(rateLimit.RequestCurrentUsage+requestBaselines[rateLimit.ID]) / float64(*rateLimit.RequestMaxLimit) * 100
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
								if effectiveMaxLimit := budget.EffectiveMaxLimit(); effectiveMaxLimit > 0 {
									budgetPercent := float64(budget.CurrentUsage+budgetBaselines[budget.ID]) / effectiveMaxLimit * 100
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
