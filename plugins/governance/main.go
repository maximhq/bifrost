// Package governance provides comprehensive governance plugin for Bifrost
package governance

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

const (
	governanceRejectedContextKey    schemas.BifrostContextKey = "bf-governance-rejected"
	governanceIsCacheReadContextKey schemas.BifrostContextKey = "bf-governance-is-cache-read"
	governanceIsBatchContextKey     schemas.BifrostContextKey = "bf-governance-is-batch"

	VirtualKeyPrefix = "sk-bf-"
)

// Config is the configuration for the governance plugin
type Config struct {
	IsVkMandatory *bool `json:"is_vk_mandatory"`
}

type InMemoryStore interface {
	GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig
}

type BaseGovernancePlugin interface {
	GetName() string
	HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
	HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error
	PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)
	PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error)
	PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error)
	Cleanup() error
	GetGovernanceStore() GovernanceStore
}

// GovernancePlugin implements the main governance plugin with hierarchical budget system
type GovernancePlugin struct {
	ctx         context.Context
	cancelFunc  context.CancelFunc
	wg          sync.WaitGroup // Track active goroutines
	cleanupOnce sync.Once      // Ensure cleanup happens only once

	// Core components with clear separation of concerns
	store    GovernanceStore // Pure data access layer
	resolver *BudgetResolver // Pure decision engine for hierarchical governance
	tracker  *UsageTracker   // Business logic owner (updates, resets, persistence)
	engine   *RoutingEngine  // Routing engine for dynamic routing

	// Dependencies
	configStore  configstore.ConfigStore
	modelCatalog *modelcatalog.ModelCatalog
	mcpCatalog   *mcpcatalog.MCPCatalog
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
//   - If `configStore` is nil, the plugin will use an in-memory LocalGovernanceStore
//     (no persistence). Init constructs a LocalGovernanceStore internally when
//     configStore is nil.
//   - If `modelCatalog` is nil, cost calculation is skipped.
//   - `config.IsVkMandatory` controls whether `x-bf-vk` is required in PreLLMHook.
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
//   - configStore: configuration store used for persistence; may be nil.
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
//   - May perform startup resets via the usage tracker when `configStore` is non-nil.
//
// Alternative entry point:
//   - Use InitFromStore to inject a custom GovernanceStore implementation instead
//     of constructing a LocalGovernanceStore internally.
func Init(
	ctx context.Context,
	config *Config,
	logger schemas.Logger,
	configStore configstore.ConfigStore,
	governanceConfig *configstore.GovernanceConfig,
	modelCatalog *modelcatalog.ModelCatalog,
	mcpCatalog *mcpcatalog.MCPCatalog,
	inMemoryStore InMemoryStore,
) (*GovernancePlugin, error) {
	if configStore == nil {
		logger.Warn("governance plugin requires config store to persist data, running in memory only mode")
	}
	if modelCatalog == nil {
		logger.Warn("governance plugin requires model catalog to calculate cost, all LLM cost calculations will be skipped.")
	}
	if mcpCatalog == nil {
		logger.Warn("governance plugin requires MCP catalog to calculate cost, all MCP cost calculations will be skipped.")
	}

	// Handle nil config - use safe default for IsVkMandatory
	var isVkMandatory *bool
	if config != nil {
		isVkMandatory = config.IsVkMandatory
	}

	governanceStore, err := NewLocalGovernanceStore(ctx, logger, configStore, governanceConfig, modelCatalog)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize governance store: %w", err)
	}
	// Initialize components in dependency order with fixed, optimal settings
	// Resolver (pure decision engine for hierarchical governance, depends only on store)
	resolver := NewBudgetResolver(governanceStore, modelCatalog, logger)

	// 3. Tracker (business logic owner, depends on store and resolver)
	tracker := NewUsageTracker(ctx, governanceStore, resolver, configStore, logger)

	// 4. Perform startup reset check for any expired limits from downtime
	// Use distributed lock to prevent race condition when multiple instances boot simultaneously
	if configStore != nil {
		lockManager := configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second))
		lock, err := lockManager.NewLock("governance_startup_reset")
		if err != nil {
			logger.Warn("failed to create governance startup reset lock: %v", err)
		} else {
			// Acquire the lock
			lockAcquired := true
			if err := lock.LockWithRetry(ctx, 10); err != nil {
				logger.Warn("failed to acquire governance startup reset lock, skipping startup reset: %v", err)
				lockAcquired = false
			}
			// Only run startup resets if we successfully acquired the lock
			if lockAcquired {
				defer func() {
					if err := lock.Unlock(ctx); err != nil && !errors.Is(err, configstore.ErrLockNotHeld) {
						logger.Warn("failed to release governance startup reset lock: %v", err)
					}
				}()
				if err := tracker.PerformStartupResets(ctx); err != nil {
					logger.Warn("startup reset failed: %v", err)
					// Continue initialization even if startup reset fails (non-critical)
				}
			}
		}
	}

	// 5. Routing engine (dynamically routing requests based on routing rules)
	engine, err := NewRoutingEngine(governanceStore, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize routing engine: %w", err)
	}

	ctx, cancelFunc := context.WithCancel(ctx)
	plugin := &GovernancePlugin{
		ctx:           ctx,
		cancelFunc:    cancelFunc,
		store:         governanceStore,
		resolver:      resolver,
		tracker:       tracker,
		engine:        engine,
		configStore:   configStore,
		modelCatalog:  modelCatalog,
		mcpCatalog:    mcpCatalog,
		logger:        logger,
		isVkMandatory: isVkMandatory,
		inMemoryStore: inMemoryStore,
	}
	return plugin, nil
}

// InitFromStore initializes and returns a governance plugin instance with a custom store.
//
// This constructor allows providing a custom GovernanceStore implementation instead of
// creating a new LocalGovernanceStore. Use this when you need to:
//   - Inject a custom store implementation for testing
//   - Use a pre-configured store instance
//   - Integrate with non-standard storage backends
//
// Parameters are the same as Init, except governanceConfig is replaced by governanceStore.
// The governanceStore must not be nil, or an error is returned.
//
// See Init documentation for details on other parameters and behavior.
func InitFromStore(
	ctx context.Context,
	config *Config,
	logger schemas.Logger,
	governanceStore GovernanceStore,
	configStore configstore.ConfigStore,
	modelCatalog *modelcatalog.ModelCatalog,
	mcpCatalog *mcpcatalog.MCPCatalog,
	inMemoryStore InMemoryStore,
) (*GovernancePlugin, error) {
	if configStore == nil {
		logger.Warn("governance plugin requires config store to persist data, running in memory only mode")
	}
	if modelCatalog == nil {
		logger.Warn("governance plugin requires model catalog to calculate cost, all cost calculations will be skipped.")
	}
	if mcpCatalog == nil {
		logger.Warn("governance plugin requires MCP catalog to calculate cost, all MCP cost calculations will be skipped.")
	}
	if governanceStore == nil {
		return nil, fmt.Errorf("governance store is nil")
	}
	// Handle nil config - use safe default for IsVkMandatory
	var isVkMandatory *bool
	if config != nil {
		isVkMandatory = config.IsVkMandatory
	}
	resolver := NewBudgetResolver(governanceStore, modelCatalog, logger)
	tracker := NewUsageTracker(ctx, governanceStore, resolver, configStore, logger)
	engine, err := NewRoutingEngine(governanceStore, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize routing engine: %w", err)
	}
	// Perform startup reset check for any expired limits from downtime
	// Use distributed lock to prevent race condition when multiple instances boot simultaneously
	if configStore != nil {
		lockManager := configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second))
		lock, err := lockManager.NewLock("governance_startup_reset")
		if err != nil {
			logger.Warn("failed to create governance startup reset lock: %v", err)
		} else if err := lock.Lock(ctx); err != nil {
			logger.Warn("failed to acquire governance startup reset lock, skipping startup reset: %v", err)
		} else {
			defer lock.Unlock(ctx)
			if err := tracker.PerformStartupResets(ctx); err != nil {
				logger.Warn("startup reset failed: %v", err)
				// Continue initialization even if startup reset fails (non-critical)
			}
		}
	}
	ctx, cancelFunc := context.WithCancel(ctx)
	plugin := &GovernancePlugin{
		ctx:           ctx,
		cancelFunc:    cancelFunc,
		store:         governanceStore,
		resolver:      resolver,
		tracker:       tracker,
		engine:        engine,
		configStore:   configStore,
		modelCatalog:  modelCatalog,
		mcpCatalog:    mcpCatalog,
		logger:        logger,
		inMemoryStore: inMemoryStore,
		isVkMandatory: isVkMandatory,
	}
	return plugin, nil
}

// GetName returns the name of the plugin
func (p *GovernancePlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook intercepts requests before they are processed (governance decision point)
// It modifies the request in-place and returns nil to continue, or an HTTPResponse to short-circuit.
// Optimized to skip unnecessary operations: only unmarshals/marshals when needed
func (p *GovernancePlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	virtualKeyValue := parseVirtualKeyFromHTTPRequest(req)
	hasRoutingRules := p.store.HasRoutingRules(ctx)

	// If no virtual key and no routing rules configured, skip all processing
	if virtualKeyValue == nil && !hasRoutingRules {
		return nil, nil
	}

	// If no body, nothing to process
	if len(req.Body) == 0 {
		return nil, nil
	}

	// Only unmarshal if we have VK or routing rules
	var payload map[string]any
	var virtualKey *configstoreTables.TableVirtualKey
	var ok bool
	var needsMarshal bool

	err := sonic.Unmarshal(req.Body, &payload)
	if err != nil {
		p.logger.Error("failed to unmarshal request body: %v", err)
		return nil, nil
	}

	// Process virtual key if provided
	if virtualKeyValue != nil {
		virtualKey, ok = p.store.GetVirtualKey(*virtualKeyValue)
		if !ok || virtualKey == nil || !virtualKey.IsActive {
			return nil, nil
		}
	}

	//1. Apply routing rules only if we have rules or matched decision
	var routingDecision *RoutingDecision
	if hasRoutingRules {
		var err error
		payload, routingDecision, err = p.applyRoutingRules(ctx, req, payload, virtualKey)
		if err != nil {
			return nil, err
		}
		// Mark for marshal if a routing rule matched
		if routingDecision != nil {
			needsMarshal = true
		}
	}

	// Process virtual key if provided
	if virtualKey != nil {
		//2. Load balance provider
		payload, err = p.loadBalanceProvider(ctx, req, payload, virtualKey)
		if err != nil {
			return nil, err
		}
		//3. Add MCP tools
		headers, err := p.addMCPIncludeTools(nil, virtualKey)
		if err != nil {
			p.logger.Error("failed to add MCP include tools: %v", err)
			return nil, nil
		}
		for header, value := range headers {
			req.Headers[header] = value
		}
		needsMarshal = true
	}

	// Only marshal if something changed (VK processing or routing decision matched)
	if needsMarshal {
		body, err := sonic.Marshal(payload)
		if err != nil {
			p.logger.Error("failed to marshal request body: %v", err)
			return nil, nil
		}
		req.Body = body
	}

	return nil, nil
}

// HTTPTransportPostHook intercepts requests after they are processed (governance decision point)
// It modifies the response in-place and returns nil to continue
func (p *GovernancePlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *GovernancePlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// loadBalanceProvider loads balances the provider for the request
// Parameters:
//   - req: The HTTP request
//   - body: The request body
//   - virtualKey: The virtual key configuration
//
// Returns:
//   - map[string]any: The updated request body
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) loadBalanceProvider(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, body map[string]any, virtualKey *configstoreTables.TableVirtualKey) (map[string]any, error) {
	// Check if the request has a model field
	modelValue, hasModel := body["model"]
	if !hasModel {
		// For genai integration, model is present in URL path instead of the request body
		if strings.Contains(req.Path, "/genai") {
			modelValue = req.CaseInsensitivePathParamLookup("model")
		} else {
			return body, nil
		}
	}
	modelStr, ok := modelValue.(string)
	if !ok || modelStr == "" {
		return body, nil
	}
	var genaiRequestSuffix string
	// Remove Google GenAI API endpoint suffixes if present
	if strings.Contains(req.Path, "/genai") {
		for _, sfx := range gemini.GeminiRequestSuffixPaths {
			if before, ok := strings.CutSuffix(modelStr, sfx); ok {
				modelStr = before
				genaiRequestSuffix = sfx
				break
			}
		}
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

	var configuredProviders []string
	for _, pc := range providerConfigs {
		configuredProviders = append(configuredProviders, pc.Provider)
	}
	p.logger.Debug("[Governance] Virtual key has %d provider configs: %v", len(providerConfigs), configuredProviders)

	allowedProviderConfigs := make([]configstoreTables.TableVirtualKeyProviderConfig, 0)
	for _, config := range providerConfigs {
		// Delegate model allowance check to model catalog
		// This handles all cross-provider logic (OpenRouter, Vertex, Groq, Bedrock)
		// and provider-prefixed allowed_models entries
		isProviderAllowed := false
		if p.modelCatalog != nil {
			isProviderAllowed = p.modelCatalog.IsModelAllowedForProvider(schemas.ModelProvider(config.Provider), modelStr, config.AllowedModels)
		} else {
			// Fallback when model catalog is not available: simple string matching
			if len(config.AllowedModels) == 0 {
				// No restrictions, allow all models
				isProviderAllowed = true
			} else {
				isProviderAllowed = slices.Contains(config.AllowedModels, modelStr)
			}
		}

		if isProviderAllowed {
			// Check if the provider's budget or rate limits are violated using resolver helper methods
			if p.resolver.isProviderBudgetViolated(ctx, virtualKey, config) || p.resolver.isProviderRateLimitViolated(ctx, virtualKey, config) {
				// Provider config violated budget or rate limits, skip this provider
				continue
			}
			allowedProviderConfigs = append(allowedProviderConfigs, config)
		}
	}

	var allowedProviders []string
	for _, pc := range allowedProviderConfigs {
		allowedProviders = append(allowedProviders, pc.Provider)
	}
	p.logger.Debug("[Governance] Allowed providers after filtering: %v", allowedProviders)

	if len(allowedProviderConfigs) == 0 {
		// No allowed provider configs, continue without modification
		return body, nil
	}
	// Weighted random selection from allowed providers for the main model
	totalWeight := 0.0
	for _, config := range allowedProviderConfigs {
		totalWeight += getWeight(config.Weight)
	}
	// Generate random number between 0 and totalWeight
	randomValue := rand.Float64() * totalWeight
	// Select provider based on weighted random selection
	var selectedProvider schemas.ModelProvider
	currentWeight := 0.0
	for _, config := range allowedProviderConfigs {
		currentWeight += getWeight(config.Weight)
		if randomValue <= currentWeight {
			selectedProvider = schemas.ModelProvider(config.Provider)
			break
		}
	}
	// Fallback: if no provider was selected (shouldn't happen but guard against FP issues)
	if selectedProvider == "" && len(allowedProviderConfigs) > 0 {
		selectedProvider = schemas.ModelProvider(allowedProviderConfigs[0].Provider)
	}

	p.logger.Debug("[Governance] Selected provider: %s", selectedProvider)

	// For genai integration, model is present in URL path instead of the request body
	if strings.Contains(req.Path, "/genai") {
		newModelWithRequestSuffix := string(selectedProvider) + "/" + modelStr + genaiRequestSuffix
		ctx.SetValue("model", newModelWithRequestSuffix)
	} else {
		var err error
		refinedModel := modelStr
		// Refine the model for the selected provider
		if p.modelCatalog != nil {
			refinedModel, err = p.modelCatalog.RefineModelForProvider(selectedProvider, modelStr)
			if err != nil {
				return body, err
			}
		}
		// Update the model field in the request body
		body["model"] = string(selectedProvider) + "/" + refinedModel
	}
	ctx.SetValue(schemas.BifrostContextKeyRoutingEngineUsed, "governance")

	// Check if fallbacks field is already present
	_, hasFallbacks := body["fallbacks"]
	if !hasFallbacks && len(allowedProviderConfigs) > 1 {
		// Sort allowed provider configs by weight (descending)
		sort.Slice(allowedProviderConfigs, func(i, j int) bool {
			return getWeight(allowedProviderConfigs[i].Weight) > getWeight(allowedProviderConfigs[j].Weight)
		})

		// Filter out the selected provider and create fallbacks array
		fallbacks := make([]string, 0, len(allowedProviderConfigs)-1)
		for _, config := range allowedProviderConfigs {
			if config.Provider != string(selectedProvider) {
				var err error
				refinedModel := modelStr
				if p.modelCatalog != nil {
					refinedModel, err = p.modelCatalog.RefineModelForProvider(schemas.ModelProvider(config.Provider), modelStr)
					if err != nil {
						// Skip fallback if model refinement fails
						p.logger.Warn("failed to refine model for fallback, skipping fallback in governance plugin: %v", err)
						continue
					}
				}
				fallbacks = append(fallbacks, string(schemas.ModelProvider(config.Provider))+"/"+refinedModel)
			}
		}

		// Add fallbacks to request body
		body["fallbacks"] = fallbacks
	}

	return body, nil
}

// applyRoutingRules evaluates routing rules and returns both the modified payload AND the routing decision
// This allows the caller to determine if marshaling is necessary (only if decision != nil or payload changed)
// Parameters:
//   - ctx: Bifrost context
//   - req: HTTP request
//   - body: Request body (may be modified if routing rule matches)
//   - virtualKey: Virtual key configuration (may be nil)
//
// Returns:
//   - map[string]any: The potentially modified request body
//   - *RoutingDecision: The matched routing decision (nil if no rule matched)
//   - error: Any error that occurred during evaluation
func (p *GovernancePlugin) applyRoutingRules(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, body map[string]any, virtualKey *configstoreTables.TableVirtualKey) (map[string]any, *RoutingDecision, error) {
	// Check if the request has a model field
	modelValue, hasModel := body["model"]
	if !hasModel {
		// For genai integration, model is present in URL path
		if strings.Contains(req.Path, "/genai") {
			modelValue = req.CaseInsensitivePathParamLookup("model")
		} else {
			return body, nil, nil
		}
	}

	modelStr, ok := modelValue.(string)
	if !ok || modelStr == "" {
		return body, nil, nil
	}

	var genaiRequestSuffix string
	if strings.Contains(req.Path, "/genai") {
		for _, sfx := range gemini.GeminiRequestSuffixPaths {
			if before, ok := strings.CutSuffix(modelStr, sfx); ok {
				modelStr = before
				genaiRequestSuffix = sfx
				break
			}
		}
	}

	// Parse provider and model from modelStr (format: "provider/model" or just "model")
	provider, model := schemas.ParseModelString(modelStr, "")

	// Extract normalized request type from context (set by HTTP middleware)
	requestType := ""
	if val := ctx.Value(schemas.BifrostContextKeyHTTPRequestType); val != nil {
		if requestTypeEnum, ok := val.(schemas.RequestType); ok {
			requestType = string(requestTypeEnum)
		} else if requestTypeStr, ok := val.(string); ok {
			requestType = requestTypeStr
		}
	}

	// Build routing context
	routingCtx := &RoutingContext{
		VirtualKey:               virtualKey,
		Provider:                 provider,
		Model:                    model,
		RequestType:              requestType,
		Headers:                  req.Headers,
		QueryParams:              req.Query,
		BudgetAndRateLimitStatus: p.store.GetBudgetAndRateLimitStatus(ctx, model, provider, virtualKey, nil, nil, nil),
	}

	p.logger.Debug("[HTTPTransport] Built routing context: provider=%s, model=%s, requestType=%s, vk=%v, headerCount=%d, paramCount=%d",
		provider, model, requestType, virtualKey != nil, len(req.Headers), len(req.Query))

	// Evaluate routing rules
	decision, err := p.engine.EvaluateRoutingRules(ctx, routingCtx)
	if err != nil {
		p.logger.Error("failed to evaluate routing rules: %v", err)
		return body, nil, nil
	}

	// If a routing rule matched, apply the decision
	if decision != nil {
		p.logger.Debug("[Governance] Routing rule matched: %s", decision.MatchedRuleName)

		// Update model in request body
		if strings.Contains(req.Path, "/genai") {
			// For genai, model is in URL path
			newModel := decision.Provider + "/" + decision.Model + genaiRequestSuffix
			ctx.SetValue("model", newModel)
		} else {
			// For regular requests, update in body
			body["model"] = decision.Provider + "/" + decision.Model
		}
		ctx.SetValue(schemas.BifrostContextKeyRoutingEngineUsed, "routing-rule")

		// Add fallbacks if present
		if len(decision.Fallbacks) > 0 {
			body["fallbacks"] = decision.Fallbacks
		}

		p.logger.Debug("[Governance] Applied routing decision: provider=%s, model=%s, fallbacks=%v", decision.Provider, decision.Model, decision.Fallbacks)
	}

	return body, decision, nil
}

// addMCPIncludeTools adds the x-bf-mcp-include-tools header to the request headers
// Parameters:
//   - headers: The request headers
//   - virtualKey: The virtual key configuration
//
// Returns:
//   - map[string]string: The updated request headers
//   - error: Any error that occurred during processing
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
				executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-*", vkMcpConfig.MCPClient.Name))
				continue
			}

			for _, tool := range vkMcpConfig.ToolsToExecute {
				if tool != "" {
					// Add the tool - client config filtering will be handled by mcp.go
					executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-%s", vkMcpConfig.MCPClient.Name, tool))
				}
			}
		}

		// Set even when empty to exclude tools when no tools are present in the virtual key config
		headers["x-bf-mcp-include-tools"] = strings.Join(executeOnlyTools, ",")
	}

	return headers, nil
}

// evaluateGovernanceRequest is a common function that handles virtual key validation
// and governance evaluation logic. It returns the evaluation result and a BifrostError
// if the request should be rejected, or nil if allowed.
//
// Parameters:
//   - ctx: The Bifrost context
//   - evaluationRequest: The evaluation request with VirtualKey, Provider, Model, and RequestID
//
// Returns:
//   - *EvaluationResult: The governance evaluation result
//   - *schemas.BifrostError: The error to return if request is not allowed, nil if allowed
func (p *GovernancePlugin) evaluateGovernanceRequest(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, requestType schemas.RequestType) (*EvaluationResult, *schemas.BifrostError) {
	// Check if virtual key is mandatory
	if evaluationRequest.VirtualKey == "" && p.isVkMandatory != nil && *p.isVkMandatory {
		return nil, &schemas.BifrostError{
			Type:       bifrost.Ptr("virtual_key_required"),
			StatusCode: bifrost.Ptr(401),
			Error: &schemas.ErrorField{
				Message: "virtual key is missing in headers and is mandatory.",
			},
		}
	}

	// First evaluate model and provider checks (applies even when virtual keys are disabled or not present)
	result := p.resolver.EvaluateModelAndProviderRequest(ctx, evaluationRequest.Provider, evaluationRequest.Model)

	// If model/provider checks passed and virtual key exists, evaluate virtual key checks
	// This will overwrite the result with virtual key-specific decision
	if result.Decision == DecisionAllow && evaluationRequest.VirtualKey != "" {
		result = p.resolver.EvaluateVirtualKeyRequest(ctx, evaluationRequest.VirtualKey, evaluationRequest.Provider, evaluationRequest.Model, requestType)
	}
	// If model/provider checks failed, skip virtual key evaluation and proceed to final decision handling

	// Mark request as rejected in context if not allowed
	if result.Decision != DecisionAllow {
		if ctx != nil {
			if _, ok := ctx.Value(governanceRejectedContextKey).(bool); !ok {
				ctx.SetValue(governanceRejectedContextKey, true)
			}
		}
	}

	// Handle decision
	switch result.Decision {
	case DecisionAllow:
		return result, nil

	case DecisionVirtualKeyNotFound, DecisionVirtualKeyBlocked, DecisionModelBlocked, DecisionProviderBlocked:
		return result, &schemas.BifrostError{
			Type:       bifrost.Ptr(string(result.Decision)),
			StatusCode: bifrost.Ptr(403),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	case DecisionRateLimited, DecisionTokenLimited, DecisionRequestLimited:
		return result, &schemas.BifrostError{
			Type:       bifrost.Ptr(string(result.Decision)),
			StatusCode: bifrost.Ptr(429),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	case DecisionBudgetExceeded:
		return result, &schemas.BifrostError{
			Type:       bifrost.Ptr(string(result.Decision)),
			StatusCode: bifrost.Ptr(402),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	default:
		// Fallback to deny for unknown decisions
		return result, &schemas.BifrostError{
			Type: bifrost.Ptr(string(result.Decision)),
			Error: &schemas.ErrorField{
				Message: "Governance decision error",
			},
		}
	}
}

// PreLLMHook intercepts requests before they are processed (governance decision point)
// Parameters:
//   - ctx: The Bifrost context
//   - req: The Bifrost request to be processed
//
// Returns:
//   - *schemas.BifrostRequest: The processed request
//   - *schemas.LLMPluginShortCircuit: The plugin short circuit if the request is not allowed
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	// Extract governance headers and virtual key using utility functions
	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	// Getting provider and mode from the request
	provider, model, _ := req.GetRequestFields()
	// Create request context for evaluation
	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKeyValue,
		Provider:   provider,
		Model:      model,
	}
	// Evaluate governance using common function
	_, bifrostError := p.evaluateGovernanceRequest(ctx, evaluationRequest, req.RequestType)
	// Convert BifrostError to LLMPluginShortCircuit if needed
	if bifrostError != nil {
		return req, &schemas.LLMPluginShortCircuit{
			Error: bifrostError,
		}, nil
	}

	return req, nil, nil
}

// PostLLMHook processes the response and updates usage tracking (business logic execution)
// Parameters:
//   - ctx: The Bifrost context
//   - result: The Bifrost response to be processed
//   - err: The Bifrost error to be processed
//
// Returns:
//   - *schemas.BifrostResponse: The processed response
//   - *schemas.BifrostError: The processed error
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if _, ok := ctx.Value(governanceRejectedContextKey).(bool); ok {
		return result, err, nil
	}

	// Extract governance information
	virtualKey := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)

	// Extract request type, provider, and model
	requestType, provider, model := bifrost.GetResponseFields(result, err)

	// Extract cache and batch flags from context
	isCacheRead := false
	isBatch := false
	if val := ctx.Value(governanceIsCacheReadContextKey); val != nil {
		if b, ok := val.(bool); ok {
			isCacheRead = b
		}
	}
	if val := ctx.Value(governanceIsBatchContextKey); val != nil {
		if b, ok := val.(bool); ok {
			isBatch = b
		}
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	// Always process usage tracking (with or without virtual key)
	// If virtualKey is empty, it will be passed as empty string to postHookWorker
	// The tracker will handle empty virtual keys gracefully by only updating provider-level and model-level usage
	if model != "" {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			// Pass virtualKey (empty string if not present) - tracker handles this case
			p.postHookWorker(result, provider, model, requestType, virtualKey, requestID, isCacheRead, isBatch, isFinalChunk)
		}()
	}

	return result, err, nil
}

// PreMCPHook intercepts MCP tool execution requests before they are processed (governance decision point)
// Parameters:
//   - ctx: The Bifrost context
//   - req: The Bifrost MCP request to be processed
//
// Returns:
//   - *schemas.BifrostMCPRequest: The processed request
//   - *schemas.MCPPluginShortCircuit: The plugin short circuit if the request is not allowed
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	toolName := req.GetToolName()

	// Skip governance for codemode tools
	if bifrost.IsCodemodeTool(toolName) {
		return req, nil, nil
	}

	// Extract governance headers and virtual key using utility functions
	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)

	// Create request context for evaluation (MCP requests don't have provider/model)
	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKeyValue,
	}

	// Evaluate governance using common function
	_, bifrostError := p.evaluateGovernanceRequest(ctx, evaluationRequest, schemas.MCPToolExecutionRequest)

	// Convert BifrostError to MCPPluginShortCircuit if needed
	if bifrostError != nil {
		return req, &schemas.MCPPluginShortCircuit{
			Error: bifrostError,
		}, nil
	}

	return req, nil, nil
}

// PostMCPHook processes the MCP response and updates usage tracking (business logic execution)
// Parameters:
//   - ctx: The Bifrost context
//   - resp: The Bifrost MCP response to be processed
//   - bifrostErr: The Bifrost error to be processed
//
// Returns:
//   - *schemas.BifrostMCPResponse: The processed response
//   - *schemas.BifrostError: The processed error
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if _, ok := ctx.Value(governanceRejectedContextKey).(bool); ok {
		return resp, bifrostErr, nil
	}

	// Extract governance information
	virtualKey := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)

	// Skip if no virtual key
	if virtualKey == "" {
		return resp, bifrostErr, nil
	}

	// Determine if request was successful
	success := (resp != nil && bifrostErr == nil)

	// Skip usage tracking for codemode tools
	if success && resp != nil && bifrost.IsCodemodeTool(resp.ExtraFields.ToolName) {
		return resp, bifrostErr, nil
	}

	// Calculate MCP tool cost from catalog if available
	var toolCost float64
	if success && resp != nil && p.mcpCatalog != nil && resp.ExtraFields.ClientName != "" && resp.ExtraFields.ToolName != "" {
		// Use separate client name and tool name fields
		if pricingEntry, ok := p.mcpCatalog.GetPricingData(resp.ExtraFields.ClientName, resp.ExtraFields.ToolName); ok {
			toolCost = pricingEntry.CostPerExecution
			p.logger.Debug("MCP tool cost for %s.%s: $%.6f", resp.ExtraFields.ClientName, resp.ExtraFields.ToolName, toolCost)
		}
	}

	// Create usage update for tracker (business logic) - MCP requests track request count and tool cost
	usageUpdate := &UsageUpdate{
		VirtualKey:   virtualKey,
		Success:      success,
		Cost:         toolCost,
		RequestID:    requestID,
		IsStreaming:  false,
		IsFinalChunk: true,
		HasUsageData: toolCost > 0, // Has usage data if we have a cost
	}

	// Queue usage update asynchronously using tracker
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.tracker.UpdateUsage(p.ctx, usageUpdate)
	}()

	return resp, bifrostErr, nil
}

// Cleanup shuts down all components gracefully
func (p *GovernancePlugin) Cleanup() error {
	var cleanupErr error
	p.cleanupOnce.Do(func() {
		if p.cancelFunc != nil {
			p.cancelFunc()
		}
		p.wg.Wait() // Wait for all background workers to complete
		if err := p.tracker.Cleanup(); err != nil {
			cleanupErr = err
		}
	})
	return cleanupErr
}

// postHookWorker is a worker function that processes the response and updates usage tracking
// It is used to avoid blocking the main thread when updating usage tracking
// Handles both cases: with virtual key and without virtual key (empty string)
// When virtualKey is empty, the tracker will only update provider-level and model-level usage
// Parameters:
//   - result: The Bifrost response to be processed
//   - provider: The provider of the request
//   - model: The model of the request
//   - requestType: The type of the request
//   - virtualKey: The virtual key of the request (empty string if not present)
//   - requestID: The request ID
//   - isCacheRead: Whether the request is a cache read
//   - isBatch: Whether the request is a batch request
//   - isFinalChunk: Whether the request is the final chunk
func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, provider schemas.ModelProvider, model string, requestType schemas.RequestType, virtualKey, requestID string, _, _, isFinalChunk bool) {
	// Determine if request was successful
	success := (result != nil)

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
			IsStreaming:  isStreaming,
			IsFinalChunk: isFinalChunk,
			HasUsageData: tokensUsed > 0,
		}

		// Queue usage update asynchronously using tracker
		// UpdateUsage handles empty virtual keys gracefully by only updating provider-level and model-level usage
		p.tracker.UpdateUsage(p.ctx, usageUpdate)
	}
}

// GetGovernanceStore returns the governance store
func (p *GovernancePlugin) GetGovernanceStore() GovernanceStore {
	return p.store
}

// GenerateVirtualKey is a helper function
func GenerateVirtualKey() string {
	return VirtualKeyPrefix + uuid.NewString()
}
