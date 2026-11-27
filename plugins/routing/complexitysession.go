package routing

import (
	"errors"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const (
	// complexitySessionKind is the session state kind under which the effective tier is kept.
	complexitySessionKind          = "complexity"
	complexityTurnTierKind         = "complexity-turn-tier"
	complexitySessionInactivityTTL = 24 * time.Hour

	// Harness headers that tell a session's subagents apart; see complexityAgentID.
	claudeCodeAgentIDHeader   = "x-claude-code-agent-id"
	codexParentThreadIDHeader = "x-codex-parent-thread-id"
	codexThreadIDHeader       = "thread-id"
)

var errInvalidComplexitySessionTier = errors.New("invalid stored complexity session tier")

type complexitySessionResolution struct {
	PreviousTier  string
	EffectiveTier string
	Existed       bool
	Escalated     bool
}

// The session table keeps one effective complexity tier per agent of a scoped session (see
// complexityStateKey), and the latest effective tier of its tool continuations. It holds no
// request content, classifier scores, model choices, provider-cache information or turn history.
// Reads and writes are separate operations, so overlapping requests for the same agent are
// best-effort and the last writer wins.

// loadTurnTier returns a valid tier cached for the current session, without refreshing its TTL.
func (p *RoutingPlugin) loadTurnTier(key string) (string, bool, error) {
	if p.sessions == nil {
		return "", false, nil
	}
	value, ok := p.sessions.Get(key)
	if !ok {
		return "", false, nil
	}
	tier, err := decodeStoredComplexityTier(value)
	return tier, true, err
}

// saveTurnTier stores the served tier using the request's session-sticky inactivity TTL.
func (p *RoutingPlugin) saveTurnTier(key, tier string, ttl time.Duration) error {
	if p.sessions == nil {
		return nil
	}
	if _, ok := complexityTierRank(tier); !ok {
		return fmt.Errorf("invalid complexity continuation tier %q", tier)
	}
	if ttl <= 0 {
		ttl = schemas.DefaultSessionStickyTTL
	}
	p.sessions.PutWithTTL(key, tier, ttl)
	return nil
}

// loadSessionTier returns the current unexpired tier under key. When refresh is true, an existing
// entry receives a fresh inactivity TTL. It never creates an entry.
func (p *RoutingPlugin) loadSessionTier(key string, refresh bool) (string, bool, error) {
	if p.sessions == nil {
		return "", false, nil
	}
	value, ok := p.sessions.Get(key)
	if !ok {
		return "", false, nil
	}

	tier, err := decodeStoredComplexityTier(value)
	if err != nil {
		return "", true, err
	}
	if refresh {
		p.sessions.PutWithTTL(key, tier, complexitySessionInactivityTTL)
	}
	return tier, true, nil
}

// resolveSessionTier stores max(current, proposed) under key and refreshes the inactivity TTL. An
// empty proposal refreshes an existing entry but never creates one. The tier comparison belongs
// here rather than in the table; Get and PutWithTTL are intentionally a best-effort
// read-modify-write sequence.
func (p *RoutingPlugin) resolveSessionTier(key, proposed string) (complexitySessionResolution, error) {
	if p.sessions == nil {
		return complexitySessionResolution{EffectiveTier: proposed}, nil
	}
	if proposed != "" {
		if _, ok := complexityTierRank(proposed); !ok {
			return complexitySessionResolution{}, fmt.Errorf("invalid proposed complexity tier %q", proposed)
		}
	}

	previous, existed, err := p.loadSessionTier(key, false)
	if err != nil {
		return complexitySessionResolution{}, err
	}
	if !existed && proposed == "" {
		return complexitySessionResolution{}, nil
	}

	effective := proposed
	if existed && (effective == "" || complexityTierAtLeast(previous, effective)) {
		effective = previous
	}
	p.sessions.PutWithTTL(key, effective, complexitySessionInactivityTTL)
	return complexitySessionResolution{
		PreviousTier:  previous,
		EffectiveTier: effective,
		Existed:       existed,
		Escalated:     existed && proposed != "" && effective != previous,
	}, nil
}

// complexityTierRank returns the ordering rank for a supported tier.
func complexityTierRank(tier string) (int, bool) {
	switch tier {
	case complexity.TierSimple:
		return 1, true
	case complexity.TierMedium:
		return 2, true
	case complexity.TierComplex:
		return 3, true
	default:
		return 0, false
	}
}

// complexityTierAtLeast reports whether the left tier is at least as complex as the right.
func complexityTierAtLeast(left, right string) bool {
	leftRank, leftOK := complexityTierRank(left)
	rightRank, rightOK := complexityTierRank(right)
	return leftOK && rightOK && leftRank >= rightRank
}

// decodeStoredComplexityTier validates a tier read from the session table.
func decodeStoredComplexityTier(tier string) (string, error) {
	if _, ok := complexityTierRank(tier); !ok {
		return "", fmt.Errorf("%w: %q", errInvalidComplexitySessionTier, tier)
	}
	return tier, nil
}

// complexitySessionKey is where the request's agent keeps its effective tier: the shared
// session state key, scoped to the virtual key and user the request is attributed to, under
// the complexity kind. See complexityStateKey for the agent scoping.
func complexitySessionKey(ctx *schemas.BifrostContext) string {
	return complexitySessionKeyFor(ctx, "")
}

// complexitySessionKeyFor is complexitySessionKey for one model router, so routers never
// raise each other's session tiers.
func complexitySessionKeyFor(ctx *schemas.BifrostContext, routerID string) string {
	return complexityStateKey(ctx, complexityRouterKind(complexitySessionKind, routerID))
}

// complexityTurnTierKey scopes continuation tier state to the request's session, caller and
// agent, so a subagent's tool loop never resumes on a tier its parent served, or the reverse.
func complexityTurnTierKey(ctx *schemas.BifrostContext) string {
	return complexityTurnTierKeyFor(ctx, "")
}

// complexityTurnTierKeyFor is complexityTurnTierKey for one model router.
func complexityTurnTierKeyFor(ctx *schemas.BifrostContext, routerID string) string {
	return complexityStateKey(ctx, complexityRouterKind(complexityTurnTierKind, routerID))
}

// complexityRouterKind scopes a state kind to a model router. The Complexity Router page's
// config, which has no router id, keeps the kinds written before routers existed, so its
// session state is unchanged.
func complexityRouterKind(kind, routerID string) string {
	if routerID == "" {
		return kind
	}
	return kind + ":" + routerID
}

// complexityStateKey keys complexity state per agent rather than per session. Harnesses run
// subagents under their parent's session id, yet each subagent works from its own prompt and
// transcript: sharing one entry would route every subagent on its parent's tier and let a
// subagent's tier escalate the parent. The main conversation adds no key part, so its keys
// stay identical to those written before agents were told apart.
func complexityStateKey(ctx *schemas.BifrostContext, kind string) string {
	if agentID := complexityAgentID(ctx); agentID != "" {
		return bifrost.SessionStateKey(ctx, kind, agentID)
	}
	return bifrost.SessionStateKey(ctx, kind)
}

// complexityAgentID names the subagent that issued the request, or "" for a session's main
// conversation and for clients that do not identify their agents. Claude Code sends an agent
// id only from agents it spawned; Codex names every thread, so its thread id counts only on a
// request that also names a parent thread. OpenCode needs no entry because it gives each
// subagent a session of its own.
func complexityAgentID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if agentID := strings.TrimSpace(headers[claudeCodeAgentIDHeader]); agentID != "" {
		return agentID
	}
	if strings.TrimSpace(headers[codexParentThreadIDHeader]) != "" {
		return strings.TrimSpace(headers[codexThreadIDHeader])
	}
	return ""
}

// complexityTurnTierTTL honors the request's session TTL, matching provider stickiness.
func complexityTurnTierTTL(ctx *schemas.BifrostContext) time.Duration {
	if ctx != nil {
		if ttl, ok := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration); ok && ttl > 0 {
			return ttl
		}
	}
	return schemas.DefaultSessionStickyTTL
}
