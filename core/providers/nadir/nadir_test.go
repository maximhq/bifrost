package nadir_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/nadir"

	"github.com/maximhq/bifrost/core/schemas"
)

// Compile-time check that NadirProvider satisfies the full Provider interface.
var _ schemas.Provider = (*nadir.NadirProvider)(nil)

func TestNadir(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("NADIR_API_KEY")) == "" {
		t.Skip("Skipping Nadir tests because NADIR_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// "auto" is Nadir's routing model: the upstream picks the model per request.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Nadir,
		ChatModel:      "auto",
		TextModel:      "", // Nadir doesn't support text completion
		EmbeddingModel: "", // Nadir doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			// Nadir's request schema carries `tools`, `tool_choice` and
			// `parallel_tool_calls`, assistant turns may have null content beside
			// `tool_calls`, and `role: tool` messages carry `tool_call_id`. The
			// streaming path forwards `delta.tool_calls` and ends the turn with
			// `finish_reason: "tool_calls"`.
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true, // parallel_tool_calls is forwarded
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Not verified against a live key yet
			ImageBase64:           false, // Not verified against a live key yet
			MultipleImages:        false, // Not verified against a live key yet
			CompleteEnd2End:       true,
			Embedding:             false, // Not supported
			ListModels:            true,
		},
	}

	t.Run("NadirTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func TestNadirProviderDefaults(t *testing.T) {
	t.Parallel()

	t.Run("default base URL", func(t *testing.T) {
		config := &schemas.ProviderConfig{}
		provider, err := nadir.NewNadirProvider(config, bifrost.NewDefaultLogger(schemas.LogLevelError))
		if err != nil {
			t.Fatalf("NewNadirProvider failed: %v", err)
		}
		if provider.GetProviderKey() != schemas.Nadir {
			t.Errorf("provider key: got %s, want %s", provider.GetProviderKey(), schemas.Nadir)
		}
		if config.NetworkConfig.BaseURL != "https://api.getnadir.com" {
			t.Errorf("base URL: got %q, want %q", config.NetworkConfig.BaseURL, "https://api.getnadir.com")
		}
	})

	t.Run("configured base URL keeps no trailing slash", func(t *testing.T) {
		config := &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: "https://nadir.internal.example.com/"},
		}
		if _, err := nadir.NewNadirProvider(config, bifrost.NewDefaultLogger(schemas.LogLevelError)); err != nil {
			t.Fatalf("NewNadirProvider failed: %v", err)
		}
		if config.NetworkConfig.BaseURL != "https://nadir.internal.example.com" {
			t.Errorf("base URL: got %q, want the trailing slash trimmed", config.NetworkConfig.BaseURL)
		}
	})
}

// TestNadirChatCompletionAutoRouting pins the two things that make Nadir a router
// rather than a plain OpenAI-compatible endpoint, against a stub upstream:
//
//  1. "auto" reaches the upstream verbatim on POST /v1/chat/completions with a
//     Bearer key. Rewriting it to a concrete model would disable routing.
//  2. The model the upstream reports back (the model Nadir actually routed to)
//     survives into the Bifrost response instead of being overwritten with the
//     requested "auto", so cost and log attribution name the real model.
func TestNadirChatCompletionAutoRouting(t *testing.T) {
	t.Parallel()

	const routedModel = "claude-haiku-4-5"

	var mu sync.Mutex
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-nadir-1",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "` + routedModel + `",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10}
		}`))
	}))
	defer server.Close()

	provider, err := nadir.NewNadirProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewNadirProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Nadir,
		Model:    "auto",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Summarize this changelog in one line.")},
		}},
	}

	response, bifrostErr := provider.ChatCompletion(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bifrostErr)
	}

	mu.Lock()
	defer mu.Unlock()

	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want %q", gotPath, "/v1/chat/completions")
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer test-key")
	}

	var wire struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("unmarshal outbound body: %v; got: %s", err, gotBody)
	}
	if wire.Model != "auto" {
		t.Errorf("outbound model: got %q, want %q (rewriting it would disable Nadir's routing); body: %s", wire.Model, "auto", gotBody)
	}

	if response.Model != routedModel {
		t.Errorf("response model: got %q, want %q (the routed model must not be replaced by the requested \"auto\")", response.Model, routedModel)
	}
}

// TestNadirResponsesStreamUsesChatCompletions covers the Responses streaming
// path the docs advertise. Nadir exposes no native Responses endpoint, so the
// request is converted and must still leave on /v1/chat/completions with
// stream enabled; sending it anywhere else would 404 against the real API.
func TestNadirResponsesStreamUsesChatCompletions(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotAuth string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"claude-haiku-4-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	provider, err := nadir.NewNadirProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewNadirProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Nadir,
		Model:    "auto",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Stream a one line summary.")},
		}},
	}

	noopHook := func(_ *schemas.BifrostContext, res *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return res, err
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, noopHook, func(context.Context) {}, key, request)
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned an error: %v", bifrostErr)
	}
	for range stream { // drain so the request completes
	}

	mu.Lock()
	defer mu.Unlock()

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want %q (Nadir has no native Responses endpoint)", gotPath, "/v1/chat/completions")
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer test-key")
	}

	var wire struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("unmarshal outbound body: %v; got: %s", err, gotBody)
	}
	if wire.Model != "auto" {
		t.Errorf("outbound model: got %q, want %q; body: %s", wire.Model, "auto", gotBody)
	}
	if !wire.Stream {
		t.Errorf("outbound stream flag: got false, want true; body: %s", gotBody)
	}
}

// unsupportedOp represents an operation that NadirProvider should reject locally.
type unsupportedOp struct {
	name        string
	requestType schemas.RequestType
	invoke      func(p *nadir.NadirProvider) *schemas.BifrostError
}

// TestNadirUnsupportedOperations verifies that operations the upstream Nadir API
// does not expose fail locally with the right request type and provider key,
// rather than being sent upstream.
func TestNadirUnsupportedOperations(t *testing.T) {
	t.Parallel()

	cases := []unsupportedOp{
		{name: "TextCompletion", requestType: schemas.TextCompletionRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.TextCompletion(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "TextCompletionStream", requestType: schemas.TextCompletionStreamRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.TextCompletionStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Embedding", requestType: schemas.EmbeddingRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.Embedding(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Rerank", requestType: schemas.RerankRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.Rerank(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Speech", requestType: schemas.SpeechRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.Speech(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Transcription", requestType: schemas.TranscriptionRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.Transcription(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageGeneration", requestType: schemas.ImageGenerationRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.ImageGeneration(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CountTokens", requestType: schemas.CountTokensRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.CountTokens(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "BatchCreate", requestType: schemas.BatchCreateRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.BatchCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "FileUpload", requestType: schemas.FileUploadRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.FileUpload(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CachedContentCreate", requestType: schemas.CachedContentCreateRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.CachedContentCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Passthrough", requestType: schemas.PassthroughRequest, invoke: func(p *nadir.NadirProvider) *schemas.BifrostError {
			_, err := p.Passthrough(nil, schemas.Key{}, nil)
			return err
		}},
	}

	provider, err := nadir.NewNadirProvider(&schemas.ProviderConfig{}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewNadirProvider: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bifrostErr := tc.invoke(provider)
			if bifrostErr == nil {
				t.Fatalf("%s: got nil error, want an unsupported-operation error", tc.name)
			}
			if bifrostErr.ExtraFields.RequestType != tc.requestType {
				t.Errorf("%s request type: got %q, want %q", tc.name, bifrostErr.ExtraFields.RequestType, tc.requestType)
			}
			if bifrostErr.ExtraFields.Provider != schemas.Nadir {
				t.Errorf("%s provider: got %q, want %q", tc.name, bifrostErr.ExtraFields.Provider, schemas.Nadir)
			}
		})
	}
}

// complexityTierRequest is the decision the complexity router sends: one choice
// question, one choice per tier, the user turn as input.
func complexityTierRequest() *schemas.BifrostDecisionRequest {
	choices := make([]schemas.DecisionChoice, 0, 3)
	for _, tier := range []string{"SIMPLE", "MEDIUM", "COMPLEX"} {
		choices = append(choices, schemas.DecisionChoice{Value: schemas.DecisionScalar{Str: schemas.Ptr(tier)}})
	}
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Nadir,
		Model:    "auto",
		Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
			Role:    "user",
			Content: schemas.DecisionInputContent{Text: schemas.Ptr("Design a sharded rate limiter with exact global quotas.")},
		}}},
		Questions: []schemas.DecisionQuestion{{
			Type:    schemas.DecisionTypeChoice,
			Name:    schemas.Ptr("complexity_tier"),
			Choices: choices,
		}},
	}
}

// newDecisionServer answers every request with status and body, recording the
// request it received.
func newDecisionServer(t *testing.T, status int, body string) (*nadir.NadirProvider, func() (method, path, auth string, sent []byte)) {
	t.Helper()
	var mu sync.Mutex
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod, gotPath, gotAuth, gotBody = r.Method, r.URL.Path, r.Header.Get("Authorization"), sent
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	provider, err := nadir.NewNadirProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 10},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewNadirProvider: %v", err)
	}
	return provider, func() (string, string, string, []byte) {
		mu.Lock()
		defer mu.Unlock()
		return gotMethod, gotPath, gotAuth, gotBody
	}
}

// TestNadirDecisionComplexityTier covers Nadir as the complexity router's
// decision model: the request leaves on /v1/decisions in OpenAI's shape and
// Nadir's tier and confidence come back as the choice answer.
func TestNadirDecisionComplexityTier(t *testing.T) {
	t.Parallel()

	// The body Nadir's /v1/decisions returns.
	provider, received := newDecisionServer(t, http.StatusOK, `{
		"model": "auto",
		"answers": [{"type": "choice", "name": "complexity_tier", "choice": "COMPLEX", "confidence": 0.81}],
		"usage": {"input_tokens": 14, "output_tokens": 0, "total_tokens": 14}
	}`)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}

	response, bifrostErr := provider.Decision(ctx, key, complexityTierRequest())
	if bifrostErr != nil {
		t.Fatalf("Decision returned an error: %v", bifrostErr.Error.Message)
	}

	method, path, auth, sent := received()
	if method != http.MethodPost || path != "/v1/decisions" {
		t.Errorf("request: got %s %s, want POST /v1/decisions", method, path)
	}
	if auth != "Bearer test-key" {
		t.Errorf("Authorization: got %q, want %q", auth, "Bearer test-key")
	}

	var wire struct {
		Model string `json:"model"`
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
		Questions []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Choices []struct {
				Value string `json:"value"`
			} `json:"choices"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(sent, &wire); err != nil {
		t.Fatalf("unmarshal outbound body: %v; got: %s", err, sent)
	}
	if wire.Model != "auto" || len(wire.Input) != 1 || wire.Input[0].Role != "user" || wire.Input[0].Content == "" {
		t.Errorf("outbound model/input not in OpenAI's Decisions shape: %s", sent)
	}
	if len(wire.Questions) != 1 || wire.Questions[0].Type != "choice" || wire.Questions[0].Name != "complexity_tier" || len(wire.Questions[0].Choices) != 3 || wire.Questions[0].Choices[2].Value != "COMPLEX" {
		t.Errorf("outbound question not the complexity tier choice: %s", sent)
	}

	if len(response.Answers) != 1 {
		t.Fatalf("answers: got %d, want 1", len(response.Answers))
	}
	answer := response.Answers[0]
	if answer.Type != schemas.DecisionTypeChoice || answer.Choice == nil {
		t.Fatalf("answer: got %+v, want a choice", answer)
	}
	if tier, _ := answer.Choice.Key(); tier != "COMPLEX" {
		t.Errorf("tier: got %q, want COMPLEX", tier)
	}
	if answer.Confidence == nil || *answer.Confidence != 0.81 {
		t.Errorf("confidence: got %v, want 0.81", answer.Confidence)
	}
	if response.Usage == nil || response.Usage.TotalTokens != 14 {
		t.Errorf("usage: got %+v, want 14 total tokens", response.Usage)
	}
}

// TestNadirDecisionUnsupportedQuestion covers a question Nadir's classifier
// does not answer: Nadir's 400 reaches the caller with its status and message.
func TestNadirDecisionUnsupportedQuestion(t *testing.T) {
	t.Parallel()

	const message = "Nadir answers a choice question only, not 'predicate'."
	provider, _ := newDecisionServer(t, http.StatusBadRequest,
		`{"error": {"message": "`+message+`", "type": "invalid_request_error", "param": "questions.0.type", "code": null}}`)

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := complexityTierRequest()
	request.Questions = []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_frustrated")}}

	_, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, request)
	if bifrostErr == nil {
		t.Fatal("Decision: got nil error, want Nadir's 400")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %v, want 400", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != message {
		t.Errorf("message: got %+v, want %q", bifrostErr.Error, message)
	}
}
