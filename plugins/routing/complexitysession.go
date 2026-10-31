package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const (
	// complexitySessionKind is the session state kind under which the effective tier is kept.
	complexitySessionKind          = "complexity"
	complexityTurnTierKind         = "complexity-turn-tier"
	complexitySessionInactivityTTL = 24 * time.Hour
)

var errInvalidComplexitySessionTier = errors.New("invalid stored complexity session tier")

// complexitySessionStore retains one effective complexity tier per scoped
// session. It stores no request content, classifier scores, model choices,
// provider-cache information, or turn history. The router calculates the
// highest tier before refreshing the entry's TTL. Because KVStore exposes the
// read and write as separate operations, overlapping requests for the same
// session are best-effort and resolve by last writer wins.
type complexitySessionStore struct {
	store schemas.KVStore
	ttl   time.Duration
}

// complexityTurnTierStore keeps the latest effective tier for tool continuations.
type complexityTurnTierStore struct {
	store schemas.KVStore
}

// load returns a valid tier cached for the current session, without refreshing its TTL.
func (s *complexityTurnTierStore) load(key string) (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, nil
	}
	value, err := s.store.Get(key)
	if errors.Is(err, kvstore.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	tier, err := decodeStoredComplexityTier(value)
	return tier, true, err
}

// save stores the served tier using the request's session-sticky inactivity TTL.
func (s *complexityTurnTierStore) save(key, tier string, ttl time.Duration) error {
	if s == nil || s.store == nil {
		return nil
	}
	if _, ok := complexityTierRank(tier); !ok {
		return fmt.Errorf("invalid complexity continuation tier %q", tier)
	}
	if ttl <= 0 {
		ttl = schemas.DefaultSessionStickyTTL
	}
	return s.store.SetWithTTL(key, tier, ttl)
}

type complexitySessionResolution struct {
	PreviousTier  string
	EffectiveTier string
	Existed       bool
	Escalated     bool
}

// newComplexitySessionStore creates monotonic session-tier storage.
func newComplexitySessionStore(store schemas.KVStore, ttl time.Duration) *complexitySessionStore {
	return &complexitySessionStore{store: store, ttl: ttl}
}

// load returns the current unexpired tier. When refresh is true, an existing
// entry receives a fresh inactivity TTL. It never creates an entry.
func (s *complexitySessionStore) load(key string, refresh bool) (string, bool, error) {
	if s == nil || s.store == nil {
		return "", false, nil
	}
	value, err := s.store.Get(key)
	if errors.Is(err, kvstore.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	tier, err := decodeStoredComplexityTier(value)
	if err != nil {
		return "", true, err
	}
	if refresh {
		if err := s.store.SetWithTTL(key, tier, s.ttl); err != nil {
			return "", true, err
		}
	}
	return tier, true, nil
}

// resolve stores max(current, proposed) and refreshes the inactivity TTL. An
// empty proposal refreshes an existing entry but never creates one. The tier
// comparison belongs here rather than in the generic KV store; Get and
// SetWithTTL are intentionally a best-effort read-modify-write sequence.
func (s *complexitySessionStore) resolve(key, proposed string) (complexitySessionResolution, error) {
	if s == nil || s.store == nil {
		return complexitySessionResolution{EffectiveTier: proposed}, nil
	}
	if proposed != "" {
		if _, ok := complexityTierRank(proposed); !ok {
			return complexitySessionResolution{}, fmt.Errorf("invalid proposed complexity tier %q", proposed)
		}
	}

	previous, existed, err := s.load(key, false)
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
	if err := s.store.SetWithTTL(key, effective, s.ttl); err != nil {
		return complexitySessionResolution{}, err
	}
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

// decodeStoredComplexityTier validates and canonicalizes a tier read from KV storage.
func decodeStoredComplexityTier(value any) (string, error) {
	var tier string
	switch typed := value.(type) {
	case string:
		tier = typed
	case []byte:
		if err := json.Unmarshal(typed, &tier); err != nil {
			tier = string(typed)
		}
	default:
		return "", fmt.Errorf("%w: value has type %T", errInvalidComplexitySessionTier, value)
	}
	if _, ok := complexityTierRank(tier); !ok {
		return "", fmt.Errorf("%w: %q", errInvalidComplexitySessionTier, tier)
	}
	return tier, nil
}

// complexitySessionKey is where the request's session keeps its effective tier: the shared
// session state key, scoped to the virtual key and user the request is attributed to, under
// the complexity kind.
func complexitySessionKey(ctx *schemas.BifrostContext) string {
	return bifrost.SessionStateKey(ctx, complexitySessionKind)
}

// complexityTurnTierKey scopes continuation tier state to the request's session and caller.
func complexityTurnTierKey(ctx *schemas.BifrostContext) string {
	return bifrost.SessionStateKey(ctx, complexityTurnTierKind)
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
