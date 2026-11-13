package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseWith builds a store holding one virtual key whose rate limit is already full, with each
// window opened at the given offset in the past, and returns what evaluating a request answers.
func refuseWith(t *testing.T, tokenDuration, requestDuration string, tokenOpened, requestOpened time.Duration) *EvaluationResult {
	t.Helper()
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 100, 100)
	rateLimit.TokenResetDuration = &tokenDuration
	rateLimit.RequestResetDuration = &requestDuration
	rateLimit.TokenLastReset = time.Now().Add(-tokenOpened)
	rateLimit.RequestLastReset = time.Now().Add(-requestOpened)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")
	return evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
}

// A refused request says how long until the window reopens, not how long the window is.
func TestRateLimitRefusalCarriesTimeUntilWindowReopens(t *testing.T) {
	// A 5m token window opened 2m ago has about 3m left. Only the token limit is full: the
	// request limit's window is long over, so it does not refuse.
	result := refuseWith(t, "5m", "1m", 2*time.Minute, 5*time.Minute)

	assertDecision(t, DecisionTokenLimited, result)
	assert.InDelta(t, (3 * time.Minute).Seconds(), result.RetryAfter.Seconds(), 2)
	// The message keeps reporting the configured duration, as it always has.
	assert.Contains(t, result.Reason, "resets every 5m")
}

// When both limits refuse, only the later of the two windows lets the caller through.
func TestRateLimitRefusalWaitsForTheLongestWindow(t *testing.T) {
	result := refuseWith(t, "1h", "1m", 10*time.Minute, 10*time.Second)

	assertDecision(t, DecisionRateLimited, result)
	assert.InDelta(t, (50 * time.Minute).Seconds(), result.RetryAfter.Seconds(), 2)
}

// The hint reaches the client as the error's retry_after_ms, unclamped: a window can be a year.
func TestRefusalPutsRetryAfterOnTheError(t *testing.T) {
	refused := refusal(&EvaluationResult{
		Decision:   DecisionTokenLimited,
		Reason:     "Rate limit exceeded",
		RetryAfter: 90*time.Minute + 400*time.Microsecond,
	}, 429, schemas.ErrorTypePolicyRateLimited)

	// 90 minutes and a fraction round up to the next whole millisecond.
	assert.Equal(t, int64((90*time.Minute)/time.Millisecond)+1, refused.ExtraFields.RetryAfter)

	for name, result := range map[string]*EvaluationResult{
		"no window known":    {Decision: DecisionRateLimited, Reason: "Rate limit exceeded"},
		"a non-rate refusal": {Decision: DecisionBudgetExceeded, Reason: "Budget exceeded"},
	} {
		assert.Zero(t, refusal(result, 429, schemas.ErrorTypePolicyRateLimited).ExtraFields.RetryAfter, name)
	}
}

func TestRetryAfterFromErrorIgnoresOtherErrors(t *testing.T) {
	assert.Zero(t, retryAfterFromError(nil))
	assert.Zero(t, retryAfterFromError(errors.New("something else")))
	assert.Equal(t, time.Second, retryAfterFromError(&rateLimitViolationError{retryAfter: time.Second}))
}

func TestWindowRemaining(t *testing.T) {
	five, bad := "5m", "not-a-duration"
	assert.Zero(t, windowRemaining(nil, time.Now()))
	assert.Zero(t, windowRemaining(&bad, time.Now()))
	// A window that has already closed is zero, never negative.
	assert.Zero(t, windowRemaining(&five, time.Now().Add(-time.Hour)))
	assert.InDelta(t, (4 * time.Minute).Seconds(), windowRemaining(&five, time.Now().Add(-time.Minute)).Seconds(), 1)
}

// An allowed request pays nothing for the hint: the allow path computes no wait.
func BenchmarkCheckRateLimitAllowed(b *testing.B) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10, 100, 1)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(b, err)
	limits := EntityWiseRateLimits{"vk": {rateLimit}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if decision, err := store.CheckRateLimit(ctx, limits, nil, nil); err != nil || decision != DecisionAllow {
			b.Fatal("request should be allowed")
		}
	}
}
