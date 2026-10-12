package ollama_test

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
	"github.com/maximhq/bifrost/core/providers/ollama"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestOllama(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")) == "" {
		t.Skip("Skipping Ollama tests because OLLAMA_BASE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Ollama,
		ChatModel:      "llama3.1:latest",
		TextModel:      "", // Ollama doesn't support text completion in newer models
		EmbeddingModel: "", // Ollama doesn't support embedding
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false, // Not supported
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

	t.Run("OllamaTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must use the key's own Ollama URL over the provider-level one, keep a
// tagged model name ("llama3.2:3b") in one path segment, and refuse a path-shaping id.
func TestOllamaModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath string
	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"llama3.2:3b","object":"model","created":1727740800,"owned_by":"library"}`))
	}))
	defer keyServer.Close()
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request reached the provider-level base URL instead of the key's: %s", r.URL.Path)
	}))
	defer providerServer.Close()

	provider, err := ollama.NewOllamaProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: providerServer.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: *schemas.NewSecretVar(keyServer.URL)}}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "llama3.2:3b"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path := gotPath
	mu.Unlock()
	require.Equal(t, "/v1/models/llama3.2:3b", path)
	require.Equal(t, "ollama/llama3.2:3b", response.ID)
	require.Equal(t, schemas.Ptr("library"), response.OwnedBy)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../api/tags"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}

const ollamaDecisionResponseBody = `{"model":"nimble","answers":{"is_red":{"type":"noul","noul":0.91}},"usage":{"input_tokens":42,"output_tokens":0}}`

func newOllamaDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Ollama,
		Model:    "nimble",
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_red"), Instructions: schemas.NewDecisionText("Is the image mostly red?")},
		},
	}
}

func ollamaUserMessage(parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}
}

func ollamaTextPart(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr(text)}
}

func ollamaImagePart(url string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(url)}
}

func encodeOllamaDecision(t *testing.T, request *schemas.BifrostDecisionRequest) map[string]json.RawMessage {
	t.Helper()
	native, err := ollama.ToOllamaDecisionRequest(request)
	require.NoError(t, err)
	data, err := providerUtils.MarshalProviderRequest(native)
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	return top
}

// An input without images or Ollama params sends exactly Typesafe's body, so
// questions and state validate as they do there.
func TestToOllamaDecisionRequest_MatchesTypesafeWithoutImages(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]schemas.DecisionInput{
		"text":       {Text: schemas.Ptr("The sky is blue.")},
		"structured": {Structured: map[string]any{"ticket": "A-4021"}},
		"messages":   {Messages: []schemas.DecisionInputMessage{ollamaUserMessage(ollamaTextPart("hi"))}},
	} {
		request := newOllamaDecisionRequest(input)
		native, err := ollama.ToOllamaDecisionRequest(request)
		require.NoError(t, err, name)
		got, err := providerUtils.MarshalProviderRequest(native)
		require.NoError(t, err, name)
		typesafeNative, err := typesafe.ToTypesafeDecisionRequest(request)
		require.NoError(t, err, name)
		want, err := providerUtils.MarshalProviderRequest(typesafeNative)
		require.NoError(t, err, name)
		require.JSONEq(t, string(want), string(got), name)
	}
}

// Image parts move, in order, into the top-level images array: a base64 data
// URL is sent as its raw payload, and anything else (an https URL) is sent
// unchanged for Ollama to judge. The text stays in the state.
func TestToOllamaDecisionRequest_MovesImagesToImagesArray(t *testing.T) {
	t.Parallel()
	top := encodeOllamaDecision(t, newOllamaDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		ollamaUserMessage(ollamaTextPart("Look at these"), ollamaImagePart("data:image/png;base64,iVBORw0KGgo=")),
		ollamaUserMessage(ollamaImagePart("https://example.com/red.png")),
	}}))
	require.JSONEq(t, `["iVBORw0KGgo=","https://example.com/red.png"]`, string(top["images"]))
	require.Contains(t, string(top["state"]), "Look at these")
	require.NotContains(t, string(top["state"]), "iVBORw0KGgo=")
	require.NotContains(t, string(top["state"]), "example.com")
}

func TestToOllamaDecisionRequest_RejectsImageWithoutURL(t *testing.T) {
	t.Parallel()
	_, err := ollama.ToOllamaDecisionRequest(newOllamaDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		ollamaUserMessage(ollamaTextPart("hi"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage}),
	}}))
	require.Error(t, err)
}

// keep_alive and native images are Ollama's own fields, so they are lifted out
// of extra params into typed fields without the passthrough flag, and native
// images follow the ones taken from image parts. Raw JSON values, as the
// Typesafe route captures them, decode the same way.
func TestToOllamaDecisionRequest_LiftsKeepAliveAndImages(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		keepAlive interface{}
		want      string
	}{
		"duration":     {keepAlive: "10m", want: `"10m"`},
		"seconds":      {keepAlive: float64(300), want: `300`},
		"negative":     {keepAlive: float64(-1), want: `-1`},
		"raw duration": {keepAlive: json.RawMessage(`"1h"`), want: `"1h"`},
		"raw seconds":  {keepAlive: json.RawMessage(`0`), want: `0`},
	} {
		request := newOllamaDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			ollamaUserMessage(ollamaTextPart("hi"), ollamaImagePart("data:image/png;base64,AAAA")),
		}})
		request.ExtraParams = map[string]interface{}{"keep_alive": tc.keepAlive, "images": []interface{}{"BBBB"}, "other": true}
		native, err := ollama.ToOllamaDecisionRequest(request)
		require.NoError(t, err, name)
		require.Equal(t, []string{"AAAA", "BBBB"}, native.Images, name)
		require.Equal(t, map[string]interface{}{"other": true}, native.GetExtraParams(), name)
		require.Contains(t, request.ExtraParams, "keep_alive", "%s: the request's extra params must not be modified", name)
		data, err := providerUtils.MarshalProviderRequest(native)
		require.NoError(t, err, name)
		var top map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &top), name)
		require.JSONEq(t, tc.want, string(top["keep_alive"]), name)
	}
}

// A keep_alive that is neither a string nor a number, or images that are not
// strings, is a local 400.
func TestToOllamaDecisionRequest_RejectsMistypedOllamaParams(t *testing.T) {
	t.Parallel()
	for name, extra := range map[string]map[string]interface{}{
		"bool keep_alive":   {"keep_alive": true},
		"object keep_alive": {"keep_alive": map[string]interface{}{"m": 5}},
		"numeric images":    {"images": []interface{}{1, 2}},
	} {
		request := newOllamaDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
		request.ExtraParams = extra
		_, err := ollama.ToOllamaDecisionRequest(request)
		require.Error(t, err, name)
		_, isBadRequest := providerUtils.AsBifrostBadRequestError(err)
		require.True(t, isBadRequest, "%s: want a 400, got %v", name, err)
	}
}

func TestOllamaKeepAlive_JSON(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]ollama.OllamaKeepAlive{
		`"5m"`: {Duration: schemas.Ptr("5m")},
		`300`:  {Seconds: schemas.Ptr(300.0)},
		`-1`:   {Seconds: schemas.Ptr(-1.0)},
		`null`: {},
	} {
		var got ollama.OllamaKeepAlive
		require.NoError(t, json.Unmarshal([]byte(input), &got), input)
		require.Equal(t, want, got, input)
		data, err := json.Marshal(got)
		require.NoError(t, err, input)
		require.JSONEq(t, input, string(data), input)
	}
	for _, input := range []string{`true`, `{}`, `[1]`} {
		var got ollama.OllamaKeepAlive
		require.Error(t, json.Unmarshal([]byte(input), &got), input)
	}
	_, err := json.Marshal(ollama.OllamaKeepAlive{Duration: schemas.Ptr("5m"), Seconds: schemas.Ptr(1.0)})
	require.Error(t, err)
}

func newOllamaDecisionProvider(t *testing.T, baseURL string) *ollama.OllamaProvider {
	t.Helper()
	provider, err := ollama.NewOllamaProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

// A decision posts to the key's Ollama URL at /v1/systemone, sends Bearer auth
// only when the key has a value, and maps Ollama's answers and usage.
func TestOllamaDecision_PostsToSystemOne(t *testing.T) {
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
		_, _ = fmt.Fprint(w, ollamaDecisionResponseBody)
	}))
	defer server.Close()

	provider := newOllamaDecisionProvider(t, "")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: *schemas.NewSecretVar(server.URL)}}
	request := newOllamaDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	request.ExtraParams = map[string]interface{}{"keep_alive": "10m"}

	resp, bifrostErr := provider.Decision(ctx, key, request)
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, auth, body := gotPath, gotAuth, gotBody
	mu.Unlock()
	require.Equal(t, "/v1/systemone", path)
	require.Empty(t, auth, "a keyless Ollama must not get an Authorization header")
	require.Contains(t, string(body), `"keep_alive":"10m"`)
	require.Len(t, resp.Answers, 1)
	require.NotNil(t, resp.Answers[0].Probability)
	require.Equal(t, 0.91, *resp.Answers[0].Probability)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 42, resp.Usage.PromptTokens)

	key.Value = *schemas.NewSecretVar("ollama-token")
	_, bifrostErr = provider.Decision(ctx, key, newOllamaDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.Nil(t, bifrostErr)
	mu.Lock()
	auth = gotAuth
	mu.Unlock()
	require.Equal(t, "Bearer ollama-token", auth)
}

// With passthrough on, keep_alive is still sent once, from the typed field.
func TestOllamaDecision_PassthroughSendsKeepAliveOnce(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, ollamaDecisionResponseBody)
	}))
	defer server.Close()

	provider := newOllamaDecisionProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	request := newOllamaDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	request.ExtraParams = map[string]interface{}{"keep_alive": float64(0), "think": false}

	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, request)
	require.Nil(t, bifrostErr)

	mu.Lock()
	body := string(gotBody)
	mu.Unlock()
	require.Equal(t, 1, strings.Count(body, `"keep_alive"`), body)
	require.Contains(t, body, `"keep_alive":0`)
	require.Contains(t, body, `"think":false`)
}

// Ollama's own errors reach the caller with their status and message, in both
// the native {"error":"..."} shape and the OpenAI-shaped one.
func TestOllamaDecision_PassesOllamaErrorsThrough(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"native not pulled":  {status: http.StatusNotFound, body: `{"error":"model 'nimble' not found"}`, want: "model 'nimble' not found"},
		"native bad request": {status: http.StatusBadRequest, body: `{"error":"images must be base64-encoded"}`, want: "images must be base64-encoded"},
		"openai shaped":      {status: http.StatusBadRequest, body: `{"error":{"message":"model does not support scoring","type":"invalid_request_error"}}`, want: "model does not support scoring"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprint(w, tc.body)
		}))
		provider := newOllamaDecisionProvider(t, server.URL)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newOllamaDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			ollamaUserMessage(ollamaTextPart("hi"), ollamaImagePart("https://example.com/red.png")),
		}}))
		server.Close()
		require.NotNil(t, bifrostErr, name)
		require.Equal(t, schemas.Ptr(tc.status), bifrostErr.StatusCode, name)
		require.NotNil(t, bifrostErr.Error, name)
		require.Equal(t, tc.want, bifrostErr.Error.Message, name)
	}
}
