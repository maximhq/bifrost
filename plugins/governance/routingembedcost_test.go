package governance

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOfflinePricingCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	abs, err := filepath.Abs("../../framework/modelcatalog/datasheet/testdata/pricing.json")
	require.NoError(t, err)
	ds := datasheet.New(nil, NewMockLogger(), datasheet.Config{URL: "file://" + abs})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// newWarmupBudgetFixture wires a plugin over a store whose "openai" provider
// carries a provider-level budget — the admin-owned ledger warmup embeds are
// attributed to when count_toward_budgets is on.
func newWarmupBudgetFixture(t *testing.T) (*GovernancePlugin, GovernanceStore) {
	t.Helper()
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("provider-budget", 1000.0, 0.0, "1d")
	budgetID := budget.ID
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets:   []configstoreTables.TableBudget{*budget},
		Providers: []configstoreTables.TableProvider{{Name: "openai", BudgetID: &budgetID}},
	}, nil, nil)
	require.NoError(t, err)
	plugin := &GovernancePlugin{
		ctx:          context.Background(),
		store:        store,
		modelCatalog: newOfflinePricingCatalog(t),
		logger:       logger,
	}
	return plugin, store
}

func TestAttributeRoutingEmbeddingCostBillsProviderBudget(t *testing.T) {
	plugin, store := newWarmupBudgetFixture(t)

	plugin.AttributeRoutingEmbeddingCost("openai", "text-embedding-3-small", 1000)

	// 1000 tokens × $0.00000002/token (text-embedding-3-small in testdata).
	usage := store.GetGovernanceData(context.Background()).Budgets["provider-budget"].CurrentUsage
	assert.InDelta(t, 0.00002, usage, 1e-12)
}

func TestAttributeRoutingEmbeddingCostWithoutStoreOrCatalog(t *testing.T) {
	// A plugin whose catalog or store never got wired must not panic; the
	// usage is simply unattributable.
	(&GovernancePlugin{}).AttributeRoutingEmbeddingCost("openai", "text-embedding-3-small", 1000)
}

func TestPostHookWorkerAddsRoutingCostToProviderUsageOnError(t *testing.T) {
	fixture := newAccountingFixture(t)
	plugin := &GovernancePlugin{
		ctx:          context.Background(),
		tracker:      fixture.tracker,
		modelCatalog: newOfflinePricingCatalog(t),
	}

	provider, model, routingTokens := "openai", "text-embedding-3-small", 13
	routingMetadata := &schemas.BifrostRoutingMetadata{
		Calls: []schemas.BifrostRoutingCall{{
			ProviderUsed:       &provider,
			ModelUsed:          &model,
			InputTokens:        &routingTokens,
			CountTowardBudgets: true,
		}},
	}
	billedUsage := &schemas.BifrostLLMUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}
	bifrostErr := &schemas.BifrostError{
		Error: &schemas.ErrorField{Message: "provider failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			BilledUsage: billedUsage,
		},
	}

	settled := settleLimits(fixture.store, "sk-bf-acct", schemas.OpenAI, "gpt-4o", &UsageUpdate{})
	plugin.postHookWorker(nil, bifrostErr, schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, "routing-error", "", false, 0, 0, false, nil, settled.Budgets, settled.RateLimits, routingMetadata, nil)

	// gpt-4o testdata: 2.5e-6 input, 1e-5 output. Routing embedding:
	// text-embedding-3-small at 2e-8/input token. Both calls are billable.
	want := float64(100)*2.5e-6 + float64(50)*1e-5 + float64(routingTokens)*2e-8
	// postHookWorker hands the update to the tracker, which the plugin treats as
	// an asynchronous queue. Poll for the settled total instead of sleeping for a
	// fixed interval that is either wasted time or, if the tracker ever moves the
	// work off the caller's goroutine, too short.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.InDelta(c, want, fixture.cost(), 1e-12)
	}, time.Second, 10*time.Millisecond)
}

func TestPostHookWorkerUsesCapturedFallbackCost(t *testing.T) {
	for _, cost := range []float64{0, 0.125} {
		t.Run(fmt.Sprint(cost), func(t *testing.T) {
			fixture := newAccountingFixture(t)
			plugin := &GovernancePlugin{ctx: context.Background(), tracker: fixture.tracker, modelCatalog: newOfflinePricingCatalog(t)}
			result := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
				Usage:       &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
				ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, OriginalModelRequested: "gpt-4o"},
			}}
			settled := settleLimits(fixture.store, "sk-bf-acct", schemas.OpenAI, "gpt-4o", &UsageUpdate{})
			plugin.postHookWorker(result, nil, schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, "fallback-cost", "", false, 0, 1, false, nil, settled.Budgets, settled.RateLimits, nil, &cost)
			// In particular, an explicitly captured zero must not be replaced by
			// the nonzero catalog price for the response's token usage.
			assert.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, cost, fixture.cost())
				assert.Equal(c, int64(150), fixture.tokens())
				assert.Equal(c, int64(1), fixture.requests())
			}, time.Second, 10*time.Millisecond)
		})
	}
}

func TestPostLLMHookCapturesCostBeforeRoutingDecoration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		streaming     bool
		fallbackIndex int
	}{
		{name: "unary_fallback", fallbackIndex: 1},
		{name: "streaming_fallback", streaming: true, fallbackIndex: 1},
		{name: "streaming_primary", streaming: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestType := schemas.ChatCompletionRequest
			if tc.streaming {
				requestType = schemas.ChatCompletionStreamRequest
			}
			fixture := newAccountingFixture(t)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, NewMockLogger(), fixture.store, nil, newOfflinePricingCatalog(t), nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
			ctx := resolverCtx(fixture.store, "sk-bf-acct")
			ctx.SetValue(schemas.BifrostContextKeyRequestID, "cost-capture-"+tc.name)
			ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, tc.fallbackIndex)
			req := &schemas.BifrostRequest{RequestType: requestType, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}}
			_, short, err := plugin.PreLLMHook(ctx, req)
			require.NoError(t, err)
			require.Nil(t, short)
			provider, model, routingTokens := "openai", "text-embedding-3-small", 13
			result := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
				Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: requestType, Provider: schemas.OpenAI, OriginalModelRequested: "gpt-4o",
					RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
					RoutingMetadata: &schemas.BifrostRoutingMetadata{Calls: []schemas.BifrostRoutingCall{{
						ProviderUsed: &provider, ModelUsed: &model, InputTokens: &routingTokens, CountTowardBudgets: true,
					}}},
				},
			}}
			if tc.streaming {
				_, _, err := plugin.PostLLMHook(ctx, result, nil)
				require.NoError(t, err)
				plugin.wg.Wait()
				assert.Zero(t, fixture.cost(), "a nonfinal stream chunk must not be billed")
				assert.Zero(t, fixture.tokens())
				assert.Zero(t, fixture.requests())
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}
			_, _, err = plugin.PostLLMHook(ctx, result, nil)
			require.NoError(t, err)
			// Core applies this decoration only after the post-hook returns. The
			// asynchronous worker must use its snapshot rather than reading it again.
			if tc.fallbackIndex > 0 {
				result.SetFallbackRoutingInfo(schemas.Anthropic, "primary-model")
			} else {
				result.PopulateRoutingInfo(schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"})
			}
			plugin.wg.Wait()
			want := float64(100)*2.5e-6 + float64(50)*1e-5 + float64(routingTokens)*2e-8
			assert.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.InDelta(c, want, fixture.cost(), 1e-12, "inference and routing sidecar cost are each charged once")
				assert.Equal(c, int64(150), fixture.tokens())
				assert.Equal(c, int64(1), fixture.requests())
			}, time.Second, 10*time.Millisecond)
		})
	}
}
