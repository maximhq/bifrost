package bifrost

import (
	"context"
	"math"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestRetryAfterHonorsProviderDelay(t *testing.T) {
	for _, format := range []string{"seconds", "http-date"} {
		t.Run(format, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
				config := createTestConfig(1, time.Millisecond, 5*time.Second)
				start := time.Now()
				header := "2"
				if format == "http-date" {
					header = start.Add(2 * time.Second).UTC().Format(http.TimeFormat)
				}
				calls := 0
				result, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
					calls++
					if calls == 1 {
						ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"retry-after": header})
						return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
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
		})
	}
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
				header := "60"
				if budget == "request deadline" {
					header = "2"
				}
				upstreamErr := createBifrostError("rate limit exceeded", Ptr(429), nil, false)
				calls := 0
				_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
					calls++
					ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": header})
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
						ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "2"})
						return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
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
				ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "2"})
				time.AfterFunc(100*time.Millisecond, ctx.Cancel)
			}
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
		}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
		if calls != 1 || time.Since(start) != 100*time.Millisecond {
			t.Fatalf("cancellation should stop the retry wait: calls=%d elapsed=%s", calls, time.Since(start))
		}
		if err == nil || err.Error == nil || err.Error.Type == nil || *err.Error.Type != schemas.RequestCancelled {
			t.Fatalf("expected request cancellation, got %v", err)
		}
	})
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, value string
		want        time.Time
	}{
		{"seconds", "2", now.Add(2 * time.Second)},
		{"whitespace", " 2\t", now.Add(2 * time.Second)},
		{"zero", "0", now},
		{"leading zeros", "002", now.Add(2 * time.Second)},
		{"http date", now.Add(time.Minute).Format(http.TimeFormat), now.Add(time.Minute)},
		{"past date", now.Add(-time.Minute).Format(http.TimeFormat), now.Add(-time.Minute)},
		{"duration overflow", "9223372037", now.Add(time.Duration(math.MaxInt64))},
		{"integer overflow", "18446744073709551616", now.Add(time.Duration(math.MaxInt64))},
		{"empty", "", time.Time{}},
		{"negative", "-1", time.Time{}},
		{"signed", "+1", time.Time{}},
		{"fraction", "0.5", time.Time{}},
		{"multiple values", "2, 3", time.Time{}},
		{"invalid date", "tomorrow", time.Time{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := retryAfterTime(map[string]string{"rEtRy-AfTeR": tt.value}, now)
			if !got.Equal(tt.want) {
				t.Errorf("retryAfterTime(%q)=%v, want %v", tt.value, got, tt.want)
			}
		})
	}
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
				ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "2"})
				return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
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
			ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "1"})
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
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
	for _, hint := range []string{"", "invalid", "0", "1", "past"} {
		t.Run(hint, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if hint == "past" {
					hint = time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
				}
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
				config := createTestConfig(1, 2*time.Second, 5*time.Second)
				calls := 0
				start := time.Now()
				_, err := executeRequestWithRetries(ctx, config, func(_ schemas.Key) (string, *schemas.BifrostError) {
					calls++
					if calls == 1 {
						ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": hint})
						return "", createBifrostError("unavailable", Ptr(503), nil, false)
					}
					return "success", nil
				}, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "test-model", nil, NewDefaultLogger(schemas.LogLevelError))
				if elapsed := time.Since(start); elapsed < 1600*time.Millisecond || elapsed > 2400*time.Millisecond || calls != 2 || err != nil {
					t.Fatalf("local backoff changed: elapsed=%s calls=%d err=%v", elapsed, calls, err)
				}
			})
		})
	}
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
				ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{"Retry-After": "2"})
				chunks <- &schemas.BifrostStreamChunk{BifrostError: createBifrostError("rate limit exceeded", Ptr(429), nil, false)}
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
