package routing

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// modelRouterRuntime is one classifier as the plugin serves it: its config
// and the classifiers built from it.
//
// Every model router owns one. The Complexity Router page's config, which
// routing rules read complexity_tier from, is served by the plugin's own
// fields, the ones it had before routers existed; complexityRuntime views
// them as a runtime with no id, so one classification path serves both.
// Request capture and phrase suggestions are a model router's only: each
// captures its own classified requests (see complexitycapture.go).
type modelRouterRuntime struct {
	id             string // "" for the Complexity Router page's config
	name           *atomic.Pointer[string]
	config         *atomic.Pointer[complexity.AnalyzerConfig]
	targets        *atomic.Pointer[[]configstoreTables.TableModelRouterTarget]
	sessionEnabled *atomic.Bool
	semantic       *complexity.SemanticClassifier
	llm            *complexity.LLMClassifier
	capture        *complexity.RequestCapture // nil for the Complexity Router page's config, or when capture is not started
	shared         bool                       // a view over the plugin's own fields, closed by Cleanup
}

// modelRouterState is the state a model router owns.
type modelRouterState struct {
	name           atomic.Pointer[string]
	config         atomic.Pointer[complexity.AnalyzerConfig]
	targets        atomic.Pointer[[]configstoreTables.TableModelRouterTarget]
	sessionEnabled atomic.Bool
}

// logEngine is the engine a runtime's classification logs are tagged with:
// the model router engine for a router, and the routing rule engine for the
// Complexity Router page's config, which routing rules classify with.
func (rt *modelRouterRuntime) logEngine() string {
	if rt.id == "" {
		return schemas.RoutingEngineRoutingRule
	}
	return schemas.RoutingEngineModelRouter
}

// publishProposal publishes a classifier result and its routing log, as
// publishComplexityProposal does, tagged with the runtime's engine.
func (rt *modelRouterRuntime) publishProposal(ctx *schemas.BifrostContext, proposal complexityProposal) {
	publishComplexityDecision(ctx, proposal.Result, proposal.Mechanism, proposal.Score)
	if proposal.LogMessage != "" {
		ctx.AppendRoutingEngineLog(rt.logEngine(), proposal.LogLevel, proposal.LogMessage)
	}
}

// loadTargets returns the model each of the router's tiers gets; nil when it
// has none.
func (rt *modelRouterRuntime) loadTargets() []configstoreTables.TableModelRouterTarget {
	if rt.targets == nil {
		return nil
	}
	if targets := rt.targets.Load(); targets != nil {
		return *targets
	}
	return nil
}

// storeTargets replaces the router's targets. The slice is copied, so the
// caller's later edits never reach a request in flight.
func (rt *modelRouterRuntime) storeTargets(targets []configstoreTables.TableModelRouterTarget) {
	if rt.targets == nil {
		return
	}
	copied := append([]configstoreTables.TableModelRouterTarget(nil), targets...)
	rt.targets.Store(&copied)
}

// displayName is the router's name for logs and context, its id until it has one.
func (rt *modelRouterRuntime) displayName() string {
	if rt.name != nil {
		if name := rt.name.Load(); name != nil && *name != "" {
			return *name
		}
	}
	return rt.id
}

// complexityRuntime returns the view over the plugin's own fields: the
// Complexity Router page's config, which routing rules read complexity_tier
// from. It has no id, name or targets: it is not a model router. It is built
// on each call, so a plugin assembled field by field, as tests do, still has
// one.
func (p *RoutingPlugin) complexityRuntime() *modelRouterRuntime {
	return &modelRouterRuntime{
		config:         &p.complexityConfig,
		sessionEnabled: &p.sessionEnabled,
		semantic:       p.semanticClassifier,
		llm:            p.llmClassifier,
		shared:         true,
	}
}

// modelRouters returns the served model routers keyed by id. The map is never
// mutated in place; writers replace it under routersMu.
func (p *RoutingPlugin) modelRouters() map[string]*modelRouterRuntime {
	if current := p.routers.Load(); current != nil {
		return *current
	}
	return nil
}

// modelRouter returns the router with the given id, or false if the plugin
// does not serve it.
func (p *RoutingPlugin) modelRouter(id string) (*modelRouterRuntime, bool) {
	rt, ok := p.modelRouters()[id]
	return rt, ok
}

// allClassifiers returns every classifier the plugin runs: the Complexity
// Router page's first, then the model routers by id.
func (p *RoutingPlugin) allClassifiers() []*modelRouterRuntime {
	routers := p.modelRouters()
	ids := make([]string, 0, len(routers))
	for id := range routers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	all := make([]*modelRouterRuntime, 0, len(routers)+1)
	all = append(all, p.complexityRuntime())
	for _, id := range ids {
		all = append(all, routers[id])
	}
	return all
}

// resolveRouterConfig validates a router's config for serving. An invalid
// one falls back to the defaults with a warning, as the single router always
// did, but session routing without a KV store is an error, since no fallback
// can serve it.
func (p *RoutingPlugin) resolveRouterConfig(config *complexity.AnalyzerConfig) (*complexity.AnalyzerConfig, error) {
	resolved, err := complexity.ValidateAndNormalize(config)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("invalid complexity analyzer config, using defaults: %v", err)
		}
		defaults := complexity.DefaultAnalyzerConfig()
		resolved = &defaults
	}
	if resolved.SessionRoutingEnabled() && p.sessionStore == nil {
		return nil, fmt.Errorf("complexity session routing requires a KV store")
	}
	return resolved, nil
}

// applyRouterConfig installs a resolved config on a router: its settings and
// its classifiers. A decision-model or llm router keeps no semantic block, so
// its semantic classifier stays idle.
func applyRouterConfig(rt *modelRouterRuntime, resolved *complexity.AnalyzerConfig) {
	rt.config.Store(resolved)
	rt.sessionEnabled.Store(resolved.SessionRoutingEnabled())
	if rt.semantic != nil {
		semanticConfig := resolved
		if resolved.Classifier != complexity.ClassifierSemantic {
			copyConfig := *resolved
			copyConfig.Semantic = nil
			semanticConfig = &copyConfig
		}
		rt.semantic.Configure(semanticConfig)
	}
	configureRouterCapture(rt, resolved)
	if rt.llm != nil {
		rt.llm.Configure(resolved)
	}
}

// newModelRouterRuntime builds a router's classifiers with the collaborators
// the plugin's own classifier was given: the warm coordinator, the embedding
// width store, the generation registry, and whatever embedding and chat
// executors are wired, and a request capture of its own for phrase
// suggestions.
func (p *RoutingPlugin) newModelRouterRuntime(id string) *modelRouterRuntime {
	ctx := p.runtimeCtx
	if ctx == nil {
		ctx = context.Background()
	}
	state := &modelRouterState{}
	semantic := complexity.NewSemanticClassifier(ctx, p.logger)
	if p.warmCoordinator != nil {
		semantic.SetWarmCoordinator(p.warmCoordinator)
	}
	if p.dimensionStore != nil {
		semantic.SetEmbeddingDimensionStore(p.dimensionStore)
	}
	if p.generations != nil {
		semantic.SetGenerationClaimer(p.generations)
	}
	var store vectorstore.VectorStore
	if ptr := p.complexityVectorStore.Load(); ptr != nil {
		store = *ptr
	}
	if p.embeddingExecutor() != nil {
		semantic.SetWarmupDependencies(store, p.embedComplexityText, p.embedComplexityTexts)
	} else if store != nil {
		semantic.SetConfiguredStore(store)
	}
	capture := p.newRouterCapture(id, semantic)
	llm := complexity.NewLLMClassifier(p.logger)
	if p.chatExecutor() != nil {
		llm.SetChatFunc(p.classifyComplexityTextViaLLM)
	}
	return &modelRouterRuntime{
		id:             id,
		name:           &state.name,
		config:         &state.config,
		targets:        &state.targets,
		sessionEnabled: &state.sessionEnabled,
		semantic:       semantic,
		llm:            llm,
		capture:        capture,
	}
}

// close stops a router's classifiers and lets its capture finish writing what
// it queued. The Complexity Router page's are the plugin's and are closed by
// Cleanup.
func (rt *modelRouterRuntime) close(logger schemas.Logger) {
	if rt.shared {
		return
	}
	if rt.semantic != nil {
		if err := rt.semantic.Close(); err != nil && logger != nil {
			logger.Warn("[Routing] failed to close model router %q semantic classifier: %v", rt.id, err)
		}
	}
	rt.capture.Close()
}

// ReloadModelRouter serves a saved router: built the first time it is seen
// and reconfigured after, with its routing recorded for the model router
// step. A config that cannot be served, or a routing condition that does not
// compile, leaves the router as it was.
func (p *RoutingPlugin) ReloadModelRouter(router *configstore.ModelRouter) error {
	if router == nil {
		return fmt.Errorf("model router is nil")
	}
	resolved, err := p.resolveRouterConfig(&router.ComplexityAnalyzerConfig)
	if err != nil {
		return err
	}
	p.routersMu.Lock()
	defer p.routersMu.Unlock()
	if err := p.setModelRouterRoutingLocked(router.ID, router.Name, router.Routing); err != nil {
		return err
	}
	p.serveModelRouterLocked(router, resolved)
	return nil
}

// serveModelRouterLocked serves a router on a runtime of its own. routersMu
// must be held.
func (p *RoutingPlugin) serveModelRouterLocked(router *configstore.ModelRouter, resolved *complexity.AnalyzerConfig) {
	current := p.modelRouters()
	rt, exists := current[router.ID]
	if !exists {
		rt = p.newModelRouterRuntime(router.ID)
	}
	applyRouterConfig(rt, resolved)
	name := router.Name
	rt.name.Store(&name)
	rt.storeTargets(router.Targets)
	if !exists {
		next := make(map[string]*modelRouterRuntime, len(current)+1)
		for id, existing := range current {
			next[id] = existing
		}
		next[router.ID] = rt
		p.routers.Store(&next)
	}
}

// setModelRouterRoutingLocked records which requests a router takes. A router
// without routing takes none. routersMu must be held, so a reload and a
// removal cannot interleave with it.
func (p *RoutingPlugin) setModelRouterRoutingLocked(id, name string, routing *configstore.ModelRouterRouting) error {
	if p.routerMatcher == nil {
		return fmt.Errorf("model router matching is not available")
	}
	if routing == nil {
		p.routerMatcher.Delete(id)
		return nil
	}
	return p.routerMatcher.Set(id, name, *routing)
}

// SetModelRouterRouting replaces which requests a served router takes,
// leaving its classifiers and targets as they are. configstore.ErrNotFound
// means the plugin does not serve the router; a condition that does not
// compile is an error and leaves the recorded routing as it was.
func (p *RoutingPlugin) SetModelRouterRouting(id string, routing configstore.ModelRouterRouting) error {
	p.routersMu.Lock()
	defer p.routersMu.Unlock()
	rt, ok := p.modelRouter(id)
	if !ok {
		return configstore.ErrNotFound
	}
	return p.setModelRouterRoutingLocked(id, rt.displayName(), &routing)
}

// SetModelRouterTargets replaces the model each tier of a served router gets,
// leaving its classifiers as they are. configstore.ErrNotFound means the
// plugin does not serve the router.
func (p *RoutingPlugin) SetModelRouterTargets(id string, targets []configstoreTables.TableModelRouterTarget) error {
	rt, ok := p.modelRouter(id)
	if !ok {
		return configstore.ErrNotFound
	}
	rt.storeTargets(targets)
	return nil
}

// RemoveModelRouter stops serving a deleted router, closes its classifiers
// and drops what it captured. Removing one not served still drops its
// captures, which another node may have written.
func (p *RoutingPlugin) RemoveModelRouter(id string) error {
	p.routersMu.Lock()
	if p.routerMatcher != nil {
		p.routerMatcher.Delete(id)
	}
	current := p.modelRouters()
	rt, exists := current[id]
	if exists {
		next := make(map[string]*modelRouterRuntime, len(current))
		for routerID, existing := range current {
			if routerID != id {
				next[routerID] = existing
			}
		}
		p.routers.Store(&next)
	}
	p.routersMu.Unlock()
	if exists {
		rt.close(p.logger)
	}
	p.dropRouterCaptures(id)
	return nil
}

// loadModelRouters serves every stored router at startup. A router that
// cannot be served, or whose routing condition no longer compiles, is logged
// and left out of matching, so one bad router cannot keep the gateway from
// starting.
func (p *RoutingPlugin) loadModelRouters(ctx context.Context, configStore configstore.ConfigStore) {
	if configStore == nil {
		return
	}
	routers, err := configStore.GetModelRouters(ctx)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("[Routing] failed to load model routers: %v", err)
		}
		return
	}
	p.routersMu.Lock()
	defer p.routersMu.Unlock()
	for i := range routers {
		router := &routers[i]
		resolved, err := p.resolveRouterConfig(&router.ComplexityAnalyzerConfig)
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("[Routing] failed to serve model router %q: %v", router.ID, err)
			}
			continue
		}
		p.serveModelRouterLocked(router, resolved)
		if err := p.setModelRouterRoutingLocked(router.ID, router.Name, router.Routing); err != nil && p.logger != nil {
			p.logger.Warn("[Routing] model router %q takes no requests: its routing condition does not compile: %v", router.ID, err)
		}
	}
}

// closeModelRouters closes every model router.
func (p *RoutingPlugin) closeModelRouters() {
	p.routersMu.Lock()
	current := p.modelRouters()
	empty := map[string]*modelRouterRuntime{}
	p.routers.Store(&empty)
	p.routersMu.Unlock()
	for _, rt := range current {
		rt.close(p.logger)
	}
}

// forEachModelRouter applies a collaborator change to every model router; the
// caller has already updated the plugin's own classifiers.
func (p *RoutingPlugin) forEachModelRouter(fn func(rt *modelRouterRuntime)) {
	for _, rt := range p.modelRouters() {
		fn(rt)
	}
}

// ModelRouterSemanticStatus reports one router's semantic classifier
// readiness, and false for a router the plugin does not serve.
func (p *RoutingPlugin) ModelRouterSemanticStatus(id string) (complexity.SemanticStatusInfo, bool) {
	rt, ok := p.modelRouter(id)
	if !ok {
		return complexity.SemanticStatusInfo{}, false
	}
	if rt.semantic == nil {
		return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}, true
	}
	return rt.semantic.Status(), true
}

// ModelRouterLLMStatus reports one router's llm classifier readiness, and
// false for a router the plugin does not serve.
func (p *RoutingPlugin) ModelRouterLLMStatus(id string) (complexity.LLMStatusInfo, bool) {
	rt, ok := p.modelRouter(id)
	if !ok {
		return complexity.LLMStatusInfo{}, false
	}
	if rt.llm == nil {
		return complexity.LLMStatusInfo{State: complexity.LLMStatusDisabled}, true
	}
	return rt.llm.Status(), true
}

// RetryModelRouterWarmup restarts one router's failed semantic warmup. It
// reports the status, whether a retry started, and false for a router the
// plugin does not serve.
func (p *RoutingPlugin) RetryModelRouterWarmup(id string) (status complexity.SemanticStatusInfo, started bool, ok bool) {
	rt, ok := p.modelRouter(id)
	if !ok {
		return complexity.SemanticStatusInfo{}, false, false
	}
	if rt.semantic == nil {
		return complexity.SemanticStatusInfo{State: complexity.SemanticStatusDisabled}, false, true
	}
	status, started = rt.semantic.RetryWarmup()
	return status, started, true
}
