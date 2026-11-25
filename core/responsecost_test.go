package bifrost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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
// was given. It stamps CacheDebug there, as the semantic cache does. With
// passThrough it lets the request reach the provider; with recover it turns
// any error its PostLLMHook sees into a response; with skipFirstChunk it drops
// a stream's first chunk with a stream-control skip, as the JSON parser does.
type costHookPlugin struct {
	fail           bool
	passThrough    bool
	recover        bool
	skipFirstChunk bool

	chunksSeen atomic.Int32

	seenResponse *schemas.BifrostResponse
	seenError    *schemas.BifrostError
}

func (p *costHookPlugin) GetName() string { return "cost-hook" }
func (p *costHookPlugin) Cleanup() error  { return nil }

func (p *costHookPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (p *costHookPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if p.passThrough {
		return req, nil, nil
	}
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
	if p.skipFirstChunk && resp != nil && p.chunksSeen.Add(1) == 1 {
		return nil, &schemas.BifrostError{StreamControl: &schemas.StreamControl{SkipStream: schemas.Ptr(true)}}, nil
	}
	if p.recover && bifrostErr != nil {
		resp, bifrostErr = &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Model: "recovered"}}, nil
	}
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

// costStreamPlugin short-circuits a stream request with its own chunks, the way
// the semantic cache replays a cached stream: it marks the last chunk final
// inside its own PostLLMHook. When failLast is set the last chunk is an error.
// When record is set it keeps every chunk its PostLLMHook was given in seen.
type costStreamPlugin struct {
	chunks   int
	failLast bool
	record   bool

	seen []*schemas.BifrostChatResponse
	mu   sync.Mutex
}

func (p *costStreamPlugin) GetName() string { return "cost-stream" }
func (p *costStreamPlugin) Cleanup() error  { return nil }

func (p *costStreamPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (p *costStreamPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	stream := make(chan *schemas.BifrostStreamChunk, p.chunks)
	for i := range p.chunks {
		if p.failLast && i == p.chunks-1 {
			stream <- &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "replay failed"}}}
			continue
		}
		stream <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
				Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr("x")},
			}}},
			ExtraFields: schemas.BifrostResponseExtraFields{ChunkIndex: i},
		}}
	}
	close(stream)
	return req, &schemas.LLMPluginShortCircuit{Stream: stream}, nil
}

func (p *costStreamPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	isLast := bifrostErr != nil || (resp != nil && resp.ChatResponse != nil && resp.ChatResponse.ExtraFields.ChunkIndex == p.chunks-1)
	if isLast {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	}
	if p.record && resp != nil && resp.ChatResponse != nil {
		p.mu.Lock()
		p.seen = append(p.seen, resp.ChatResponse)
		p.mu.Unlock()
	}
	return resp, bifrostErr, nil
}

func newStreamCostClient(t testing.TB, baseURL string, catalog schemas.ModelInfoProvider, sendBackCost bool, plugins ...schemas.LLMPlugin) *Bifrost {
	t.Helper()
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, baseURL)
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:      account,
		Logger:       NewNoOpLogger(),
		ModelCatalog: catalog,
		LLMPlugins:   plugins,
		SendBackCost: sendBackCost,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(client.Shutdown)
	return client
}

func anthropicStreamRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-3-5-haiku-20241022",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}
}

// streamCosts drains a stream and returns, per chunk, the cost it carried (nil
// for none), plus the error chunks' costs.
func streamCosts(ch chan *schemas.BifrostStreamChunk) (chunkCosts, errorCosts []*schemas.BifrostCost) {
	for chunk := range ch {
		if chunk.BifrostError != nil {
			errorCosts = append(errorCosts, chunk.BifrostError.ExtraFields.Cost)
			continue
		}
		if chunk.BifrostChatResponse != nil {
			chunkCosts = append(chunkCosts, chunk.BifrostChatResponse.ExtraFields.Cost)
		}
	}
	return chunkCosts, errorCosts
}

func assertOnlyLastChunkPriced(t *testing.T, costs []*schemas.BifrostCost, want float64) {
	t.Helper()
	if len(costs) < 2 {
		t.Fatalf("got %d chunks, want a multi-chunk stream", len(costs))
	}
	for i, cost := range costs[:len(costs)-1] {
		if cost != nil {
			t.Fatalf("chunk %d of %d carries cost %+v, want only the last chunk priced", i, len(costs), cost)
		}
	}
	if last := costs[len(costs)-1]; last == nil || last.TotalCost != want {
		t.Fatalf("last chunk cost = %+v, want total %v", last, want)
	}
}

func TestSendBackCostPricesOnlyTheFinalStreamChunk(t *testing.T) {
	server := httptest.NewServer(anthropicMessagesHandler())
	defer server.Close()
	catalog := &costCatalog{responseCost: testCost(0.25)}
	client := newStreamCostClient(t, server.URL, catalog, true)

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed: %v", bifrostErr.Error.Message)
	}
	chunkCosts, errorCosts := streamCosts(stream)
	if len(errorCosts) != 0 {
		t.Fatalf("stream emitted %d error chunks", len(errorCosts))
	}
	assertOnlyLastChunkPriced(t, chunkCosts, 0.25)
	if calls := catalog.responseCalls.Load(); calls != 1 {
		t.Fatalf("catalog priced %d chunks, want only the final one", calls)
	}
}

func TestSendBackCostLeavesStreamsUnpricedWhenOff(t *testing.T) {
	server := httptest.NewServer(anthropicMessagesHandler())
	defer server.Close()
	catalog := &costCatalog{responseCost: testCost(0.25)}
	client := newStreamCostClient(t, server.URL, catalog, false)

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed: %v", bifrostErr.Error.Message)
	}
	chunkCosts, _ := streamCosts(stream)
	for i, cost := range chunkCosts {
		if cost != nil {
			t.Fatalf("chunk %d carries cost %+v with the setting off", i, cost)
		}
	}
	if calls := catalog.responseCalls.Load(); calls != 0 {
		t.Fatalf("catalog priced %d times with the setting off, want 0", calls)
	}
}

// A provider error after the stream started reaches the client as an error
// chunk, which carries the cost of the failed request.
func TestSendBackCostPricesMidStreamErrorChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, e := range []struct{ typ, data string }{
			{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-haiku-20241022","usage":{"input_tokens":10,"output_tokens":1}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`},
			{"error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		} {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.typ, e.data)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer server.Close()
	catalog := &costCatalog{responseCost: testCost(0.25), errorCost: testCost(0.1)}
	client := newStreamCostClient(t, server.URL, catalog, true)

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed before it started: %v", bifrostErr.Error.Message)
	}
	chunkCosts, errorCosts := streamCosts(stream)
	if len(errorCosts) != 1 || errorCosts[0] == nil || errorCosts[0].TotalCost != 0.1 {
		t.Fatalf("error chunk costs = %+v, want one error chunk priced at 0.1", errorCosts)
	}
	for i, cost := range chunkCosts {
		if cost != nil {
			t.Fatalf("content chunk %d carries cost %+v, want the cost on the error chunk only", i, cost)
		}
	}
}

// An error before the stream starts is returned, not streamed, and priced once
// however many places handled it.
func TestSendBackCostPricesPreStreamErrorOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(anthropicRateLimitHandler))
	defer server.Close()
	catalog := &costCatalog{errorCost: testCost(0.1)}
	client := newStreamCostClient(t, server.URL, catalog, true)

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if stream != nil {
		for range stream {
		}
	}
	if bifrostErr == nil {
		t.Fatal("stream started, want the provider's error")
	}
	if bifrostErr.ExtraFields.Cost == nil || bifrostErr.ExtraFields.Cost.TotalCost != 0.1 {
		t.Fatalf("error cost = %+v, want 0.1", bifrostErr.ExtraFields.Cost)
	}
	if calls := catalog.errorCalls.Load(); calls != 1 {
		t.Fatalf("error priced %d times, want once", calls)
	}
}

// A plugin's one-message answer to a stream request is the whole stream.
func TestSendBackCostPricesShortCircuitStreamAnswer(t *testing.T) {
	catalog := &costCatalog{responseCost: testCost(0.25)}
	client := newStreamCostClient(t, "http://127.0.0.1:1", catalog, true, &costHookPlugin{})

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed: %v", bifrostErr.Error.Message)
	}
	chunkCosts, _ := streamCosts(stream)
	if len(chunkCosts) != 1 || chunkCosts[0] == nil || chunkCosts[0].TotalCost != 0.25 {
		t.Fatalf("chunk costs = %+v, want one chunk priced at 0.25", chunkCosts)
	}
}

func TestSendBackCostPricesShortCircuitStreamErrorOnce(t *testing.T) {
	catalog := &costCatalog{errorCost: testCost(0.1)}
	client := newStreamCostClient(t, "http://127.0.0.1:1", catalog, true, &costHookPlugin{fail: true})

	_, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr == nil || bifrostErr.ExtraFields.Cost == nil || bifrostErr.ExtraFields.Cost.TotalCost != 0.1 {
		t.Fatalf("error = %+v, want the short-circuit error priced at 0.1", bifrostErr)
	}
	if calls := catalog.errorCalls.Load(); calls != 1 {
		t.Fatalf("error priced %d times, want once", calls)
	}
}

// A replayed stream is marked final by its plugin inside PostLLMHook, so the
// runner must read the end of stream after the hooks, and price on a copy.
func TestSendBackCostPricesReplayedStreamOnItsLastChunk(t *testing.T) {
	catalog := &costCatalog{responseCost: testCost(0.25)}
	plugin := &costStreamPlugin{chunks: 3, record: true}
	client := newStreamCostClient(t, "http://127.0.0.1:1", catalog, true, plugin)

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed: %v", bifrostErr.Error.Message)
	}
	chunkCosts, _ := streamCosts(stream)
	assertOnlyLastChunkPriced(t, chunkCosts, 0.25)

	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	for i, seen := range plugin.seen {
		if seen.ExtraFields.Cost != nil {
			t.Fatalf("the cost was written onto chunk %d the post-hook was given", i)
		}
	}
}

func TestSendBackCostPricesReplayedStreamErrorChunk(t *testing.T) {
	catalog := &costCatalog{responseCost: testCost(0.25), errorCost: testCost(0.1)}
	client := newStreamCostClient(t, "http://127.0.0.1:1", catalog, true, &costStreamPlugin{chunks: 2, failLast: true})

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed: %v", bifrostErr.Error.Message)
	}
	chunkCosts, errorCosts := streamCosts(stream)
	if len(errorCosts) != 1 || errorCosts[0] == nil || errorCosts[0].TotalCost != 0.1 {
		t.Fatalf("error chunk costs = %+v, want one priced at 0.1", errorCosts)
	}
	if len(chunkCosts) != 1 || chunkCosts[0] != nil {
		t.Fatalf("content chunk costs = %+v, want one unpriced chunk", chunkCosts)
	}
}

// BenchmarkSendBackCostStream drives a replayed stream of 10 and 20 chunks
// through the short-circuit stream runner, with no network, so allocations are
// exact. With the setting off, 10 and 20 chunks must differ only by what the
// runner already spends per chunk; on adds one pricing call and one copy, on
// the last chunk only, so on minus off is the same for both lengths.
func BenchmarkSendBackCostStream(b *testing.B) {
	for _, chunks := range []int{10, 20} {
		for _, test := range []struct {
			name string
			on   bool
		}{{"off", false}, {"on", true}} {
			b.Run(fmt.Sprintf("%s/%d-chunks", test.name, chunks), func(b *testing.B) {
				client := newStreamCostClient(b, "http://127.0.0.1:1", &costCatalog{responseCost: testCost(0.25)}, test.on, &costStreamPlugin{chunks: chunks})
				b.ReportAllocs()
				for b.Loop() {
					stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
					if bifrostErr != nil {
						b.Fatalf("stream failed: %v", bifrostErr.Error.Message)
					}
					for range stream {
					}
				}
			})
		}
	}
}

// A plugin can drop a chunk by returning a stream-control skip. No client sees
// that error, so it is never priced, and the stream's last chunk still is.
func TestSendBackCostSkipsDroppedChunkErrors(t *testing.T) {
	server := httptest.NewServer(anthropicMessagesHandler())
	defer server.Close()
	catalog := &costCatalog{responseCost: testCost(0.25), errorCost: testCost(0.1)}
	client := newStreamCostClient(t, server.URL, catalog, true, &costHookPlugin{passThrough: true, skipFirstChunk: true})

	stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream failed: %v", bifrostErr.Error.Message)
	}
	chunkCosts, errorCosts := streamCosts(stream)
	if len(errorCosts) != 0 {
		t.Fatalf("the dropped chunk reached the client as %d error chunks", len(errorCosts))
	}
	assertOnlyLastChunkPriced(t, chunkCosts, 0.25)
	if calls := catalog.errorCalls.Load(); calls != 0 {
		t.Fatalf("a dropped chunk's skip error was priced %d times, want 0", calls)
	}
}

// When a plugin recovers a stream's error into a response, core wraps that
// response in a one-chunk stream, and that chunk carries the cost.
func TestSendBackCostPricesRecoveredStreamErrors(t *testing.T) {
	rateLimited := httptest.NewServer(http.HandlerFunc(anthropicRateLimitHandler))
	defer rateLimited.Close()

	for _, test := range []struct {
		name    string
		baseURL string
		plugin  *costHookPlugin
		noKeys  bool
	}{
		// The plugin's own short-circuit error, recovered by the same plugin.
		{name: "short-circuit error", baseURL: "http://127.0.0.1:1", plugin: &costHookPlugin{fail: true, recover: true}},
		// A startup error the worker held back and then ended for plugins.
		{name: "held-back startup error", baseURL: rateLimited.URL, plugin: &costHookPlugin{passThrough: true, recover: true}},
		// An error the worker returned without a stream (no key to call with).
		{name: "worker error", baseURL: rateLimited.URL, plugin: &costHookPlugin{passThrough: true, recover: true}, noKeys: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &costCatalog{responseCost: testCost(0.25)}
			client := newStreamCostClient(t, test.baseURL, catalog, true, test.plugin)
			if test.noKeys {
				account := client.account.(*MockAccount)
				account.SetKeysForProvider(schemas.Anthropic, nil)
			}

			stream, bifrostErr := client.ChatCompletionStreamRequest(newCostTestContext(), anthropicStreamRequest())
			if bifrostErr != nil {
				t.Fatalf("stream failed, want the recovered response: %v", bifrostErr.Error.Message)
			}
			chunkCosts, _ := streamCosts(stream)
			if len(chunkCosts) != 1 || chunkCosts[0] == nil || chunkCosts[0].TotalCost != 0.25 {
				t.Fatalf("chunk costs = %+v, want the one recovered chunk priced at 0.25", chunkCosts)
			}
		})
	}
}
