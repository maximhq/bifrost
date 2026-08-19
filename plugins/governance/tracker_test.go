package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUsageTracker_FailedRequestWithUsage_IsBilled verifies the fix:
// a request that failed (Success=false) but still consumed provider tokens
// (Cost/TokensUsed > 0, e.g. a cancelled mid-stream or a 5xx after input
// processing) MUST update the budget. Anthropic bills for tokens it processed
// regardless of whether Bifrost classified the request as successful.
func TestUsageTracker_FailedRequestWithUsage_IsBilled(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		VirtualKey:   "sk-bf-test",
		Provider:     schemas.OpenAI,
		Model:        "gpt-4",
		Success:      false, // Failed/cancelled request...
		TokensUsed:   100,
		Cost:         25.5, // ...that nonetheless consumed provider tokens.
		RequestID:    "req-123",
		HasUsageData: true,
	}

	tracker.UpdateUsage(context.Background(), update)

	// Give time for async processing
	time.Sleep(200 * time.Millisecond)

	// Verify budget WAS updated - retrieve from store
	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget, exists := budgets["budget1"]
	require.True(t, exists)
	require.NotNil(t, updatedBudget)

	assert.Equal(t, 25.5, updatedBudget.CurrentUsage,
		"Failed request that consumed tokens should still bill the budget")
}

// TestUsageTracker_FailedRequestNoUsage_IsSkipped verifies the inverse: a
// request that failed WITHOUT consuming any tokens (e.g. 401/403/429 before the
// model ran) carries no usage and must NOT bill anything.
func TestUsageTracker_FailedRequestNoUsage_IsSkipped(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		VirtualKey: "sk-bf-test",
		Provider:   schemas.OpenAI,
		Model:      "gpt-4",
		Success:    false, // Failed before the model ran...
		TokensUsed: 0,
		Cost:       0.0, // ...so no tokens were consumed.
		RequestID:  "req-456",
	}

	tracker.UpdateUsage(context.Background(), update)

	// Give time for async processing
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget, exists := budgets["budget1"]
	require.True(t, exists)
	require.NotNil(t, updatedBudget)

	assert.Equal(t, 0.0, updatedBudget.CurrentUsage,
		"Failed request with no usage should not bill anything")
}

// TestUsageTracker_UpdateUsage_VirtualKeyNotFound tests handling of missing VK
func TestUsageTracker_UpdateUsage_VirtualKeyNotFound(t *testing.T) {
	logger := NewMockLogger()

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		VirtualKey: "sk-bf-nonexistent",
		Provider:   schemas.OpenAI,
		Model:      "gpt-4",
		Success:    true,
		TokensUsed: 100,
		Cost:       25.5,
	}

	// Should not panic or error
	tracker.UpdateUsage(context.Background(), update)

	time.Sleep(100 * time.Millisecond)
	// Just verify it doesn't crash
	assert.True(t, true)
}

// TestUsageTracker_UpdateUsage_StreamingOptimization tests streaming request handling
func TestUsageTracker_UpdateUsage_StreamingOptimization(t *testing.T) {
	logger := NewMockLogger()

	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	// First streaming chunk (not final, has usage data)
	update1 := &UsageUpdate{
		VirtualKey:   "sk-bf-test",
		Provider:     schemas.OpenAI,
		Model:        "gpt-4",
		Success:      true,
		TokensUsed:   50,
		Cost:         0.0, // No cost on non-final chunks
		RequestID:    "req-123",
		IsStreaming:  true,
		IsFinalChunk: false,
		HasUsageData: true,
	}

	tracker.UpdateUsage(context.Background(), update1)
	time.Sleep(200 * time.Millisecond)

	// Retrieve the updated rate limit from the main RateLimits map
	governanceData := store.GetGovernanceData(context.Background())
	updatedRateLimit, exists := governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	// Tokens should be updated but not requests (not final chunk)
	assert.Equal(t, int64(50), updatedRateLimit.TokenCurrentUsage, "Tokens should be updated on non-final chunk")

	// Final chunk
	update2 := &UsageUpdate{
		VirtualKey:   "sk-bf-test",
		Provider:     schemas.OpenAI,
		Model:        "gpt-4",
		Success:      true,
		TokensUsed:   0, // Already counted
		Cost:         12.5,
		RequestID:    "req-123",
		IsStreaming:  true,
		IsFinalChunk: true,
		HasUsageData: true,
	}

	tracker.UpdateUsage(context.Background(), update2)
	time.Sleep(200 * time.Millisecond)

	// Retrieve the updated rate limit again
	governanceData = store.GetGovernanceData(context.Background())
	updatedRateLimit, exists = governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	// Request counter should be updated on final chunk
	assert.Equal(t, int64(1), updatedRateLimit.RequestCurrentUsage, "Request should be incremented on final chunk")
}

// TestUsageTracker_Idempotency_SameAttemptBilledOnce verifies the billing dedup:
// the same physical provider call (RequestID + AttemptNumber) settling twice —
// e.g. both the core ctx.Done() return path and the provider goroutine's
// terminal post-hook — bills the budget only once.
func TestUsageTracker_Idempotency_SameAttemptBilledOnce(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func() *UsageUpdate {
		return &UsageUpdate{
			VirtualKey:    "sk-bf-test",
			Provider:      schemas.OpenAI,
			Model:         "gpt-4",
			Success:       false,
			TokensUsed:    100,
			Cost:          10.0,
			RequestID:     "req-dup",
			AttemptNumber: 0,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), mk())
	tracker.UpdateUsage(context.Background(), mk()) // duplicate settlement
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, 10.0, updatedBudget.CurrentUsage,
		"Same RequestID+attempt must bill exactly once")
}

// TestUsageTracker_Idempotency_DifferentAttemptsBothBilled verifies that two
// distinct physical provider calls under one logical RequestID (e.g. a failed
// attempt that consumed partial tokens, then a successful retry) each bill —
// the dedup key includes the attempt number so legitimate per-attempt charges
// are not suppressed.
func TestUsageTracker_Idempotency_DifferentAttemptsBothBilled(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func(attempt int, success bool, cost float64) *UsageUpdate {
		return &UsageUpdate{
			VirtualKey:    "sk-bf-test",
			Provider:      schemas.OpenAI,
			Model:         "gpt-4",
			Success:       success,
			TokensUsed:    100,
			Cost:          cost,
			RequestID:     "req-retry",
			AttemptNumber: attempt,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), mk(0, false, 4.0)) // failed attempt, partial usage
	tracker.UpdateUsage(context.Background(), mk(1, true, 6.0))  // successful retry
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, 10.0, updatedBudget.CurrentUsage,
		"Distinct attempts under one RequestID must each bill")
}

// TestUsageTracker_Cleanup tests cleanup of the usage tracker
func TestUsageTracker_Cleanup(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)

	// Should cleanup without error
	err = tracker.Cleanup()
	assert.NoError(t, err, "Cleanup should succeed")
}
