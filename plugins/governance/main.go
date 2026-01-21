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
	PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)
	PostHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	Cleanup() error
	GetGovernanceStore() GovernanceStore
}

// GovernancePlugin implements the main governance plugin with hierarchical budget system
type GovernancePlugin struct {
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup // Track active goroutines

	// Core components with clear separation of concerns
	store    GovernanceStore // Pure data access layer
	resolver *BudgetResolver // Pure decision engine for hierarchical governance
	tracker  *UsageTracker   // Business logic owner (updates, resets, persistence)

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
//   - If `configStore` is nil, the plugin will use an in-memory LocalGovernanceStore
//     (no persistence). Init constructs a LocalGovernanceStore internally when
//     configStore is nil.
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
	inMemoryStore InMemoryStore,
) (*GovernancePlugin, error) {
	if configStore == nil {
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

	governanceStore, err := NewLocalGovernanceStore(ctx, logger, configStore, governanceConfig)
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
	ctx, cancelFunc := context.WithCancel(ctx)
	plugin := &GovernancePlugin{
		ctx:           ctx,
		cancelFunc:    cancelFunc,
		store:         governanceStore,
		resolver:      resolver,
		tracker:       tracker,
		configStore:   configStore,
		modelCatalog:  modelCatalog,
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
	inMemoryStore InMemoryStore,
) (*GovernancePlugin, error) {
	if configStore == nil {
		logger.Warn("governance plugin requires config store to persist data, running in memory only mode")
	}
	if modelCatalog == nil {
		logger.Warn("governance plugin requires model catalog to calculate cost, all cost calculations will be skipped.")
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
		configStore:   configStore,
		modelCatalog:  modelCatalog,
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

func parseVirtualKeyFromHTTPRequest(req *schemas.HTTPRequest) *string {
	var virtualKeyValue string
	vkHeader := req.CaseInsensitiveHeaderLookup("x-bf-vk")
	if vkHeader != "" {
		return bifrost.Ptr(vkHeader)
	}
	authHeader := req.CaseInsensitiveHeaderLookup("authorization")
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				virtualKeyValue = authHeaderValue
			}
		}
	}
	if virtualKeyValue != "" {
		return bifrost.Ptr(virtualKeyValue)
	}
	xAPIKey := req.CaseInsensitiveHeaderLookup("x-api-key")
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xAPIKey)
	}
	// Checking x-goog-api-key header
	xGoogleAPIKey := req.CaseInsensitiveHeaderLookup("x-goog-api-key")
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xGoogleAPIKey)
	}
	return nil
}

// HTTPTransportPreHook intercepts requests before they are processed (governance decision point)
// It modifies the request in-place and returns nil to continue, or an HTTPResponse to short-circuit.
func (p *GovernancePlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	virtualKeyValue := parseVirtualKeyFromHTTPRequest(req)
	if virtualKeyValue == nil {
		return nil, nil
	}
	// Get the virtual key from the store
	virtualKey, ok := p.store.GetVirtualKey(*virtualKeyValue)
	if !ok || virtualKey == nil || !virtualKey.IsActive {
		return nil, nil
	}
	headers, err := p.addMCPIncludeTools(nil, virtualKey)
	if err != nil {
		p.logger.Error("failed to add MCP include tools: %v", err)
		return nil, nil
	}
	for header, value := range headers {
		req.Headers[header] = value
	}
	if len(req.Body) == 0 {
		return nil, nil
	}
	var payload map[string]any
	err = sonic.Unmarshal(req.Body, &payload)
	if err != nil {
		p.logger.Error("failed to unmarshal request body to check for virtual key: %v", err)
		return nil, nil
	}
	payload, err = p.loadBalanceProvider(ctx, req, payload, virtualKey)
	if err != nil {
		return nil, err
	}
	body, err := sonic.Marshal(payload)
	if err != nil {
		p.logger.Error("failed to marshal request body to check for virtual key: %v", err)
		return nil, nil
	}
	req.Body = body
	return nil, nil
}

// HTTPTransportPostHook intercepts requests after they are processed (governance decision point)
// It modifies the response in-place and returns nil to continue
func (p *GovernancePlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
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
// Parameters:
//   - ctx: The Bifrost context
//   - req: The Bifrost request to be processed
//
// Returns:
//   - *schemas.BifrostRequest: The processed request
//   - *schemas.PluginShortCircuit: The plugin short circuit if the request is not allowed
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Extract governance headers and virtual key using utility functions
	virtualKeyValue := getStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := getStringFromContext(ctx, schemas.BifrostContextKeyRequestID)
	provider, model, _ := req.GetRequestFields()

	// Check if virtual key is mandatory when none is provided
	if virtualKeyValue == "" {
		if p.isVkMandatory != nil && *p.isVkMandatory {
			return req, &schemas.PluginShortCircuit{
				Error: &schemas.BifrostError{
					Type:       bifrost.Ptr("virtual_key_required"),
					StatusCode: bifrost.Ptr(401),
					Error: &schemas.ErrorField{
						Message: "virtual key is missing in headers and is mandatory.",
					},
				},
			}, nil
		}
	}

	// First evaluate model and provider checks (applies even when virtual keys are disabled or not present)
	result := p.resolver.EvaluateModelAndProviderRequest(ctx, provider, model, requestID)

	// If model/provider checks passed and virtual key exists, evaluate virtual key checks
	// This will overwrite the result with virtual key-specific decision
	if result.Decision == DecisionAllow && virtualKeyValue != "" {
		result = p.resolver.EvaluateVirtualKeyRequest(ctx, virtualKeyValue, provider, model, requestID)
	}
	// If model/provider checks failed, skip virtual key evaluation and proceed to final decision handling

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
// Parameters:
//   - ctx: The Bifrost context
//   - result: The Bifrost response to be processed
//   - err: The Bifrost error to be processed
//
// Returns:
//   - *schemas.BifrostResponse: The processed response
//   - *schemas.BifrostError: The processed error
//   - error: Any error that occurred during processing
func (p *GovernancePlugin) PostHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if _, ok := ctx.Value(governanceRejectedContextKey).(bool); ok {
		return result, err, nil
	}

	// Extract governance information
	virtualKey := getStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	requestID := getStringFromContext(ctx, schemas.BifrostContextKeyRequestID)

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
