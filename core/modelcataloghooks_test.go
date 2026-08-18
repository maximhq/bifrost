package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// probeCatalog is a schemas.ModelInfoProvider returning known values, so a hook
// reading a real one proves the handle reached it rather than that some other
// lookup happened to succeed.
type probeCatalog struct {
	info *schemas.Model
	cost float64

	gotProvider schemas.ModelProvider
	gotModel    string
	infoCalls   int
	costCalls   int
}

func (p *probeCatalog) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	p.infoCalls++
	p.gotProvider = provider
	p.gotModel = model
	return p.info
}

func (p *probeCatalog) CalculateRequestCost(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) float64 {
	p.costCalls++
	return p.cost
}

// catalogProbePlugin reads ctx.GetModelInfo / ctx.CalculateCost from each hook
// and records what it saw, then short-circuits so no provider is called.
type catalogProbePlugin struct {
	preRequestInfo *schemas.Model
	preLLMInfo     *schemas.Model
	postInfo       *schemas.Model
	postCost       float64

	preRequestRan bool
	preLLMRan     bool
	postRan       bool
}

func (d *catalogProbePlugin) GetName() string { return "catalog-probe" }
func (d *catalogProbePlugin) Cleanup() error  { return nil }

func (d *catalogProbePlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	d.preRequestRan = true
	d.preRequestInfo = ctx.GetModelInfo(schemas.OpenAI, "gpt-4o")
	return nil
}

func (d *catalogProbePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	d.preLLMRan = true
	d.preLLMInfo = ctx.GetModelInfo(schemas.OpenAI, "gpt-4o")

	// Short-circuit rather than call a provider: this test is about the handle
	// reaching every hook, and a short-circuited response still runs
	// PostLLMHook, which is the hook cost is usually read from.
	return req, &schemas.LLMPluginShortCircuit{
		Response: &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				Model: "gpt-4o",
				Usage: &schemas.BifrostLLMUsage{
					PromptTokens:     5,
					CompletionTokens: 10,
					TotalTokens:      15,
				},
			},
		},
	}, nil
}

func (d *catalogProbePlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	d.postRan = true
	d.postInfo = ctx.GetModelInfo(schemas.OpenAI, "gpt-4o")
	d.postCost = ctx.CalculateCost(resp)
	return resp, bifrostErr, nil
}

func newCatalogProbeClient(t *testing.T, catalog schemas.ModelInfoProvider, plugins ...schemas.LLMPlugin) *Bifrost {
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
		LLMPlugins:   plugins,
	})
	if initErr != nil {
		t.Fatalf("Init failed: %v", initErr)
	}
	t.Cleanup(client.Shutdown)
	return client
}

func chatProbeRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
	}
}

// The accessors are unit-tested in schemas and the catalog is unit-tested in
// framework, but nothing covered the two meeting on a real request. This drives
// an actual ChatCompletionRequest through Init-supplied wiring and asserts a
// plugin reads the catalog from every hook it runs in.
//
// Worth having as a test rather than a manual check: no shipped plugin calls
// these accessors yet, so a break in the stamping would otherwise surface only
// in a third-party plugin.
func TestModelCatalogReachesPluginHooksOnRealRequest(t *testing.T) {
	want := &schemas.Model{ID: "gpt-4o", ContextLength: new(128000)}
	catalog := &probeCatalog{info: want, cost: 0.25}
	plugin := &catalogProbePlugin{}
	client := newCatalogProbeClient(t, catalog, plugin)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	defer ctx.Cancel()

	if _, bfErr := client.ChatCompletionRequest(ctx, chatProbeRequest()); bfErr != nil {
		t.Fatalf("ChatCompletionRequest failed: %v", bfErr)
	}

	if !plugin.preRequestRan || !plugin.preLLMRan || !plugin.postRan {
		t.Fatalf("hooks ran: preRequest=%v preLLM=%v post=%v, want all three",
			plugin.preRequestRan, plugin.preLLMRan, plugin.postRan)
	}

	// Every hook phase stamps the handle, so all three must resolve it.
	if plugin.preRequestInfo != want {
		t.Errorf("PreRequestHook GetModelInfo = %v, want the catalog's model", plugin.preRequestInfo)
	}
	if plugin.preLLMInfo != want {
		t.Errorf("PreLLMHook GetModelInfo = %v, want the catalog's model", plugin.preLLMInfo)
	}
	if plugin.postInfo != want {
		t.Errorf("PostLLMHook GetModelInfo = %v, want the catalog's model", plugin.postInfo)
	}
	if plugin.postCost != 0.25 {
		t.Errorf("PostLLMHook CalculateCost = %v, want 0.25", plugin.postCost)
	}

	// The arguments must arrive unchanged; a mangled provider would silently
	// return nil for every real model.
	if catalog.gotProvider != schemas.OpenAI || catalog.gotModel != "gpt-4o" {
		t.Errorf("catalog saw (%q, %q), want (openai, gpt-4o)", catalog.gotProvider, catalog.gotModel)
	}
	if catalog.costCalls != 1 {
		t.Errorf("CalculateRequestCost called %d times, want 1", catalog.costCalls)
	}
}

// Core is usable as a Go SDK with no catalog wired. Plugins written against the
// accessors must then degrade to zero values rather than panicking, or a plugin
// would be unusable outside the gateway.
func TestModelCatalogAbsentLeavesHooksInert(t *testing.T) {
	plugin := &catalogProbePlugin{}
	client := newCatalogProbeClient(t, nil, plugin)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	defer ctx.Cancel()

	if _, bfErr := client.ChatCompletionRequest(ctx, chatProbeRequest()); bfErr != nil {
		t.Fatalf("ChatCompletionRequest failed: %v", bfErr)
	}

	// Guard every hook, not just the last: a nil reading from a hook that never
	// ran would satisfy the assertions below for entirely the wrong reason.
	if !plugin.preRequestRan || !plugin.preLLMRan || !plugin.postRan {
		t.Fatalf("hooks ran: preRequest=%v preLLM=%v post=%v, want all three",
			plugin.preRequestRan, plugin.preLLMRan, plugin.postRan)
	}
	if plugin.preRequestInfo != nil || plugin.preLLMInfo != nil || plugin.postInfo != nil {
		t.Errorf("GetModelInfo returned non-nil with no catalog wired: preRequest=%v preLLM=%v post=%v",
			plugin.preRequestInfo, plugin.preLLMInfo, plugin.postInfo)
	}
	if plugin.postCost != 0 {
		t.Errorf("CalculateCost = %v with no catalog wired, want 0", plugin.postCost)
	}
}
