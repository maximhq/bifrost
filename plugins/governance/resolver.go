// Package governance provides the budget evaluation and decision engine
package governance

import (
	"context"
	"fmt"
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
	VirtualKey string                `json:"virtual_key"` // Virtual key value
	Provider   schemas.ModelProvider `json:"provider"`
	Model      string                `json:"model"`
}

// EvaluationResult contains the complete result of governance evaluation
type EvaluationResult struct {
	Decision      Decision                           `json:"decision"`
	Reason        string                             `json:"reason"`
	VirtualKey    *configstoreTables.TableVirtualKey `json:"virtual_key,omitempty"`
	RateLimitInfo *configstoreTables.TableRateLimit  `json:"rate_limit_info,omitempty"`
	BudgetInfo    []*configstoreTables.TableBudget   `json:"budget_info,omitempty"` // All budgets in hierarchy
	UsageInfo     *UsageInfo                         `json:"usage_info,omitempty"`
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
	store        GovernanceStore
	logger       schemas.Logger
	modelCatalog *modelcatalog.ModelCatalog
}

// NewBudgetResolver creates a new budget-based governance resolver
func NewBudgetResolver(store GovernanceStore, modelCatalog *modelcatalog.ModelCatalog, logger schemas.Logger) *BudgetResolver {
	return &BudgetResolver{
		store:        store,
		logger:       logger,
		modelCatalog: modelCatalog,
	}
}

// EvaluateModelAndProviderRequest evaluates provider-level and model-level rate limits and budgets
// This applies even when virtual keys are disabled or not present
func (r *BudgetResolver) EvaluateModelAndProviderRequest(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) *EvaluationResult {
	// Create evaluation request for the checks
	request := &EvaluationRequest{
		Provider: provider,
		Model:    model,
	}

	// 1. Check provider-level rate limits FIRST (before model-level checks)
	if provider != "" {
		if err, decision := r.store.CheckProviderRateLimit(ctx, request, nil, nil); err != nil {
			return &EvaluationResult{
				Decision: decision,
				Reason:   fmt.Sprintf("Provider-level rate limit check failed: %s", err.Error()),
			}
		}

		// 2. Check provider-level budgets FIRST (before model-level checks)
		if err := r.store.CheckProviderBudget(ctx, request, nil); err != nil {
			return &EvaluationResult{
				Decision: DecisionBudgetExceeded,
				Reason:   fmt.Sprintf("Provider-level budget exceeded: %s", err.Error()),
			}
		}
	}

	// 3. Check model-level rate limits (after provider-level checks)
	if model != "" {
		if err, decision := r.store.CheckModelRateLimit(ctx, request, nil, nil); err != nil {
			return &EvaluationResult{
				Decision: decision,
				Reason:   fmt.Sprintf("Model-level rate limit check failed: %s", err.Error()),
			}
		}

		// 4. Check model-level budgets (after provider-level checks)
		if err := r.store.CheckModelBudget(ctx, request, nil); err != nil {
			return &EvaluationResult{
				Decision: DecisionBudgetExceeded,
				Reason:   fmt.Sprintf("Model-level budget exceeded: %s", err.Error()),
			}
		}
	}

	// All provider-level and model-level checks passed
	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "Request allowed by governance policy (provider-level and model-level checks passed)",
	}
}

// EvaluateVirtualKeyRequest evaluates virtual key-specific checks including validation, filtering, rate limits, and budgets
func (r *BudgetResolver) EvaluateVirtualKeyRequest(ctx *schemas.BifrostContext, virtualKeyValue string, provider schemas.ModelProvider, model string, requestType schemas.RequestType) *EvaluationResult {
	// 1. Validate virtual key exists and is active
	vk, exists := r.store.GetVirtualKey(virtualKeyValue)
	if !exists {
		return &EvaluationResult{
			Decision: DecisionVirtualKeyNotFound,
			Reason:   "Virtual key not found",
		}
	}

	// Set virtual key id and name in context
	ctx.SetValue(schemas.BifrostContextKey("bf-governance-virtual-key-id"), vk.ID)
	ctx.SetValue(schemas.BifrostContextKey("bf-governance-virtual-key-name"), vk.Name)
	if vk.Team != nil {
		ctx.SetValue(schemas.BifrostContextKey("bf-governance-team-id"), vk.Team.ID)
		ctx.SetValue(schemas.BifrostContextKey("bf-governance-team-name"), vk.Team.Name)
		if vk.Team.Customer != nil {
			ctx.SetValue(schemas.BifrostContextKey("bf-governance-customer-id"), vk.Team.Customer.ID)
			ctx.SetValue(schemas.BifrostContextKey("bf-governance-customer-name"), vk.Team.Customer.Name)
		}
	}
	if vk.Customer != nil {
		ctx.SetValue(schemas.BifrostContextKey("bf-governance-customer-id"), vk.Customer.ID)
		ctx.SetValue(schemas.BifrostContextKey("bf-governance-customer-name"), vk.Customer.Name)
	}

	if !vk.IsActive {
		return &EvaluationResult{
			Decision: DecisionVirtualKeyBlocked,
			Reason:   "Virtual key is inactive",
		}
	}

	// 2. Check provider filtering
	if requestType != schemas.MCPToolExecutionRequest && !r.isProviderAllowed(vk, provider) {
		return &EvaluationResult{
			Decision:   DecisionProviderBlocked,
			Reason:     fmt.Sprintf("Provider '%s' is not allowed for this virtual key", provider),
			VirtualKey: vk,
		}
	}

	// 3. Check model filtering
	if requestType != schemas.MCPToolExecutionRequest && !r.isModelAllowed(vk, provider, model) {
		return &EvaluationResult{
			Decision:   DecisionModelBlocked,
			Reason:     fmt.Sprintf("Model '%s' is not allowed for this virtual key", model),
			VirtualKey: vk,
		}
	}

	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKeyValue,
		Provider:   provider,
		Model:      model,
	}

	// 4. Check rate limits hierarchy (VK level)
	if rateLimitResult := r.checkRateLimitHierarchy(ctx, vk, evaluationRequest); rateLimitResult != nil {
		return rateLimitResult
	}

	// 5. Check budget hierarchy (VK → Team → Customer)
	if budgetResult := r.checkBudgetHierarchy(ctx, vk, evaluationRequest); budgetResult != nil {
		return budgetResult
	}

	// Find the provider config that matches the request's provider and get its allowed keys
	for _, pc := range vk.ProviderConfigs {
		if schemas.ModelProvider(pc.Provider) == provider && len(pc.Keys) > 0 {
			includeOnlyKeys := make([]string, 0, len(pc.Keys))
			for _, dbKey := range pc.Keys {
				includeOnlyKeys = append(includeOnlyKeys, dbKey.KeyID)
			}
			ctx.SetValue(schemas.BifrostContextKey("bf-governance-include-only-keys"), includeOnlyKeys)
			break
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
func (r *BudgetResolver) isModelAllowed(vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider, model string) bool {
	// Empty ProviderConfigs means all models are allowed
	if len(vk.ProviderConfigs) == 0 {
		return true
	}

	for _, pc := range vk.ProviderConfigs {
		if pc.Provider == string(provider) {
			// Delegate model allowance check to model catalog
			// This handles all cross-provider logic (OpenRouter, Vertex, Groq, Bedrock)
			// and provider-prefixed allowed_models entries
			if r.modelCatalog != nil {
				return r.modelCatalog.IsModelAllowedForProvider(provider, model, pc.AllowedModels)
			}
			// Fallback when model catalog is not available: simple string matching
			if len(pc.AllowedModels) == 0 {
				return true
			}
			return slices.Contains(pc.AllowedModels, model)
		}
	}

	return false
}

// isProviderAllowed checks if the requested provider is allowed for this VK
func (r *BudgetResolver) isProviderAllowed(vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider) bool {
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

// checkRateLimitHierarchy checks provider-level rate limits first, then VK rate limits using flexible approach
func (r *BudgetResolver) checkRateLimitHierarchy(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest) *EvaluationResult {
	if decision, err := r.store.CheckRateLimit(ctx, vk, request, nil, nil); err != nil {
		// Check provider-level first (matching check order), then VK-level
		var rateLimitInfo *configstoreTables.TableRateLimit
		for _, pc := range vk.ProviderConfigs {
			if pc.Provider == string(request.Provider) && pc.RateLimit != nil {
				rateLimitInfo = pc.RateLimit
				break
			}
		}
		if rateLimitInfo == nil && vk.RateLimit != nil {
			rateLimitInfo = vk.RateLimit
		}
		return &EvaluationResult{
			Decision:      decision,
			Reason:        fmt.Sprintf("Rate limit check failed: %s", err.Error()),
			VirtualKey:    vk,
			RateLimitInfo: rateLimitInfo,
		}
	}

	return nil // No rate limit violations
}

// checkBudgetHierarchy checks the budget hierarchy atomically (VK → Team → Customer)
func (r *BudgetResolver) checkBudgetHierarchy(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest) *EvaluationResult {
	// Use atomic budget checking to prevent race conditions
	if err := r.store.CheckBudget(ctx, vk, request, nil); err != nil {
		r.logger.Debug(fmt.Sprintf("Atomic budget exceeded for VK %s: %s", vk.ID, err.Error()))

		return &EvaluationResult{
			Decision:   DecisionBudgetExceeded,
			Reason:     fmt.Sprintf("Budget exceeded: %s", err.Error()),
			VirtualKey: vk,
		}
	}

	return nil // No budget violations
}

// Helper methods for provider config validation (used by TransportInterceptor)

// isProviderBudgetViolated checks if a provider config's budget is violated
func (r *BudgetResolver) isProviderBudgetViolated(ctx context.Context, vk *configstoreTables.TableVirtualKey, config configstoreTables.TableVirtualKeyProviderConfig) bool {
	request := &EvaluationRequest{Provider: schemas.ModelProvider(config.Provider)}

	// 1. Check global provider-level budget first
	if err := r.store.CheckProviderBudget(ctx, request, nil); err != nil {
		r.logger.Debug(fmt.Sprintf("Global provider budget exceeded for provider %s: %s", config.Provider, err.Error()))
		return true
	}

	// 2. Check VK-level provider config budget
	if config.Budget == nil {
		return false
	}
	if err := r.store.CheckBudget(ctx, vk, request, nil); err != nil {
		r.logger.Debug(fmt.Sprintf("VK provider config budget exceeded for VK %s: %s", vk.ID, err.Error()))
		return true
	}
	return false
}

// isProviderRateLimitViolated checks if a provider config's rate limit is violated
func (r *BudgetResolver) isProviderRateLimitViolated(ctx context.Context, vk *configstoreTables.TableVirtualKey, config configstoreTables.TableVirtualKeyProviderConfig) bool {
	request := &EvaluationRequest{Provider: schemas.ModelProvider(config.Provider)}

	// 1. Check global provider-level rate limit first
	if err, decision := r.store.CheckProviderRateLimit(ctx, request, nil, nil); err != nil || isRateLimitViolation(decision) {
		r.logger.Debug(fmt.Sprintf("Global provider rate limit exceeded for provider %s", config.Provider))
		return true
	}

	// 2. Check VK-level provider config rate limit
	if config.RateLimit == nil {
		return false
	}
	decision, err := r.store.CheckRateLimit(ctx, vk, request, nil, nil)
	if err != nil || isRateLimitViolation(decision) {
		r.logger.Debug(fmt.Sprintf("VK provider config rate limit exceeded for VK %s, provider %s", vk.ID, config.Provider))
		return true
	}
	return false
}

// isRateLimitViolation returns true if the decision indicates a rate limit violation
func isRateLimitViolation(decision Decision) bool {
	return decision == DecisionRateLimited || decision == DecisionTokenLimited || decision == DecisionRequestLimited
}
