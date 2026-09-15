package governance

import (
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// BudgetUsageSnapshot is one budget's usage counters, with none of the
// configuration or relationships that hang off the budget itself.
type BudgetUsageSnapshot struct {
	CurrentUsage float64
	LastReset    time.Time
}

// RateLimitUsageSnapshot is one rate limit's usage counters.
type RateLimitUsageSnapshot struct {
	TokenCurrentUsage   int64
	RequestCurrentUsage int64
	TokenLastReset      time.Time
	RequestLastReset    time.Time
}

// RangeBudgetUsage calls fn for every budget's usage counters, stopping early
// if fn returns false.
//
// This exists because cluster usage sync wants exactly these numbers and
// nothing else. Reaching them through GetGovernanceData meant deep copying
// every virtual key, team and customer, hydrating their budget slices and
// sorting nested lists, then reading two fields off the result and discarding
// the rest. On a deployment with a hundred thousand virtual keys that is a
// hundred thousand struct copies every few seconds on every node, whether or
// not any of it changed.
//
// Values are read from the live map, so a budget being charged concurrently may
// be observed either side of that charge. Usage sync is eventually consistent
// by design and re-reads on the next tick, so a single tick's skew is expected.
func (gs *LocalGovernanceStore) RangeBudgetUsage(fn func(budgetID string, usage BudgetUsageSnapshot) bool) {
	gs.budgets.Range(func(key, value any) bool {
		budgetID, ok := key.(string)
		if !ok {
			return true
		}
		budget, ok := value.(*configstoreTables.TableBudget)
		if !ok || budget == nil {
			return true
		}
		return fn(budgetID, BudgetUsageSnapshot{
			CurrentUsage: budget.CurrentUsage,
			LastReset:    budget.LastReset,
		})
	})
}

// RangeRateLimitUsage calls fn for every rate limit's usage counters, stopping
// early if fn returns false. See RangeBudgetUsage.
func (gs *LocalGovernanceStore) RangeRateLimitUsage(fn func(rateLimitID string, usage RateLimitUsageSnapshot) bool) {
	gs.rateLimits.Range(func(key, value any) bool {
		rateLimitID, ok := key.(string)
		if !ok {
			return true
		}
		rateLimit, ok := value.(*configstoreTables.TableRateLimit)
		if !ok || rateLimit == nil {
			return true
		}
		return fn(rateLimitID, RateLimitUsageSnapshot{
			TokenCurrentUsage:   rateLimit.TokenCurrentUsage,
			RequestCurrentUsage: rateLimit.RequestCurrentUsage,
			TokenLastReset:      rateLimit.TokenLastReset,
			RequestLastReset:    rateLimit.RequestLastReset,
		})
	})
}

// BudgetUsageByID returns one budget's usage counters. Reports false when the
// budget is not in memory, which a caller working from a list of recently
// charged IDs must handle: the budget may have been deleted in between.
func (gs *LocalGovernanceStore) BudgetUsageByID(budgetID string) (BudgetUsageSnapshot, bool) {
	value, ok := gs.budgets.Load(budgetID)
	if !ok {
		return BudgetUsageSnapshot{}, false
	}
	budget, ok := value.(*configstoreTables.TableBudget)
	if !ok || budget == nil {
		return BudgetUsageSnapshot{}, false
	}
	return BudgetUsageSnapshot{
		CurrentUsage: budget.CurrentUsage,
		LastReset:    budget.LastReset,
	}, true
}

// RateLimitUsageByID returns one rate limit's usage counters. See
// BudgetUsageByID.
func (gs *LocalGovernanceStore) RateLimitUsageByID(rateLimitID string) (RateLimitUsageSnapshot, bool) {
	value, ok := gs.rateLimits.Load(rateLimitID)
	if !ok {
		return RateLimitUsageSnapshot{}, false
	}
	rateLimit, ok := value.(*configstoreTables.TableRateLimit)
	if !ok || rateLimit == nil {
		return RateLimitUsageSnapshot{}, false
	}
	return RateLimitUsageSnapshot{
		TokenCurrentUsage:   rateLimit.TokenCurrentUsage,
		RequestCurrentUsage: rateLimit.RequestCurrentUsage,
		TokenLastReset:      rateLimit.TokenLastReset,
		RequestLastReset:    rateLimit.RequestLastReset,
	}, true
}
