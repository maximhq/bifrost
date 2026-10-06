package keyselectors

import (
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Rotator applies a provider's KeySelectionConfig across requests. It owns the state the
// stateless WeightedRandom selector does not need: round-robin positions, in-flight and
// served counters per key, and cooldowns for keys that recently hit a rate limit, an
// exhausted quota or a rejected credential.
//
// State is keyed by provider and key ID, so it survives provider hot reloads (which rebuild
// provider structs and workers) and keeps working when the pool changes between requests:
// a key that leaves the pool simply stops being chosen, and one that joins starts fresh.
type Rotator struct {
	mu        sync.Mutex
	providers map[schemas.ModelProvider]*providerState
	now       func() time.Time
}

type providerState struct {
	mu          sync.Mutex
	keys        map[string]*keyState
	stickyKeyID string
	stickyCount int
}

type keyState struct {
	rrCurrent      int64                // smooth weighted round-robin running weight
	inFlight       int64                // attempts currently running on the key
	served         uint64               // attempts the key was selected for
	cooldownUntil  time.Time            // key-wide cooldown (rejected credential)
	modelCooldowns map[string]time.Time // per-model cooldown (rate limit, quota)
}

// NewRotator returns an empty Rotator.
func NewRotator() *Rotator {
	return &Rotator{providers: make(map[schemas.ModelProvider]*providerState), now: time.Now}
}

func (r *Rotator) state(provider schemas.ModelProvider) *providerState {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps, ok := r.providers[provider]
	if !ok {
		ps = &providerState{keys: make(map[string]*keyState)}
		r.providers[provider] = ps
	}
	return ps
}

// key returns the state for keyID; the caller holds ps.mu.
func (ps *providerState) key(keyID string) *keyState {
	ks, ok := ps.keys[keyID]
	if !ok {
		ks = &keyState{}
		ps.keys[keyID] = ks
	}
	return ks
}

// coolingDown reports whether the key is cooling down for model at now, pruning expired entries.
// The caller holds ps.mu.
func (ks *keyState) coolingDown(model string, now time.Time) bool {
	if !ks.cooldownUntil.IsZero() {
		if now.Before(ks.cooldownUntil) {
			return true
		}
		ks.cooldownUntil = time.Time{}
	}
	if until, ok := ks.modelCooldowns[model]; ok {
		if now.Before(until) {
			return true
		}
		delete(ks.modelCooldowns, model)
	}
	return false
}

// Select picks a key from keys according to cfg. keys must be non-empty and is the pool
// already filtered for the request (enabled, model-compatible, not failed in this request).
// Keys cooling down from an earlier request are skipped while any other key is available;
// when every key is cooling down the whole pool is used, so a cooldown never fails a request
// on its own. fallback is the selector used by weighted_random (nil = WeightedRandom), which
// keeps a custom BifrostConfig.KeySelector in charge of the random pick.
func (r *Rotator) Select(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, cfg *schemas.KeySelectionConfig, keys []schemas.Key, fallback schemas.KeySelector) (schemas.Key, error) {
	if fallback == nil {
		fallback = WeightedRandom
	}
	if len(keys) == 0 {
		return fallback(ctx, keys, provider, model)
	}
	strategy := schemas.KeySelectionWeightedRandom
	stickyLimit := 1
	if cfg != nil {
		if cfg.Strategy != "" {
			strategy = cfg.Strategy
		}
		if cfg.StickyLimit > 1 {
			stickyLimit = cfg.StickyLimit
		}
	}

	ps := r.state(provider)
	ps.mu.Lock()
	now := r.now()
	candidates := keys
	cooling := 0
	for i := range keys {
		if ps.key(keys[i].ID).coolingDown(model, now) {
			cooling++
		}
	}
	if cooling > 0 && cooling < len(keys) {
		candidates = make([]schemas.Key, 0, len(keys)-cooling)
		for i := range keys {
			if !ps.keys[keys[i].ID].coolingDown(model, now) {
				candidates = append(candidates, keys[i])
			}
		}
	}

	var chosen schemas.Key
	switch strategy {
	case schemas.KeySelectionRoundRobin:
		chosen = ps.roundRobin(candidates, stickyLimit)
	case schemas.KeySelectionLeastUsed:
		chosen = ps.leastUsed(candidates)
	case schemas.KeySelectionFillFirst:
		chosen = candidates[0]
	default:
		ps.mu.Unlock()
		key, err := fallback(ctx, candidates, provider, model)
		if err == nil {
			ps.mu.Lock()
			ps.key(key.ID).served++
			ps.mu.Unlock()
		}
		return key, err
	}
	ps.key(chosen.ID).served++
	ps.mu.Unlock()
	return chosen, nil
}

// roundRobin runs smooth weighted round robin (the nginx algorithm) over candidates: every
// key gains its weight, the leader is chosen and pays back the total. Over any window each
// key serves in proportion to its weight, interleaved rather than in bursts. Keys with zero
// weight are only used when every candidate has zero weight, matching WeightedRandom.
// stickyLimit keeps the chosen key for that many consecutive selections. The caller holds ps.mu.
func (ps *providerState) roundRobin(candidates []schemas.Key, stickyLimit int) schemas.Key {
	if stickyLimit > 1 && ps.stickyKeyID != "" && ps.stickyCount < stickyLimit {
		for i := range candidates {
			if candidates[i].ID == ps.stickyKeyID {
				ps.stickyCount++
				return candidates[i]
			}
		}
	}
	anyWeighted := false
	for i := range candidates {
		if candidates[i].Weight > 0 {
			anyWeighted = true
			break
		}
	}
	var total int64
	best := -1
	var bestState *keyState
	for i := range candidates {
		weight := int64(1)
		if anyWeighted {
			weight = int64(candidates[i].Weight * 100)
			if weight <= 0 {
				continue
			}
		}
		ks := ps.key(candidates[i].ID)
		ks.rrCurrent += weight
		total += weight
		if best < 0 || ks.rrCurrent > bestState.rrCurrent {
			best = i
			bestState = ks
		}
	}
	if best < 0 {
		// Every weight rounded down to zero; fall back to plain order.
		best = 0
		bestState = ps.key(candidates[0].ID)
	}
	bestState.rrCurrent -= total
	ps.stickyKeyID = candidates[best].ID
	ps.stickyCount = 1
	return candidates[best]
}

// leastUsed picks the key with the fewest attempts in flight, then the fewest attempts served
// so far, then the first in configured order. The caller holds ps.mu.
func (ps *providerState) leastUsed(candidates []schemas.Key) schemas.Key {
	best := 0
	bestState := ps.key(candidates[0].ID)
	for i := 1; i < len(candidates); i++ {
		ks := ps.key(candidates[i].ID)
		if ks.inFlight < bestState.inFlight || (ks.inFlight == bestState.inFlight && ks.served < bestState.served) {
			best = i
			bestState = ks
		}
	}
	return candidates[best]
}

// Begin marks one attempt in flight on keyID and returns the function that ends it. The
// returned function is safe to call more than once; only the first call counts.
func (r *Rotator) Begin(provider schemas.ModelProvider, keyID string) func() {
	ps := r.state(provider)
	ps.mu.Lock()
	ps.key(keyID).inFlight++
	ps.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			ps.mu.Lock()
			if ks := ps.key(keyID); ks.inFlight > 0 {
				ks.inFlight--
			}
			ps.mu.Unlock()
		})
	}
}

// ReportSuccess clears any cooldown the key carried, for model and key-wide: it just proved
// it can serve.
func (r *Rotator) ReportSuccess(provider schemas.ModelProvider, keyID, model string) {
	ps := r.state(provider)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ks := ps.key(keyID)
	ks.cooldownUntil = time.Time{}
	delete(ks.modelCooldowns, model)
}

// ReportFailure puts the key on cooldown when the failure says later requests should avoid
// it: a rate limit or exhausted quota cools the key for that model (subscription quotas are
// commonly per model family), a rejected credential cools the whole key. The cooldown lasts
// retryAfter when the upstream gave one, else cfg's CooldownSeconds; a zero configured
// cooldown disables cooldowns altogether. Other failure classes leave the key alone.
func (r *Rotator) ReportFailure(provider schemas.ModelProvider, keyID, model string, class schemas.FailureClass, retryAfter time.Duration, cfg *schemas.KeySelectionConfig) {
	configured := cfg.Cooldown()
	if configured <= 0 {
		return
	}
	var keyWide bool
	switch class {
	case schemas.FailureClassRateLimit, schemas.FailureClassQuota:
		keyWide = model == ""
	case schemas.FailureClassCredential:
		keyWide = true
	default:
		return
	}
	cooldown := configured
	if retryAfter > 0 {
		cooldown = retryAfter
	}
	ps := r.state(provider)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	until := r.now().Add(cooldown)
	ks := ps.key(keyID)
	if keyWide {
		if until.After(ks.cooldownUntil) {
			ks.cooldownUntil = until
		}
	} else {
		if ks.modelCooldowns == nil {
			ks.modelCooldowns = make(map[string]time.Time)
		}
		if until.After(ks.modelCooldowns[model]) {
			ks.modelCooldowns[model] = until
		}
	}
	if ps.stickyKeyID == keyID {
		ps.stickyKeyID = ""
		ps.stickyCount = 0
	}
}
