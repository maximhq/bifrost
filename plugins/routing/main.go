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
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
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
	PublishRoutingAllowlist(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey, modelStr string)
	LoadBalanceProvider(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, virtualKey *configstoreTables.TableVirtualKey) error
	// AttributeRoutingEmbeddingCost bills one warmup/boot embedding of semantic
	// complexity routing to the admin-owned provider/model budgets. Governance
	// owns the model catalog and the budget ledger; routing only reports usage.
	AttributeRoutingEmbeddingCost(provider schemas.ModelProvider, model string, inputTokens int)
}

const noComplexitySignalLog = "Complexity analysis skipped: no configured complexity signal matched the latest user message; continuing with existing routing path"

// Config is the configuration for the routing plugin
type Config struct {
	// ChainMaxDepth caps how many times a chain_rule may re-enter evaluation.
	// Pointer to live config value; changes are reflected immediately without restart.
	ChainMaxDepth *int `json:"routing_chain_max_depth"`
	// ComplexityAnalyzerConfig overrides the analyzer defaults. When nil, the persisted
	// config is used, falling back to the built-in defaults.
	ComplexityAnalyzerConfig *complexity.AnalyzerConfig `json:"complexity_analyzer_config,omitempty"`
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

	// governance supplies the virtual key, its live budget/rate-limit usage, and the provider
	// materialization that runs once rules have decided. Required: rules address budgets and
	// rate limits, and the scope chain is built from the virtual key hierarchy.
	governance  Governance
	configStore configstore.ConfigStore
	logger      schemas.Logger
	cleanupOnce sync.Once

	// Wired by the HTTP server after the bifrost client exists; see
	// SetEmbeddingRequestExecutor / SetWarmupEmbedUsageObserver /
	// SetComplexityVectorStore in embedding.go.
	embeddingRequestExecutor atomic.Pointer[EmbeddingRequestExecutor]
	warmupEmbedUsageObserver atomic.Pointer[WarmupEmbedUsageObserver]
	complexityVectorStore    atomic.Pointer[vectorstore.VectorStore]
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
	}

	var analyzerOverride *complexity.AnalyzerConfig
	if config != nil {
		analyzerOverride = config.ComplexityAnalyzerConfig
	}
	plugin.storeComplexityAnalyzerConfig(resolveAnalyzerConfigFromStoreOrArg(ctx, logger, configStore, analyzerOverride))
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
func (p *RoutingPlugin) ReloadComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) {
	p.storeComplexityAnalyzerConfig(config)
}

func (p *RoutingPlugin) storeComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) {
	resolved, err := complexity.ValidateAndNormalize(config)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("invalid complexity analyzer config, using defaults: %v", err)
		}
		defaults := complexity.DefaultAnalyzerConfig()
		resolved = &defaults
	}
	p.complexityAnalyzer.Store(complexity.NewComplexityAnalyzerWithConfig(resolved))
	if p.semanticClassifier != nil {
		p.semanticClassifier.Configure(resolved)
	}
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
// This is schema validation only. It is routed through the classifier rather
// than calling complexity.ValidateAndNormalize directly so that a future
// setting whose validity depends on live process state has a seam to hook
// into; no semantic setting needs one today.
func (p *RoutingPlugin) ValidateComplexityAnalyzerConfig(config *complexity.AnalyzerConfig) error {
	if p.semanticClassifier == nil {
		return nil
	}
	return p.semanticClassifier.ValidateConfig(config)
}

// ComplexitySemanticStatus returns the current semantic classifier readiness.
func (p *RoutingPlugin) ComplexitySemanticStatus() complexity.SemanticStatusInfo {
	if p.semanticClassifier == nil {
		return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}
	}
	return p.semanticClassifier.Status()
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

	virtualKey, ok := p.resolveVirtualKey(ctx)
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
		newModel, err := p.routeLargePayloadModel(ctx, virtualKey, metadata.Model, req.RequestType)
		if err != nil {
			return err
		}
		if newModel != "" && newModel != metadata.Model {
			metadata.Model = newModel
		}
		return nil
	}

	if _, err := p.applyRoutingRules(ctx, req, virtualKey); err != nil {
		return err
	}

	_, routedModel, _ := req.GetRequestFields()

	// Downstream routing layers (load balancing, model-catalog resolution) and core
	// enforcement intersect their candidates with this allowlist, so a later layer cannot
	// select a provider the key forbids for the routed model. Without a virtual key there is
	// nothing to allow or deny and the call is a no-op.
	p.governance.PublishRoutingAllowlist(ctx, virtualKey, routedModel)
	return p.governance.LoadBalanceProvider(ctx, req, virtualKey)
}

// routeLargePayloadModel wraps a model string in a synthetic BifrostRequest, runs the same
// rule evaluation and virtual key materialization as the main PreRequestHook path, and returns the resolved
// model (provider-prefixed when a provider was selected, plain model otherwise). Used by the
// large-payload branch where req.Model is empty because the body wasn't parsed.
func (p *RoutingPlugin) routeLargePayloadModel(ctx *schemas.BifrostContext, virtualKey *configstoreTables.TableVirtualKey, modelIn string, requestType schemas.RequestType) (string, error) {
	// Parse a provider-prefixed model string the same way the transport does for
	// body-having requests, so an explicit prefix like "openai/gpt-4o" lands in
	// ChatRequest.Provider and rule evaluation honors the caller's routing intent.
	providerIn, parsedModel := schemas.ParseModelString(modelIn, "")
	synthetic := &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{Provider: providerIn, Model: parsedModel},
	}

	if _, err := p.applyRoutingRules(ctx, synthetic, virtualKey); err != nil {
		return modelIn, err
	}

	// Publish before load balancing, exactly as the body-having path does: the allowlist is
	// matched against the key's allowed/blacklisted model patterns, which are written against
	// caller-facing model names. Load balancing may replace the model with a provider-specific
	// one (RefineModelForProvider), and an allowlist computed from that refined name can come
	// back empty, which downstream layers read as "no provider permitted".
	_, routedModel, _ := synthetic.GetRequestFields()
	p.governance.PublishRoutingAllowlist(ctx, virtualKey, routedModel)

	if err := p.governance.LoadBalanceProvider(ctx, synthetic, virtualKey); err != nil {
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
func (p *RoutingPlugin) applyRoutingRules(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, virtualKey *configstoreTables.TableVirtualKey) (*rules.Decision, error) {
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
	if analyzer := p.complexityAnalyzer.Load(); analyzer != nil {
		computeComplexity = func() *complexity.ComplexityResult {
			input, ok := complexity.BuildInput(ctx, req)
			if !ok {
				if p.logger != nil {
					p.logger.Debug("[Routing] Complexity analysis skipped: unsupported request type")
				}
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, "Complexity analysis skipped: no supported text-bearing input detected")
				return nil
			}

			result := analyzer.Analyze(input)
			if result == nil {
				if p.logger != nil {
					p.logger.Debug("[Routing] %s", noComplexitySignalLog)
				}
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelDebug, noComplexitySignalLog)
				return nil
			}
			if p.logger != nil {
				p.logger.Debug(
					"[Routing] Complexity analysis details: tier=%s score=%.2f words=%d",
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

	routingCtx := &rules.EvaluationContext{
		VirtualKey:               virtualKey,
		UserID:                   bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID),
		Provider:                 provider,
		Model:                    model,
		RequestType:              requestType,
		Headers:                  headers,
		QueryParams:              queryParams,
		BudgetAndRateLimitStatus: p.governance.GetBudgetAndRateLimitStatus(ctx, model, provider, virtualKey, nil, nil, nil),
		ComputeComplexity:        computeComplexity,
	}

	p.logger.Debug("[Routing] Built routing context: provider=%s, model=%s, requestType=%s, vk=%v",
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
			stampRoutingDebug(ctx, resp, extraFields.RequestType, bifrost.IsFinalChunk(ctx))
		}
	}
	return resp, bifrostErr, nil
}

// Cleanup implements schemas.BasePlugin.
func (p *RoutingPlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		if p.semanticClassifier != nil {
			if err := p.semanticClassifier.Close(); err != nil {
				p.logger.Warn("[Routing] failed to close semantic classifier: %v", err)
			}
		}
		p.logger.Debug("[Routing] plugin cleaned up")
	})
	return nil
}
