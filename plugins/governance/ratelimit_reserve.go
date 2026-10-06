package governance

import (
	"context"
	"fmt"
	"strings"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// CheckAndReserveRateLimits atomically checks rate limits AND reserves one
// request in a single CAS loop per rate-limit entry, closing the TOCTOU gap
// between the former separate CheckRateLimits / ChargeRateLimits calls.
// If the check passes, the request count is already incremented; the caller
// must call ReleaseRateLimitReservation on failure to undo it.
func (gs *LocalGovernanceStore) CheckAndReserveRateLimits(ctx context.Context, entityWiseRateLimits EntityWiseRateLimits, tokensBaselines map[string]int64, requestsBaselines map[string]int64) (Decision, []string, error) {
	sessionContinuation := isLiveSessionContinuation(ctx)
	var reserved []string // rate limit IDs we incremented

	for entity, rateLimits := range entityWiseRateLimits {
		for _, rateLimit := range rateLimits {
			decision, err := gs.checkAndReserveSingle(ctx, rateLimit, entity, sessionContinuation, tokensBaselines, requestsBaselines)
			if err != nil || isRateLimitViolation(decision) {
				// Roll back any reservations already made
				for _, id := range reserved {
					_ = gs.BumpRateLimitUsageBy(ctx, id, 0, -1)
				}
				return decision, nil, err
			}
			if !sessionContinuation {
				reserved = append(reserved, rateLimit.ID)
			}
		}
	}
	return DecisionAllow, reserved, nil
}

// checkAndReserveSingle atomically checks one rate limit and, if allowed,
// increments RequestCurrentUsage by 1 inside the same CAS loop.
// ponytail: per-entry CAS, no global lock; contention scales with entry fan-out
func (gs *LocalGovernanceStore) checkAndReserveSingle(
	ctx context.Context,
	rateLimit *configstoreTables.TableRateLimit,
	entity EntityLabel,
	sessionContinuation bool,
	tokensBaselines map[string]int64,
	requestsBaselines map[string]int64,
) (Decision, error) {
	for {
		raw, exists := gs.rateLimits.Load(rateLimit.ID)
		if !exists || raw == nil {
			return DecisionAllow, nil
		}
		old, ok := raw.(*configstoreTables.TableRateLimit)
		if !ok || old == nil {
			return DecisionAllow, nil
		}

		// Check token window expiry
		tokenLimitExpired := false
		if old.TokenResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*old.TokenResetDuration); err == nil {
				if time.Since(old.TokenLastReset) >= duration {
					tokenLimitExpired = true
				}
			}
		}
		// Check request window expiry
		requestLimitExpired := false
		if old.RequestResetDuration != nil {
			if duration, err := configstoreTables.ParseDuration(*old.RequestResetDuration); err == nil {
				if time.Since(old.RequestLastReset) >= duration {
					requestLimitExpired = true
				}
			}
		}

		tokensBaseline := tokensBaselines[old.ID]
		requestsBaseline := requestsBaselines[old.ID]

		var violations []string

		// Token limit check
		if !tokenLimitExpired && old.TokenMaxLimit != nil && old.TokenCurrentUsage+tokensBaseline >= *old.TokenMaxLimit {
			duration := "unknown"
			if old.TokenResetDuration != nil {
				duration = *old.TokenResetDuration
			}
			violations = append(violations, fmt.Sprintf("token limit exceeded (%d/%d, resets every %s)",
				old.TokenCurrentUsage+tokensBaseline, *old.TokenMaxLimit, duration))
		}

		// Request limit check (with +1 for the reservation we're about to make)
		newRequestUsage := old.RequestCurrentUsage
		if !sessionContinuation {
			newRequestUsage++ // prospective reservation
		}
		if !sessionContinuation && !requestLimitExpired && old.RequestMaxLimit != nil && newRequestUsage+requestsBaseline > *old.RequestMaxLimit {
			duration := "unknown"
			if old.RequestResetDuration != nil {
				duration = *old.RequestResetDuration
			}
			violations = append(violations, fmt.Sprintf("request limit exceeded (%d/%d, resets every %s)",
				old.RequestCurrentUsage+requestsBaseline, *old.RequestMaxLimit, duration))
		}

		if len(violations) > 0 {
			decision := DecisionRateLimited
			if len(violations) == 1 {
				if strings.Contains(violations[0], "token") {
					decision = DecisionTokenLimited
				} else if strings.Contains(violations[0], "request") {
					decision = DecisionRequestLimited
				}
			}
			return decision, fmt.Errorf("rate limit violated for %s: %s", entity, violations)
		}

		// Atomically reserve: clone with incremented request count, CAS it in
		if sessionContinuation {
			return DecisionAllow, nil
		}
		clone := *old
		clone.RequestCurrentUsage = newRequestUsage
		if gs.rateLimits.CompareAndSwap(old.ID, raw, &clone) {
			return DecisionAllow, nil
		}
		// CAS failed — another goroutine modified the entry; retry
	}
}
