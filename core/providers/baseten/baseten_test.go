package baseten_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/baseten"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestBaseten(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("BASETEN_API_KEY")) == "" {
		t.Skip("Skipping Baseten tests because BASETEN_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider: schemas.Baseten,
		// gpt-oss-120b emits one tool call per turn even when asked for two, so the
		// multiple-tool-call scenarios use GLM-5.3, which returns both.
		ChatModel: "zai-org/GLM-5.3",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Baseten, Model: "openai/gpt-oss-120b"},
		},
		ReasoningModel: "zai-org/GLM-5.3",
		DecisionModel:  "decision-models/baseten-joey-2b-latest",
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			CompleteEnd2End:       true,
			ListModels:            true,
			Reasoning:             true,
			Decision:              true,
		},
	}

	t.Run("BasetenTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// capturedRequest is what the stub upstream saw for one call.
type capturedRequest struct {
	Path string
	Auth string
	Body map[string]any
}

// newStub serves canned OpenAI-shaped bodies and records every request.
func newStub(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		seen = append(seen, capturedRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	return server, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), seen...)
	}
}

func newProvider(t *testing.T, baseURL string) *baseten.BasetenProvider {
	t.Helper()
	provider, err := baseten.NewBasetenProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 10},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewBasetenProvider: %v", err)
	}
	return provider
}

// contextWithDeployment mimics core's alias resolution: request.Model is already the
// alias model_id and the resolved alias rides on the context.
func contextWithDeployment(deploymentURL string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if deploymentURL != "" {
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
			Key: "my-deployment",
			Config: &schemas.AliasConfig{
				ModelID:         "not-required",
				BasetenAliasCfg: &schemas.BasetenAliasCfg{DeploymentURL: schemas.NewSecretVar(deploymentURL)},
			},
		})
	}
	return ctx
}

var testKey = schemas.Key{Value: *schemas.NewSecretVar("bt-test-key")}

func chatRequest(model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Baseten,
		Model:    model,
		Input:    []schemas.ChatMessage{llmtests.CreateBasicChatMessage("hello")},
	}
}

const chatCompletionBody = `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

func writeChat(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, chatCompletionBody)
}

func TestBasetenChatUsesModelAPIsWithoutAlias(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, writeChat)
	provider := newProvider(t, server.URL+"/v1")

	if _, err := provider.ChatCompletion(contextWithDeployment(""), testKey, chatRequest("openai/gpt-oss-120b")); err != nil {
		t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
	}
	got := seen()
	if len(got) != 1 || got[0].Path != "/v1/chat/completions" {
		t.Fatalf("expected one call to /v1/chat/completions, got %+v", got)
	}
	if got[0].Auth != "Bearer bt-test-key" {
		t.Fatalf("expected Bearer auth, got %q", got[0].Auth)
	}
	if got[0].Body["model"] != "openai/gpt-oss-120b" {
		t.Fatalf("expected the Model APIs slug on the wire, got %v", got[0].Body["model"])
	}
}

func TestBasetenChatUsesAliasDeploymentURL(t *testing.T) {
	t.Parallel()
	modelAPIs, modelAPIsSeen := newStub(t, writeChat)
	deployment, deploymentSeen := newStub(t, writeChat)
	provider := newProvider(t, modelAPIs.URL+"/v1")

	ctx := contextWithDeployment(deployment.URL + "/environments/production/sync/v1/")
	if _, err := provider.ChatCompletion(ctx, testKey, chatRequest("not-required")); err != nil {
		t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
	}
	if n := len(modelAPIsSeen()); n != 0 {
		t.Fatalf("expected no Model APIs call for a deployment alias, got %d", n)
	}
	got := deploymentSeen()
	if len(got) != 1 || got[0].Path != "/environments/production/sync/v1/chat/completions" {
		t.Fatalf("expected one call to the deployment chat route, got %+v", got)
	}
	if got[0].Body["model"] != "not-required" {
		t.Fatalf("expected the alias model_id on the wire, got %v", got[0].Body["model"])
	}
}

func TestBasetenEmptyDeploymentURLFails(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, writeChat)
	provider := newProvider(t, server.URL+"/v1")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "my-deployment",
		Config: &schemas.AliasConfig{
			ModelID:         "not-required",
			BasetenAliasCfg: &schemas.BasetenAliasCfg{DeploymentURL: schemas.NewSecretVar("")},
		},
	})
	if _, err := provider.ChatCompletion(ctx, testKey, chatRequest("not-required")); err == nil {
		t.Fatal("expected an error for an empty deployment_url, got success")
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream call, got %d", n)
	}
}

func TestBasetenChatRequestFitsBasetenSchema(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, writeChat)
	provider := newProvider(t, server.URL+"/v1")

	req := chatRequest("openai/gpt-oss-120b")
	req.Input = append([]schemas.ChatMessage{{
		Role:    schemas.ChatMessageRoleDeveloper,
		Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Answer briefly.")},
	}}, req.Input...)
	tier := schemas.BifrostServiceTier("flex")
	req.Params = &schemas.ChatParameters{
		MaxCompletionTokens: schemas.Ptr(64),
		ServiceTier:         &tier,
		Store:               schemas.Ptr(true),
		PromptCacheKey:      schemas.Ptr("k"),
		Metadata:            &map[string]any{"team": "a"},
		SafetyIdentifier:    schemas.Ptr("user-1"),
		Modalities:          []string{"text"},
		Speed:               schemas.Ptr("fast"),
		TopK:                schemas.Ptr(20),
	}
	if _, err := provider.ChatCompletion(contextWithDeployment(""), testKey, req); err != nil {
		t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
	}
	body := seen()[0].Body
	if body["max_tokens"] != float64(64) {
		t.Fatalf("expected max_tokens=64, got %v", body["max_tokens"])
	}
	if body["top_k"] != float64(20) {
		t.Fatalf("expected top_k, which Baseten's schema defines, to be kept, got %v", body["top_k"])
	}
	for _, field := range []string{"max_completion_tokens", "service_tier", "store", "prompt_cache_key", "metadata", "safety_identifier", "modalities", "speed"} {
		if _, ok := body[field]; ok {
			t.Fatalf("expected %s to be stripped, body: %v", field, body)
		}
	}
	messages, _ := body["messages"].([]any)
	if first, _ := messages[0].(map[string]any); first["role"] != "system" {
		t.Fatalf("expected the developer message sent as system, got %v", messages[0])
	}
	if req.Input[0].Role != schemas.ChatMessageRoleDeveloper {
		t.Fatalf("expected the caller's message left as developer, got %q", req.Input[0].Role)
	}
}

// TestBasetenResponsesEffortClamp pins that /v1/responses on Model APIs gets an
// effort inside the minimal..high range its schema allows, while chat keeps the wider
// ladder (TestBasetenReasoningEffortLadder).
func TestBasetenResponsesEffortClamp(t *testing.T) {
	t.Parallel()
	for effort, want := range map[string]string{"none": "minimal", "minimal": "minimal", "medium": "medium", "high": "high", "xhigh": "high", "max": "high"} {
		t.Run(effort, func(t *testing.T) {
			t.Parallel()
			server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[]}`)
			})
			provider := newProvider(t, server.URL+"/v1")
			req := &schemas.BifrostResponsesRequest{
				Provider: schemas.Baseten,
				Model:    "baseten-test/unlisted-model",
				Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hello")},
				Params:   &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr(effort)}},
			}
			if _, err := provider.Responses(contextWithDeployment(""), testKey, req); err != nil {
				t.Fatalf("Responses: %v", llmtests.GetErrorMessage(err))
			}
			reasoning, _ := seen()[0].Body["reasoning"].(map[string]any)
			if reasoning["effort"] != want {
				t.Fatalf("expected reasoning.effort %q on the wire, got %v", want, reasoning["effort"])
			}
		})
	}
}

func TestBasetenEmbeddingRequiresDeployment(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"m","usage":{"prompt_tokens":2,"total_tokens":2}}`)
	})
	provider := newProvider(t, server.URL+"/v1")
	req := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Baseten,
		Model:    "not-required",
		Input:    []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hello")}}}},
	}

	if _, err := provider.Embedding(contextWithDeployment(""), testKey, req); err == nil {
		t.Fatal("expected embeddings without a deployment_url to fail")
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream call without a deployment, got %d", n)
	}

	resp, err := provider.Embedding(contextWithDeployment(server.URL+"/environments/production/sync/v1"), testKey, req)
	if err != nil {
		t.Fatalf("Embedding: %v", llmtests.GetErrorMessage(err))
	}
	if got := seen(); len(got) != 1 || got[0].Path != "/environments/production/sync/v1/embeddings" {
		t.Fatalf("expected one call to the deployment embeddings route, got %+v", got)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected one embedding, got %d", len(resp.Data))
	}
}

// TestBasetenImageGenerationRequiresDeployment pins that image generation only goes to
// an alias deployment's OpenAI-compatible /images/generations route, with the alias
// model_id on the wire, and never reaches Model APIs.
func TestBasetenImageGenerationRequiresDeployment(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"created":1,"data":[{"b64_json":"aW1n"}]}`)
	})
	provider := newProvider(t, server.URL+"/v1")
	req := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.Baseten,
		Model:    "not-required",
		Input:    &schemas.ImageGenerationInput{Prompt: "a lighthouse at dusk"},
		Params:   &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024"), ResponseFormat: schemas.Ptr("b64_json")},
	}

	if _, err := provider.ImageGeneration(contextWithDeployment(""), testKey, req); err == nil {
		t.Fatal("expected image generation without a deployment_url to fail")
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream call without a deployment, got %d", n)
	}

	resp, err := provider.ImageGeneration(contextWithDeployment(server.URL+"/environments/production/sync/v1"), testKey, req)
	if err != nil {
		t.Fatalf("ImageGeneration: %v", llmtests.GetErrorMessage(err))
	}
	got := seen()
	if len(got) != 1 || got[0].Path != "/environments/production/sync/v1/images/generations" {
		t.Fatalf("expected one call to the deployment images route, got %+v", got)
	}
	if got[0].Auth != "Bearer bt-test-key" || got[0].Body["model"] != "not-required" || got[0].Body["prompt"] != "a lighthouse at dusk" || got[0].Body["size"] != "1024x1024" {
		t.Fatalf("unexpected upstream request: %+v", got[0])
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aW1n" {
		t.Fatalf("expected one base64 image, got %+v", resp.Data)
	}
}

// TestBasetenUnsupportedFileAndPassthrough pins what the doc comments on these methods claim:
// each returns an unsupported-operation error without calling upstream.
func TestBasetenUnsupportedFileAndPassthrough(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	provider := newProvider(t, server.URL+"/v1")
	ctx := contextWithDeployment("")
	keys := []schemas.Key{testKey}

	_, listErr := provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{})
	_, retrieveErr := provider.FileRetrieve(ctx, keys, &schemas.BifrostFileRetrieveRequest{})
	_, deleteErr := provider.FileDelete(ctx, keys, &schemas.BifrostFileDeleteRequest{})
	_, streamErr := provider.PassthroughStream(ctx, nil, nil, testKey, &schemas.BifrostPassthroughRequest{})
	for name, err := range map[string]*schemas.BifrostError{
		"FileList": listErr, "FileRetrieve": retrieveErr, "FileDelete": deleteErr, "PassthroughStream": streamErr,
	} {
		if err == nil || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
			t.Errorf("%s: expected an unsupported_operation error, got %+v", name, err)
		}
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream call, got %d", n)
	}
}

// TestBasetenImageGenerationPredict pins that a deployment_url copied from the
// dashboard as /predict is called as given with OpenAI's image body and Bearer auth,
// and that both reply shapes come back as images: a FLUX.1 Truss's {"data": base64}
// and the OpenAI {"data": [{"b64_json"}]} of recipes that map /predict to
// /v1/images/generations.
func TestBasetenImageGenerationPredict(t *testing.T) {
	t.Parallel()
	req := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.Baseten,
		Model:    "flux-dev",
		Input:    &schemas.ImageGenerationInput{Prompt: "a lighthouse at dusk"},
		Params:   &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024"), Seed: schemas.Ptr(7), NumInferenceSteps: schemas.Ptr(20)},
	}
	for name, tc := range map[string]struct {
		reply string
		want  []string
	}{
		"truss base64":  {reply: `{"data":"aW1n"}`, want: []string{"aW1n"}},
		"openai images": {reply: `{"created":5,"data":[{"b64_json":"YQ=="},{"b64_json":"Yg=="}]}`, want: []string{"YQ==", "Yg=="}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.reply)
			})
			provider := newProvider(t, server.URL+"/v1")
			resp, err := provider.ImageGeneration(contextWithDeployment(server.URL+"/environments/production/predict/"), testKey, req)
			if err != nil {
				t.Fatalf("ImageGeneration: %v", llmtests.GetErrorMessage(err))
			}
			got := seen()
			if len(got) != 1 || got[0].Path != "/environments/production/predict" {
				t.Fatalf("expected one call to the /predict URL as given, got %+v", got)
			}
			body := got[0].Body
			if got[0].Auth != "Bearer bt-test-key" || body["prompt"] != "a lighthouse at dusk" || body["size"] != "1024x1024" || body["seed"] != float64(7) || body["num_inference_steps"] != float64(20) {
				t.Fatalf("unexpected upstream request: %+v", got[0])
			}
			if len(resp.Data) != len(tc.want) {
				t.Fatalf("expected %d images, got %+v", len(tc.want), resp.Data)
			}
			for i, b64 := range tc.want {
				if resp.Data[i].B64JSON != b64 || resp.Data[i].Index != i {
					t.Fatalf("image %d: expected %q at index %d, got %+v", i, b64, i, resp.Data[i])
				}
			}
		})
	}
}

// TestBasetenImageGenerationPredictRejects pins that a /predict reply without an
// image fails rather than returning an empty list, and that other operations refuse
// a /predict URL before any call.
func TestBasetenImageGenerationPredictRejects(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"output":"not an image"}`)
	})
	provider := newProvider(t, server.URL+"/v1")
	predictURL := server.URL + "/environments/production/predict"
	req := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.Baseten,
		Model:    "flux-dev",
		Input:    &schemas.ImageGenerationInput{Prompt: "a lighthouse"},
	}

	if _, err := provider.ImageGeneration(contextWithDeployment(predictURL), testKey, req); err == nil || !strings.Contains(llmtests.GetErrorMessage(err), "no image") {
		t.Fatalf("expected a missing-image error, got %+v", err)
	}
	if _, err := provider.ChatCompletion(contextWithDeployment(predictURL), testKey, chatRequest("flux-dev")); err == nil || !strings.Contains(llmtests.GetErrorMessage(err), "/predict") {
		t.Fatalf("expected chat against a /predict URL to fail naming it, got %+v", err)
	}
	if n := len(seen()); n != 1 {
		t.Fatalf("expected only the image call upstream, got %d", n)
	}
}

// TestBasetenImageGenerationDefaultsToBase64 pins that a request without
// response_format asks the deployment for b64_json on both URL forms, since Baseten's
// image servers reject OpenAI's url default, and that the caller's request is not
// modified.
func TestBasetenImageGenerationDefaultsToBase64(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"created":1,"data":[{"b64_json":"aW1n"}]}`)
	})
	provider := newProvider(t, server.URL+"/v1")
	req := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.Baseten,
		Model:    "flux-2",
		Input:    &schemas.ImageGenerationInput{Prompt: "dog playing"},
		Params:   &schemas.ImageGenerationParameters{Size: schemas.Ptr("512x512")},
	}
	for _, u := range []string{server.URL + "/environments/production/sync/v1", server.URL + "/environments/production/predict"} {
		if _, err := provider.ImageGeneration(contextWithDeployment(u), testKey, req); err != nil {
			t.Fatalf("ImageGeneration(%s): %v", u, llmtests.GetErrorMessage(err))
		}
	}
	got := seen()
	if len(got) != 2 {
		t.Fatalf("expected two upstream calls, got %+v", got)
	}
	for _, call := range got {
		if call.Body["response_format"] != "b64_json" || call.Body["size"] != "512x512" {
			t.Fatalf("expected response_format b64_json and the caller's size on %s, got %+v", call.Path, call.Body)
		}
	}
	if req.Params.ResponseFormat != nil {
		t.Fatalf("expected the caller's request to stay unmodified, got response_format %q", *req.Params.ResponseFormat)
	}
}

// TestBasetenImageGenerationErrorDetail pins that a deployment error carried in
// error.detail or a top-level detail reaches the client as the message.
func TestBasetenImageGenerationErrorDetail(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]string{
		"error.detail": `{"error":{"detail":"response_format=url is not supported"}}`,
		"detail":       `{"detail":"response_format=url is not supported"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, _ := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, reply)
			})
			provider := newProvider(t, server.URL+"/v1")
			req := &schemas.BifrostImageGenerationRequest{
				Provider: schemas.Baseten,
				Model:    "flux-2",
				Input:    &schemas.ImageGenerationInput{Prompt: "dog playing"},
				Params:   &schemas.ImageGenerationParameters{ResponseFormat: schemas.Ptr("url")},
			}
			_, err := provider.ImageGeneration(contextWithDeployment(server.URL+"/environments/production/sync/v1"), testKey, req)
			if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest || err.Error.Message != "response_format=url is not supported" {
				t.Fatalf("expected a 400 carrying the deployment's detail, got %+v", err)
			}
		})
	}
}

// TestBasetenRerankBEI pins BEI rerank: the alias /sync/v1 deployment_url is called
// on /sync/rerank with the query and document texts, results come back highest score
// first, top_n trims them and return_documents attaches the request documents.
func TestBasetenRerankBEI(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"index":1,"score":0.2},{"index":2,"score":0.9},{"index":0,"score":0.5}]`)
	})
	provider := newProvider(t, server.URL+"/v1")
	req := &schemas.BifrostRerankRequest{
		Provider:  schemas.Baseten,
		Model:     "qwen3-reranker",
		Query:     "what causes tides",
		Documents: []schemas.RerankDocument{{Text: "a"}, {Text: "b"}, {Text: "c", ID: schemas.Ptr("doc-c")}},
		Params:    &schemas.RerankParameters{TopN: schemas.Ptr(2), ReturnDocuments: schemas.Ptr(true)},
	}

	if _, err := provider.Rerank(contextWithDeployment(""), testKey, req); err == nil {
		t.Fatal("expected rerank without a deployment_url to fail")
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream call without a deployment, got %d", n)
	}

	resp, err := provider.Rerank(contextWithDeployment(server.URL+"/environments/production/sync/v1"), testKey, req)
	if err != nil {
		t.Fatalf("Rerank: %v", llmtests.GetErrorMessage(err))
	}
	got := seen()
	if len(got) != 1 || got[0].Path != "/environments/production/sync/rerank" || got[0].Auth != "Bearer bt-test-key" {
		t.Fatalf("expected one Bearer call to /sync/rerank, got %+v", got)
	}
	if got[0].Body["query"] != "what causes tides" || fmt.Sprint(got[0].Body["texts"]) != "[a b c]" {
		t.Fatalf("unexpected rerank body: %+v", got[0].Body)
	}
	for _, field := range []string{"model", "documents", "top_n", "return_documents"} {
		if _, ok := got[0].Body[field]; ok {
			t.Fatalf("expected %q to stay off the BEI body, got %+v", field, got[0].Body)
		}
	}
	if len(resp.Results) != 2 || resp.Model != "qwen3-reranker" {
		t.Fatalf("expected two results for qwen3-reranker, got %+v", resp)
	}
	first, second := resp.Results[0], resp.Results[1]
	if first.Index != 2 || first.RelevanceScore != 0.9 || first.ID == nil || *first.ID != "doc-c" || first.Document == nil || first.Document.Text != "c" {
		t.Fatalf("expected doc-c first with its document, got %+v", first)
	}
	if second.Index != 0 || second.RelevanceScore != 0.5 || second.Document == nil || second.Document.Text != "a" {
		t.Fatalf("expected document 0 second, got %+v", second)
	}
}

// TestBasetenRerankRejects pins that an empty query or document list fails as a 400
// before any call, that an out-of-range index from BEI fails rather than panicking,
// and that BEI's {"error": "..."} body reaches the client as the message.
func TestBasetenRerankRejects(t *testing.T) {
	t.Parallel()
	reply := `[{"index":5,"score":0.9}]`
	status := http.StatusOK
	var mu sync.Mutex
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, reply)
	})
	provider := newProvider(t, server.URL+"/v1")
	deployment := server.URL + "/environments/production/sync/v1"
	request := func(query string, docs ...string) *schemas.BifrostRerankRequest {
		documents := make([]schemas.RerankDocument, len(docs))
		for i, d := range docs {
			documents[i] = schemas.RerankDocument{Text: d}
		}
		return &schemas.BifrostRerankRequest{Provider: schemas.Baseten, Model: "reranker", Query: query, Documents: documents}
	}

	for name, req := range map[string]*schemas.BifrostRerankRequest{
		"empty query":  request(" ", "a"),
		"no documents": request("q"),
	} {
		_, err := provider.Rerank(contextWithDeployment(deployment), testKey, req)
		if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: expected a 400, got %+v", name, err)
		}
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream call for rejected requests, got %d", n)
	}

	if _, err := provider.Rerank(contextWithDeployment(deployment), testKey, request("q", "a")); err == nil || !strings.Contains(llmtests.GetErrorMessage(err), "outside the documents") {
		t.Fatalf("expected an out-of-range index error, got %+v", err)
	}

	mu.Lock()
	reply, status = `{"error":"input exceeds max batch size","error_type":"Validation"}`, http.StatusUnprocessableEntity
	mu.Unlock()
	_, err := provider.Rerank(contextWithDeployment(deployment), testKey, request("q", "a"))
	if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusUnprocessableEntity || err.Error.Message != "input exceeds max batch size" {
		t.Fatalf("expected BEI's 422 message, got %+v", err)
	}
}

// basetenDecisionQuestions asks one question of each System One type.
func basetenDecisionQuestions() []schemas.DecisionQuestion {
	return []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("refund"), Instructions: schemas.NewDecisionText("Is the customer asking for a refund?")},
		{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("route"), Choices: []schemas.DecisionChoice{
			{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("Payments and refunds")},
			{Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}, Description: schemas.NewDecisionText("Software defects")},
		}},
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Levels: []schemas.DecisionLevel{
			{Label: "0", Description: schemas.NewDecisionText("Routine")},
			{Label: "1", Description: schemas.NewDecisionText("Urgent")},
		}},
	}
}

const basetenDecisionBody = `{"model":"joey","answers":{` +
	`"refund":{"type":"noul","noul":0.91},` +
	`"route":{"type":"choice","choice":"billing","probabilities":{"billing":0.8,"bug":0.2},"confidence":0.4},` +
	`"urgency":{"type":"score","score":0.7,"probabilities":{"0":0.3,"1":0.7},"legend":{"0":"Routine","1":"Urgent"}}},` +
	`"usage":{"input_tokens":40,"output_tokens":3}}`

// TestBasetenDecisionSystemOne pins the System One wire shape: text state as a string,
// questions keyed by name, answers mapped back in question order.
func TestBasetenDecisionSystemOne(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, basetenDecisionBody)
	})
	provider := newProvider(t, server.URL+"/v1")
	resp, err := provider.Decision(contextWithDeployment(""), testKey, &schemas.BifrostDecisionRequest{
		Provider:  schemas.Baseten,
		Model:     "decision-models/baseten-joey-2b-latest",
		Input:     schemas.DecisionInput{Text: schemas.Ptr("I was charged twice, please refund me.")},
		Questions: basetenDecisionQuestions(),
	})
	if err != nil {
		t.Fatalf("Decision: %v", llmtests.GetErrorMessage(err))
	}
	got := seen()
	if len(got) != 1 || got[0].Path != "/v1/systemone" || got[0].Auth != "Bearer bt-test-key" {
		t.Fatalf("expected one authorized call to /v1/systemone, got %+v", got)
	}
	body := got[0].Body
	if body["model"] != "decision-models/baseten-joey-2b-latest" || body["state"] != "I was charged twice, please refund me." {
		t.Fatalf("unexpected model or state: %+v", body)
	}
	questions, _ := body["questions"].(map[string]any)
	route, _ := questions["route"].(map[string]any)
	urgency, _ := questions["urgency"].(map[string]any)
	refund, _ := questions["refund"].(map[string]any)
	if refund["type"] != "noul" || route["type"] != "choice" || urgency["type"] != "score" {
		t.Fatalf("unexpected question types: %+v", questions)
	}
	if criteria, _ := route["criteria"].(map[string]any); criteria["billing"] != "Payments and refunds" {
		t.Fatalf("choice criteria not keyed by option: %+v", route)
	}
	if levels, _ := urgency["criteria"].([]any); len(levels) != 2 {
		t.Fatalf("score criteria not an ordered array: %+v", urgency)
	}

	if len(resp.Answers) != 3 {
		t.Fatalf("expected 3 answers, got %+v", resp.Answers)
	}
	if a := resp.Answers[0]; a.Type != schemas.DecisionTypePredicate || a.Probability == nil || *a.Probability != 0.91 {
		t.Fatalf("unexpected predicate answer: %+v", a)
	}
	if a := resp.Answers[1]; a.Type != schemas.DecisionTypeChoice || a.Choice == nil || a.Choice.Str == nil || *a.Choice.Str != "billing" {
		t.Fatalf("unexpected choice answer: %+v", a)
	}
	if a := resp.Answers[2]; a.Type != schemas.DecisionTypeScore || a.Score == nil || *a.Score != 0.7 {
		t.Fatalf("unexpected score answer: %+v", a)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 40 || resp.Usage.CompletionTokens != 3 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

// TestBasetenDecisionConversationState pins that a message input is wrapped as
// {"messages": [...]} (Baseten renders a top-level array as text), with text and
// inline images as System One content parts.
func TestBasetenDecisionConversationState(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, basetenDecisionBody)
	})
	provider := newProvider(t, server.URL+"/v1")
	_, err := provider.Decision(contextWithDeployment(""), testKey, &schemas.BifrostDecisionRequest{
		Provider: schemas.Baseten,
		Model:    "decision-models/baseten-joey-2b-latest",
		Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("The order arrived damaged.")}},
			{Role: "assistant", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
				{Type: schemas.DecisionInputPartTypeOutputText, Text: schemas.NewDecisionText("Replacement or refund?")},
			}}},
			{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
				{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("A refund. Photo attached.")},
				{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,iVBORw0KGgo=")},
			}}},
		}},
		Questions: basetenDecisionQuestions(),
	})
	if err != nil {
		t.Fatalf("Decision: %v", llmtests.GetErrorMessage(err))
	}
	got := seen()
	if len(got) != 1 {
		t.Fatalf("expected one upstream call, got %d", len(got))
	}
	state, ok := got[0].Body["state"].(map[string]any)
	if !ok {
		t.Fatalf("expected state to be a {messages} object, got %#v", got[0].Body["state"])
	}
	want := []any{
		map[string]any{"role": "user", "content": "The order arrived damaged."},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Replacement or refund?"}}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "A refund. Photo attached."},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}},
	}
	if !reflect.DeepEqual(state["messages"], want) {
		t.Fatalf("unexpected conversation state:\n got %#v\nwant %#v", state["messages"], want)
	}
}

// TestBasetenDecisionRejectsUnreadableInput pins that inputs System One has no shape
// for fail before any upstream call instead of being sent as text.
func TestBasetenDecisionRejectsUnreadableInput(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, basetenDecisionBody)
	})
	provider := newProvider(t, server.URL+"/v1")
	cases := []struct {
		name     string
		messages []schemas.DecisionInputMessage
		wantErr  string
	}{
		{"tool output", []schemas.DecisionInputMessage{{Type: schemas.Ptr(schemas.DecisionInputItemTypeFunctionCallOutput), CallID: schemas.Ptr("c1")}}, "send only user and assistant messages"},
		{"image by reference", []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("img1"), Data: schemas.DecisionData{File: []byte{0x89, 'P', 'N', 'G'}}},
		}}}}, "image sent by reference"},
		{"audio part", []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{{Type: "input_audio"}}}}}, `an "input_audio" part`},
		{"system role", []schemas.DecisionInputMessage{{Role: "system", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Be strict.")}}}, `has role "system"`},
		{"developer role", []schemas.DecisionInputMessage{{Role: "developer", Content: schemas.DecisionInputContent{Text: schemas.Ptr("Be strict.")}}}, `has role "developer"`},
		{"assistant image", []schemas.DecisionInputMessage{{Role: "assistant", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,iVBORw0KGgo=")},
		}}}}, "images only on user messages"},
		{"17 images", []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: slices.Repeat([]schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,iVBORw0KGgo=")},
		}, 17)}}}, "at most 16 per request"},
	}
	for _, tc := range cases {
		_, err := provider.Decision(contextWithDeployment(""), testKey, &schemas.BifrostDecisionRequest{
			Provider:  schemas.Baseten,
			Model:     "decision-models/baseten-joey-2b-latest",
			Input:     schemas.DecisionInput{Messages: tc.messages},
			Questions: basetenDecisionQuestions(),
		})
		if err == nil || err.Error == nil {
			t.Errorf("%s: expected an error, got %+v", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error.Message, tc.wantErr) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err.Error.Message, tc.wantErr)
		}
		if err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: want status 400, got %v", tc.name, err.StatusCode)
		}
		if err.Error.Type == nil || *err.Error.Type != "invalid_request_error" || err.ExtraFields.ErrorType != schemas.ErrorTypeCallerInvalidRequest {
			t.Errorf("%s: want an invalid_request_error from the caller, got type %v, class %q", tc.name, err.Error.Type, err.ExtraFields.ErrorType)
		}
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("expected no upstream calls, got %d", n)
	}
}

// TestBasetenDecisionImageDetailAndDefaultRole pins that a message without a role is
// sent as user, and that an image detail other than "auto", which System One rejects,
// is dropped while "auto" is kept.
func TestBasetenDecisionImageDetailAndDefaultRole(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, basetenDecisionBody)
	})
	provider := newProvider(t, server.URL+"/v1")
	_, err := provider.Decision(contextWithDeployment(""), testKey, &schemas.BifrostDecisionRequest{
		Provider: schemas.Baseten,
		Model:    "decision-models/baseten-joey-2b-latest",
		Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			{Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
				{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA"), Detail: schemas.Ptr("high")},
				{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,BBBB"), Detail: schemas.Ptr("auto")},
			}}},
		}},
		Questions: basetenDecisionQuestions(),
	})
	if err != nil {
		t.Fatalf("Decision: %v", llmtests.GetErrorMessage(err))
	}
	state, _ := seen()[0].Body["state"].(map[string]any)
	want := []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,BBBB", "detail": "auto"}},
	}}}
	if !reflect.DeepEqual(state["messages"], want) {
		t.Fatalf("unexpected conversation state:\n got %#v\nwant %#v", state["messages"], want)
	}
}

// TestBasetenDecisionError pins that System One's string-valued error body surfaces
// with its message, type and status.
func TestBasetenDecisionError(t *testing.T) {
	t.Parallel()
	server, _ := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"Question type must be choice, noul or score","error_type":"Validation"}`)
	})
	provider := newProvider(t, server.URL+"/v1")
	_, err := provider.Decision(contextWithDeployment(""), testKey, &schemas.BifrostDecisionRequest{
		Provider:  schemas.Baseten,
		Model:     "decision-models/baseten-joey-2b-latest",
		Input:     schemas.DecisionInput{Text: schemas.Ptr("x")},
		Questions: basetenDecisionQuestions(),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %v", err.StatusCode)
	}
	if err.Error == nil || err.Error.Message != "Question type must be choice, noul or score" || err.Error.Type == nil || *err.Error.Type != "Validation" {
		t.Fatalf("unexpected error: %+v", err.Error)
	}
}

func TestBasetenResponsesRouting(t *testing.T) {
	t.Parallel()
	server, seen := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"m","output":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, chatCompletionBody)
	})
	provider := newProvider(t, server.URL+"/v1")
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Baseten,
		Model:    "openai/gpt-oss-120b",
		Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hello")},
	}

	if _, err := provider.Responses(contextWithDeployment(""), testKey, req); err != nil {
		t.Fatalf("Responses on Model APIs: %v", llmtests.GetErrorMessage(err))
	}
	if _, err := provider.Responses(contextWithDeployment(server.URL+"/environments/production/sync/v1"), testKey, req); err != nil {
		t.Fatalf("Responses on a deployment: %v", llmtests.GetErrorMessage(err))
	}
	got := seen()
	if len(got) != 2 || got[0].Path != "/v1/responses" || got[1].Path != "/environments/production/sync/v1/chat/completions" {
		t.Fatalf("expected native /v1/responses then deployment chat completions, got %+v", got)
	}
}

// TestBasetenStreamContinuousUsage pins that Baseten's per-chunk usage (stream_options
// include_usage and continuous_usage_stats default to true) reaches the client once,
// as the last cumulative value, so nothing downstream sums partial counts.
func TestBasetenStreamContinuousUsage(t *testing.T) {
	t.Parallel()
	server, _ := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(content, finish string, completion int) string {
			finishJSON := "null"
			if finish != "" {
				finishJSON = `"` + finish + `"`
			}
			return fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":%q},"finish_reason":%s}],"usage":{"prompt_tokens":3,"completion_tokens":%d,"total_tokens":%d}}`+"\n\n", content, finishJSON, completion, 3+completion)
		}
		_, _ = fmt.Fprint(w, chunk("a", "", 1))
		_, _ = fmt.Fprint(w, chunk("b", "", 2))
		_, _ = fmt.Fprint(w, chunk("c", "stop", 3))
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	})
	provider := newProvider(t, server.URL+"/v1")
	postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}

	stream, err := provider.ChatCompletionStream(contextWithDeployment(""), postHookRunner, nil, testKey, chatRequest("openai/gpt-oss-120b"))
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", llmtests.GetErrorMessage(err))
	}
	var text strings.Builder
	var usages []*schemas.BifrostLLMUsage
	for chunk := range stream {
		if chunk == nil || chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
				text.WriteString(*choice.Delta.Content)
			}
		}
		if chunk.BifrostChatResponse.Usage != nil {
			usages = append(usages, chunk.BifrostChatResponse.Usage)
		}
	}
	if text.String() != "abc" {
		t.Fatalf("expected streamed text abc, got %q", text.String())
	}
	if len(usages) != 1 {
		t.Fatalf("expected usage on exactly one chunk, got %d", len(usages))
	}
	if usages[0].CompletionTokens != 3 || usages[0].TotalTokens != 6 {
		t.Fatalf("expected the final cumulative usage completion=3 total=6, got %+v", usages[0])
	}
}

// TestBasetenReasoningEffortLadder pins that the levels Model APIs documents
// (minimal through max) reach the wire unclamped when no datasheet row exists.
func TestBasetenReasoningEffortLadder(t *testing.T) {
	t.Parallel()
	for _, effort := range []string{"minimal", "low", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			t.Parallel()
			server, seen := newStub(t, writeChat)
			provider := newProvider(t, server.URL+"/v1")
			req := chatRequest("baseten-test/unlisted-model")
			req.Params = &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(effort)}}
			if _, err := provider.ChatCompletion(contextWithDeployment(""), testKey, req); err != nil {
				t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
			}
			if got := seen()[0].Body["reasoning_effort"]; got != effort {
				t.Fatalf("expected reasoning_effort %q on the wire, got %v", effort, got)
			}
		})
	}
}

// BenchmarkBasetenChatCompletion measures a unary chat call against a local stub, with
// and without a dedicated-deployment alias, so the cost of the alias lookup is visible
// next to the Model APIs path.
func BenchmarkBasetenChatCompletion(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(writeChat))
	defer server.Close()
	provider, err := baseten.NewBasetenProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1", DefaultRequestTimeoutInSeconds: 10},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct{ name, deployment string }{
		{"model_apis", ""},
		{"deployment_alias", server.URL + "/environments/production/sync/v1"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := provider.ChatCompletion(contextWithDeployment(tc.deployment), testKey, chatRequest("openai/gpt-oss-120b")); err != nil {
					b.Fatal(llmtests.GetErrorMessage(err))
				}
			}
		})
	}
}
