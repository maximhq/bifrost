package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
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

func TestReportUsage_IdempotentPerAggregateAndTarget(t *testing.T) {
	f := newAccountingFixture(t)
	plugin := &GovernancePlugin{store: f.store, tracker: f.tracker}
	report := jobaccounting.UsageReport{
		RequestID:    "batch-cost:openai:batch-1",
		Provider:     schemas.OpenAI,
		Model:        "gpt-4o-mini",
		Cost:         12.5,
		TokensUsed:   123,
		BudgetIDs:    []string{"budget1"},
		RateLimitIDs: []string{"rl1"},
	}

	require.NoError(t, plugin.ReportUsage(context.Background(), report))
	require.NoError(t, plugin.ReportUsage(context.Background(), report))
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

// Whatever funds the user was gathered onto the request's grant when the batch was
// created, alongside every other co-payer, so the user's ids are already in
// BudgetIDs and RateLimitIDs. Naming the user on the report must therefore not add
// a charge of its own: a second pass keyed by user would bump those same limits
// twice.
//
// This is asserted on the charged amount rather than by spying on the user-scoped
// store methods, because those are not on the GovernanceStore interface at all —
// the plugin cannot reach them, so a spy could never observe anything and would
// pass no matter what the code did.
func TestReportUsage_UserOnReportDoesNotAddASecondCharge(t *testing.T) {
	newReport := func(requestID, userID string) jobaccounting.UsageReport {
		return jobaccounting.UsageReport{
			RequestID:    requestID,
			Provider:     schemas.OpenAI,
			Model:        "gpt-4o-mini",
			Cost:         12.5,
			TokensUsed:   123,
			BudgetIDs:    []string{"budget1"},
			RateLimitIDs: []string{"rl1"},
			UserID:       userID,
		}
	}

	withUser := newAccountingFixture(t)
	withUserPlugin := &GovernancePlugin{store: withUser.store, tracker: withUser.tracker}
	// Settlement is at-least-once, so a repeated report must not double-charge either.
	require.NoError(t, withUserPlugin.ReportUsage(context.Background(), newReport("batch-cost:openai:with-user", "user-alice")))
	require.NoError(t, withUserPlugin.ReportUsage(context.Background(), newReport("batch-cost:openai:with-user", "user-alice")))

	withoutUser := newAccountingFixture(t)
	withoutUserPlugin := &GovernancePlugin{store: withoutUser.store, tracker: withoutUser.tracker}
	require.NoError(t, withoutUserPlugin.ReportUsage(context.Background(), newReport("batch-cost:openai:no-user", "")))

	assert.Equal(t, 12.5, withUser.cost(), "the grant's ids are charged exactly once")
	assert.Equal(t, withoutUser.cost(), withUser.cost(),
		"naming the user must not change the amount charged; the user is a co-payer on the grant, not a tier of its own")
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

func TestReportUsage_ChargesPerModelBudgets(t *testing.T) {
	f := newModelScopedFixture(t)
	plugin := &GovernancePlugin{store: f.store, tracker: f.tracker}

	// What a settled batch looks like: the wildcard budget was collected at create
	// time (and so is already charged the full total), the per-model budget was not.
	report := jobaccounting.UsageReport{
		RequestID:    "batch-cost:openai:batch-models",
		Provider:     schemas.OpenAI,
		Cost:         30.0,
		TokensUsed:   300,
		BudgetIDs:    []string{"wildcard-budget"},
		UserID:       "user-alice",
		VirtualKeyID: "vk-alice",
		ModelUsage: []jobaccounting.ModelUsage{
			{Model: "gpt-5", Cost: 20.0, TokensUsed: 200},
			{Model: "gpt-4o", Cost: 10.0, TokensUsed: 100},
		},
	}

	// Settlement is at-least-once, so a repeat must not double-charge.
	require.NoError(t, plugin.ReportUsage(context.Background(), report))
	require.NoError(t, plugin.ReportUsage(context.Background(), report))

	assert.Equal(t, 20.0, f.budgetUsage("model-budget"), "the gpt-5 budget takes gpt-5's share, not the batch total")
	assert.Equal(t, 30.0, f.budgetUsage("wildcard-budget"), "an already-charged budget must not be charged again per model")
}

// A batch whose models carry no per-model config must behave exactly as before.
func TestReportUsage_PerModelChargingIsInertWithoutModelConfigs(t *testing.T) {
	f := newModelScopedFixture(t)
	plugin := &GovernancePlugin{store: f.store, tracker: f.tracker}

	require.NoError(t, plugin.ReportUsage(context.Background(), jobaccounting.UsageReport{
		RequestID:  "batch-cost:openai:batch-nomodels",
		Provider:   schemas.OpenAI,
		Cost:       12.0,
		TokensUsed: 100,
		BudgetIDs:  []string{"wildcard-budget"},
		UserID:     "user-alice",
		ModelUsage: []jobaccounting.ModelUsage{{Model: "gpt-4o", Cost: 12.0, TokensUsed: 100}},
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

// governedPassthroughFixture builds a plugin over a virtual key whose own rate limit
// is the only thing governing it — no model configs — so what a request moves can be
// read without a model being involved anywhere.
func governedPassthroughFixture(t *testing.T) (*GovernancePlugin, GovernanceStore) {
	t.Helper()
	logger := NewMockLogger()

	rl := buildRateLimit("rl-vk", 1_000_000, 1_000_000)
	vk := buildVirtualKeyWithRateLimit("vk-pt", "sk-bf-pt", "Passthrough VK", rl)
	vk.ProviderConfigs = append(vk.ProviderConfigs, buildProviderConfig("vertex", []string{"*"}))

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)},
		logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, store
}

// passthroughResponseWithUsage is what a provider returns for a raw forwarded route,
// carrying billable tokens and, optionally, the model the caller named.
func passthroughResponseWithUsage(requestType schemas.RequestType, model string, tokens int) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		PassthroughResponse: &schemas.BifrostPassthroughResponse{
			StatusCode: 200,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            requestType,
				Provider:               schemas.Vertex,
				OriginalModelRequested: model,
				ResolvedModelUsed:      model,
			},
			PassthroughUsage: &schemas.BifrostPassthroughUsage{
				LLMUsage: &schemas.BifrostLLMUsage{
					PromptTokens:     tokens,
					CompletionTokens: 0,
					TotalTokens:      tokens,
				},
			},
		},
	}
}

// settleAccounting waits for the accounting the post hook handed to a goroutine, rather than
// guessing at a duration that passes on an idle machine and flakes on a loaded one.
//
// Cleanup is the join: it waits on the same wait group PostLLMHook adds to. It is guarded by a
// sync.Once, so the fixture's own deferred Cleanup is a no-op afterwards and a test may cross
// this barrier once, at the end. The plugin is spent once it returns.
func settleAccounting(t *testing.T, plugin *GovernancePlugin) {
	t.Helper()
	require.NoError(t, plugin.Cleanup())
}

// A passthrough request that names no model is still charged to the limits it was
// checked against. A Vertex custom endpoint names a model nowhere — not in the path,
// which carries an endpoint id, and not in the body, which carries instances — yet the
// virtual key's own limits never depended on a model to begin with: they are the key's,
// and HolderLimits takes no model at all. Dropping the usage left such a request checked
// against a counter it could never move, so a workload made entirely of them ran without
// bound while the key reported nothing spent.
// Both forwarding request types are covered, because both are charged by the same branch and
// a streaming one settles differently: it is billed once, on the chunk that closes the stream.
func TestAccounting_ModellessPassthroughIsCharged(t *testing.T) {
	for _, tc := range []struct {
		name        string
		requestType schemas.RequestType
	}{
		{"unary", schemas.PassthroughRequest},
		{"streaming", schemas.PassthroughStreamRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A fixture per case: settling the accounting spends the plugin.
			plugin, store := governedPassthroughFixture(t)

			ctx := presentCtx("sk-bf-pt")
			_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
				RequestType: tc.requestType,
				PassthroughRequest: &schemas.BifrostPassthroughRequest{
					Provider: schemas.Vertex,
					Method:   "POST",
					Path:     "/v1/projects/p/locations/l/endpoints/123456:predict",
				},
			})
			require.NoError(t, err)
			require.Nil(t, shortCircuit, "the key permits this provider and can afford the request")

			if tc.requestType == schemas.PassthroughStreamRequest {
				// A stream is billed on the chunk that ends it, which is the only one carrying
				// the usage for the whole call.
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}

			_, _, err = plugin.PostLLMHook(ctx, passthroughResponseWithUsage(tc.requestType, "", 5000), nil)
			require.NoError(t, err)
			settleAccounting(t, plugin)

			rateLimit := store.GetGovernanceData(context.Background()).RateLimits["rl-vk"]
			assert.Equal(t, int64(5000), rateLimit.TokenCurrentUsage,
				"tokens a model-less passthrough spent count against the key that spent them")
			assert.Equal(t, int64(1), rateLimit.RequestCurrentUsage,
				"one call is one request, however many chunks carried it")

			// The same list is what the log row records, which is what lets a node's usage be
			// reconciled from logs after the fact. Stamping nothing loses it for good.
			rateLimitIDs, _ := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string)
			assert.Contains(t, rateLimitIDs, "rl-vk",
				"the limits charged are recorded on the request for the log row")
		})
	}
}

// A call that spent nothing is charged nothing, and being forwarded raw does not change that.
// The passthrough routes carry metadata traffic as well as inference — a job status poll, a file
// retrieve — and an agent polling one of those would otherwise drain a caller's request allowance
// without ever reaching a model. What admits a call to accounting is the usage the provider
// reported on it, not the shape of the route it arrived on.
func TestAccounting_RequestsThatSpendNothingAreNotCharged(t *testing.T) {
	for _, tc := range []struct {
		name        string
		requestType schemas.RequestType
		request     *schemas.BifrostRequest
		response    *schemas.BifrostResponse
	}{
		{
			name:        "passthrough poll carrying no usage",
			requestType: schemas.PassthroughRequest,
			request: &schemas.BifrostRequest{
				RequestType: schemas.PassthroughRequest,
				PassthroughRequest: &schemas.BifrostPassthroughRequest{
					Provider: schemas.Vertex,
					Method:   "GET",
					Path:     "/v1/projects/p/locations/l/operations/987654",
				},
			},
			response: &schemas.BifrostResponse{
				PassthroughResponse: &schemas.BifrostPassthroughResponse{
					StatusCode: 200,
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType: schemas.PassthroughRequest,
						Provider:    schemas.Vertex,
					},
					// No PassthroughUsage: the provider reported nothing billable.
				},
			},
		},
		{
			name:        "file listing",
			requestType: schemas.FileListRequest,
			request: &schemas.BifrostRequest{
				RequestType:     schemas.FileListRequest,
				FileListRequest: &schemas.BifrostFileListRequest{Provider: schemas.Vertex},
			},
			response: &schemas.BifrostResponse{
				FileListResponse: &schemas.BifrostFileListResponse{
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType: schemas.FileListRequest,
						Provider:    schemas.Vertex,
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin, store := governedPassthroughFixture(t)

			// Through the request hook first, so the key's limits are settled onto the grant exactly
			// as a real call's are. Without that the assertion below is worth nothing: a post hook
			// reached with an empty grant charges nothing whatever accounting decides, so the test
			// would hold just as well if these requests were being billed.
			ctx := presentCtx("sk-bf-pt")
			_, shortCircuit, err := plugin.PreLLMHook(ctx, tc.request)
			require.NoError(t, err)
			require.Nil(t, shortCircuit, "the call is permitted and costs nothing")
			require.NotEmpty(t, ctx.Grant().Limits().RateLimits(),
				"the request carries the limits it would be charged against, so not charging it is a decision")

			_, _, err = plugin.PostLLMHook(ctx, tc.response, nil)
			require.NoError(t, err)
			settleAccounting(t, plugin)

			rateLimit := store.GetGovernanceData(context.Background()).RateLimits["rl-vk"]
			assert.Equal(t, int64(0), rateLimit.RequestCurrentUsage,
				"a call that spent nothing must not consume the key's request allowance")
			assert.Equal(t, int64(0), rateLimit.TokenCurrentUsage)
		})
	}
}
