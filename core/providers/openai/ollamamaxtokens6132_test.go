package openai

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

func chatWireFor(t *testing.T, provider schemas.ModelProvider, params *schemas.ChatParameters) gjson.Result {
	t.Helper()
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	req := ToOpenAIChatRequest(ctx, &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    "qwen2.5vl:32b",
		Input:    []schemas.ChatMessage{},
		Params:   params,
	})
	if req == nil {
		t.Fatal("nil request")
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return gjson.ParseBytes(wire)
}

// Regression for #6132: Ollama's OpenAI-compatible endpoint only reads the
// legacy max_tokens field, so a cap forwarded as max_completion_tokens is
// silently ignored and generation runs unbounded.
func TestOllamaChatRequestCarriesMaxTokens(t *testing.T) {
	wire := chatWireFor(t, schemas.Ollama, &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(5)})

	if got := wire.Get("max_tokens"); !got.Exists() || got.Int() != 5 {
		t.Errorf("max_tokens = %v, want 5 (client value un-clamped)", got)
	}
	if wire.Get("max_completion_tokens").Exists() {
		t.Error("max_completion_tokens must not be sent to Ollama")
	}
}

// The OpenAI wire shape must stay unchanged: max_completion_tokens with the
// 16-token floor applied.
func TestOpenAIChatRequestKeepsMaxCompletionTokens(t *testing.T) {
	wire := chatWireFor(t, schemas.OpenAI, &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(5)})

	if got := wire.Get("max_completion_tokens"); !got.Exists() || got.Int() != int64(MinMaxCompletionTokens) {
		t.Errorf("max_completion_tokens = %v, want %d", got, MinMaxCompletionTokens)
	}
	if wire.Get("max_tokens").Exists() {
		t.Error("max_tokens must not be sent to OpenAI")
	}
}

// A cap above the OpenAI floor passes through to Ollama unchanged, and no cap
// at all stays absent.
func TestOllamaChatRequestMaxTokensEdgeCases(t *testing.T) {
	wire := chatWireFor(t, schemas.Ollama, &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(4096)})
	if got := wire.Get("max_tokens"); got.Int() != 4096 {
		t.Errorf("max_tokens = %v, want 4096", got)
	}

	wire = chatWireFor(t, schemas.Ollama, &schemas.ChatParameters{})
	if wire.Get("max_tokens").Exists() || wire.Get("max_completion_tokens").Exists() {
		t.Error("no cap requested: neither token field should be present")
	}
}
