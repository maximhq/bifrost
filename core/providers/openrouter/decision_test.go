package openrouter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openrouter"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
		Input:    schemas.DecisionInput{Text: schemas.Ptr("I was double charged and nobody replied. I want a refund today.")},
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_frustrated"), Instructions: schemas.NewDecisionText("Is the customer frustrated?")},
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("category"), Instructions: schemas.NewDecisionText("Pick the ticket category"), Choices: []schemas.DecisionChoice{
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("charges and refunds")},
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}, Description: schemas.NewDecisionText("anything else")},
			}},
			{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Instructions: schemas.NewDecisionText("Rate how urgently this needs a human reply"), Levels: []schemas.DecisionLevel{
				{Label: "0", Description: schemas.NewDecisionText("can wait a week")},
				{Label: "1", Description: schemas.NewDecisionText("needs a reply today")},
			}},
		},
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
			if len(resp.Answers) != 3 {
				t.Fatalf("answers = %+v", resp.Answers)
			}
			frustrated, category, urgency := resp.Answers[0], resp.Answers[1], resp.Answers[2]
			if *frustrated.Probability != 0.96 || *category.Choice.Str != "billing" || *urgency.Score != 0.99 {
				t.Errorf("answers not preserved: %+v", resp.Answers)
			}
			if category.Probabilities[0].Probability != 0.95 || *category.Probabilities[0].Value.Str != "billing" || *category.Confidence != 0.9 {
				t.Errorf("choice probabilities or confidence lost: %+v", category)
			}
			if urgency.Legend["1"] != "needs a reply today" {
				t.Errorf("score legend lost: %+v", urgency)
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

// TestDecision_EveryModelReachesNativeEndpoint pins that OpenRouter, not
// Bifrost, decides which models its decisions endpoint serves: every model is
// sent there, including one with no TypeSafe name (a newly served decisions
// model such as Clef, or a chat model OpenRouter rejects itself) and one whose
// datasheet row says it does not serve decisions.
func TestDecision_EveryModelReachesNativeEndpoint(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var wire map[string]any
		_ = json.Unmarshal(body, &wire)
		calls = append(calls, fmt.Sprintf("%s %v", r.URL.Path, wire["model"]))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, decisionResponseBody)
	}))
	defer server.Close()

	notDecisions := false
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if model == "typesafe/jev-router" {
			return &schemas.ModelCapabilities{SupportsDecisions: &notDecisions}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	provider := newDecisionTestProvider(server.URL)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
	for _, model := range []string{"cloudflare/clef", "openai/gpt-4o", "typesafe/jev-router", "~typesafe/jev-latest"} {
		t.Run(model, func(t *testing.T) {
			calls = nil
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if _, bifrostErr := provider.Decision(ctx, key, newDecisionRequest(model)); bifrostErr != nil {
				t.Fatalf("expected the native endpoint, got %v", bifrostErr.Error.Message)
			}
			if len(calls) != 1 || calls[0] != "/alpha/decisions "+model {
				t.Errorf("expected one native call for %s, got %v", model, calls)
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
	request := &schemas.BifrostDecisionRequest{Questions: []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_spam"), Instructions: schemas.NewDecisionText("Is this spam?")},
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

// TestListModels_MergesLiveDecisionModels pins that the decision models come from
// OpenRouter's decisions listing, which its default /v1/models omits: each listed model
// appears once, models it does not list (the old hardcoded alias) do not appear, and
// they go through the same per-key filtering as every other model.
func TestListModels_MergesLiveDecisionModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/models" && r.URL.Query().Get("output_modalities") == "decisions":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"microsoft/microsoft-decision-1"},{"id":"perplexity/pplx-decider-v1.1-27b"},{"id":"typesafe/jev-1.13"}]}`)
		case r.URL.Path == "/v1/models":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"openai/gpt-4o"},{"id":"typesafe/jev-1.13"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newDecisionTestProvider(server.URL)
	for name, tt := range map[string]struct {
		allowed []string
		want    []string
	}{
		"all models": {
			allowed: []string{"*"},
			want:    []string{"openrouter/microsoft/microsoft-decision-1", "openrouter/openai/gpt-4o", "openrouter/perplexity/pplx-decider-v1.1-27b", "openrouter/typesafe/jev-1.13"},
		},
		"allowed list filters decision models too": {
			allowed: []string{"openai/gpt-4o", "perplexity/pplx-decider-v1.1-27b"},
			want:    []string{"openrouter/openai/gpt-4o", "openrouter/perplexity/pplx-decider-v1.1-27b"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{{Models: tt.allowed}}, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
			if bifrostErr != nil {
				t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
			}
			ids := make([]string, 0, len(resp.Data))
			for _, model := range resp.Data {
				ids = append(ids, model.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tt.want) {
				t.Errorf("models = %v, want %v", ids, tt.want)
			}
		})
	}
}

// TestListModels_DecisionListingFailureKeepsOtherModels pins that the decisions listing
// is best-effort, like the embeddings one: when it fails, the rest of the list is served.
func TestListModels_DecisionListingFailureKeepsOtherModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("output_modalities") == "decisions" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"openai/gpt-4o"}]}`)
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := newDecisionTestProvider(server.URL).ListModels(ctx, []schemas.Key{{Models: []string{"*"}}}, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "openrouter/openai/gpt-4o" {
		t.Errorf("models = %+v, want only the chat model", resp.Data)
	}
}

const testImageURL = "data:image/png;base64,iVBORw0KGgo="

func encodeOpenRouterDecision(t *testing.T, request *schemas.BifrostDecisionRequest) string {
	t.Helper()
	native, err := openrouter.ToOpenRouterDecisionRequest(request)
	if err != nil {
		t.Fatalf("ToOpenRouterDecisionRequest: %v", err)
	}
	data, err := providerUtils.MarshalProviderRequest(native)
	if err != nil {
		t.Fatalf("MarshalProviderRequest: %v", err)
	}
	return string(data)
}

// TestToOpenRouterDecisionRequest_IsTypesafeBodyPlusUser pins that without images the
// body is exactly Typesafe's, plus OpenRouter's user field from safety_identifier.
func TestToOpenRouterDecisionRequest_IsTypesafeBodyPlusUser(t *testing.T) {
	request := newDecisionRequest("typesafe/jev-1.13")
	native, err := typesafe.ToTypesafeDecisionRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	want, err := providerUtils.MarshalProviderRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	if got := encodeOpenRouterDecision(t, request); got != string(want) {
		t.Errorf("body without safety_identifier =\n%s\nwant Typesafe's\n%s", got, want)
	}

	request.SafetyIdentifier = schemas.Ptr("user-123")
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encodeOpenRouterDecision(t, request)), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["user"]) != `"user-123"` {
		t.Errorf("user = %s, want the safety_identifier", body["user"])
	}
}

// TestToOpenRouterDecisionRequest_SendsImagesAsStateItems pins OpenRouter's documented
// multimodal layout: plain-string text items and image_url items, in order, at the top
// level of the state array. A message object there reaches the model as JSON text.
func TestToOpenRouterDecisionRequest_SendsImagesAsStateItems(t *testing.T) {
	request := newDecisionRequest("perplexity/pplx-decider-v1.1-27b")
	request.Input = schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
		Role: "user",
		Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("Look at this")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(testImageURL)},
		}},
	}}}
	body := encodeOpenRouterDecision(t, request)
	want := `"state":["Look at this",{"type":"image_url","image_url":{"url":"` + testImageURL + `"}}]`
	if !strings.Contains(body, want) {
		t.Errorf("body =\n%s\nwant state\n%s", body, want)
	}
}

// TestDecision_ForwardsProviderHeaders pins that OpenRouter's response headers reach the
// caller on success (in the response) and on failure (on the context, where the HTTP
// layer reads them for an error reply).
func TestDecision_ForwardsProviderHeaders(t *testing.T) {
	for name, status := range map[string]int{"success": http.StatusOK, "error": http.StatusTooManyRequests} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Generation-Id", "gen-dec-1")
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = fmt.Fprint(w, decisionResponseBody)
					return
				}
				_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
			}))
			defer server.Close()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, _ := newDecisionTestProvider(server.URL).Decision(ctx, schemas.Key{Value: schemas.SecretVar{Val: "k"}}, newDecisionRequest("typesafe/jev-1.13"))
			headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
			if status == http.StatusOK {
				if resp == nil {
					t.Fatal("expected a response")
				}
				headers = resp.ExtraFields.ProviderResponseHeaders
			}
			got := make(map[string]string, len(headers))
			for k, v := range headers {
				got[strings.ToLower(k)] = v
			}
			if got["x-generation-id"] != "gen-dec-1" || got["retry-after"] != "7" {
				t.Errorf("headers = %v, want x-generation-id and retry-after", headers)
			}
		})
	}
}
