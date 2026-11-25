package bifrost

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Upstream health tracking.
//
// Every provider attempt chain (one provider in a fallback list, after core's key rotation and
// retries) ends in a success or a final error. The final error is what tells a client that this
// provider cannot serve this model right now: core already walked every key it could, so a
// rate limit or quota refusal that survives the walk is the provider's, not one key's. The
// tracker keeps that verdict per provider/model pair with a cooldown, so a client choosing
// between routes can skip a pair Bifrost knows will be refused before it spends a request on it.
//
// A streamed chain ends when its stream does: a stream that fails after reaching the client
// counts as a failure but starts no cooldown, since no other key could take over by then.

const (
	// upstreamRateLimitCooldown is the cooldown after a rate limit the provider gave no
	// Retry-After hint for, multiplied by the consecutive failures (capped below).
	upstreamRateLimitCooldown = 30 * time.Second
	// upstreamRateLimitCooldownMax caps the escalated rate-limit cooldown.
	upstreamRateLimitCooldownMax = 5 * time.Minute
	// upstreamRefusalCooldown is the cooldown after a refusal waiting does not fix on its own
	// timescale (exhausted quota, rejected credential, no model access, region block, retired
	// model). It is a re-probe interval: the next request after it either clears the pair or
	// starts a new cooldown.
	upstreamRefusalCooldown = 10 * time.Minute
	// upstreamHealthMaxEntries bounds the tracked pairs; idle pairs are dropped beyond it.
	upstreamHealthMaxEntries = 4096
	// upstreamHealthIdleTTL is how long a pair with no new failure is kept once its cooldown
	// has passed.
	upstreamHealthIdleTTL = time.Hour
	// upstreamFailureBuckets is the number of one-second failure counters kept per pair, one for
	// each second of the reporting window, so every failure inside the window is counted.
	upstreamFailureBuckets = int64(schemas.UpstreamHealthWindow / time.Second)
)

type upstreamHealthKey struct {
	provider schemas.ModelProvider
	model    string
}

// upstreamFailureBucket counts the failures a pair saw in one second.
type upstreamFailureBucket struct {
	second     int64 // Unix second the counts belong to
	failures   int32
	rateLimits int32
}

type upstreamHealthState struct {
	mu                  sync.Mutex
	failureClass        schemas.FailureClass
	statusCode          *int
	cooldownUntil       time.Time
	lastFailureAt       time.Time
	lastSuccessAt       time.Time
	consecutiveFailures int
	recent              [upstreamFailureBuckets]upstreamFailureBucket
}

// upstreamHealthTracker records per provider/model upstream outcomes. The zero value is ready.
type upstreamHealthTracker struct {
	mu      sync.RWMutex
	entries map[upstreamHealthKey]*upstreamHealthState
}

// upstreamCooldown returns how long a pair stays unavailable after a final failure of class,
// and false for a class that does not make the pair unavailable.
func upstreamCooldown(class schemas.FailureClass, retryAfterMs int64, consecutive int) (time.Duration, bool) {
	hint := time.Duration(retryAfterMs) * time.Millisecond
	switch class {
	case schemas.FailureClassRateLimit:
		if hint > 0 {
			return hint, true
		}
		d := upstreamRateLimitCooldown * time.Duration(max(consecutive, 1))
		return min(d, upstreamRateLimitCooldownMax), true
	case schemas.FailureClassQuota, schemas.FailureClassCredential, schemas.FailureClassModelAccess,
		schemas.FailureClassRegionBlocked, schemas.FailureClassModelGone:
		return max(hint, upstreamRefusalCooldown), true
	}
	return 0, false
}

// recordsUpstreamFailure reports whether a final failure of class says anything about the
// upstream. Caller faults, cancellations and failures Bifrost cannot classify are the
// request's or the gateway's, not the provider's, and leave the pair as it was.
func recordsUpstreamFailure(class schemas.FailureClass) bool {
	switch class {
	case schemas.FailureClassRateLimit, schemas.FailureClassQuota, schemas.FailureClassCredential,
		schemas.FailureClassModelAccess, schemas.FailureClassRegionBlocked, schemas.FailureClassModelGone,
		schemas.FailureClassTransient, schemas.FailureClassTimeout:
		return true
	}
	return false
}

// upstreamFinalFailure classifies the final error of one provider attempt chain. When core
// ends the chain with its synthetic 502 upstream_credentials_exhausted (every key in the pool
// was refused), that error carries no upstream class of its own, so the class and Retry-After
// hint of the last upstream answer in the attempt trail stand for it. Core resets the trail on
// the chain's first attempt and returns that error only after attempts on it, so the trail is
// this chain's. Each trail hint counts from when its answer arrived, so the hint returned is what
// is left of it at now: a cooldown starting at now ends no later than that answer plus its hint.
func upstreamFinalFailure(ctx *schemas.BifrostContext, err *schemas.BifrostError, now time.Time) (schemas.FailureClass, *int, int64) {
	class := ClassifyFailure(err)
	statusCode := err.StatusCode
	retryAfter := err.ExtraFields.RetryAfter
	if ctx == nil || err.ExtraFields.ErrorType != schemas.ErrorTypeProviderCredentialsExhausted {
		return class, statusCode, retryAfter
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	for i := len(trail) - 1; i >= 0; i-- {
		if recordsUpstreamFailure(trail[i].FailureClass) {
			// A rate-limited pool is free again when the soonest key is: take the smallest hint.
			// A hint whose wait has already passed leaves 1 ms, so it still stands for the
			// provider's wait rather than falling back to the hintless cooldown.
			hint := int64(0)
			for j := range trail {
				if trail[j].FailureClass != trail[i].FailureClass || trail[j].RetryAfter <= 0 {
					continue
				}
				left := trail[j].RetryAfter
				if !trail[j].AnsweredAt.IsZero() {
					left = max(left-now.Sub(trail[j].AnsweredAt).Milliseconds(), 1)
				}
				if hint == 0 || left < hint {
					hint = left
				}
			}
			return trail[i].FailureClass, trail[i].StatusCode, hint
		}
	}
	return class, statusCode, retryAfter
}

// recordSuccess marks a pair as serving. Pairs never seen failing are not tracked, so the
// success path takes only a read lock.
func (t *upstreamHealthTracker) recordSuccess(provider schemas.ModelProvider, model string, now time.Time) {
	if model == "" {
		return
	}
	t.mu.RLock()
	st := t.entries[upstreamHealthKey{provider, model}]
	t.mu.RUnlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	st.lastSuccessAt = now
	st.consecutiveFailures = 0
	st.cooldownUntil = time.Time{}
	st.mu.Unlock()
}

// recordFailure records the final failure of one provider attempt chain. accepted reports that
// the upstream had accepted the request before failing it (a stream that failed after reaching
// the client): that counts as a failure, but no other key could have taken over and the upstream
// did serve the pair, so it ends any cooldown instead of starting one.
func (t *upstreamHealthTracker) recordFailure(provider schemas.ModelProvider, model string, class schemas.FailureClass, statusCode *int, retryAfterMs int64, accepted bool, now time.Time) {
	if model == "" || !recordsUpstreamFailure(class) {
		return
	}
	key := upstreamHealthKey{provider, model}
	// t.mu stays held until the pair is locked: pruneLocked takes t.mu for writing, so it cannot
	// drop the pair between the lookup and this update.
	t.mu.RLock()
	st := t.entries[key]
	if st != nil {
		st.mu.Lock()
		t.mu.RUnlock()
	} else {
		t.mu.RUnlock()
		t.mu.Lock()
		if t.entries == nil {
			t.entries = make(map[upstreamHealthKey]*upstreamHealthState)
		}
		if st = t.entries[key]; st == nil {
			if len(t.entries) >= upstreamHealthMaxEntries {
				t.pruneLocked(now)
			}
			if len(t.entries) >= upstreamHealthMaxEntries {
				t.mu.Unlock()
				return
			}
			st = &upstreamHealthState{}
			t.entries[key] = st
		}
		st.mu.Lock()
		t.mu.Unlock()
	}
	defer st.mu.Unlock()
	st.consecutiveFailures++
	st.failureClass = class
	if statusCode != nil {
		code := *statusCode
		st.statusCode = &code
	} else {
		st.statusCode = nil
	}
	st.lastFailureAt = now
	sec := now.Unix()
	bucket := &st.recent[sec%upstreamFailureBuckets]
	if bucket.second != sec {
		*bucket = upstreamFailureBucket{second: sec}
	}
	bucket.failures++
	if class == schemas.FailureClassRateLimit || class == schemas.FailureClassQuota {
		bucket.rateLimits++
	}
	if accepted {
		st.cooldownUntil = time.Time{}
		return
	}
	if d, ok := upstreamCooldown(class, retryAfterMs, st.consecutiveFailures); ok {
		if until := now.Add(d); until.After(st.cooldownUntil) {
			st.cooldownUntil = until
		}
	}
}

// pruneLocked drops pairs whose cooldown has passed and that saw no failure within the idle
// TTL. Callers hold t.mu for writing.
func (t *upstreamHealthTracker) pruneLocked(now time.Time) {
	for key, st := range t.entries {
		st.mu.Lock()
		idle := now.After(st.cooldownUntil) && now.Sub(st.lastFailureAt) > upstreamHealthIdleTTL
		st.mu.Unlock()
		if idle {
			delete(t.entries, key)
		}
	}
}

// snapshot returns every tracked pair, sorted by provider then model. EffectiveRemainingFraction
// reflects only the upstream state (0 or 1); callers fold their own static limits into it.
func (t *upstreamHealthTracker) snapshot(now time.Time) []schemas.UpstreamHealth {
	nowSec := now.Unix()
	t.mu.RLock()
	out := make([]schemas.UpstreamHealth, 0, len(t.entries))
	for key, st := range t.entries {
		st.mu.Lock()
		h := schemas.UpstreamHealth{
			Provider:            key.provider,
			Model:               key.model,
			Status:              schemas.UpstreamHealthAvailable,
			Available:           true,
			FailureClass:        st.failureClass,
			ConsecutiveFailures: st.consecutiveFailures,
		}
		if st.statusCode != nil {
			code := *st.statusCode
			h.StatusCode = &code
		}
		if !st.lastFailureAt.IsZero() {
			at := st.lastFailureAt
			h.LastFailureAt = &at
		}
		if !st.lastSuccessAt.IsZero() {
			at := st.lastSuccessAt
			h.LastSuccessAt = &at
		}
		for i := range st.recent {
			if bucket := &st.recent[i]; bucket.second <= nowSec && nowSec-bucket.second < upstreamFailureBuckets {
				h.RecentFailures += int(bucket.failures)
				h.RecentRateLimits += int(bucket.rateLimits)
			}
		}
		switch {
		case now.Before(st.cooldownUntil):
			until := st.cooldownUntil
			h.Status = schemas.UpstreamHealthCoolingDown
			h.Available = false
			h.CooldownUntil = &until
			h.RetryAfterMs = until.Sub(now).Milliseconds()
		case st.consecutiveFailures > 0 && now.Sub(st.lastFailureAt) <= schemas.UpstreamHealthWindow:
			h.Status = schemas.UpstreamHealthDegraded
		}
		if h.Available {
			h.EffectiveRemainingFraction = 1
		}
		st.mu.Unlock()
		out = append(out, h)
	}
	t.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// UpstreamHealthSnapshot reports what this Bifrost instance has recently observed from each
// upstream provider/model pair it dispatched to: which pairs are cooling down after their
// whole key pool was refused, until when, and recent failure counts. Pairs that have only
// ever succeeded are omitted; an absent pair is available.
func (bifrost *Bifrost) UpstreamHealthSnapshot(now time.Time) []schemas.UpstreamHealth {
	return bifrost.upstreamHealth.snapshot(now)
}

// upstreamPoolShared reports whether a provider attempt chain runs on the provider's shared key
// pool, the keys any request for the pair can be served from. It does not when the request
// brings its own credential (a direct key, or its own token with key selection skipped), is
// pinned to one key by ID or name (by the caller, a routing rule or a fallback), or is limited
// by governance to some of the keys. Every reader sees the same tracker, so only a chain on the
// shared pool is recorded: a request-scoped pool being refused says nothing about the keys
// other requests are served from.
func upstreamPoolShared(ctx *schemas.BifrostContext, baseProvider schemas.ModelProvider, requestType schemas.RequestType) bool {
	if ctx == nil {
		return true
	}
	if _, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		return false
	}
	if skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); skip && isKeySkippingAllowed(baseProvider, requestType) {
		return false
	}
	if id, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); strings.TrimSpace(id) != "" {
		return false
	}
	if name, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); strings.TrimSpace(name) != "" {
		return false
	}
	_, restricted := ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys).([]string)
	return !restricted
}

// recordUpstreamOutcome records the outcome of one provider attempt chain.
func (bifrost *Bifrost) recordUpstreamOutcome(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, err *schemas.BifrostError) {
	now := time.Now()
	if err == nil {
		bifrost.upstreamHealth.recordSuccess(provider, model, now)
		return
	}
	class, statusCode, retryAfter := upstreamFinalFailure(ctx, err, now)
	bifrost.upstreamHealth.recordFailure(provider, model, class, statusCode, retryAfter, false, now)
}

// recordUpstreamStreamEnd records how a stream that reached the client ended. A failure counts
// toward the pair's failures but starts no cooldown: once the client had the stream no other
// key could take over, and the upstream had served the pair.
func (bifrost *Bifrost) recordUpstreamStreamEnd(provider schemas.ModelProvider, model string, err *schemas.BifrostError) {
	now := time.Now()
	if err == nil {
		bifrost.upstreamHealth.recordSuccess(provider, model, now)
		return
	}
	bifrost.upstreamHealth.recordFailure(provider, model, ClassifyFailure(err), err.StatusCode, 0, true, now)
}
