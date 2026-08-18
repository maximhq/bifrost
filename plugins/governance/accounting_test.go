package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/batchaccounting"
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

func TestReportBatchUsage_IdempotentPerAggregateAndTarget(t *testing.T) {
	f := newAccountingFixture(t)
	plugin := &GovernancePlugin{store: f.store, tracker: f.tracker}
	report := batchaccounting.BatchUsageReport{
		RequestID:    "batch-cost:openai:batch-1",
		Provider:     schemas.OpenAI,
		Model:        "gpt-4o-mini",
		Cost:         12.5,
		TokensUsed:   123,
		BudgetIDs:    []string{"budget1"},
		RateLimitIDs: []string{"rl1"},
	}

	require.NoError(t, plugin.ReportBatchUsage(context.Background(), report))
	require.NoError(t, plugin.ReportBatchUsage(context.Background(), report))
	assert.Equal(t, 12.5, f.cost())
	assert.Equal(t, int64(123), f.tokens())
	assert.Equal(t, int64(1), f.requests())
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
	}, nil, nil)
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

// userUsageSpy records the user-entity bumps a batch settlement makes. The OSS
// store implements them as no-ops (user governance is enterprise), so the spy is
// what pins the contract an enterprise store relies on.
type userUsageSpy struct {
	GovernanceStore
	budgetBumps    []float64
	rateLimitBumps []int64
	users          []string
}

func (s *userUsageSpy) UpdateUserBudgetUsageInMemory(ctx context.Context, userID string, cost float64) error {
	s.users = append(s.users, userID)
	s.budgetBumps = append(s.budgetBumps, cost)
	return nil
}

func (s *userUsageSpy) UpdateUserRateLimitUsageInMemory(ctx context.Context, userID string, tokensUsed int64, shouldUpdateTokens bool, shouldUpdateRequests bool) error {
	s.rateLimitBumps = append(s.rateLimitBumps, tokensUsed)
	return nil
}

// A user's own budget has no id in BudgetIDs, so before this it was never charged
// for batch spend — the gap that let a batch bypass every per-user limit under an
// access profile, where the virtual key is shared and the user is the only thing
// separating one person's spend from another's.
func TestReportBatchUsage_ChargesUserEntity(t *testing.T) {
	f := newAccountingFixture(t)
	spy := &userUsageSpy{GovernanceStore: f.store}
	plugin := &GovernancePlugin{store: spy, tracker: f.tracker}
	report := batchaccounting.BatchUsageReport{
		RequestID:    "batch-cost:openai:batch-user",
		Provider:     schemas.OpenAI,
		Model:        "gpt-4o-mini",
		Cost:         12.5,
		TokensUsed:   123,
		BudgetIDs:    []string{"budget1"},
		RateLimitIDs: []string{"rl1"},
		UserID:       "user-alice",
	}

	// Settlement is at-least-once, so a repeated report must not double-charge.
	require.NoError(t, plugin.ReportBatchUsage(context.Background(), report))
	require.NoError(t, plugin.ReportBatchUsage(context.Background(), report))

	assert.Equal(t, []string{"user-alice"}, spy.users)
	assert.Equal(t, []float64{12.5}, spy.budgetBumps)
	assert.Equal(t, []int64{123}, spy.rateLimitBumps)
	// The budget-id tier is charged exactly once as well, and stays independent of
	// the user tier: the ids carry the user's model-config scopes, not the user.
	assert.Equal(t, 12.5, f.cost())
}

// No user on the report (no user auth, or a pre-migration batch job) must leave the
// user tier entirely alone.
func TestReportBatchUsage_WithoutUserSkipsUserEntity(t *testing.T) {
	f := newAccountingFixture(t)
	spy := &userUsageSpy{GovernanceStore: f.store}
	plugin := &GovernancePlugin{store: spy, tracker: f.tracker}

	require.NoError(t, plugin.ReportBatchUsage(context.Background(), batchaccounting.BatchUsageReport{
		RequestID:    "batch-cost:openai:batch-nouser",
		Provider:     schemas.OpenAI,
		Cost:         3.0,
		TokensUsed:   10,
		BudgetIDs:    []string{"budget1"},
		RateLimitIDs: []string{"rl1"},
	}))

	assert.Empty(t, spy.users)
	assert.Empty(t, spy.budgetBumps)
	assert.Empty(t, spy.rateLimitBumps)
}

// modelScopedFixture wires a user-scoped per-model budget (how an access profile's
// model-level limits are stored) alongside a user-scoped all-models wildcard, so a
// settlement can be checked for charging the first and not double-charging the second.
type modelScopedFixture struct {
	store   GovernanceStore
	tracker *UsageTracker
}

func newModelScopedFixture(t *testing.T) *modelScopedFixture {
	t.Helper()
	logger := NewMockLogger()
	providerName := "openai"
	userID := "user-alice"

	perModel := buildBudgetWithUsage("model-budget", 1_000_000.0, 0.0, "1d")
	perModelRL := buildRateLimit("model-rl", 1_000_000_000, 1_000_000)
	perModelMC := buildModelConfig("mc-user-gpt5", "gpt-5", &providerName, perModel, perModelRL)
	perModelMC.Scope = configstoreTables.ModelConfigScopeUser
	perModelMC.ScopeID = &userID

	wildcard := buildBudgetWithUsage("wildcard-budget", 1_000_000.0, 0.0, "1d")
	wildcardMC := buildModelConfig("mc-user-all", configstoreTables.ModelConfigAllModels, &providerName, wildcard, nil)
	wildcardMC.Scope = configstoreTables.ModelConfigScopeUser
	wildcardMC.ScopeID = &userID

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*perModelMC, *wildcardMC},
		Budgets:      []configstoreTables.TableBudget{*perModel, *wildcard},
		RateLimits:   []configstoreTables.TableRateLimit{*perModelRL},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	t.Cleanup(func() { _ = tracker.Cleanup() })

	return &modelScopedFixture{store: store, tracker: tracker}
}

func (f *modelScopedFixture) budgetUsage(id string) float64 {
	return f.store.GetGovernanceData(context.Background()).Budgets[id].CurrentUsage
}

func TestReportBatchUsage_ChargesPerModelBudgets(t *testing.T) {
	f := newModelScopedFixture(t)
	plugin := &GovernancePlugin{store: f.store, tracker: f.tracker}

	// What a settled batch looks like: the wildcard budget was collected at create
	// time (and so is already charged the full total), the per-model budget was not.
	report := batchaccounting.BatchUsageReport{
		RequestID:  "batch-cost:openai:batch-models",
		Provider:   schemas.OpenAI,
		Cost:       30.0,
		TokensUsed: 300,
		BudgetIDs:  []string{"wildcard-budget"},
		UserID:     "user-alice",
		ModelUsage: []batchaccounting.BatchModelUsage{
			{Model: "gpt-5", Cost: 20.0, TokensUsed: 200},
			{Model: "gpt-4o", Cost: 10.0, TokensUsed: 100},
		},
	}

	// Settlement is at-least-once, so a repeat must not double-charge.
	require.NoError(t, plugin.ReportBatchUsage(context.Background(), report))
	require.NoError(t, plugin.ReportBatchUsage(context.Background(), report))

	assert.Equal(t, 20.0, f.budgetUsage("model-budget"), "the gpt-5 budget takes gpt-5's share, not the batch total")
	assert.Equal(t, 30.0, f.budgetUsage("wildcard-budget"), "an already-charged budget must not be charged again per model")
}

// A batch whose models carry no per-model config must behave exactly as before.
func TestReportBatchUsage_PerModelChargingIsInertWithoutModelConfigs(t *testing.T) {
	f := newModelScopedFixture(t)
	plugin := &GovernancePlugin{store: f.store, tracker: f.tracker}

	require.NoError(t, plugin.ReportBatchUsage(context.Background(), batchaccounting.BatchUsageReport{
		RequestID:  "batch-cost:openai:batch-nomodels",
		Provider:   schemas.OpenAI,
		Cost:       12.0,
		TokensUsed: 100,
		BudgetIDs:  []string{"wildcard-budget"},
		UserID:     "user-alice",
		ModelUsage: []batchaccounting.BatchModelUsage{{Model: "gpt-4o", Cost: 12.0, TokensUsed: 100}},
	}))

	assert.Equal(t, 0.0, f.budgetUsage("model-budget"), "a model with no config of its own charges nothing extra")
	assert.Equal(t, 12.0, f.budgetUsage("wildcard-budget"))
}
