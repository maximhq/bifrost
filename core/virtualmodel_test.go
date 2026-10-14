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
