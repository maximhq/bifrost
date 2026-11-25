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
	b.recordUpstreamOutcome(nil, schemas.Anthropic, "claude-x", upstreamError(429, "rate_limit_error", 20_000), 0)

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
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(429, "insufficient_quota", 0), 0)
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", nil, 0)

	h := findUpstreamHealth(t, b.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-x")
	if !h.Available || h.Status != schemas.UpstreamHealthAvailable || h.ConsecutiveFailures != 0 || h.LastSuccessAt == nil {
		t.Fatalf("expected available after success, got %+v", h)
	}
}

func TestUpstreamHealthIgnoresCallerFaultsAndUntrackedSuccesses(t *testing.T) {
	b := &Bifrost{}
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(400, "invalid_request_error", 0), 0)
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-y", nil, 0)
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
	b.recordUpstreamOutcome(ctx, schemas.Gemini, "gemini-x", exhausted, 0)

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
	class, status, hint := upstreamFinalFailure(ctx, exhausted, true, end)
	if class != schemas.FailureClassRateLimit || status == nil || *status != 429 {
		t.Fatalf("expected the trail's rate limit, got %s %v", class, status)
	}
	if hint != 2_000 {
		t.Fatalf("expected the first key's hint less the 18s since its answer (2000 ms), got %d ms", hint)
	}

	var tracker upstreamHealthTracker
	tracker.recordFailure(schemas.Gemini, "gemini-x", class, status, hint, cooldownStart, end)
	if h := findUpstreamHealth(t, tracker.snapshot(t0.Add(20*time.Second-time.Millisecond)), schemas.Gemini, "gemini-x"); h.Available {
		t.Fatalf("expected cooling down before the first answer plus its hint, got %+v", h)
	}
	if h := findUpstreamHealth(t, tracker.snapshot(t0.Add(20*time.Second)), schemas.Gemini, "gemini-x"); !h.Available {
		t.Fatalf("expected available at the first answer plus its hint, got %+v", h)
	}

	// A hint whose wait has fully passed still stands for the provider's wait, not the hintless
	// escalating cooldown.
	if _, _, hint := upstreamFinalFailure(ctx, exhausted, true, t0.Add(time.Minute)); hint != 1 {
		t.Fatalf("expected an elapsed hint to leave 1 ms, got %d ms", hint)
	}
}

// A failure on a tracked pair keeps the tracker locked until it holds the pair: pruneLocked takes
// the tracker's write lock, so it must not be able to drop the pair between the lookup and the
// update, or the update lands on a pair no snapshot reports.
func TestUpstreamHealthFailureHoldsTrackerUntilPairLocked(t *testing.T) {
	b := &Bifrost{}
	b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(503, "server_error", 0), 0)
	st := b.upstreamHealth.entries[upstreamHealthKey{schemas.OpenAI, "gpt-x"}]
	if st == nil {
		t.Fatal("expected the first failure to track the pair")
	}

	st.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", upstreamError(503, "server_error", 0), 0)
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
		b.recordUpstreamOutcome(nil, schemas.Anthropic, "claude-x", upstreamError(429, "rate_limit_error", 0), 0)
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

// The tracker runs once per provider attempt chain on the request path: a success on a pair never
// seen failing (the common case) takes a read lock and one map lookup, a success on a tracked pair
// also takes the pair's lock, and a failure updates the pair.
func BenchmarkUpstreamHealthRecordOutcome(b *testing.B) {
	failure := upstreamError(429, "rate_limit_error", 0)
	b.Run("success_untracked", func(b *testing.B) {
		client := &Bifrost{}
		b.ReportAllocs()
		for b.Loop() {
			client.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", nil, 2)
		}
	})
	b.Run("success_tracked", func(b *testing.B) {
		client := &Bifrost{}
		client.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", failure, 0)
		b.ReportAllocs()
		for b.Loop() {
			client.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", nil, 2)
		}
	})
	b.Run("failure_tracked", func(b *testing.B) {
		client := &Bifrost{}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{
			{KeyID: "key-a", FailureClass: schemas.FailureClassRateLimit},
			{KeyID: "key-b", FailureClass: schemas.FailureClassRateLimit},
		})
		b.ReportAllocs()
		for b.Loop() {
			client.recordUpstreamOutcome(ctx, schemas.OpenAI, "gpt-x", failure, 2)
		}
	})
	b.Run("parallel_success_tracked", func(b *testing.B) {
		client := &Bifrost{}
		client.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", failure, 0)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				client.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", nil, 2)
			}
		})
	})
	b.Run("parallel_failure_tracked", func(b *testing.B) {
		client := &Bifrost{}
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				client.recordUpstreamOutcome(nil, schemas.OpenAI, "gpt-x", failure, 0)
			}
		})
	})
}

// newUpstreamHealthTestClient serves two OpenAI keys, key-a and key-b, from handler, with
// maxRetries retries per attempt chain.
func newUpstreamHealthTestClient(t *testing.T, maxRetries int, handler http.HandlerFunc) *Bifrost {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 4, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = maxRetries
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffInitial = time.Millisecond
	account.configs[schemas.OpenAI].NetworkConfig.RetryBackoffMax = time.Millisecond
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
			client := newUpstreamHealthTestClient(t, 1, rateLimitedHandler)
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
		client := newUpstreamHealthTestClient(t, 1, rateLimitedHandler)
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

// A pool whose every key was refused is free again when its soonest key is, whether the chain
// ended on the last key's own 429 or on the synthetic credentials-exhausted error: key A's 20s
// hint ends the cooldown even though the chain ended on key B's 30s one.
func TestUpstreamHealthRefusedPoolCoolsDownUntilSoonestKey(t *testing.T) {
	var calls atomic.Int32
	client := newUpstreamHealthTestClient(t, 1, func(w http.ResponseWriter, r *http.Request) {
		hint := "30"
		if calls.Add(1) == 1 {
			hint = "20"
		}
		w.Header().Set("Retry-After", hint)
		rateLimitedHandler(w, r)
	})
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	_, bifrostErr := client.ChatCompletionRequest(ctx, upstreamHealthChatRequest())
	if bifrostErr == nil {
		t.Fatal("expected the rate-limited upstream to fail the request")
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected one attempt on each key, got %d upstream calls", n)
	}
	if bifrostErr.ExtraFields.ErrorType == schemas.ErrorTypeProviderCredentialsExhausted || bifrostErr.ExtraFields.RetryAfter != 30_000 {
		t.Fatalf("expected the chain to end on the second key's own 30s answer, got %+v", bifrostErr.ExtraFields)
	}
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	if len(trail) != 2 || trail[0].AnsweredAt.IsZero() {
		t.Fatalf("expected two answered trail records, got %+v", trail)
	}
	h := findUpstreamHealth(t, client.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-4o-mini")
	if h.Available || h.CooldownUntil == nil {
		t.Fatalf("expected the refused pool to cool down, got %+v", h)
	}
	if soonest := trail[0].AnsweredAt.Add(20 * time.Second); h.CooldownUntil.After(soonest) {
		t.Fatalf("expected the cooldown to end by the first key's answer plus 20s (%s), got %s", soonest, h.CooldownUntil)
	}
	if !findUpstreamHealth(t, client.UpstreamHealthSnapshot(trail[0].AnsweredAt.Add(20*time.Second)), schemas.OpenAI, "gpt-4o-mini").Available {
		t.Fatal("expected the pair usable once the soonest key's hint has passed")
	}
}

// A chain the retry budget ends before every key was refused proves nothing about the keys it
// never tried: with MaxRetries 0 a rate limit on the first key ends the chain, and the second key
// may still serve the pair, so the pair is degraded but stays available.
func TestUpstreamHealthPartialKeyWalkStartsNoCooldown(t *testing.T) {
	var calls atomic.Int32
	client := newUpstreamHealthTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rateLimitedHandler(w, r)
	})
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	if _, err := client.ChatCompletionRequest(ctx, upstreamHealthChatRequest()); err == nil {
		t.Fatal("expected the rate-limited upstream to fail the request")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected MaxRetries 0 to stop after the first key, got %d upstream calls", n)
	}
	h := findUpstreamHealth(t, client.UpstreamHealthSnapshot(time.Now()), schemas.OpenAI, "gpt-4o-mini")
	if !h.Available || h.Status != schemas.UpstreamHealthDegraded || h.FailureClass != schemas.FailureClassRateLimit || h.ConsecutiveFailures != 1 {
		t.Fatalf("expected a pair with an untried key to stay available and degraded, got %+v", h)
	}
}

// A pool is refused once each distinct key returned a per-key refusal. A partial walk counts as a
// failure but leaves a running cooldown alone: the refusal that started it covered every key, and
// a chain that tried fewer says nothing newer about the rest.
func TestUpstreamPoolRefusedCountsDistinctRefusedKeys(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyAttemptTrail, []schemas.KeyAttemptRecord{
		{KeyID: "key-a", FailureClass: schemas.FailureClassRateLimit},
		{KeyID: "key-a", FailureClass: schemas.FailureClassRateLimit},
		{KeyID: "key-b", FailureClass: schemas.FailureClassTransient},
	})
	if upstreamPoolRefused(ctx, 2) {
		t.Fatal("a pool with one refused key and one transient failure is not refused")
	}
	if !upstreamPoolRefused(ctx, 1) || !upstreamPoolRefused(nil, 0) {
		t.Fatal("a single-key pool is refused by its final failure")
	}

	var tracker upstreamHealthTracker
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tracker.recordFailure(schemas.OpenAI, "gpt-x", schemas.FailureClassRateLimit, schemas.Ptr(429), 20_000, cooldownStart, t0)
	tracker.recordFailure(schemas.OpenAI, "gpt-x", schemas.FailureClassRateLimit, schemas.Ptr(429), 0, cooldownKeep, t0.Add(time.Second))
	h := findUpstreamHealth(t, tracker.snapshot(t0.Add(2*time.Second)), schemas.OpenAI, "gpt-x")
	if h.Available || h.CooldownUntil == nil || !h.CooldownUntil.Equal(t0.Add(20*time.Second)) || h.ConsecutiveFailures != 2 {
		t.Fatalf("expected the partial walk to count but keep the running cooldown, got %+v", h)
	}
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
	client := newUpstreamHealthTestClient(t, 1, func(w http.ResponseWriter, r *http.Request) {
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
