package openrouter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openrouter"
	"github.com/maximhq/bifrost/core/schemas"
)

const decisionResponseBody = `{
  "id": "gen-dec-1",
  "model": "typesafe/jev-1.13-20260917",
  "provider": "TypeSafe",
  "answers": {
    "is_frustrated": {"type": "noul", "noul": 0.96},
    "category": {"type": "choice", "choice": "billing", "confidence": 0.9, "probabilities": {"billing": 0.95, "other": 0.05}},
    "urgency": {"type": "score", "score": 0.99, "confidence": 0.98, "probabilities": {"0": 0.01, "1": 0.99}, "legend": {"0": "can wait a week", "1": "needs a reply today"}}
  },
  "usage": {"input_tokens": 476, "output_tokens": 70, "cost": 0.000019992}
}`

func newDecisionTestProvider(baseURL string) *openrouter.OpenRouterProvider {
	return openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
}

func newDecisionRequest(model string) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.OpenRouter,
		Model:    model,
		State:    "I was double charged and nobody replied. I want a refund today.",
		Questions: map[string]schemas.DecisionQuestion{
			"is_frustrated": {Kind: schemas.DecisionKindNoul, Instructions: "Is the customer frustrated?"},
			"category": {
				Kind:         schemas.DecisionKindChoice,
				Instructions: "Pick the ticket category",
				Criteria:     map[string]interface{}{"billing": "charges and refunds", "other": "anything else"},
			},
			"urgency": {
				Kind:         schemas.DecisionKindScore,
				Instructions: "Rate how urgently this needs a human reply",
				Criteria:     []interface{}{"can wait a week", "needs a reply today"},
			},
		},
	}
}

func TestIsTypesafeModel(t *testing.T) {
	cases := map[string]bool{
		"typesafe/jev-1.13":    true,
		"~typesafe/jev-latest": true,
		"TypeSafe/jev-1.13":    true,
		"openai/gpt-4o":        false,
		"typesafe-jev":         false,
		"nottypesafe/jev":      false,
		"":                     false,
	}
	for model, want := range cases {
		if got := schemas.IsTypesafeModel(model); got != want {
			t.Errorf("IsTypesafeModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestDecision_NonTypesafeModelIsUnsupported(t *testing.T) {
	provider := newDecisionTestProvider("http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newDecisionRequest("openai/gpt-4o"))
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation so core emulates, got %+v", bifrostErr)
	}
}

func TestDecision_TypesafeModelUsesNativeEndpoint(t *testing.T) {
	for _, model := range []string{"typesafe/jev-1.13", "~typesafe/jev-latest"} {
		t.Run(model, func(t *testing.T) {
			var capturedPath, capturedAuth string
			var capturedBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				capturedAuth = r.Header.Get("Authorization")
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, &capturedBody); err != nil {
					t.Errorf("request body is not JSON: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, decisionResponseBody)
			}))
			defer server.Close()

			provider := newDecisionTestProvider(server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
			resp, bifrostErr := provider.Decision(ctx, key, newDecisionRequest(model))
			if bifrostErr != nil {
				t.Fatalf("Decision: %v", bifrostErr.Error.Message)
			}

			if capturedPath != "/alpha/decisions" {
				t.Errorf("path = %q, want /alpha/decisions", capturedPath)
			}
			if capturedAuth != "Bearer test-api-key" {
				t.Errorf("Authorization = %q", capturedAuth)
			}
			if capturedBody["model"] != model {
				t.Errorf("wire model = %v, want %q", capturedBody["model"], model)
			}
			question := capturedBody["questions"].(map[string]any)["category"].(map[string]any)
			if question["type"] != "choice" || question["kind"] != nil {
				t.Errorf("wire question must use the native type field, got %v", question)
			}

			if resp.ID != "gen-dec-1" {
				t.Errorf("id = %q, want generation id", resp.ID)
			}
			if resp.Model != "typesafe/jev-1.13-20260917" {
				t.Errorf("model = %q, want resolved upstream model", resp.Model)
			}
			if resp.Answers["is_frustrated"].Value != 0.96 || resp.Answers["category"].Value != "billing" || resp.Answers["urgency"].Value != 0.99 {
				t.Errorf("answers not preserved: %+v", resp.Answers)
			}
			if resp.Answers["category"].Probabilities["billing"] != 0.95 || *resp.Answers["category"].Confidence != 0.9 {
				t.Errorf("choice probabilities or confidence lost: %+v", resp.Answers["category"])
			}
			if resp.Answers["urgency"].Legend["1"] != "needs a reply today" {
				t.Errorf("score legend lost: %+v", resp.Answers["urgency"])
			}
			if resp.Usage == nil || resp.Usage.PromptTokens != 476 || resp.Usage.CompletionTokens != 70 || resp.Usage.TotalTokens != 546 {
				t.Fatalf("usage tokens = %+v", resp.Usage)
			}
			if resp.Usage.Cost == nil || resp.Usage.Cost.TotalCost != 0.000019992 {
				t.Errorf("billed cost not carried: %+v", resp.Usage.Cost)
			}
		})
	}
}

func TestDecision_UpstreamErrorPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = fmt.Fprint(w, `{"error":{"code":402,"message":"Insufficient credits. Add more using https://openrouter.ai/credits"}}`)
	}))
	defer server.Close()

	provider := newDecisionTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newDecisionRequest("typesafe/jev-1.13"))
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected upstream 402, got %+v", bifrostErr)
	}
	if !strings.Contains(bifrostErr.Error.Message, "Insufficient credits") {
		t.Errorf("upstream message lost: %q", bifrostErr.Error.Message)
	}
}

func TestToBifrostDecisionResponse_CostOnlyWhenBilled(t *testing.T) {
	request := &schemas.BifrostDecisionRequest{Questions: map[string]schemas.DecisionQuestion{
		"is_spam": {Kind: schemas.DecisionKindNoul, Instructions: "Is this spam?"},
	}}
	tests := []struct {
		name string
		cost *float64
	}{
		{"absent", nil},
		{"zero", schemas.Ptr(0.0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var native openrouter.OpenRouterDecisionResponse
			if err := json.Unmarshal([]byte(`{"model":"typesafe/jev-1.13","answers":{"is_spam":{"type":"noul","noul":0.2}},"usage":{"input_tokens":3,"output_tokens":0}}`), &native); err != nil {
				t.Fatal(err)
			}
			native.Usage.Cost = tt.cost
			resp, bifrostErr := openrouter.ToBifrostDecisionResponse(&native, request)
			if bifrostErr != nil {
				t.Fatalf("ToBifrostDecisionResponse: %v", bifrostErr.Error.Message)
			}
			if resp.Usage == nil || resp.Usage.PromptTokens != 3 {
				t.Fatalf("token usage lost: %+v", resp.Usage)
			}
			if resp.Usage.Cost != nil {
				t.Errorf("cost must stay nil so the datasheet prices the request, got %+v", resp.Usage.Cost)
			}
		})
	}
}

func TestListModels_MergesDecisionModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"openai/gpt-4o"},{"id":"typesafe/jev-1.13"}]}`)
	}))
	defer server.Close()

	provider := newDecisionTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{{Models: []string{"*"}}}, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}

	ids := make(map[string]int, len(resp.Data))
	for _, model := range resp.Data {
		ids[model.ID]++
	}
	for _, want := range []string{"openrouter/openai/gpt-4o", "openrouter/typesafe/jev-1.13", "openrouter/~typesafe/jev-latest"} {
		if ids[want] != 1 {
			t.Errorf("%s listed %d times, want exactly once in %v", want, ids[want], ids)
		}
	}
	if len(resp.Data) != 3 {
		t.Errorf("got %d models, want 3: %v", len(resp.Data), ids)
	}
}
