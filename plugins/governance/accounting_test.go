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

// accountingFixture wires a tracker over a single virtual key that carries both
// a budget (for cost accumulation) and a rate limit (for request/token counts),
// so accounting assertions can read all three dimensions.
type accountingFixture struct {
	store   GovernanceStore
	tracker *UsageTracker
}

func newAccountingFixture(t *testing.T) *accountingFixture {
	t.Helper()
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 1_000_000.0, 0.0, "1d")
	rl := buildRateLimit("rl1", 1_000_000_000, 1_000_000)
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-acct", "Acct VK", budget)
	vk.RateLimit = rl
	rlID := rl.ID
	vk.RateLimitID = &rlID

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	t.Cleanup(func() { _ = tracker.Cleanup() })

	return &accountingFixture{store: store, tracker: tracker}
}

func (f *accountingFixture) apply(updates ...*UsageUpdate) {
	for _, u := range updates {
		f.tracker.UpdateUsage(context.Background(), u)
	}
	// Let async processing settle.
	time.Sleep(250 * time.Millisecond)
}

func (f *accountingFixture) cost() float64 {
	return f.store.GetGovernanceData(context.Background()).Budgets["budget1"].CurrentUsage
}

func (f *accountingFixture) requests() int64 {
	return f.store.GetGovernanceData(context.Background()).RateLimits["rl1"].RequestCurrentUsage
}

func (f *accountingFixture) tokens() int64 {
	return f.store.GetGovernanceData(context.Background()).RateLimits["rl1"].TokenCurrentUsage
}

// acctUpdate builds a terminal (non-streaming) usage update for accounting tests.
func acctUpdate(requestID string, attempt int, success bool, cost float64, tokens int64) *UsageUpdate {
	return &UsageUpdate{
		VirtualKey:    "sk-bf-acct",
		Provider:      schemas.OpenAI,
		Model:         "gpt-4",
		Success:       success,
		TokensUsed:    tokens,
		Cost:          cost,
		RequestID:     requestID,
		AttemptNumber: attempt,
		HasUsageData:  tokens > 0 || cost > 0,
	}
}

// TestAccounting_CumulativeCostAcrossRequests: distinct successful requests each
// add to the budget — the budget is a running total, not a per-request value.
func TestAccounting_CumulativeCostAcrossRequests(t *testing.T) {
	f := newAccountingFixture(t)

	f.apply(
		acctUpdate("req-1", 0, true, 10.0, 100),
		acctUpdate("req-2", 0, true, 10.0, 100),
		acctUpdate("req-3", 0, true, 10.0, 100),
	)

	assert.Equal(t, 30.0, f.cost(), "cost must accumulate across requests")
	assert.Equal(t, int64(3), f.requests(), "each successful request counts once")
	assert.Equal(t, int64(300), f.tokens(), "tokens must accumulate across requests")
}

// TestAccounting_StreamingChunksAccumulate: a streaming request reports token
// deltas on intermediate chunks and cost on the final chunk; the request counts
// exactly once and totals are correct.
func TestAccounting_StreamingChunksAccumulate(t *testing.T) {
	f := newAccountingFixture(t)

	nonFinal := &UsageUpdate{
		VirtualKey: "sk-bf-acct", Provider: schemas.OpenAI, Model: "gpt-4",
		Success: true, TokensUsed: 50, Cost: 0.0, RequestID: "req-s", AttemptNumber: 0,
		IsStreaming: true, IsFinalChunk: false, HasUsageData: true,
	}
	final := &UsageUpdate{
		VirtualKey: "sk-bf-acct", Provider: schemas.OpenAI, Model: "gpt-4",
		Success: true, TokensUsed: 0, Cost: 12.5, RequestID: "req-s", AttemptNumber: 0,
		IsStreaming: true, IsFinalChunk: true, HasUsageData: true,
	}
	f.apply(nonFinal, final)

	assert.Equal(t, 12.5, f.cost(), "final-chunk cost is billed once")
	assert.Equal(t, int64(1), f.requests(), "streaming request counts once (final chunk only)")
	assert.Equal(t, int64(50), f.tokens(), "token delta from the non-final chunk is counted")
}

// TestAccounting_FailedStreamingBilledOnceAndAccumulates: cancelled/failed
// streaming requests that consumed tokens are billed (cost accumulates) but do
// NOT increment the request counter.
func TestAccounting_FailedStreamingBilledOnceAndAccumulates(t *testing.T) {
	f := newAccountingFixture(t)

	mk := func(reqID string) *UsageUpdate {
		return &UsageUpdate{
			VirtualKey: "sk-bf-acct", Provider: schemas.OpenAI, Model: "gpt-4",
			Success: false, TokensUsed: 200, Cost: 8.0, RequestID: reqID, AttemptNumber: 0,
			IsStreaming: true, IsFinalChunk: true, HasUsageData: true,
		}
	}
	f.apply(mk("req-f1"), mk("req-f2"))

	assert.Equal(t, 16.0, f.cost(), "partial cost from failed streams accumulates")
	assert.Equal(t, int64(0), f.requests(), "failed requests do not increment request count")
	assert.Equal(t, int64(400), f.tokens(), "consumed tokens are still counted")
}

// TestAccounting_RetryAttemptsEachBilledAndSummed: each physical attempt under
// one logical RequestID that consumed tokens bills separately; the budget is the
// sum across attempts.
func TestAccounting_RetryAttemptsEachBilledAndSummed(t *testing.T) {
	f := newAccountingFixture(t)

	f.apply(
		acctUpdate("req-retry", 0, false, 5.0, 100),
		acctUpdate("req-retry", 1, false, 5.0, 100),
		acctUpdate("req-retry", 2, false, 5.0, 100),
	)

	assert.Equal(t, 15.0, f.cost(), "each token-consuming attempt bills; budget is the sum")
	assert.Equal(t, int64(0), f.requests(), "failed attempts do not count as requests")
	assert.Equal(t, int64(300), f.tokens(), "tokens accumulate across attempts")
}

// TestAccounting_FailedAttemptThenSuccessfulRetry: a failed attempt that
// consumed partial tokens plus a successful retry both bill (cost sums), but only
// the successful attempt increments the request counter.
func TestAccounting_FailedAttemptThenSuccessfulRetry(t *testing.T) {
	f := newAccountingFixture(t)

	f.apply(
		acctUpdate("req-mix", 0, false, 4.0, 100), // failed attempt, partial usage
		acctUpdate("req-mix", 1, true, 6.0, 150),  // successful retry
	)

	assert.Equal(t, 10.0, f.cost(), "failed-attempt cost + successful-retry cost both bill")
	assert.Equal(t, int64(1), f.requests(), "only the successful attempt counts as a request")
	assert.Equal(t, int64(250), f.tokens(), "tokens from both attempts accumulate")
}

// TestAccounting_NoDoubleBillSuccessVsCancelTerminal: when both a success
// terminal and a cancellation terminal fire for the SAME physical call
// (RequestID+attempt), the budget is charged exactly once.
func TestAccounting_NoDoubleBillSuccessVsCancelTerminal(t *testing.T) {
	f := newAccountingFixture(t)

	success := acctUpdate("req-race", 0, true, 10.0, 100)
	cancel := acctUpdate("req-race", 0, false, 10.0, 100) // duplicate settlement of the same call
	f.apply(success, cancel)

	assert.Equal(t, 10.0, f.cost(), "same physical call must bill exactly once")
	assert.Equal(t, int64(1), f.requests(), "request counted once (the successful settlement)")
	assert.Equal(t, int64(100), f.tokens(), "tokens counted once")
}

// TestAccounting_ZeroCostFailureNotBilled: a failure that consumed nothing
// (e.g. 401/403/429 before the model ran) must not touch any counter.
func TestAccounting_ZeroCostFailureNotBilled(t *testing.T) {
	f := newAccountingFixture(t)

	f.apply(acctUpdate("req-z", 0, false, 0.0, 0))

	assert.Equal(t, 0.0, f.cost(), "no-usage failure bills no cost")
	assert.Equal(t, int64(0), f.requests(), "no-usage failure counts no request")
	assert.Equal(t, int64(0), f.tokens(), "no-usage failure counts no tokens")
}
