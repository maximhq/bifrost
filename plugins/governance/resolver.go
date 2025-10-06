// Package governance provides the budget evaluation and decision engine
package governance

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// Decision represents the result of governance evaluation
type Decision string

const (
	DecisionAllow              Decision = "allow"
	DecisionVirtualKeyNotFound Decision = "virtual_key_not_found"
	DecisionVirtualKeyBlocked  Decision = "virtual_key_blocked"
	DecisionRateLimited        Decision = "rate_limited"
	DecisionBudgetExceeded     Decision = "budget_exceeded"
	DecisionTokenLimited       Decision = "token_limited"
	DecisionRequestLimited     Decision = "request_limited"
	DecisionModelBlocked       Decision = "model_blocked"
	DecisionProviderBlocked    Decision = "provider_blocked"
)

// EvaluationRequest contains the context for evaluating a request
type EvaluationRequest struct {
	VirtualKey string                `json:"virtual_key"`
	Provider   schemas.ModelProvider `json:"provider"`
	Model      string                `json:"model"`
	Headers    map[string]string     `json:"headers"`
	RequestID  string                `json:"request_id"`
}

// EvaluationResult contains the complete result of governance evaluation
type EvaluationResult struct {
	Decision      Decision                     `json:"decision"`
	Reason        string                       `json:"reason"`
	VirtualKey    *configstore.TableVirtualKey `json:"virtual_key,omitempty"`
	RateLimitInfo *configstore.TableRateLimit  `json:"rate_limit_info,omitempty"`
	BudgetInfo    []*configstore.TableBudget   `json:"budget_info,omitempty"` // All budgets in hierarchy
	UsageInfo     *UsageInfo                   `json:"usage_info,omitempty"`
}

// UsageInfo represents current usage levels for rate limits and budgets
type UsageInfo struct {
	// Rate limit usage
	TokensUsedMinute   int64 `json:"tokens_used_minute"`
	TokensUsedHour     int64 `json:"tokens_used_hour"`
	TokensUsedDay      int64 `json:"tokens_used_day"`
	RequestsUsedMinute int64 `json:"requests_used_minute"`
	RequestsUsedHour   int64 `json:"requests_used_hour"`
	RequestsUsedDay    int64 `json:"requests_used_day"`

	// Budget usage
	VKBudgetUsage       int64 `json:"vk_budget_usage"`
	TeamBudgetUsage     int64 `json:"team_budget_usage"`
	CustomerBudgetUsage int64 `json:"customer_budget_usage"`
}

// BudgetResolver provides decision logic for the new hierarchical governance system
type BudgetResolver struct {
	store  *GovernanceStore
	logger schemas.Logger
}

// NewBudgetResolver creates a new budget-based governance resolver
func NewBudgetResolver(store *GovernanceStore, logger schemas.Logger) *BudgetResolver {
	return &BudgetResolver{
		store:  store,
		logger: logger,
	}
}

// EvaluateRequest evaluates a request against the new hierarchical governance system
func (r *BudgetResolver) EvaluateRequest(ctx *context.Context, evaluationRequest *EvaluationRequest) *EvaluationResult {
	// 1. Validate virtual key exists and is active
	vk, exists := r.store.GetVirtualKey(evaluationRequest.VirtualKey)
	if !exists {
		return &EvaluationResult{
			Decision: DecisionVirtualKeyNotFound,
			Reason:   "Virtual key not found",
		}
	}

	if !vk.IsActive {
		return &EvaluationResult{
			Decision: DecisionVirtualKeyBlocked,
			Reason:   "Virtual key is inactive",
		}
	}

	// 2. Check provider filtering
	if !r.isProviderAllowed(vk, evaluationRequest.Provider) {
		return &EvaluationResult{
			Decision:   DecisionProviderBlocked,
			Reason:     fmt.Sprintf("Provider '%s' is not allowed for this virtual key", evaluationRequest.Provider),
			VirtualKey: vk,
		}
	}

	// 3. Check model filtering
	if !r.isModelAllowed(vk, evaluationRequest.Provider, evaluationRequest.Model) {
		return &EvaluationResult{
			Decision:   DecisionModelBlocked,
			Reason:     fmt.Sprintf("Model '%s' is not allowed for this virtual key", evaluationRequest.Model),
			VirtualKey: vk,
		}
	}

	// 4. Check rate limits (VK level only)
	if rateLimitResult := r.checkRateLimits(vk); rateLimitResult != nil {
		return rateLimitResult
	}

	// 5. Check budget hierarchy (VK → Team → Customer)
	if budgetResult := r.checkBudgetHierarchy(*ctx, vk); budgetResult != nil {
		return budgetResult
	}

	if vk.Keys != nil {
		includeOnlyKeys := make([]string, 0, len(vk.Keys))
		for _, dbKey := range vk.Keys {
			includeOnlyKeys = append(includeOnlyKeys, dbKey.KeyID)
		}

		if len(includeOnlyKeys) > 0 {
			*ctx = context.WithValue(*ctx, schemas.BifrostContextKey("bf-governance-include-only-keys"), includeOnlyKeys)
		}
	}

	// All checks passed
	return &EvaluationResult{
		Decision:   DecisionAllow,
		Reason:     "Request allowed by governance policy",
		VirtualKey: vk,
	}
}

// isModelAllowed checks if the requested model is allowed for this VK
func (r *BudgetResolver) isModelAllowed(vk *configstore.TableVirtualKey, provider schemas.ModelProvider, model string) bool {
	// Empty AllowedModels means all models are allowed
	if len(vk.ProviderConfigs) == 0 {
		return true
	}

	for _, pc := range vk.ProviderConfigs {
		if pc.Provider == string(provider) {
			if len(pc.AllowedModels) == 0 {
				return true
			}
			return slices.Contains(pc.AllowedModels, model)
		}
	}

	return false
}

// isProviderAllowed checks if the requested provider is allowed for this VK
func (r *BudgetResolver) isProviderAllowed(vk *configstore.TableVirtualKey, provider schemas.ModelProvider) bool {
	// Empty AllowedProviders means all providers are allowed
	if len(vk.ProviderConfigs) == 0 {
		return true
	}

	for _, pc := range vk.ProviderConfigs {
		if pc.Provider == string(provider) {
			return true
		}
	}

	return false
}

// checkRateLimits checks the VK's rate limits using flexible approach
func (r *BudgetResolver) checkRateLimits(vk *configstore.TableVirtualKey) *EvaluationResult {
	// No rate limits defined
	if vk.RateLimit == nil {
		return nil
	}

	rateLimit := vk.RateLimit

	// Check if any rate limits are exceeded
	var violations []string

	// Token limits
	if rateLimit.TokenMaxLimit != nil && rateLimit.TokenCurrentUsage >= *rateLimit.TokenMaxLimit {
		duration := "unknown"
		if rateLimit.TokenResetDuration != nil {
			duration = *rateLimit.TokenResetDuration
		}
		violations = append(violations, fmt.Sprintf("token limit exceeded (%d/%d, resets every %s)",
			rateLimit.TokenCurrentUsage, *rateLimit.TokenMaxLimit, duration))
	}

	// Request limits
	if rateLimit.RequestMaxLimit != nil && rateLimit.RequestCurrentUsage >= *rateLimit.RequestMaxLimit {
		duration := "unknown"
		if rateLimit.RequestResetDuration != nil {
			duration = *rateLimit.RequestResetDuration
		}
		violations = append(violations, fmt.Sprintf("request limit exceeded (%d/%d, resets every %s)",
			rateLimit.RequestCurrentUsage, *rateLimit.RequestMaxLimit, duration))
	}

	if len(violations) > 0 {
		// Determine specific violation type
		decision := DecisionRateLimited
		if len(violations) == 1 {
			if strings.Contains(violations[0], "token") {
				decision = DecisionTokenLimited
			} else if strings.Contains(violations[0], "request") {
				decision = DecisionRequestLimited
			}
		}

		return &EvaluationResult{
			Decision:      decision,
			Reason:        fmt.Sprintf("Rate limits exceeded: %v", violations),
			VirtualKey:    vk,
			RateLimitInfo: rateLimit,
		}
	}

	return nil // No rate limit violations
}

// checkBudgetHierarchy checks the budget hierarchy atomically (VK → Team → Customer)
func (r *BudgetResolver) checkBudgetHierarchy(ctx context.Context, vk *configstore.TableVirtualKey) *EvaluationResult {
	// Use atomic budget checking to prevent race conditions
	if err := r.store.CheckBudget(ctx, vk); err != nil {
		r.logger.Debug(fmt.Sprintf("Atomic budget check failed for VK %s: %s", vk.ID, err.Error()))

		return &EvaluationResult{
			Decision:   DecisionBudgetExceeded,
			Reason:     fmt.Sprintf("Budget check failed: %s", err.Error()),
			VirtualKey: vk,
		}
	}

	return nil // No budget violations
}
