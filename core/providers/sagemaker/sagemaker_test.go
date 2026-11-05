package sagemaker_test

import (
	"context"
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
	"github.com/maximhq/bifrost/core/providers/sagemaker"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestSageMaker runs the live scenarios against a real endpoint's /openai/v1 route. Bring one up
// with scripts/terraform/sagemaker (llamacpp on ml.c7i.2xlarge is the cheapest) and export
// SAGEMAKER_ENABLED=1, SAGEMAKER_ENDPOINT_NAME and AWS credentials.
func TestSageMaker(t *testing.T) {
	if os.Getenv("SAGEMAKER_ENABLED") != "1" {
		t.Skip("Skipping SageMaker tests: set SAGEMAKER_ENABLED=1 and SAGEMAKER_ENDPOINT_NAME (requires a live endpoint)")
	}
	endpoint := strings.TrimSpace(os.Getenv("SAGEMAKER_ENDPOINT_NAME"))
	if endpoint == "" {
		t.Skip("Skipping SageMaker tests because SAGEMAKER_ENDPOINT_NAME is not set")
	}
	t.Parallel()

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.SageMaker,
		ChatModel: endpoint,
		Scenarios: llmtests.TestScenarios{
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			ListModels:                 true,

			// SageMaker routes only chat/completions on /openai/v1.
			TextCompletion: false,
			Embedding:      false,
			ImageURL:       false,
			ImageBase64:    false,
			MultipleImages: false,
			Rerank:         false,
			BatchCreate:    false,
			BatchList:      false,
			BatchRetrieve:  false,
			BatchCancel:    false,
			BatchResults:   false,
			FileUpload:     false,
			FileList:       false,
			FileRetrieve:   false,
			FileDelete:     false,
			FileContent:    false,
			FileBatchInput: false,
		},
	}
	t.Run("SageMakerTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// Streams captured from live endpoints on 2026-10-24 (trimmed). vLLM sends SSE ending in
// [DONE]; LMI sends JSON lines with no data: prefix and no [DONE], under the same
// text/event-stream Content-Type that SageMaker sets for every /openai/v1 stream.
const (
	vllmStream = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen3-4b","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen3-4b","choices":[{"index":0,"delta":{"reasoning":"Simple sum."},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen3-4b","choices":[{"index":0,"delta":{"content":"2 + 2 equals 4."},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen3-4b","choices":[{"index":0,"delta":{},"finish_reason":"stop","stop_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"qwen3-4b","choices":[],"usage":{"prompt_tokens":22,"total_tokens":108,"completion_tokens":86,"completion_tokens_details":{"reasoning_tokens":74}}}` + "\n\n" +
		"data: [DONE]\n\n"

	lmiStream = `{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1,"model":"lmi","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"prompt_token_ids":null,"prompt_text":null}` + "\n" +
		`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1,"model":"lmi","choices":[{"index":0,"delta":{"reasoning":"Simple sum."},"logprobs":null,"finish_reason":null,"token_ids":null}]}` + "\n" +
		`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1,"model":"lmi","choices":[{"index":0,"delta":{"content":"2 + 2 equals 4."},"logprobs":null,"finish_reason":null,"token_ids":null}]}` + "\n" +
		`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1,"model":"lmi","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop","stop_reason":null,"token_ids":null}]}` + "\n" +
		`{"id":"chatcmpl-2","object":"chat.completion.chunk","created":1,"model":"lmi","choices":[],"usage":{"prompt_tokens":22,"total_tokens":124,"completion_tokens":102,"completion_tokens_details":{"reasoning_tokens":90}}}` + "\n"

	chatResponseBody = `{"id":"chatcmpl-3","object":"chat.completion","created":1,"model":"qwen3-4b","choices":[{"index":0,"message":{"role":"assistant","content":"2 + 2 equals 4.","reasoning":"Simple sum."},"finish_reason":"stop"}],"usage":{"prompt_tokens":22,"completion_tokens":8,"total_tokens":30}}`

	vllmModelErrorBody = `{"error":{"message":"Received client error (400) from primary with message \"{\"error\":{\"message\":\"max_tokens=100000 cannot be greater than max_model_len=32768.\",\"type\":\"BadRequestError\",\"param\":\"max_tokens\",\"code\":400}}\". See https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logEventViewer:group=/aws/sagemaker/Endpoints/ep in account 123456789012 for more information.","type":"server_error","code":"model_error"}}`
)

// capturedRequest is what the fake SageMaker runtime saw.
type capturedRequest struct {
	path string
	auth string
	body string
}

// fakeRuntime answers every request with the given status, content type and body, flushing line
// by line so streams arrive incrementally, and records the request.
func fakeRuntime(t *testing.T, status int, contentType, body string) (*httptest.Server, func() capturedRequest) {
	t.Helper()
	var (
		mu  sync.Mutex
		got capturedRequest
	)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		mu.Lock()
		got = capturedRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(reqBody)}
		mu.Unlock()

		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		flusher, _ := w.(http.Flusher)
		for _, line := range strings.SplitAfter(body, "\n") {
			if line == "" {
				continue
			}
			if _, err := w.Write([]byte(line)); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	return ts, func() capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func newProvider(t *testing.T) *sagemaker.SageMakerProvider {
	t.Helper()
	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		DefaultRequestTimeoutInSeconds: 5,
		StreamIdleTimeoutInSeconds:     2,
		InsecureSkipVerify:             true,
		AllowPrivateNetwork:            true,
	}}
	config.CheckAndSetDefaults()
	provider, err := sagemaker.NewSageMakerProvider(config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	return provider
}

// testKey points the runtime host at the fake server and carries a pre-minted bearer token.
func testKey(ts *httptest.Server) schemas.Key {
	return schemas.Key{
		Value:  *schemas.NewSecretVar("sagemaker-api-key-test"),
		Models: schemas.WhiteList{"qwen3-ep", "shared-ep/qwen3-ic"},
		SageMakerKeyConfig: &schemas.SageMakerKeyConfig{
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: &schemas.SageMakerEndpoints{Runtime: schemas.NewSecretVar(strings.TrimPrefix(ts.URL, "https://"))},
		},
	}
}

func newContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

func chatRequest(model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.SageMaker,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 2 + 2?")},
		}},
	}
}

func passthroughHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, bErr
}

// collectStream drains a provider stream, failing the test if it does not close in time.
func collectStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return chunks
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for the provider stream to close")
		}
	}
}

func TestChatCompletionWireRequest(t *testing.T) {
	ts, seen := fakeRuntime(t, http.StatusOK, "application/json", chatResponseBody)
	defer ts.Close()
	provider := newProvider(t)

	resp, bErr := provider.ChatCompletion(newContext(t), testKey(ts), chatRequest("qwen3-ep"))
	require.Nil(t, bErr, "%+v", bErr)
	require.Equal(t, "2 + 2 equals 4.", *resp.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)

	got := seen()
	require.Equal(t, "/endpoints/qwen3-ep/openai/v1/chat/completions", got.path)
	require.Equal(t, "Bearer sagemaker-api-key-test", got.auth)
	require.Contains(t, got.body, `"model":""`, "SageMaker routes by URL; vLLM rejects an endpoint name but accepts an empty model")
	require.NotContains(t, got.body, "qwen3-ep", "the endpoint name must never reach the container as the model")
}

// The Go SDK lets callers reuse a request (multi-turn loops append to Input and send it again),
// so sending model "" to the container must not blank the caller's request.Model.
func TestChatDoesNotMutateCallerModel(t *testing.T) {
	ts, _ := fakeRuntime(t, http.StatusOK, "application/json", chatResponseBody)
	defer ts.Close()
	provider := newProvider(t)

	req := chatRequest("qwen3-ep")
	_, bErr := provider.ChatCompletion(newContext(t), testKey(ts), req)
	require.Nil(t, bErr, "%+v", bErr)
	require.Equal(t, "qwen3-ep", req.Model)

	streamTS, _ := fakeRuntime(t, http.StatusOK, "text/event-stream", vllmStream)
	defer streamTS.Close()
	req = chatRequest("qwen3-ep")
	stream, bErr := provider.ChatCompletionStream(newContext(t), passthroughHook, nil, testKey(streamTS), req)
	require.Nil(t, bErr, "%+v", bErr)
	collectStream(t, stream)
	require.Equal(t, "qwen3-ep", req.Model)
}

func TestChatCompletionInferenceComponent(t *testing.T) {
	ts, seen := fakeRuntime(t, http.StatusOK, "application/json", chatResponseBody)
	defer ts.Close()
	provider := newProvider(t)

	_, bErr := provider.ChatCompletion(newContext(t), testKey(ts), chatRequest("shared-ep/qwen3-ic"))
	require.Nil(t, bErr, "%+v", bErr)

	got := seen()
	require.Equal(t, "/endpoints/shared-ep/inference-components/qwen3-ic/openai/v1/chat/completions", got.path)
	require.Contains(t, got.body, `"model":""`)
}

func TestChatCompletionStreamFormats(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantPrompt   int
		wantComplete int
	}{
		{"vllm SSE with [DONE]", vllmStream, 22, 86},
		{"lmi JSON lines without [DONE]", lmiStream, 22, 102},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, seen := fakeRuntime(t, http.StatusOK, "text/event-stream", tc.body)
			defer ts.Close()
			provider := newProvider(t)

			stream, bErr := provider.ChatCompletionStream(newContext(t), passthroughHook, nil, testKey(ts), chatRequest("qwen3-ep"))
			require.Nil(t, bErr, "%+v", bErr)
			chunks := collectStream(t, stream)
			require.Contains(t, seen().body, `"stream":true`)

			var content, reasoning strings.Builder
			for i, chunk := range chunks {
				require.Nil(t, chunk.BifrostError, "chunk %d carried an error: %+v", i, chunk.BifrostError)
				resp := chunk.BifrostChatResponse
				if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].ChatStreamResponseChoice == nil || resp.Choices[0].ChatStreamResponseChoice.Delta == nil {
					continue
				}
				delta := resp.Choices[0].ChatStreamResponseChoice.Delta
				if delta.Content != nil {
					content.WriteString(*delta.Content)
				}
				if delta.Reasoning != nil {
					reasoning.WriteString(*delta.Reasoning)
				}
			}
			require.Equal(t, "2 + 2 equals 4.", content.String())
			require.Equal(t, "Simple sum.", reasoning.String())

			final := chunks[len(chunks)-1].BifrostChatResponse
			require.NotNil(t, final)
			require.NotNil(t, final.Choices[0].FinishReason)
			require.Equal(t, "stop", *final.Choices[0].FinishReason)
			require.NotNil(t, final.Usage, "the usage chunk after finish_reason must be kept")
			require.Equal(t, tc.wantPrompt, final.Usage.PromptTokens)
			require.Equal(t, tc.wantComplete, final.Usage.CompletionTokens)
		})
	}
}

func TestContainerErrorIsUnwrapped(t *testing.T) {
	ts, _ := fakeRuntime(t, http.StatusBadRequest, "application/json", vllmModelErrorBody)
	defer ts.Close()
	provider := newProvider(t)

	_, bErr := provider.ChatCompletion(newContext(t), testKey(ts), chatRequest("qwen3-ep"))
	require.NotNil(t, bErr)
	require.Equal(t, 400, *bErr.StatusCode)
	require.Equal(t, "max_tokens=100000 cannot be greater than max_model_len=32768.", bErr.Error.Message)

	// A failed stream request returns the same JSON body, still labelled text/event-stream.
	streamTS, _ := fakeRuntime(t, http.StatusBadRequest, "text/event-stream", vllmModelErrorBody)
	defer streamTS.Close()
	_, bErr = provider.ChatCompletionStream(newContext(t), passthroughHook, nil, testKey(streamTS), chatRequest("qwen3-ep"))
	require.NotNil(t, bErr)
	require.Equal(t, 400, *bErr.StatusCode)
	require.Equal(t, "max_tokens=100000 cannot be greater than max_model_len=32768.", bErr.Error.Message)
}

func TestResponsesGoesThroughChat(t *testing.T) {
	ts, seen := fakeRuntime(t, http.StatusOK, "application/json", chatResponseBody)
	defer ts.Close()
	provider := newProvider(t)

	resp, bErr := provider.Responses(newContext(t), testKey(ts), &schemas.BifrostResponsesRequest{
		Provider: schemas.SageMaker,
		Model:    "qwen3-ep",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is 2 + 2?")},
		}},
	})
	require.Nil(t, bErr, "%+v", bErr)
	require.NotEmpty(t, resp.Output)
	require.Equal(t, "/endpoints/qwen3-ep/openai/v1/chat/completions", seen().path, "SageMaker routes only chat/completions")
}

func TestUnsupportedAndConfigErrors(t *testing.T) {
	provider := newProvider(t)
	ctx := newContext(t)
	key := schemas.Key{Value: *schemas.NewSecretVar("t"), SageMakerKeyConfig: &schemas.SageMakerKeyConfig{Region: schemas.NewSecretVar("us-east-1")}}

	_, bErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{Provider: schemas.SageMaker, Model: "qwen3-ep"})
	require.NotNil(t, bErr, "SageMaker returns 404 for /openai/v1/embeddings")
	_, bErr = provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{Provider: schemas.SageMaker, Model: "qwen3-ep"})
	require.NotNil(t, bErr, "SageMaker returns 404 for /openai/v1/completions")

	_, bErr = provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("t")}, chatRequest("qwen3-ep"))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "sagemaker_key_config.region is required")

	_, bErr = provider.ChatCompletion(ctx, key, chatRequest("bad/model/id/parts"))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "invalid sagemaker model id")
}

func TestListModelsFromAllowlist(t *testing.T) {
	provider := newProvider(t)
	key := schemas.Key{Models: schemas.WhiteList{"qwen3-ep", "us-west-2/shared-ep/qwen3-ic"}}

	resp, bErr := provider.ListModels(newContext(t), []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.SageMaker})
	require.Nil(t, bErr, "%+v", bErr)
	var ids []string
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	require.ElementsMatch(t, []string{"sagemaker/qwen3-ep", "sagemaker/us-west-2/shared-ep/qwen3-ic"}, ids)
}
