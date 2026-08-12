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
			ToolCalls:             false, // Nadir's request schema has no `tools` field
			ToolCallsStreaming:    false, // no function calling, see ToolCalls
			MultipleToolCalls:     false, // no function calling, see ToolCalls
			End2EndToolCalling:    false, // no function calling, see ToolCalls
			AutomaticFunctionCall: false, // no function calling, see ToolCalls
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
