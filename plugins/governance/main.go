// Package governance provides comprehensive governance plugin for Bifrost
package governance

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

const (
	governanceRejectedContextKey    schemas.BifrostContextKey = "bf-governance-rejected"
	governanceIsCacheReadContextKey schemas.BifrostContextKey = "bf-governance-is-cache-read"
	governanceIsBatchContextKey     schemas.BifrostContextKey = "bf-governance-is-batch"
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
	wg         sync.WaitGroup // Track active goroutines

	// Core components with clear separation of concerns
	store    *GovernanceStore // Pure data access layer
	resolver *BudgetResolver  // Pure decision engine for hierarchical governance
	tracker  *UsageTracker    // Business logic owner (updates, resets, persistence)

	// Dependencies
	configStore  configstore.ConfigStore
	modelCatalog *modelcatalog.ModelCatalog
	logger       schemas.Logger

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
//   - If `modelCatalog` is nil, cost calculation is skipped.
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
//   - modelCatalog: optional model catalog to compute request cost.
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
	modelCatalog *modelcatalog.ModelCatalog,
	inMemoryStore InMemoryStore,
) (*GovernancePlugin, error) {
	if store == nil {
		logger.Warn("governance plugin requires config store to persist data, running in memory only mode")
	}
	if modelCatalog == nil {
		logger.Warn("governance plugin requires model catalog to calculate cost, all cost calculations will be skipped.")
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
		ctx:           ctx,
		cancelFunc:    cancelFunc,
		store:         governanceStore,
		resolver:      resolver,
		tracker:       tracker,
		configStore:   store,
		modelCatalog:  modelCatalog,
		logger:        logger,
		isVkMandatory: isVkMandatory,
		inMemoryStore: inMemoryStore,
	}
	return plugin, nil
}

// GetName returns the name of the plugin
func (p *GovernancePlugin) GetName() string {
	return PluginName
}

// TransportInterceptor intercepts requests before they are processed (governance decision point)
func (p *GovernancePlugin) TransportInterceptor(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	var virtualKeyValue string
	var err error

	for header, value := range headers {
		if strings.ToLower(string(header)) == string(schemas.BifrostContextKeyVirtualKey) {
			virtualKeyValue = string(value)
			break
		}
	}
	if virtualKeyValue == "" {
		return headers, body, nil
	}

	virtualKey, ok := p.store.GetVirtualKey(virtualKeyValue)
	if !ok || virtualKey == nil || !virtualKey.IsActive {
		return headers, body, nil
	}

	body, err = p.loadBalanceProvider(body, virtualKey)
	if err != nil {
		return headers, body, err
	}

	headers, err = p.addMCPIncludeTools(headers, virtualKey)
	if err != nil {
		return headers, body, err
	}

	return headers, body, nil
}

func (p *GovernancePlugin) loadBalanceProvider(body map[string]any, virtualKey *configstoreTables.TableVirtualKey) (map[string]any, error) {
	// Check if the request has a model field
	modelValue, hasModel := body["model"]
	if !hasModel {
		return body, nil
	}
	modelStr, ok := modelValue.(string)
	if !ok || modelStr == "" {
		return body, nil
	}

	// Check if model already has provider prefix (contains "/")
	if strings.Contains(modelStr, "/") {
		provider, _ := schemas.ParseModelString(modelStr, "")
		// Checking valid provider when store is available; if store is nil,
		// assume the prefixed model should be left unchanged.
		if p.inMemoryStore != nil {
			if _, ok := p.inMemoryStore.GetConfiguredProviders()[provider]; ok {
				return body, nil
			}
		} else {
			return body, nil
		}
	}

	// Get provider configs for this virtual key
	providerConfigs := virtualKey.ProviderConfigs
	if len(providerConfigs) == 0 {
		// No provider configs, continue without modification
		return body, nil
	}
	allowedProviderConfigs := make([]configstoreTables.TableVirtualKeyProviderConfig, 0)
	for _, config := range providerConfigs {
		if len(config.AllowedModels) == 0 || slices.Contains(config.AllowedModels, modelStr) {
			// Check if the provider's budget or rate limits are violated using resolver helper methods
			if p.resolver.isProviderBudgetViolated(config) || p.resolver.isProviderRateLimitViolated(config) {
				// Provider config violated budget or rate limits, skip this provider
				continue
			}

			allowedProviderConfigs = append(allowedProviderConfigs, config)
		}
	}
	if len(allowedProviderConfigs) == 0 {
		// No allowed provider configs, continue without modification
		return body, nil
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

	return body, nil
}

func (p *GovernancePlugin) addMCPIncludeTools(headers map[string]string, virtualKey *configstoreTables.TableVirtualKey) (map[string]string, error) {
	if len(virtualKey.MCPConfigs) > 0 {
		if headers == nil {
			headers = make(map[string]string)
		}
		executeOnlyTools := make([]string, 0)
		for _, vkMcpConfig := range virtualKey.MCPConfigs {
			if len(vkMcpConfig.ToolsToExecute) == 0 {
				// No tools specified in virtual key config - skip this client entirely
				continue
			}

			// Handle wildcard in virtual key config - allow all tools from this client
			if slices.Contains(vkMcpConfig.ToolsToExecute, "*") {
				// Virtual key uses wildcard - use client-specific wildcard
				executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s/*", vkMcpConfig.MCPClient.Name))
				continue
			}

			for _, tool := range vkMcpConfig.ToolsToExecute {
				if tool != "" {
					// Add the tool - client config filtering will be handled by mcp.go
					executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s/%s", vkMcpConfig.MCPClient.Name, tool))
				}
			}
		}

		// Set even when empty to exclude tools when no tools are present in the virtual key config
		headers["x-bf-mcp-include-tools"] = strings.Join(executeOnlyTools, ",")
	}

	return headers, nil
}

// PreHook intercepts requests before they are processed (governance decision point)
func (p *GovernancePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Extract governance headers and virtual key using utility functions
	headers := extractHeadersFromContext(*ctx)
	virtualKey := getStringFromContext(*ctx, schemas.BifrostContextKeyVirtualKey)
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

	provider, model, _ := req.GetRequestFields()

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
	virtualKey := getStringFromContext(*ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := getStringFromContext(*ctx, schemas.BifrostContextKeyRequestID)

	// Skip if no virtual key
	if virtualKey == "" {
		return result, err, nil
	}

	// Extract request type, provider, and model
	requestType, provider, model := bifrost.GetResponseFields(result, err)

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

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.postHookWorker(result, provider, model, requestType, virtualKey, requestID, headers, isCacheRead, isBatch, bifrost.IsFinalChunk(ctx))
	}()

	return result, err, nil
}

// Cleanup shuts down all components gracefully
func (p *GovernancePlugin) Cleanup() error {
	p.wg.Wait() // Wait for all background workers to complete
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if err := p.tracker.Cleanup(); err != nil {
		return err
	}

	return nil
}

func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, provider schemas.ModelProvider, model string, requestType schemas.RequestType, virtualKey, requestID string, headers map[string]string, isCacheRead, isBatch bool, isFinalChunk bool) {
	// Determine if request was successful
	success := (result != nil)

	// Extract team/customer info for audit trail
	var teamID, customerID *string
	if teamIDValue := headers["x-bf-team"]; teamIDValue != "" {
		teamID = &teamIDValue
	}
	if customerIDValue := headers["x-bf-customer"]; customerIDValue != "" {
		customerID = &customerIDValue
	}

	// Streaming detection
	isStreaming := bifrost.IsStreamRequestType(requestType)

	if !isStreaming || (isStreaming && isFinalChunk) {
		var cost float64
		if p.modelCatalog != nil && result != nil {
			cost = p.modelCatalog.CalculateCostWithCacheDebug(result)
		}
		tokensUsed := 0
		if result != nil {
			switch {
			case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
				tokensUsed = result.TextCompletionResponse.Usage.TotalTokens
			case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
				tokensUsed = result.ChatResponse.Usage.TotalTokens
			case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
				tokensUsed = result.ResponsesResponse.Usage.TotalTokens
			case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
				tokensUsed = result.ResponsesStreamResponse.Response.Usage.TotalTokens
			case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
				tokensUsed = result.EmbeddingResponse.Usage.TotalTokens
			case result.SpeechResponse != nil && result.SpeechResponse.Usage != nil:
				tokensUsed = result.SpeechResponse.Usage.TotalTokens
			case result.SpeechStreamResponse != nil && result.SpeechStreamResponse.Usage != nil:
				tokensUsed = result.SpeechStreamResponse.Usage.TotalTokens
			case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil && result.TranscriptionResponse.Usage.TotalTokens != nil:
				tokensUsed = *result.TranscriptionResponse.Usage.TotalTokens
			case result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.Usage != nil && result.TranscriptionStreamResponse.Usage.TotalTokens != nil:
				tokensUsed = *result.TranscriptionStreamResponse.Usage.TotalTokens
			}
		}
		// Create usage update for tracker (business logic)
		usageUpdate := &UsageUpdate{
			VirtualKey:   virtualKey,
			Provider:     provider,
			Model:        model,
			Success:      success,
			TokensUsed:   int64(tokensUsed),
			Cost:         cost,
			RequestID:    requestID,
			TeamID:       teamID,
			CustomerID:   customerID,
			IsStreaming:  isStreaming,
			IsFinalChunk: isFinalChunk,
			HasUsageData: tokensUsed > 0,
		}

		// Queue usage update asynchronously using tracker
		p.tracker.UpdateUsage(p.ctx, usageUpdate)
	}
}

// GetGovernanceStore returns the governance store
func (p *GovernancePlugin) GetGovernanceStore() *GovernanceStore {
	return p.store
}
