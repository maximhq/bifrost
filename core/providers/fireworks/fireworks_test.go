package fireworks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	fireworksprovider "github.com/maximhq/bifrost/core/providers/fireworks"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestFireworks(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("FIREWORKS_API_KEY")) == "" {
		t.Skip("Skipping Fireworks tests because FIREWORKS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	chatModel, textModel, embeddingModel := resolveFireworksModels(t, client, ctx)

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:                schemas.Fireworks,
		ChatModel:               chatModel,
		Fallbacks:               []schemas.Fallback{},
		TextModel:               textModel,
		TextCompletionFallbacks: []schemas.Fallback{},
		EmbeddingModel:          embeddingModel,
		ReasoningModel:          "",
		TranscriptionModel:      "",
		SpeechSynthesisModel:    "",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        textModel != "",
			TextCompletionStream:  textModel != "",
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			FileBase64:            false,
			FileURL:               false,
			CompleteEnd2End:       true,
			Embedding:             embeddingModel != "",
			ListModels:            true,
			Reasoning:             false,
			Transcription:         false,
			SpeechSynthesis:       false,
			PromptCaching:         false,
		},
	}
	t.Run("FireworksTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// resolveFireworksModels discovers live Fireworks models for chat, completions, and embeddings.
func resolveFireworksModels(t *testing.T, client *bifrost.Bifrost, ctx context.Context) (string, string, string) {
	t.Helper()

	requestedChatModel := normalizeFireworksModelID(os.Getenv("FIREWORKS_CHAT_MODEL"))
	requestedTextModel := normalizeFireworksModelID(os.Getenv("FIREWORKS_TEXT_MODEL"))
	requestedEmbeddingModel := normalizeFireworksModelID(os.Getenv("FIREWORKS_EMBEDDING_MODEL"))

	chatModel := ""
	textModel := ""
	embeddingModel := ""

	pageToken := ""
	for page := 0; page < 5; page++ {
		req := &schemas.BifrostListModelsRequest{
			Provider:  schemas.Fireworks,
			PageSize:  200,
			PageToken: pageToken,
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		resp, bifrostErr := client.ListModelsRequest(bfCtx, req)
		if bifrostErr != nil {
			t.Fatalf("Failed to list Fireworks models for test discovery: %v", llmtests.GetErrorMessage(bifrostErr))
		}

		if chatModel == "" {
			chatModel = pickFireworksChatModel(resp.Data)
		}
		if textModel == "" {
			textModel = pickFireworksChatModel(resp.Data)
		}
		if embeddingModel == "" {
			embeddingModel = pickFireworksEmbeddingModel(resp.Data)
		}

		if chatModel != "" && textModel != "" && embeddingModel != "" {
			break
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	chatModel = selectFireworksModel(t, "chat", requestedChatModel, chatModel)
	textModel = selectFireworksModel(t, "text", requestedTextModel, textModel)
	embeddingModel = selectFireworksModel(t, "embedding", requestedEmbeddingModel, embeddingModel)

	if chatModel == "" {
		t.Fatal("Unable to discover a Fireworks chat model from /v1/models; set FIREWORKS_CHAT_MODEL to override")
	}
	if textModel != "" && !fireworksModelSupportsTextCompletions(t, client, ctx, textModel) {
		t.Logf("Skipping Fireworks text completion scenarios because model %q did not accept /v1/completions", textModel)
		textModel = ""
	}
	if embeddingModel != "" && !fireworksModelSupportsEmbeddings(t, client, ctx, embeddingModel) {
		t.Logf("Skipping Fireworks embedding scenario because model %q did not accept /v1/embeddings", embeddingModel)
		embeddingModel = ""
	}
	if textModel == "" {
		t.Log("No Fireworks completions-capable model discovered from /v1/models; text completion scenarios will be skipped unless FIREWORKS_TEXT_MODEL is set")
	}
	if embeddingModel == "" {
		t.Log("No Fireworks embedding model discovered from /v1/models; embedding scenario will be skipped unless FIREWORKS_EMBEDDING_MODEL is set")
	}

	return chatModel, textModel, embeddingModel
}

// selectFireworksModel prefers an explicit env override and otherwise falls back to a discovered model.
func selectFireworksModel(t *testing.T, modelType, requestedModel, discoveredModel string) string {
	t.Helper()

	if requestedModel != "" {
		t.Logf("Using FIREWORKS_%s_MODEL=%q override", strings.ToUpper(modelType), requestedModel)
		return requestedModel
	}

	return discoveredModel
}

// fireworksModelSupportsTextCompletions validates that the selected model actually accepts Fireworks /v1/completions.
func fireworksModelSupportsTextCompletions(t *testing.T, client *bifrost.Bifrost, ctx context.Context, model string) bool {
	t.Helper()

	prompt := "Say ok"
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	resp, bifrostErr := client.TextCompletionRequest(bfCtx, &schemas.BifrostTextCompletionRequest{
		Provider: schemas.Fireworks,
		Model:    model,
		Input: &schemas.TextCompletionInput{
			PromptStr: &prompt,
		},
		Params: &schemas.TextCompletionParameters{
			MaxTokens: schemas.Ptr(8),
		},
	})
	if bifrostErr != nil {
		t.Logf("Fireworks /v1/completions probe failed for %q: %v", model, llmtests.GetErrorMessage(bifrostErr))
		return false
	}

	return resp != nil && len(resp.Choices) > 0
}

// fireworksModelSupportsEmbeddings validates that the selected model actually accepts Fireworks /v1/embeddings.
func fireworksModelSupportsEmbeddings(t *testing.T, client *bifrost.Bifrost, ctx context.Context, model string) bool {
	t.Helper()

	text := "embedding probe"
	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	resp, bifrostErr := client.EmbeddingRequest(bfCtx, &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Fireworks,
		Model:    model,
		Input: &schemas.EmbeddingInput{
			Text: &text,
		},
	})
	if bifrostErr != nil {
		t.Logf("Fireworks /v1/embeddings probe failed for %q: %v", model, llmtests.GetErrorMessage(bifrostErr))
		return false
	}

	return resp != nil && len(resp.Data) > 0
}

// pickFireworksChatModel selects a text-capable Fireworks model from ListModels output.
func pickFireworksChatModel(models []schemas.Model) string {
	for _, model := range models {
		normalized := normalizeFireworksModelID(model.ID)
		if isFireworksTextCapable(normalized) {
			return normalized
		}
	}
	return ""
}

// pickFireworksEmbeddingModel selects an embedding-capable Fireworks model from ListModels output.
func pickFireworksEmbeddingModel(models []schemas.Model) string {
	for _, model := range models {
		normalized := normalizeFireworksModelID(model.ID)
		lower := strings.ToLower(normalized)
		if strings.Contains(lower, "embedding") || strings.Contains(lower, "embed") {
			return normalized
		}
	}
	return ""
}

// normalizeFireworksModelID strips any provider prefix so tests can pass raw Fireworks model IDs to Bifrost requests.
func normalizeFireworksModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	_, normalized := schemas.ParseModelString(modelID, schemas.Fireworks)
	return normalized
}

// isFireworksTextCapable applies a conservative name-based heuristic for text/chat-capable Fireworks models.
func isFireworksTextCapable(modelID string) bool {
	lower := strings.ToLower(modelID)
	excludedHints := []string{
		"flux",
		"whisper",
		"audio",
		"speech",
		"transcrib",
		"embedding",
		"embed",
		"rerank",
	}
	for _, hint := range excludedHints {
		if strings.Contains(lower, hint) {
			return false
		}
	}

	preferredHints := []string{
		"instruct",
		"chat",
		"gpt-oss",
		"deepseek",
		"qwen",
		"llama",
		"glm",
		"mixtral",
		"mistral",
		"cogito",
		"gemma",
	}
	for _, hint := range preferredHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}

	return false
}

// TestFireworksProviderUsesNativeEndpoints verifies that the Fireworks provider targets native completions, responses, and embeddings endpoints.
func TestFireworksProviderUsesNativeEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		expectedPath string
		run          func(t *testing.T, provider *fireworksprovider.FireworksProvider, ctx *schemas.BifrostContext, key schemas.Key)
	}{
		{
			name:         "TextCompletion",
			expectedPath: "/v1/completions",
			run: func(t *testing.T, provider *fireworksprovider.FireworksProvider, ctx *schemas.BifrostContext, key schemas.Key) {
				prompt := "A is for apple and B is for"
				resp, err := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{
					Provider: schemas.Fireworks,
					Model:    "accounts/fireworks/models/deepseek-v3p2",
					Input: &schemas.TextCompletionInput{
						PromptStr: &prompt,
					},
				})
				if err != nil {
					t.Fatalf("TextCompletion returned error: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Text == nil || *resp.Choices[0].Text != " banana" {
					t.Fatalf("unexpected text completion response: %#v", resp)
				}
			},
		},
		{
			name:         "Responses",
			expectedPath: "/v1/responses",
			run: func(t *testing.T, provider *fireworksprovider.FireworksProvider, ctx *schemas.BifrostContext, key schemas.Key) {
				resp, err := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
					Provider: schemas.Fireworks,
					Model:    "accounts/fireworks/models/deepseek-v3p2",
					Input: []schemas.ResponsesMessage{
						llmtests.CreateBasicResponsesMessage("hello"),
					},
					Params: &schemas.ResponsesParameters{
						PreviousResponseID: schemas.Ptr("resp_previous"),
						MaxToolCalls:       schemas.Ptr(2),
						Store:              schemas.Ptr(true),
					},
				})
				if err != nil {
					t.Fatalf("Responses returned error: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || resp.PreviousResponseID == nil || *resp.PreviousResponseID != "resp_previous" {
					t.Fatalf("unexpected responses response: %#v", resp)
				}
			},
		},
		{
			name:         "Embedding",
			expectedPath: "/v1/embeddings",
			run: func(t *testing.T, provider *fireworksprovider.FireworksProvider, ctx *schemas.BifrostContext, key schemas.Key) {
				resp, err := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{
					Provider: schemas.Fireworks,
					Model:    "accounts/fireworks/models/nomic-embed-text-v1.5",
					Input: &schemas.EmbeddingInput{
						Text: schemas.Ptr("embedding test"),
					},
				})
				if err != nil {
					t.Fatalf("Embedding returned error: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Data) != 1 || len(resp.Data[0].Embedding.EmbeddingArray) != 3 {
					t.Fatalf("unexpected embedding response: %#v", resp)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestedPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/completions":
					_, _ = fmt.Fprint(w, `{"id":"cmpl_1","object":"text_completion","created":1,"model":"accounts/fireworks/models/deepseek-v3p2","choices":[{"text":" banana","index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`)
				case "/v1/responses":
					_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"accounts/fireworks/models/deepseek-v3p2","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[],"logprobs":[]}]}],"previous_response_id":"resp_previous","max_tool_calls":2,"store":true,"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0,"cached_read_tokens":0,"cached_write_tokens":0},"output_tokens":1,"total_tokens":2}}`)
				case "/v1/embeddings":
					_, _ = fmt.Fprint(w, `{"object":"list","model":"accounts/fireworks/models/nomic-embed-text-v1.5","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider := newTestFireworksProvider(t, server.URL)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

			tt.run(t, provider, ctx, key)

			if requestedPath != tt.expectedPath {
				t.Fatalf("expected request path %q, got %q", tt.expectedPath, requestedPath)
			}
		})
	}
}

// TestFireworksResponsesStreamUsesNativeResponsesEndpoint verifies that Fireworks responses streaming targets the native responses endpoint.
func TestFireworksResponsesStreamUsesNativeResponsesEndpoint(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"accounts/fireworks/models/deepseek-v3p2\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[],\"logprobs\":[]}]}],\"usage\":{\"input_tokens\":1,\"input_tokens_details\":{\"cached_tokens\":0,\"cached_read_tokens\":0,\"cached_write_tokens\":0},\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	provider := newTestFireworksProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}
	postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}

	stream, err := provider.ResponsesStream(ctx, postHookRunner, key, &schemas.BifrostResponsesRequest{
		Provider: schemas.Fireworks,
		Model:    "accounts/fireworks/models/deepseek-v3p2",
		Input: []schemas.ResponsesMessage{
			llmtests.CreateBasicResponsesMessage("hello"),
		},
	})
	if err != nil {
		t.Fatalf("ResponsesStream returned error: %v", llmtests.GetErrorMessage(err))
	}

	sawCompleted := false
	for chunk := range stream {
		if chunk != nil && chunk.BifrostResponsesStreamResponse != nil &&
			chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			sawCompleted = true
		}
	}

	if requestedPath != "/v1/responses" {
		t.Fatalf("expected responses stream to hit /v1/responses, got %q", requestedPath)
	}
	if !sawCompleted {
		t.Fatal("expected a completed responses stream chunk")
	}
}

// newTestFireworksProvider creates a Fireworks provider configured to hit a local test server.
func newTestFireworksProvider(t *testing.T, baseURL string) *fireworksprovider.FireworksProvider {
	t.Helper()

	provider, err := fireworksprovider.NewFireworksProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("failed to create Fireworks provider: %v", err)
	}
	return provider
}
