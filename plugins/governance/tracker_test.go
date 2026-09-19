package governance

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		Success:      false, // Failed/cancelled request...
		TokensUsed:   100,
		Cost:         25.5, // ...that nonetheless consumed provider tokens.
		RequestID:    "req-123",
		HasUsageData: true,
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", update))

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
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		Success:    false, // Failed before the model ran...
		TokensUsed: 0,
		Cost:       0.0, // ...so no tokens were consumed.
		RequestID:  "req-456",
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", update))

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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	update := &UsageUpdate{
		Success:    true,
		TokensUsed: 100,
		Cost:       25.5,
	}

	// Should not panic or error
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", update))

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
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	// First streaming chunk (not final, has usage data)
	update1 := &UsageUpdate{
		Success:      true,
		TokensUsed:   50,
		Cost:         0.0, // No cost on non-final chunks
		RequestID:    "req-123",
		IsStreaming:  true,
		IsFinalChunk: false,
		HasUsageData: true,
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", update1))
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
		Success:      true,
		TokensUsed:   0, // Already counted
		Cost:         12.5,
		RequestID:    "req-123",
		IsStreaming:  true,
		IsFinalChunk: true,
		HasUsageData: true,
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", update2))
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
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func() *UsageUpdate {
		return &UsageUpdate{
			Success:       false,
			TokensUsed:    100,
			Cost:          10.0,
			RequestID:     "req-dup",
			AttemptNumber: 0,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk()))
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk())) // duplicate settlement
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
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func(attempt int, success bool, cost float64) *UsageUpdate {
		return &UsageUpdate{
			Success:       success,
			TokensUsed:    100,
			Cost:          cost,
			RequestID:     "req-retry",
			AttemptNumber: attempt,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk(0, false, 4.0))) // failed attempt, partial usage
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk(1, true, 6.0)))  // successful retry
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, 10.0, updatedBudget.CurrentUsage,
		"Distinct attempts under one RequestID must each bill")
}

// TestUsageTracker_Idempotency_ForgedRequestIDsBothBilled verifies that two
// INDEPENDENT physical calls sharing a caller-chosen request ID (x-request-id
// is caller-supplied) each bill: the transport mints a distinct BillingNonce
// per HTTP request, and the nonce is part of the billing key precisely so a
// forged duplicate ID cannot claim another request's settlement.
func TestUsageTracker_Idempotency_ForgedRequestIDsBothBilled(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func(nonce string) *UsageUpdate {
		return &UsageUpdate{
			Success:       true,
			TokensUsed:    100,
			Cost:          10.0,
			RequestID:     "attacker-chosen-id",
			BillingNonce:  nonce,
			AttemptNumber: 0,
			HasUsageData:  true,
		}
	}

	// Two physically distinct HTTP requests: same forged request ID, both at
	// attempt 0, but each carrying its own transport-minted nonce.
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk("nonce-a")))
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk("nonce-b")))
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, 20.0, updatedBudget.CurrentUsage,
		"Independent physical calls sharing a caller-chosen RequestID must each bill")
}

// TestUsageTracker_Idempotency_ForgedRequestIDsCrossVKBothBilled verifies the
// cross-principal case: a duplicate request ID sent under a DIFFERENT virtual
// key must not suppress the second key's charges — the billing map is
// process-global, so without the nonce vk2's settlement would be claimed by
// vk1's earlier request.
func TestUsageTracker_Idempotency_ForgedRequestIDsCrossVKBothBilled(t *testing.T) {
	logger := NewMockLogger()

	budget1 := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	budget2 := buildBudgetWithUsage("budget2", 1000.0, 0.0, "1d")
	vk1 := buildVirtualKeyWithBudget("vk1", "sk-bf-one", "VK One", budget1)
	vk2 := buildVirtualKeyWithBudget("vk2", "sk-bf-two", "VK Two", budget2)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk1, *vk2},
		Budgets:     []configstoreTables.TableBudget{*budget1, *budget2},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func(nonce string) *UsageUpdate {
		return &UsageUpdate{
			Success:       true,
			TokensUsed:    100,
			Cost:          10.0,
			RequestID:     "shared-id",
			BillingNonce:  nonce,
			AttemptNumber: 0,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-one", schemas.OpenAI, "gpt-4", mk("nonce-vk1")))
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-two", schemas.OpenAI, "gpt-4", mk("nonce-vk2")))
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	require.NotNil(t, budgets["budget1"])
	require.NotNil(t, budgets["budget2"])
	assert.Equal(t, 10.0, budgets["budget1"].CurrentUsage,
		"vk1's request must bill its own budget")
	assert.Equal(t, 10.0, budgets["budget2"].CurrentUsage,
		"vk2's request must bill despite sharing a request ID with vk1's")
}

// TestUsageTracker_Idempotency_SameNonceSameAttemptBilledOnce verifies the race
// the dedup was built for still holds with the nonce in the key: the
// success-terminal and cancellation-terminal paths of ONE physical call share
// nonce, request ID, and attempt, and must settle at most once.
func TestUsageTracker_Idempotency_SameNonceSameAttemptBilledOnce(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func(success bool) *UsageUpdate {
		return &UsageUpdate{
			Success:       success,
			TokensUsed:    100,
			Cost:          10.0,
			RequestID:     "req-one-call",
			BillingNonce:  "nonce-one-call",
			AttemptNumber: 0,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk(true)))
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk(false))) // competing cancellation-terminal
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, 10.0, updatedBudget.CurrentUsage,
		"Competing terminal paths of one physical call must settle at most once")
}

// TestUsageTracker_Idempotency_NestedCallsUnderOneNonceEachBilled verifies the
// MCP-agent/codemode shape: nested inference calls mint a fresh RequestID each
// (core/mcp/agent.go) but share the parent HTTP request's nonce. The RequestID
// stays in the billing key so those calls each bill rather than colliding on
// the shared nonce.
func TestUsageTracker_Idempotency_NestedCallsUnderOneNonceEachBilled(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	mk := func(requestID string) *UsageUpdate {
		return &UsageUpdate{
			Success:       true,
			TokensUsed:    100,
			Cost:          10.0,
			RequestID:     requestID,
			BillingNonce:  "nonce-parent-http-request",
			AttemptNumber: 0,
			HasUsageData:  true,
		}
	}

	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk("nested-call-1")))
	tracker.UpdateUsage(context.Background(), settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", mk("nested-call-2")))
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, 20.0, updatedBudget.CurrentUsage,
		"Nested calls sharing one nonce but distinct RequestIDs must each bill")
}

// TestUsageTracker_Idempotency_ConcurrentForgedRequestIDsAllBilled verifies the
// forged-ID fix under concurrency: N parallel physical calls sharing one
// caller-chosen request ID, each with its own nonce, must all bill.
func TestUsageTracker_Idempotency_ConcurrentForgedRequestIDsAllBilled(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1000.0, 0.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	const calls = 10
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			update := settleLimits(store, "sk-bf-test", schemas.OpenAI, "gpt-4", &UsageUpdate{
				Success:       true,
				TokensUsed:    100,
				Cost:          1.0,
				RequestID:     "attacker-chosen-id",
				BillingNonce:  fmt.Sprintf("nonce-%d", i),
				AttemptNumber: 0,
				HasUsageData:  true,
			})
			tracker.UpdateUsage(context.Background(), update)
		}(i)
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	budgets := store.GetGovernanceData(context.Background()).Budgets
	updatedBudget := budgets["budget1"]
	require.NotNil(t, updatedBudget)
	assert.Equal(t, float64(calls), updatedBudget.CurrentUsage,
		"All concurrent physical calls sharing a forged RequestID must bill")
}

// TestUsageTracker_Cleanup tests cleanup of the usage tracker
func TestUsageTracker_Cleanup(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)

	// Should cleanup without error
	err = tracker.Cleanup()
	assert.NoError(t, err, "Cleanup should succeed")
}

type cleanupOrderingStore struct {
	GovernanceStore
	periodicEntered       chan struct{}
	periodicExited        chan struct{}
	finalDumped           chan struct{}
	finalBeforeWorkerExit atomic.Bool
}

func (s *cleanupOrderingStore) ResetExpiredRateLimitsInMemory(context.Context, bool, ...string) []*configstoreTables.TableRateLimit {
	return nil
}

func (s *cleanupOrderingStore) ResetExpiredBudgetsInMemory(context.Context, bool, ...string) []*configstoreTables.TableBudget {
	return nil
}

func (s *cleanupOrderingStore) ResetExpiredRateLimits(context.Context, []*configstoreTables.TableRateLimit) error {
	return nil
}

func (s *cleanupOrderingStore) ResetExpiredBudgets(context.Context, []*configstoreTables.TableBudget) error {
	return nil
}

func (s *cleanupOrderingStore) DumpBudgets(context.Context, map[string]float64) error {
	return nil
}

func (s *cleanupOrderingStore) DumpRateLimits(ctx context.Context, _ map[string]int64, _ map[string]int64) error {
	if ctx.Done() == nil {
		select {
		case <-s.periodicExited:
		default:
			s.finalBeforeWorkerExit.Store(true)
		}
		close(s.finalDumped)
		return nil
	}

	close(s.periodicEntered)
	<-ctx.Done()
	close(s.periodicExited)
	return fmt.Errorf("failed to dump rate limits to database: failed to dump 4 rate limits: %w", ctx.Err())
}

// Cleanup must first cancel and join an in-flight periodic dump, then take the
// final snapshot. Cancellation is expected during shutdown and must not be
// reported as a database failure.
func TestUsageTracker_CleanupWaitsForPeriodicDumpBeforeFinalFlush(t *testing.T) {
	store := &cleanupOrderingStore{
		periodicEntered: make(chan struct{}),
		periodicExited:  make(chan struct{}),
		finalDumped:     make(chan struct{}),
	}
	logger := NewMockLogger()
	tracker := NewUsageTracker(context.Background(), store, nil, nil, logger)

	// Enter the same reset cycle as resetWorker without waiting for the
	// production ten-second ticker, and account for it in the worker wait group.
	tracker.wg.Add(1)
	go func() {
		defer tracker.wg.Done()
		tracker.resetExpiredCounters(tracker.trackerCtx)
	}()

	select {
	case <-store.periodicEntered:
	case <-time.After(time.Second):
		t.Fatal("periodic dump did not start")
	}

	require.NoError(t, tracker.Cleanup())
	assert.False(t, store.finalBeforeWorkerExit.Load(), "final dump raced the periodic worker")
	select {
	case <-store.finalDumped:
	default:
		t.Fatal("final rate-limit dump was not called")
	}

	logger.mu.Lock()
	defer logger.mu.Unlock()
	assert.NotContains(t, logger.errors, "failed to dump rate limits to database: %v",
		"shutdown cancellation must not be logged as a database failure")
}
