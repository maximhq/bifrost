package bifrost

import (
	"github.com/maximhq/bifrost/core/schemas"
	"testing"
)

func TestVirtualModelDisabled(t *testing.T) {
	engine := &Bifrost{}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "ordinary"}}
	if err := engine.resolveVirtualModel(ctx, req); err != nil {
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
		engine.resolveVirtualModel(ctx, req)
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

	if err := engine.resolveVirtualModel(ctx, req); err != nil {
		t.Fatalf("a concrete request addressed to openai was refused: %v", err)
	}
	provider, model, _ := req.GetRequestFields()
	if provider != schemas.OpenAI || model != "gpt-4o" {
		t.Fatalf("overlay rewrote a request addressed to openai: got %s/%s", provider, model)
	}
	if stub.resolved != 0 {
		t.Fatal("resolver consulted for a request addressed to another provider")
	}
	if schemas.GetVirtualModel(ctx) != "" {
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

		if err := engine.resolveVirtualModel(ctx, req); err != nil {
			t.Fatalf("provider %q: %v", provider, err)
		}
		gotProvider, gotModel, _ := req.GetRequestFields()
		if gotProvider != "ollama" || gotModel != "flash" {
			t.Fatalf("provider %q: resolved to %s/%s, want ollama/flash", provider, gotProvider, gotModel)
		}
		if schemas.GetVirtualModel(ctx) != "qwen38" {
			t.Fatalf("provider %q: virtual name not recorded on the context", provider)
		}
		ctx.Cancel()
	}
}
