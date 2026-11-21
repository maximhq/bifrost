package requestguard

// BudgetViolation describes one exceeded request budget. Limit and Actual are
// represented as float64 so token and USD metrics share one compact shape.
type BudgetViolation struct {
	Metric string  `json:"metric"`
	Limit  float64 `json:"limit"`
	Actual float64 `json:"actual"`
	Reason string  `json:"reason"`
}

// BudgetResult reports the decision for a request estimate and keeps the
// original estimate available for explainability and recommendation ranking.
type BudgetResult struct {
	Passed          bool              `json:"passed"`
	Action          string            `json:"action"`
	Estimate        CostEstimate      `json:"estimate"`
	Violations      []BudgetViolation `json:"violations,omitempty"`
	ShouldBlock     bool              `json:"should_block"`
	ShouldDowngrade bool              `json:"should_downgrade"`
	ShouldWarn      bool              `json:"should_warn"`
}

// EvaluateBudget applies configured token and pricing limits to an estimate.
// A zero limit means that limit is disabled. Cost is checked only when catalog
// pricing was available for the estimate.
func EvaluateBudget(cfg BudgetGuardConfig, estimate CostEstimate) BudgetResult {
	result := BudgetResult{Passed: true, Action: CapabilityActionAllow, Estimate: estimate}
	if cfg.Mode == PolicyOff || cfg.Mode == "" {
		return result
	}
	if cfg.MaxInputTokens > 0 && estimate.InputTokens > cfg.MaxInputTokens {
		result.Violations = append(result.Violations, BudgetViolation{
			Metric: "input_tokens", Limit: float64(cfg.MaxInputTokens), Actual: float64(estimate.InputTokens),
			Reason: "estimated input tokens exceed the configured limit",
		})
	}
	if cfg.MaxTotalTokens > 0 && estimate.TotalTokens > cfg.MaxTotalTokens {
		result.Violations = append(result.Violations, BudgetViolation{
			Metric: "total_tokens", Limit: float64(cfg.MaxTotalTokens), Actual: float64(estimate.TotalTokens),
			Reason: "estimated total tokens exceed the configured limit",
		})
	}
	if cfg.MaxEstimatedCostUSD > 0 && estimate.PricingAvailable && estimate.EstimatedCostUSD > cfg.MaxEstimatedCostUSD {
		result.Violations = append(result.Violations, BudgetViolation{
			Metric: "estimated_cost_usd", Limit: cfg.MaxEstimatedCostUSD, Actual: estimate.EstimatedCostUSD,
			Reason: "estimated cost exceeds the configured limit",
		})
	}
	if len(result.Violations) == 0 {
		return result
	}

	switch cfg.Mode {
	case PolicyAudit:
		result.Action = CapabilityActionAudit
	case PolicyWarn:
		result.Action = CapabilityActionWarn
		result.ShouldWarn = true
	case PolicyBlock:
		result.Action = CapabilityActionBlock
		result.Passed = false
		result.ShouldBlock = true
	case PolicyDowngrade:
		result.Action = CapabilityActionDowngrade
		result.Passed = false
		result.ShouldDowngrade = true
	default:
		// Validation rejects unknown modes. Direct callers fail open and retain
		// the evidence as audit information.
		result.Action = CapabilityActionAudit
	}
	return result
}
