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
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

const (
	governanceRejectedContextKey schemas.BifrostContextKey = "bf-governance-rejected"

	VirtualKeyPrefix = "sk-bf-"
)

// Config is the configuration for the governance plugin
type Config struct {
	IsVkMandatory         *bool     `json:"is_vk_mandatory"`
	RequiredHeaders       *[]string `json:"required_headers"` // Pointer to live config slice; changes are reflected immediately without restart
	IsEnterprise          bool      `json:"is_enterprise"`
	DisableAutoToolInject *bool     `json:"disable_auto_tool_inject"`
}

type InMemoryStore interface {
	GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig
	GetMCPClientsAllowingAllVirtualKeys() map[string]string // clientID → clientName
	GetMCPClientNames() map[string]string                   // clientID → clientName, every client
}

type BaseGovernancePlugin interface {
	GetName() string
	Evaluate(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest) (*EvaluationResult, *schemas.BifrostError)
	HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
	HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error
	PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)
	PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error)
	PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error)
	Cleanup() error
	GetGovernanceStore() GovernanceStore
	// Routing collaboration: the routing plugin calls these after evaluating routing rules,
	// so the allowlist and the load balancer both act on the post-rule provider/model.
	// ResolveEffectiveAccess answers what a request may reach, without recording anything on it.
	// Listing surfaces that run outside the request pipeline ask for it directly. It takes the
	// request context concretely because resolution reads request-scoped values off it, which a
	// plain context.Context carrying the same request cannot provide.
	ResolveEffectiveAccess(ctx *schemas.BifrostContext) *grants.EffectiveAccess
	GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus
	PublishRoutingAllowlist(ctx *schemas.BifrostContext, modelStr string)
	LoadBalanceProvider(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error
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
	if config != nil {
		isVkMandatory = config.IsVkMandatory
		requiredHeaders = config.RequiredHeaders
		disableAutoToolInject = config.DisableAutoToolInject
	}

	newStoreStart := time.Now()
	governanceStore, err := NewLocalGovernanceStore(ctx, logger, configStore, governanceConfig, modelCatalog)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize governance store: %w", err)
	}
	logger.Info("[startup-timing] NewLocalGovernanceStore took %v", time.Since(newStoreStart))
	// The store answers what grants a request carries, so it needs the same view of
	// open-to-every-key MCP clients that the tool-list stamping uses.
	governanceStore.inMemoryStore = inMemoryStore

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

	ctx, cancelFunc := context.WithCancel(ctx)
	plugin := &GovernancePlugin{
		ctx:                   ctx,
		cancelFunc:            cancelFunc,
		store:                 governanceStore,
		resolver:              resolver,
		tracker:               tracker,
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
	if config != nil {
		isVkMandatory = config.IsVkMandatory
		requiredHeaders = config.RequiredHeaders
		disableAutoToolInject = config.DisableAutoToolInject
	}
	resolver := NewBudgetResolver(governanceStore, modelCatalog, logger, inMemoryStore)
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
		ctx:                   ctx,
		cancelFunc:            cancelFunc,
		store:                 governanceStore,
		resolver:              resolver,
		tracker:               tracker,
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
	return plugin, nil
}

// GetName returns the name of the plugin
func (p *GovernancePlugin) GetName() string {
	return PluginName
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

// HTTPTransportPostHook intercepts requests after they are processed (governance decision point)
// It modifies the response in-place and returns nil to continue
func (p *GovernancePlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *GovernancePlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// GetBudgetAndRateLimitStatus reports how close a request is to the limits it answers to, for a provider
// and model pair. Exposed so routing rules can test budget_used, tokens_used and request counts.
//
// It reads the request's access off the context, rather than taking either that or the credential it
// presented: what governs a request is what granted it, and a rule asking "how much room is left" wants
// the answer for every holder paying — which for a caller granted access by something other than a key is
// not answerable from a key at all. Nothing is passed, so nothing can be passed wrongly.
func (p *GovernancePlugin) GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *BudgetAndRateLimitStatus {
	return p.store.GetBudgetAndRateLimitStatus(ctx, provider, model, budgetBaselines, tokenBaselines, requestBaselines)
}

// LoadBalanceProvider picks a weighted provider among those the request may reach for req.Model
// and mutates req.Provider/req.Model with the refined provider/model. Also populates req.Fallbacks
// from the remaining weighted providers if no fallbacks were configured by the caller.
//
// Candidacy comes from the request's grants, so a request that holds providers without presenting
// a key is balanced across them too. Everything it needs comes off the request: the grants say
// which providers may serve it, and the store says which of those can be paid for.
func (p *GovernancePlugin) LoadBalanceProvider(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	provider, modelStr, existingFallbacks := req.GetRequestFields()
	if modelStr == "" {
		return nil
	}

	if provider != "" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Skipping load balancing for model %s: provider %s already set", modelStr, provider))
		return nil
	}

	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Load balancing provider for model %s", modelStr))

	access := p.ensureEffectiveAccess(ctx)
	if access == nil {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelWarn, "This request carries no access to balance across, skipping load balancing")
		return nil
	}

	// Every provider this request may use at all, before narrowing to the ones that serve
	// this model. Reported as-is because it is what makes the exclusions below readable.
	configuredProviders := access.GrantedProvidersFor("")
	if len(configuredProviders) == 0 {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelWarn, fmt.Sprintf("No providers are available to this request for model %s, skipping load balancing", modelStr))
		return nil
	}
	p.logger.Debug("[Governance] Request may use %d providers: %v", len(configuredProviders), configuredProviders)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Load balancing model %s across %d available providers: %v", modelStr, len(configuredProviders), configuredProviders))

	// Candidates are the provider configs that permit this model — model allowance, blacklists
	// and composition already applied. Everything below is about which of them may serve the
	// request right now, and which one gets it.
	candidates := access.ProvidersFor(modelStr)
	serving := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		serving[candidate.Provider] = struct{}{}
	}
	for _, availableProvider := range configuredProviders {
		if _, ok := serving[string(availableProvider)]; !ok {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Provider %s excluded: model %s is not permitted", availableProvider, modelStr))
		}
	}

	eligible := make([]grants.ProviderCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		// Whether a candidate can be afforded right now is the store's answer, because the store
		// owns what pays for it. Load balancing passes the candidate and does not need to know
		// whether a key, a team or something else is funding it.
		if decision, err := p.store.CheckProviderCandidateExclusion(ctx, candidate); err != nil || decision != DecisionAllow {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo,
				fmt.Sprintf("Provider %s excluded: %s", candidate.Provider, candidateExclusionReason(decision, err)))
			continue
		}
		eligible = append(eligible, candidate)
	}

	var eligibleProviders []string
	for _, candidate := range eligible {
		eligibleProviders = append(eligibleProviders, candidate.Provider)
	}
	p.logger.Debug("[Governance] Allowed providers after filtering: %v", eligibleProviders)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Allowed providers after filtering: %v", eligibleProviders))

	if len(eligible) == 0 {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("No eligible providers remaining after filtering for model %s, skipping load balancing", modelStr))
		// TODO: Send proper error if (overall VK budget/rate limit) or (all provider budgets/rate limits) are violated
		return nil
	}

	weighted := make([]grants.ProviderCandidate, 0, len(eligible))
	for _, candidate := range eligible {
		if candidate.Weight != nil {
			weighted = append(weighted, candidate)
		}
	}

	if len(weighted) == 0 {
		// Everything survived the model-allowance / budget / rate-limit filters, but none of it
		// carries a weight — there is nothing to feed weighted selection. Say so explicitly, so
		// the routing trail explains why governance stops here instead of trailing off after
		// "Allowed providers after filtering: [...]".
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("No weighted providers for model %s — none of the allowed providers have a weight assigned; skipping load balancing", modelStr))
		return nil
	}

	var selectedProvider schemas.ModelProvider
	totalWeight := 0.0
	for _, candidate := range weighted {
		totalWeight += getWeight(candidate.Weight)
	}
	// Generate random number between 0 and totalWeight
	randomValue := rand.Float64() * totalWeight
	// Select provider based on weighted random selection
	currentWeight := 0.0
	for _, candidate := range weighted {
		currentWeight += getWeight(candidate.Weight)
		if randomValue <= currentWeight {
			selectedProvider = schemas.ModelProvider(candidate.Provider)
			break
		}
	}
	// Fallback: if no provider was selected (shouldn't happen but guard against FP issues)
	if selectedProvider == "" {
		selectedProvider = schemas.ModelProvider(weighted[0].Provider)
	}

	p.logger.Debug("[governance] Selected provider: %s", selectedProvider)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineGovernance, schemas.LogLevelInfo, fmt.Sprintf("Selected provider %s for model %s (from %d eligible: %v)", selectedProvider, modelStr, len(eligible), eligibleProviders))

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

	if len(existingFallbacks) == 0 && len(weighted) > 1 {
		fallbackCandidates := append([]grants.ProviderCandidate(nil), weighted...)
		sort.Slice(fallbackCandidates, func(i, j int) bool {
			return getWeight(fallbackCandidates[i].Weight) > getWeight(fallbackCandidates[j].Weight)
		})

		// Filter out the selected provider and create fallbacks array
		fallbacks := make([]schemas.Fallback, 0, len(fallbackCandidates)-1)
		for _, candidate := range fallbackCandidates {
			if candidate.Provider == string(selectedProvider) {
				continue
			}
			fbProvider := schemas.ModelProvider(candidate.Provider)
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

// candidateExclusionReason is what a routing log says about a candidate governance would not fund.
//
// Routing logs are read by whoever made the request, so this says what happened in plain words rather
// than naming the decision that produced it. The store answers with a Decision because that is what its
// other checks answer with; turning one into a sentence belongs here, with the log.
func candidateExclusionReason(decision Decision, err error) string {
	switch {
	case isRateLimitViolation(decision):
		return "rate limit violated"
	case isBudgetViolation(decision):
		return "budget limit violated"
	case err != nil:
		return err.Error()
	default:
		return "governance would not fund it"
	}
}

// PublishRoutingAllowlist records, for downstream routing layers, which of the providers the
// request may reach permit modelStr. It is a coarse provider gate
// (BifrostContextKeyRoutingAllowedProviders) layered on top of the model catalog checks those
// layers already run — its purpose is to stop a later routing layer (load balancing,
// model-catalog resolution) from selecting a provider the request may not use for this model,
// even when governance itself couldn't pick one. An empty slice means "no provider is permitted"
// (fail-closed via the empty-provider validation in handleRequest); a request carrying no grants
// publishes nothing.
//
// The virtual key is not consulted: what the request may reach decides the allowlist, so a
// request holding grants without presenting a key is narrowed the same way one with a key is.
//
// Provider prefixes on the request model are already split into req.Provider + bare model at the
// HTTP layer (resolveModelAndProvider), so allowed and blocked model names are matched against
// bare names and plain membership checks are sufficient here.
func (p *GovernancePlugin) PublishRoutingAllowlist(ctx *schemas.BifrostContext, modelStr string) {
	access := p.ensureEffectiveAccess(ctx)
	if access == nil || access.IsUnrestricted() {
		// Publishing nothing is not the same as publishing an empty list: an empty list means no
		// provider is permitted, and would fail-close a request that nothing has narrowed. Access that
		// permits everything narrows to nothing, so it has no allowlist to publish either.
		return
	}
	ctx.SetValue(schemas.BifrostContextKeyRoutingAllowedProviders, access.GrantedProvidersFor(modelStr))
}

// presentedGrantBearingCredential reports whether the request carried a credential that should have
// produced a grant. Two do: a virtual key, and the identity a caller authenticated as. Each names an
// entity whose access is configured somewhere, so failing to find that entity is a failed
// authentication rather than an absent one.
//
// Both, not just the key, because a deployment may resolve grants from either. Reading only the key
// would refuse a caller who authenticated as themselves for not holding a key, and would let one whose
// configured access has gone missing proceed as though they had presented nothing — which is
// unrestricted.
//
// It asks what the request carried, never what that resolved to. Both callers need it for that reason:
// a request presenting nothing resolves to unrestricted access, so a caller reading the resolved answer
// would find an access and conclude something was presented.
//
// A direct key is not one of them, though it is a credential the request carried. It is a raw provider
// key supplied to bypass the configured key pool, so nothing in the governance model describes it and
// nothing could have resolved a grant for it. Counting it here would refuse every direct-key request for
// lacking an access it was never meant to have. It still answers the mandatory-auth question — something
// was presented — which is why that step asks about it separately.
func presentedGrantBearingCredential(ctx *schemas.BifrostContext) bool {
	return bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey) != "" ||
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID) != ""
}

// Evaluate is the governance verdict for a request: whether it may proceed, and why not when it
// may not. It is the one entry point — every hook and every caller outside the pipeline arrives
// here, which is also why it resolves what the request may reach if nothing has yet.
//
// The steps it runs are the plugin's own and stay private: a caller asks for the verdict, not for
// one level of it. Their order is load bearing and documented below.
//
// Parameters:
//   - ctx: The Bifrost context
//   - evaluationRequest: The evaluation request with VirtualKey, Provider, Model, and RequestID
//
// Returns:
//   - *EvaluationResult: The governance evaluation result
//   - *schemas.BifrostError: The error to return if request is not allowed, nil if allowed
func (p *GovernancePlugin) Evaluate(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest) (*EvaluationResult, *schemas.BifrostError) {
	// Evaluation reads what a request may reach rather than working it out, and not every caller
	// arrives through a hook that has already worked it out.
	p.ensureEffectiveAccess(ctx)

	// This request now has the provider and model it will use, so the limits it answers to can be
	// settled and recorded on its access. Doing it here rather than in a hook covers every path:
	// the streaming, realtime-turn and MCP pipelines run no request hook, and this is the funnel
	// they all pass through. Everything downstream — the checks below, the co-payers named for the
	// log — then reads one resolved set instead of assembling its own.
	ea := resolveLimits(ctx, p.store, evaluationRequest.Provider, evaluationRequest.Model)

	// The order of everything below is load bearing:
	//
	//   key identity -> is authentication even present -> access -> limits (key, provider/model,
	//   customer, team, user)
	//
	// Identity is FIRST because a key that may not be used is refused as a key, before anything
	// else speaks for it. Putting any limit ahead of it would answer a revoked key with somebody
	// else's exhausted budget — a 402 or 429 that tells the holder of a dead key which budgets
	// exist and were consulted.
	//
	// Access comes next, and runs for EVERY request, whether or not it presented a key. What a
	// request may reach is a property of the grants it carries and the store decides what those
	// are, so a request granted access by something other than a key is held to the same gates
	// rather than to a second implementation of them alongside. A request carrying no grants at
	// all is unrestricted here, which is what a key-less request has always been. It also runs
	// before any limit, so a caller reaching for something they were never granted is told that,
	// rather than that its budget ran out.
	//
	// Limits come last, in the order they can refuse: the deployment's own provider and model
	// limits, then the key's, then what the key rolls up into (customer, team), then the user.

	// Step 1: was any authentication presented at all? A credential that was presented and turned out to
	// grant nothing is a different answer, reported by the identity step below — telling a caller who
	// supplied a key to supply one is not a useful refusal.
	p.cfgMutex.RLock()
	if !presentedGrantBearingCredential(ctx) && !hasDirectKeyAuth(ctx) && p.isVkMandatory != nil && *p.isVkMandatory {
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

	// Step 2: the access itself — may it be used at all? Whether a credential is real, active and
	// unexpired is settled when the grant is created, so this reads the answer off the grant rather
	// than resolving a key again. A caller that presented a credential no grant could be built for
	// is refused here: no grant means nothing authorised the request, which is a different answer
	// from having presented nothing.
	base := ea.Base()
	if base == nil {
		if presentedGrantBearingCredential(ctx) {
			return p.decide(ctx, &EvaluationResult{
				Decision: DecisionAccessNotFound,
				Reason:   "access not found. The provided credential does not exist or has been revoked.",
			})
		}
	} else {
		if !base.IsActive {
			return p.decide(ctx, &EvaluationResult{
				Decision: DecisionAccessBlocked,
				Reason:   fmt.Sprintf("%s is inactive", base.Type.PrettyString()),
			})
		}
		if base.IsExpired {
			return p.decide(ctx, &EvaluationResult{
				Decision: DecisionAccessBlocked,
				Reason:   fmt.Sprintf("%s has expired", base.Type.PrettyString()),
			})
		}
	}

	// Step 3: what the request may reach.
	result := p.resolver.evaluateAccess(ctx, evaluationRequest, ea)

	// Step 4: what the request can afford. One check over every limit covering this attempt — the
	// deployment's provider limits, the model configs that apply, and whatever the grant is funded
	// by — so no step here knows what kind of holder is paying.
	if result.Decision == DecisionAllow {
		result = p.resolver.evaluateLimits(ctx, evaluationRequest, ea)
	}

	// Check the actual MCP tools injected into the request against the VK MCPConfigs.
	// BifrostContextKeyMCPAddedTools is populated by AddToolsToRequest (which runs before
	// PreLLMHook), so it contains the real expanded tool names (e.g. "youtube-search") rather
	// than raw header patterns (e.g. "youtube-*"), giving us exact per-tool validation.
	if result.Decision == DecisionAllow && ea != nil {
		if addedTools, ok := ctx.Value(schemas.BifrostContextKeyMCPAddedTools).([]string); ok && len(addedTools) > 0 {
			access := grants.EffectiveAccessFromContext(ctx)
			var disallowed []string
			for _, tool := range addedTools {
				if !access.IsMCPToolAllowed(tool) {
					disallowed = append(disallowed, tool)
				}
			}
			if len(disallowed) > 0 {
				result = &EvaluationResult{
					Decision: DecisionMCPToolBlocked,
					Reason:   denialReason(fmt.Sprintf("MCP tools not allowed: %s", strings.Join(disallowed, ", ")), ea.Base()),
				}
			}
		}
	}

	return p.decide(ctx, result)
}

// decide turns a governance decision into what the caller gets back: the result, and the error to
// refuse the request with when it was not allowed. Every step of Evaluate ends here, so a refusal
// is marked on the request and mapped to a status in one place regardless of which step refused.
func (p *GovernancePlugin) decide(ctx *schemas.BifrostContext, result *EvaluationResult) (*EvaluationResult, *schemas.BifrostError) {
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

	case DecisionAccessNotFound:
		// The credential itself did not resolve, so this is a failure to authenticate rather than
		// a permission the caller lacks.
		return result, &schemas.BifrostError{
			Type:       new(string(result.Decision)),
			StatusCode: new(401),
			Error: &schemas.ErrorField{
				Message: result.Reason,
			},
		}

	case DecisionAccessBlocked, DecisionModelBlocked, DecisionProviderBlocked:
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

// ensureEffectiveAccess resolves what a request may reach and records it on the request, once per
// attempt. Later calls within the attempt find the answer already recorded and return it, so calling
// it wherever a request is first seen costs one map read on all but the first.
//
// An attempt, not a request: core clears the recorded answer before it fails over, so the attempt
// that runs next resolves against configuration as it stands then rather than inheriting what the
// previous attempt was admitted under.
//
// Resolving is the store's answer to what grants the request carries, folded under one composition
// mode. Because the store decides that, a deployment resolving grant holders beyond virtual keys
// answers for them here without this path changing.
//
// Two places call it. The request hook, because that is where a request's grant decisions start on
// the ordinary path. And evaluation, because it is the one funnel every evaluating caller passes
// through — including the streaming, realtime-turn and MCP pipelines, which never run the request
// hook, and callers that evaluate governance with no hook in front of them at all.
//
// A request that carries no grant at all resolves to nil rather than to access permitting nothing,
// and nothing is recorded for it: consumers keep the behavior they had before grants existed, and
// such a request stays indistinguishable from one nobody has resolved yet.
func (p *GovernancePlugin) ensureEffectiveAccess(ctx *schemas.BifrostContext) *grants.EffectiveAccess {
	if ctx == nil {
		return nil
	}
	if access := grants.EffectiveAccessFromContext(ctx); access != nil {
		return access
	}

	base, scoping, mode := p.store.GetGrantSet(ctx)
	if base == nil && scoping == nil {
		return nil
	}

	access := grants.NewEffectiveAccess(base, scoping, mode, p.modelCatalog, p.inMemoryStore)
	grants.RecordEffectiveAccess(ctx, access)
	return access
}

// resolveLimits settles the limits a request answers to, now that the provider and model it will use are
// known, and records them on its access for everything downstream.
//
// Until this runs, only the limits that can tell one provider from another are known: each provider
// config's own, and the deployment's for that provider. That is what load balancing needs and all it
// needs. What funds a holder across every provider, and what a model costs, are settled here.
//
// Assembly is the store's — see GatherLimits — because what funds a holder is the store's to know. This
// settles the answer onto the request, which is what makes it one list: what is checked, what is named
// as a co-payer on the log row, and what is charged all read it, so they cannot disagree about which
// limits a request was subject to.
//
// A request carrying no grant has no access to settle them on and gets none: it is still subject to the
// deployment's own limits, which the check gathers for itself in that case. Recording an access for it
// would make a request nobody granted anything look like one granted nothing.
func resolveLimits(ctx *schemas.BifrostContext, store GovernanceStore, provider schemas.ModelProvider, model string) *grants.EffectiveAccess {
	access := grants.EffectiveAccessFromContext(ctx)
	if access == nil {
		return nil
	}

	budgets, rateLimits := store.GatherLimits(ctx, access, provider, model)
	access = access.WithResolvedLimits(budgets, rateLimits)
	grants.RecordEffectiveAccess(ctx, access)
	return access
}

// PreRequestHook is the per-request governance phase: it resolves the request's virtual key,
// stamps the key's scope on ctx for downstream plugins, and narrows the MCP tool list to what
// the key grants.
//
// It deliberately runs before the routing plugin so a routing rule evaluates against a fully
// stamped context. The provider allowlist and load balancing that used to live here now run
// from the routing plugin after rule evaluation, through PublishRoutingAllowlist and
// LoadBalanceProvider, because both must act on the post-rule model.
//
// Realtime + generic streaming bypass handleRequest (see core/bifrost.go
// RunRealtimeTurnPreHooks / RunStreamPreHooks) and are still handled at HTTPTransportPreHook.
func (p *GovernancePlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}

	// Resolve the request's access and record it before anything derives a grant decision from the
	// credential, so every consumer of this request reads the same answer instead of working out its own
	// — starting with the tool-list pruning just below. Resolving is also what stamps this request's
	// scope on ctx, so nothing here reads the key a second time to do it.
	access := p.ensureEffectiveAccess(ctx)

	// Whether the credential may be used at all is settled when its grant is built, so this reads the
	// answer off the grant. Asking the key again would answer a settled question from a second source.
	// Nothing is refused here — this hook only stamps and prunes; the funnel refuses.
	base := access.Base()
	if base == nil || !base.IsActive || base.IsExpired {
		return nil
	}

	// A caller-provided include-tools list can only narrow the virtual key's
	// tool grant, never expand it — prune entries the key does not allow.
	includeToolsProvided := p.pruneMCPIncludeToolsFromContext(ctx, access)

	p.cfgMutex.RLock()
	autoInjectDisabled := p.disableAutoToolInject != nil && *p.disableAutoToolInject
	p.cfgMutex.RUnlock()
	// An include-clients filter opts the request into tool injection even when
	// auto-injection is disabled (see ParseAndAddToolsToRequest in core/mcp), so
	// the key's allowlist must be stamped on every path where injection can run.
	includeClientsPresent := ctx.Value(schemas.MCPContextKeyIncludeClients) != nil
	if !includeToolsProvided && !access.IsUnrestricted() && (!autoInjectDisabled || includeClientsPresent) {
		// Unrestricted access is excluded because it enumerates no tools: stamping what it lists would
		// stamp an empty list, and an empty allowlist reads downstream as no tool being permitted. A grant
		// that lists nothing because it grants nothing is a different answer, and does belong on ctx.
		if tools := access.MCPIncludeList(); tools != nil {
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, tools)
		}
	}

	return nil
}

// pruneMCPIncludeToolsFromContext narrows a caller-provided include-tools list (stamped on ctx
// from the x-bf-mcp-include-tools header in lib/ctx.go) down to the tools the virtual key
// allows, and writes the pruned list back to ctx. Returns true when a caller list was present,
// regardless of how many entries survived. Entries the key does not grant are dropped; a
// "client-*" wildcard is kept only when the key itself is unrestricted for that client,
// otherwise it is replaced by the key's specific grants for that client (passing the wildcard
// through would read downstream as "all tools of this client").
func (p *GovernancePlugin) pruneMCPIncludeToolsFromContext(ctx *schemas.BifrostContext, access *grants.EffectiveAccess) bool {
	existing := ctx.Value(schemas.MCPContextKeyIncludeTools)
	if existing == nil {
		return false
	}
	requested, _ := existing.([]string)

	// Access that permits every tool has nothing to narrow to, and rewriting the list would drop the
	// wildcard patterns it cannot enumerate — leaving an empty list, which downstream reads as no tool
	// being permitted at all. The list is still the caller's, so this reports that one was provided.
	if access.IsUnrestricted() {
		return true
	}

	// The request's access was resolved once, so the wildcard checks and the per-tool checks
	// below cannot observe different states across a concurrent config reload.
	granted := access.MCPIncludeList()
	grantedSet := make(map[string]struct{}, len(granted))
	for _, tool := range granted {
		grantedSet[tool] = struct{}{}
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
			if _, ok := grantedSet[pattern]; ok {
				add(pattern)
				continue
			}
			for _, tool := range granted {
				if strings.HasPrefix(tool, clientName+"-") {
					add(tool)
				}
			}
			continue
		}
		if access.IsMCPToolAllowed(pattern) {
			add(pattern)
		}
	}

	ctx.SetValue(schemas.MCPContextKeyIncludeTools, pruned)
	return true
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
	// Getting provider and mode from the request
	provider, model, _ := req.GetRequestFields()
	// Create request context for evaluation
	evaluationRequest := &EvaluationRequest{
		RequestType: req.RequestType,
		Provider:    provider,
		Model:       model}
	// Evaluate governance using common function
	_, bifrostError := p.Evaluate(ctx, evaluationRequest)
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

	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	// What this request was admitted under, read rather than resolved: everything below reports on a call
	// that has already happened, and resolving now would answer for whatever configuration has become in
	// the meantime. Nothing here asks what credential was presented — the access is the answer to that.
	access := grants.EffectiveAccessFromContext(ctx)

	// A listing shows only the models the request may use. Which those are is entirely the access's
	// answer: unrestricted access lists everything, and a credential that resolved to nothing lists
	// nothing.
	if requestType == schemas.ListModelsRequest && result != nil && result.ListModelsResponse != nil {
		result.ListModelsResponse.Data = p.filterModelsForAccess(access, result.ListModelsResponse.Data)
	}

	// Build pricing scopes from context using the governance VK ID (not the raw VK token)
	pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(provider))

	// A caller can ask for the holder's usage not to be counted, leaving what the deployment and the user
	// answer to still counted.
	skipHolderUsage := bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipVirtualKeyUsageTracking)
	if requestedModel != "" {
		// Collect the affected budget and rate-limit IDs synchronously (fast in-memory
		// lookups) and attach them to the context. The logging plugin reads these keys
		// when building the log entry, enabling ghost-node usage reconciliation to
		// attribute cost/tokens to the correct governance entities.
		//
		// Read the limits settled onto the access, and only those. The access is the one record of what
		// this request was subject to, so what is billed and what is recorded are the same list the
		// check used. A request that reached here without being evaluated answers to nothing and is
		// billed nothing, which is the only consistent answer — charging what was never checked is the
		// divergence this exists to prevent.
		accountedBudgets, accountedRateLimits := access.ResolvedBudgets(), access.ResolvedRateLimits()
		if skipHolderUsage {
			// The holder's usage is not being counted, so nothing it funds is billed or recorded. What
			// the deployment and the user fund still is: those are owed whatever granted the request,
			// and dropping them would let a caller spend against them without limit.
			accountedBudgets = grants.LimitsFrom(accountedBudgets, untrackedHolderKinds...)
			accountedRateLimits = grants.LimitsFrom(accountedRateLimits, untrackedHolderKinds...)
		}
		if budgetIDs := limitIDsOf(accountedBudgets); len(budgetIDs) > 0 {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, budgetIDs)
		}
		if rateLimitIDs := limitIDsOf(accountedRateLimits); len(rateLimitIDs) > 0 {
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
			p.postHookWorker(result, err, provider, requestedModel, requestType, requestID, isFinalChunk, attemptNumber, pricingScopes, accountedBudgets, accountedRateLimits)
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

	// Create request context for evaluation (MCP requests don't have provider/model)
	evaluationRequest := &EvaluationRequest{
		RequestType: schemas.MCPToolExecutionRequest,
	}

	// Evaluate governance using common function
	_, bifrostError := p.Evaluate(ctx, evaluationRequest)

	// Convert BifrostError to MCPPluginShortCircuit if needed
	if bifrostError != nil {
		return req, &schemas.MCPPluginShortCircuit{
			Error: bifrostError,
		}, nil
	}

	// The one question evaluation cannot answer: this specific tool. Evaluation checks the tools already
	// injected into the request, which is not the tool being executed now, so the execution-time
	// allow-list is enforced here.
	//
	// Whether the caller may be here at all is not rechecked. Evaluation has already refused a credential
	// that resolves to nothing, one that is inactive and one that has expired, reading each off the grant
	// — asking the key again would answer the same question from a second source, and the two could
	// disagree. What is left is a question about the tool, which the access answers whatever granted it.
	access := grants.EffectiveAccessFromContext(ctx)
	if !access.IsMCPToolAllowed(toolName) {
		ctx.SetValue(governanceRejectedContextKey, true)
		return req, &schemas.MCPPluginShortCircuit{Error: &schemas.BifrostError{
			Type:       bifrost.Ptr(string(DecisionMCPToolBlocked)),
			StatusCode: bifrost.Ptr(403),
			Error: &schemas.ErrorField{
				Message: denialReason(fmt.Sprintf("MCP tool '%s' is not allowed", toolName), access.Base()),
			},
		}}, nil
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

	requestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID)

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

	// Create usage update for tracker (business logic) - MCP requests track request count and tool cost.
	//
	// Tool execution names no provider and no model, so what it answers to is whatever funds the holder —
	// read from the access settled when the tool call was evaluated, the same list that admitted it.
	//
	// Not gated on a credential. Skipping accounting when no key was presented would mean a request the
	// deployment does charge for goes unbilled, and asking the key here would be a second answer to a
	// question the access already settled.
	access := grants.EffectiveAccessFromContext(ctx)
	budgets, rateLimits := access.ResolvedBudgets(), access.ResolvedRateLimits()
	if bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipVirtualKeyUsageTracking) {
		// The holder's usage is not being counted; what the deployment and the user answer to still is.
		budgets = grants.LimitsFrom(budgets, untrackedHolderKinds...)
		rateLimits = grants.LimitsFrom(rateLimits, untrackedHolderKinds...)
	}
	usageUpdate := &UsageUpdate{
		Success:      success,
		Cost:         toolCost,
		RequestID:    requestID,
		IsStreaming:  false,
		IsFinalChunk: true,
		HasUsageData: toolCost > 0, // Has usage data if we have a cost
		Budgets:      budgets,
		RateLimits:   rateLimits,
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
// The hook is intentionally narrow: it decides nothing. Resolving the access records the identity
// context keys (row ID, name, team / customer fan-out) and stops there. Policy checks (budget, rate
// limit, tool allow-list) stay on PreMCPHook for the actual CallTool — Connect is transport setup, not
// the gated operation.
//
// No short-circuit returned even when the VK isn't recognized: bad-VK
// rejection belongs on the tool-call path so the caller gets a stable
// error format. An unknown VK here simply leaves the row ID empty, and the
// resolver will surface the "requires an identity" error itself.
func (p *GovernancePlugin) PreMCPConnectionHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, error) {
	// Resolving the request's access is what stamps its scope on ctx, so the row ID the credential
	// resolver needs arrives as a side effect of asking the same question every other path asks. Doing it
	// here rather than reading the key directly is what keeps one answer to "whose request is this" — the
	// identity was previously derived a second time, from a second read of the key.
	//
	// A credential that resolves to nothing leaves the identity unset, as before: the resolver surfaces
	// the error on the per-user auth path, and shared-connection auth types never read these keys.
	p.ensureEffectiveAccess(ctx)
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

// postHookWorker works out what a response cost and hands it to the tracker, off the request thread so
// billing never blocks a caller.
//
// budgets and rateLimits are the limits this attempt answers to, resolved before the call and passed in
// rather than looked up here: charging runs after the request has moved on, and by then the request may be
// on a later attempt whose limits are not the ones this usage was incurred under. Nothing else about who
// made the request reaches this far — what to charge is the whole of it.
//
// isFinalChunk and requestType decide whether there is anything to bill yet: a streaming response reports
// token deltas as it goes and its cost at the end.
func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, provider schemas.ModelProvider, model string, requestType schemas.RequestType, requestID string, isFinalChunk bool, attemptNumber int, pricingScopes *modelcatalog.PricingLookupScopes, budgets, rateLimits []grants.Limit) {
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
			Success:       success,
			TokensUsed:    int64(tokensUsed),
			Cost:          cost,
			RequestID:     requestID,
			IsStreaming:   isStreaming,
			IsFinalChunk:  isFinalChunk,
			HasUsageData:  tokensUsed > 0 || cost > 0,
			AttemptNumber: attemptNumber,
			BilledReason:  billedReason,
			Budgets:       budgets,
			RateLimits:    rateLimits,
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
