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
		f.tracker.UpdateUsage(context.Background(), settleLimits(f.store, "sk-bf-acct", schemas.OpenAI, "gpt-4", u))
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
		Success: true, TokensUsed: 50, Cost: 0.0, RequestID: "req-s", AttemptNumber: 0,
		IsStreaming: true, IsFinalChunk: false, HasUsageData: true,
	}
	final := &UsageUpdate{
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

// modelScopedFixture wires a virtual-key-scoped per-model budget alongside a
// virtual-key-scoped all-models wildcard, so a settlement can be checked for
// charging the first and not double-charging the second.
type modelScopedFixture struct {
	store   GovernanceStore
	tracker *UsageTracker
}

func newModelScopedFixture(t *testing.T) *modelScopedFixture {
	t.Helper()
	logger := NewMockLogger()
	providerName := "openai"
	vkID := "vk-alice"

	perModel := buildBudgetWithUsage("model-budget", 1_000_000.0, 0.0, "1d")
	perModelRL := buildRateLimit("model-rl", 1_000_000_000, 1_000_000)
	perModelMC := buildModelConfig("mc-vk-gpt5", "gpt-5", &providerName, perModel, perModelRL)
	perModelMC.Scope = configstoreTables.ModelConfigScopeVirtualKey
	perModelMC.ScopeID = &vkID

	wildcard := buildBudgetWithUsage("wildcard-budget", 1_000_000.0, 0.0, "1d")
	wildcardMC := buildModelConfig("mc-vk-all", configstoreTables.ModelConfigAllModels, &providerName, wildcard, nil)
	wildcardMC.Scope = configstoreTables.ModelConfigScopeVirtualKey
	wildcardMC.ScopeID = &vkID

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
		RequestID:    "batch-cost:openai:batch-models",
		Provider:     schemas.OpenAI,
		Cost:         30.0,
		TokensUsed:   300,
		BudgetIDs:    []string{"wildcard-budget"},
		VirtualKeyID: "vk-alice",
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
		RequestID:    "batch-cost:openai:batch-nomodels",
		Provider:     schemas.OpenAI,
		Cost:         12.0,
		TokensUsed:   100,
		BudgetIDs:    []string{"wildcard-budget"},
		VirtualKeyID: "vk-alice",
		ModelUsage:   []batchaccounting.BatchModelUsage{{Model: "gpt-4o", Cost: 12.0, TokensUsed: 100}},
	}))

	assert.Equal(t, 0.0, f.budgetUsage("model-budget"), "a model with no config of its own charges nothing extra")
	assert.Equal(t, 12.0, f.budgetUsage("wildcard-budget"))
}

// The set a request is checked against, the set named on its log row, and the set its usage is counted
// against are one list. This is what several independent walks over the holder's shape used to risk: a
// limit enforced on a request and then not counted lets a caller spend past it forever, and one counted
// but never enforced drains without ever refusing anything.
func TestAccounting_ChargesExactlyWhatWasChecked(t *testing.T) {
	logger := NewMockLogger()

	keyRL := buildRateLimitWithUsage("rl-key", 1_000_000, 0, 1_000_000, 0)
	openaiRL := buildRateLimitWithUsage("rl-key-openai", 1_000_000, 0, 1_000_000, 0)
	bedrockRL := buildRateLimitWithUsage("rl-key-bedrock", 1_000_000, 0, 1_000_000, 0)
	teamRL := buildRateLimitWithUsage("rl-team", 1_000_000, 0, 1_000_000, 0)
	providerRL := buildRateLimitWithUsage("rl-provider", 1_000_000, 0, 1_000_000, 0)
	modelRL := buildRateLimitWithUsage("rl-model", 1_000_000, 0, 1_000_000, 0)

	team := buildTeam("team1", "Team 1", nil)
	team.RateLimitID = &teamRL.ID
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-charge", "Charging VK", keyRL)
	vk.TeamID = &team.ID
	vk.Team = team
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfigWithRateLimit("openai", []string{"*"}, openaiRL),
		buildProviderConfigWithRateLimit("bedrock", []string{"*"}, bedrockRL),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Teams:        []configstoreTables.TableTeam{*team},
		Providers:    []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", nil, providerRL)},
		ModelConfigs: []configstoreTables.TableModelConfig{*buildModelConfig("mc1", "gpt-4o", nil, nil, modelRL)},
		RateLimits: []configstoreTables.TableRateLimit{
			*keyRL, *openaiRL, *bedrockRL, *teamRL, *providerRL, *modelRL,
		},
	}, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	ctx := resolverCtx(store, "sk-bf-charge")

	// The whole funnel: the check settles what this request answers to, the response counts against it.
	_, shortCircuit, err := plugin.PreLLMHook(ctx, newChatRequest())
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the request is within every limit, so it is not refused")

	_, _, err = plugin.PostLLMHook(ctx, countableResponse(), nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	// What the log row names as co-payers, for a node replaying usage it never saw.
	recorded, _ := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
	require.NotEmpty(t, recorded)

	// What actually moved.
	counted := []string{}
	for id, rateLimit := range store.GetGovernanceData(context.Background()).RateLimits {
		if rateLimit.TokenCurrentUsage > 0 {
			counted = append(counted, id)
		}
	}

	assert.ElementsMatch(t, recorded, counted,
		"every co-payer recorded was counted, and nothing was counted that was not recorded")
	assert.ElementsMatch(t,
		[]string{"rl-provider", "rl-model", "rl-key", "rl-key-openai", "rl-team"}, counted)
	assert.NotContains(t, counted, "rl-key-bedrock",
		"a provider this request did not use does not pay for it")

	for _, id := range counted {
		limit := store.GetGovernanceData(context.Background()).RateLimits[id]
		assert.Equal(t, int64(1000), limit.TokenCurrentUsage, id)
		assert.Equal(t, int64(1), limit.RequestCurrentUsage, id)
	}
}

// countableResponse is a completed chat response whose usage the accounting path counts.
func countableResponse() *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4o",
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 600, CompletionTokens: 400, TotalTokens: 1000},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o",
			},
		},
	}
}
