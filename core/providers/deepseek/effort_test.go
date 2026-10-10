package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// captureAnthropicWire stands in for DeepSeek's /anthropic/v1/messages endpoint
// and records the decoded request body the provider actually sent.
func captureAnthropicWire(t *testing.T, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, captured); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "decode body", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
}

func anthropicEndpointKey() schemas.Key {
	return schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
}

// TestChatCompletion_AnthropicEndpointEmitsOutputConfigEffort: DeepSeek's
// Anthropic-compatible API takes reasoning depth through output_config.effort
// ("output_config: only effort is supported") and ignores thinking.budget_tokens.
// A caller's reasoning.effort must therefore reach the wire as
// output_config.effort rather than being collapsed into a budget the upstream
// discards.
func TestChatCompletion_AnthropicEndpointEmitsOutputConfigEffort(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := captureAnthropicWire(t, &captured)
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	effort := "medium"
	msg := "hello"
	_, bifrostErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &msg}}},
		Params:   &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: &effort}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; wire body = %#v", captured["output_config"], effort, captured)
	}
}

// TestResponses_AnthropicEndpointEmitsOutputConfigEffort covers the Responses
// converter, which has its own reasoning ladder.
func TestResponses_AnthropicEndpointEmitsOutputConfigEffort(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := captureAnthropicWire(t, &captured)
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	effort := "high"
	msg := "hello"
	_, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-pro",
		Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &msg}}},
		Params:   &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort}},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; wire body = %#v", captured["output_config"], effort, captured)
	}
}

// TestAnthropicEndpointOmitsThinkingBudgetOnEffortOnlyRequest: DeepSeek
// documents thinking.budget_tokens as ignored, so an effort-only request must
// not carry a synthesized one - it would be a dead field on the wire. Reported
// by @is911 on #6740 against head 54db65ce, where effort reached the wire
// correctly but a budget was still synthesized beside it.
//
// Both a v4 and a non-v4 model name are covered. The non-v4 case is the one
// that must keep failing open: an unrecognised name still reaches the
// provider-wide native-effort grant, so it gets the same treatment rather than
// silently falling back to a budget.
func TestAnthropicEndpointOmitsThinkingBudgetOnEffortOnlyRequest(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"deepseek-v4-flash", "deepseek-chat"} {
		t.Run("chat/"+model, func(t *testing.T) {
			t.Parallel()
			var captured map[string]any
			server := captureAnthropicWire(t, &captured)
			defer server.Close()

			provider, err := newTestDeepSeekProvider(server.URL)
			if err != nil {
				t.Fatalf("NewDeepSeekProvider: %v", err)
			}
			effort := "medium"
			msg := "hello"
			_, bifrostErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostChatRequest{
				Provider: schemas.DeepSeek,
				Model:    model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &msg}}},
				Params:   &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: &effort}},
			})
			if bifrostErr != nil {
				t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
			}
			assertEffortWithoutBudget(t, captured, effort)
		})

		t.Run("responses/"+model, func(t *testing.T) {
			t.Parallel()
			var captured map[string]any
			server := captureAnthropicWire(t, &captured)
			defer server.Close()

			provider, err := newTestDeepSeekProvider(server.URL)
			if err != nil {
				t.Fatalf("NewDeepSeekProvider: %v", err)
			}
			effort := "high"
			msg := "hello"
			_, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostResponsesRequest{
				Provider: schemas.DeepSeek,
				Model:    model,
				Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &msg}}},
				Params:   &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort}},
			})
			if bifrostErr != nil {
				t.Fatalf("Responses: %v", bifrostErr.Error.Message)
			}
			assertEffortWithoutBudget(t, captured, effort)
		})
	}
}

// assertEffortWithoutBudget pins both halves of the contract at once: the
// effort the caller asked for is on the wire, and no thinking budget rode along
// with it.
func assertEffortWithoutBudget(t *testing.T, captured map[string]any, effort string) {
	t.Helper()
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q; wire body = %#v", captured["output_config"], effort, captured)
	}
	thinking, present := captured["thinking"]
	if !present {
		return
	}
	block, ok := thinking.(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want absent or an object; wire body = %#v", thinking, captured)
	}
	if budget, has := block["budget_tokens"]; has {
		t.Fatalf("thinking.budget_tokens = %#v on an effort-only request; DeepSeek ignores it, so it must not be synthesized; wire body = %#v", budget, captured)
	}
}

// TestAnthropicEndpointKeepsEffortAlongsideExplicitBudget: a caller may send
// both reasoning.effort and reasoning.max_tokens. The explicit-budget branch
// honours the budget, and before this change it returned without ever emitting
// output_config.effort - so the effort was dropped for exactly the combination
// where the caller was most explicit about wanting it. Reported by
// @coderabbitai on #6740.
//
// responses.go already preserved a co-present effort in its adaptive-thinking
// sub-branch; this extends the same treatment to the branches that were missing
// it. The budget itself is untouched.
func TestAnthropicEndpointKeepsEffortAlongsideExplicitBudget(t *testing.T) {
	t.Parallel()
	const budget = 3000

	t.Run("chat", func(t *testing.T) {
		t.Parallel()
		var captured map[string]any
		server := captureAnthropicWire(t, &captured)
		defer server.Close()

		provider, err := newTestDeepSeekProvider(server.URL)
		if err != nil {
			t.Fatalf("NewDeepSeekProvider: %v", err)
		}
		effort := "medium"
		msg := "hello"
		_, bifrostErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostChatRequest{
			Provider: schemas.DeepSeek,
			Model:    "deepseek-v4-flash",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &msg}}},
			Params:   &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: &effort, MaxTokens: schemas.Ptr(budget)}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
		}
		assertEffortAndBudget(t, captured, effort, budget)
	})

	t.Run("responses", func(t *testing.T) {
		t.Parallel()
		var captured map[string]any
		server := captureAnthropicWire(t, &captured)
		defer server.Close()

		provider, err := newTestDeepSeekProvider(server.URL)
		if err != nil {
			t.Fatalf("NewDeepSeekProvider: %v", err)
		}
		effort := "high"
		msg := "hello"
		_, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), anthropicEndpointKey(), &schemas.BifrostResponsesRequest{
			Provider: schemas.DeepSeek,
			Model:    "deepseek-v4-pro",
			Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &msg}}},
			Params:   &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort, MaxTokens: schemas.Ptr(budget)}},
		})
		if bifrostErr != nil {
			t.Fatalf("Responses: %v", bifrostErr.Error.Message)
		}
		assertEffortAndBudget(t, captured, effort, budget)
	})
}

// assertEffortAndBudget pins that an explicit budget does not cost the caller
// their effort: both reach the wire.
func assertEffortAndBudget(t *testing.T, captured map[string]any, effort string, budget int) {
	t.Helper()
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != effort {
		t.Fatalf("output_config.effort = %#v, want %q alongside the explicit budget; wire body = %#v", captured["output_config"], effort, captured)
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want an object carrying the caller's budget; wire body = %#v", captured["thinking"], captured)
	}
	got, has := thinking["budget_tokens"].(float64)
	if !has || int(got) != budget {
		t.Fatalf("thinking.budget_tokens = %#v, want %d; the explicit budget must survive; wire body = %#v", thinking["budget_tokens"], budget, captured)
	}
}
