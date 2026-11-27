package governance

import (
	"context"
	"time"
)

// CheckBudget checks the budgets of each entity in hierarchy order against their baselines, the
// per-entity form of CheckBudgets the budget tests drive.
func (gs *LocalGovernanceStore) CheckBudget(ctx context.Context, entityWiseBudgets EntityWiseBudgets, baselines map[string]float64) (Decision, error) {
	// Check each budget in hierarchy order using in-memory data
	now := time.Now()
	for entity, budgets := range entityWiseBudgets {
		for _, budget := range budgets {
			if decision, err := gs.checkBudget(entity, budget, baselines, now); err != nil {
				return decision, err
			}
		}
	}
	return DecisionAllow, nil
}
