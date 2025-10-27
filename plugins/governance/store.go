// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GovernanceStore provides in-memory cache for governance data with fast, non-blocking access
type GovernanceStore struct {
	// Core data maps using sync.Map for lock-free reads
	virtualKeys sync.Map // string -> *VirtualKey (VK value -> VirtualKey with preloaded relationships)
	teams       sync.Map // string -> *Team (Team ID -> Team)
	customers   sync.Map // string -> *Customer (Customer ID -> Customer)
	budgets     sync.Map // string -> *Budget (Budget ID -> Budget)

	// Config store for refresh operations
	configStore configstore.ConfigStore

	// Logger
	logger schemas.Logger
}

// NewGovernanceStore creates a new in-memory governance store
func NewGovernanceStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore, governanceConfig *configstore.GovernanceConfig) (*GovernanceStore, error) {
	store := &GovernanceStore{
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

// GetVirtualKey retrieves a virtual key by its value (lock-free) with all relationships preloaded
func (gs *GovernanceStore) GetVirtualKey(vkValue string) (*configstoreTables.TableVirtualKey, bool) {
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
func (gs *GovernanceStore) GetAllBudgets() map[string]*configstoreTables.TableBudget {
	result := make(map[string]*configstoreTables.TableBudget)
	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		keyStr, keyOk := key.(string)
		budget, budgetOk := value.(*configstoreTables.TableBudget)

		if keyOk && budgetOk && budget != nil {
			result[keyStr] = budget
		}
		return true // continue iteration
	})
	return result
}

// CheckBudget performs budget checking using in-memory store data (lock-free for high performance)
func (gs *GovernanceStore) CheckBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}

	// Use helper to collect budgets and their names (lock-free)
	budgetsToCheck, budgetNames := gs.collectBudgetsFromHierarchy(ctx, vk)

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

		// Check if current usage exceeds budget limit
		if budget.CurrentUsage > budget.MaxLimit {
			return fmt.Errorf("%s budget exceeded: %.4f > %.4f dollars",
				budgetNames[i], budget.CurrentUsage, budget.MaxLimit)
		}
	}

	return nil
}

// UpdateBudget performs atomic budget updates across the hierarchy (both in memory and in database)
func (gs *GovernanceStore) UpdateBudget(ctx context.Context, vk *configstoreTables.TableVirtualKey, cost float64) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}

	// Collect budget IDs using fast in-memory lookup instead of DB queries
	budgetIDs := gs.collectBudgetIDsFromMemory(ctx, vk)

	if gs.configStore == nil {
		for _, budgetID := range budgetIDs {
			// Update in-memory cache for next read (lock-free)
			if cachedBudgetValue, exists := gs.budgets.Load(budgetID); exists && cachedBudgetValue != nil {
				if cachedBudget, ok := cachedBudgetValue.(*configstoreTables.TableBudget); ok && cachedBudget != nil {
					clone := *cachedBudget
					clone.CurrentUsage += cost
					gs.budgets.Store(budgetID, &clone)
				}
			}
		}

		return nil
	}

	return gs.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// budgetIDs already collected from in-memory data - no need to duplicate

		// Update each budget atomically
		for _, budgetID := range budgetIDs {
			var budget configstoreTables.TableBudget
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&budget, "id = ?", budgetID).Error; err != nil {
				return fmt.Errorf("failed to lock budget %s: %w", budgetID, err)
			}

			// Check if budget needs reset
			if err := gs.resetBudgetIfNeeded(ctx, tx, &budget); err != nil {
				return fmt.Errorf("failed to reset budget: %w", err)
			}

			// Update usage
			budget.CurrentUsage += cost
			if err := gs.configStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to save budget %s: %w", budgetID, err)
			}

			// Update in-memory cache for next read (lock-free)
			if cachedBudgetValue, exists := gs.budgets.Load(budgetID); exists && cachedBudgetValue != nil {
				if cachedBudget, ok := cachedBudgetValue.(*configstoreTables.TableBudget); ok && cachedBudget != nil {
					clone := *cachedBudget
					clone.CurrentUsage += cost
					clone.LastReset = budget.LastReset
					gs.budgets.Store(budgetID, &clone)
				}
			}
		}

		return nil
	})
}

// UpdateRateLimitUsage updates rate limit counters for both provider-level and VK-level rate limits (lock-free)
func (gs *GovernanceStore) UpdateRateLimitUsage(ctx context.Context, vkValue string, provider string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	if vkValue == "" {
		return fmt.Errorf("virtual key value cannot be empty")
	}

	vkValue_, exists := gs.virtualKeys.Load(vkValue)
	if !exists || vkValue_ == nil {
		return fmt.Errorf("virtual key not found: %s", vkValue)
	}

	vk, ok := vkValue_.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		return fmt.Errorf("invalid virtual key type for: %s", vkValue)
	}

	var rateLimitsToUpdate []*configstoreTables.TableRateLimit

	// First, update provider-level rate limits if they exist
	if provider != "" && vk.ProviderConfigs != nil {
		for _, pc := range vk.ProviderConfigs {
			if pc.Provider == provider && pc.RateLimit != nil {
				if gs.updateSingleRateLimit(pc.RateLimit, tokensUsed, shouldUpdateTokens, shouldUpdateRequests) {
					rateLimitsToUpdate = append(rateLimitsToUpdate, pc.RateLimit)
				}
				break
			}
		}
	}

	// Then, update VK-level rate limits if they exist
	if vk.RateLimit != nil {
		if gs.updateSingleRateLimit(vk.RateLimit, tokensUsed, shouldUpdateTokens, shouldUpdateRequests) {
			rateLimitsToUpdate = append(rateLimitsToUpdate, vk.RateLimit)
		}
	}

	// Save all updated rate limits to database
	if len(rateLimitsToUpdate) > 0 && gs.configStore != nil {
		if err := gs.configStore.UpdateRateLimits(ctx, rateLimitsToUpdate); err != nil {
			return fmt.Errorf("failed to update rate limit usage: %w", err)
		}
	}

	return nil
}

// updateSingleRateLimit updates a single rate limit's counters and returns true if any changes were made
func (gs *GovernanceStore) updateSingleRateLimit(rateLimit *configstoreTables.TableRateLimit, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) bool {
	now := time.Now()
	updated := false

	// Check and reset token counter if needed
	if rateLimit.TokenResetDuration != nil {
		if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
			if now.Sub(rateLimit.TokenLastReset) >= duration {
				rateLimit.TokenCurrentUsage = 0
				rateLimit.TokenLastReset = now
				updated = true
			}
		}
	}

	// Check and reset request counter if needed
	if rateLimit.RequestResetDuration != nil {
		if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
			if now.Sub(rateLimit.RequestLastReset) >= duration {
				rateLimit.RequestCurrentUsage = 0
				rateLimit.RequestLastReset = now
				updated = true
			}
		}
	}

	// Update usage counters based on flags
	if shouldUpdateTokens && tokensUsed > 0 {
		rateLimit.TokenCurrentUsage += tokensUsed
		updated = true
	}

	if shouldUpdateRequests {
		rateLimit.RequestCurrentUsage += 1
		updated = true
	}

	return updated
}

// checkAndResetSingleRateLimit checks and resets a single rate limit's counters if expired
func (gs *GovernanceStore) checkAndResetSingleRateLimit(ctx context.Context, rateLimit *configstoreTables.TableRateLimit, now time.Time) bool {
	updated := false

	// Check and reset token counter if needed
	if rateLimit.TokenResetDuration != nil {
		if duration, err := configstoreTables.ParseDuration(*rateLimit.TokenResetDuration); err == nil {
			if now.Sub(rateLimit.TokenLastReset).Round(time.Millisecond) >= duration {
				rateLimit.TokenCurrentUsage = 0
				rateLimit.TokenLastReset = now
				updated = true
			}
		}
	}

	// Check and reset request counter if needed
	if rateLimit.RequestResetDuration != nil {
		if duration, err := configstoreTables.ParseDuration(*rateLimit.RequestResetDuration); err == nil {
			if now.Sub(rateLimit.RequestLastReset).Round(time.Millisecond) >= duration {
				rateLimit.RequestCurrentUsage = 0
				rateLimit.RequestLastReset = now
				updated = true
			}
		}
	}

	return updated
}

// ResetExpiredRateLimits performs background reset of expired rate limits for both provider-level and VK-level (lock-free)
func (gs *GovernanceStore) ResetExpiredRateLimits(ctx context.Context) error {
	now := time.Now()
	var resetRateLimits []*configstoreTables.TableRateLimit

	gs.virtualKeys.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		vk, ok := value.(*configstoreTables.TableVirtualKey)
		if !ok || vk == nil {
			return true // continue
		}

		// Check provider-level rate limits
		if vk.ProviderConfigs != nil {
			for _, pc := range vk.ProviderConfigs {
				if pc.RateLimit != nil {
					if gs.checkAndResetSingleRateLimit(ctx, pc.RateLimit, now) {
						resetRateLimits = append(resetRateLimits, pc.RateLimit)
					}
				}
			}
		}

		// Check VK-level rate limits
		if vk.RateLimit != nil {
			if gs.checkAndResetSingleRateLimit(ctx, vk.RateLimit, now) {
				resetRateLimits = append(resetRateLimits, vk.RateLimit)
			}
		}

		return true // continue
	})

	// Persist reset rate limits to database
	if len(resetRateLimits) > 0 && gs.configStore != nil {
		if err := gs.configStore.UpdateRateLimits(ctx, resetRateLimits); err != nil {
			return fmt.Errorf("failed to persist rate limit resets to database: %w", err)
		}
	}

	return nil
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration (lock-free)
func (gs *GovernanceStore) ResetExpiredBudgets(ctx context.Context) error {
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

// DATABASE METHODS

// loadFromDatabase loads all governance data from the database into memory
func (gs *GovernanceStore) loadFromDatabase(ctx context.Context) error {
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

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets)

	return nil
}

// loadFromConfigMemory loads all governance data from the config's memory into store's memory
func (gs *GovernanceStore) loadFromConfigMemory(ctx context.Context, config *configstore.GovernanceConfig) error {
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

		virtualKeys[i] = *vk
	}

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(ctx, customers, teams, virtualKeys, budgets)

	return nil
}

// rebuildInMemoryStructures rebuilds all in-memory data structures (lock-free)
func (gs *GovernanceStore) rebuildInMemoryStructures(ctx context.Context, customers []configstoreTables.TableCustomer, teams []configstoreTables.TableTeam, virtualKeys []configstoreTables.TableVirtualKey, budgets []configstoreTables.TableBudget) {
	// Clear existing data by creating new sync.Maps
	gs.virtualKeys = sync.Map{}
	gs.teams = sync.Map{}
	gs.customers = sync.Map{}
	gs.budgets = sync.Map{}

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

	// Build virtual keys map and track active VKs
	for i := range virtualKeys {
		vk := &virtualKeys[i]
		gs.virtualKeys.Store(vk.Value, vk)
	}
}

// UTILITY FUNCTIONS

// collectBudgetsFromHierarchy collects budgets and their metadata from the hierarchy (Provider Configs → VK → Team → Customer)
func (gs *GovernanceStore) collectBudgetsFromHierarchy(ctx context.Context, vk *configstoreTables.TableVirtualKey) ([]*configstoreTables.TableBudget, []string) {
	if vk == nil {
		return nil, nil
	}

	var budgets []*configstoreTables.TableBudget
	var budgetNames []string

	// Collect all budgets in hierarchy order using lock-free sync.Map access (Provider Configs → VK → Team → Customer)
	for _, pc := range vk.ProviderConfigs {
		if pc.BudgetID != nil {
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
func (gs *GovernanceStore) collectBudgetIDsFromMemory(ctx context.Context, vk *configstoreTables.TableVirtualKey) []string {
	budgets, _ := gs.collectBudgetsFromHierarchy(ctx, vk)

	budgetIDs := make([]string, len(budgets))
	for i, budget := range budgets {
		budgetIDs[i] = budget.ID
	}

	return budgetIDs
}

// resetBudgetIfNeeded checks and resets budget within a transaction
func (gs *GovernanceStore) resetBudgetIfNeeded(ctx context.Context, tx *gorm.DB, budget *configstoreTables.TableBudget) error {
	duration, err := configstoreTables.ParseDuration(budget.ResetDuration)
	if err != nil {
		return fmt.Errorf("invalid reset duration %s: %w", budget.ResetDuration, err)
	}

	now := time.Now()
	if now.Sub(budget.LastReset) >= duration {
		budget.CurrentUsage = 0
		budget.LastReset = now

		if gs.configStore != nil {
			// Save reset to database
			if err := gs.configStore.UpdateBudget(ctx, budget, tx); err != nil {
				return fmt.Errorf("failed to save budget reset: %w", err)
			}
		}
	}

	return nil
}

// PUBLIC API METHODS

// CreateVirtualKeyInMemory adds a new virtual key to the in-memory store (lock-free)
func (gs *GovernanceStore) CreateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey) { // with rateLimit preloaded
	if vk == nil {
		return // Nothing to create
	}
	gs.virtualKeys.Store(vk.Value, vk)
}

// UpdateVirtualKeyInMemory updates an existing virtual key in the in-memory store (lock-free)
func (gs *GovernanceStore) UpdateVirtualKeyInMemory(vk *configstoreTables.TableVirtualKey) { // with rateLimit preloaded
	if vk == nil {
		return // Nothing to update
	}
	gs.virtualKeys.Store(vk.Value, vk)
}

// DeleteVirtualKeyInMemory removes a virtual key from the in-memory store
func (gs *GovernanceStore) DeleteVirtualKeyInMemory(vkID string) {
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
			gs.virtualKeys.Delete(key)
			return false // stop iteration
		}
		return true // continue iteration
	})
}

// CreateTeamInMemory adds a new team to the in-memory store (lock-free)
func (gs *GovernanceStore) CreateTeamInMemory(team *configstoreTables.TableTeam) {
	if team == nil {
		return // Nothing to create
	}
	gs.teams.Store(team.ID, team)
}

// UpdateTeamInMemory updates an existing team in the in-memory store (lock-free)
func (gs *GovernanceStore) UpdateTeamInMemory(team *configstoreTables.TableTeam) {
	if team == nil {
		return // Nothing to update
	}
	gs.teams.Store(team.ID, team)
}

// DeleteTeamInMemory removes a team from the in-memory store (lock-free)
func (gs *GovernanceStore) DeleteTeamInMemory(teamID string) {
	if teamID == "" {
		return // Nothing to delete
	}
	gs.teams.Delete(teamID)
}

// CreateCustomerInMemory adds a new customer to the in-memory store (lock-free)
func (gs *GovernanceStore) CreateCustomerInMemory(customer *configstoreTables.TableCustomer) {
	if customer == nil {
		return // Nothing to create
	}
	gs.customers.Store(customer.ID, customer)
}

// UpdateCustomerInMemory updates an existing customer in the in-memory store (lock-free)
func (gs *GovernanceStore) UpdateCustomerInMemory(customer *configstoreTables.TableCustomer) {
	if customer == nil {
		return // Nothing to update
	}
	gs.customers.Store(customer.ID, customer)
}

// DeleteCustomerInMemory removes a customer from the in-memory store (lock-free)
func (gs *GovernanceStore) DeleteCustomerInMemory(customerID string) {
	if customerID == "" {
		return // Nothing to delete
	}
	gs.customers.Delete(customerID)
}

// CreateBudgetInMemory adds a new budget to the in-memory store (lock-free)
func (gs *GovernanceStore) CreateBudgetInMemory(budget *configstoreTables.TableBudget) {
	if budget == nil {
		return // Nothing to create
	}
	gs.budgets.Store(budget.ID, budget)
}

// UpdateBudgetInMemory updates a specific budget in the in-memory cache (lock-free)
func (gs *GovernanceStore) UpdateBudgetInMemory(budget *configstoreTables.TableBudget) error {
	if budget == nil {
		return fmt.Errorf("budget cannot be nil")
	}
	gs.budgets.Store(budget.ID, budget)
	return nil
}

// DeleteBudgetInMemory removes a budget from the in-memory store (lock-free)
func (gs *GovernanceStore) DeleteBudgetInMemory(budgetID string) {
	if budgetID == "" {
		return // Nothing to delete
	}
	gs.budgets.Delete(budgetID)
}
