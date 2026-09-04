// Package routing provides the routing rule engine as a Bifrost plugin. It owns the rule
// set, the compiled CEL programs behind each rule, and the request-complexity analyzer that
// backs the complexity_tier variable.
//
// The plugin runs in PreRequestHook, after governance has resolved the request's virtual key
// and stamped its scope on the context, and it drives the rest of the routing pipeline from
// there: rules decide provider, model, fallback chain and key pin, and only then does the
// virtual key's provider allowlist get published and its providers load balanced. Requests
// with no rules configured pay a single map check before that materialization.
package routing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// PluginName is the name of the routing plugin
const PluginName = "routing"

// Governance is what routing needs from the governance plugin. The two reads feed rule
// evaluation; the two writes materialize the virtual key's provider choice for the model the
// rules settled on, and run from the routing hook so they see post-rule values.
//
// It is satisfied by the registered governance plugin rather than by its store, so a
// deployment that swaps in its own governance implementation supplies its own load balancing
// through this same call.
type Governance interface {
	rules.GovernanceStore
	// ResolveAccess answers what the request may reach, and is what routing reads a request's
	// governance scope from: the identifiers it needs are stamped when the access is resolved.
	ResolveAccess(ctx *schemas.BifrostContext) (schemas.Access, error)
	PublishRoutingAllowlist(ctx *schemas.BifrostContext, modelStr string)
	LoadBalanceProvider(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error
}

const noSemanticClassifierLog = "Complexity analysis skipped: no embedding model is configured for the complexity router; continuing with existing routing path"

// Config is the configuration for the routing plugin
type Config struct {
	// ChainMaxDepth caps how many times a chain_rule may re-enter evaluation.
	// Pointer to live config value; changes are reflected immediately without restart.
	ChainMaxDepth *int `json:"routing_chain_max_depth"`
	// ComplexityAnalyzerConfig overrides the analyzer defaults. When nil, the persisted
	// config is used, falling back to the built-in defaults.
	ComplexityAnalyzerConfig *complexity.AnalyzerConfig `json:"complexity_analyzer_config,omitempty"`
	// KVStore is the runtime-only shared store used for complexity
	// session state. It is injected by the host and is never serialized.
	KVStore schemas.KVStore `json:"-"`
}

// chainMaxDepthOrDefault resolves the configured chain depth, falling back to the default.
// The pointer is kept live so a config edit takes effect without a restart.
func (c *Config) chainMaxDepthOrDefault() *int {
	if c != nil && c.ChainMaxDepth != nil {
		return c.ChainMaxDepth
	}
	defaultDepth := rules.DefaultChainMaxDepth
	return &defaultDepth
}

// RoutingPlugin evaluates routing rules for every request.
type RoutingPlugin struct {
	rules              rules.Store
	engine             *rules.Engine
	complexityAnalyzer atomic.Pointer[complexity.ComplexityAnalyzer]
	semanticClassifier *complexity.SemanticClassifier
	llmClassifier      *complexity.LLMClassifier
	sessionStore       *complexitySessionStore
	sessionEnabled     atomic.Bool

	// governance supplies the virtual key, its live budget/rate-limit usage, and the provider
	// materialization that runs once rules have decided. Required: rules address budgets and
	// rate limits, and the scope chain is built from the virtual key hierarchy.
	governance  Governance
	configStore configstore.ConfigStore
	logger      schemas.Logger
	cleanupOnce sync.Once
	// generationsCancel stops the background registration and sweep that
	// reclaims exemplar generations no node is using; generationsWg lets
	// Cleanup wait for that loop to actually exit.
	generationsCancel context.CancelFunc
	generationsWg     sync.WaitGroup
	// generations answers what other nodes have claimed, so a manual deletion
	// can refuse a generation still in use elsewhere.
	generations *complexityGenerationRegistry

	// Wired by the HTTP server after the bifrost client exists; see
	// SetEmbeddingRequestExecutor / SetWarmupEmbedUsageObserver /
	// SetComplexityVectorStore in embedding.go.
	embeddingRequestExecutor atomic.Pointer[EmbeddingRequestExecutor]
	warmupEmbedUsageObserver atomic.Pointer[WarmupEmbedUsageObserver]
	complexityVectorStore    atomic.Pointer[vectorstore.VectorStore]

	// chatRequestExecutor is wired by the HTTP server after the bifrost client
	// exists (post-Init) via SetChatRequestExecutor, exactly like
	// embeddingRequestExecutor; the llm complexity classifier reads it on the
	// request hot path.
	chatRequestExecutor atomic.Pointer[ChatRequestExecutor]
	// responsesRequestExecutor is the Responses-API counterpart, wired the same
	// way via SetResponsesRequestExecutor. The llm classifier reads it only when
	// a chat completion is rejected because the judge model requires
	// /v1/responses; it stays nil until wired, in which case no fallback runs.
	responsesRequestExecutor atomic.Pointer[ResponsesRequestExecutor]
}

// Init initializes and returns a routing plugin instance.
//
// The rule cache is built internally, loaded from configStore.
//
// Parameters:
//   - ctx: base context, used for the initial rule load.
//   - config: plugin flags; may be nil, in which case defaults apply.
//   - logger: logger used by the engine and the rule store.
//   - configStore: configuration store rules are read from; may be nil.
//   - governancePlugin: virtual key state and provider materialization; must not be nil.
func Init(
	ctx context.Context,
	config *Config,
	logger schemas.Logger,
	configStore configstore.ConfigStore,
	governancePlugin Governance,
) (*RoutingPlugin, error) {
	ruleStore, err := rules.NewLocalStore(ctx, logger, configStore)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize routing rule store: %w", err)
	}
	return InitFromStore(ctx, config, logger, configStore, ruleStore, governancePlugin)
}

// InitFromStore initializes and returns a routing plugin instance with a custom rule store.
//
// Use this to supply a rule store implementation other than LocalRuleStore, for example one
// backed by a shared cache. Parameters are the same as Init, plus the rule store itself,
// which must not be nil.
func InitFromStore(
	ctx context.Context,
	config *Config,
	logger schemas.Logger,
	configStore configstore.ConfigStore,
	ruleStore rules.Store,
	governancePlugin Governance,
) (*RoutingPlugin, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if ruleStore == nil {
		return nil, fmt.Errorf("rule store cannot be nil")
	}

	chainMaxDepth := config.chainMaxDepthOrDefault()

	engine, err := rules.NewEngine(ruleStore, governancePlugin, logger, chainMaxDepth)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize routing engine: %w", err)
	}

	plugin := &RoutingPlugin{
		rules:              ruleStore,
		engine:             engine,
		governance:         governancePlugin,
		configStore:        configStore,
		logger:             logger,
		semanticClassifier: complexity.NewSemanticClassifier(ctx, logger),
		llmClassifier:      complexity.NewLLMClassifier(logger),
	}
	if config != nil && config.KVStore != nil {
		plugin.sessionStore = newComplexitySessionStore(config.KVStore, complexitySessionInactivityTTL)
		// Peers sharing a vector store otherwise embed the same phrases against
		// the same namespace at the same time, because a configuration change
		// reaches every node at once and the marker that would let a late node
		// skip the work is written last. Absent a KVStore this stays nil and each
		// node warms independently, exactly as before.
		if coordinator := newKVComplexityWarmCoordinator(config.KVStore, newRoutingNodeID()); coordinator != nil {
			plugin.semanticClassifier.SetWarmCoordinator(coordinator)
		}
	}
	// Remembering the measured embedding width is what lets a restart adopt an
	// already-complete generation without calling the provider at all. It is
	// persisted rather than held in the KVStore because it has to survive every
	// node restarting at once, which is exactly when nothing is left in memory
	// to ask.
	if dimensions := newPersistentEmbeddingDimensionStore(ctx, configStore, logger); dimensions != nil {
		plugin.semanticClassifier.SetEmbeddingDimensionStore(dimensions)
	}
	// Retired generations are reclaimed in the background. A node-local store
	// drops its own immediately, but on a shared one a peer may still be serving
	// a generation this node has retired, so nodes register what they use and
	// only unclaimed namespaces are collected.
	if registry := newComplexityGenerationRegistry(configStore, logger, newRoutingNodeID(), plugin); registry != nil {
		registryCtx, cancel := context.WithCancel(ctx)
		plugin.generationsCancel = cancel
		plugin.generations = registry
		// The classifier announces the namespace it is building through this,
		// so a peer's sweep can see a generation under construction rather than
		// only one already in service.
		plugin.semanticClassifier.SetGenerationClaimer(registry)
		plugin.generationsWg.Add(1)
		go func() {
			defer plugin.generationsWg.Done()
			registry.Run(registryCtx)
		}()
	}

	var analyzerOverride *complexity.AnalyzerConfig
	if config != nil {
		analyzerOverride = config.ComplexityAnalyzerConfig
	}
	if err := plugin.storeComplexityAnalyzerConfig(resolveAnalyzerConfigFromStoreOrArg(ctx, logger, configStore, analyzerOverride)); err != nil {
		_ = plugin.Cleanup()
		return nil, err
	}
	return plugin, nil
}

// GetName implements schemas.BasePlugin.
func (p *RoutingPlugin) GetName() string {
	return PluginName
}

// GetRuleStore returns the rule cache backing this plugin.
func (p *RoutingPlugin) GetRuleStore() rules.Store {
	return p.rules
}

// ReloadComplexityAnalyzerConfig swaps the analyzer used by complexity_tier routing.
func (p *RoutingPlugin) ReloadComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) error {
	return p.storeComplexityAnalyzerConfig(config)
}

func (p *RoutingPlugin) storeComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) error {
	resolved, err := complexity.ValidateAndNormalize(config)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("invalid complexity analyzer config, using defaults: %v", err)
		}
		defaults := complexity.DefaultAnalyzerConfig()
		resolved = &defaults
	}
	if resolved.SessionRoutingEnabled() && p.sessionStore == nil {
		return fmt.Errorf("complexity session routing requires a KV store")
	}
	p.complexityAnalyzer.Store(complexity.NewComplexityAnalyzerWithConfig(resolved))
	p.sessionEnabled.Store(resolved.SessionRoutingEnabled())
	if p.semanticClassifier != nil {
		p.semanticClassifier.Configure(resolved)
	}
	if p.llmClassifier != nil {
		p.llmClassifier.Configure(resolved)
	}
	return nil
}

// ComplexityLLMStatus returns the current llm classifier readiness.
func (p *RoutingPlugin) ComplexityLLMStatus() complexity.LLMStatusInfo {
	if p.llmClassifier == nil {
		return complexity.LLMStatusInfo{State: complexity.LLMStatusDisabled}
	}
	return p.llmClassifier.Status()
}

// RearmComplexitySemanticClassifier restarts semantic warmup after the given
// provider's own configuration changed (key re-enabled, model list widened).
// The classifier decides whether the change is worth acting on.
func (p *RoutingPlugin) RearmComplexitySemanticClassifier(provider schemas.ModelProvider) {
	if p.semanticClassifier != nil {
		p.semanticClassifier.RearmForProvider(provider)
	}
}

// ValidateComplexityAnalyzerConfig runs the semantic classifier's own
// validation before a handler persists a complexity configuration.
//
// Semantic configuration also requires the runtime classifier and embedding
// executor that will apply it. Keyword-only configuration remains valid without
// either dependency.
func (p *RoutingPlugin) ValidateComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) error {
	if config != nil && config.Semantic != nil && p.semanticClassifier == nil {
		return fmt.Errorf("semantic complexity classifier is unavailable")
	}
	if config != nil && config.Semantic != nil && p.embeddingExecutor() == nil {
		return fmt.Errorf("semantic complexity embedding executor is unavailable")
	}
	if p.semanticClassifier != nil {
		if err := p.semanticClassifier.ValidateConfig(config); err != nil {
			return err
		}
	}
	resolved, err := complexity.ValidateAndNormalize(config)
	if err != nil {
		return err
	}
	if resolved.SessionRoutingEnabled() && p.sessionStore == nil {
		return fmt.Errorf("complexity session routing requires a KV store")
	}
	return nil
}

// ComplexitySemanticStatus returns the current semantic classifier readiness.
func (p *RoutingPlugin) ComplexitySemanticStatus() complexity.SemanticStatusInfo {
	if p.semanticClassifier == nil {
		return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}
	}
	return p.semanticClassifier.Status()
}

// ListComplexityGenerations reports the exemplar generations held in the vector
// store, flagging the one currently serving.
func (p *RoutingPlugin) ListComplexityGenerations(ctx context.Context) ([]complexity.GenerationInfo, error) {
	if p.semanticClassifier == nil {
		return []complexity.GenerationInfo{}, nil
	}
	return p.semanticClassifier.ListGenerations(ctx)
}

// DeleteComplexityGeneration removes one retired exemplar generation.
func (p *RoutingPlugin) DeleteComplexityGeneration(ctx context.Context, namespace string) error {
	if p.semanticClassifier == nil {
		// Reporting success here would tell an operator a generation was removed
		// when nothing was even asked to remove it.
		return complexity.ErrClassifierUnavailable
	}
	// The classifier can only see its own state, so on its own it would happily
	// delete a generation another node is serving. The registry knows what every
	// node has claimed, and is asked the same question the sweep asks before it
	// reclaims anything.
	//
	// The sweep must not come through here: it has already answered this
	// question and still holds the registry lock, so re-asking would deadlock on
	// its own lease. It calls deleteComplexityGenerationUnchecked instead.
	if p.generations != nil {
		// One operation under the lifecycle lock: a claim arriving between a
		// separate check and delete would be recorded against a namespace that
		// is already gone.
		return p.generations.DeleteIfUnclaimed(ctx, namespace)
	}
	return p.deleteComplexityGenerationUnchecked(ctx, namespace)
}

// deleteComplexityGenerationUnchecked removes a generation without consulting
// the claims registry.
//
// It exists for the sweep, which reaches this while holding the registry lock
// and having just established that nothing claims the namespace. Re-checking
// there would block on a lease this node already owns, and every reclamation
// would fail. The classifier's own guards — the serving generation, in-flight
// requests, a warm in progress — still apply.
func (p *RoutingPlugin) deleteComplexityGenerationUnchecked(ctx context.Context, namespace string) error {
	if p.semanticClassifier == nil {
		return complexity.ErrClassifierUnavailable
	}
	return p.semanticClassifier.DeleteGeneration(ctx, namespace)
}

// PreRequestHook evaluates routing rules, then materializes the virtual key's provider choice
// for whatever provider/model the rules settled on.
//
// The order inside this hook is load bearing: a matched rule can rewrite provider, model and
// fallbacks, so the virtual key's provider allowlist and its load balancer must both see the
// post-rule values. Both of those belong to the governance plugin and are invoked from here
// rather than from governance's own hook, which runs earlier so that rules evaluate against a
// fully stamped context.
//
// It handles both normal body-having requests (routing on req.Model) and large-payload
// streaming requests (routing on LargePayloadMetadata.Model from ctx, since the body is
// opaque mid-stream).
func (p *RoutingPlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}

	scope, ok := p.resolveGovernanceScope(ctx)
	if !ok {
		return nil
	}

	// Large-payload mode: the body streams to the provider unparsed, so req.Model is
	// empty for routes where the model lives in the body (OpenAI/Anthropic chat,
	// responses, etc.). Route on LargePayloadMetadata.Model — the provider's
	// streaming body rewriter (ApplyLargePayloadRequestBodyWithModelNormalization)
	// reads metadata.Model when it rewrites the model field in the body prefix, so
	// mutating it here is what propagates the routing decision to the upstream call.
	if metadata, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMetadata).(*schemas.LargePayloadMetadata); metadata != nil && metadata.Model != "" {
		newModel, err := p.routeLargePayloadModel(ctx, scope, metadata.Model, req.RequestType)
		if err != nil {
			return err
		}
		if newModel != "" && newModel != metadata.Model {
			metadata.Model = newModel
		}
		return nil
	}

	if _, err := p.applyRoutingRules(ctx, req, scope); err != nil {
		return err
	}

	_, routedModel, _ := req.GetRequestFields()

	// Downstream routing layers (load balancing, model-catalog resolution) and core
	// enforcement intersect their candidates with this allowlist, so a later layer cannot
	// select a provider the request may not use for the routed model. A request that carries no
	// grants at all has nothing to allow or deny, and the call is a no-op.
	p.governance.PublishRoutingAllowlist(ctx, routedModel)
	return p.governance.LoadBalanceProvider(ctx, req)
}

// routeLargePayloadModel wraps a model string in a synthetic BifrostRequest, runs the same
// rule evaluation and virtual key materialization as the main PreRequestHook path, and returns the resolved
// model (provider-prefixed when a provider was selected, plain model otherwise). Used by the
// large-payload branch where req.Model is empty because the body wasn't parsed.
func (p *RoutingPlugin) routeLargePayloadModel(ctx *schemas.BifrostContext, scope rules.GovernanceScope, modelIn string, requestType schemas.RequestType) (string, error) {
	// Parse a provider-prefixed model string the same way the transport does for
	// body-having requests, so an explicit prefix like "openai/gpt-4o" lands in
	// ChatRequest.Provider and rule evaluation honors the caller's routing intent.
	providerIn, parsedModel := schemas.ParseModelString(modelIn, "")
	synthetic := &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{Provider: providerIn, Model: parsedModel},
	}

	if _, err := p.applyRoutingRules(ctx, synthetic, scope); err != nil {
		return modelIn, err
	}

	// Publish before load balancing, exactly as the body-having path does: the allowlist is
	// matched against the key's allowed/blacklisted model patterns, which are written against
	// caller-facing model names. Load balancing may replace the model with a provider-specific
	// one (RefineModelForProvider), and an allowlist computed from that refined name can come
	// back empty, which downstream layers read as "no provider permitted".
	_, routedModel, _ := synthetic.GetRequestFields()
	p.governance.PublishRoutingAllowlist(ctx, routedModel)

	if err := p.governance.LoadBalanceProvider(ctx, synthetic); err != nil {
		return modelIn, err
	}

	provider, model, _ := synthetic.GetRequestFields()
	if provider != "" {
		return string(provider) + "/" + model, nil
	}
	return model, nil
}

// applyRoutingRules evaluates routing rules against req and mutates
// req.Provider/req.Model/req.Fallbacks when a rule matches, returning the matched
// rules.Decision, or nil when no rule matched. A deployment with no rules configured pays a
// single map check here and falls through to the virtual key's own provider selection.
func (p *RoutingPlugin) applyRoutingRules(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, scope rules.GovernanceScope) (*rules.Decision, error) {
	if !p.rules.HasRules(ctx) {
		return nil, nil
	}

	provider, model, _ := req.GetRequestFields()
	if model == "" {
		return nil, nil
	}

	requestType := string(req.RequestType)
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	queryParams, _ := ctx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string)

	// Set up lazy complexity computation; only runs if a rule references complexity_tier.
	var computeComplexity func() *complexity.ComplexityResult
	if p.complexityAnalyzer.Load() != nil {
		computeComplexity = func() *complexity.ComplexityResult {
			return p.computeComplexity(ctx, req, scope.VirtualKeyID)
		}
	}

	routingCtx := &rules.EvaluationContext{
		Scope:                    scope,
		Provider:                 provider,
		Model:                    model,
		RequestType:              requestType,
		Headers:                  headers,
		QueryParams:              queryParams,
		BudgetAndRateLimitStatus: p.governance.GetBudgetAndRateLimitStatus(ctx, provider, model, nil, nil, nil),
		ComputeComplexity:        computeComplexity,
	}

	p.logger.Debug("[Routing] Built routing context: provider=%s, model=%s, requestType=%s, vk=%s",
		provider, model, requestType, scope.VirtualKeyID)

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

	p.logger.Debug("[Routing] Routing rule matched: %s", decision.MatchedRuleName)

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

	p.logger.Debug("[Routing] Applied routing decision: provider=%s, model=%s, keyID=%s, fallbacks=%v", decision.Provider, decision.Model, decision.KeyID, decision.Fallbacks)
	return decision, nil
}

// PreLLMHook implements schemas.LLMPlugin (no-op).
func (p *RoutingPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook stamps routing-classification telemetry onto the response.
// Routing's post hook runs before governance's (post hooks run in reverse
// pre-hook order), so the stamp is visible to governance cost calculation and
// every later post-hook consumer.
func (p *RoutingPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx != nil && resp != nil {
		if extraFields := resp.GetExtraFields(); extraFields != nil {
			stampRoutingMetadata(ctx, resp, extraFields.RequestType, bifrost.IsFinalChunk(ctx))
		}
	}
	return resp, bifrostErr, nil
}

// Cleanup implements schemas.BasePlugin.
func (p *RoutingPlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		if p.generationsCancel != nil {
			p.generationsCancel()
			p.generationsWg.Wait()
		}
		if p.semanticClassifier != nil {
			if err := p.semanticClassifier.Close(); err != nil {
				p.logger.Warn("[Routing] failed to close semantic classifier: %v", err)
			}
		}
		p.logger.Debug("[Routing] plugin cleaned up")
	})
	return nil
}
