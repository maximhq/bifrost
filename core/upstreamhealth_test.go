package bifrost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// A trail hint counts from the answer that carried it, not from when the chain ended: time spent
// on later keys comes off it, so the pair cools down no later than that answer plus its hint.
func TestUpstreamHealthTrailHintCountsFromAnswer(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{
		{Attempt: 0, FailureClass: schemas.FailureClassRateLimit, StatusCode: schemas.Ptr(429), RetryAfter: 20_000, AnsweredAt: t0},
		{Attempt: 1, FailureClass: schemas.FailureClassRateLimit, StatusCode: schemas.Ptr(429), RetryAfter: 30_000, AnsweredAt: t0.Add(15 * time.Second)},
	})
	exhausted := &schemas.BifrostError{
		StatusCode:  schemas.Ptr(502),
		Error:       &schemas.ErrorField{Message: "all keys dead"},
		ExtraFields: schemas.BifrostErrorExtraFields{ErrorType: schemas.ErrorTypeProviderCredentialsExhausted},
	}
	end := t0.Add(18 * time.Second)
	class, status, hint := upstreamFinalFailure(ctx, exhausted, end)
	if class != schemas.FailureClassRateLimit || status == nil || *status != 429 {
		t.Fatalf("expected the trail's rate limit, got %s %v", class, status)
	}
	if hint != 2_000 {
		t.Fatalf("expected the first key's hint less the 18s since its answer (2000 ms), got %d ms", hint)
	}

	var tracker upstreamHealthTracker
	tracker.recordFailure(schemas.Gemini, "gemini-x", class, status, hint, false, end)
	if h := findUpstreamHealth(t, tracker.snapshot(t0.Add(20*time.Second-time.Millisecond)), schemas.Gemini, "gemini-x"); h.Available {
		t.Fatalf("expected cooling down before the first answer plus its hint, got %+v", h)
	}
	if h := findUpstreamHealth(t, tracker.snapshot(t0.Add(20*time.Second)), schemas.Gemini, "gemini-x"); !h.Available {
		t.Fatalf("expected available at the first answer plus its hint, got %+v", h)
	}

	// A hint whose wait has fully passed still stands for the provider's wait, not the hintless
	// escalating cooldown.
	if _, _, hint := upstreamFinalFailure(ctx, exhausted, t0.Add(time.Minute)); hint != 1 {
		t.Fatalf("expected an elapsed hint to leave 1 ms, got %d ms", hint)
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

// newUpstreamHealthTestClient serves two OpenAI keys, key-a and key-b, from handler.
func newUpstreamHealthTestClient(t *testing.T, handler http.HandlerFunc) *Bifrost {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 4, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "key-a", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 100},
		{ID: "key-b", Name: "key-b", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	return newStreamTestClient(t, account)
}

func rateLimitedHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	fmt.Fprint(w, `{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
}

func upstreamHealthChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
	}
}

// A chain on a request-scoped pool (a key the caller pinned or brought, or the keys governance
// limits the request to) says nothing about the keys other requests are served from, so its
// refusal must not cool the pair down for every reader. A chain on the shared pool still does.
func TestUpstreamHealthRecordsOnlySharedPoolChains(t *testing.T) {
	scoped := []struct {
		name  string
		scope func(ctx *schemas.BifrostContext)
	}{
		{"key id", func(ctx *schemas.BifrostContext) { ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "key-a") }},
		{"key name", func(ctx *schemas.BifrostContext) { ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "key-a") }},
		{"direct key", func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "direct", Value: *schemas.NewSecretVar("sk-direct")})
		}},
		{"governance key restriction", func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, []string{"key-a"})
		}},
	}
	for _, tc := range scoped {
		t.Run(tc.name, func(t *testing.T) {
			client := newUpstreamHealthTestClient(t, rateLimitedHandler)
			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
			tc.scope(ctx)
			if _, err := client.ChatCompletionRequest(ctx, upstreamHealthChatRequest()); err == nil {
				t.Fatal("expected the rate-limited upstream to fail the request")
			}
			if snap := client.UpstreamHealthSnapshot(time.Now()); len(snap) != 0 {
				t.Fatalf("a request-scoped pool's refusal must not be recorded for every reader, got %+v", snap)
			}
		})
	}

	t.Run("shared pool", func(t *testing.T) {
		client := newUpstreamHealthTestClient(t, rateLimitedHandler)
		ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
		if _, err := client.ChatCompletionRequest(ctx, upstreamHealthChatRequest()); err == nil {
			t.Fatal("expected the rate-limited upstream to fail the request")
		}
		h := findUpstreamHealth(t, client.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-4o-mini")
		if h.Available || h.Status != schemas.UpstreamHealthCoolingDown || h.FailureClass != schemas.FailureClassRateLimit {
			t.Fatalf("expected the shared pool's refusal to cool the pair down, got %+v", h)
		}
	})
}

// A stream records how it ended, not only that it started: one that fails after reaching the
// client counts as a failure (without a cooldown, since no other key could take over by then),
// a startup failure is recorded once, and a stream that completes clears the pair.
func TestUpstreamHealthRecordsStreamOutcome(t *testing.T) {
	const (
		midStreamFailure = iota + 1
		startupRateLimit
		completes
	)
	var phase atomic.Int32
	client := newUpstreamHealthTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch phase.Load() {
		case midStreamFailure:
			sseHandler(
				`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}`,
				`{"error":{"message":"The server had an error while processing your request","type":"server_error"}}`,
			)(w, r)
		case startupRateLimit:
			sseHandler(`{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`)(w, r)
		default:
			sseHandler(
				`{"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
				`{"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
			)(w, r)
		}
	})
	stream := func() (string, []string, *schemas.BifrostError) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
		ch, err := client.ChatCompletionStreamRequest(ctx, upstreamHealthChatRequest())
		if ch == nil {
			return "", nil, err
		}
		content, errs := drainChatStream(ch)
		return content, errs, err
	}
	pair := func() schemas.UpstreamHealth {
		return findUpstreamHealth(t, client.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-4o-mini")
	}

	phase.Store(midStreamFailure)
	if content, errs, err := stream(); err != nil || content != "he" || len(errs) != 1 {
		t.Fatalf("expected a stream that fails after its first chunk, got content=%q errs=%v err=%v", content, errs, err)
	}
	if h := pair(); !h.Available || h.Status != schemas.UpstreamHealthDegraded || h.FailureClass != schemas.FailureClassTransient ||
		h.ConsecutiveFailures != 1 || h.RecentFailures != 1 {
		t.Fatalf("expected the failed stream to leave the pair degraded but available, got %+v", h)
	}

	phase.Store(startupRateLimit)
	if _, _, err := stream(); err == nil {
		t.Fatal("expected the rate-limited stream to fail before it started")
	}
	if h := pair(); h.Available || h.Status != schemas.UpstreamHealthCoolingDown || h.ConsecutiveFailures != 2 || h.RecentFailures != 2 || h.RecentRateLimits != 1 {
		t.Fatalf("expected the startup refusal recorded once and cooling the pair down, got %+v", h)
	}

	phase.Store(completes)
	if content, errs, err := stream(); err != nil || content != "hello" || len(errs) != 0 {
		t.Fatalf("expected a completed stream, got content=%q errs=%v err=%v", content, errs, err)
	}
	if h := pair(); !h.Available || h.Status != schemas.UpstreamHealthAvailable || h.ConsecutiveFailures != 0 || h.LastSuccessAt == nil {
		t.Fatalf("expected the completed stream to clear the pair, got %+v", h)
	}
}
