package bifrost

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// costCatalog prices responses and errors with fixed breakdowns and counts its
// calls, so a test can tell whether core priced a result and with what.
type costCatalog struct {
	responseCost *schemas.BifrostCost
	errorCost    *schemas.BifrostCost
	// pricedAfterHooks makes responses billable only once a post-hook has stamped
	// CacheDebug, the way semantic cache metadata reaches the price.
	pricedAfterHooks bool

	responseCalls atomic.Int32
	errorCalls    atomic.Int32
}

func (c *costCatalog) GetModelInfo(schemas.ModelProvider, string) *schemas.Model { return nil }

func (c *costCatalog) CalculateRequestCost(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) float64 {
	if cost := c.CalculateRequestCostBreakdown(ctx, resp); cost != nil {
		return cost.TotalCost
	}
	return 0
}

func (c *costCatalog) CalculateRequestCostBreakdown(_ *schemas.BifrostContext, resp *schemas.BifrostResponse) *schemas.BifrostCost {
	c.responseCalls.Add(1)
	if c.pricedAfterHooks && resp.GetExtraFields().CacheDebug == nil {
		return nil
	}
	return c.responseCost.DeepCopy()
}

func (c *costCatalog) CalculateErrorCostBreakdown(_ *schemas.BifrostContext, _ *schemas.BifrostError) *schemas.BifrostCost {
	c.errorCalls.Add(1)
	return c.errorCost.DeepCopy()
}

// costHookPlugin short-circuits every request with a response, or with an error
// when fail is set, so no provider is called, and records what its PostLLMHook
// was given. It stamps CacheDebug there, as the semantic cache does.
type costHookPlugin struct {
	fail bool

	seenResponse *schemas.BifrostResponse
	seenError    *schemas.BifrostError
}

func (p *costHookPlugin) GetName() string { return "cost-hook" }
func (p *costHookPlugin) Cleanup() error  { return nil }

func (p *costHookPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (p *costHookPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if p.fail {
		status := 500
		return req, &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			StatusCode: &status,
			Error:      &schemas.ErrorField{Message: "upstream failed"},
		}}, nil
	}
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4o",
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		},
	}}, nil
}

func (p *costHookPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	p.seenResponse = resp
	p.seenError = bifrostErr
	if resp != nil {
		resp.GetExtraFields().CacheDebug = &schemas.BifrostCacheMetadata{}
	}
	return resp, bifrostErr, nil
}

func newSendBackCostClient(t testing.TB, catalog schemas.ModelInfoProvider, plugin schemas.LLMPlugin, sendBackCost bool) *Bifrost {
	t.Helper()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID:     "openai-key-1",
		Value:  *schemas.NewSecretVar("sk-test"),
		Models: schemas.WhiteList{"*"},
	}})
	client, initErr := Init(context.Background(), schemas.BifrostConfig{
		Account:      account,
		Logger:       NewNoOpLogger(),
		ModelCatalog: catalog,
		LLMPlugins:   []schemas.LLMPlugin{plugin},
		SendBackCost: sendBackCost,
	})
	if initErr != nil {
		t.Fatalf("Init failed: %v", initErr)
	}
	t.Cleanup(client.Shutdown)
	return client
}

func newCostTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func testCost(total float64) *schemas.BifrostCost {
	return &schemas.BifrostCost{InputCost: total, TotalCost: total}
}

func TestSendBackCostIsOffByDefault(t *testing.T) {
	catalog := &costCatalog{responseCost: testCost(0.25)}
	client := newSendBackCostClient(t, catalog, &costHookPlugin{}, false)

	resp, bifrostErr := client.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest error: %v", bifrostErr.Error.Message)
	}
	if resp.ExtraFields.Cost != nil {
		t.Fatalf("cost = %+v, want none with the setting off", resp.ExtraFields.Cost)
	}
	if calls := catalog.responseCalls.Load(); calls != 0 {
		t.Fatalf("catalog priced %d times with the setting off, want 0", calls)
	}
}

// The cost covers what post-hooks stamp (here CacheDebug, without which the
// catalog prices nothing), and it is written on a copy, never on the response
// the plugin was given.
func TestSendBackCostPricesResponseAfterPostHooksOnACopy(t *testing.T) {
	catalog := &costCatalog{responseCost: testCost(0.25), pricedAfterHooks: true}
	plugin := &costHookPlugin{}
	client := newSendBackCostClient(t, catalog, plugin, true)

	resp, bifrostErr := client.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest error: %v", bifrostErr.Error.Message)
	}
	if resp.ExtraFields.Cost == nil || resp.ExtraFields.Cost.TotalCost != 0.25 {
		t.Fatalf("cost = %+v, want total 0.25", resp.ExtraFields.Cost)
	}
	if plugin.seenResponse == nil || plugin.seenResponse.ChatResponse == resp {
		t.Fatal("the returned response is the one the post-hook was given, want a copy")
	}
	if plugin.seenResponse.ChatResponse.ExtraFields.Cost != nil {
		t.Fatal("the cost was written onto the response the post-hook was given")
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Fatalf("copy lost the response body: usage %+v", resp.Usage)
	}
}

func TestSendBackCostPerRequestOverride(t *testing.T) {
	for _, test := range []struct {
		name     string
		global   bool
		override bool
		wantCost bool
	}{
		{name: "header on over global off", global: false, override: true, wantCost: true},
		{name: "header off over global on", global: true, override: false, wantCost: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newSendBackCostClient(t, &costCatalog{responseCost: testCost(0.25)}, &costHookPlugin{}, test.global)
			ctx := newCostTestContext()
			ctx.SetValue(schemas.BifrostContextKeySendBackCost, test.override)

			resp, bifrostErr := client.ChatCompletionRequest(ctx, chatProbeRequest())
			if bifrostErr != nil {
				t.Fatalf("ChatCompletionRequest error: %v", bifrostErr.Error.Message)
			}
			if got := resp.ExtraFields.Cost != nil; got != test.wantCost {
				t.Fatalf("cost present = %v, want %v", got, test.wantCost)
			}
		})
	}
}

// Nothing billable means no field at all, never a zero cost.
func TestSendBackCostOmitsNothingBillable(t *testing.T) {
	for name, cost := range map[string]*schemas.BifrostCost{
		"no breakdown": nil,
		"zero total":   testCost(0),
	} {
		t.Run(name, func(t *testing.T) {
			client := newSendBackCostClient(t, &costCatalog{responseCost: cost, errorCost: cost}, &costHookPlugin{}, true)
			resp, bifrostErr := client.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
			if bifrostErr != nil {
				t.Fatalf("ChatCompletionRequest error: %v", bifrostErr.Error.Message)
			}
			if resp.ExtraFields.Cost != nil {
				t.Fatalf("cost = %+v, want none", resp.ExtraFields.Cost)
			}

			failing := newSendBackCostClient(t, &costCatalog{responseCost: cost, errorCost: cost}, &costHookPlugin{fail: true}, true)
			_, bifrostErr = failing.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
			if bifrostErr == nil || bifrostErr.ExtraFields.Cost != nil {
				t.Fatalf("error = %+v, want an error with no cost", bifrostErr)
			}
		})
	}
}

func TestSendBackCostFollowsConfigReload(t *testing.T) {
	client := newSendBackCostClient(t, &costCatalog{responseCost: testCost(0.25)}, &costHookPlugin{}, false)
	priced := func() bool {
		t.Helper()
		resp, bifrostErr := client.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest error: %v", bifrostErr.Error.Message)
		}
		return resp.ExtraFields.Cost != nil
	}

	if err := client.ReloadConfig(schemas.BifrostConfig{SendBackCost: true}); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if !client.GetSendBackCost() || !priced() {
		t.Fatal("ReloadConfig with SendBackCost did not turn the cost on")
	}
	client.UpdateSendBackCost(false)
	if client.GetSendBackCost() || priced() {
		t.Fatal("UpdateSendBackCost(false) did not turn the cost off")
	}
}

func TestSendBackCostPricesErrorsOnACopy(t *testing.T) {
	catalog := &costCatalog{errorCost: testCost(0.1)}
	plugin := &costHookPlugin{fail: true}
	client := newSendBackCostClient(t, catalog, plugin, true)

	_, bifrostErr := client.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
	if bifrostErr == nil {
		t.Fatal("ChatCompletionRequest succeeded, want the short-circuit error")
	}
	if bifrostErr.ExtraFields.Cost == nil || bifrostErr.ExtraFields.Cost.TotalCost != 0.1 {
		t.Fatalf("error cost = %+v, want total 0.1", bifrostErr.ExtraFields.Cost)
	}
	if plugin.seenError == nil || plugin.seenError == bifrostErr {
		t.Fatal("the returned error is the one the post-hook was given, want a copy")
	}
	if plugin.seenError.ExtraFields.Cost != nil {
		t.Fatal("the cost was written onto the error the post-hook was given")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "upstream failed" {
		t.Fatalf("copy lost the error body: %+v", bifrostErr.Error)
	}
}

// A catalog that only implements ModelInfoProvider still prices responses; its
// errors carry no cost.
func TestSendBackCostSkipsErrorsWhenCatalogCannotPriceThem(t *testing.T) {
	catalog := &probeCatalog{breakdown: testCost(0.25)}
	client := newSendBackCostClient(t, catalog, &costHookPlugin{fail: true}, true)

	_, bifrostErr := client.ChatCompletionRequest(newCostTestContext(), chatProbeRequest())
	if bifrostErr == nil {
		t.Fatal("ChatCompletionRequest succeeded, want the short-circuit error")
	}
	if bifrostErr.ExtraFields.Cost != nil {
		t.Fatalf("error cost = %+v, want none from a catalog without ErrorCostProvider", bifrostErr.ExtraFields.Cost)
	}
}

// A result that already carries a cost (a stream's startup error priced by its
// runner, an agent turn sum) is returned as is, without pricing it again.
func TestSendBackCostDoesNotRepriceAPricedResult(t *testing.T) {
	catalog := &costCatalog{responseCost: testCost(0.25), errorCost: testCost(0.1)}
	client := newSendBackCostClient(t, catalog, &costHookPlugin{}, true)
	ctx := newCostTestContext()
	ctx.SetValue(schemas.BifrostContextKeyModelCatalog, catalog)

	resp := (&schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}).WithCost(testCost(1))
	if got := client.withResponseCost(ctx, resp); got != resp {
		t.Fatal("withResponseCost replaced a response that already had a cost")
	}
	bifrostErr := (&schemas.BifrostError{}).WithCost(testCost(1))
	if got := client.withErrorCost(ctx, bifrostErr); got != bifrostErr {
		t.Fatal("withErrorCost replaced an error that already had a cost")
	}
	if catalog.responseCalls.Load() != 0 || catalog.errorCalls.Load() != 0 {
		t.Fatal("an already-priced result was priced again")
	}
}

// routingCostCatalog prices a response at a base cost plus one unit per budget-counted
// routing call on the response itself, the way the datasheet prices RoutingMetadata.
type routingCostCatalog struct{ costCatalog }

func (c *routingCostCatalog) CalculateRequestCostBreakdown(_ *schemas.BifrostContext, resp *schemas.BifrostResponse) *schemas.BifrostCost {
	total := 0.5
	if metadata := resp.GetExtraFields().RoutingMetadata; metadata != nil {
		for _, call := range metadata.Calls {
			if call.CountTowardBudgets {
				total++
			}
		}
	}
	return testCost(total)
}

// The routing classifier runs once per request, so its cost is returned once: on a
// request that succeeds on a retry or a fallback, whose response the routing plugin
// does not stamp, and on an MCP agent loop's first turn only, since the loop sums its
// turns and every later turn shares the first turn's context. The routing metadata
// never leaks onto the returned or the original response.
func TestSendBackCostChargesTheRoutingCallOncePerRequest(t *testing.T) {
	catalog := &routingCostCatalog{}
	client := newSendBackCostClient(t, catalog, &costHookPlugin{}, true)
	provider, model, tokens := "openai", "text-embedding-3-small", 10
	call := schemas.BifrostRoutingCall{ProviderUsed: &provider, ModelUsed: &model, InputTokens: &tokens, CountTowardBudgets: true}
	newResp := func() *schemas.BifrostResponse {
		return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: "gpt-4o"}}
	}

	for _, tc := range []struct {
		name         string
		routedOnCtx  bool
		stampedOnRes bool
		agentTurn    bool
		want         float64
	}{
		{name: "retry or fallback succeeded", routedOnCtx: true, want: 1.5},
		{name: "first attempt succeeded", routedOnCtx: true, stampedOnRes: true, want: 1.5},
		{name: "no routing", want: 0.5},
		{name: "agent continuation turn", routedOnCtx: true, stampedOnRes: true, agentTurn: true, want: 0.5},
		{name: "agent continuation turn after a fallback", routedOnCtx: true, agentTurn: true, want: 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newCostTestContext()
			ctx.SetValue(schemas.BifrostContextKeyModelCatalog, catalog)
			if tc.agentTurn {
				ctx.SetValue(schemas.BifrostContextKeyMCPAgentServedRoute, &schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"})
			}
			if tc.routedOnCtx && !schemas.AppendRoutingCallOnContext(ctx, call) {
				t.Fatal("routing call was not recorded on the context")
			}
			resp := newResp()
			if tc.stampedOnRes {
				resp.ChatResponse.ExtraFields.RoutingMetadata = &schemas.BifrostRoutingMetadata{Calls: []schemas.BifrostRoutingCall{call}}
			}

			priced := client.withResponseCost(ctx, resp)
			if priced.ChatResponse.ExtraFields.Cost == nil || priced.ChatResponse.ExtraFields.Cost.TotalCost != tc.want {
				t.Fatalf("cost = %+v, want total %v", priced.ChatResponse.ExtraFields.Cost, tc.want)
			}
			if !tc.stampedOnRes && (priced.ChatResponse.ExtraFields.RoutingMetadata != nil || resp.ChatResponse.ExtraFields.RoutingMetadata != nil) {
				t.Fatal("routing metadata leaked onto a response")
			}
			if tc.stampedOnRes && (priced.ChatResponse.ExtraFields.RoutingMetadata == nil || resp.ChatResponse.ExtraFields.RoutingMetadata == nil) {
				t.Fatal("the routing metadata the plugin stamped was removed from a response")
			}
			if resp.ChatResponse.ExtraFields.Cost != nil {
				t.Fatal("the cost was written onto the original response")
			}
		})
	}
}

// Agent turns 2+ return early through continueOnServedRoute; they are priced
// like any other request, which the agent loop's turn sum relies on.
func TestSendBackCostPricesAgentContinuationTurns(t *testing.T) {
	client := newSendBackCostClient(t, &costCatalog{responseCost: testCost(0.25)}, &costHookPlugin{}, true)
	ctx := newCostTestContext()
	ctx.SetValue(schemas.BifrostContextKeyMCPAgentServedRoute, &schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o"})

	resp, bifrostErr := client.ChatCompletionRequest(ctx, chatProbeRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest error: %v", bifrostErr.Error.Message)
	}
	if resp.ExtraFields.Cost == nil || resp.ExtraFields.Cost.TotalCost != 0.25 {
		t.Fatalf("cost = %+v, want the continuation turn priced", resp.ExtraFields.Cost)
	}
}

// The off path is what every request pays when nobody asked for the cost: a
// context read and an atomic load, no allocation. "on" is the copy and the
// pricing call with a catalog that does no work, so the catalog's own pricing
// cost (BenchmarkCalculateRequestCostBreakdownRealCtx in framework/modelcatalog)
// adds on top of it.
func BenchmarkWithResponseCost(b *testing.B) {
	catalog := &costCatalog{responseCost: testCost(0.25)}
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	for _, test := range []struct {
		name string
		on   bool
	}{{"off", false}, {"on", true}} {
		b.Run(test.name, func(b *testing.B) {
			client := newSendBackCostClient(b, catalog, &costHookPlugin{}, test.on)
			ctx := newCostTestContext()
			ctx.SetValue(schemas.BifrostContextKeyModelCatalog, catalog)
			b.ReportAllocs()
			for b.Loop() {
				_ = client.withResponseCost(ctx, resp)
			}
		})
	}
}

func BenchmarkWithErrorCost(b *testing.B) {
	catalog := &costCatalog{errorCost: testCost(0.1)}
	bifrostErr := &schemas.BifrostError{}
	for _, test := range []struct {
		name string
		on   bool
	}{{"off", false}, {"on", true}} {
		b.Run(test.name, func(b *testing.B) {
			client := newSendBackCostClient(b, catalog, &costHookPlugin{}, test.on)
			ctx := newCostTestContext()
			ctx.SetValue(schemas.BifrostContextKeyModelCatalog, catalog)
			b.ReportAllocs()
			for b.Loop() {
				_ = client.withErrorCost(ctx, bifrostErr)
			}
		})
	}
}
