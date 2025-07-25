// Package governance provides the in-memory cache store for fast governance data access
package governance

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// GovernanceStore provides in-memory cache for governance data with fast, non-blocking access
type GovernanceStore struct {
	// Core data maps with RWMutex for concurrent access
	virtualKeys map[string]*VirtualKey // VK value -> VirtualKey (with preloaded relationships)
	teams       map[string]*Team       // Team ID -> Team
	customers   map[string]*Customer   // Customer ID -> Customer
	budgets     map[string]*Budget     // Budget ID -> Budget

	// Concurrency control
	mu sync.RWMutex

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
		virtualKeys: make(map[string]*VirtualKey),
		teams:       make(map[string]*Team),
		customers:   make(map[string]*Customer),
		budgets:     make(map[string]*Budget),
		db:          db,
		logger:      logger,
	}

	// Load initial data from database
	if err := store.loadFromDatabase(); err != nil {
		return nil, fmt.Errorf("failed to load initial data: %w", err)
	}

	store.logger.Info("Governance store initialized successfully")
	return store, nil
}

// GetVirtualKey retrieves a virtual key by its value (thread-safe) with all relationships preloaded
func (gs *GovernanceStore) GetVirtualKey(vkValue string) (*VirtualKey, bool) {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	vk, exists := gs.virtualKeys[vkValue]
	return vk, exists
}

// GetAllBudgets returns all budgets (for background reset operations)
func (gs *GovernanceStore) GetAllBudgets() map[string]*Budget {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	result := make(map[string]*Budget, len(gs.budgets))
	maps.Copy(result, gs.budgets)
	return result
}

// CheckBudget performs budget checking using in-memory store data (optimized for request path)
func (gs *GovernanceStore) CheckBudget(vk *VirtualKey) error {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	// Use helper to collect budgets and their names
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
			return fmt.Errorf("%s budget exceeded: %d > %d cents",
				budgetNames[i], budget.CurrentUsage, budget.MaxLimit)
		}
	}

	return nil
}

// UpdateBudget performs atomic budget updates across the hierarchy (both in memory and in database)
func (gs *GovernanceStore) UpdateBudget(vk *VirtualKey, costCents int64) error {
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
			budget.CurrentUsage += costCents
			if err := tx.Save(&budget).Error; err != nil {
				return fmt.Errorf("failed to save budget %s: %w", budgetID, err)
			}

			// Update in-memory cache for next read
			gs.mu.Lock()
			if cachedBudget, exists := gs.budgets[budgetID]; exists {
				cachedBudget.CurrentUsage = budget.CurrentUsage
				cachedBudget.LastReset = budget.LastReset
			}
			gs.mu.Unlock()
		}

		return nil
	})
}

// UpdateRateLimitUsage updates rate limit counters with proper mutex protection (both in memory and in database)
func (gs *GovernanceStore) UpdateRateLimitUsage(vkValue string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	vk, exists := gs.virtualKeys[vkValue]
	if !exists {
		return fmt.Errorf("virtual key not found: %s", vkValue)
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

// ResetExpiredRateLimits performs background reset of expired rate limits
func (gs *GovernanceStore) ResetExpiredRateLimits() error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	now := time.Now()
	var resetRateLimits []*RateLimit

	for _, vk := range gs.virtualKeys {
		if vk.RateLimit == nil {
			continue
		}

		rateLimit := vk.RateLimit

		// Use helper method to check and reset rate limit
		if gs.checkAndResetSingleRateLimit(rateLimit, now) {
			resetRateLimits = append(resetRateLimits, rateLimit)
		}
	}

	// Persist reset rate limits to database
	if len(resetRateLimits) > 0 {
		if err := gs.db.Save(&resetRateLimits).Error; err != nil {
			return fmt.Errorf("failed to persist rate limit resets to database: %w", err)
		}
	}

	return nil
}

// ResetExpiredBudgets checks and resets budgets that have exceeded their reset duration
func (gs *GovernanceStore) ResetExpiredBudgets() error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	now := time.Now()
	var resetBudgets []*Budget

	for _, budget := range gs.budgets {
		duration, err := ParseDuration(budget.ResetDuration)
		if err != nil {
			gs.logger.Error(fmt.Errorf("invalid budget reset duration %s: %w", budget.ResetDuration, err))
			continue
		}

		if now.Sub(budget.LastReset) >= duration {
			oldUsage := budget.CurrentUsage
			budget.CurrentUsage = 0
			budget.LastReset = now
			resetBudgets = append(resetBudgets, budget)

			gs.logger.Debug(fmt.Sprintf("Reset budget %s (was %d, reset to 0)",
				budget.ID, oldUsage))
		}
	}

	// Persist to database if any resets occurred
	if len(resetBudgets) > 0 {
		if err := gs.db.Save(&resetBudgets).Error; err != nil {
			return fmt.Errorf("failed to persist budget resets to database: %w", err)
		}
	}

	return nil
}

// PUBLIC API METHODS

// UpdateBudgetInMemory updates a specific budget in the in-memory cache (called after DB operations)
func (gs *GovernanceStore) UpdateBudgetInMemory(budgetID string) error {
	var budget Budget
	if err := gs.db.First(&budget, "id = ?", budgetID).Error; err != nil {
		return fmt.Errorf("failed to load budget %s: %w", budgetID, err)
	}

	gs.mu.Lock()
	gs.budgets[budgetID] = &budget
	gs.mu.Unlock()

	return nil
}

// UpdateRateLimitInMemory updates a specific rate limit in the in-memory cache (called after DB operations)
func (gs *GovernanceStore) UpdateRateLimitInMemory(rateLimitID string, vkValue string) error {
	var rateLimit RateLimit
	if err := gs.db.First(&rateLimit, "id = ?", rateLimitID).Error; err != nil {
		return fmt.Errorf("failed to load rate limit %s: %w", rateLimitID, err)
	}

	gs.mu.Lock()
	vk, exists := gs.virtualKeys[vkValue]
	if exists {
		vk.RateLimit = &rateLimit
	}
	gs.mu.Unlock()

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

	// Rebuild in-memory structures
	gs.mu.Lock()
	defer gs.mu.Unlock()

	gs.rebuildInMemoryStructures(customers, teams, virtualKeys, budgets)

	return nil
}

// rebuildInMemoryStructures rebuilds all in-memory data structures (must be called with write lock)
func (gs *GovernanceStore) rebuildInMemoryStructures(customers []Customer, teams []Team, virtualKeys []VirtualKey, budgets []Budget) {
	// Clear existing data
	gs.virtualKeys = make(map[string]*VirtualKey)
	gs.teams = make(map[string]*Team)
	gs.customers = make(map[string]*Customer)
	gs.budgets = make(map[string]*Budget)

	// Build customers map
	for i := range customers {
		customer := &customers[i]
		gs.customers[customer.ID] = customer
	}

	// Build teams map
	for i := range teams {
		team := &teams[i]
		gs.teams[team.ID] = team
	}

	// Build budgets map
	for i := range budgets {
		budget := &budgets[i]
		gs.budgets[budget.ID] = budget
	}

	// Build virtual keys map and track active VKs
	for i := range virtualKeys {
		vk := &virtualKeys[i]
		gs.virtualKeys[vk.Value] = vk
	}
}

// UTILITY FUNCTIONS

// collectBudgetsFromHierarchy collects budgets and their metadata from the hierarchy (VK → Team → Customer)
func (gs *GovernanceStore) collectBudgetsFromHierarchy(vk *VirtualKey) ([]*Budget, []string) {
	var budgets []*Budget
	var budgetNames []string

	// Collect all budgets in hierarchy order using in-memory data (VK → Team → Customer)
	if vk.BudgetID != nil {
		if budget, exists := gs.budgets[*vk.BudgetID]; exists {
			budgets = append(budgets, budget)
			budgetNames = append(budgetNames, "VK")
		}
	}

	if vk.TeamID != nil {
		if team, exists := gs.teams[*vk.TeamID]; exists && team.BudgetID != nil {
			if budget, exists := gs.budgets[*team.BudgetID]; exists {
				budgets = append(budgets, budget)
				budgetNames = append(budgetNames, "Team")
			}

			// Check if team belongs to a customer
			if team.CustomerID != nil {
				if customer, exists := gs.customers[*team.CustomerID]; exists && customer.BudgetID != nil {
					if budget, exists := gs.budgets[*customer.BudgetID]; exists {
						budgets = append(budgets, budget)
						budgetNames = append(budgetNames, "Customer")
					}
				}
			}
		}
	}

	if vk.CustomerID != nil {
		if customer, exists := gs.customers[*vk.CustomerID]; exists && customer.BudgetID != nil {
			if budget, exists := gs.budgets[*customer.BudgetID]; exists {
				budgets = append(budgets, budget)
				budgetNames = append(budgetNames, "Customer")
			}
		}
	}

	return budgets, budgetNames
}

// collectBudgetIDsFromMemory collects budget IDs from in-memory store data (optimized for performance)
func (gs *GovernanceStore) collectBudgetIDsFromMemory(vk *VirtualKey) []string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	budgets, _ := gs.collectBudgetsFromHierarchy(vk)

	var budgetIDs []string
	for _, budget := range budgets {
		budgetIDs = append(budgetIDs, budget.ID)
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
