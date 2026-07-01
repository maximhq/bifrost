// Package governance provides comprehensive governance plugin for Bifrost
package governance

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

const (
	governanceRejectedContextKey schemas.BifrostContextKey = "bf-governance-rejected"

	VirtualKeyPrefix = "sk-bf-"

	noComplexitySignalLog = "Complexity analysis skipped: no configured complexity signal matched the latest user message; continuing with existing routing path"
)

// Config is the configuration for the governance plugin
type Config struct {
	IsVkMandatory         *bool     `json:"is_vk_mandatory"`
	RequiredHeaders       *[]string `json:"required_headers"` // Pointer to live config slice; changes are reflected immediately without restart
	IsEnterprise          bool      `json:"is_enterprise"`
	DisableAutoToolInject *bool     `json:"disable_auto_tool_inject"`
	RoutingChainMaxDepth  *int      `json:"routing_chain_max_depth"` // Pointer to live config value; changes are reflected immediately without restart
}

type InMemoryStore interface {
	GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig
	GetMCPClientsAllowingAllVirtualKeys() map[string]string // clientID → clientName
}

type BaseGovernancePlugin interface {
	GetName() string
	EvaluateGovernanceRequest(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, requestType schemas.RequestType) (*EvaluationResult, *schemas.BifrostError)
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

	cfgMutex sync.RWMutex

	isVkMandatory         *bool
	requiredHeaders       *[]string // pointer to live config slice; lowercased at check time
	isEnterprise          bool
	disableAutoToolInject *bool

	complexityAnalyzer atomic.Pointer[complexity.ComplexityAnalyzer]
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

	// Handle nil config - use safe defaults
	var isVkMandatory *bool
	var requiredHeaders *[]string
	var disableAutoToolInject *bool
	var routingChainMaxDepth *int
	if config != nil {
		isVkMandatory = config.IsVkMandatory
		requiredHeaders = config.RequiredHeaders
		disableAutoToolInject = config.DisableAutoToolInject
		routingChainMaxDepth = config.RoutingChainMaxDepth
	}
	if routingChainMaxDepth == nil {
		defaultDepth := DefaultRoutingChainMaxDepth
		routingChainMaxDepth = &defaultDepth
	}

	newStoreStart := time.Now()
	governanceStore, err := NewLocalGovernanceStore(ctx, logger, configStore, governanceConfig, modelCatalog)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize governance store: %w", err)
	}
	logger.Info("[startup-timing] NewLocalGovernanceStore took %v", time.Since(newStoreStart))
	// Initialize components in dependency order with fixed, optimal settings
	// Resolver (pure decision engine for hierarchical governance, depends only on store)
	resolver := NewBudgetResolver(governanceStore, modelCatalog, logger, inMemoryStore)

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
			lockWaitStart := time.Now()
			if err := lock.LockWithRetry(ctx, 10); err != nil {
				logger.Warn("failed to acquire governance startup reset lock, skipping startup reset: %v", err)
				lockAcquired = false
			}
			logger.Info("[startup-timing] governance_startup_reset lock acquisition took %v (acquired=%t)", time.Since(lockWaitStart), lockAcquired)
			// Only run startup resets if we successfully acquired the lock
			if lockAcquired {
				defer func() {
					if err := lock.Unlock(ctx); err != nil && !errors.Is(err, configstore.ErrLockNotHeld) {
						logger.Warn("failed to release governance startup reset lock: %v", err)
					}
				}()
				resetStart := time.Now()
				if err := tracker.PerformStartupResets(ctx); err != nil {
					logger.Warn("startup reset failed: %v", err)
					// Continue initialization even if startup reset fails (non-critical)
				}
				logger.Info("[startup-timing] PerformStartupResets took %v", time.Since(resetStart))
			}
		}
	}

	// 5. Routing engine (dynamically routing requests based on routing rules)
	engine, err := NewRoutingEngine(governanceStore, logger, routingChainMaxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize routing engine: %w", err)
	}

	ctx, cancelFunc := context.WithCancel(ctx)
	plugin := &GovernancePlugin{
		ctx:                   ctx,
		cancelFunc:            cancelFunc,
		store:                 governanceStore,
		resolver:              resolver,
		tracker:               tracker,
		engine:                engine,
		configStore:           configStore,
		modelCatalog:          modelCatalog,
		mcpCatalog:            mcpCatalog,
		logger:                logger,
		isVkMandatory:         isVkMandatory,
		cfgMutex:              sync.RWMutex{},
		requiredHeaders:       requiredHeaders,
		isEnterprise:          config != nil && config.IsEnterprise,
		disableAutoToolInject: disableAutoToolInject,
		inMemoryStore:         inMemoryStore,
	}
	plugin.storeComplexityAnalyzerConfig(resolveAnalyzerConfigFromStoreOrArg(ctx, logger, configStore, governanceConfig))
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
	// Handle nil config - use safe defaults
	var isVkMandatory *bool
	var requiredHeaders *[]string
	var disableAutoToolInject *bool
	var routingChainMaxDepth *int
	if config != nil {
		isVkMandatory = config.IsVkMandatory
		requiredHeaders = config.RequiredHeaders
		disableAutoToolInject = config.DisableAutoToolInject
		routingChainMaxDepth = config.RoutingChainMaxDepth
	}
	if routingChainMaxDepth == nil {
		defaultDepth := DefaultRoutingChainMaxDepth
		routingChainMaxDepth = &defaultDepth
	}
	resolver := NewBudgetResolver(governanceStore, modelCatalog, logger, inMemoryStore)
	tracker := NewUsageTracker(ctx, governanceStore, resolver, configStore, logger)
	engine, err := NewRoutingEngine(governanceStore, logger, routingChainMaxDepth)
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
		ctx:                   ctx,
		cancelFunc:            cancelFunc,
		store:                 governanceStore,
		resolver:              resolver,
		tracker:               tracker,
		engine:                engine,
		configStore:           configStore,
		modelCatalog:          modelCatalog,
		mcpCatalog:            mcpCatalog,
		logger:                logger,
		inMemoryStore:         inMemoryStore,
		isVkMandatory:         isVkMandatory,
		cfgMutex:              sync.RWMutex{},
		requiredHeaders:       requiredHeaders,
		isEnterprise:          config != nil && config.IsEnterprise,
		disableAutoToolInject: disableAutoToolInject,
	}
	plugin.storeComplexityAnalyzerConfig(resolveAnalyzerConfigFromStoreOrArg(ctx, logger, configStore, nil))
	return plugin, nil
}

// GetName returns the name of the plugin
func (p *GovernancePlugin) GetName() string {
	return PluginName
}

// ReloadComplexityAnalyzerConfig swaps the analyzer used by complexity_tier routing.
func (p *GovernancePlugin) ReloadComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) {
	p.storeComplexityAnalyzerConfig(config)
}

func (p *GovernancePlugin) storeComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) {
	resolved, err := complexity.ValidateAndNormalize(config)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("invalid complexity analyzer config, using defaults: %v", err)
		}
		defaults := complexity.DefaultAnalyzerConfig()
		resolved = &defaults
	}
	p.complexityAnalyzer.Store(complexity.NewComplexityAnalyzerWithConfig(resolved))
}

func resolveAnalyzerConfigFromStoreOrArg(
	ctx context.Context,
	logger schemas.Logger,
	configStore configstore.ConfigStore,
	governanceConfig *configstore.GovernanceConfig,
) *complexity.AnalyzerConfig {
	if governanceConfig != nil && governanceConfig.ComplexityAnalyzerConfig != nil {
		cfg, err := complexity.ValidateAndNormalize(governanceConfig.ComplexityAnalyzerConfig)
		if err != nil {
			if logger != nil {
				logger.Warn("invalid complexity analyzer config from governance config: %v", err)
			}
		} else if cfg != nil {
			return cfg
		}
	}
	if configStore != nil {
		cfg, err := configStore.GetComplexityAnalyzerConfig(ctx)
		if err != nil {
			if logger != nil {
				logger.Warn("failed to load complexity analyzer config from store: %v", err)
			}
		} else if cfg != nil {
			return cfg
		}
	}
	return nil
}

// UpdateEnforceAuthOnInference updates the enforce auth on inference config
func (p *GovernancePlugin) UpdateEnforceAuthOnInference(enforceAuthOnInference bool) {
	p.cfgMutex.Lock()
	defer p.cfgMutex.Unlock()
	p.isVkMandatory = new(enforceAuthOnInference)
}

// HTTPTransportPreHook is retained as a no-op so governance still satisfies the
// HTTPTransportPlugin interface (used by the enterprise wrapper's 503 gate delegation).
// All routing now flows through PreRequestHook: body-having requests via handleRequest,
// large-payload requests via PreRequestHook reading LargePayloadMetadata, and realtime WS
// upgrades via the realtime handler's explicit RunPreRequestHooks call.
func (p *GovernancePlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// runPreRequestRouting wraps a model string in a synthetic BifrostRequest, runs the same
// applyRoutingRules + loadBalanceProvider helpers used by the main PreRequestHook path, and
// returns the resolved model (provider-prefixed when a provider was selected, plain model
// otherwise). Used by PreRequestHook's large-payload branch where req.Model is empty because
// the body wasn't parsed.
func (p *GovernancePlugin) runPreRequestRouting(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey, hasRoutingRules bool, modelIn string, requestType schemas.RequestType) (string, error) {
	// Parse a provider-prefixed model string the same way the transport does for
	// body-having requests, so an explicit prefix like "openai/gpt-4o" lands in
	// ChatRequest.Provider and load balancing honors the caller's routing intent.
	providerIn, parsedModel := schemas.ParseModelString(modelIn, "")
	synthetic := &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{Provider: providerIn, Model: parsedModel},
	}

	if hasRoutingRules {
		if _, err := p.applyRoutingRules(ctx, synthetic, virtualKey); err != nil {
			return modelIn, err
		}
	}

	if virtualKey != nil {
		if err := p.loadBalanceProvider(ctx, synthetic, virtualKey); err != nil {
			return modelIn, err
		}

		// A caller-provided include-tools list can only narrow the virtual key's
		// tool grant, never expand it — prune entries the key does not allow.
		includeToolsProvided := p.pruneMCPIncludeToolsFromContext(ctx, virtualKey)

		p.cfgMutex.RLock()
		autoInjectDisabled := p.disableAutoToolInject != nil && *p.disableAutoToolInject
		p.cfgMutex.RUnlock()
		// An include-clients filter opts the request into tool injection even when
		// auto-injection is disabled (see ParseAndAddToolsToRequest in core/mcp), so
		// the key's allowlist must be stamped on every path where injection can run.
		includeClientsPresent := ctx.Value(schemas.MCPContextKeyIncludeClients) != nil
		if !includeToolsProvided && (!autoInjectDisabled || includeClientsPresent) {
			if tools := p.computeMCPIncludeTools(virtualKey); tools != nil {
				ctx.SetValue(schemas.MCPContextKeyIncludeTools, tools)
			}
		}
	}

	provider, model, _ := synthetic.GetRequestFields()
	if provider != "" {
		return string(provider) + "/" + model, nil
	}
	return model, nil
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

// loadBalanceProvider picks a weighted provider from the VK's configs for req.Model
// and mutates req.Provider/req.Model with the refined provider/model. Also populates req.Fallbacks
// from the remaining weighted providers if no fallbacks were configured by the caller.
func (p *GovernancePlugin) loadBalanceProvider(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, virtualKey *configstoreTables.TableVirtualKey) error {
	provider, modelStr, existingFallbacks := req.GetRequestFields()
	if modelStr == "" {
		return nil
	}

	if provider != "" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Skipping load balancing for model %s: provider %s already set", modelStr, provider))
		return nil
	}

	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Load balancing provider for model %s", modelStr))

	// Get provider configs for this virtual key
	providerConfigs := virtualKey.ProviderConfigs
	if len(providerConfigs) == 0 {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelWarn, fmt.Sprintf("No provider configs on virtual key %s for model %s, skipping load balancing", virtualKey.Name, modelStr))
		// No provider configs, continue without modification
		return nil
	}

	var configuredProviders []string
	for _, pc := range providerConfigs {
		configuredProviders = append(configuredProviders, pc.Provider)
	}
	p.logger.Debug("[Governance] Virtual key has %d provider configs: %v", len(providerConfigs), configuredProviders)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Load balancing model %s across %d configured providers: %v", modelStr, len(providerConfigs), configuredProviders))

	// Pre-pass: if any config for a provider blacklists the model, that provider is fully blocked.
	blacklistedProviders := make(map[string]bool)
	for _, config := range providerConfigs {
		if config.BlacklistedModels.IsBlocked(modelStr) {
			blacklistedProviders[config.Provider] = true
		}
	}

	allowedProviderConfigs := make([]configstoreTables.TableVirtualKeyProviderConfig, 0)
	for _, config := range providerConfigs {
		// Blacklist check wins over allowlist (same as provider-key enforcement)
		if blacklistedProviders[config.Provider] {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Provider %s excluded: model %s is blacklisted", config.Provider, modelStr))
			continue
		}

		// Delegate model allowance check to model catalog
		// This handles all cross-provider logic (OpenRouter, Vertex, Groq, Bedrock)
		// and provider-prefixed allowed_models entries
		isProviderAllowed := false
		if p.modelCatalog != nil && p.inMemoryStore != nil {
			provider := schemas.ModelProvider(config.Provider)
			providerConfig, ok := p.inMemoryStore.GetConfiguredProviders()[provider]
			providerConfigPtr := &providerConfig
			if !ok {
				providerConfigPtr = nil
			}
			isProviderAllowed = p.modelCatalog.IsModelAllowedForProvider(provider, modelStr, providerConfigPtr, config.AllowedModels)
		} else {
			// Fallback when model catalog is not available: simple string matching
			// ["*"] = allow all models; [] = deny all models
			isProviderAllowed = config.AllowedModels.IsAllowed(modelStr)
		}

		if isProviderAllowed {
			// Check if the provider's budget or rate limits are violated using resolver helper methods
			if p.resolver.isProviderBudgetViolated(ctx, virtualKey, config) {
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Provider %s excluded: budget limit violated", config.Provider))
				continue
			}
			if p.resolver.isProviderRateLimitViolated(ctx, virtualKey, config) {
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Provider %s excluded: rate limit violated", config.Provider))
				continue
			}
			allowedProviderConfigs = append(allowedProviderConfigs, config)
		} else {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Provider %s excluded: model %s not in allowed models list", config.Provider, modelStr))
		}
	}

	var allowedProviders []string
	for _, pc := range allowedProviderConfigs {
		allowedProviders = append(allowedProviders, pc.Provider)
	}
	p.logger.Debug("[Governance] Allowed providers after filtering: %v", allowedProviders)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Allowed providers after filtering: %v", allowedProviders))

	if len(allowedProviderConfigs) == 0 {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("No eligible providers remaining after filtering for model %s, skipping load balancing", modelStr))
		// TODO: Send proper error if (overall VK budget/rate limit) or (all provider budgets/rate limits) are violated
		// No allowed provider configs, continue without modification
		return nil
	}

	weightedConfigs := make([]configstoreTables.TableVirtualKeyProviderConfig, 0, len(allowedProviderConfigs))
	for _, config := range allowedProviderConfigs {
		if config.Weight != nil {
			weightedConfigs = append(weightedConfigs, config)
		}
	}

	if len(weightedConfigs) == 0 {
		// All allowed configs survived the model-allowance / budget / rate-limit filters,
		// but none of them have a Weight set — there's nothing to feed weighted selection.
		// Emit an explicit log so the routing trail explains why governance stops here
		// instead of trailing off after "Allowed providers after filtering: [...]".
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("No weighted configs for model %s — none of the allowed VK provider configs have a weight assigned; skipping load balancing", modelStr))
		return nil
	}

	var selectedProvider schemas.ModelProvider
	totalWeight := 0.0
	for _, config := range weightedConfigs {
		totalWeight += getWeight(config.Weight)
	}
	// Generate random number between 0 and totalWeight
	randomValue := rand.Float64() * totalWeight
	// Select provider based on weighted random selection
	currentWeight := 0.0
	for _, config := range weightedConfigs {
		currentWeight += getWeight(config.Weight)
		if randomValue <= currentWeight {
			selectedProvider = schemas.ModelProvider(config.Provider)
			break
		}
	}
	// Fallback: if no provider was selected (shouldn't happen but guard against FP issues)
	if selectedProvider == "" {
		selectedProvider = schemas.ModelProvider(weightedConfigs[0].Provider)
	}

	p.logger.Debug("[governance] Selected provider: %s", selectedProvider)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Selected provider %s for model %s (from %d eligible: %v)", selectedProvider, modelStr, len(allowedProviderConfigs), allowedProviders))

	refinedModel := modelStr
	// Refine the model for the selected provider
	if p.modelCatalog != nil {
		var err error
		refinedModel, err = p.modelCatalog.RefineModelForProvider(selectedProvider, modelStr)
		if err != nil {
			return err
		}
	}

	req.SetProvider(selectedProvider)
	req.SetModel(refinedModel)

	schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineGovernance)

	if len(existingFallbacks) == 0 && len(weightedConfigs) > 1 {
		fallbackConfigs := append([]configstoreTables.TableVirtualKeyProviderConfig(nil), weightedConfigs...)
		sort.Slice(fallbackConfigs, func(i, j int) bool {
			return getWeight(fallbackConfigs[i].Weight) > getWeight(fallbackConfigs[j].Weight)
		})

		// Filter out the selected provider and create fallbacks array
		fallbacks := make([]schemas.Fallback, 0, len(fallbackConfigs)-1)
		for _, config := range fallbackConfigs {
			if config.Provider == string(selectedProvider) {
				continue
			}
			fbProvider := schemas.ModelProvider(config.Provider)
			fbModel := modelStr
			if p.modelCatalog != nil {
				refined, err := p.modelCatalog.RefineModelForProvider(fbProvider, modelStr)
				if err != nil {
					p.logger.Warn("failed to refine model for fallback, skipping fallback in governance plugin: %v", err)
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelWarn, fmt.Sprintf("Fallback provider %s skipped: failed to refine model %s for this provider", fbProvider, modelStr))
					continue
				}
				fbModel = refined
			}
			fallbacks = append(fallbacks, schemas.Fallback{Provider: fbProvider, Model: fbModel})
		}
		req.SetFallbacks(fallbacks)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Added %d fallback providers", len(fallbacks)))
	}

	return nil
}

// publishRoutingAllowlist records, for downstream routing layers, which of the VK's configured
// providers permit modelStr according to the VK's own allowed_models / blocked_models. It is a
// coarse provider gate (BifrostContextKeyRoutingAllowedProviders) layered on top of the model
// catalog checks those layers already run — its purpose is to stop a later routing layer (load
// balancing, model-catalog resolution) from selecting a provider the VK forbids for this model,
// even when governance itself couldn't pick one. An empty slice means "no provider is permitted"
// (fail-closed via the empty-provider validation in handleRequest); a nil VK publishes nothing.
//
// Provider prefixes on the request model are already split into req.Provider + bare model at the
// HTTP layer (resolveModelAndProvider), so VK allowed_models / blocked_models are matched against
// bare names and plain membership checks are sufficient here.
func (p *GovernancePlugin) publishRoutingAllowlist(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey, modelStr string) {
	if virtualKey == nil {
		return
	}
	allowed := make([]schemas.ModelProvider, 0, len(virtualKey.ProviderConfigs))
	for _, pc := range virtualKey.ProviderConfigs {
		// No model to filter on → keep the provider so we don't over-restrict.
		if modelStr == "" ||
			(pc.AllowedModels.IsAllowed(modelStr) && !pc.BlacklistedModels.IsBlocked(modelStr)) {
			allowed = append(allowed, schemas.ModelProvider(pc.Provider))
		}
	}
	ctx.SetValue(schemas.BifrostContextKeyRoutingAllowedProviders, allowed)
}

// applyRoutingRules evaluates routing rules against req and mutates
// req.Provider/req.Model/req.Fallbacks when a rule matches. Returns the matched RoutingDecision
// (nil if no rule matched). Integrations normalize req.Model (and Provider when applicable) before
// the BifrostRequest reaches this point.
func (p *GovernancePlugin) applyRoutingRules(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, virtualKey *configstoreTables.TableVirtualKey) (*RoutingDecision, error) {
	provider, model, _ := req.GetRequestFields()
	if model == "" {
		return nil, nil
	}

	requestType := string(req.RequestType)
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	queryParams, _ := ctx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string)

	// Set up lazy complexity computation; only runs if a rule references complexity_tier.
	var computeComplexity func() *complexity.ComplexityResult
	if analyzer := p.complexityAnalyzer.Load(); analyzer != nil {
		computeComplexity = func() *complexity.ComplexityResult {
			input, ok := buildComplexityInput(req)
			if !ok {
				if p.logger != nil {
					p.logger.Debug("[Governance] Complexity analysis skipped: unsupported request type")
				}
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, "Complexity analysis skipped: no supported text-bearing input detected")
				return nil
			}

			result := analyzer.Analyze(input)
			if result == nil {
				if p.logger != nil {
					p.logger.Debug("[Governance] %s", noComplexitySignalLog)
				}
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelDebug, noComplexitySignalLog)
				return nil
			}
			if p.logger != nil {
				p.logger.Debug(
					"[Governance] Complexity analysis details: tier=%s score=%.2f words=%d",
					result.Tier,
					result.Score,
					result.WordCount,
				)
			}
			ctx.AppendRoutingEngineLog(
				schemas.RoutingEngineRoutingRule,
				schemas.LogLevelInfo,
				fmt.Sprintf("Complexity: tier=%s score=%.2f words=%d", result.Tier, result.Score, result.WordCount),
			)
			return result
		}
	}

	routingCtx := &RoutingContext{
		VirtualKey:               virtualKey,
		Provider:                 provider,
		Model:                    model,
		RequestType:              requestType,
		Headers:                  headers,
		QueryParams:              queryParams,
		BudgetAndRateLimitStatus: p.store.GetBudgetAndRateLimitStatus(ctx, model, provider, virtualKey, nil, nil, nil),
		computeComplexity:        computeComplexity,
	}

	p.logger.Debug("[PreRequestHook] Built routing context: provider=%s, model=%s, requestType=%s, vk=%v",
		provider, model, requestType, virtualKey != nil)

	// Evaluate routing rules
	decision, err := p.engine.EvaluateRoutingRules(ctx, routingCtx)
	if err != nil {
		p.logger.Error("failed to evaluate routing rules: %v", err)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelError, fmt.Sprintf("Routing rule evaluation error: %v", err))
		return nil, nil
	}
	if decision == nil {
		return nil, nil
	}

	p.logger.Debug("[Governance] Routing rule matched: %s", decision.MatchedRuleName)

	if decision.Provider != "" {
		req.SetProvider(schemas.ModelProvider(decision.Provider))
	}
	if decision.Model != "" {
		req.SetModel(decision.Model)
	}

	schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineRoutingRule)

	// Add fallbacks if present; fill in the incoming model for fallbacks that omit it
	if len(decision.Fallbacks) > 0 {
		resolvedFallbacks := make([]schemas.Fallback, 0, len(decision.Fallbacks))
		for _, fb := range decision.Fallbacks {
			fbProvider, fbModel := schemas.ParseModelString(fb, "")
			trimmedFbProvider := strings.TrimSpace(string(fbProvider))
			trimmedFbModel := strings.TrimSpace(fbModel)
			if trimmedFbProvider == "" {
				continue
			}
			if trimmedFbModel == "" && model != "" {
				trimmedFbModel = model
			}
			resolvedFallbacks = append(resolvedFallbacks, schemas.Fallback{
				Provider: schemas.ModelProvider(trimmedFbProvider),
				Model:    trimmedFbModel,
			})
		}
		req.SetFallbacks(resolvedFallbacks)
	}

	// Pin specific API key by ID if the routing rule specifies one. This uses a dedicated,
	// non-reserved context key (not BifrostContextKeyAPIKeyID): routing runs inside
	// PreRequestHook, where core blocks writes to reserved key-selection keys, so a write to
	// the caller-pin key would be silently dropped. Key selection reads this routing pin first
	// and resolves it against the configured key pool.
	if decision.KeyID != "" {
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, decision.KeyID)
	}

	p.logger.Debug("[Governance] Applied routing decision: provider=%s, model=%s, keyID=%s, fallbacks=%v", decision.Provider, decision.Model, decision.KeyID, decision.Fallbacks)
	return decision, nil
}

// computeMCPIncludeTools builds the MCP include-tools list for a virtual key. Returns the list
// directly; callers store it via ctx.SetValue(schemas.MCPContextKeyIncludeTools, ...). VK-specific
// MCP configs take precedence over AllowOnAllVirtualKeys clients.
func (p *GovernancePlugin) computeMCPIncludeTools(virtualKey *configstoreTables.TableVirtualKey) []string {
	var allowAllVKsClients map[string]string
	if p.inMemoryStore != nil {
		allowAllVKsClients = p.inMemoryStore.GetMCPClientsAllowingAllVirtualKeys()
	}
	return p.computeMCPIncludeToolsWith(virtualKey, allowAllVKsClients)
}

// computeMCPIncludeToolsWith is the computeMCPIncludeTools variant taking a pre-fetched
// AllowOnAllVirtualKeys map (clientID → clientName), so callers that make multiple
// grant decisions per request can evaluate them all against one consistent snapshot.
func (p *GovernancePlugin) computeMCPIncludeToolsWith(virtualKey *configstoreTables.TableVirtualKey, allowAllVKsClients map[string]string) []string {
	executeOnlyTools := make([]string, 0)

	if allowAllVKsClients == nil {
		allowAllVKsClients = make(map[string]string)
	}

	// Process VK-specific MCP configs first — explicit config always overrides AllowOnAllVirtualKeys.
	// Track which AllowOnAllVirtualKeys clients have an explicit VK config so we don't double-add them.
	handledClients := make(map[string]bool)
	for _, vkMcpConfig := range virtualKey.MCPConfigs {
		clientID := vkMcpConfig.MCPClient.ClientID
		if _, isAllowAll := allowAllVKsClients[clientID]; isAllowAll {
			// Explicit VK config exists — it takes precedence; mark as handled regardless of tool list
			handledClients[clientID] = true
		}
		if vkMcpConfig.ToolsToExecute.IsEmpty() {
			// No tools specified in virtual key config - skip this client entirely
			continue
		}
		if vkMcpConfig.ToolsToExecute.IsUnrestricted() {
			executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-*", vkMcpConfig.MCPClient.Name))
			continue
		}
		for _, tool := range vkMcpConfig.ToolsToExecute {
			if tool != "" {
				executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-%s", vkMcpConfig.MCPClient.Name, tool))
			}
		}
	}

	// For AllowOnAllVirtualKeys clients with no explicit VK config, fall back to allowing all tools
	for clientID, clientName := range allowAllVKsClients {
		if !handledClients[clientID] {
			executeOnlyTools = append(executeOnlyTools, fmt.Sprintf("%s-*", clientName))
		}
	}

	return executeOnlyTools
}

// pruneMCPIncludeToolsFromContext narrows a caller-provided include-tools list (stamped on ctx
// from the x-bf-mcp-include-tools header in lib/ctx.go) down to the tools the virtual key
// allows, and writes the pruned list back to ctx. Returns true when a caller list was present,
// regardless of how many entries survived. Entries the key does not grant are dropped; a
// "client-*" wildcard is kept only when the key itself is unrestricted for that client,
// otherwise it is replaced by the key's specific grants for that client (passing the wildcard
// through would read downstream as "all tools of this client").
func (p *GovernancePlugin) pruneMCPIncludeToolsFromContext(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey) bool {
	existing := ctx.Value(schemas.MCPContextKeyIncludeTools)
	if existing == nil {
		return false
	}
	requested, _ := existing.([]string)

	// Fetch the AllowOnAllVirtualKeys snapshot once so the wildcard checks (via vkSet)
	// and the per-tool checks (via isMCPToolAllowedByVKWith) can't observe different
	// states across a concurrent config reload.
	var allowAllClients map[string]string
	if p.inMemoryStore != nil {
		allowAllClients = p.inMemoryStore.GetMCPClientsAllowingAllVirtualKeys()
	}

	vkTools := p.computeMCPIncludeToolsWith(virtualKey, allowAllClients)
	vkSet := make(map[string]struct{}, len(vkTools))
	for _, tool := range vkTools {
		vkSet[tool] = struct{}{}
	}

	pruned := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	add := func(tool string) {
		if _, dup := seen[tool]; !dup {
			seen[tool] = struct{}{}
			pruned = append(pruned, tool)
		}
	}
	for _, pattern := range requested {
		if pattern == "" {
			continue
		}
		if clientName, isWildcard := strings.CutSuffix(pattern, "-*"); isWildcard {
			if _, ok := vkSet[pattern]; ok {
				add(pattern)
				continue
			}
			for _, tool := range vkTools {
				if strings.HasPrefix(tool, clientName+"-") {
					add(tool)
				}
			}
			continue
		}
		if p.isMCPToolAllowedByVKWith(virtualKey, pattern, allowAllClients) {
			add(pattern)
		}
	}

	ctx.SetValue(schemas.MCPContextKeyIncludeTools, pruned)
	return true
}

// EvaluateGovernanceRequest is a common function that handles virtual key validation
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
func (p *GovernancePlugin) EvaluateGovernanceRequest(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, requestType schemas.RequestType) (*EvaluationResult, *schemas.BifrostError) {
	// Check if authentication is mandatory (either VK or user auth)
	// Checking if the virtual key is valid or not
	isVirtualKeyValid := false
	if evaluationRequest.VirtualKey != "" {
		_, exists := p.store.GetVirtualKey(ctx, evaluationRequest.VirtualKey)
		if exists {
			isVirtualKeyValid = true
		} else {
			// VK was provided but does not exist in the store — reject regardless of mandatory setting
			return nil, &schemas.BifrostError{
				Type:       new("virtual_key_not_found"),
				StatusCode: new(401),
				Error: &schemas.ErrorField{
					Message: "virtual key not found. The provided virtual key does not exist or has been revoked.",
				},
			}
		}
	}
	p.cfgMutex.RLock()
	if !isVirtualKeyValid && !hasDirectKeyAuth(ctx) && evaluationRequest.UserID == "" && p.isVkMandatory != nil && *p.isVkMandatory {
		message := "virtual key is required. Provide a virtual key via the x-bf-vk header."
		if p.isEnterprise {
			message = "authentication is required. Provide a virtual key (x-bf-vk), API key, or user token."
		}
		p.cfgMutex.RUnlock()
		return nil, &schemas.BifrostError{
			Type:       new("virtual_key_required"),
			StatusCode: new(401),
			Error: &schemas.ErrorField{
				Message: message,
			},
		}
	}
	p.cfgMutex.RUnlock()

	// First evaluate model and provider checks (applies even when virtual keys are disabled or not present)
	result := p.resolver.EvaluateModelAndProviderRequest(ctx, evaluationRequest.Provider, evaluationRequest.Model)

	// The flow for governance checks is:
	//   VK (identity + VK-level budget/rate-limit) -> Customer -> Team -> User
	// VK identity runs FIRST so that revoked, provider-disallowed, or model-disallowed
	// keys are blocked before any hierarchy state is consulted. Running Customer/Team/
	// User ahead of VK would leak topology: a revoked key attached to an over-budget
	// team would return 429 team-budget-exceeded instead of 403 VK-blocked, telling
	// an attacker the key's team structure was validated.

	// Resolve the VK once; it feeds both the VK evaluation and hierarchy-ID extraction.
	var hierarchyVK *configstoreTables.TableVirtualKey
	if evaluationRequest.VirtualKey != "" {
		if vk, ok := p.store.GetVirtualKey(ctx, evaluationRequest.VirtualKey); ok && vk != nil {
			hierarchyVK = vk
		}
	}

	// Read-only metadata calls (e.g. list models) set this flag to skip budget/rate-limit
	// checks while still enforcing VK identity (existence, active status, provider/model filtering).
	skipBudgetsAndRateLimits := bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipBudgetAndRateLimits)

	// Step 1: Evaluate virtual key (identity + VK-level budget/rate-limit hierarchy).
	// Short-circuits with VirtualKeyBlocked / ProviderBlocked / ModelBlocked before
	// we touch Customer / Team / User.
	if result.Decision == DecisionAllow && evaluationRequest.VirtualKey != "" {
		skipVKBudgetLimit := evaluationRequest.UserID != "" || skipBudgetsAndRateLimits
		result = p.resolver.EvaluateVirtualKeyRequest(ctx, evaluationRequest.VirtualKey, evaluationRequest.Provider, evaluationRequest.Model, requestType, skipVKBudgetLimit)
	}

	// Step 2: Customer-level budget (customer attached directly to VK, or via the VK's team).
	// Fall back to the loaded relation IDs so VKs populated via joins without FK
	// pointer columns still participate in customer-level enforcement.
	if !skipBudgetsAndRateLimits && result.Decision == DecisionAllow && hierarchyVK != nil {
		var customerID string
		customerFromTeam := false
		switch {
		case hierarchyVK.CustomerID != nil:
			customerID = *hierarchyVK.CustomerID
		case hierarchyVK.Customer != nil:
			customerID = hierarchyVK.Customer.ID
		case hierarchyVK.Team != nil && hierarchyVK.Team.CustomerID != nil:
			customerID = *hierarchyVK.Team.CustomerID
			customerFromTeam = true
		case hierarchyVK.Team != nil && hierarchyVK.Team.Customer != nil:
			customerID = hierarchyVK.Team.Customer.ID
			customerFromTeam = true
		}
		// When the request is scoped to a specific customer (header-driven, team-VK
		// path; stamped by the enterprise plugin), skip enforcing the scalar
		// team.CustomerID customer if it is not the scoped one — the enterprise layer
		// enforces the scoped customer instead. Mirrors collectBudgetsFromHierarchy.
		scopedCustomerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceScopedCustomerID).(string)
		scopedAway := customerFromTeam && scopedCustomerID != "" && scopedCustomerID != customerID
		if customerID != "" && !scopedAway {
			result = p.resolver.EvaluateCustomerRequest(ctx, customerID, evaluationRequest)
		}
	}

	// Step 3: Team-level budget. Fall back to vk.Team.ID when the FK pointer is nil
	// but the relation is populated.
	if !skipBudgetsAndRateLimits && result.Decision == DecisionAllow && hierarchyVK != nil {
		var teamID string
		switch {
		case hierarchyVK.TeamID != nil:
			teamID = *hierarchyVK.TeamID
		case hierarchyVK.Team != nil:
			teamID = hierarchyVK.Team.ID
		}
		if teamID != "" {
			result = p.resolver.EvaluateTeamRequest(ctx, teamID, evaluationRequest)
		}
	}

	// Step 4: User-level governance (enterprise-only).
	if !skipBudgetsAndRateLimits && result.Decision == DecisionAllow {
		result = p.resolver.EvaluateUserRequest(ctx, evaluationRequest.UserID, evaluationRequest)
	}

	// Check the actual MCP tools injected into the request against the VK MCPConfigs.
	// BifrostContextKeyMCPAddedTools is populated by AddToolsToRequest (which runs before
	// PreLLMHook), so it contains the real expanded tool names (e.g. "youtube-search") rather
	// than raw header patterns (e.g. "youtube-*"), giving us exact per-tool validation.
	if result.Decision == DecisionAllow && result.VirtualKey != nil {
		if addedTools, ok := ctx.Value(schemas.BifrostContextKeyMCPAddedTools).([]string); ok && len(addedTools) > 0 {
			// Fetch once before the loop to avoid repeated lock acquisitions per tool.
			var allowAllClients map[string]string
			if p.inMemoryStore != nil {
				allowAllClients = p.inMemoryStore.GetMCPClientsAllowingAllVirtualKeys()
			}
			var disallowed []string
			for _, tool := range addedTools {
				if !p.isMCPToolAllowedByVKWith(result.VirtualKey, tool, allowAllClients) {
					disallowed = append(disallowed, tool)
				}
			}
			if len(disallowed) > 0 {
				result = &EvaluationResult{
					Decision:   DecisionMCPToolBlocked,
					Reason:     fmt.Sprintf("MCP tools not allowed for virtual key '%s': %s", result.VirtualKey.Name, strings.Join(disallowed, ", ")),
					VirtualKey: result.VirtualKey,
				}
			}
		}
	}

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
		// Clear any prior rejection flag (e.g. from a failed primary attempt
		// before a fallback retry). Without this, PostLLMHook would see the
		// stale flag and skip budget/rate-limit ID collection for the
		// successful fallback attempt.
		if ctx != nil {
			ctx.ClearValue(governanceRejectedContextKey)
		}
		return result, nil

	case DecisionVirtualKeyNotFound, DecisionVirtualKeyBlocked, DecisionModelBlocked, DecisionProviderBlocked:
		return result, &schemas.BifrostError{
			Type:       new(string(result.Decision)),
			StatusCode: new(403),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	case DecisionRateLimited, DecisionTokenLimited, DecisionRequestLimited:
		return result, &schemas.BifrostError{
			Type:       new(string(result.Decision)),
			StatusCode: new(429),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	case DecisionBudgetExceeded:
		return result, &schemas.BifrostError{
			Type:       new(string(result.Decision)),
			StatusCode: new(402),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	case DecisionMCPToolBlocked:
		return result, &schemas.BifrostError{
			Type:       new(string(result.Decision)),
			StatusCode: new(403),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	default:
		// Fallback to deny for unknown decisions
		return result, &schemas.BifrostError{
			Type: new(string(result.Decision)),
			Error: &schemas.ErrorField{
				Message: "Governance decision error",
			},
		}
	}
}

// hasDirectKeyAuth returns true when the transport accepted an admin-enabled direct provider key.
func hasDirectKeyAuth(ctx *schemas.BifrostContext) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	return ok
}

// isMCPToolAllowedByVK checks whether a tool pattern (in "clientName-toolName" or "clientName-*"
// format) is permitted by the virtual key's MCPConfigs.
//
// Priority order:
//  1. If the VK has an explicit MCP config for this client, that config is authoritative (can allow or deny).
//  2. If no explicit config exists and the client has AllowOnAllVirtualKeys=true, all tools are allowed.
//
// For wildcard patterns ("clientName-*"): allowed if VK has the client configured with any tools.
// Specific tool enforcement happens at execution time via checkVKMCPToolAllowance.
// For specific tools ("clientName-toolName"): allowed if VK has "*" or the exact tool name.
func (p *GovernancePlugin) isMCPToolAllowedByVK(vk *configstoreTables.TableVirtualKey, toolPattern string) bool {
	var allowAllClients map[string]string
	if p.inMemoryStore != nil {
		allowAllClients = p.inMemoryStore.GetMCPClientsAllowingAllVirtualKeys()
	}
	return p.isMCPToolAllowedByVKWith(vk, toolPattern, allowAllClients)
}

// isMCPToolAllowedByVKWith checks whether a tool pattern is allowed by the virtual key,
// using a pre-fetched allowAllClients map (clientID → clientName) to avoid repeated lock
// acquisitions in loops.
func (p *GovernancePlugin) isMCPToolAllowedByVKWith(vk *configstoreTables.TableVirtualKey, toolPattern string, allowAllClients map[string]string) bool {
	// Check VK-specific MCP configs first — explicit config always overrides AllowOnAllVirtualKeys.
	for _, mcpConfig := range vk.MCPConfigs {
		clientName := mcpConfig.MCPClient.Name
		if toolPattern != clientName+"-*" && !strings.HasPrefix(toolPattern, clientName+"-") {
			continue
		}
		// Found an explicit config for this client — use it; do not fall back to AllowOnAllVirtualKeys.
		if toolPattern == clientName+"-*" {
			return !mcpConfig.ToolsToExecute.IsEmpty()
		}
		if mcpConfig.ToolsToExecute.IsUnrestricted() {
			return true
		}
		toolSuffix := strings.TrimPrefix(toolPattern, clientName+"-")
		return mcpConfig.ToolsToExecute.Contains(toolSuffix)
	}
	// No explicit VK config found — fall back to AllowOnAllVirtualKeys (allows all tools).
	for _, clientName := range allowAllClients {
		if strings.HasPrefix(toolPattern, clientName+"-") || toolPattern == clientName+"-*" {
			return true
		}
	}
	return false
}

// PreRequestHook is the per-request governance phase. It runs for both normal body-having
// requests (route on req.Model) and large-payload streaming requests (route on
// LargePayloadMetadata.Model from ctx — the body is opaque mid-stream, so routing is
// constrained to same-protocol-family targets that the upstream provider can hydrate
// from the rewritten metadata).
//
// Realtime + generic streaming bypass handleRequest (see core/bifrost.go
// RunRealtimeTurnPreHooks / RunStreamPreHooks) and are still handled at HTTPTransportPreHook.
func (p *GovernancePlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}

	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	hasRoutingRules := p.store.HasRoutingRules(ctx)
	if virtualKeyValue == "" && !hasRoutingRules {
		return nil
	}

	var virtualKey *configstoreTables.TableVirtualKey
	if virtualKeyValue != "" {
		var ok bool
		virtualKey, ok = p.store.GetVirtualKey(ctx, virtualKeyValue)
		if !ok || virtualKey == nil || !virtualKey.IsActiveValue() || virtualKey.IsExpiredAt(time.Now().UTC()) {
			return nil
		}
	}

	stampGovernanceCtxFromVK(ctx, virtualKey)

	// Large-payload mode: the body streams to the provider unparsed, so req.Model is
	// empty for routes where the model lives in the body (OpenAI/Anthropic chat,
	// responses, etc.). Route on LargePayloadMetadata.Model — the provider's
	// streaming body rewriter (ApplyLargePayloadRequestBodyWithModelNormalization)
	// reads metadata.Model when it rewrites the model field in the body prefix, so
	// mutating it here is what propagates the routing decision to the upstream call.
	if metadata, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMetadata).(*schemas.LargePayloadMetadata); metadata != nil && metadata.Model != "" {
		newModel, err := p.runPreRequestRouting(ctx, virtualKey, hasRoutingRules, metadata.Model, req.RequestType)
		if err != nil {
			return err
		}
		if newModel != "" && newModel != metadata.Model {
			metadata.Model = newModel
		}
		_, routedModel := schemas.ParseModelString(metadata.Model, "")
		p.publishRoutingAllowlist(ctx, virtualKey, routedModel)
		return nil
	}

	if hasRoutingRules {
		if _, err := p.applyRoutingRules(ctx, req, virtualKey); err != nil {
			return err
		}
	}

	// Publish the VK provider allowlist for the (post routing-rules) model so downstream routing
	// layers (load balancing, model-catalog resolution) and core enforcement intersect their
	// candidates with it — a later layer must not select a provider the VK forbids for this model.
	_, routedModel, _ := req.GetRequestFields()
	p.publishRoutingAllowlist(ctx, virtualKey, routedModel)

	if virtualKey != nil {
		if err := p.loadBalanceProvider(ctx, req, virtualKey); err != nil {
			return err
		}

		// A caller-provided include-tools list can only narrow the virtual key's
		// tool grant, never expand it — prune entries the key does not allow.
		includeToolsProvided := p.pruneMCPIncludeToolsFromContext(ctx, virtualKey)

		p.cfgMutex.RLock()
		autoInjectDisabled := p.disableAutoToolInject != nil && *p.disableAutoToolInject
		p.cfgMutex.RUnlock()
		// An include-clients filter opts the request into tool injection even when
		// auto-injection is disabled (see ParseAndAddToolsToRequest in core/mcp), so
		// the key's allowlist must be stamped on every path where injection can run.
		includeClientsPresent := ctx.Value(schemas.MCPContextKeyIncludeClients) != nil
		if !includeToolsProvided && (!autoInjectDisabled || includeClientsPresent) {
			if tools := p.computeMCPIncludeTools(virtualKey); tools != nil {
				ctx.SetValue(schemas.MCPContextKeyIncludeTools, tools)
			}
		}
	}

	return nil
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
	// Validate required headers are present
	if headerErr := p.validateRequiredHeaders(ctx); headerErr != nil {
		return req, &schemas.LLMPluginShortCircuit{Error: headerErr}, nil
	}
	// Extract virtual key using utility functions
	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	// Extract user ID for enterprise user-level governance
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)
	// Getting provider and mode from the request
	provider, model, _ := req.GetRequestFields()
	// Create request context for evaluation
	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKeyValue,
		Provider:   provider,
		Model:      model,
		UserID:     userID,
	}
	// Evaluate governance using common function
	_, bifrostError := p.EvaluateGovernanceRequest(ctx, evaluationRequest, req.RequestType)
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

	// Extract request type, provider, and model
	requestType, provider, requestedModel, _ := bifrost.GetResponseFields(result, err)

	// Extract governance information
	virtualKey := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	// Extract user ID for enterprise user-level governance
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)

	if requestType == schemas.ListModelsRequest && result != nil && result.ListModelsResponse != nil && virtualKey != "" {
		// filter models which are not supported on this virtual key
		result.ListModelsResponse.Data = p.filterModelsForVirtualKey(ctx, result.ListModelsResponse.Data, virtualKey)
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	// Build pricing scopes from context using the governance VK ID (not the raw VK token)
	pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(provider))

	// Always process usage tracking. When both virtual key and user are present,
	// track both scopes; callers that intentionally want user-only accounting can
	// set BifrostContextKeySkipVirtualKeyUsageTracking.
	effectiveVK := virtualKey
	if bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipVirtualKeyUsageTracking) {
		effectiveVK = ""
	}
	// If effectiveVK is empty, it will be passed as empty string to postHookWorker
	// The tracker will handle empty virtual keys gracefully by only updating provider-level and model-level usage
	if requestedModel != "" {
		// Collect the affected budget and rate-limit IDs synchronously (fast in-memory
		// lookups) and attach them to the context. The logging plugin reads these keys
		// when building the log entry, enabling ghost-node usage reconciliation to
		// attribute cost/tokens to the correct governance entities.
		budgetIDs, rateLimitIDs := p.store.CollectApplicableGovernanceIDs(ctx, effectiveVK, userID, provider, requestedModel)
		if len(budgetIDs) > 0 {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, budgetIDs)
		}
		if len(rateLimitIDs) > 0 {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceRateLimitIDs, rateLimitIDs)
		}

		// Attempt number distinguishes physical provider calls within one
		// logical request so each token-consuming attempt bills exactly once.
		// Set by core on every retry iteration.
		attemptNumber := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyNumberOfRetries)

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			// Recover so a billing panic (e.g. an unexpected nil deref) can never
			// crash the process and lose in-memory counters.
			defer func() {
				if r := recover(); r != nil {
					p.logger.Error("recovered from panic in governance postHookWorker: %v", r)
				}
			}()
			// Use the requested model for usage tracking
			p.postHookWorker(result, err, provider, requestedModel, requestType, effectiveVK, requestID, userID, isFinalChunk, attemptNumber, pricingScopes)
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

	// Skip for non tool execution requests
	if !req.RequestType.IsExecuteTool() {
		return req, nil, nil
	}

	// Skip governance for codemode tools
	if bifrost.IsCodemodeTool(toolName) {
		return req, nil, nil
	}

	// Validate required headers are present
	if headerErr := p.validateRequiredHeaders(ctx); headerErr != nil {
		return req, &schemas.MCPPluginShortCircuit{Error: headerErr}, nil
	}

	// Extract governance headers and virtual key using utility functions
	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	// Extract user ID for enterprise user-level governance
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)

	// Create request context for evaluation (MCP requests don't have provider/model)
	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKeyValue,
		UserID:     userID,
	}

	// Evaluate governance using common function
	_, bifrostError := p.EvaluateGovernanceRequest(ctx, evaluationRequest, schemas.MCPToolExecutionRequest)

	// Convert BifrostError to MCPPluginShortCircuit if needed
	if bifrostError != nil {
		return req, &schemas.MCPPluginShortCircuit{
			Error: bifrostError,
		}, nil
	}

	// Blind single-tool check: validate the specific tool being executed against VK MCPConfigs.
	// This runs independently of EvaluateGovernanceRequest to enforce execution-time allow-list.
	if virtualKeyValue != "" {
		vk, ok := p.store.GetVirtualKey(ctx, virtualKeyValue)
		if !ok || vk == nil {
			// VK became invalid after initial check - fail closed for security
			ctx.SetValue(governanceRejectedContextKey, true)
			return req, &schemas.MCPPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionVirtualKeyNotFound)),
				StatusCode: bifrost.Ptr(403),
				Error: &schemas.ErrorField{
					Message: "Virtual key not found",
				},
			}}, nil
		}
		if !vk.IsActiveValue() {
			ctx.SetValue(governanceRejectedContextKey, true)
			return req, &schemas.MCPPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionVirtualKeyBlocked)),
				StatusCode: bifrost.Ptr(403),
				Error: &schemas.ErrorField{
					Message: "Virtual key is inactive",
				},
			}}, nil
		}
		if vk.IsExpiredAt(time.Now().UTC()) {
			ctx.SetValue(governanceRejectedContextKey, true)
			return req, &schemas.MCPPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionVirtualKeyBlocked)),
				StatusCode: bifrost.Ptr(403),
				Error: &schemas.ErrorField{
					Message: "Virtual key has expired",
				},
			}}, nil
		}
		if !p.isMCPToolAllowedByVK(vk, toolName) {
			ctx.SetValue(governanceRejectedContextKey, true)
			return req, &schemas.MCPPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionMCPToolBlocked)),
				StatusCode: bifrost.Ptr(403),
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("MCP tool '%s' is not allowed for virtual key '%s'", toolName, vk.Name),
				},
			}}, nil
		}
		return req, nil, nil
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

	// Skip non tool-execute envelopes. The MCP gate stamps MCPRequestType on both
	// the success response (BifrostMCPResponse.ExtraFields) and the error
	// (BifrostError.ExtraFields), so a single check covers both paths.
	mcpReqType := schemas.MCPRequestType("")
	if resp != nil {
		mcpReqType = resp.ExtraFields.MCPRequestType
	} else if bifrostErr != nil {
		mcpReqType = bifrostErr.ExtraFields.MCPRequestType
	}
	if !mcpReqType.IsExecuteTool() {
		return resp, bifrostErr, nil
	}

	// Extract governance information
	virtualKey := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)

	if bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipVirtualKeyUsageTracking) {
		virtualKey = ""
	}

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

// PreMCPConnectionHook resolves the caller's identity onto the BifrostContext
// before the connect-plugin gate releases control to the credential-store
// resolver. This is the only point in the MCP connect lifecycle where we can
// turn the raw x-bf-vk header into the resolved VK row ID — anything later
// (PreMCPHook / PostMCPHook) runs after the resolver has already needed that
// row ID, and per-user auth types (per_user_oauth, per_user_headers) key
// their stored credentials by it.
//
// The hook is intentionally narrow: it ONLY populates the identity context
// keys (VK row ID, name, team / customer fan-out). Policy checks (budget,
// rate limit, tool allow-list) stay on PreMCPHook for the actual CallTool —
// Connect is transport setup, not the gated operation.
//
// No short-circuit returned even when the VK isn't recognized: bad-VK
// rejection belongs on the tool-call path so the caller gets a stable
// error format. An unknown VK here simply leaves the row ID empty, and the
// resolver will surface the "requires an identity" error itself.
func (p *GovernancePlugin) PreMCPConnectionHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, error) {
	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	if virtualKeyValue == "" {
		return req, nil, nil
	}
	vk, ok := p.store.GetVirtualKey(ctx, virtualKeyValue)
	if !ok || vk == nil {
		// Unknown VK — leave identity unset; the resolver will surface the
		// appropriate error on the per-user auth path. For shared-connection
		// auth types this is a no-op (they don't read these keys).
		return req, nil, nil
	}
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
	return req, nil, nil
}

// PostMCPConnectionHook is a pass-through; the identity resolution that
// PreMCPConnectionHook performs is observation-only and has no post-connect
// cleanup. Implementing this satisfies MCPConnectionPlugin so the typed
// PreMCPConnectionHook is dispatched by the plugin pipeline.
func (p *GovernancePlugin) PostMCPConnectionHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError, error) {
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
//   - virtualKey: The raw virtual key token of the request (empty string if not present)
//   - selectedKeyID: The selected provider key ID used for scoped pricing overrides
//   - requestID: The request ID
//   - userID: The user ID for enterprise user-level governance (empty string if not present)
//   - isCacheRead: Whether the request is a cache read
//   - isBatch: Whether the request is a batch request
//   - isFinalChunk: Whether the request is the final chunk
//   - pricingScopes: Prebuilt pricing lookup scopes using governance VK ID (nil if not applicable)
func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, provider schemas.ModelProvider, model string, requestType schemas.RequestType, virtualKey, requestID, userID string, isFinalChunk bool, attemptNumber int, pricingScopes *modelcatalog.PricingLookupScopes) {
	// Determine if request was successful
	success := (result != nil)
	billedReason := "success"

	// Streaming detection
	isStreaming := bifrost.IsStreamRequestType(requestType)

	if !isStreaming || (isStreaming && isFinalChunk) {
		var cost float64
		if p.modelCatalog != nil && result != nil {
			cost = p.modelCatalog.CalculateCost(result, pricingScopes)
		}
		tokensUsed := 0
		// The request failed/was cancelled but the provider still
		// processed tokens (carried on BifrostError.ExtraFields.BilledUsage).
		// Bill those tokens — Anthropic charges us for them regardless.
		if result == nil && bifrostErr != nil && bifrostErr.ExtraFields.BilledUsage != nil {
			billedUsage := bifrostErr.ExtraFields.BilledUsage
			tokensUsed = billedUsage.TotalTokens
			billedReason = "partial_usage_on_error"
			if p.modelCatalog != nil {
				cost = p.modelCatalog.CalculateCostForUsage(billedUsage, provider, model, requestType, pricingScopes)
			}
		}
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
			case result.PassthroughResponse != nil:
				if su := result.PassthroughResponse.PassthroughUsage; su != nil && su.LLMUsage != nil {
					tokensUsed = su.LLMUsage.TotalTokens
				}
			}
		}

		// Create usage update for tracker (business logic)
		usageUpdate := &UsageUpdate{
			VirtualKey:    virtualKey,
			Provider:      provider,
			Model:         model,
			Success:       success,
			TokensUsed:    int64(tokensUsed),
			Cost:          cost,
			RequestID:     requestID,
			UserID:        userID,
			IsStreaming:   isStreaming,
			IsFinalChunk:  isFinalChunk,
			HasUsageData:  tokensUsed > 0 || cost > 0,
			AttemptNumber: attemptNumber,
			BilledReason:  billedReason,
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
