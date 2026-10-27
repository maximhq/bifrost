package bifrost

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"testing"
)

func TestVirtualModelDisabled(t *testing.T) {
	engine := &Bifrost{}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "ordinary"}}
	if err := engine.resolveVirtualModel(ctx, req, schemas.OpenAI); err != nil {
		t.Fatal(err)
	}
	provider, model, _ := req.GetRequestFields()
	if provider != schemas.OpenAI || model != "ordinary" {
		t.Fatal("disabled resolver changed request")
	}
}

func BenchmarkVirtualModelDisabled(b *testing.B) {
	engine := &Bifrost{}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "ordinary"}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		engine.resolveVirtualModel(ctx, req, schemas.OpenAI)
	}
}

// stubVirtualResolver stands in for the engines' service. It answers one name and counts how
// often core asked it to resolve, so a request that is not a virtual model request can be shown
// to have left the overlay alone entirely rather than merely produced no error.
type stubVirtualResolver struct {
	vm       *schemas.VirtualModel
	target   *schemas.VirtualModelResolution
	resolved int
}

func (s *stubVirtualResolver) Lookup(name string) (*schemas.VirtualModel, bool) {
	if s.vm.Matches(name) {
		return s.vm, true
	}
	return nil, false
}

func (s *stubVirtualResolver) List() []*schemas.VirtualModel { return []*schemas.VirtualModel{s.vm} }

func (s *stubVirtualResolver) Resolve(*schemas.BifrostContext, *schemas.VirtualModel) (*schemas.VirtualModelResolution, *schemas.BifrostError) {
	s.resolved++
	return s.target, nil
}

func stubEngine(vm *schemas.VirtualModel, target *schemas.VirtualModelResolution) (*Bifrost, *stubVirtualResolver) {
	engine := &Bifrost{}
	stub := &stubVirtualResolver{vm: vm, target: target}
	var resolver schemas.VirtualModelResolver = stub
	engine.virtualModels.Store(&resolver)
	return engine, stub
}

// A request that names a provider explicitly addresses that provider's own model of the same
// name, and that is a real configuration: a virtual model named after a vendor model is the usual
// migration shape, and provider/model is the escape hatch that pins the vendor. The overlay must
// not shadow it, so a provider disagreeing with the owner means "not mine" and the request
// continues untouched for the named provider to answer.
func TestVirtualModelDoesNotShadowAnotherProvidersModel(t *testing.T) {
	engine, stub := stubEngine(
		&schemas.VirtualModel{Name: "gpt-4o", Provider: "ollama", Models: []string{"llama3.1:8b"}},
		&schemas.VirtualModelResolution{Provider: "ollama", Model: "llama3.1:8b"},
	)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}}

	if err := engine.resolveVirtualModel(ctx, req, schemas.OpenAI); err != nil {
		t.Fatalf("a concrete request addressed to openai was refused: %v", err)
	}
	provider, model, _ := req.GetRequestFields()
	if provider != schemas.OpenAI || model != "gpt-4o" {
		t.Fatalf("overlay rewrote a request addressed to openai: got %s/%s", provider, model)
	}
	if stub.resolved != 0 {
		t.Fatal("resolver consulted for a request addressed to another provider")
	}
	if name, _ := ctx.Value(schemas.BifrostContextKeyVirtualModel).(string); name != "" {
		t.Fatal("virtual name recorded for a request that is not a virtual model request")
	}
}

// Naming the owning provider, in either case, or no provider at all, is the construct working as
// intended: the name is resolved and the decision recorded.
func TestVirtualModelResolvesForOwningProvider(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{"", "ollama", "Ollama"} {
		engine, _ := stubEngine(
			&schemas.VirtualModel{Name: "qwen38", Provider: "ollama", Models: []string{"flash", "27b"}},
			&schemas.VirtualModelResolution{Provider: "ollama", Model: "flash"},
		)
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: provider, Model: "qwen38"}}

		if err := engine.resolveVirtualModel(ctx, req, provider); err != nil {
			t.Fatalf("provider %q: %v", provider, err)
		}
		gotProvider, gotModel, _ := req.GetRequestFields()
		if gotProvider != "ollama" || gotModel != "flash" {
			t.Fatalf("provider %q: resolved to %s/%s, want ollama/flash", provider, gotProvider, gotModel)
		}
		if name, _ := ctx.Value(schemas.BifrostContextKeyVirtualModel).(string); name != "qwen38" {
			t.Fatalf("provider %q: virtual name not recorded on the context", provider)
		}
		ctx.Cancel()
	}
}

// The provider on the request is not necessarily the provider the client named: the model-catalog
// resolver and governance load balancing both fill one in for a bare name. That inferred value must
// not be read as the client pinning a vendor, or a virtual model whose name collides with a vendor
// model would step aside and send the request to that vendor instead, with nothing to show for it.
// This is the case the bare-name feature exists for.
func TestVirtualModelIgnoresProviderInferredByHooks(t *testing.T) {
	engine, _ := stubEngine(
		&schemas.VirtualModel{Name: "gpt-4o", Provider: "ollama", Models: []string{"flash", "27b"}},
		&schemas.VirtualModelResolution{Provider: "ollama", Model: "flash"},
	)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()

	// req carries openai because a hook put it there; the client sent a bare name.
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}}
	if err := engine.resolveVirtualModel(ctx, req, ""); err != nil {
		t.Fatalf("%v", err)
	}
	provider, model, _ := req.GetRequestFields()
	if provider != "ollama" || model != "flash" {
		t.Fatalf("a catalog-inferred provider overrode the virtual model: got %s/%s, want ollama/flash", provider, model)
	}
}

// A bare model name leaves the catalog resolver having promoted every other provider serving it,
// marked Inferred. Once the virtual model resolves, that chain answers a question the request no
// longer asks: the next hop after a local 5xx would be a paid vendor the client never named.
func TestVirtualModelResolutionDropsInferredFallbacks(t *testing.T) {
	engine, stub := stubEngine(
		&schemas.VirtualModel{Name: "gpt-4o", Provider: "ollama", Models: []string{"flash"}},
		&schemas.VirtualModelResolution{Provider: "ollama", Model: "flash"},
	)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
		Model: "gpt-4o",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o", Inferred: true},
			{Provider: schemas.Anthropic, Model: "gpt-4o"},
		},
	}}

	if err := engine.resolveVirtualModel(ctx, req, ""); err != nil {
		t.Fatalf("%v", err)
	}
	_, model, fallbacks := req.GetRequestFields()
	if model != "flash" {
		t.Fatalf("model = %q, want the resident candidate", model)
	}
	if len(fallbacks) != 1 || fallbacks[0].Provider != schemas.Anthropic {
		t.Fatalf("fallbacks after resolution = %+v, want only the entry the caller wrote", fallbacks)
	}
	if stub.resolved != 1 {
		t.Fatalf("resolve calls = %d, want 1", stub.resolved)
	}
}

func BenchmarkVirtualModelEnabledMiss(b *testing.B) {
	engine, _ := stubEngine(
		&schemas.VirtualModel{Name: "qwen38", Provider: "ollama", Models: []string{"flash"}},
		&schemas.VirtualModelResolution{Provider: "ollama", Model: "flash"},
	)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	// Feature configured, this model not virtual: the ordinary request every caller makes.
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := engine.resolveVirtualModel(ctx, req, schemas.OpenAI); err != nil {
			b.Fatal(err)
		}
	}
}

// virtualAnsweringPlugin answers requests for one provider itself, as a cache hit does.
type virtualAnsweringPlugin struct{ answers schemas.ModelProvider }

func (p *virtualAnsweringPlugin) GetName() string { return "virtual-answering" }
func (p *virtualAnsweringPlugin) Cleanup() error  { return nil }
func (p *virtualAnsweringPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *virtualAnsweringPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if provider, _, _ := req.GetRequestFields(); provider != p.answers {
		return req, nil, nil
	}
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{ID: "answered-by-plugin", Model: "flash"},
	}}, nil
}
func (p *virtualAnsweringPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// A plugin's answer runs on no provider worker, and the worker is what normally records the
// route, so an answer used to come back with no routing_info at all. For a virtual model that
// loses the only place the name the client sent survives.
func TestVirtualModelAnsweredByAPluginKeepsTheVirtualName(t *testing.T) {
	account := NewMockAccount()
	account.AddProviderWithBaseURL("ollama", 1, 1, "http://127.0.0.1:1")
	account.SetKeysForProvider("ollama", []schemas.Key{{ID: "ollama-key", Value: *schemas.NewSecretVar("k"), Models: schemas.WhiteList{"*"}, Weight: 1}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&virtualAnsweringPlugin{answers: "ollama"}},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)

	var resolver schemas.VirtualModelResolver = &stubVirtualResolver{
		vm:     &schemas.VirtualModel{Name: "qwen38", Provider: "ollama", Models: []string{"flash", "27b"}},
		target: &schemas.VirtualModelResolution{Provider: "ollama", Model: "flash"},
	}
	client.virtualModels.Store(&resolver)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	resp, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: "ollama",
		Model:    "qwen38",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	})
	if bifrostErr != nil {
		t.Fatalf("request failed: %v", bifrostErr.Error.Message)
	}
	route := resp.ExtraFields.RoutingInfo
	if route.Provider != "ollama" || route.Model != "flash" {
		t.Fatalf("answer names %s/%s, want ollama/flash: %+v", route.Provider, route.Model, route)
	}
	if route.RequestedModel != "qwen38" {
		t.Fatalf("requested_model = %q, want the virtual name the client sent", route.RequestedModel)
	}
}
