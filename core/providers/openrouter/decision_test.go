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
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
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

// TestListModels_MergesLiveDecisionModels covers OpenRouter's dedicated decisions
// listing for the full catalog and its authenticated listing for the key's models.
// Each route lists its decision models once, and the key's list applies local filters.
func TestListModels_MergesLiveDecisionModels(t *testing.T) {
	paths := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/auth/key":
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/v1/models/user" && r.URL.Query().Get("output_modalities") == "all":
			fmt.Fprint(w, `{"data":[{"id":"microsoft/microsoft-decision-1"},{"id":"perplexity/pplx-decider-v1.1-27b"},{"id":"typesafe/jev-1.13"},{"id":"openai/gpt-4o"}]}`)
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
		allowed    []string
		unfiltered bool
		want       []string
		wantPaths  []string
	}{
		"all models": {
			allowed:    []string{"openai/gpt-4o"},
			unfiltered: true,
			want:       []string{"openrouter/microsoft/microsoft-decision-1", "openrouter/openai/gpt-4o", "openrouter/perplexity/pplx-decider-v1.1-27b", "openrouter/typesafe/jev-1.13"},
			wantPaths:  []string{"/v1/auth/key", "/v1/models", "/v1/embeddings/models", "/v1/models?output_modalities=decisions"},
		},
		"allowed list filters decision models too": {
			allowed:   []string{"openai/gpt-4o", "perplexity/pplx-decider-v1.1-27b"},
			want:      []string{"openrouter/openai/gpt-4o", "openrouter/perplexity/pplx-decider-v1.1-27b"},
			wantPaths: []string{"/v1/models/user?output_modalities=all"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			ctx.SetValue(schemas.BifrostContextKeyValidateKeys, true)
			keys := []schemas.Key{{Models: tt.allowed, Value: *schemas.NewSecretVar("fixture-key")}}
			resp, bifrostErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter, Unfiltered: tt.unfiltered})
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
			var gotPaths []string
			for len(paths) > 0 {
				gotPaths = append(gotPaths, <-paths)
			}
			if !slices.Equal(gotPaths, tt.wantPaths) {
				t.Errorf("upstream paths = %v, want %v", gotPaths, tt.wantPaths)
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
	defer ctx.Cancel()
	resp, bifrostErr := newDecisionTestProvider(server.URL).ListModels(ctx, []schemas.Key{{Models: []string{"*"}}}, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter, Unfiltered: true})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "openrouter/openai/gpt-4o" {
		t.Errorf("models = %+v, want only the chat model", resp.Data)
	}
}

const testImageURL = "data:image/png;base64,iVBORw0KGgo="

func TestListModels_UsesKeyScopedCatalog(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models/user":
			if r.Method != http.MethodGet || r.URL.RawQuery != "output_modalities=all" ||
				r.Header.Get("Authorization") != "Bearer fixture-key" {
				t.Errorf("expected an authenticated GET for all modalities, got %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"data":[{"id":"openai/allowed"},{"id":"qwen/embedding"},{"id":"typesafe/decision"}]}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"openai/allowed"},{"id":"qwen/embedding"},{"id":"typesafe/decision"}]}`)
		case "/v1/embeddings/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen/restricted-embedding"}]}`)
		case "/v1/auth/key":
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	ctx.SetValue(schemas.BifrostContextKeyValidateKeys, true)
	keys := []schemas.Key{{ID: "a", Value: *schemas.NewSecretVar("fixture-key"), Models: schemas.WhiteList{"*"}}}
	response, err := newDecisionTestProvider(server.URL).ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
	if err != nil {
		t.Fatalf("authenticated discovery failed: %v", err.GetErrorString())
	}
	want := []string{"openrouter/openai/allowed", "openrouter/qwen/embedding", "openrouter/typesafe/decision"}
	if got := listedModelIDs(response); !slices.Equal(got, want) {
		t.Errorf("models = %v, want %v", got, want)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func listedModelIDs(response *schemas.BifrostListModelsResponse) []string {
	var ids []string
	for _, model := range response.Data {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestListModels_KeyScopedCatalogPreservesAuthorizationHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer configured-key" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"authorization required"}}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"openai/allowed"}]}`)
	}))
	t.Cleanup(server.Close)
	provider := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL,
			ExtraHeaders: map[string]schemas.SecretVar{
				"Authorization": *schemas.NewSecretVar("Bearer configured-key"),
			},
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	response, err := provider.ListModels(ctx, []schemas.Key{{Models: schemas.WhiteList{"*"}}}, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
	if err != nil {
		t.Fatalf("list models with configured authorization: %s", err.GetErrorString())
	}
	if got := listedModelIDs(response); !slices.Equal(got, []string{"openrouter/openai/allowed"}) {
		t.Fatalf("models = %v, want the upstream model", got)
	}
}

func TestListModels_KeyScopedCatalogAppliesLocalFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"openai/allowed"},{"id":"openai/blocked"},{"id":"typesafe/decision"}]}`)
	}))
	t.Cleanup(server.Close)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	key := schemas.Key{
		ID:                "a",
		Value:             *schemas.NewSecretVar("fixture-key"),
		Models:            schemas.WhiteList{"regex:openai/.*", "openrouter/typesafe/decision", "friendly", "unavailable-alias", "nearby-alias", "openai/restricted"},
		BlacklistedModels: schemas.BlackList{"openrouter/openai/blocked"},
		Aliases: schemas.KeyAliases{
			"openrouter/friendly": {ModelID: "openrouter/openai/allowed"},
			"unavailable-alias":   {ModelID: "openai/restricted"},
			"nearby-alias":        {ModelID: "openai/allowed-20261001"},
		},
	}
	response, err := newDecisionTestProvider(server.URL).ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
	if err != nil {
		t.Fatalf("list models: %s", err.GetErrorString())
	}
	want := []string{"openrouter/friendly", "openrouter/openai/allowed", "openrouter/typesafe/decision"}
	if got := listedModelIDs(response); !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	if alias := response.Data[0].Alias; alias == nil || *alias != "openai/allowed" {
		t.Errorf("friendly alias did not retain its allowed upstream target: %+v", response.Data[0])
	}
}

type discoveryTestGrant struct {
	schemas.Grant
	access schemas.Access
}

func (g discoveryTestGrant) Access() schemas.Access { return g.access }

type discoveryTestAccess struct {
	schemas.Access
	keyIDs []string
}

func (discoveryTestAccess) IsModelAllowed(_ string, model string) bool {
	return model != "openai/caller-blocked"
}

func (a discoveryTestAccess) KeysForModel(_ string, model string) ([]string, bool) {
	if a.keyIDs != nil {
		return a.keyIDs, true
	}
	switch model {
	case "openai/wrong-key", "openai/shared":
		return []string{"b"}, true
	case "openai/no-keys":
		return nil, true
	default:
		return nil, false
	}
}

func TestListModels_KeyScopedCatalogQueriesOnlyAuthorizedKeys(t *testing.T) {
	for _, ids := range [][]string{{"b"}, {}} {
		t.Run(fmt.Sprintf("allowed keys %v", ids), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture-b" {
					t.Errorf("queried a key the caller cannot use: %s", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"data":[{"id":"openai/allowed"}]}`)
			}))
			t.Cleanup(server.Close)
			keys := []schemas.Key{
				{ID: "a", Value: *schemas.NewSecretVar("fixture-a"), Models: schemas.WhiteList{"*"}},
				{ID: "b", Value: *schemas.NewSecretVar("fixture-b"), Models: schemas.WhiteList{"*"}},
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			ctx.SetGrant(discoveryTestGrant{access: discoveryTestAccess{keyIDs: ids}})
			response, err := newDecisionTestProvider(server.URL).ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
			if err != nil {
				t.Fatalf("list models: %s", err.GetErrorString())
			}
			if got := calls.Load(); got != int32(len(ids)) {
				t.Errorf("upstream requests = %d, want %d", got, len(ids))
			}
			var want []string
			if len(ids) > 0 {
				want = []string{"openrouter/openai/allowed"}
			}
			if got := listedModelIDs(response); !slices.Equal(got, want) {
				t.Errorf("models = %v, want %v", got, want)
			}
			if len(response.KeyStatuses) != len(ids) {
				t.Errorf("key statuses = %+v, want only queried keys %v", response.KeyStatuses, ids)
			}
			if keys[0].ID != "a" || keys[1].ID != "b" {
				t.Fatal("list models changed the caller's keys")
			}
		})
	}
}

func TestListModels_KeyScopedCatalogCombinesAuthorizedKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer fixture-a":
			fmt.Fprint(w, `{"data":[{"id":"openai/a"},{"id":"openai/shared"},{"id":"openai/wrong-key"},{"id":"openai/no-keys"}]}`)
		case "Bearer fixture-b":
			fmt.Fprint(w, `{"data":[{"id":"openai/b"},{"id":"openai/shared"},{"id":"openai/caller-blocked"}]}`)
		default:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"message":"key forbidden"}}`)
		}
	}))
	t.Cleanup(server.Close)
	keys := []schemas.Key{
		{ID: "a", Value: *schemas.NewSecretVar("fixture-a"), Models: schemas.WhiteList{"*"}},
		{ID: "b", Value: *schemas.NewSecretVar("fixture-b"), Models: schemas.WhiteList{"*"}},
		{ID: "c", Value: *schemas.NewSecretVar("fixture-c"), Models: schemas.WhiteList{"*"}},
	}
	provider := newDecisionTestProvider(server.URL)
	request := &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter}
	for _, tc := range []struct {
		name  string
		setup func(*schemas.BifrostContext)
		want  []string
	}{
		{"configured keys", func(*schemas.BifrostContext) {}, []string{"openrouter/openai/a", "openrouter/openai/b", "openrouter/openai/caller-blocked", "openrouter/openai/no-keys", "openrouter/openai/shared", "openrouter/openai/wrong-key"}},
		{"caller grants", func(*schemas.BifrostContext) {}, []string{"openrouter/openai/a", "openrouter/openai/b", "openrouter/openai/shared"}},
		{"background", func(ctx *schemas.BifrostContext) { ctx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true) }, []string{"openrouter/openai/a", "openrouter/openai/b", "openrouter/openai/caller-blocked", "openrouter/openai/no-keys", "openrouter/openai/shared", "openrouter/openai/wrong-key"}},
		{"direct key", func(ctx *schemas.BifrostContext) { ctx.SetValue(schemas.BifrostContextKeyDirectKey, keys[0]) }, []string{"openrouter/openai/a", "openrouter/openai/no-keys", "openrouter/openai/shared", "openrouter/openai/wrong-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			if tc.name != "configured keys" {
				access := discoveryTestAccess{}
				if tc.name == "background" || tc.name == "direct key" {
					access.keyIDs = []string{"b"}
				}
				ctx.SetGrant(discoveryTestGrant{access: access})
			}
			tc.setup(ctx)
			selected := keys
			if tc.name == "direct key" {
				selected = keys[:1]
			}
			response, err := provider.ListModels(ctx, selected, request)
			if err != nil {
				t.Fatalf("list models: %s", err.GetErrorString())
			}
			if got := listedModelIDs(response); !slices.Equal(got, tc.want) {
				t.Errorf("models = %v, want %v", got, tc.want)
			}
			if len(response.KeyStatuses) != len(selected) {
				t.Fatalf("key statuses = %+v", response.KeyStatuses)
			}
			for _, status := range response.KeyStatuses {
				want := schemas.KeyStatusSuccess
				if status.KeyID == "c" {
					want = schemas.KeyStatusListModelsFailed
				}
				if status.Status != want {
					t.Errorf("key %q status = %s, want %s", status.KeyID, status.Status, want)
				}
			}
		})
	}
}

func TestListModels_KeyScopedCatalogErrorsAndEmptyResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"empty", http.StatusOK, `{"data":[]}`},
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"unauthorized"}}`},
		{"forbidden", http.StatusForbidden, `{"error":{"message":"forbidden"}}`},
		{"malformed", http.StatusOK, `{"data":[`},
		{"deadline", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 0 {
					time.Sleep(100 * time.Millisecond)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(server.Close)
			deadline := schemas.NoDeadline
			if tc.status == 0 {
				deadline = time.Now().Add(20 * time.Millisecond)
			}
			ctx := schemas.NewBifrostContext(context.Background(), deadline)
			defer ctx.Cancel()
			keys := []schemas.Key{{ID: "a", Value: *schemas.NewSecretVar("fixture-key"), Models: schemas.WhiteList{"openai/restricted"}}}
			response, err := newDecisionTestProvider(server.URL).ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: schemas.OpenRouter})
			if tc.name == "empty" {
				if err != nil || len(response.Data) != 0 || len(response.KeyStatuses) != 1 || response.KeyStatuses[0].Status != schemas.KeyStatusSuccess {
					t.Fatalf("expected an empty success, got %v and error %v", response, err)
				}
				return
			}
			if err == nil || response != nil {
				t.Fatalf("expected a discovery error, got %v and error %v", response, err)
			}
			if tc.status >= 400 && (err.StatusCode == nil || *err.StatusCode != tc.status) {
				t.Errorf("expected status %d, got %+v", tc.status, err.StatusCode)
			}
			if statuses := err.ExtraFields.KeyStatuses; len(statuses) != 1 || statuses[0].Status != schemas.KeyStatusListModelsFailed {
				t.Errorf("expected a failed key status, got %+v", statuses)
			}
		})
	}
}

func BenchmarkOpenRouterListModels(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/models/user":
			fmt.Fprint(w, `{"data":[{"id":"openai/chat"},{"id":"qwen/embedding"},{"id":"typesafe/decision"}]}`)
		case r.URL.Path == "/v1/embeddings/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen/embedding"}]}`)
		case r.URL.Query().Get("output_modalities") == "decisions":
			fmt.Fprint(w, `{"data":[{"id":"typesafe/decision"}]}`)
		default:
			fmt.Fprint(w, `{"data":[{"id":"openai/chat"}]}`)
		}
	}))
	b.Cleanup(server.Close)
	provider := newDecisionTestProvider(server.URL)
	keys := []schemas.Key{{ID: "a", Value: *schemas.NewSecretVar("fixture-key"), Models: schemas.WhiteList{"*"}}}

	for _, tc := range []struct {
		name       string
		provider   schemas.Provider
		unfiltered bool
		access     schemas.Access
		wantCount  int
	}{
		{name: "available", provider: provider, wantCount: 3},
		{name: "catalog", provider: provider, unfiltered: true, wantCount: 3},
		{name: "authorized", provider: provider, access: discoveryTestAccess{keyIDs: []string{"a"}}, wantCount: 3},
		{name: "legacy_openai", provider: openai.NewOpenAIProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		}, bifrost.NewDefaultLogger(schemas.LogLevelError)), wantCount: 1},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			if tc.access != nil {
				ctx.SetGrant(discoveryTestGrant{access: tc.access})
			}
			request := &schemas.BifrostListModelsRequest{Provider: tc.provider.GetProviderKey(), Unfiltered: tc.unfiltered}
			b.ReportAllocs()
			for b.Loop() {
				response, err := tc.provider.ListModels(ctx, keys, request)
				if err != nil || len(response.Data) != tc.wantCount {
					b.Fatalf("expected %d models, got %v, error: %v", tc.wantCount, response, err)
				}
			}
		})
	}
}

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
