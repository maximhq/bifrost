// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// GovernanceStore provides in-memory cache for governance data with fast, non-blocking access
type GovernanceStore struct {
	// Core data maps using sync.Map for lock-free reads
	virtualKeys sync.Map // string -> *VirtualKey (VK value -> VirtualKey with preloaded relationships)
	teams       sync.Map // string -> *Team (Team ID -> Team)
	customers   sync.Map // string -> *Customer (Customer ID -> Customer)
	budgets     sync.Map // string -> *Budget (Budget ID -> Budget)

	// Database connection for refresh operations
	db *gorm.DB

	// Logger
	logger schemas.Logger
}

// NewGovernanceStore creates a new in-memory governance store
func NewGovernanceStore(db *gorm.DB, logger schemas.Logger) (*GovernanceStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection cannot be nil")
	}

	store := &GovernanceStore{
		db:     db,
		logger: logger,
	}

	// Load initial data from database
	if err := store.loadFromDatabase(); err != nil {
		return nil, fmt.Errorf("failed to load initial data: %w", err)
	}

	store.logger.Info("Governance store initialized successfully")
	return store, nil
}

// GetVirtualKey retrieves a virtual key by its value (lock-free) with all relationships preloaded
func (gs *GovernanceStore) GetVirtualKey(vkValue string) (*VirtualKey, bool) {
	value, exists := gs.virtualKeys.Load(vkValue)
	if !exists || value == nil {
		return nil, false
	}

	vk, ok := value.(*VirtualKey)
	if !ok || vk == nil {
		return nil, false
	}
	return vk, true
}

// GetAllBudgets returns all budgets (for background reset operations)
func (gs *GovernanceStore) GetAllBudgets() map[string]*Budget {
	result := make(map[string]*Budget)
	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		keyStr, keyOk := key.(string)
		budget, budgetOk := value.(*Budget)

		if keyOk && budgetOk && budget != nil {
			result[keyStr] = budget
		}
		return true // continue iteration
	})
	return result
}

// CheckBudget performs budget checking using in-memory store data (lock-free for high performance)
func (gs *GovernanceStore) CheckBudget(vk *VirtualKey) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}

	// Use helper to collect budgets and their names (lock-free)
	budgetsToCheck, budgetNames := gs.collectBudgetsFromHierarchy(vk)

	// Check each budget in hierarchy order using in-memory data
	for i, budget := range budgetsToCheck {
		// Check if budget needs reset (in-memory check)
		if budget.ResetDuration != "" {
			if duration, err := ParseDuration(budget.ResetDuration); err == nil {
				if time.Since(budget.LastReset).Round(time.Millisecond) >= duration {
					// Budget expired but hasn't been reset yet - treat as reset
					// Note: actual reset will happen in post-hook via AtomicBudgetUpdate
					continue // Skip budget check for expired budgets
				}
			}
		}

		// Check if current usage exceeds budget limit
		if budget.CurrentUsage > budget.MaxLimit {
			return fmt.Errorf("%s budget exceeded: %.2f > %.2f dollars",
				budgetNames[i], budget.CurrentUsage, budget.MaxLimit)
		}
	}

	return nil
}

// UpdateBudget performs atomic budget updates across the hierarchy (both in memory and in database)
func (gs *GovernanceStore) UpdateBudget(vk *VirtualKey, cost float64) error {
	if vk == nil {
		return fmt.Errorf("virtual key cannot be nil")
	}

	// Collect budget IDs using fast in-memory lookup instead of DB queries
	budgetIDs := gs.collectBudgetIDsFromMemory(vk)

	return gs.db.Transaction(func(tx *gorm.DB) error {
		// budgetIDs already collected from in-memory data - no need to duplicate

		// Update each budget atomically
		for _, budgetID := range budgetIDs {
			var budget Budget
			if err := tx.Set("gorm:query_option", "FOR UPDATE").First(&budget, "id = ?", budgetID).Error; err != nil {
				return fmt.Errorf("failed to lock budget %s: %w", budgetID, err)
			}

			// Check if budget needs reset
			if err := gs.resetBudgetIfNeeded(tx, &budget); err != nil {
				return fmt.Errorf("failed to reset budget: %w", err)
			}

			// Update usage
			budget.CurrentUsage += cost
			if err := tx.Save(&budget).Error; err != nil {
				return fmt.Errorf("failed to save budget %s: %w", budgetID, err)
			}

			// Update in-memory cache for next read (lock-free)
			if cachedBudgetValue, exists := gs.budgets.Load(budgetID); exists && cachedBudgetValue != nil {
				if cachedBudget, ok := cachedBudgetValue.(*Budget); ok && cachedBudget != nil {
					cachedBudget.CurrentUsage = budget.CurrentUsage
					cachedBudget.LastReset = budget.LastReset
				}
			}
		}

		return nil
	})
}

// UpdateRateLimitUsage updates rate limit counters (lock-free)
func (gs *GovernanceStore) UpdateRateLimitUsage(vkValue string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	if vkValue == "" {
		return fmt.Errorf("virtual key value cannot be empty")
	}

	vkValue_, exists := gs.virtualKeys.Load(vkValue)
	if !exists || vkValue_ == nil {
		return fmt.Errorf("virtual key not found: %s", vkValue)
	}

	vk, ok := vkValue_.(*VirtualKey)
	if !ok || vk == nil {
		return fmt.Errorf("invalid virtual key type for: %s", vkValue)
	}
	if vk.RateLimit == nil {
		return nil // No rate limit configured, nothing to update
	}

	rateLimit := vk.RateLimit
	now := time.Now()
	updated := false

	// Check and reset token counter if needed
	if rateLimit.TokenResetDuration != nil {
		if duration, err := ParseDuration(*rateLimit.TokenResetDuration); err == nil {
			if now.Sub(rateLimit.TokenLastReset) >= duration {
				rateLimit.TokenCurrentUsage = 0
				rateLimit.TokenLastReset = now
				updated = true
			}
		}
	}

	// Check and reset request counter if needed
	if rateLimit.RequestResetDuration != nil {
		if duration, err := ParseDuration(*rateLimit.RequestResetDuration); err == nil {
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

	// Save to database only if something changed
	if updated {
		if err := gs.db.Save(rateLimit).Error; err != nil {
			return fmt.Errorf("failed to update rate limit usage: %w", err)
		}
	}

	return nil
}

// checkAndResetSingleRateLimit checks and resets a single rate limit's counters if expired
func (gs *GovernanceStore) checkAndResetSingleRateLimit(rateLimit *RateLimit, now time.Time) bool {
	updated := false

	// Check and reset token counter if needed
	if rateLimit.TokenResetDuration != nil {
		if duration, err := ParseDuration(*rateLimit.TokenResetDuration); err == nil {
			if now.Sub(rateLimit.TokenLastReset).Round(time.Millisecond) >= duration {
				rateLimit.TokenCurrentUsage = 0
				rateLimit.TokenLastReset = now
				updated = true
			}
		}
	}

	// Check and reset request counter if needed
	if rateLimit.RequestResetDuration != nil {
		if duration, err := ParseDuration(*rateLimit.RequestResetDuration); err == nil {
			if now.Sub(rateLimit.RequestLastReset).Round(time.Millisecond) >= duration {
				rateLimit.RequestCurrentUsage = 0
				rateLimit.RequestLastReset = now
				updated = true
			}
		}
	}

	return updated
}

// ResetExpiredRateLimits performs background reset of expired rate limits (lock-free)
func (gs *GovernanceStore) ResetExpiredRateLimits() error {
	now := time.Now()
	var resetRateLimits []*RateLimit

	gs.virtualKeys.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		vk, ok := value.(*VirtualKey)
		if !ok || vk == nil || vk.RateLimit == nil {
			return true // continue
		}

		rateLimit := vk.RateLimit

		// Use helper method to check and reset rate limit
		if gs.checkAndResetSingleRateLimit(rateLimit, now) {
			resetRateLimits = append(resetRateLimits, rateLimit)
		}
		return true // continue
	})

	// Persist reset rate limits to database
	if len(resetRateLimits) > 0 {
		if err := gs.db.Save(&resetRateLimits).Error; err != nil {
			return fmt.Errorf("failed to persist rate limit resets to database: %w", err)
		}
	}

	return nil
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration (lock-free)
func (gs *GovernanceStore) ResetExpiredBudgets() error {
	now := time.Now()
	var resetBudgets []*Budget

	gs.budgets.Range(func(key, value interface{}) bool {
		// Type-safe conversion
		budget, ok := value.(*Budget)
		if !ok || budget == nil {
			return true // continue
		}

		duration, err := ParseDuration(budget.ResetDuration)
		if err != nil {
			gs.logger.Error(fmt.Errorf("invalid budget reset duration %s: %w", budget.ResetDuration, err))
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
	if len(resetBudgets) > 0 {
		if err := gs.db.Save(&resetBudgets).Error; err != nil {
			return fmt.Errorf("failed to persist budget resets to database: %w", err)
		}
	}

	return nil
}

// DATABASE METHODS

// loadFromDatabase loads all governance data from the database into memory
func (gs *GovernanceStore) loadFromDatabase() error {
	// Load customers with their budgets
	var customers []Customer
	if err := gs.db.Find(&customers).Error; err != nil {
		return fmt.Errorf("failed to load customers: %w", err)
	}

	// Load teams with their budgets
	var teams []Team
	if err := gs.db.Find(&teams).Error; err != nil {
		return fmt.Errorf("failed to load teams: %w", err)
	}

	// Load virtual keys with all relationships
	var virtualKeys []VirtualKey
	if err := gs.db.Preload("RateLimit").Where("is_active = ?", true).Find(&virtualKeys).Error; err != nil {
		return fmt.Errorf("failed to load virtual keys: %w", err)
	}

	// Load budgets
	var budgets []Budget
	if err := gs.db.Find(&budgets).Error; err != nil {
		return fmt.Errorf("failed to load budgets: %w", err)
	}

	// Rebuild in-memory structures (lock-free)
	gs.rebuildInMemoryStructures(customers, teams, virtualKeys, budgets)

	return nil
}

// rebuildInMemoryStructures rebuilds all in-memory data structures (lock-free)
func (gs *GovernanceStore) rebuildInMemoryStructures(customers []Customer, teams []Team, virtualKeys []VirtualKey, budgets []Budget) {
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

// collectBudgetsFromHierarchy collects budgets and their metadata from the hierarchy (VK → Team → Customer)
func (gs *GovernanceStore) collectBudgetsFromHierarchy(vk *VirtualKey) ([]*Budget, []string) {
	if vk == nil {
		return nil, nil
	}

	var budgets []*Budget
	var budgetNames []string

	// Collect all budgets in hierarchy order using lock-free sync.Map access (VK → Team → Customer)
	if vk.BudgetID != nil {
		if budgetValue, exists := gs.budgets.Load(*vk.BudgetID); exists && budgetValue != nil {
			if budget, ok := budgetValue.(*Budget); ok && budget != nil {
				budgets = append(budgets, budget)
				budgetNames = append(budgetNames, "VK")
			}
		}
	}

	if vk.TeamID != nil {
		if teamValue, exists := gs.teams.Load(*vk.TeamID); exists && teamValue != nil {
			if team, ok := teamValue.(*Team); ok && team != nil {
				if team.BudgetID != nil {
					if budgetValue, exists := gs.budgets.Load(*team.BudgetID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*Budget); ok && budget != nil {
							budgets = append(budgets, budget)
							budgetNames = append(budgetNames, "Team")
						}
					}
				}

				// Check if team belongs to a customer
				if team.CustomerID != nil {
					if customerValue, exists := gs.customers.Load(*team.CustomerID); exists && customerValue != nil {
						if customer, ok := customerValue.(*Customer); ok && customer != nil {
							if customer.BudgetID != nil {
								if budgetValue, exists := gs.budgets.Load(*customer.BudgetID); exists && budgetValue != nil {
									if budget, ok := budgetValue.(*Budget); ok && budget != nil {
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
			if customer, ok := customerValue.(*Customer); ok && customer != nil {
				if customer.BudgetID != nil {
					if budgetValue, exists := gs.budgets.Load(*customer.BudgetID); exists && budgetValue != nil {
						if budget, ok := budgetValue.(*Budget); ok && budget != nil {
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
func (gs *GovernanceStore) collectBudgetIDsFromMemory(vk *VirtualKey) []string {
	budgets, _ := gs.collectBudgetsFromHierarchy(vk)

	budgetIDs := make([]string, len(budgets))
	for i, budget := range budgets {
		budgetIDs[i] = budget.ID
	}

	return budgetIDs
}

// resetBudgetIfNeeded checks and resets budget within a transaction
func (gs *GovernanceStore) resetBudgetIfNeeded(tx *gorm.DB, budget *Budget) error {
	duration, err := ParseDuration(budget.ResetDuration)
	if err != nil {
		return fmt.Errorf("invalid reset duration %s: %w", budget.ResetDuration, err)
	}

	now := time.Now()
	if now.Sub(budget.LastReset) >= duration {
		budget.CurrentUsage = 0
		budget.LastReset = now

		// Save reset to database
		if err := tx.Save(budget).Error; err != nil {
			return fmt.Errorf("failed to save budget reset: %w", err)
		}
	}

	return nil
}

// PUBLIC API METHODS

// CreateVirtualKeyInMemory adds a new virtual key to the in-memory store (lock-free)
func (gs *GovernanceStore) CreateVirtualKeyInMemory(vk *VirtualKey) { // with rateLimit preloaded
	if vk == nil {
		return // Nothing to create
	}
	gs.virtualKeys.Store(vk.Value, vk)
}

// UpdateVirtualKeyInMemory updates an existing virtual key in the in-memory store (lock-free)
func (gs *GovernanceStore) UpdateVirtualKeyInMemory(vk *VirtualKey) { // with rateLimit preloaded
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
		vk, ok := value.(*VirtualKey)
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
func (gs *GovernanceStore) CreateTeamInMemory(team *Team) {
	if team == nil {
		return // Nothing to create
	}
	gs.teams.Store(team.ID, team)
}

// UpdateTeamInMemory updates an existing team in the in-memory store (lock-free)
func (gs *GovernanceStore) UpdateTeamInMemory(team *Team) {
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
func (gs *GovernanceStore) CreateCustomerInMemory(customer *Customer) {
	if customer == nil {
		return // Nothing to create
	}
	gs.customers.Store(customer.ID, customer)
}

// UpdateCustomerInMemory updates an existing customer in the in-memory store (lock-free)
func (gs *GovernanceStore) UpdateCustomerInMemory(customer *Customer) {
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
func (gs *GovernanceStore) CreateBudgetInMemory(budget *Budget) {
	if budget == nil {
		return // Nothing to create
	}
	gs.budgets.Store(budget.ID, budget)
}

// UpdateBudgetInMemory updates a specific budget in the in-memory cache (lock-free)
func (gs *GovernanceStore) UpdateBudgetInMemory(budget *Budget) error {
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
