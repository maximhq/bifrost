package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func upstreamError(status int, errType string, retryAfterMs int64) *schemas.BifrostError {
	return &schemas.BifrostError{
		StatusCode:  schemas.Ptr(status),
		Error:       &schemas.ErrorField{Type: schemas.Ptr(errType), Message: "refused"},
		ExtraFields: schemas.BifrostErrorExtraFields{RetryAfter: retryAfterMs},
	}
}

func findUpstreamHealth(t *testing.T, snap []schemas.UpstreamHealth, provider schemas.ModelProvider, model string) schemas.UpstreamHealth {
	t.Helper()
	for _, h := range snap {
		if h.Provider == provider && h.Model == model {
			return h
		}
	}
	t.Fatalf("no upstream health entry for %s/%s in %+v", provider, model, snap)
	return schemas.UpstreamHealth{}
}

func TestUpstreamHealthRateLimitCoolsDownUntilRetryAfter(t *testing.T) {
	b := &Bifrost{}
	b.recordUpstreamOutcome(nil, schemas.Anthropic, "claude-x", upstreamError(429, "rate_limit_error", 20_000))

	now := time.Now()
	h := findUpstreamHealth(t, b.UpstreamHealthSnapshot(now), schemas.Anthropic, "claude-x")
	if h.Available || h.Status != schemas.UpstreamHealthCoolingDown {
		t.Fatalf("expected cooling_down, got %+v", h)
	}
	if h.FailureClass != schemas.FailureClassRateLimit || h.EffectiveRemainingFraction != 0 {
		t.Fatalf("unexpected class/fraction: %+v", h)
	}
	if h.RetryAfterMs <= 0 || h.RetryAfterMs > 20_000 || h.RecentRateLimits != 1 || h.ConsecutiveFailures != 1 {
		t.Fatalf("unexpected cooldown accounting: %+v", h)
	}

	later := findUpstreamHealth(t, b.UpstreamHealthSnapshot(now.Add(25*time.Second)), schemas.Anthropic, "claude-x")
	if !later.Available || later.Status != schemas.UpstreamHealthDegraded || later.EffectiveRemainingFraction != 1 {
		t.Fatalf("expected degraded but available after the hint, got %+v", later)
	}
}

func TestUpstreamHealthSuccessClearsCooldown(t *testing.T) {
	b := &Bifrost{}
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(429, "insufficient_quota", 0))
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", nil)

	h := findUpstreamHealth(t, b.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-x")
	if !h.Available || h.Status != schemas.UpstreamHealthAvailable || h.ConsecutiveFailures != 0 || h.LastSuccessAt == nil {
		t.Fatalf("expected available after success, got %+v", h)
	}
}

func TestUpstreamHealthIgnoresCallerFaultsAndUntrackedSuccesses(t *testing.T) {
	b := &Bifrost{}
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(400, "invalid_request_error", 0))
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-y", nil)
	if snap := b.UpstreamHealthSnapshot(time.Now()); len(snap) != 0 {
		t.Fatalf("expected no tracked pairs, got %+v", snap)
	}
}

func TestUpstreamHealthCredentialsExhaustedUsesAttemptTrail(t *testing.T) {
	b := &Bifrost{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{
		{Attempt: 0, FailureClass: schemas.FailureClassRateLimit, StatusCode: schemas.Ptr(429), RetryAfter: 40_000},
		{Attempt: 1, FailureClass: schemas.FailureClassRateLimit, StatusCode: schemas.Ptr(429), RetryAfter: 15_000},
	})
	exhausted := &schemas.BifrostError{
		StatusCode:  schemas.Ptr(502),
		Type:        schemas.Ptr("upstream_credentials_exhausted"),
		Error:       &schemas.ErrorField{Type: schemas.Ptr("upstream_credentials_exhausted"), Message: "all keys dead"},
		ExtraFields: schemas.BifrostErrorExtraFields{ErrorType: schemas.ErrorTypeProviderCredentialsExhausted},
	}
	b.recordUpstreamOutcome(ctx, schemas.Gemini, "gemini-x", exhausted)

	h := findUpstreamHealth(t, b.UpstreamHealthSnapshot(time.Now()), schemas.Gemini, "gemini-x")
	if h.Available || h.FailureClass != schemas.FailureClassRateLimit || h.StatusCode == nil || *h.StatusCode != 429 {
		t.Fatalf("expected the trail's rate limit to stand for the exhausted pool, got %+v", h)
	}
	if h.RetryAfterMs > 15_000 {
		t.Fatalf("expected the soonest key's hint, got %d ms", h.RetryAfterMs)
	}
}

// A failure on a tracked pair keeps the tracker locked until it holds the pair: pruneLocked takes
// the tracker's write lock, so it must not be able to drop the pair between the lookup and the
// update, or the update lands on a pair no snapshot reports.
func TestUpstreamHealthFailureHoldsTrackerUntilPairLocked(t *testing.T) {
	b := &Bifrost{}
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(503, "server_error", 0))
	st := b.upstreamHealth.entries[upstreamHealthKey{schemas.OpenAI, "gpt-x"}]
	if st == nil {
		t.Fatal("expected the first failure to track the pair")
	}

	st.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(503, "server_error", 0))
	}()
	// While the second failure waits for the pair, a writer must not get the tracker. Two misses in
	// a row rule out catching the lookup's own brief read lock.
	misses := 0
	for deadline := time.Now().Add(2 * time.Second); misses < 2 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if b.upstreamHealth.mu.TryLock() {
			b.upstreamHealth.mu.Unlock()
			misses = 0
			continue
		}
		misses++
	}
	st.mu.Unlock()
	<-done
	if misses < 2 {
		t.Fatal("recordFailure released the tracker lock before locking the pair; pruning could drop the pair in between")
	}
	if h := findUpstreamHealth(t, b.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-x"); h.ConsecutiveFailures != 2 {
		t.Fatalf("expected both failures on the tracked pair, got %+v", h)
	}
}

// Every failure inside the reporting window is counted, however many there are, and none
// outside it.
func TestUpstreamHealthCountsEveryFailureInWindow(t *testing.T) {
	b := &Bifrost{}
	const failures = 40
	for range failures {
		b.recordUpstreamOutcome(nil, schemas.Anthropic, "claude-x", upstreamError(429, "rate_limit_error", 0))
	}

	now := time.Now()
	h := findUpstreamHealth(t, b.UpstreamHealthSnapshot(now), schemas.Anthropic, "claude-x")
	if h.RecentFailures != failures || h.RecentRateLimits != failures || h.ConsecutiveFailures != failures {
		t.Fatalf("expected all %d failures counted, got recent=%d rate_limits=%d consecutive=%d", failures, h.RecentFailures, h.RecentRateLimits, h.ConsecutiveFailures)
	}
	if later := findUpstreamHealth(t, b.UpstreamHealthSnapshot(now.Add(schemas.UpstreamHealthWindow-10*time.Second)), schemas.Anthropic, "claude-x"); later.RecentFailures != failures {
		t.Fatalf("expected all %d failures still inside the window, got %d", failures, later.RecentFailures)
	}
	if past := findUpstreamHealth(t, b.UpstreamHealthSnapshot(now.Add(schemas.UpstreamHealthWindow+2*time.Second)), schemas.Anthropic, "claude-x"); past.RecentFailures != 0 || past.RecentRateLimits != 0 {
		t.Fatalf("expected no failures once the window has passed, got %+v", past)
	}
}
