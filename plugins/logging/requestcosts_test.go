package logging

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/stretchr/testify/require"
)

func receiptContext(t *testing.T, requestID, scope string) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, scope)
	return ctx
}

func receiptResponse(requestType schemas.RequestType) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Model: "gpt-4o",
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: requestType, Provider: schemas.OpenAI,
			OriginalModelRequested: "gpt-4o", ResolvedModelUsed: "gpt-4o",
			RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"},
		},
	}}
}

func receiptPreHook(t *testing.T, p *LoggerPlugin, ctx *schemas.BifrostContext, requestType schemas.RequestType) {
	t.Helper()
	_, _, err := p.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o", Params: &schemas.ChatParameters{}},
	})
	require.NoError(t, err)
}

func receiptPending(t *testing.T, p *LoggerPlugin, scope string) []*logstore.Log {
	t.Helper()
	pending, ok := p.pendingLogsToInject.Load(scope)
	require.True(t, ok)
	return pending.(*pendingInjectEntries).entries
}

func TestRequestCostReceiptMatchesLog(t *testing.T) {
	p := newCostFidelityPlugin(t)
	p.includeRequestCosts = true
	ctx := receiptContext(t, "receipt-request", "receipt-scope")
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	response := receiptResponse(schemas.ChatCompletionRequest)
	_, _, err := p.PostLLMHook(ctx, response, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(response.ChatResponse.ExtraFields)
	require.NoError(t, err)
	var extra map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &extra))
	require.Contains(t, extra, "request_costs", "completed response is missing request_costs")
	costs := response.GetExtraFields().RequestCosts
	require.Equal(t, 1, costs.Version)
	require.Equal(t, "USD", costs.Currency)
	require.True(t, costs.IsComplete)
	require.Len(t, costs.Requests, 1)
	entry := receiptPending(t, p, "receipt-scope")[0]
	require.NotNil(t, entry.Cost)
	require.Positive(t, *entry.Cost)
	require.Equal(t, *entry.Cost, *costs.Requests[0].AmountUSD)
	require.Equal(t, entry.ID, costs.Requests[0].RequestID)
	// A response snapshot cannot change when the in-memory log is later enriched.
	*entry.Cost = 99
	require.NotEqual(t, *entry.Cost, *costs.Requests[0].AmountUSD)
}

func TestRequestCostReceiptFallbackRetainsBilledFailure(t *testing.T) {
	p := newCostFidelityPlugin(t)
	p.includeRequestCosts = true
	ctx := receiptContext(t, "main", "fallback-scope")
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	failure := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider failure"}, ExtraFields: schemas.BifrostErrorExtraFields{
		RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI,
		OriginalModelRequested: "gpt-4o", ResolvedModelUsed: "gpt-4o",
		BilledUsage: &schemas.BifrostLLMUsage{PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50},
	}}
	_, _, err := p.PostLLMHook(ctx, nil, failure)
	require.NoError(t, err)
	require.NotNil(t, failure.ExtraFields.RequestCosts)
	first := failure.ExtraFields.RequestCosts
	require.True(t, first.IsComplete)
	ctx.SetValue(schemas.BifrostContextKeyFallbackRequestID, "arbitrary-fallback-id")
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	response := receiptResponse(schemas.ChatCompletionRequest)
	_, _, err = p.PostLLMHook(ctx, response, nil)
	require.NoError(t, err)
	costs := response.GetExtraFields().RequestCosts
	require.NotNil(t, costs)
	require.True(t, costs.IsComplete)
	require.Len(t, costs.Requests, 2)
	require.Equal(t, "main", costs.Requests[1].ParentRequestID)
	for i, entry := range receiptPending(t, p, "fallback-scope") {
		require.Equal(t, entry.ID, costs.Requests[i].RequestID)
		require.Equal(t, *entry.Cost, *costs.Requests[i].AmountUSD)
	}
	require.Len(t, first.Requests, 1, "earlier immutable snapshots must not gain fallback entries")
}

func TestRequestCostReceiptUnknownAndInnerRetry(t *testing.T) {
	for _, mode := range []string{"missing-usage", "missing-price", "invalid-usage", "inner-retry", "cache-hit"} {
		t.Run(mode, func(t *testing.T) {
			p := newCostFidelityPlugin(t)
			p.includeRequestCosts = true
			ctx := receiptContext(t, "request", mode)
			receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
			response := receiptResponse(schemas.ChatCompletionRequest)
			switch mode {
			case "missing-usage":
				response.ChatResponse.Usage = nil
			case "missing-price":
				response.GetExtraFields().RoutingInfo.Model = "unknown-model"
			case "invalid-usage":
				response.ChatResponse.Usage.TotalTokens = 999
			case "inner-retry":
				ctx.SetValue(schemas.BifrostContextKeyNumberOfRetries, 1)
			case "cache-hit":
				response.GetExtraFields().CacheDebug = &schemas.BifrostCacheDebug{CacheHit: true, HitType: bifrost.Ptr("direct")}
			}
			_, _, err := p.PostLLMHook(ctx, response, nil)
			require.NoError(t, err)
			costs := response.GetExtraFields().RequestCosts
			require.NotNil(t, costs)
			if mode == "invalid-usage" {
				require.Nil(t, receiptPending(t, p, mode)[0].TokenUsageParsed.Cost)
			}
			require.Len(t, costs.Requests, 1)
			if mode == "cache-hit" {
				require.True(t, costs.IsComplete)
				require.NotNil(t, costs.Requests[0].AmountUSD)
				require.Zero(t, *costs.Requests[0].AmountUSD)
			} else {
				require.False(t, costs.IsComplete)
				require.False(t, costs.Requests[0].IsComplete)
				if mode == "inner-retry" {
					require.NotNil(t, costs.Requests[0].AmountUSD)
				} else {
					require.Nil(t, costs.Requests[0].AmountUSD)
				}
			}
		})
	}
}

func TestRequestCostReceiptStreamingUsesScopedCostOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cacheHit bool
		start    time.Time
		want     float64
	}{
		{"scoped-peak-price", false, time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC), 2},
		{"scoped-off-peak-price", false, time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC), 1},
		{"cache-hit-provider-cost", true, time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCostFidelityPlugin(t)
			p.includeRequestCosts = true
			userID := "billing-user"
			require.NoError(t, p.pricingManager.SetPricingOverrides([]configtables.TablePricingOverride{{
				ID: "user-price", ScopeKind: "user", UserID: &userID,
				MatchType: "exact", Pattern: "gpt-4o",
				RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
				PricingPatchJSON: `{"input_cost_per_token":0.01,"output_cost_per_token":0.02,"off_peak_cost_multiplier":0.5,"peak_hours":{"timezone":"UTC","windows":[{"days":[2],"start":"00:00","end":"24:00"}]}}`,
			}}))
			store := tracing.NewTraceStore(time.Minute, testLogger{})
			tracer := tracing.NewTracer(store, p.pricingManager, testLogger{})
			t.Cleanup(tracer.Stop)
			traceID := tracer.CreateTrace("")
			ctx := receiptContext(t, "stream", traceID)
			tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
			ctx.SetValue(schemas.BifrostContextKeyUserID, userID)
			ctx.SetValue(schemas.BifrostContextKeyRequestStartTime, tc.start)
			ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
			receiptPreHook(t, p, ctx, schemas.ChatCompletionStreamRequest)
			first := receiptResponse(schemas.ChatCompletionStreamRequest)
			first.ChatResponse.Usage = nil
			first.GetExtraFields().RequestCosts = &schemas.RequestCosts{Version: 1, Currency: "USD", IsComplete: true}
			_, _, err := p.PostLLMHook(ctx, first, nil)
			require.NoError(t, err)
			require.Nil(t, first.GetExtraFields().RequestCosts, "cached receipts must never leak from intermediate chunks")
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			response := receiptResponse(schemas.ChatCompletionStreamRequest)
			response.GetExtraFields().ChunkIndex = 1
			if tc.cacheHit {
				response.GetExtraFields().CacheDebug = &schemas.BifrostCacheDebug{CacheHit: true, HitType: bifrost.Ptr("direct")}
				response.ChatResponse.Usage.Cost = &schemas.BifrostCost{TotalCost: 99}
			}
			_, _, err = p.PostLLMHook(ctx, response, nil)
			require.NoError(t, err)
			costs := response.GetExtraFields().RequestCosts
			require.NotNil(t, costs)
			require.True(t, costs.IsComplete)
			require.Len(t, costs.Requests, 1)
			want := tc.want
			require.Equal(t, want, *costs.Requests[0].AmountUSD)
			require.Equal(t, want, *receiptPending(t, p, traceID)[0].Cost)
		})
	}
}

func TestRequestCostReceiptScopeAndDeduplication(t *testing.T) {
	p := &LoggerPlugin{logger: testLogger{}, includeRequestCosts: true}
	ctx := receiptContext(t, "same-id", "scope-one")
	entry := &logstore.Log{ID: "same-id", Cost: bifrost.Ptr(1.0), CostIsComplete: true}
	p.storeOrEnqueueEntry(ctx, entry, nil)
	costs := p.storeOrEnqueueEntry(ctx, entry, nil)
	require.Len(t, costs.Requests, 1)
	require.True(t, costs.IsComplete)
	other := receiptContext(t, "same-id", "scope-two")
	otherCosts := p.storeOrEnqueueEntry(other, &logstore.Log{ID: "same-id", Cost: bifrost.Ptr(2.0), CostIsComplete: true}, nil)
	require.Len(t, otherCosts.Requests, 1)
	require.Equal(t, 2.0, *otherCosts.Requests[0].AmountUSD)
	require.Equal(t, 1.0, *costs.Requests[0].AmountUSD)
}

func TestRequestCostReceiptMatchesPersistedLog(t *testing.T) {
	store := newTestStore(t)
	p, err := Init(context.Background(), &Config{IncludeRequestCosts: true}, testLogger{}, store, nil, newTestPricingManager(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Cleanup()) })
	ctx := receiptContext(t, "persisted", "persisted-scope")
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	response := receiptResponse(schemas.ChatCompletionRequest)
	_, _, err = p.PostLLMHook(ctx, response, nil)
	require.NoError(t, err)
	costs := response.GetExtraFields().RequestCosts
	require.NotNil(t, costs)
	require.True(t, costs.IsComplete)
	require.NoError(t, p.Inject(context.Background(), &schemas.Trace{InternalID: "persisted-scope"}))
	require.NoError(t, p.Cleanup())
	entry, err := store.FindByID(context.Background(), "persisted")
	require.NoError(t, err)
	require.NotNil(t, entry.Cost)
	require.Equal(t, *entry.Cost, *costs.Requests[0].AmountUSD)
}

func TestRequestCostReceiptSemanticLookupIsIncludedOnce(t *testing.T) {
	p := newCostFidelityPlugin(t)
	p.includeRequestCosts = true
	ctx := receiptContext(t, "semantic", "semantic-scope")
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	response := receiptResponse(schemas.ChatCompletionRequest)
	base := p.pricingManager.CalculateCost(response, nil)
	// The semantic cache's internal embedding call skips the plugin pipeline;
	// its usage is charged on the main log through CacheDebug, not a child log.
	response.GetExtraFields().CacheDebug = &schemas.BifrostCacheDebug{
		CacheHit: false, ProviderUsed: bifrost.Ptr("openai"),
		ModelUsed: bifrost.Ptr("text-embedding-3-small"), InputTokens: bifrost.Ptr(100),
	}
	expected := p.pricingManager.CalculateCost(response, nil)
	require.Greater(t, expected, base)
	_, _, err := p.PostLLMHook(ctx, response, nil)
	require.NoError(t, err)
	costs := response.GetExtraFields().RequestCosts
	require.NotNil(t, costs)
	require.True(t, costs.IsComplete)
	require.Len(t, costs.Requests, 1)
	require.Equal(t, expected, *costs.Requests[0].AmountUSD)
	require.Equal(t, expected, *receiptPending(t, p, "semantic-scope")[0].Cost)
}

func TestRequestCostReceiptDisabledByDefault(t *testing.T) {
	p := newCostFidelityPlugin(t)
	ctx := receiptContext(t, "private", "private-scope")
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	response := receiptResponse(schemas.ChatCompletionRequest)
	response.GetExtraFields().RequestCosts = &schemas.RequestCosts{Version: 1, Currency: "USD", IsComplete: true}
	_, _, err := p.PostLLMHook(ctx, response, nil)
	require.NoError(t, err)
	require.Nil(t, response.GetExtraFields().RequestCosts, "disabled logging must remove cached cost receipts")
	entry := receiptPending(t, p, "private-scope")[0]
	require.NotNil(t, entry.Cost, "operator opt-in affects disclosure, not cost logging")
	require.Nil(t, p.requestCostsSnapshot([]*logstore.Log{entry}), "disabled receipts do not allocate a snapshot")
}

func TestRequestCostReceiptUsesDollars(t *testing.T) {
	p := newCostFidelityPlugin(t)
	p.includeRequestCosts = true
	ctx := receiptContext(t, "usd", "usd-scope")
	receiptPreHook(t, p, ctx, schemas.ChatCompletionRequest)
	response := receiptResponse(schemas.ChatCompletionRequest)
	response.ChatResponse.Model = "gpt-4o-mini"
	response.GetExtraFields().ResolvedModelUsed = "gpt-4o-mini"
	response.GetExtraFields().RoutingInfo.Model = "gpt-4o-mini"
	response.ChatResponse.Usage = &schemas.BifrostLLMUsage{
		PromptTokens: 2000, CompletionTokens: 100, TotalTokens: 2100,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 1000},
	}
	breakdown, calculation := p.pricingManager.CalculateCostBreakdownWithStatus(response, nil)
	require.NotNil(t, breakdown)
	require.True(t, calculation.IsComplete)
	// The committed catalog is denominated in USD per token: uncached input
	// 1000 * 0.00000015 + cached input 1000 * 0.000000075 + output 100 * 0.0000006.
	require.InDelta(t, 0.000285, breakdown.TotalCost, 1e-15)
	_, _, err := p.PostLLMHook(ctx, response, nil)
	require.NoError(t, err)
	costs := response.GetExtraFields().RequestCosts
	require.NotNil(t, costs)
	require.Equal(t, "USD", costs.Currency)
	require.True(t, costs.IsComplete)
	require.Equal(t, breakdown.TotalCost, *costs.Requests[0].AmountUSD)
	require.Equal(t, breakdown.TotalCost, *receiptPending(t, p, "usd-scope")[0].Cost)
}
