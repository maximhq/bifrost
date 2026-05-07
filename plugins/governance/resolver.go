// Package governance provides the budget evaluation and decision engine
package governance

import (
	"context"
	"fmt"

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
	DecisionMCPToolBlocked     Decision = "mcp_tool_blocked"
)

// EvaluationRequest contains the context for evaluating a request
type EvaluationRequest struct {
	VirtualKey string                `json:"virtual_key"` // Virtual key value
	Provider   schemas.ModelProvider `json:"provider"`
	Model      string                `json:"model"`
	UserID     string                `json:"user_id,omitempty"` // User ID for user-level governance (enterprise only)
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
	store                   GovernanceStore
	logger                  schemas.Logger
	modelCatalog            *modelcatalog.ModelCatalog
	governanceInMemoryStore InMemoryStore
}

// NewBudgetResolver creates a new budget-based governance resolver
func NewBudgetResolver(store GovernanceStore, modelCatalog *modelcatalog.ModelCatalog, logger schemas.Logger, governanceInMemoryStore InMemoryStore) *BudgetResolver {
	return &BudgetResolver{
		store:                   store,
		logger:                  logger,
		modelCatalog:            modelCatalog,
		governanceInMemoryStore: governanceInMemoryStore,
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
		if decision, err := r.store.CheckProviderRateLimit(ctx, request, nil, nil); err != nil || isRateLimitViolation(decision) {
			return &EvaluationResult{
				Decision: decision,
				Reason:   fmt.Sprintf("Provider-level rate limit check failed: %s", reasonFromErr(err, decision)),
			}
		}
		// 2. Check provider-level budgets FIRST (before model-level checks)
		if decision, err := r.store.CheckProviderBudget(ctx, request, nil); err != nil || isBudgetViolation(decision) {
			return &EvaluationResult{
				Decision: decision,
				Reason:   fmt.Sprintf("Provider-level budget exceeded: %s", reasonFromErr(err, decision)),
			}
		}
	}
	// 3. Check model-level rate limits (after provider-level checks)
	if model != "" {
		if decision, err := r.store.CheckModelRateLimit(ctx, request, nil, nil); err != nil || isRateLimitViolation(decision) {
			return &EvaluationResult{
				Decision: decision,
				Reason:   fmt.Sprintf("Model-level rate limit check failed: %s", reasonFromErr(err, decision)),
			}
		}

		// 4. Check model-level budgets (after provider-level checks)
		if decision, err := r.store.CheckModelBudget(ctx, request, nil); err != nil || isBudgetViolation(decision) {
			return &EvaluationResult{
				Decision: decision,
				Reason:   fmt.Sprintf("Model-level budget exceeded: %s", reasonFromErr(err, decision)),
			}
		}
	}
	// All provider-level and model-level checks passed
	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "Request allowed by governance policy (provider-level and model-level checks passed)",
	}
}

func (r *BudgetResolver) EvaluateCustomerRequest(ctx *schemas.BifrostContext, customerID string, request *EvaluationRequest) *EvaluationResult {
	// Skip if no customerID
	if customerID == "" {
		return &EvaluationResult{
			Decision: DecisionAllow,
			Reason:   "No customer ID provided, skipping customer-level checks",
		}
	}
	// Check customer-level rate limits
	if decision, err := r.store.CheckCustomerRateLimit(ctx, customerID, request, nil, nil); err != nil || isRateLimitViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("Customer-level rate limit exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	// Check customer-level budget
	if decision, err := r.store.CheckCustomerBudget(ctx, customerID, request, nil); err != nil || isBudgetViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("Customer-level budget exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "Customer-level checks passed",
	}
}

func (r *BudgetResolver) EvaluateTeamRequest(ctx *schemas.BifrostContext, teamID string, request *EvaluationRequest) *EvaluationResult {
	// Skip if no teamID
	if teamID == "" {
		return &EvaluationResult{
			Decision: DecisionAllow,
			Reason:   "No team ID provided, skipping team-level checks",
		}
	}
	// Check team-level rate limits
	if decision, err := r.store.CheckTeamRateLimit(ctx, teamID, request, nil, nil); err != nil || isRateLimitViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("Team-level rate limit exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	// Check team-level budget
	if decision, err := r.store.CheckTeamBudget(ctx, teamID, request, nil); err != nil || isBudgetViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("Team-level budget exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "Team-level checks passed",
	}

}

// EvaluateUserRequest evaluates user-level rate limits and budgets (enterprise-only)
// This runs after provider/model checks but before VK checks
// Returns DecisionAllow if userID is empty or user has no governance configured
func (r *BudgetResolver) EvaluateUserRequest(ctx *schemas.BifrostContext, userID string, request *EvaluationRequest) *EvaluationResult {
	// Skip if no userID (non-enterprise or anonymous request)
	if userID == "" {
		return &EvaluationResult{
			Decision: DecisionAllow,
			Reason:   "No user ID provided, skipping user-level checks",
		}
	}

	// Check user-level rate limits
	if decision, err := r.store.CheckUserRateLimit(ctx, userID, request, nil, nil); err != nil || isRateLimitViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("User-level rate limit exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	// Check user-level budget
	if decision, err := r.store.CheckUserBudget(ctx, userID, request, nil); err != nil || isBudgetViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("User-level budget exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "User-level checks passed",
	}
}

// isModelRequired checks if the requested model is required for this request
func (r *BudgetResolver) isModelRequired(requestType schemas.RequestType) bool {
	// Here we will have to check for some requests which do not need model
	// For example, batches, container, files, videos, passthrough requests
	// For these requests, we will only check for provider filtering
	if requestType == schemas.ListModelsRequest || requestType == schemas.MCPToolExecutionRequest || requestType == schemas.BatchCreateRequest || requestType == schemas.BatchListRequest || requestType == schemas.BatchRetrieveRequest || requestType == schemas.BatchCancelRequest || requestType == schemas.BatchResultsRequest || requestType == schemas.FileUploadRequest || requestType == schemas.FileListRequest || requestType == schemas.FileRetrieveRequest || requestType == schemas.FileDeleteRequest || requestType == schemas.FileContentRequest || requestType == schemas.ContainerCreateRequest || requestType == schemas.ContainerListRequest || requestType == schemas.ContainerRetrieveRequest || requestType == schemas.ContainerDeleteRequest || requestType == schemas.ContainerFileCreateRequest || requestType == schemas.ContainerFileListRequest || requestType == schemas.ContainerFileRetrieveRequest || requestType == schemas.ContainerFileContentRequest || requestType == schemas.ContainerFileDeleteRequest || requestType == schemas.VideoRetrieveRequest || requestType == schemas.VideoDownloadRequest || requestType == schemas.VideoListRequest || requestType == schemas.VideoDeleteRequest || requestType == schemas.VideoRemixRequest || requestType == schemas.PassthroughRequest || requestType == schemas.PassthroughStreamRequest {
		return false
	}
	return true
}

// EvaluateVirtualKeyRequest evaluates virtual key-specific checks including validation, filtering, rate limits, and budgets
// skipRateLimitsAndBudgets evaluates to true when we want to skip rate limits and budgets. This is used when user auth is present (user governance handles limits).
func (r *BudgetResolver) EvaluateVirtualKeyRequest(ctx *schemas.BifrostContext, virtualKeyValue string, provider schemas.ModelProvider, model string, requestType schemas.RequestType, skipRateLimitsAndBudgets bool) *EvaluationResult {
	// 1. Validate virtual key exists and is active
	vk, exists := r.store.GetVirtualKey(ctx, virtualKeyValue)
	if !exists {
		return &EvaluationResult{
			Decision: DecisionVirtualKeyNotFound,
			Reason:   "Virtual key not found",
		}
	}
	// Set virtual key id and name in context
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, vk.ID)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, vk.Name)
	if vk.Team != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, vk.Team.ID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, vk.Team.Name)
		if vk.Team.Customer != nil {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, vk.Team.Customer.ID)
			ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, vk.Team.Customer.Name)
		}
	}
	if vk.Customer != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, vk.Customer.ID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, vk.Customer.Name)
	}
	if !vk.IsActiveValue() {
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
	if r.isModelRequired(requestType) && !r.isModelAllowed(vk, provider, model) {
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
	if !skipRateLimitsAndBudgets {
		if rateLimitResult := r.checkRateLimitHierarchy(ctx, vk, evaluationRequest); rateLimitResult != nil {
			return rateLimitResult
		}

		// 5. Check budget hierarchy (VK → Team → Customer)
		if budgetResult := r.checkBudgetHierarchy(ctx, vk, evaluationRequest); budgetResult != nil {
			return budgetResult
		}
	}

	// Find the provider config that matches the request's provider and apply key filtering
	for _, pc := range vk.ProviderConfigs {
		if schemas.ModelProvider(pc.Provider) == provider {
			if !pc.AllowAllKeys {
				// Restrict to specific keys (empty slice = no keys allowed)
				includeOnlyKeys := make([]string, 0, len(pc.Keys))
				for _, dbKey := range pc.Keys {
					includeOnlyKeys = append(includeOnlyKeys, dbKey.KeyID)
				}
				ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, includeOnlyKeys)
			}
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
	// Empty ProviderConfigs means no models are allowed (deny-by-default)
	if len(vk.ProviderConfigs) == 0 {
		return false
	}

	for _, pc := range vk.ProviderConfigs {
		if pc.Provider == string(provider) {
			// Delegate model allowance check to model catalog
			// This handles all cross-provider logic (OpenRouter, Vertex, Groq, Bedrock)
			// and provider-prefixed allowed_models entries
			if r.modelCatalog != nil && r.governanceInMemoryStore != nil {
				providerConfig, ok := r.governanceInMemoryStore.GetConfiguredProviders()[provider]
				providerConfigPtr := &providerConfig
				if !ok {
					providerConfigPtr = nil
				}
				return r.modelCatalog.IsModelAllowedForProvider(provider, model, providerConfigPtr, pc.AllowedModels)
			}
			// Fallback when model catalog is not available: simple string matching
			// ["*"] = allow all models; [] = deny all models
			return pc.AllowedModels.IsAllowed(model)
		}
	}

	return false
}

// isProviderAllowed checks if the requested provider is allowed for this VK
func (r *BudgetResolver) isProviderAllowed(vk *configstoreTables.TableVirtualKey, provider schemas.ModelProvider) bool {
	// Empty ProviderConfigs means no providers are allowed (deny-by-default)
	if len(vk.ProviderConfigs) == 0 {
		return false
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
	if decision, err := r.store.CheckVirtualKeyRateLimit(ctx, vk, request, nil, nil); err != nil || isRateLimitViolation(decision) {
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
			Reason:        fmt.Sprintf("Rate limit check failed: %s", reasonFromErr(err, decision)),
			VirtualKey:    vk,
			RateLimitInfo: rateLimitInfo,
		}
	}

	return nil // No rate limit violations
}

// checkBudgetHierarchy checks the budget hierarchy atomically (VK → Team → Customer)
func (r *BudgetResolver) checkBudgetHierarchy(ctx context.Context, vk *configstoreTables.TableVirtualKey, request *EvaluationRequest) *EvaluationResult {
	// Use atomic budget checking to prevent race conditions
	if decision, err := r.store.CheckVirtualKeyBudget(ctx, vk, request, nil); err != nil || isBudgetViolation(decision) {
		r.logger.Debug(fmt.Sprintf("Atomic budget exceeded for VK %s: %s", vk.ID, reasonFromErr(err, decision)))
		return &EvaluationResult{
			Decision:   decision,
			Reason:     fmt.Sprintf("Budget exceeded: %s", reasonFromErr(err, decision)),
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
	if _, err := r.store.CheckProviderBudget(ctx, request, nil); err != nil {
		r.logger.Debug(fmt.Sprintf("Global provider budget exceeded for provider %s: %s", config.Provider, err.Error()))
		return true
	}

	// 2. Check VK-level provider config budget
	if len(config.Budgets) == 0 {
		return false
	}
	if _, err := r.store.CheckVirtualKeyBudget(ctx, vk, request, nil); err != nil {
		r.logger.Debug(fmt.Sprintf("VK provider config budget exceeded for VK %s: %s", vk.ID, err.Error()))
		return true
	}
	return false
}

// isProviderRateLimitViolated checks if a provider config's rate limit is violated
func (r *BudgetResolver) isProviderRateLimitViolated(ctx context.Context, vk *configstoreTables.TableVirtualKey, config configstoreTables.TableVirtualKeyProviderConfig) bool {
	request := &EvaluationRequest{Provider: schemas.ModelProvider(config.Provider)}

	// 1. Check global provider-level rate limit first
	if decision, err := r.store.CheckProviderRateLimit(ctx, request, nil, nil); err != nil || isRateLimitViolation(decision) {
		r.logger.Debug(fmt.Sprintf("Global provider rate limit exceeded for provider %s", config.Provider))
		return true
	}

	// 2. Check VK-level provider config rate limit
	if config.RateLimit == nil {
		return false
	}
	decision, err := r.store.CheckVirtualKeyRateLimit(ctx, vk, request, nil, nil)
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

// isBudgetViolation returns true if the decision indicates a budget violation.
func isBudgetViolation(decision Decision) bool {
	return decision == DecisionBudgetExceeded
}

// reasonFromErr yields a non-nil-safe reason string. When the store returns a
// non-allow decision without an accompanying error, err.Error() would panic —
// fall back to a generic phrase that still names the decision.
func reasonFromErr(err error, decision Decision) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("policy violation (%s)", decision)
}
