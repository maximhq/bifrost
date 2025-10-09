// Package governance provides comprehensive governance plugin for Bifrost
package governance

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/pricing"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	governanceRejectedContextKey    contextKey = "bf-governance-rejected"
	governanceIsCacheReadContextKey contextKey = "bf-governance-is-cache-read"
	governanceIsBatchContextKey     contextKey = "bf-governance-is-batch"
)

// Config is the configuration for the governance plugin
type Config struct {
	IsVkMandatory *bool `json:"is_vk_mandatory"`
}

type InMemoryStore interface {
	GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig
}

// GovernancePlugin implements the main governance plugin with hierarchical budget system
type GovernancePlugin struct {
	ctx        context.Context
	cancelFunc context.CancelFunc

	// Core components with clear separation of concerns
	store    *GovernanceStore // Pure data access layer
	resolver *BudgetResolver  // Pure decision engine for hierarchical governance
	tracker  *UsageTracker    // Business logic owner (updates, resets, persistence)

	// Dependencies
	configStore    configstore.ConfigStore
	pricingManager *pricing.PricingManager
	logger         schemas.Logger

	// Transport dependencies
	inMemoryStore InMemoryStore

	isVkMandatory *bool
}

// Init initializes and returns a governance plugin instance.
//
// It wires the core components (store, resolver, tracker), performs a best-effort
// startup reset of expired limits when a persistent `configstore.ConfigStore` is
// provided, and establishes a cancellable plugin context used by background work.
//
// Behavior and defaults:
//   - Enables all governance features with optimized defaults.
//   - If `store` is nil, the plugin runs in-memory only (no persistence).
//   - If `pricingManager` is nil, cost calculation is skipped.
//   - `config.IsVkMandatory` controls whether `x-bf-vk` is required in PreHook.
//   - `inMemoryStore` is used by TransportInterceptor to validate configured providers
//     and build provider-prefixed models; it may be nil. When nil, transport-level
//     provider validation/routing is skipped and existing model strings are left
//     unchanged. This is safe and recommended when using the plugin directly from
//     the Go SDK without the HTTP transport.
//
// Parameters:
//   - ctx: base context for the plugin; a child context with cancel is created.
//   - config: plugin flags; may be nil.
//   - logger: logger used by all subcomponents.
//   - store: configuration store used for persistence; may be nil.
//   - governanceConfig: initial/seed governance configuration for the store.
//   - pricingManager: optional pricing manager to compute request cost.
//   - inMemoryStore: provider registry used for routing/validation in transports.
//
// Returns:
//   - *GovernancePlugin on success.
//   - error if the governance store fails to initialize.
//
// Side effects:
//   - Logs warnings when optional dependencies are missing.
//   - May perform startup resets via the usage tracker when `store` is non-nil.
func Init(
	ctx context.Context,
	config *Config,
	logger schemas.Logger,
	store configstore.ConfigStore,
	governanceConfig *configstore.GovernanceConfig,
	pricingManager *pricing.PricingManager,
	inMemoryStore InMemoryStore,
) (*GovernancePlugin, error) {
	if store == nil {
		logger.Warn("governance plugin requires config store to persist data, running in memory only mode")
	}
	if pricingManager == nil {
		logger.Warn("governance plugin requires pricing manager to calculate cost, all cost calculations will be skipped.")
	}

	// Handle nil config - use safe default for IsVkMandatory
	var isVkMandatory *bool
	if config != nil {
		isVkMandatory = config.IsVkMandatory
	}

	governanceStore, err := NewGovernanceStore(ctx, logger, store, governanceConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize governance store: %w", err)
	}
	// Initialize components in dependency order with fixed, optimal settings
	// Resolver (pure decision engine for hierarchical governance, depends only on store)
	resolver := NewBudgetResolver(governanceStore, logger)

	// 3. Tracker (business logic owner, depends on store and resolver)
	tracker := NewUsageTracker(ctx, governanceStore, resolver, store, logger)

	// 4. Perform startup reset check for any expired limits from downtime
	if store != nil {
		if err := tracker.PerformStartupResets(ctx); err != nil {
			logger.Warn("startup reset failed: %v", err)
			// Continue initialization even if startup reset fails (non-critical)
		}
	}
	ctx, cancelFunc := context.WithCancel(ctx)
	plugin := &GovernancePlugin{
		ctx:            ctx,
		cancelFunc:     cancelFunc,
		store:          governanceStore,
		resolver:       resolver,
		tracker:        tracker,
		configStore:    store,
		pricingManager: pricingManager,
		logger:         logger,
		isVkMandatory:  isVkMandatory,
		inMemoryStore:  inMemoryStore,
	}

	return plugin, nil
}

// GetName returns the name of the plugin
func (p *GovernancePlugin) GetName() string {
	return PluginName
}

// TransportInterceptor intercepts requests before they are processed (governance decision point)
func (p *GovernancePlugin) TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	var virtualKeyValue string

	for header, value := range headers {
		if strings.ToLower(string(header)) == "x-bf-vk" {
			virtualKeyValue = string(value)
			break
		}
	}
	if virtualKeyValue == "" {
		return headers, body, nil
	}

	// Check if the request has a model field
	modelValue, hasModel := body["model"]
	if !hasModel {
		return headers, body, nil
	}
	modelStr, ok := modelValue.(string)
	if !ok || modelStr == "" {
		return headers, body, nil
	}

	// Check if model already has provider prefix (contains "/")
	if strings.Contains(modelStr, "/") {
		provider, _ := schemas.ParseModelString(modelStr, "")
		// Checking valid provider when store is available; if store is nil,
		// assume the prefixed model should be left unchanged.
		if p.inMemoryStore != nil {
			if _, ok := p.inMemoryStore.GetConfiguredProviders()[provider]; ok {
				return headers, body, nil
			}
		} else {
			return headers, body, nil
		}
	}

	virtualKey, ok := p.store.GetVirtualKey(virtualKeyValue)
	if !ok || virtualKey == nil || !virtualKey.IsActive {
		return headers, body, nil
	}

	// Get provider configs for this virtual key
	providerConfigs := virtualKey.ProviderConfigs
	if len(providerConfigs) == 0 {
		// No provider configs, continue without modification
		return headers, body, nil
	}
	allowedProviderConfigs := make([]configstore.TableVirtualKeyProviderConfig, 0)
	for _, config := range providerConfigs {
		if len(config.AllowedModels) == 0 || slices.Contains(config.AllowedModels, modelStr) {
			allowedProviderConfigs = append(allowedProviderConfigs, config)
		}
	}
	if len(allowedProviderConfigs) == 0 {
		// No allowed provider configs, continue without modification
		return headers, body, nil
	}
	// Weighted random selection from allowed providers for the main model
	totalWeight := 0.0
	for _, config := range allowedProviderConfigs {
		totalWeight += config.Weight
	}
	// Generate random number between 0 and totalWeight
	randomValue := rand.Float64() * totalWeight
	// Select provider based on weighted random selection
	var selectedProvider schemas.ModelProvider
	currentWeight := 0.0
	for _, config := range allowedProviderConfigs {
		currentWeight += config.Weight
		if randomValue <= currentWeight {
			selectedProvider = schemas.ModelProvider(config.Provider)
			break
		}
	}
	// Fallback: if no provider was selected (shouldn't happen but guard against FP issues)
	if selectedProvider == "" && len(allowedProviderConfigs) > 0 {
		selectedProvider = schemas.ModelProvider(allowedProviderConfigs[0].Provider)
	}
	// Update the model field in the request body
	body["model"] = string(selectedProvider) + "/" + modelStr

	// Check if fallbacks field is already present
	_, hasFallbacks := body["fallbacks"]
	if !hasFallbacks && len(allowedProviderConfigs) > 1 {
		// Sort allowed provider configs by weight (descending)
		sort.Slice(allowedProviderConfigs, func(i, j int) bool {
			return allowedProviderConfigs[i].Weight > allowedProviderConfigs[j].Weight
		})

		// Filter out the selected provider and create fallbacks array
		fallbacks := make([]string, 0, len(allowedProviderConfigs)-1)
		for _, config := range allowedProviderConfigs {
			if config.Provider != string(selectedProvider) {
				fallbacks = append(fallbacks, string(schemas.ModelProvider(config.Provider))+"/"+modelStr)
			}
		}

		// Add fallbacks to request body
		body["fallbacks"] = fallbacks
	}

	return headers, body, nil
}

// PreHook intercepts requests before they are processed (governance decision point)
func (p *GovernancePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Extract governance headers and virtual key using utility functions
	headers := extractHeadersFromContext(*ctx)
	virtualKey := getStringFromContext(*ctx, schemas.BifrostContextKeyVirtualKeyHeader)
	requestID := getStringFromContext(*ctx, schemas.BifrostContextKeyRequestID)

	if virtualKey == "" {
		if p.isVkMandatory != nil && *p.isVkMandatory {
			return req, &schemas.PluginShortCircuit{
				Error: &schemas.BifrostError{
					Type:       bifrost.Ptr("virtual_key_required"),
					StatusCode: bifrost.Ptr(400),
					Error: &schemas.ErrorField{
						Message: "x-bf-vk header is missing",
					},
				},
			}, nil
		} else {
			return req, nil, nil
		}
	}

	// Extract provider and model from request
	provider := req.Provider
	model := req.Model

	// Create request context for evaluation
	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKey,
		Provider:   provider,
		Model:      model,
		Headers:    headers,
		RequestID:  requestID,
	}

	// Use resolver to make governance decision (pure decision engine)
	result := p.resolver.EvaluateRequest(ctx, evaluationRequest)

	if result.Decision != DecisionAllow {
		if ctx != nil {
			if _, ok := (*ctx).Value(governanceRejectedContextKey).(bool); !ok {
				*ctx = context.WithValue(*ctx, governanceRejectedContextKey, true)
			}
		}
	}

	// Handle decision
	switch result.Decision {
	case DecisionAllow:
		return req, nil, nil

	case DecisionVirtualKeyNotFound, DecisionVirtualKeyBlocked, DecisionModelBlocked, DecisionProviderBlocked:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(result.Decision)),
				StatusCode: bifrost.Ptr(403),
				Error: &schemas.ErrorField{
					Message: result.Reason,
				},
			},
		}, nil

	case DecisionRateLimited, DecisionTokenLimited, DecisionRequestLimited:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(result.Decision)),
				StatusCode: bifrost.Ptr(429),
				Error: &schemas.ErrorField{
					Message: result.Reason,
				},
			},
		}, nil

	case DecisionBudgetExceeded:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(result.Decision)),
				StatusCode: bifrost.Ptr(402),
				Error: &schemas.ErrorField{
					Message: result.Reason,
				},
			},
		}, nil

	default:
		// Fallback to deny for unknown decisions
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type: bifrost.Ptr(string(result.Decision)),
				Error: &schemas.ErrorField{
					Message: "Governance decision error",
				},
			},
		}, nil
	}
}

// PostHook processes the response and updates usage tracking (business logic execution)
func (p *GovernancePlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if _, ok := (*ctx).Value(governanceRejectedContextKey).(bool); ok {
		return result, err, nil
	}

	// Extract governance information
	headers := extractHeadersFromContext(*ctx)
	virtualKey := getStringFromContext(*ctx, ContextKey(schemas.BifrostContextKeyVirtualKeyHeader))
	requestID := getStringFromContext(*ctx, schemas.BifrostContextKeyRequestID)

	// Skip if no virtual key
	if virtualKey == "" {
		return result, err, nil
	}

	// Extract request type, provider, and model
	requestType, provider, model := bifrost.GetRequestFields(result, err)

	// Extract cache and batch flags from context
	isCacheRead := false
	isBatch := false
	if val := (*ctx).Value(governanceIsCacheReadContextKey); val != nil {
		if b, ok := val.(bool); ok {
			isCacheRead = b
		}
	}
	if val := (*ctx).Value(governanceIsBatchContextKey); val != nil {
		if b, ok := val.(bool); ok {
			isBatch = b
		}
	}

	// Extract team/customer info for audit trail
	var teamID, customerID *string
	if teamIDValue := headers["x-bf-team"]; teamIDValue != "" {
		teamID = &teamIDValue
	}
	if customerIDValue := headers["x-bf-customer"]; customerIDValue != "" {
		customerID = &customerIDValue
	}

	go p.postHookWorker(result, provider, model, requestType, virtualKey, requestID, teamID, customerID, isCacheRead, isBatch, bifrost.IsFinalChunk(ctx))

	return result, err, nil
}

// Cleanup shuts down all components gracefully
func (p *GovernancePlugin) Cleanup() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if err := p.tracker.Cleanup(); err != nil {
		return err
	}

	return nil
}

func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, provider schemas.ModelProvider, model string, requestType schemas.RequestType, virtualKey, requestID string, teamID, customerID *string, isCacheRead, isBatch bool, isFinalChunk bool) {
	// Determine if request was successful
	success := (result != nil)

	// Streaming detection
	isStreaming := bifrost.IsStreamRequestType(requestType)
	hasUsageData := hasUsageData(result)

	// Extract usage information from response (including speech and transcribe)
	var tokensUsed int64

	if result != nil {
		if result.Usage != nil {
			tokensUsed = int64(result.Usage.TotalTokens)
		} else if result.Speech != nil && result.Speech.Usage != nil {
			tokensUsed = int64(result.Speech.Usage.TotalTokens)
		} else if result.Transcribe != nil && result.Transcribe.Usage != nil && result.Transcribe.Usage.TotalTokens != nil {
			tokensUsed = int64(*result.Transcribe.Usage.TotalTokens)
		}
	}

	cost := 0.0
	if !isStreaming || (isStreaming && isFinalChunk) {
		if p.pricingManager != nil && result != nil {
			cost = p.pricingManager.CalculateCost(result)
		}
	}

	// Create usage update for tracker (business logic)
	usageUpdate := &UsageUpdate{
		VirtualKey:   virtualKey,
		Provider:     provider,
		Model:        model,
		Success:      success,
		TokensUsed:   tokensUsed,
		Cost:         cost,
		RequestID:    requestID,
		TeamID:       teamID,
		CustomerID:   customerID,
		IsStreaming:  isStreaming,
		IsFinalChunk: isFinalChunk,
		HasUsageData: hasUsageData,
	}

	// Queue usage update asynchronously using tracker
	p.tracker.UpdateUsage(p.ctx, usageUpdate)
}

// GetGovernanceStore returns the governance store
func (p *GovernancePlugin) GetGovernanceStore() *GovernanceStore {
	return p.store
}
