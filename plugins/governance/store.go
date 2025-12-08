// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// LocalGovernanceStore provides in-memory cache for governance data with fast, non-blocking access
type LocalGovernanceStore struct {
	// Core data maps using sync.Map for lock-free reads
	virtualKeys sync.Map // string -> *VirtualKey (VK value -> VirtualKey with preloaded relationships)
	teams       sync.Map // string -> *Team (Team ID -> Team)
	customers   sync.Map // string -> *Customer (Customer ID -> Customer)
	budgets     sync.Map // string -> *Budget (Budget ID -> Budget)
	rateLimits  sync.Map // string -> *RateLimit (RateLimit ID -> RateLimit)

	// Config store for refresh operations
	configStore configstore.ConfigStore

	// Logger
	logger schemas.Logger
}

type GovernanceData struct {
	VirtualKeys map[string]*configstoreTables.TableVirtualKey `json:"virtual_keys"`
	Teams       map[string]*configstoreTables.TableTeam       `json:"teams"`
	Customers   map[string]*configstoreTables.TableCustomer   `json:"customers"`
	Budgets     map[string]*configstoreTables.TableBudget     `json:"budgets"`
	RateLimits  map[string]*configstoreTables.TableRateLimit  `json:"rate_limits"`
}

type GovernanceStore interface {
	GetGovernanceData() *GovernanceData
	GetVirtualKey(vkValue string) (*configstoreTables.TableVirtualKey, bool)
	GetAllBudgets() map[string]*configstoreTables.TableBudget
	GetAllRateLimits() map[string]*configstoreTables.TableRateLimit
	CheckBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest, baselines map[string]float64) error
	CheckRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, model string, requestID string, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (error, Decision)
	UpdateBudgetUsage(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, cost float64) error
	UpdateRateLimitUsage(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error
	ResetExpiredRateLimits(ctx context.Context) error
	ResetExpiredBudgets(ctx context.Context) error
	DumpRateLimits(ctx context.Context, tokenBaselines map[string]int64, requestBaselines map[string]int64) error
	DumpBudgets(ctx context.Context, baselines map[string]float64) error
	CreateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey)
	UpdateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey)
	DeleteVirtualKeyInMemory(vkID string)
	CreateTeamInMemory(team *configstoreTables.TableTeam)
	UpdateTeamInMemory(team *configstoreTables.TableTeam)
	DeleteTeamInMemory(teamID string)
	CreateCustomerInMemory(customer *configstoreTables.TableCustomer)
	UpdateCustomerInMemory(customer *configstoreTables.TableCustomer)
	DeleteCustomerInMemory(customerID string)
}

// NewLocalGovernanceStore creates a new in-memory governance store
func NewLocalGovernanceStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore, governanceConfig *configstore.GovernanceConfig) (*LocalGovernanceStore, error) {
	store := &LocalGovernanceStore{
		configStore: configStore,
		logger:      logger,
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
		virtualKeys[key.(string)] = vk
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
	return &GovernanceData{
		VirtualKeys: virtualKeys,
		Teams:       teams,
		Customers:   customers,
		Budgets:     budgets,
		RateLimits:  rateLimits,
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

// GetAllBudgets returns all budgets (for background reset operations)
func (gs *LocalGovernanceStore) GetAllBudgets() map[string]*configstoreTables.TableBudget {
	result := make(map[string]*configstoreTables.TableBudget)
	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		keyStr, keyOk := key.(string)
		budget, budgetOk := value.(*configstoreTables.TableBudget)

		if keyOk && budgetOk && budget != nil {
			result[keyStr] = budget // Store budget by ID
		}
		return true // continue iteration
	})
	return result
}

// GetAllRateLimits returns all rate limits (for background reset operations)
func (gs *LocalGovernanceStore) GetAllRateLimits() map[string]*configstoreTables.TableRateLimit {
	result := make(map[string]*configstoreTables.TableRateLimit)
	gs.rateLimits.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		keyStr, keyOk := key.(string)
		rateLimit, rateLimitOk := value.(*configstoreTables.TableRateLimit)

		if keyOk && rateLimitOk && rateLimit != nil {
			result[keyStr] = rateLimit // Store rate limit by ID
		}
		return true // continue iteration
	})
	return result
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

	// Use helper to collect budgets and their names (lock-free)
	budgetsToCheck, budgetNames := gs.collectBudgetsFromHierarchy(vk, request.Provider)

	// Check each budget in hierarchy order using in-memory data
	for i, budget := range budgetsToCheck {
		// Check if budget needs reset (in-memory check)
		if budget.ResetDuration != "" {
			if duration, err := configstoreTables.ParseDuration(budget.ResetDuration); err == nil {
				if time.Since(budget.LastReset).Round(time.Millisecond) >= duration {
					// Budget expired but hasn't been reset yet - treat as reset
					// Note: actual reset will happen in post-hook via AtomicBudgetUpdate
					continue // Skip budget check for expired budgets
				}
			}
		}

		baseline, exists := baselines[budget.ID]
		if !exists {
			baseline = 0
		}

		// Check if current usage exceeds budget limit
		if budget.CurrentUsage >= budget.MaxLimit+baseline {
			return fmt.Errorf("%s budget exceeded: %.4f > %.4f dollars",
				budgetNames[i], budget.CurrentUsage, budget.MaxLimit+baseline)
		}
	}

	return nil
}

// CheckRateLimit checks a single rate limit and returns evaluation result if violated (true if violated, false if not)
func (gs *LocalGovernanceStore) CheckRateLimit(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, model string, requestID string, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (error, Decision) {
	var violations []string

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
		// Check if rate limit needs reset (in-memory check)
		if rateLimit.TokenResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
				if time.Since(rateLimit.TokenLastReset).Round(time.Millisecond) >= duration {
					// Token rate limit expired but hasn't been reset yet - skip check
					// Note: actual reset will happen in post-hook via AtomicRateLimitUpdate
					continue // Skip rate limit check for expired rate limits
				}
			}
		}
		if rateLimit.RequestResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
				if time.Since(rateLimit.RequestLastReset).Round(time.Millisecond) >= duration {
					// Request rate limit expired but hasn't been reset yet - skip check
					// Note: actual reset will happen in post-hook via AtomicRateLimitUpdate
					continue // Skip rate limit check for expired rate limits
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

		// Token limits
		if rateLimit.TokenMaxLimit != nil && rateLimit.TokenCurrentUsage >= *rateLimit.TokenMaxLimit+tokensBaseline {
			duration := "unknown"
			if rateLimit.TokenResetDuration != nil {
				duration = *rateLimit.TokenResetDuration
			}
			violations = append(violations, fmt.Sprintf("token limit exceeded (%d/%d, resets every %s)",
				rateLimit.TokenCurrentUsage, *rateLimit.TokenMaxLimit+tokensBaseline, duration))
		}

		// Request limits
		if rateLimit.RequestMaxLimit != nil && rateLimit.RequestCurrentUsage >= *rateLimit.RequestMaxLimit+requestsBaseline {
			duration := "unknown"
			if rateLimit.RequestResetDuration != nil {
				duration = *rateLimit.RequestResetDuration
			}
			violations = append(violations, fmt.Sprintf("request limit exceeded (%d/%d, resets every %s)",
				rateLimit.RequestCurrentUsage, *rateLimit.RequestMaxLimit+requestsBaseline, duration))
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

// UpdateBudgetUsage performs atomic budget updates across the hierarchy (both in memory and in database)
func (gs *LocalGovernanceStore) UpdateBudgetUsage(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, cost float64) error {
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
				// Check if budget needs reset (in-memory check)
				if cachedBudget.ResetDuration != "" {
					if duration, err := configstoreTables.ParseDuration(cachedBudget.ResetDuration); err == nil {
						if now.Sub(cachedBudget.LastReset).Round(time.Millisecond) >= duration {
							cachedBudget.CurrentUsage = 0
							cachedBudget.LastReset = now
						}
					}
				}
				clone := *cachedBudget
				clone.CurrentUsage += cost
				gs.budgets.Store(budgetID, &clone)
			}
		}
	}
	return nil
}

// UpdateRateLimitUsage updates rate limit counters for both provider-level and VK-level rate limits (lock-free)
func (gs *LocalGovernanceStore) UpdateRateLimitUsage(ctx context.Context, vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
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
				// Check if rate limit needs reset (in-memory check)
				if cachedRateLimit.TokenResetDuration != nil {
					if duration, err := configstoreTables.ParseDuration(*cachedRateLimit.TokenResetDuration); err == nil {
						if now.Sub(cachedRateLimit.TokenLastReset).Round(time.Millisecond) >= duration {
							cachedRateLimit.TokenCurrentUsage = 0
							cachedRateLimit.TokenLastReset = now
						}
					}
				}
				if cachedRateLimit.RequestResetDuration != nil {
					if duration, err := configstoreTables.ParseDuration(*cachedRateLimit.RequestResetDuration); err == nil {
						if now.Sub(cachedRateLimit.RequestLastReset).Round(time.Millisecond) >= duration {
							cachedRateLimit.RequestCurrentUsage = 0
							cachedRateLimit.RequestLastReset = now
						}
					}
				}
				clone := *cachedRateLimit
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

// ResetExpiredRateLimits performs background reset of expired rate limits for both provider-level and VK-level (lock-free)
func (gs *LocalGovernanceStore) ResetExpiredRateLimits(ctx context.Context) error {
	now := time.Now()
	var resetRateLimits []*configstoreTables.TableRateLimit

	gs.rateLimits.Range(func(key, value interface{}) bool {
		addedToReset := false
		// Type-safe conversion
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true // continue
		}

		if rateLimit.TokenResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
				if now.Sub(rateLimit.TokenLastReset).Round(time.Millisecond) >= duration {
					rateLimit.TokenCurrentUsage = 0
					rateLimit.TokenLastReset = now
					resetRateLimits = append(resetRateLimits, rateLimit)
					addedToReset = true
				}
			}
		}
		if rateLimit.RequestResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
				if now.Sub(rateLimit.RequestLastReset).Round(time.Millisecond) >= duration {
					rateLimit.RequestCurrentUsage = 0
					rateLimit.RequestLastReset = now
					if !addedToReset {
						resetRateLimits = append(resetRateLimits, rateLimit)
					}
				}
			}
		}

		gs.rateLimits.Store(key, rateLimit)
		return true // continue
	})

	// Persist to database if any resets occurred
	if len(resetRateLimits) > 0 && gs.configStore != nil {
		if err := gs.configStore.UpdateRateLimits(ctx, resetRateLimits); err != nil {
			return fmt.Errorf("failed to persist budget resets to database: %w", err)
		}
	}
	return nil
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration (lock-free)
func (gs *LocalGovernanceStore) ResetExpiredBudgets(ctx context.Context) error {
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
			gs.logger.Error("invalid budget reset duration %s: %w", budget.ResetDuration, err)
			return true // continue
		}

		if now.Sub(budget.LastReset) >= duration {
			oldUsage := budget.CurrentUsage
			budget.CurrentUsage = 0
			budget.LastReset = now
			resetBudgets = append(resetBudgets, budget)

			gs.logger.Debug(fmt.Sprintf("Reset budget %s (was %.2f, reset to 0)",
				budget.ID, oldUsage))
		}
		return true // continue
	})

	// Persist to database if any resets occurred
	if len(resetBudgets) > 0 && gs.configStore != nil {
		if err := gs.configStore.UpdateBudgets(ctx, resetBudgets); err != nil {
			return fmt.Errorf("failed to persist budget resets to database: %w", err)
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

	var rateLimits []*configstoreTables.TableRateLimit
	gs.virtualKeys.Range(func(key, value interface{}) bool {
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}
		// Clone the VK to avoid modifying the original
		clone := *vk
		if clone.RateLimit != nil {
			if tokenBaseline, exists := tokenBaselines[clone.RateLimit.ID]; exists {
				clone.RateLimit.TokenCurrentUsage += tokenBaseline
			}
			if requestBaseline, exists := requestBaselines[clone.RateLimit.ID]; exists {
				clone.RateLimit.RequestCurrentUsage += requestBaseline
			}
			rateLimits = append(rateLimits, clone.RateLimit)
		}
		if clone.ProviderConfigs != nil {
			for _, pc := range clone.ProviderConfigs {
				if pc.RateLimit != nil {
					if tokenBaseline, exists := tokenBaselines[pc.RateLimit.ID]; exists {
						pc.RateLimit.TokenCurrentUsage += tokenBaseline
					}
					if requestBaseline, exists := requestBaselines[pc.RateLimit.ID]; exists {
						pc.RateLimit.RequestCurrentUsage += requestBaseline
					}
					rateLimits = append(rateLimits, pc.RateLimit)
				}
			}
		}
		return true // continue
	})

	// Save all updated rate limits to database
	if len(rateLimits) > 0 && gs.configStore != nil {
		if err := gs.configStore.UpdateRateLimits(ctx, rateLimits); err != nil {
			return fmt.Errorf("failed to update rate limit usage: %w", err)
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

	budgets := gs.GetAllBudgets()
	budgetsToDelete := make([]string, 0)
	if err := gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Update each budget atomically
		for _, inMemoryBudget := range budgets {
			// Check if budget exists in database
			var budget configstoreTables.TableBudget
			if err := tx.WithContext(ctx).First(&budget, "id = ?", inMemoryBudget.ID).Error; err != nil {
				// If budget not found then it must be deleted, so we remove it from the in-memory store
				if errors.Is(err, gorm.ErrRecordNotFound) {
					budgetsToDelete = append(budgetsToDelete, inMemoryBudget.ID)
					continue
				}
				return fmt.Errorf("failed to get budget %s: %w", inMemoryBudget.ID, err)
			}

			// Update usage
			if baseline, exists := baselines[inMemoryBudget.ID]; exists {
				budget.CurrentUsage = inMemoryBudget.CurrentUsage + baseline
			} else {
				budget.CurrentUsage = inMemoryBudget.CurrentUsage
			}
			if err := tx.WithContext(ctx).Save(&budget).Error; err != nil {
				return fmt.Errorf("failed to save budget %s: %w", inMemoryBudget.ID, err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to dump rate limits to database: %w", err)
	}

	for _, budgetID := range budgetsToDelete {
		gs.budgets.Delete(budgetID)
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

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits)

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
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets, rateLimits)

	return nil
}

// rebuildInMemoryStructures rebuilds all in-memory data structures (lock-free)
func (gs *LocalGovernanceStore) rebuildInMemoryStructures(ctx context.Context, customers []configstoreTables.TableCustomer, teams []configstoreTables.TableTeam, virtualKeys []configstoreTables.TableVirtualKey, budgets []configstoreTables.TableBudget, rateLimits []configstoreTables.TableRateLimit) {
	// Clear existing data by creating new sync.Maps
	gs.virtualKeys = sync.Map{}
	gs.teams = sync.Map{}
	gs.customers = sync.Map{}
	gs.budgets = sync.Map{}
	gs.rateLimits = sync.Map{}

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
func (gs *LocalGovernanceStore) UpdateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey) {
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
		// Update Budget using checkAndUpdateBudget logic (preserve usage unless currentUsage+baseline > newMaxLimit)
		if clone.Budget != nil {
			var existingBudget *configstoreTables.TableBudget
			if existingVK.Budget != nil {
				existingBudget = existingVK.Budget
			}
			// Use baseline of 0.0 for virtual key updates (can be made configurable later)
			clone.Budget = checkAndUpdateBudget(clone.Budget, existingBudget, 0.0)
			// Update the budget in the main budgets sync.Map
			if clone.Budget != nil {
				gs.budgets.Store(clone.Budget.ID, clone.Budget)
			}
		} else if existingVK.Budget != nil {
			// Budget was removed from the virtual key, delete it from memory
			gs.budgets.Delete(existingVK.Budget.ID)
		}
		if clone.RateLimit != nil {
			var existingRateLimit *configstoreTables.TableRateLimit
			if existingVK.RateLimit != nil {
				existingRateLimit = existingVK.RateLimit
			}
			// Use baseline of 0 for virtual key updates (can be made configurable later)
			clone.RateLimit = checkAndUpdateRateLimit(clone.RateLimit, existingRateLimit, 0, 0)
			// Update the rate limit in the main rateLimits sync.Map
			if clone.RateLimit != nil {
				gs.rateLimits.Store(clone.RateLimit.ID, clone.RateLimit)
			}
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
					// Find matching existing provider config by ID
					var existingProviderRateLimit *configstoreTables.TableRateLimit
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.RateLimit != nil {
						existingProviderRateLimit = existingPC.RateLimit
					}
					// Use baseline of 0 for provider config updates (can be made configurable later)
					clone.ProviderConfigs[i].RateLimit = checkAndUpdateRateLimit(pc.RateLimit, existingProviderRateLimit, 0, 0)
					// Also update the rate limit in the main rateLimits sync.Map
					if clone.ProviderConfigs[i].RateLimit != nil {
						gs.rateLimits.Store(clone.ProviderConfigs[i].RateLimit.ID, clone.ProviderConfigs[i].RateLimit)
					}
				} else {
					// Rate limit was removed from provider config, delete it from memory if it existed
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.RateLimit != nil {
						gs.rateLimits.Delete(existingPC.RateLimit.ID)
						clone.ProviderConfigs[i].RateLimit = nil
					}
				}
				// Update Budget for provider config (preserve usage unless currentUsage+baseline > newMaxLimit)
				if pc.Budget != nil {
					var existingProviderBudget *configstoreTables.TableBudget
					if existingPC, exists := existingProviderConfigs[pc.ID]; exists && existingPC.Budget != nil {
						existingProviderBudget = existingPC.Budget
					}
					// Use baseline of 0.0 for provider config updates (can be made configurable later)
					clone.ProviderConfigs[i].Budget = checkAndUpdateBudget(pc.Budget, existingProviderBudget, 0.0)
					// Also update the budget in the main budgets sync.Map
					if clone.ProviderConfigs[i].Budget != nil {
						gs.budgets.Store(clone.ProviderConfigs[i].Budget.ID, clone.ProviderConfigs[i].Budget)
					}
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
			// Delete associated budget if exists and not shared
			if vk.BudgetID != nil {
				gs.budgets.Delete(*vk.BudgetID)
			}

			// Delete associated rate limit if exists and not shared
			if vk.RateLimitID != nil {
				gs.rateLimits.Delete(*vk.RateLimitID)
			}

			// Delete provider config budgets and rate limits if not shared
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
func (gs *LocalGovernanceStore) UpdateTeamInMemory(team *configstoreTables.TableTeam) {
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
			var existingBudget *configstoreTables.TableBudget
			if existingTeam.Budget != nil {
				existingBudget = existingTeam.Budget
			}
			// Use baseline of 0.0 for team updates (can be made configurable later)
			clone.Budget = checkAndUpdateBudget(clone.Budget, existingBudget, 0.0)
			// Update the budget in the main budgets sync.Map
			if clone.Budget != nil {
				gs.budgets.Store(clone.Budget.ID, clone.Budget)
			}
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
			// Delete associated budget if exists and not shared
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
			vk.TeamID = nil
			vk.Team = nil
			gs.virtualKeys.Store(key, vk)
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
func (gs *LocalGovernanceStore) UpdateCustomerInMemory(customer *configstoreTables.TableCustomer) {
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
			var existingBudget *configstoreTables.TableBudget
			if existingCustomer.Budget != nil {
				existingBudget = existingCustomer.Budget
			}
			// Use baseline of 0.0 for customer updates (can be made configurable later)
			clone.Budget = checkAndUpdateBudget(clone.Budget, existingBudget, 0.0)
			// Update the budget in the main budgets sync.Map
			if clone.Budget != nil {
				gs.budgets.Store(clone.Budget.ID, clone.Budget)
			}
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
			// Delete associated budget if exists and not shared
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
			vk.CustomerID = nil
			vk.Customer = nil
			gs.virtualKeys.Store(key, vk)
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
			team.CustomerID = nil
			team.Customer = nil
			gs.teams.Store(key, team)
		}
		return true // continue iteration
	})

	gs.customers.Delete(customerID)
}

// Helper functions

// checkAndUpdateBudget checks and updates a budget with usage reset logic
// If currentUsage+baseline > newMaxLimit, reset usage to 0
// Otherwise preserve existing usage and accept reset duration and max limit changes
func checkAndUpdateBudget(budgetToUpdate *configstoreTables.TableBudget, existingBudget *configstoreTables.TableBudget, baseline float64) *configstoreTables.TableBudget {
	// Create clone to avoid modifying the original
	clone := *budgetToUpdate
	if existingBudget == nil {
		// New budget, return as-is
		return budgetToUpdate
	}

	// Check if reset duration or max limit changed
	resetDurationChanged := budgetToUpdate.ResetDuration != existingBudget.ResetDuration
	maxLimitChanged := budgetToUpdate.MaxLimit != existingBudget.MaxLimit

	if resetDurationChanged || maxLimitChanged {
		// If currentUsage + baseline > new max limit, reset usage to 0
		if existingBudget.CurrentUsage+baseline > budgetToUpdate.MaxLimit {
			clone.CurrentUsage = 0
		} else {
			// Otherwise, preserve the existing usage
			clone.CurrentUsage = existingBudget.CurrentUsage
		}
	} else {
		// No changes to max limit or reset duration, preserve existing usage
		clone.CurrentUsage = existingBudget.CurrentUsage
	}

	return &clone
}

// checkAndUpdateRateLimit checks and updates a rate limit with usage reset logic
// If currentUsage+baseline > newMaxLimit, reset usage to 0
// Otherwise preserve existing usage and accept reset duration and max limit changes
func checkAndUpdateRateLimit(rateLimitToUpdate *configstoreTables.TableRateLimit, existingRateLimit *configstoreTables.TableRateLimit, tokenBaseline int64, requestBaseline int64) *configstoreTables.TableRateLimit {
	// Create clone to avoid modifying the original
	clone := *rateLimitToUpdate
	if existingRateLimit == nil {
		// New rate limit, return as-is
		return rateLimitToUpdate
	}

	// Check if token settings changed
	tokenMaxLimitChanged := existingRateLimit.TokenMaxLimit != rateLimitToUpdate.TokenMaxLimit
	tokenResetDurationChanged := existingRateLimit.TokenResetDuration != rateLimitToUpdate.TokenResetDuration

	// Check if request settings changed
	requestMaxLimitChanged := existingRateLimit.RequestMaxLimit != rateLimitToUpdate.RequestMaxLimit
	requestResetDurationChanged := existingRateLimit.RequestResetDuration != rateLimitToUpdate.RequestResetDuration

	if tokenMaxLimitChanged || tokenResetDurationChanged {
		// If currentUsage + baseline > new max limit, reset usage to 0
		if rateLimitToUpdate.TokenMaxLimit != nil && existingRateLimit.TokenCurrentUsage+tokenBaseline > *rateLimitToUpdate.TokenMaxLimit {
			clone.TokenCurrentUsage = 0
		} else {
			// Otherwise, preserve the existing usage
			clone.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
		}
	} else {
		// No changes to max limit or reset duration, preserve existing usage
		clone.TokenCurrentUsage = existingRateLimit.TokenCurrentUsage
	}

	if requestMaxLimitChanged || requestResetDurationChanged {
		// If currentUsage + baseline > new max limit, reset usage to 0
		if rateLimitToUpdate.RequestMaxLimit != nil && existingRateLimit.RequestCurrentUsage+requestBaseline > *rateLimitToUpdate.RequestMaxLimit {
			clone.RequestCurrentUsage = 0
		} else {
			// Otherwise, preserve the existing usage
			clone.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
		}
	} else {
		// No changes to max limit or reset duration, preserve existing usage
		clone.RequestCurrentUsage = existingRateLimit.RequestCurrentUsage
	}

	return &clone
}
