package governance

import (
	"errors"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// complexitySessionGlobalScope namespaces sessions on deployments that route
// without virtual keys. Callers in this scope must provide globally unique
// session IDs because no narrower ownership boundary is available.
const complexitySessionGlobalScope = "global"

// complexitySessionContextKey carries the resolved session across the request so
// the response path can record what the provider reported without repeating
// identity resolution and key derivation.
const complexitySessionContextKey schemas.BifrostContextKey = "bf-governance-complexity-session"

const (
	// maxSessionRouteObservations bounds retained per-route history. A session
	// that cycles through fallbacks would otherwise grow this map without limit,
	// and the whole record is rewritten — and replicated — on every write.
	maxSessionRouteObservations = 8
	// sessionObservationChangeRatio is how much the cached-token count must move
	// before it is worth a write. Cache sizes drift a little every turn, and
	// persisting each drift would put a cluster broadcast back on every request.
	sessionObservationChangeRatio = 0.1

	complexitySessionTierSourceClassified = "classified"
	complexitySessionTierSourceMemoised   = "memoised"
	complexitySessionTierSourceHeld       = "held"
)

var (
	errSessionRouteObservationUnchanged = errors.New("complexity session route observation unchanged")
	errSessionTierDecisionUnchanged     = errors.New("complexity session tier decision unchanged")
)

// complexitySessionState is the per-request session context, resolved once
// before routing so both the routing engine and the classification closure work
// from the same identity.
type complexitySessionState struct {
	// ID is the resolved conversation identity, empty when nothing identified it.
	ID string
	// Source names which rung of the identity ladder produced ID.
	Source string
	// Key is the tenant-namespaced store key, empty when ID is empty.
	Key string
	// Config is the normalized session-policy snapshot captured at request start.
	// Keeping one value avoids mixing thresholds from a concurrent config reload.
	Config configstore.ComplexitySessionConfig
}

// resolveComplexitySessionState resolves the conversation identity for this
// request. It returns nil when session behaviour is off, when no store is
// attached, or when nothing identified the conversation — all of which mean
// "classify this turn normally", the pre-session behaviour.
//
// Resolution runs eagerly because the routing engine needs the ID to make target
// selection sticky, and the ladder only reads request context. The store lookup
// stays lazy: it happens inside the classification closure, so a request whose
// rules never mention complexity never touches the store at all.
func (p *GovernancePlugin) resolveComplexitySessionState(
	ctx *schemas.BifrostContext,
	virtualKey *configstoreTables.TableVirtualKey,
) *complexitySessionState {
	config := p.complexitySessionConfig.Load()
	if config == nil || p.complexitySessionStore.Load() == nil {
		return nil
	}

	sessionID, source, ok := resolveSessionID(ctx, config.IdentitySources)
	if !ok {
		return nil
	}

	// Sessions are namespaced per virtual key so one tenant's conversation can
	// never adopt another's pinned tier, even if both present the same id.
	scopeID := complexitySessionGlobalScope
	if virtualKey != nil && virtualKey.ID != "" {
		scopeID = virtualKey.ID
	}
	key, ok := buildComplexitySessionKey(scopeID, sessionID)
	if !ok {
		return nil
	}

	ttl := config.TTL
	if ttl <= 0 {
		// Normalization fills this in, so reaching here means a record was
		// written by an older build or hand-edited. A non-positive ttl is
		// rejected by the store, which would disable sessions silently.
		return nil
	}

	state := &complexitySessionState{ID: sessionID, Source: source, Key: key, Config: *config}
	// Carried on the context because the response path cannot redo this: identity
	// resolution reads the request body, which is gone by then.
	ctx.SetValue(complexitySessionContextKey, state)
	return state
}

// recordSessionRouteObservation stores cache-reuse evidence for the route that
// actually served this turn. Cache-aware switching decides whether discarding a
// warm cache is worth it, which is unanswerable without these facts.
//
// It records nothing on non-final chunks: usage generally only arrives with the
// last one, so recording earlier would persist useless unknown observations and
// add replication traffic to streamed requests.
func (p *GovernancePlugin) recordSessionRouteObservation(
	ctx *schemas.BifrostContext,
	result *schemas.BifrostResponse,
	provider schemas.ModelProvider,
	model string,
	isFinalChunk bool,
) {
	if !isFinalChunk || result == nil {
		return
	}
	state, _ := ctx.Value(complexitySessionContextKey).(*complexitySessionState)
	if state == nil || state.Config.Mode != configstore.ComplexitySessionModeCacheAware {
		return
	}
	stored := p.complexitySessionStore.Load()
	if stored == nil {
		return
	}
	store := *stored

	routeID := effectiveSessionRouteIdentity(ctx, provider, model)
	if routeID == "" {
		return
	}
	cachedTokens, cacheObserved := sessionCachedReadTokens(result)
	observation := SessionRouteObservation{
		CachedReadTokens: cachedTokens,
		CacheObserved:    cacheObserved,
		LastSeenAt:       time.Now(),
	}

	// The comparison belongs inside Update. A separate Get followed by Update can
	// make its decision from a stale record and let an older response overwrite a
	// newer observation. Returning the private sentinel aborts both the write and
	// its replication event when this turn adds no information.
	_, _, err := store.Update(ctx, state.Key, state.Config.TTL, func(current *SessionComplexityRecord) error {
		if !applySessionRouteObservation(current, routeID, observation, state.Config.TTL) {
			return errSessionRouteObservationUnchanged
		}
		return nil
	})
	if err != nil && !errors.Is(err, errSessionRouteObservationUnchanged) && p.logger != nil {
		p.logger.Debug("[Governance] Could not record complexity session route observation: %v", err)
	}
}

// applySessionRouteObservation conditionally applies observation to the latest
// record owned by SessionStore.Update. It returns false for stale or immaterial
// observations so the caller can abort the write and avoid replication.
func applySessionRouteObservation(
	record *SessionComplexityRecord,
	routeID string,
	observation SessionRouteObservation,
	ttl time.Duration,
) bool {
	previous := record.RouteObservations[routeID]
	if !sessionObservationNeedsUpdate(
		previous,
		observation.CachedReadTokens,
		observation.CacheObserved,
		observation.LastSeenAt,
		ttl,
	) {
		return false
	}
	if record.RouteObservations == nil {
		record.RouteObservations = make(map[string]SessionRouteObservation, 1)
	}
	record.RouteObservations[routeID] = observation
	boundSessionRouteObservations(record.RouteObservations, maxSessionRouteObservations)
	return true
}

// sessionObservationNeedsUpdate decides whether this turn told us anything the
// stored observation does not already say.
func sessionObservationNeedsUpdate(
	previous SessionRouteObservation,
	cachedTokens int,
	cacheObserved bool,
	observedAt time.Time,
	ttl time.Duration,
) bool {
	// Never seen this route.
	if previous.LastSeenAt.IsZero() {
		return true
	}
	// A response that completed earlier must never overwrite a later observation,
	// even when its cache count differs enough that it would otherwise be useful.
	if !observedAt.After(previous.LastSeenAt) {
		return false
	}
	// Whether the normalized response proves cache reuse is a different fact from
	// how many tokens it proves, and it changes how the number should be read.
	if previous.CacheObserved != cacheObserved {
		return true
	}
	if cacheObserved && sessionCachedTokensMateriallyChanged(previous.CachedReadTokens, cachedTokens) {
		return true
	}
	// Otherwise refresh on the same cadence as the read path, so LastSeenAt stays
	// roughly current without a write per turn.
	return observedAt.Sub(previous.LastSeenAt) >= complexitySessionRefreshInterval(ttl)
}

// sessionCachedTokensMateriallyChanged treats any crossing of zero as material.
// The current response path never marks an ambiguous zero as observed, but
// replicated records or a future presence-aware writer may carry an observed
// zero, so the comparison remains complete for every valid record.
func sessionCachedTokensMateriallyChanged(previous, next int) bool {
	if previous == next {
		return false
	}
	if previous == 0 || next == 0 {
		return true
	}
	delta := next - previous
	if delta < 0 {
		delta = -delta
	}
	return float64(delta) > float64(previous)*sessionObservationChangeRatio
}

// boundSessionRouteObservations evicts the least recently seen routes until the
// map fits. Recency is the right axis: a route not seen for a while is not the
// one a switch would be giving up.
func boundSessionRouteObservations(observations map[string]SessionRouteObservation, limit int) {
	for len(observations) > limit {
		oldestKey := ""
		var oldestSeen time.Time
		for key, observation := range observations {
			if oldestKey == "" || observation.LastSeenAt.Before(oldestSeen) {
				oldestKey, oldestSeen = key, observation.LastSeenAt
			}
		}
		delete(observations, oldestKey)
	}
}

// effectiveSessionRouteIdentity identifies the route that actually served, which
// is not necessarily the one routing selected: a fallback changes provider and
// model, and key rotation changes which cache is warm. The value is hashed
// because callers only ever compare identities, and the record replicates.
func effectiveSessionRouteIdentity(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) string {
	keyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	if provider == "" && model == "" && keyID == "" {
		return ""
	}
	return complexitySessionHash(string(provider) + "\x00" + model + "\x00" + keyID)
}

// sessionCachedReadTokens reports positive evidence of provider cache reuse.
// The normalized usage structs do not preserve whether a zero-valued cache field
// was present, and their detail blocks may exist only for modality accounting.
// Ambiguous zero is therefore treated as unobserved rather than as a cold cache.
// The chat and responses APIs carry the same fact under different types, so each
// is unwrapped on its own rather than through a shared usage value.
func sessionCachedReadTokens(result *schemas.BifrostResponse) (int, bool) {
	switch {
	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		return chatCachedReadTokens(result.ChatResponse.Usage)
	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		return chatCachedReadTokens(result.TextCompletionResponse.Usage)
	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		return responsesCachedReadTokens(result.ResponsesResponse.Usage)
	case result.ResponsesStreamResponse != nil &&
		result.ResponsesStreamResponse.Response != nil &&
		result.ResponsesStreamResponse.Response.Usage != nil:
		return responsesCachedReadTokens(result.ResponsesStreamResponse.Response.Usage)
	}
	return 0, false
}

func chatCachedReadTokens(usage *schemas.BifrostLLMUsage) (int, bool) {
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedReadTokens <= 0 {
		return 0, false
	}
	return usage.PromptTokensDetails.CachedReadTokens, true
}

func responsesCachedReadTokens(usage *schemas.ResponsesResponseUsage) (int, bool) {
	if usage.InputTokensDetails == nil || usage.InputTokensDetails.CachedReadTokens <= 0 {
		return 0, false
	}
	return usage.InputTokensDetails.CachedReadTokens, true
}

// publishSessionKeyAffinity hands the resolved conversation identity to the
// core's API-key stickiness.
//
// Without this the feature protects the wrong layer. A provider's prompt cache
// is keyed per API key, so holding a conversation on one tier while its key
// rotates between turns discards the cache anyway — the pin would do the
// bookkeeping and deliver none of the benefit.
//
// It is safe to publish unconditionally here because state is only non-nil when
// session behaviour is switched on and something identified the conversation.
// That matters: core activates stickiness on any non-empty session id, so
// writing a derived id outside those conditions would silently pin API keys for
// callers who never asked for it.
func (p *GovernancePlugin) publishSessionKeyAffinity(ctx *schemas.BifrostContext, state *complexitySessionState) {
	if state == nil {
		return
	}
	// A header-sourced ID is already here and this rewrites the same value; a
	// harness-native one is new, which is the whole point.
	ctx.SetValue(schemas.BifrostContextKeySessionID, state.ID)

	// Align the two lifetimes. Core otherwise falls back to its own one-hour
	// default, so a shorter configured session would release its tier while the
	// key stayed pinned — or a longer one would keep classifying against a key
	// that had already rotated. An explicit per-request ttl still wins: the
	// caller asked for it by name.
	if existing, ok := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration); !ok || existing <= 0 {
		ctx.SetValue(schemas.BifrostContextKeySessionTTL, state.Config.TTL)
	}
}

// withComplexitySession wraps the lazy classification closure with the selected
// session policy. Pinned mode reuses an established tier without classifying;
// cache-aware mode classifies each relevant turn and decides atomically whether
// the session may move.
//
// It deliberately wraps rather than running ahead of classification. The inner
// closure is only invoked when a routing rule references complexity_tier, so
// wrapping inherits that condition for free and a request that never routes on
// complexity performs no session reads.
func (p *GovernancePlugin) withComplexitySession(
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	classify func() *complexity.ComplexityResult,
) func() *complexity.ComplexityResult {
	if state == nil || classify == nil {
		return classify
	}
	stored := p.complexitySessionStore.Load()
	if stored == nil {
		return classify
	}
	store := *stored

	switch state.Config.Mode {
	case configstore.ComplexitySessionModePinned:
		return func() *complexity.ComplexityResult {
			return p.classifyWithPinnedSession(ctx, store, state, classify)
		}
	case configstore.ComplexitySessionModeCacheAware:
		return func() *complexity.ComplexityResult {
			return p.classifyWithCacheAwareSession(ctx, store, state, classify)
		}
	default:
		return classify
	}
}

func (p *GovernancePlugin) classifyWithPinnedSession(
	ctx *schemas.BifrostContext,
	store SessionStore,
	state *complexitySessionState,
	classify func() *complexity.ComplexityResult,
) *complexity.ComplexityResult {
	// A store that cannot be read is not a reason to fail the request: fall
	// through and classify, which is exactly the pre-session behaviour.
	record, found, err := store.Get(ctx, state.Key, state.Config.TTL)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("[Governance] Complexity session lookup failed, classifying this turn: %v", err)
		}
	} else if found && record != nil && record.Tier != "" {
		p.publishPinnedSessionTier(ctx, state, record)
		return &complexity.ComplexityResult{Tier: record.Tier}
	}

	result := classify()
	switchCount, switchCountKnown := p.persistInitialSessionTier(ctx, store, state, result)
	publishSessionClassificationTelemetry(ctx, state, result, switchCount, switchCountKnown)
	return result
}

func (p *GovernancePlugin) classifyWithCacheAwareSession(
	ctx *schemas.BifrostContext,
	store SessionStore,
	state *complexitySessionState,
	classify func() *complexity.ComplexityResult,
) *complexity.ComplexityResult {
	proposed := classify()
	var decision sessionTierDecision
	var switchCount int
	var switchCountKnown bool
	decisionTime := time.Now()
	_, found, err := store.Update(ctx, state.Key, state.Config.TTL, func(current *SessionComplexityRecord) error {
		decision = updateSessionTierRecord(current, proposed, &state.Config, decisionTime)
		if decision.Reason == sessionTierReasonInvalidState {
			return errInvalidComplexitySession
		}
		switchCount = current.SwitchCount
		switchCountKnown = true
		if !decision.RecordChanged {
			return errSessionTierDecisionUnchanged
		}
		return nil
	})
	if errors.Is(err, errSessionTierDecisionUnchanged) {
		// Aborting Update avoids a replicated write, but also leaves the expiry
		// untouched. Get performs the store's coarse sliding-TTL refresh without
		// changing the decision, which was already made from the locked record.
		if _, _, refreshErr := store.Get(ctx, state.Key, state.Config.TTL); refreshErr != nil && p.logger != nil {
			p.logger.Debug("[Governance] Could not refresh held complexity session: %v", refreshErr)
		}
		err = nil
	}
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("[Governance] Complexity session update failed, using this turn's classification: %v", err)
		}
		publishSessionClassificationTelemetry(ctx, state, proposed, 0, false)
		return proposed
	}
	if !found {
		// Update found no live record. Treat this request as a new session rather
		// than trying to apply switching policy without a held tier.
		switchCount, switchCountKnown = p.persistInitialSessionTier(ctx, store, state, proposed)
		publishSessionClassificationTelemetry(ctx, state, proposed, switchCount, switchCountKnown)
		return proposed
	}

	p.publishCacheAwareSessionTier(ctx, state, proposed, decision, switchCount, switchCountKnown)
	return complexityResultForSessionDecision(proposed, decision)
}

// persistInitialSessionTier records the first accepted tier for either session
// mode. A failed or rejected classification is never persisted: pinning "no
// tier" would turn one bad turn into a session-long routing outage.
func (p *GovernancePlugin) persistInitialSessionTier(
	ctx *schemas.BifrostContext,
	store SessionStore,
	state *complexitySessionState,
	result *complexity.ComplexityResult,
) (switchCount int, known bool) {
	if result == nil || result.Tier == "" {
		return 0, false
	}

	// RuleID is deliberately left unset. The tier is classifier memory about
	// the conversation, not state owned by whichever rule happened to consult
	// it first — binding them would let an unrelated rule edit drop every live
	// session's tier.
	created, err := store.Create(ctx, state.Key, &SessionComplexityRecord{
		Tier:      result.Tier,
		DecidedAt: time.Now(),
	}, state.Config.TTL)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("[Governance] Failed to persist complexity session tier; session policy will retry later: %v", err)
		}
		return 0, false
	}
	if created {
		ctx.AppendRoutingEngineLog(
			schemas.RoutingEngineRoutingRule,
			schemas.LogLevelInfo,
			"Complexity session established: tier="+result.Tier+" identity="+state.Source,
		)
		return 0, true
	}
	// Another request won the create race. Its stored switch count is not known
	// without another read, and observability is not worth an extra store call.
	return 0, false
}

// publishPinnedSessionTier records a reused tier with a mechanism that makes it
// explicit that no classifier or embedding ran for this turn.
func (p *GovernancePlugin) publishPinnedSessionTier(
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	record *SessionComplexityRecord,
) {
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, record.Tier)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismSession)
	publishComplexitySessionLogContext(ctx, state, complexitySessionTierSourceMemoised, record.SwitchCount, true)
	if p.logger != nil {
		p.logger.Debug("[Governance] Complexity session hit: tier=%s identity=%s", record.Tier, state.Source)
	}
	ctx.AppendRoutingEngineLog(
		schemas.RoutingEngineRoutingRule,
		schemas.LogLevelInfo,
		"Complexity tier held from session: tier="+record.Tier+" identity="+state.Source+
			" (no classification ran for this turn)",
	)
}

// publishCacheAwareSessionTier publishes the policy's final tier while leaving
// the classifier mechanism intact: semantic means an embedding produced a
// proposal, and skipped means it produced no tier. A held tier must not retain
// a score calculated for a different proposed tier.
func (p *GovernancePlugin) publishCacheAwareSessionTier(
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	proposed *complexity.ComplexityResult,
	decision sessionTierDecision,
	switchCount int,
	switchCountKnown bool,
) {
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, decision.Tier)
	if proposed == nil || proposed.Tier != decision.Tier {
		ctx.ClearValue(schemas.BifrostContextKeyGovernanceComplexityScore)
	}
	tierSource := complexitySessionTierSourceHeld
	if proposed != nil && proposed.Tier == decision.Tier {
		tierSource = complexitySessionTierSourceClassified
	}
	publishComplexitySessionLogContext(ctx, state, tierSource, switchCount, switchCountKnown)

	proposedTier := "none"
	if proposed != nil && proposed.Tier != "" {
		proposedTier = proposed.Tier
	}
	message := fmt.Sprintf(
		"Cache-aware complexity session: tier=%s proposed=%s switched=%t reason=%s identity=%s",
		decision.Tier,
		proposedTier,
		decision.Switched,
		decision.Reason,
		state.Source,
	)
	if p.logger != nil {
		p.logger.Debug("[Governance] %s", message)
	}
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, message)
}

// publishSessionClassificationTelemetry records a turn whose routing outcome
// came directly from this turn's classifier, either because the session is new
// or because the store failed open. The switch count stays absent unless this
// request successfully created the initial record.
func publishSessionClassificationTelemetry(
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	result *complexity.ComplexityResult,
	switchCount int,
	switchCountKnown bool,
) {
	tierSource := ""
	if result != nil && result.Tier != "" {
		tierSource = complexitySessionTierSourceClassified
	}
	publishComplexitySessionLogContext(ctx, state, tierSource, switchCount, switchCountKnown)
}

// publishComplexitySessionLogContext exposes small, request-local facts for the
// logging plugin. The raw, opaque session ID supports exact log lookup, while
// the separately derived state.Key remains hashed and scope-namespaced.
//
// This is invoked only from the lazy complexity closure. Merely enabling
// sessions therefore adds no log fields to requests whose rules never inspect
// complexity_tier.
func publishComplexitySessionLogContext(
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	tierSource string,
	switchCount int,
	switchCountKnown bool,
) {
	if ctx == nil || state == nil || state.ID == "" || state.Key == "" {
		return
	}
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexitySessionID, state.ID)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexitySessionMode, state.Config.Mode)
	if tierSource == "" {
		ctx.ClearValue(schemas.BifrostContextKeyGovernanceComplexitySessionTierSource)
	} else {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexitySessionTierSource, tierSource)
	}
	if switchCountKnown {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexitySessionSwitchCount, switchCount)
	} else {
		ctx.ClearValue(schemas.BifrostContextKeyGovernanceComplexitySessionSwitchCount)
	}
}

func complexityResultForSessionDecision(
	proposed *complexity.ComplexityResult,
	decision sessionTierDecision,
) *complexity.ComplexityResult {
	if proposed != nil && proposed.Tier == decision.Tier {
		return proposed
	}
	return &complexity.ComplexityResult{Tier: decision.Tier}
}
