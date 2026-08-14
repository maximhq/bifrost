package routing

import (
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// complexitySessionGlobalScope namespaces sessions on deployments that route
// without virtual keys. Callers in this scope must provide globally unique
// session IDs because no narrower ownership boundary is available.
const complexitySessionGlobalScope = "global"

// complexitySessionContextKey carries the resolved session across the request so
// the response path can record what the provider reported without repeating
// identity resolution and key derivation.
const complexitySessionContextKey schemas.BifrostContextKey = "bf-routing-complexity-session"

const (
	complexitySessionTierSourceClassified = "classified"
	complexitySessionTierSourceMemoised   = "memoised"
	complexitySessionTierSourceHeld       = "held"
)

var errSessionTierDecisionUnchanged = errors.New("complexity session tier decision unchanged")

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

// effectiveSessionTTL returns the ttl actually used for this request's store
// expiry and key-affinity window. Neither enabled mode reads config.TTL as
// part of its tier decision — pinned either reuses a stored tier
// unconditionally or classifies once and persists, and cache-aware's
// switching policy has no cache-evidence check left to bound — so it is not
// admin-tunable for either mode, and the fixed internal constant is used
// instead of whatever (possibly stale, possibly hand-edited) value
// config.TTL carries.
func effectiveSessionTTL(*configstore.ComplexitySessionConfig) time.Duration {
	return configstore.ComplexitySessionInternalTTL
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
func (p *RoutingPlugin) resolveComplexitySessionState(
	ctx *schemas.BifrostContext,
	virtualKey *configstoreTables.TableVirtualKey,
) *complexitySessionState {
	config := p.complexitySessionConfig.Load()
	if config == nil || p.complexitySessionStore.Load() == nil {
		return nil
	}

	sessionID, source, ok := complexity.ResolveSessionID(ctx, config.IdentitySources)
	if !ok {
		return nil
	}

	// Sessions are namespaced per virtual key so one tenant's conversation can
	// never adopt another's pinned tier, even if both present the same id.
	scopeID := complexitySessionGlobalScope
	if virtualKey != nil && virtualKey.ID != "" {
		scopeID = virtualKey.ID
	}
	key, ok := complexity.BuildSessionKey(scopeID, sessionID)
	if !ok {
		return nil
	}

	// Config is a per-request value copy, so overwriting TTL here with the
	// fixed internal value is safe: it never touches the shared config behind
	// p.complexitySessionConfig.Load(), which concurrent requests read
	// independently. effectiveSessionTTL always returns a positive constant,
	// so there is no non-positive case left to guard against here.
	sessionConfig := *config
	sessionConfig.TTL = effectiveSessionTTL(config)
	state := &complexitySessionState{ID: sessionID, Source: source, Key: key, Config: sessionConfig}
	// Carried on the context because the response path cannot redo this: identity
	// resolution reads the request body, which is gone by then.
	ctx.SetValue(complexitySessionContextKey, state)
	return state
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
func (p *RoutingPlugin) publishSessionKeyAffinity(ctx *schemas.BifrostContext, state *complexitySessionState) {
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
func (p *RoutingPlugin) withComplexitySession(
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

func (p *RoutingPlugin) classifyWithPinnedSession(
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

func (p *RoutingPlugin) classifyWithCacheAwareSession(
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
func (p *RoutingPlugin) persistInitialSessionTier(
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
func (p *RoutingPlugin) publishPinnedSessionTier(
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
func (p *RoutingPlugin) publishCacheAwareSessionTier(
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
		"Complexity session evaluated: tier=%s proposed=%s switched=%t reason=%s identity=%s",
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
