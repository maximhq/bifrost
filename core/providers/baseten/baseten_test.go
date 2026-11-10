package baseten_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
