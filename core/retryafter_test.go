package bifrost

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// retryAfterError models the attempt-owned hint that providers attach to the
// error after parsing their response. Retry scheduling must use this value,
// rather than re-reading the mutable response-header slot on the context.
func retryAfterError(message string, statusCode *int, hint time.Duration) *schemas.BifrostError {
	err := createBifrostError(message, statusCode, nil, false)
	err.ExtraFields.RetryAfter = hint.Milliseconds()
	return err
}

func TestRetryAfterHonorsProviderDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		config := createTestConfig(1, time.Millisecond, 5*time.Second)
		start := time.Now()
		calls := 0
		result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
			calls++
			if calls == 1 {
				return "", retryAfterError("rate limit exceeded", Ptr(429), 2*time.Second)
			}
			if elapsed := time.Since(start); elapsed < 2*time.Second {
				t.Errorf("retried after %s, before the provider's 2s Retry-After", elapsed)
			}
			return "success", nil
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if err != nil || result != "success" || calls != 2 {
			t.Fatalf("result=%q err=%v calls=%d", result, err, calls)
		}
	})
}

func TestRetryAfterStopsWhenWaitExceedsBudget(t *testing.T) {
	for _, budget := range []string{"backoff cap", "request deadline"} {
		t.Run(budget, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				deadline := schemas.NoDeadline
				if budget == "request deadline" {
					deadline = time.Now().Add(time.Second)
				}
				ctx := schemas.NewBifrostContext(context.Background(), deadline)
				defer ctx.Cancel()
				ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
				config := createTestConfig(2, time.Millisecond, 5*time.Second)
				hint := time.Minute
				if budget == "request deadline" {
					hint = 2 * time.Second
				}
				upstreamErr := createBifrostError("rate limit exceeded", Ptr(429), nil, false)
				calls := 0
				_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
					calls++
					upstreamErr.ExtraFields.RetryAfter = hint.Milliseconds()
					return "", upstreamErr
				}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
				if calls != 1 || err != upstreamErr {
					t.Fatalf("must preserve upstream error without retrying early: calls=%d err=%v", calls, err)
				}
			})
		})
	}
}

func TestRetryAfterDoesNotReuseResponseHeaders(t *testing.T) {
	for _, reuse := range []string{"next attempt", "fallback"} {
		t.Run(reuse, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
				config := createTestConfig(2, time.Millisecond, 5*time.Second)
				calls := 0
				var secondCall time.Time
				if reuse == "fallback" {
					ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "60"})
					clearCtxForFallback(ctx)
				}
				result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
					calls++
					if calls == 1 && reuse == "next attempt" {
						return "", retryAfterError("rate limit exceeded", Ptr(429), 2*time.Second)
					}
					if (calls == 2 && reuse == "next attempt") || (calls == 1 && reuse == "fallback") {
						secondCall = time.Now()
						// A network failure before receiving headers must not reuse the previous response.
						return "", createBifrostError("connection failed", Ptr(502), nil, false)
					}
					if elapsed := time.Since(secondCall); elapsed > 3*time.Millisecond {
						t.Errorf("stale Retry-After delayed the next attempt by %s", elapsed)
					}
					return "success", nil
				}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
				if err != nil || result != "success" {
					t.Fatalf("result=%q err=%v", result, err)
				}
			})
		})
	}
}

func TestRetryAfterUsesTheFailedAttemptsHintWhenContextsAreShared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The public core API accepts a caller-owned BifrostContext. A caller that
		// (incorrectly, but safely at the map-operation level) reuses it for two
		// concurrent requests must not let the second response's headers alter the
		// first request's retry schedule.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		primaryStarted := make(chan struct{})
		releasePrimary := make(chan struct{})
		type result struct {
			value string
			err   *schemas.BifrostError
			calls int
		}
		resultCh := make(chan result, 1)

		go func() {
			calls := 0
			value, err := executeRequestWithRetries(ctx, createTestConfig(1, time.Millisecond, 5*time.Second), func(_ schemas.Key) (string, *schemas.BifrostError) {
				calls++
				if calls == 1 {
					ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "60"})
					close(primaryStarted)
					<-releasePrimary
					return "", retryAfterError("primary rate limit", Ptr(429), 2*time.Second)
				}
				return "primary success", nil
			}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
			resultCh <- result{value: value, err: err, calls: calls}
		}()

		<-primaryStarted
		_, _ = executeRequestWithRetries(ctx, createTestConfig(0, time.Millisecond, 5*time.Second), func(_ schemas.Key) (string, *schemas.BifrostError) {
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "60"})
			return "", retryAfterError("other request rate limit", Ptr(429), time.Minute)
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		close(releasePrimary)

		got := <-resultCh
		if got.err != nil || got.value != "primary success" || got.calls != 2 {
			t.Fatalf("the primary attempt must keep its own 2s hint: value=%q err=%v calls=%d", got.value, got.err, got.calls)
		}
	})
}

func TestRetryAfterWaitIsCancellable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		defer ctx.Cancel()
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		config := createTestConfig(1, time.Millisecond, 5*time.Second)
		start := time.Now()
		calls := 0
		_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
			calls++
			if calls == 1 {
				time.AfterFunc(100*time.Millisecond, ctx.Cancel)
			}
			return "", retryAfterError("rate limit exceeded", Ptr(429), 2*time.Second)
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if calls != 1 || time.Since(start) != 100*time.Millisecond {
			t.Fatalf("cancellation should stop the retry wait: calls=%d elapsed=%s", calls, time.Since(start))
		}
		if err == nil || err.Error == nil || err.Error.Type == nil || *err.Error.Type != schemas.RequestCancelled {
			t.Fatalf("expected request cancellation, got %v", err)
		}
		logs := ctx.GetRoutingEngineLogs()
		if len(logs) == 0 {
			t.Fatal("expected a terminal cancellation audit entry")
		}
		terminal := logs[len(logs)-1]
		if terminal.Engine != schemas.RoutingEngineCore || terminal.Level != schemas.LogLevelError || terminal.Message != "Request to openai/test-model cancelled after 1 attempt(s)" {
			t.Fatalf("expected cancellation audit entry, got %+v", terminal)
		}
	})
}

func TestRetryAfterAllowsWaitAtBackoffCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		config := createTestConfig(1, time.Millisecond, 2*time.Second)
		start := time.Now()
		calls := 0
		result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
			calls++
			if calls == 1 {
				return "", retryAfterError("rate limit exceeded", Ptr(429), 2*time.Second)
			}
			return "success", nil
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if calls != 2 || result != "success" || err != nil || time.Since(start) != 2*time.Second {
			t.Fatalf("a hint equal to the cap must be honored: calls=%d result=%q err=%v elapsed=%s", calls, result, err, time.Since(start))
		}
	})
}

func TestRetryAfterDeadlineExpiresDuringLocalBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx := schemas.NewBifrostContext(context.Background(), start.Add(1500*time.Millisecond))
		defer ctx.Cancel()
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		config := createTestConfig(1, 2*time.Second, 5*time.Second)
		calls := 0
		_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
			calls++
			// The provider hint fits the deadline, but the longer local backoff does not.
			return "", retryAfterError("rate limit exceeded", Ptr(429), time.Second)
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if calls != 1 || time.Since(start) != 1500*time.Millisecond {
			t.Fatalf("deadline must stop the wait: calls=%d elapsed=%s", calls, time.Since(start))
		}
		if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusGatewayTimeout {
			t.Fatalf("expected a timeout error, got %v", err)
		}
	})
}

func TestRetryAfterPreservesLocalBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		config := createTestConfig(1, 2*time.Second, 5*time.Second)
		calls := 0
		start := time.Now()
		_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
			calls++
			if calls == 1 {
				return "", retryAfterError("unavailable", Ptr(503), 0)
			}
			return "success", nil
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if elapsed := time.Since(start); elapsed < 1600*time.Millisecond || elapsed > 2400*time.Millisecond || calls != 2 || err != nil {
			t.Fatalf("local backoff changed: elapsed=%s calls=%d err=%v", elapsed, calls, err)
		}
	})
}

func TestRetryAfterStreamKeyRotation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
		config := createTestConfig(1, time.Millisecond, 5*time.Second)
		keyProvider := func(used, dead map[string]bool) (schemas.Key, error) {
			if used["a"] {
				return schemas.Key{ID: "b"}, nil
			}
			return schemas.Key{ID: "a"}, nil
		}
		start := time.Now()
		calls := 0
		stream, err := executeRequestWithRetries(ctx, config, func(key schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			calls++
			chunks := make(chan *schemas.BifrostStreamChunk, 1)
			if calls == 1 {
				chunks <- &schemas.BifrostStreamChunk{BifrostError: retryAfterError("rate limit exceeded", Ptr(429), 2*time.Second)}
			} else {
				if key.ID != "b" || time.Since(start) != 2*time.Second {
					t.Errorf("rotation must honor account-level wait: key=%s elapsed=%s", key.ID, time.Since(start))
				}
				chunks <- &schemas.BifrostStreamChunk{}
			}
			close(chunks)
			return chunks, nil
		}, keyProvider, schemas.ChatCompletionStreamRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if err != nil || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		for chunk := range stream {
			if chunk.BifrostError != nil {
				t.Fatal("failed attempt escaped to caller")
			}
		}
	})
}
