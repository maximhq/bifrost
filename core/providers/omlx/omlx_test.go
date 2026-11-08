package omlx_test

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
	"github.com/maximhq/bifrost/core/providers/omlx"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestOmlx(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OMLX_BASE_URL")) == "" {
		t.Skip("Skipping oMLX tests because OMLX_BASE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Live suite wiring lives in internal/llmtests/account.go (omlx case with
	// OmlxKeyConfig.URL from env.OMLX_BASE_URL). Point OMLX_BASE_URL at a
	// running oMLX instance serving the chat model below to enable this block;
	// the unit tests further down do not need a live server.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Omlx,
		ChatModel:      "Qwen/Qwen3-4B",
		TextModel:      "", // server-dependent; enable per deployment
		EmbeddingModel: "", // server-dependent; enable per deployment
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			FileBase64:                 false,
			FileURL:                    false,
			CompleteEnd2End:            true,
			Embedding:                  false,
			ListModels:                 true,
		},
	}

	t.Run("OmlxTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

const omlxChatResponseBody = `{
	"id": "chatcmpl-test",
	"object": "chat.completion",
	"created": 1234567890,
	"model": "Qwen/Qwen3-4B",
	"choices": [{
		"index": 0,
		"message": {"role": "assistant", "content": "Hello!"},
		"finish_reason": "stop"
	}],
	"usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
}`

func newTestOmlxProvider(t *testing.T, baseURL string) *omlx.OmlxProvider {
	t.Helper()
	provider, err := omlx.NewOmlxProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func omlxUserChatRequest(provider schemas.ModelProvider, extra map[string]interface{}) *schemas.BifrostChatRequest {
	hello := "Hello"
	req := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    "Qwen/Qwen3-4B",
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

// GetProviderKey identifies the provider as omlx, so registrations, keys and
// error payloads all agree on the name.
func TestOmlxGetProviderKey(t *testing.T) {
	t.Parallel()
	provider := newTestOmlxProvider(t, "")
	require.Equal(t, schemas.Omlx, provider.GetProviderKey())
}

// The constructor trims a trailing slash off the provider-level base URL, so
// request paths are joined once: a chat request lands on /v1/chat/completions,
// not //v1/chat/completions.
func TestNewOmlxProvider_TrimsTrailingSlash(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, omlxChatResponseBody)
	}))
	defer server.Close()

	provider := newTestOmlxProvider(t, server.URL+"/")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, omlxUserChatRequest(schemas.Omlx, nil))
	require.Nil(t, bifrostErr)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/chat/completions", path)
	require.Len(t, resp.Choices, 1)
}

// With no key URL and no provider base URL, requests fail locally with a
// configuration error instead of dialing an empty host.
func TestOmlxMissingBaseURLFailsBeforeNetwork(t *testing.T) {
	t.Parallel()
	provider := newTestOmlxProvider(t, "")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, omlxUserChatRequest(schemas.Omlx, nil))
	require.NotNil(t, bifrostErr)
	require.Contains(t, bifrostErr.Error.Message, "no base URL configured")

	_, bifrostErr = provider.Decision(ctx, schemas.Key{}, newOmlxDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.NotNil(t, bifrostErr)
	require.Contains(t, bifrostErr.Error.Message, "no base URL configured")

	_, bifrostErr = provider.ListModels(ctx, []schemas.Key{{}}, &schemas.BifrostListModelsRequest{})
	require.NotNil(t, bifrostErr)
}

// oMLX accepts native sampling extensions (e.g. chat_template_kwargs, used to
// switch Qwen thinking off with enable_thinking:false) on the wire. The omlx
// provider turns extra-param passthrough on by itself: the caller does not set
// BifrostContextKeyPassthroughExtraParams, yet the fields still reach the
// backend, while the standard OpenAI fields convert as usual.
func TestOmlxChatCompletion_PassesChatTemplateKwargsThrough(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, omlxChatResponseBody)
	}))
	defer server.Close()

	// The provider-level URL is a decoy: the key's omlx_key_config.url wins.
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the provider-level base URL instead of the key's: %s", r.URL.Path)
	}))
	defer decoy.Close()
	provider := newTestOmlxProvider(t, decoy.URL+"/")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		Models:        []string{"*"},
		OmlxKeyConfig: &schemas.OmlxKeyConfig{URL: *schemas.NewSecretVar(server.URL + "/")},
	}
	req := omlxUserChatRequest(schemas.Omlx, map[string]interface{}{
		"chat_template_kwargs": map[string]interface{}{"enable_thinking": false},
		"repetition_penalty":   1.05,
	})

	resp, bifrostErr := provider.ChatCompletion(ctx, key, req)
	require.Nil(t, bifrostErr)

	mu.Lock()
	body := string(gotBody)
	mu.Unlock()
	require.Contains(t, body, `"chat_template_kwargs":{"enable_thinking":false}`)
	require.Contains(t, body, `"repetition_penalty":1.05`)
	require.Contains(t, body, `"model":"Qwen/Qwen3-4B"`)

	require.Equal(t, "chatcmpl-test", resp.ID)
	require.Len(t, resp.Choices, 1)
	require.NotNil(t, resp.Choices[0].ChatNonStreamResponseChoice)
	require.NotNil(t, resp.Choices[0].Message.Content.ContentStr)
	require.Equal(t, "Hello!", *resp.Choices[0].Message.Content.ContentStr)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 5, resp.Usage.PromptTokens)
	require.Equal(t, 8, resp.Usage.TotalTokens)
	require.Equal(t, "chat.completion", resp.Object)
}

// A keyless oMLX must not get an Authorization header; a key value is sent as a
// bearer token. This is the ollama/sgl local-server posture.
func TestOmlxChatAuthentication(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, omlxChatResponseBody)
	}))
	defer server.Close()
	provider := newTestOmlxProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.ChatCompletion(ctx, schemas.Key{}, omlxUserChatRequest(schemas.Omlx, nil))
	require.Nil(t, err)
	_, err = provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("omlx-token")}, omlxUserChatRequest(schemas.Omlx, nil))
	require.Nil(t, err)

	mu.Lock()
	auths := gotAuth
	mu.Unlock()
	require.Len(t, auths, 2)
	require.Empty(t, auths[0], "a keyless oMLX must not get an Authorization header")
	require.Equal(t, "Bearer omlx-token", auths[1])
}

// The same request run through the typed OpenAI converter without the
// passthrough flag loses chat_template_kwargs entirely — the silent drop that
// makes a plain openai-compat shim unsafe for oMLX-native sampling params.
// With the flag on, the converter path emits them again.
func TestOpenAIConverterDropsExtraParamsWithoutPassthrough(t *testing.T) {
	t.Parallel()
	extra := map[string]interface{}{
		"chat_template_kwargs": map[string]interface{}{"enable_thinking": false},
	}
	bifrostReq := omlxUserChatRequest(schemas.OpenAI, extra)

	withoutFlag := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	body, bifrostErr := providerUtils.CheckContextAndGetRequestBody(withoutFlag, bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIChatRequest(withoutFlag, bifrostReq), nil
		})
	require.Nil(t, bifrostErr)
	var bare map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &bare))
	require.NotContains(t, bare, "chat_template_kwargs", "the typed converter must drop unmodeled fields without passthrough")

	withFlag := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	withFlag.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	body, bifrostErr = providerUtils.CheckContextAndGetRequestBody(withFlag, bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIChatRequest(withFlag, bifrostReq), nil
		})
	require.Nil(t, bifrostErr)
	require.Contains(t, string(body), `"chat_template_kwargs":{"enable_thinking":false}`)
}

// Streaming is standard OpenAI SSE: the body carries the extra params (the
// provider forces passthrough for streams too), chunks parse, and [DONE] ends
// the stream.
func TestOmlxChatCompletionStream_PassesExtraParamsAndParsesSSE(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1,"model":"Qwen/Qwen3-4B","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1,"model":"Qwen/Qwen3-4B","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOmlxProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := omlxUserChatRequest(schemas.Omlx, map[string]interface{}{
		"chat_template_kwargs": map[string]interface{}{"enable_thinking": false},
	})

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, schemas.Key{}, req)
	require.Nil(t, bifrostErr)
	require.NotNil(t, stream)

	var texts []string
	timeout := time.After(10 * time.Second)
	for chunk := range stream {
		select {
		case <-timeout:
			t.Fatal("stream did not finish")
		default:
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
	require.Contains(t, body, `"chat_template_kwargs":{"enable_thinking":false}`)
	require.Contains(t, body, `"stream":true`)
}

func noopPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// Responses is not offered by oMLX; the provider converts the request to chat,
// posts to /v1/chat/completions, and returns a Responses-shaped answer.
func TestOmlxResponses_FallsBackToChatCompletions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, omlxChatResponseBody)
	}))
	defer server.Close()

	provider := newTestOmlxProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Responses(ctx, schemas.Key{}, &schemas.BifrostResponsesRequest{
		Provider: schemas.Omlx,
		Model:    "Qwen/Qwen3-4B",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Hello")},
		}},
		Params: &schemas.ResponsesParameters{},
	})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/chat/completions", path)
	require.NotNil(t, resp)
}

// Embedding reaches oMLX's OpenAI-compatible /v1/embeddings.
func TestOmlxEmbedding(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","model":"qwen3-embedding","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
	}))
	defer server.Close()

	provider := newTestOmlxProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	text := "hello"
	resp, bifrostErr := provider.Embedding(ctx, schemas.Key{}, &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Omlx,
		Model:    "qwen3-embedding",
		Input:    []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
	})
	require.Nil(t, bifrostErr)
	require.Len(t, resp.Data, 1)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/embeddings", path)
}

// List models fans out per key, so every oMLX server answers for its own URL;
// results come back under the omlx/ prefix.
func TestOmlxListModels_QueriesEachKeysURL(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	modelServer := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model","created":1727740800,"owned_by":"omlx"}]}`, id)
		}))
	}
	serverA := modelServer("Qwen/Qwen3-4B")
	defer serverA.Close()
	serverB := modelServer("gpt-oss-20b")
	defer serverB.Close()

	// Provider-level URL must not be used when every key carries its own.
	provider := newTestOmlxProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keys := []schemas.Key{
		{Models: []string{"*"}, OmlxKeyConfig: &schemas.OmlxKeyConfig{URL: *schemas.NewSecretVar(serverA.URL)}},
		{Models: []string{"*"}, OmlxKeyConfig: &schemas.OmlxKeyConfig{URL: *schemas.NewSecretVar(serverB.URL)}},
	}
	resp, bifrostErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{
		Provider: schemas.Omlx,
	})
	require.Nil(t, bifrostErr)
	require.Len(t, resp.Data, 2)

	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	require.Contains(t, ids, "omlx/Qwen/Qwen3-4B")
	require.Contains(t, ids, "omlx/gpt-oss-20b")

	mu.Lock()
	got := paths
	mu.Unlock()
	require.ElementsMatch(t, []string{"/v1/models", "/v1/models"}, got)
}

// A retrieve must use the key's own oMLX URL over the provider-level one,
// accept namespaced HF-style ids ("Qwen/Qwen3-4B") by escaping each path
// segment, and refuse dot-segment ids that could reshape the URL.
func TestOmlxModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath string
	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/v1/models/")
		_, _ = fmt.Fprintf(w, `{"id":%q,"object":"model","created":1727740800,"owned_by":"omlx"}`, id)
	}))
	defer keyServer.Close()
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the provider-level base URL instead of the key's: %s", r.URL.Path)
	}))
	defer providerServer.Close()

	provider := newTestOmlxProvider(t, providerServer.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{OmlxKeyConfig: &schemas.OmlxKeyConfig{URL: *schemas.NewSecretVar(keyServer.URL)}}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "gpt-oss-20b"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/models/gpt-oss-20b", path)
	require.Equal(t, "omlx/gpt-oss-20b", response.ID)
	require.Equal(t, schemas.Ptr("omlx"), response.OwnedBy)

	// HF-style ids contain '/': each segment must be escaped and the slash
	// kept as a path separator, not rejected with a 400.
	response, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "Qwen/Qwen3-4B"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path = gotPath
	mu.Unlock()
	require.Equal(t, "/v1/models/Qwen/Qwen3-4B", path)
	require.Equal(t, "omlx/Qwen/Qwen3-4B", response.ID)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../api/tags"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}

// --- decisions -------------------------------------------------------------

const omlxDecisionResponseBody = `{"model":"nimble","answers":{"is_red":{"type":"noul","noul":0.91}},"usage":{"input_tokens":42,"output_tokens":0}}`

func newOmlxDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Omlx,
		Model:    "nimble",
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_red"), Instructions: schemas.NewDecisionText("Is the image mostly red?")},
		},
	}
}

// A decision posts Typesafe's systemone body to the key's oMLX URL at
// /v1/systemone, sends Bearer auth only when the key has a value, and maps
// oMLX's answers and usage back to Bifrost.
func TestOmlxDecision_PostsSystemOneBody(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath, gotAuth string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotAuth, gotBody = r.URL.Path, r.Header.Get("Authorization"), body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, omlxDecisionResponseBody)
	}))
	defer server.Close()

	provider := newTestOmlxProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := newOmlxDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})

	resp, bifrostErr := provider.Decision(ctx, schemas.Key{}, request)
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, auth, body := gotPath, gotAuth, gotBody
	mu.Unlock()
	require.Equal(t, "/v1/systemone", path)
	require.Empty(t, auth, "a keyless oMLX must not get an Authorization header")

	want, err := typesafe.ToTypesafeDecisionRequest(request)
	require.NoError(t, err)
	wantBody, err := providerUtils.MarshalProviderRequest(want)
	require.NoError(t, err)
	require.JSONEq(t, string(wantBody), string(body), "the wire body must be exactly Typesafe's systemone shape")

	require.Len(t, resp.Answers, 1)
	require.NotNil(t, resp.Answers[0].Probability)
	require.Equal(t, 0.91, *resp.Answers[0].Probability)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 42, resp.Usage.PromptTokens)

	keyWithValue := schemas.Key{Value: *schemas.NewSecretVar("omlx-token")}
	_, bifrostErr = provider.Decision(ctx, keyWithValue, newOmlxDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.Nil(t, bifrostErr)
	mu.Lock()
	auth = gotAuth
	mu.Unlock()
	require.Equal(t, "Bearer omlx-token", auth)
}

// Unlike chat, decisions do not force passthrough on: oMLX-native extra params
// (a chat_template_kwargs the systemone route might want) reach the wire only
// when the caller asks, and are otherwise dropped like any unmodeled field.
func TestOmlxDecision_ExtraParamsRequirePassthrough(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, omlxDecisionResponseBody)
	}))
	defer server.Close()

	provider := newTestOmlxProvider(t, server.URL)
	extra := map[string]interface{}{"chat_template_kwargs": map[string]interface{}{"enable_thinking": false}}

	plain := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := newOmlxDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	request.ExtraParams = extra
	_, bifrostErr := provider.Decision(plain, schemas.Key{}, request)
	require.Nil(t, bifrostErr)

	opted := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	opted.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	request = newOmlxDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	request.ExtraParams = extra
	_, bifrostErr = provider.Decision(opted, schemas.Key{}, request)
	require.Nil(t, bifrostErr)

	mu.Lock()
	got := bodies
	mu.Unlock()
	require.Len(t, got, 2)
	require.NotContains(t, got[0], "chat_template_kwargs")
	require.Contains(t, got[1], `"chat_template_kwargs":{"enable_thinking":false}`)
}

// oMLX's own errors reach the caller with their status and message, in both
// its native {"error":"..."} string shape (read via gjson, since the OpenAI
// parser cannot decode it) and the OpenAI-shaped one.
func TestOmlxDecision_PassesOmlxErrorsThrough(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"native not pulled":  {status: http.StatusNotFound, body: `{"error":"model 'nimble' not found"}`, want: "model 'nimble' not found"},
		"native bad request": {status: http.StatusBadRequest, body: `{"error":"state must be a string, object, or array"}`, want: "state must be a string, object, or array"},
		"openai shaped":      {status: http.StatusBadRequest, body: `{"error":{"message":"model does not support decisions","type":"invalid_request_error"}}`, want: "model does not support decisions"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprint(w, tc.body)
		}))
		provider := newTestOmlxProvider(t, server.URL)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newOmlxDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
		server.Close()
		require.NotNil(t, bifrostErr, name)
		require.Equal(t, schemas.Ptr(tc.status), bifrostErr.StatusCode, name)
		require.NotNil(t, bifrostErr.Error, name)
		require.Equal(t, tc.want, bifrostErr.Error.Message, name)
	}
}

// --- unsupported surface ----------------------------------------------------

// Everything oMLX does not offer — rerank, OCR, image, video, batch, files,
// token counting, containers, cached contents (Google-only; delegating to
// openai would dial api.openai.com), and passthrough — fails fast with the
// standard unsupported-operation error instead of hitting the network.
func TestOmlxUnsupportedOperations(t *testing.T) {
	t.Parallel()
	provider := newTestOmlxProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{}
	keys := []schemas.Key{{}}
	finalizer := func(context.Context) {}

	cases := []struct {
		name string
		call func() *schemas.BifrostError
	}{
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
		require.Contains(t, bifrostErr.Error.Message, "is not supported by omlx provider", tc.name)
		require.Equal(t, schemas.Omlx, bifrostErr.ExtraFields.Provider, tc.name)
	}
}
