package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// modelRouterRuntime is one classifier as the plugin serves it: its config
// and the classifiers built from it.
//
// Every classifier router owns one. A conditional router classifies nothing,
// so it has none: the plugin serves it from its saved record and its routing
// alone, and a gateway with many of them builds nothing for them. The
// Complexity Router page's config, which
// routing rules read complexity_tier from, is served by the plugin's own
// fields, the ones it had before routers existed; complexityRuntime views
// them as a runtime with no id, so one classification path serves both.
// Request capture and phrase suggestions are a model router's only: each
// captures its own classified requests (see complexitycapture.go).
//
// Routers whose classifier settings are the same (classifierShareKey) classify
// with the same classifiers: the first one builds them and the others join it,
// so a hundred routers made from one configuration embed their phrases once
// and hold one phrase bank. Each still has a runtime of its own, with its own
// settings and session toggle. The classifiers are closed when the last router
// using them leaves (classifiersUsed).
type modelRouterRuntime struct {
	id string // "" for the Complexity Router page's config
	// record reads the router as saved: its name, targets and routing come from the live store,
	// so the runtime keeps only what it derives (the resolved config) and its classifiers. nil
	// for the Complexity Router page's config.
	record         func() (*registry.ModelRouter, bool)
	config         *atomic.Pointer[complexity.AnalyzerConfig]
	sessionEnabled *atomic.Bool
	shareKey       *atomic.Pointer[string] // see classifierKey; nil for the Complexity Router page's config
	semantic       *complexity.SemanticClassifier
	llm            *complexity.LLMClassifier
	capture        *complexity.RequestCapture // nil for the Complexity Router page's config, when capture is not started, or for a router that joined another's classifiers
	built          bool                       // the router built the classifiers it uses, so what they capture is keyed by it
	shared         bool                       // a view over the plugin's own fields, closed by Cleanup
}

// modelRouterState is the state a model router owns.
type modelRouterState struct {
	config         atomic.Pointer[complexity.AnalyzerConfig]
	sessionEnabled atomic.Bool
	shareKey       atomic.Pointer[string]
}

// classifierKey names the classifier settings the router classifies with, as
// classifierShareKey made it. Routers with the same key use the same
// classifiers and reuse one classification within a request. Empty for a
// router that shares neither, and for the Complexity Router page's config.
func (rt *modelRouterRuntime) classifierKey() string {
	if rt.shareKey == nil {
		return ""
	}
	if key := rt.shareKey.Load(); key != nil {
		return *key
	}
	return ""
}

// classifierShareKey names a router's classifier settings, so routers that
// classify alike can be told apart from ones that do not. Everything that can
// change a classification or what it costs is part of it: the classifier, its
// semantic, decision and llm blocks, the phrases and the tier boundaries. The
// session block is left out, because each router applies its own session
// memory to the answer afterwards.
//
// A router with a suggestions block gets no key, enabled or not: what its
// classifier captures, and what a suggestion run reads back, is keyed by the
// router, so it keeps classifiers of its own.
func classifierShareKey(resolved *complexity.AnalyzerConfig) string {
	if resolved == nil || resolved.Suggestions != nil {
		return ""
	}
	settings := *resolved
	settings.Session = nil
	data, err := json.Marshal(settings)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// classifiersUsed reports whether a router other than rt classifies with rt's
// classifiers.
func classifiersUsed(routers map[string]*modelRouterRuntime, rt *modelRouterRuntime) bool {
	if rt.semantic == nil {
		return false
	}
	for _, other := range routers {
		if other != rt && other.semantic == rt.semantic {
			return true
		}
	}
	return false
}

// routerWithClassifierKey returns a router other than id that classifies with
// the given settings, for a router with the same settings to join.
func routerWithClassifierKey(routers map[string]*modelRouterRuntime, id, key string) *modelRouterRuntime {
	if key == "" {
		return nil
	}
	for otherID, other := range routers {
		if otherID != id && other.classifierKey() == key {
			return other
		}
	}
	return nil
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
// publishComplexityProposal does, tagged with the runtime's engine. A reused
// answer's log line was written by the router that classified.
func (rt *modelRouterRuntime) publishProposal(ctx *schemas.BifrostContext, proposal complexityProposal) {
	publishComplexityDecision(ctx, proposal.Result, proposal.Mechanism, proposal.Score)
	if proposal.LogMessage != "" && !proposal.Reused {
		ctx.AppendRoutingEngineLog(rt.logEngine(), proposal.LogLevel, proposal.LogMessage)
	}
}

// loadTargets returns the model each of the router's tiers gets, as saved; nil when it has none.
// The record is shared: callers must not change what it returns.
func (rt *modelRouterRuntime) loadTargets() []configstoreTables.TableModelRouterTarget {
	if rt.record == nil {
		return nil
	}
	if record, ok := rt.record(); ok {
		return record.Router.Targets
	}
	return nil
}

// displayName is the router's name for logs and context, its id until it has one.
func (rt *modelRouterRuntime) displayName() string {
	if rt.record != nil {
		if record, ok := rt.record(); ok && record.Router.Name != "" {
			return record.Router.Name
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
// Router page's first, then the model routers' by id. Routers that share
// classifiers are listed once, through the first of them.
func (p *RoutingPlugin) allClassifiers() []*modelRouterRuntime {
	routers := p.modelRouters()
	ids := make([]string, 0, len(routers))
	for id := range routers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	all := make([]*modelRouterRuntime, 0, len(routers)+1)
	all = append(all, p.complexityRuntime())
	listed := make(map[*complexity.SemanticClassifier]struct{}, len(routers))
	for _, id := range ids {
		rt := routers[id]
		if rt.semantic != nil {
			if _, done := listed[rt.semantic]; done {
				continue
			}
			listed[rt.semantic] = struct{}{}
		}
		all = append(all, rt)
	}
	return all
}

// resolveRouterConfig validates a router's config for serving. An invalid
// one falls back to the defaults with a warning, as the single router always
// did, but session routing without a session store is an error, since no fallback
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
	if resolved.SessionRoutingEnabled() && p.sessions == nil {
		return nil, fmt.Errorf("complexity session routing requires a session store")
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

// applyRouterSettings installs what is the router's own in a resolved config,
// for a router whose classifiers stay as they are: its settings as requests
// read them, its session toggle and how its capture samples.
func applyRouterSettings(rt *modelRouterRuntime, resolved *complexity.AnalyzerConfig) {
	rt.config.Store(resolved)
	rt.sessionEnabled.Store(resolved.SessionRoutingEnabled())
	configureRouterCapture(rt, resolved)
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
		record:         p.routerRecordReader(id),
		config:         &state.config,
		sessionEnabled: &state.sessionEnabled,
		shareKey:       &state.shareKey,
		semantic:       semantic,
		llm:            llm,
		capture:        capture,
		built:          true,
	}
}

// joinModelRouterRuntime serves a router on the classifiers a router with the
// same classifier settings already uses. Nothing is built or warmed: the new
// runtime holds only the router's own settings. It has no capture, which stays
// with the router the classifiers were built for.
func (p *RoutingPlugin) joinModelRouterRuntime(id string, with *modelRouterRuntime) *modelRouterRuntime {
	state := &modelRouterState{}
	return &modelRouterRuntime{
		id:             id,
		record:         p.routerRecordReader(id),
		config:         &state.config,
		sessionEnabled: &state.sessionEnabled,
		shareKey:       &state.shareKey,
		semantic:       with.semantic,
		llm:            with.llm,
	}
}

// routerRecordReader reads one router as the live store holds it now.
func (p *RoutingPlugin) routerRecordReader(id string) func() (*registry.ModelRouter, bool) {
	records := p.routerRecords
	return func() (*registry.ModelRouter, bool) {
		if records == nil {
			return nil, false
		}
		return records.Get(id)
	}
}

// close stops what a runtime holds once its router has left it: its capture,
// which finishes writing what it queued, and its classifiers unless other
// routers still classify with them (stillUsed, read while the routers were
// locked). The Complexity Router page's are the plugin's and are closed by
// Cleanup.
func (rt *modelRouterRuntime) close(logger schemas.Logger, stillUsed bool) {
	if rt == nil || rt.shared {
		return
	}
	if rt.semantic != nil && !stillUsed {
		if err := rt.semantic.Close(); err != nil && logger != nil {
			logger.Warn("[Routing] failed to close model router %q semantic classifier: %v", rt.id, err)
		}
	}
	rt.capture.Close()
}

// ReloadModelRouter serves a saved router: built the first time it is seen
// and reconfigured after, with its routing recorded for the model router
// step. A config that cannot be served, or a routing condition that does not
// compile, leaves the router as it was. A conditional router has no
// classifier to build, so only its routing is recorded.
func (p *RoutingPlugin) ReloadModelRouter(router *configstore.ModelRouter) error {
	if router == nil {
		return fmt.Errorf("model router is nil")
	}
	if isConditionalRouter(router) {
		p.routersMu.Lock()
		defer p.routersMu.Unlock()
		return p.setModelRouterRoutingLocked(router.ID, router.Name, router.Routing)
	}
	resolved, err := p.resolveRouterConfig(&router.ComplexityAnalyzerConfig)
	if err != nil {
		return err
	}
	p.routersMu.Lock()
	if err := p.setModelRouterRoutingLocked(router.ID, router.Name, router.Routing); err != nil {
		p.routersMu.Unlock()
		return err
	}
	left, stillUsed := p.serveModelRouterLocked(router, resolved)
	p.routersMu.Unlock()
	left.close(p.logger, stillUsed)
	return nil
}

// serveModelRouterLocked serves a router with its resolved config. routersMu
// must be held.
//
// A router keeps the classifiers it has, reconfigured as a reload always did,
// while no other router uses them and no other router already classifies with
// its new settings. So a router that shares with no one behaves as it did
// before routers shared: an edit keeps its previous phrase bank serving while
// the new one warms. Otherwise it moves: onto the classifiers of a router with
// the same settings, which are warm already, or onto new ones of its own, which
// warm as a new router's do. The routers it leaves behind are not touched.
//
// When the router moved, the runtime it left is returned for the caller to
// close once routersMu is released, with whether other routers still use its
// classifiers.
func (p *RoutingPlugin) serveModelRouterLocked(router *configstore.ModelRouter, resolved *complexity.AnalyzerConfig) (left *modelRouterRuntime, stillUsed bool) {
	key := classifierShareKey(resolved)
	current := p.modelRouters()
	rt, exists := current[router.ID]
	peer := routerWithClassifierKey(current, router.ID, key)
	if exists {
		alone := !classifiersUsed(current, rt)
		switch {
		case !alone && key == rt.classifierKey():
			// The classifier settings are the ones the others still use: only
			// what is the router's own changed.
			applyRouterSettings(rt, resolved)
			return nil, false
		case alone && peer == nil && (key != "" || rt.built):
			// A router with no key needs classifiers built for it, since what
			// they capture is keyed by the router.
			applyRouterConfig(rt, resolved)
			rt.shareKey.Store(&key)
			return nil, false
		}
		left, stillUsed = rt, !alone
	}
	if peer != nil {
		rt = p.joinModelRouterRuntime(router.ID, peer)
		applyRouterSettings(rt, resolved)
	} else {
		rt = p.newModelRouterRuntime(router.ID)
		applyRouterConfig(rt, resolved)
	}
	rt.shareKey.Store(&key)
	next := make(map[string]*modelRouterRuntime, len(current)+1)
	for id, existing := range current {
		next[id] = existing
	}
	next[router.ID] = rt
	p.routers.Store(&next)
	return left, stillUsed
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
	name, ok := p.servedRouterName(id)
	if !ok {
		return configstore.ErrNotFound
	}
	return p.setModelRouterRoutingLocked(id, name, &routing)
}

// servedRouterName returns the name of a router the plugin serves: a
// classifier router it has built, or a conditional router the live store
// holds, which needs nothing built.
func (p *RoutingPlugin) servedRouterName(id string) (string, bool) {
	if rt, ok := p.modelRouter(id); ok {
		return rt.displayName(), true
	}
	record, ok := p.routerRecord(id)
	if !ok || !isConditionalRouter(&record.Router) {
		return "", false
	}
	if record.Router.Name != "" {
		return record.Router.Name, true
	}
	return id, true
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
	stillUsed := false
	if exists {
		next := make(map[string]*modelRouterRuntime, len(current))
		for routerID, existing := range current {
			if routerID != id {
				next[routerID] = existing
			}
		}
		p.routers.Store(&next)
		stillUsed = classifiersUsed(next, rt)
	}
	p.routersMu.Unlock()
	if exists {
		rt.close(p.logger, stillUsed)
	}
	p.dropRouterCaptures(id)
	return nil
}

// loadModelRouters serves every saved router at startup, as the live store holds them. A router
// that cannot be served, or whose routing condition no longer compiles, is logged and left out of
// matching. One stored in a form this version cannot read never gets here: it fails the live
// store's load.
func (p *RoutingPlugin) loadModelRouters() {
	var records []*registry.ModelRouter
	p.routerRecords.Range(func(r *registry.ModelRouter) bool {
		records = append(records, r)
		return true
	})
	sort.Slice(records, func(i, j int) bool { return records[i].Router.ID < records[j].Router.ID })
	p.routersMu.Lock()
	defer p.routersMu.Unlock()
	for _, record := range records {
		router := &record.Router
		if !isConditionalRouter(router) {
			resolved, err := p.resolveRouterConfig(&router.ComplexityAnalyzerConfig)
			if err != nil {
				if p.logger != nil {
					p.logger.Warn("[Routing] failed to serve model router %q: %v", router.ID, err)
				}
				continue
			}
			// Each id is served once here, so no router leaves a runtime behind.
			p.serveModelRouterLocked(router, resolved)
		}
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
	closed := make(map[*complexity.SemanticClassifier]struct{}, len(current))
	for _, rt := range current {
		_, done := closed[rt.semantic]
		rt.close(p.logger, done)
		closed[rt.semantic] = struct{}{}
	}
}

// forEachModelRouter visits every model router, for what is each router's
// own: its settings and its capture.
func (p *RoutingPlugin) forEachModelRouter(fn func(rt *modelRouterRuntime)) {
	for _, rt := range p.modelRouters() {
		fn(rt)
	}
}

// forEachRouterClassifiers applies a collaborator change to the model routers'
// classifiers, once for each set: routers that share classifiers are visited
// through one of them, so a change that restarts a warmup restarts it once.
// The caller has already updated the plugin's own classifiers.
func (p *RoutingPlugin) forEachRouterClassifiers(fn func(rt *modelRouterRuntime)) {
	routers := p.modelRouters()
	if len(routers) == 0 {
		return
	}
	visited := make(map[*complexity.SemanticClassifier]struct{}, len(routers))
	for _, rt := range routers {
		if rt.semantic != nil {
			if _, done := visited[rt.semantic]; done {
				continue
			}
			visited[rt.semantic] = struct{}{}
		}
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
