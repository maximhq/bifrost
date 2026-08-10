package governance

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// complexitySessionGlobalScope namespaces sessions on deployments that route
// without virtual keys. Callers in this scope must provide globally unique
// session IDs because no narrower ownership boundary is available.
const complexitySessionGlobalScope = "global"

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
	// TTL is the sliding idle window from configuration.
	TTL time.Duration
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

	return &complexitySessionState{ID: sessionID, Source: source, Key: key, TTL: ttl}
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
		ctx.SetValue(schemas.BifrostContextKeySessionTTL, state.TTL)
	}
}

// withComplexitySession wraps the lazy classification closure so an established
// session reuses its tier instead of classifying again. That reuse is the point
// of the feature: it keeps the provider's prompt cache warm and skips the
// per-turn embedding call.
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

	return func() *complexity.ComplexityResult {
		// A store that cannot be read is not a reason to fail the request: fall
		// through and classify, which is exactly the pre-session behaviour.
		record, found, err := store.Get(ctx, state.Key, state.TTL)
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("[Governance] Complexity session lookup failed, classifying this turn: %v", err)
			}
		} else if found && record != nil && record.Tier != "" {
			p.publishSessionTier(ctx, state, record)
			return &complexity.ComplexityResult{Tier: record.Tier}
		}

		result := classify()
		// Only a real tier is worth remembering. Persisting a failed or rejected
		// classification would pin the session to "no tier" for the whole ttl,
		// turning one bad turn into a session-long outage of complexity routing.
		if result == nil || result.Tier == "" {
			return result
		}

		// RuleID is deliberately left unset. The tier is classifier memory about
		// the conversation, not state owned by whichever rule happened to consult
		// it first — binding them would let an unrelated rule edit drop every
		// live session's tier.
		created, err := store.Create(ctx, state.Key, &SessionComplexityRecord{
			Tier:      result.Tier,
			DecidedAt: time.Now(),
		}, state.TTL)
		if err != nil && p.logger != nil {
			p.logger.Warn("[Governance] Failed to persist complexity session tier, the next turn will classify again: %v", err)
		}
		if created {
			ctx.AppendRoutingEngineLog(
				schemas.RoutingEngineRoutingRule,
				schemas.LogLevelInfo,
				"Complexity session established: tier="+result.Tier+" identity="+state.Source,
			)
		}
		return result
	}
}

// publishSessionTier records a held tier the same way a fresh classification is
// recorded, so downstream logging sees a tier rather than a gap — with a
// mechanism that says it was reused, not embedded.
func (p *GovernancePlugin) publishSessionTier(
	ctx *schemas.BifrostContext,
	state *complexitySessionState,
	record *SessionComplexityRecord,
) {
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, record.Tier)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismSession)
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
