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

// TestUsageTracker_UpdateUsage_FailedRequest tests usage tracking for a failed request
func TestUsageTracker_UpdateUsage_FailedRequest(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		VirtualKey: "sk-bf-test",
		Provider:   schemas.OpenAI,
		Model:      "gpt-4",
		Success:    false, // Failed request
		TokensUsed: 100,
		Cost:       25.5,
		RequestID:  "req-123",
	}

	tracker.UpdateUsage(context.Background(), update)

	// Give time for async processing
	time.Sleep(200 * time.Millisecond)

	// Verify budget was NOT updated - retrieve from store
	budgets := store.GetGovernanceData().Budgets
	updatedBudget, exists := budgets["budget1"]
	require.True(t, exists)
	require.NotNil(t, updatedBudget)

	assert.Equal(t, 0.0, updatedBudget.CurrentUsage, "Failed request should not update budget")
}

// TestUsageTracker_UpdateUsage_VirtualKeyNotFound tests handling of missing VK
func TestUsageTracker_UpdateUsage_VirtualKeyNotFound(t *testing.T) {
	logger := NewMockLogger()

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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

	resolver := NewBudgetResolver(store, nil, logger)
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
	governanceData := store.GetGovernanceData()
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
	governanceData = store.GetGovernanceData()
	updatedRateLimit, exists = governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	// Request counter should be updated on final chunk
	assert.Equal(t, int64(1), updatedRateLimit.RequestCurrentUsage, "Request should be incremented on final chunk")
}

// TestUsageTracker_Cleanup tests cleanup of the usage tracker
func TestUsageTracker_Cleanup(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)

	// Should cleanup without error
	err = tracker.Cleanup()
	assert.NoError(t, err, "Cleanup should succeed")
}
