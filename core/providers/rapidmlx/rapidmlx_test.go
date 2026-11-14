package rapidmlx_test

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
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/rapidmlx"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestRapidmlx matches make test-core's provider-name convention.
func TestRapidmlx(t *testing.T) {
	if os.Getenv("RAPID_MLX_BASE_URL") == "" {
		t.Skip("RAPID_MLX_BASE_URL is not set")
	}
	client, ctx, cancel, err := llmtests.SetupTest()
	require.NoError(t, err)
	defer cancel()
	defer client.Shutdown()
	config := llmtests.ComprehensiveTestConfig{
		Provider: schemas.RapidMLX, ChatModel: "mlx-community/Qwen3.5-4B-MLX-4bit",
		Scenarios: llmtests.TestScenarios{SimpleChat: true, CompletionStream: true, ListModels: true},
	}
	t.Run("RapidmlxTests", func(t *testing.T) { llmtests.RunAllComprehensiveTests(t, client, ctx, config) })
}

const rapidmlxChatResponseBody = `{
	"id": "chatcmpl-test",
	"object": "chat.completion",
	"created": 1234567890,
	"model": "mlx-community/Qwen3.5-4B-MLX-4bit",
	"choices": [{
		"index": 0,
		"message": {"role": "assistant", "content": "Hello!"},
		"finish_reason": "stop"
	}],
	"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
}`

func newTestRapidMLXProvider(t *testing.T, baseURL string) *rapidmlx.RapidMLXProvider {
	t.Helper()
	provider, err := rapidmlx.NewRapidMLXProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func rapidmlxUserChatRequest(provider schemas.ModelProvider, extra map[string]interface{}) *schemas.BifrostChatRequest {
	hello := "Hello"
	req := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    "mlx-community/Qwen3.5-4B-MLX-4bit",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &hello},
			},
		},
		Params: &schemas.ChatParameters{ExtraParams: extra},
	}
	if extra == nil {
		req.Params = &schemas.ChatParameters{}
	}
	return req
}

func TestRapidMLXGetProviderKey(t *testing.T) {
	t.Parallel()
	provider := newTestRapidMLXProvider(t, "")
	require.Equal(t, schemas.RapidMLX, provider.GetProviderKey())
}

func TestNewRapidMLXProvider_TrimsTrailingSlash(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, rapidmlxChatResponseBody)
	}))
	defer server.Close()

	provider := newTestRapidMLXProvider(t, server.URL+"/v1/")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, rapidmlxUserChatRequest(schemas.RapidMLX, nil))
	require.Nil(t, bifrostErr)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/chat/completions", path)
	require.Len(t, resp.Choices, 1)
}

func TestRapidMLXChatAuthentication(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, rapidmlxChatResponseBody)
	}))
	defer server.Close()
	provider := newTestRapidMLXProvider(t, server.URL+"/v1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.ChatCompletion(ctx, schemas.Key{}, rapidmlxUserChatRequest(schemas.RapidMLX, nil))
	require.Nil(t, err)
	_, err = provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("rapidmlx-token")}, rapidmlxUserChatRequest(schemas.RapidMLX, nil))
	require.Nil(t, err)

	mu.Lock()
	auths := gotAuth
	mu.Unlock()
	require.Len(t, auths, 2)
	require.Empty(t, auths[0], "a keyless Rapid-MLX must not get an Authorization header")
	require.Equal(t, "Bearer rapidmlx-token", auths[1])
}

func TestRapidMLXChatCompletionStream_ParsesSSE(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1,"model":"mlx-community/Qwen3.5-4B-MLX-4bit","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1,"model":"mlx-community/Qwen3.5-4B-MLX-4bit","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestRapidMLXProvider(t, server.URL+"/v1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := rapidmlxUserChatRequest(schemas.RapidMLX, nil)

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, schemas.Key{}, req)
	require.Nil(t, bifrostErr)
	require.NotNil(t, stream)

	var texts []string
	timeout := time.After(10 * time.Second)
streamLoop:
	for {
		var chunk *schemas.BifrostStreamChunk
		select {
		case <-timeout:
			t.Fatal("stream did not finish")
		case next, ok := <-stream:
			if !ok {
				break streamLoop
			}
			chunk = next
		}
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			t.Fatalf("stream error: %v", chunk.BifrostError.Error.Message)
		}
		if chunk.BifrostChatResponse != nil {
			for _, choice := range chunk.BifrostChatResponse.Choices {
				if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
					texts = append(texts, *choice.Delta.Content)
				}
			}
		}
	}
	require.Equal(t, "Hello!", strings.Join(texts, ""))

	mu.Lock()
	body := string(gotBody)
	mu.Unlock()
	require.Contains(t, body, `"model":"mlx-community/Qwen3.5-4B-MLX-4bit"`)
	require.Contains(t, body, `"stream":true`)
}

func TestRapidMLXEmbedding(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","model":"test-embedding","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
	}))
	defer server.Close()

	provider := newTestRapidMLXProvider(t, server.URL+"/v1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	text := "hello"
	resp, bifrostErr := provider.Embedding(ctx, schemas.Key{}, &schemas.BifrostEmbeddingRequest{
		Provider: schemas.RapidMLX,
		Model:    "test-embedding",
		Input:    []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
	})
	require.Nil(t, bifrostErr)
	require.Len(t, resp.Data, 1)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/embeddings", path)
}

func TestRapidMLXListModels_QueriesEachKeysURL(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	modelServer := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model","created":1727740800,"owned_by":"rapidmlx"}]}`, id)
		}))
	}
	serverA := modelServer("mlx-community/Qwen3.5-4B-MLX-4bit")
	defer serverA.Close()
	serverB := modelServer("test-model")
	defer serverB.Close()

	// Provider-level URL must not be used when every key carries its own.
	provider := newTestRapidMLXProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keys := []schemas.Key{
		{Models: []string{"*"}, RapidMLXKeyConfig: &schemas.RapidMLXKeyConfig{URL: *schemas.NewSecretVar(serverA.URL + "/v1")}},
		{Models: []string{"*"}, RapidMLXKeyConfig: &schemas.RapidMLXKeyConfig{URL: *schemas.NewSecretVar(serverB.URL + "/v1")}},
	}
	resp, bifrostErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{
		Provider: schemas.RapidMLX,
	})
	require.Nil(t, bifrostErr)
	require.Len(t, resp.Data, 2)

	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	require.Contains(t, ids, "rapidmlx/mlx-community/Qwen3.5-4B-MLX-4bit")
	require.Contains(t, ids, "rapidmlx/test-model")

	mu.Lock()
	got := paths
	mu.Unlock()
	require.ElementsMatch(t, []string{"/v1/models", "/v1/models"}, got)
}

func noopPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func TestRapidMLXResponses_UsesNativeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Empty(t, r.Header.Get("Authorization"))
		var body struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "mlx-community/Qwen3.5-4B-MLX-4bit", body.Model)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp-test","object":"response","created_at":1,"model":"mlx-community/Qwen3.5-4B-MLX-4bit","status":"completed","output":[{"id":"msg-test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello!","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`)
	}))
	defer server.Close()
	provider := newTestRapidMLXProvider(t, server.URL+"/v1")
	resp, err := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{}, &schemas.BifrostResponsesRequest{
		Provider: schemas.RapidMLX, Model: "mlx-community/Qwen3.5-4B-MLX-4bit",
		Input: []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Hello")}}},
	})
	require.Nil(t, err)
	require.Equal(t, schemas.Ptr("resp-test"), resp.ID)
	require.NotEmpty(t, resp.Output)
	require.Equal(t, 8, resp.Usage.TotalTokens)
}

func TestRapidMLXURLRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, rapidmlxChatResponseBody)
	}))
	defer server.Close()
	provider := newTestRapidMLXProvider(t, "http://127.0.0.1:1/v1")
	resp, err := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{
		RapidMLXKeyConfig: &schemas.RapidMLXKeyConfig{URL: *schemas.NewSecretVar(server.URL + "/v1/")},
	}, rapidmlxUserChatRequest(schemas.RapidMLX, nil))
	require.Nil(t, err)
	require.Equal(t, "Hello!", *resp.Choices[0].Message.Content.ContentStr)
}

// This benchmark includes loopback HTTP; it compares the new wrapper with the existing OpenAI provider.
func BenchmarkRapidMLXChatCompletion(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, rapidmlxChatResponseBody)
	}))
	defer server.Close()
	config := func() *schemas.ProviderConfig {
		return &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1", AllowPrivateNetwork: true}}
	}
	provider, err := rapidmlx.NewRapidMLXProvider(config(), bifrost.NewNoOpLogger())
	if err != nil {
		b.Fatal(err)
	}
	baseline := openai.NewOpenAIProvider(config(), bifrost.NewNoOpLogger())
	for name, target := range map[string]schemas.Provider{"OpenAIReference": baseline, "RapidMLX": provider} {
		b.Run(name, func(b *testing.B) {
			request := rapidmlxUserChatRequest(target.GetProviderKey(), nil)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, e := target.ChatCompletion(ctx, schemas.Key{}, request); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// --- unsupported surface ----------------------------------------------------

// Unsupported integration operations fail locally without dialing a backend.
func TestRapidMLXUnsupportedOperations(t *testing.T) {
	t.Parallel()
	provider := newTestRapidMLXProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{}
	keys := []schemas.Key{{}}
	finalizer := func(context.Context) {}

	cases := []struct {
		name string
		call func() *schemas.BifrostError
	}{
		{"TextCompletion", func() *schemas.BifrostError {
			_, e := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{})
			return e
		}},
		{"ModelRetrieve", func() *schemas.BifrostError {
			_, e := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{})
			return e
		}},
		{"Decision", func() *schemas.BifrostError {
			_, e := provider.Decision(ctx, key, &schemas.BifrostDecisionRequest{})
			return e
		}},
		{"TextCompletionStream", func() *schemas.BifrostError {
			_, e := provider.TextCompletionStream(ctx, noopPostHookRunner, finalizer, key, &schemas.BifrostTextCompletionRequest{})
			return e
		}},
		{"Rerank", func() *schemas.BifrostError {
			_, e := provider.Rerank(ctx, key, &schemas.BifrostRerankRequest{})
			return e
		}},
		{"OCR", func() *schemas.BifrostError { _, e := provider.OCR(ctx, key, &schemas.BifrostOCRRequest{}); return e }},
		{"SpeechStream", func() *schemas.BifrostError {
			_, e := provider.SpeechStream(ctx, noopPostHookRunner, finalizer, key, &schemas.BifrostSpeechRequest{})
			return e
		}},
		{"TranscriptionStream", func() *schemas.BifrostError {
			_, e := provider.TranscriptionStream(ctx, noopPostHookRunner, finalizer, key, &schemas.BifrostTranscriptionRequest{})
			return e
		}},
		{"ImageGeneration", func() *schemas.BifrostError {
			_, e := provider.ImageGeneration(ctx, key, &schemas.BifrostImageGenerationRequest{})
			return e
		}},
		{"ImageGenerationStream", func() *schemas.BifrostError {
			_, e := provider.ImageGenerationStream(ctx, noopPostHookRunner, finalizer, key, &schemas.BifrostImageGenerationRequest{})
			return e
		}},
		{"ImageEdit", func() *schemas.BifrostError {
			_, e := provider.ImageEdit(ctx, key, &schemas.BifrostImageEditRequest{})
			return e
		}},
		{"ImageEditStream", func() *schemas.BifrostError {
			_, e := provider.ImageEditStream(ctx, noopPostHookRunner, finalizer, key, &schemas.BifrostImageEditRequest{})
			return e
		}},
		{"ImageVariation", func() *schemas.BifrostError {
			_, e := provider.ImageVariation(ctx, key, &schemas.BifrostImageVariationRequest{})
			return e
		}},
		{"VideoGeneration", func() *schemas.BifrostError {
			_, e := provider.VideoGeneration(ctx, key, &schemas.BifrostVideoGenerationRequest{})
			return e
		}},
		{"VideoRetrieve", func() *schemas.BifrostError {
			_, e := provider.VideoRetrieve(ctx, key, &schemas.BifrostVideoRetrieveRequest{})
			return e
		}},
		{"VideoDownload", func() *schemas.BifrostError {
			_, e := provider.VideoDownload(ctx, key, &schemas.BifrostVideoDownloadRequest{})
			return e
		}},
		{"VideoDelete", func() *schemas.BifrostError {
			_, e := provider.VideoDelete(ctx, key, &schemas.BifrostVideoDeleteRequest{})
			return e
		}},
		{"VideoList", func() *schemas.BifrostError {
			_, e := provider.VideoList(ctx, key, &schemas.BifrostVideoListRequest{})
			return e
		}},
		{"VideoEdit", func() *schemas.BifrostError {
			_, e := provider.VideoEdit(ctx, key, &schemas.BifrostVideoEditRequest{})
			return e
		}},
		{"VideoRemix", func() *schemas.BifrostError {
			_, e := provider.VideoRemix(ctx, key, &schemas.BifrostVideoRemixRequest{})
			return e
		}},
		{"BatchCreate", func() *schemas.BifrostError {
			_, e := provider.BatchCreate(ctx, key, &schemas.BifrostBatchCreateRequest{})
			return e
		}},
		{"BatchList", func() *schemas.BifrostError {
			_, e := provider.BatchList(ctx, keys, &schemas.BifrostBatchListRequest{})
			return e
		}},
		{"BatchRetrieve", func() *schemas.BifrostError {
			_, e := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{})
			return e
		}},
		{"BatchCancel", func() *schemas.BifrostError {
			_, e := provider.BatchCancel(ctx, keys, &schemas.BifrostBatchCancelRequest{})
			return e
		}},
		{"BatchDelete", func() *schemas.BifrostError {
			_, e := provider.BatchDelete(ctx, keys, &schemas.BifrostBatchDeleteRequest{})
			return e
		}},
		{"BatchResults", func() *schemas.BifrostError {
			_, e := provider.BatchResults(ctx, keys, &schemas.BifrostBatchResultsRequest{})
			return e
		}},
		{"FileUpload", func() *schemas.BifrostError {
			_, e := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{})
			return e
		}},
		{"FileList", func() *schemas.BifrostError {
			_, e := provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{})
			return e
		}},
		{"FileRetrieve", func() *schemas.BifrostError {
			_, e := provider.FileRetrieve(ctx, keys, &schemas.BifrostFileRetrieveRequest{})
			return e
		}},
		{"FileDelete", func() *schemas.BifrostError {
			_, e := provider.FileDelete(ctx, keys, &schemas.BifrostFileDeleteRequest{})
			return e
		}},
		{"FileContent", func() *schemas.BifrostError {
			_, e := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{})
			return e
		}},
		{"CountTokens", func() *schemas.BifrostError {
			_, e := provider.CountTokens(ctx, key, &schemas.BifrostResponsesRequest{})
			return e
		}},
		{"Compaction", func() *schemas.BifrostError {
			_, e := provider.Compaction(ctx, key, &schemas.BifrostCompactionRequest{})
			return e
		}},
		{"ContainerCreate", func() *schemas.BifrostError {
			_, e := provider.ContainerCreate(ctx, key, &schemas.BifrostContainerCreateRequest{})
			return e
		}},
		{"ContainerList", func() *schemas.BifrostError {
			_, e := provider.ContainerList(ctx, keys, &schemas.BifrostContainerListRequest{})
			return e
		}},
		{"ContainerRetrieve", func() *schemas.BifrostError {
			_, e := provider.ContainerRetrieve(ctx, keys, &schemas.BifrostContainerRetrieveRequest{})
			return e
		}},
		{"ContainerDelete", func() *schemas.BifrostError {
			_, e := provider.ContainerDelete(ctx, keys, &schemas.BifrostContainerDeleteRequest{})
			return e
		}},
		{"ContainerFileCreate", func() *schemas.BifrostError {
			_, e := provider.ContainerFileCreate(ctx, key, &schemas.BifrostContainerFileCreateRequest{})
			return e
		}},
		{"ContainerFileList", func() *schemas.BifrostError {
			_, e := provider.ContainerFileList(ctx, keys, &schemas.BifrostContainerFileListRequest{})
			return e
		}},
		{"ContainerFileRetrieve", func() *schemas.BifrostError {
			_, e := provider.ContainerFileRetrieve(ctx, keys, &schemas.BifrostContainerFileRetrieveRequest{})
			return e
		}},
		{"ContainerFileContent", func() *schemas.BifrostError {
			_, e := provider.ContainerFileContent(ctx, keys, &schemas.BifrostContainerFileContentRequest{})
			return e
		}},
		{"ContainerFileDelete", func() *schemas.BifrostError {
			_, e := provider.ContainerFileDelete(ctx, keys, &schemas.BifrostContainerFileDeleteRequest{})
			return e
		}},
		{"Passthrough", func() *schemas.BifrostError {
			_, e := provider.Passthrough(ctx, key, &schemas.BifrostPassthroughRequest{})
			return e
		}},
		{"PassthroughStream", func() *schemas.BifrostError {
			_, e := provider.PassthroughStream(ctx, noopPostHookRunner, finalizer, key, &schemas.BifrostPassthroughRequest{})
			return e
		}},
		{"CachedContentCreate", func() *schemas.BifrostError {
			_, e := provider.CachedContentCreate(ctx, key, &schemas.BifrostCachedContentCreateRequest{})
			return e
		}},
		{"CachedContentList", func() *schemas.BifrostError {
			_, e := provider.CachedContentList(ctx, keys, &schemas.BifrostCachedContentListRequest{})
			return e
		}},
		{"CachedContentRetrieve", func() *schemas.BifrostError {
			_, e := provider.CachedContentRetrieve(ctx, keys, &schemas.BifrostCachedContentRetrieveRequest{})
			return e
		}},
		{"CachedContentUpdate", func() *schemas.BifrostError {
			_, e := provider.CachedContentUpdate(ctx, keys, &schemas.BifrostCachedContentUpdateRequest{})
			return e
		}},
		{"CachedContentDelete", func() *schemas.BifrostError {
			_, e := provider.CachedContentDelete(ctx, keys, &schemas.BifrostCachedContentDeleteRequest{})
			return e
		}},
	}

	for _, tc := range cases {
		bifrostErr := tc.call()
		require.NotNil(t, bifrostErr, tc.name)
		require.Equal(t, schemas.Ptr("unsupported_operation"), bifrostErr.Error.Code, tc.name)
		require.Contains(t, bifrostErr.Error.Message, "is not supported by rapidmlx provider", tc.name)
		require.Equal(t, schemas.RapidMLX, bifrostErr.ExtraFields.Provider, tc.name)
	}
}
