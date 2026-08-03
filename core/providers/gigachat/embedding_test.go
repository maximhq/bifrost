package gigachat

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func testGigaChatEmbedding(t *testing.T) {
	t.Parallel()

	t.Run("ConverterMapsStringInput", testGigaChatEmbeddingConverterMapsStringInput)
	t.Run("ConverterMapsArrayInput", testGigaChatEmbeddingConverterMapsArrayInput)
	t.Run("ConverterAcceptsEncodingFormat", testGigaChatEmbeddingConverterAcceptsEncodingFormat)
	t.Run("ResponseAppliesBase64EncodingFormat", testGigaChatEmbeddingResponseAppliesBase64EncodingFormat)
	t.Run("RejectsUnsupportedParams", testGigaChatEmbeddingRejectsUnsupportedParams)
	t.Run("ExecutesWithOAuthToken", testGigaChatEmbeddingExecutesWithOAuthToken)
	t.Run("MapsProviderErrors", testGigaChatEmbeddingMapsProviderErrors)
	t.Run("RefreshesTokenAfterUnauthorized", testGigaChatEmbeddingRefreshesTokenAfterUnauthorized)
}

func TestGigaChatEmbedding(t *testing.T) {
	testGigaChatEmbedding(t)
}

func testGigaChatEmbeddingConverterMapsStringInput(t *testing.T) {
	t.Parallel()

	text := "hello"
	request := &schemas.BifrostEmbeddingRequest{
		Model: "Embeddings",
		Input: &schemas.EmbeddingInput{Text: &text},
	}

	gigaChatReq, err := ToGigaChatEmbeddingRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatEmbeddingRequest returned error: %v", err)
	}
	if gigaChatReq.Model != "Embeddings" {
		t.Fatalf("model mismatch: got %q", gigaChatReq.Model)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"input":"hello"`) {
		t.Fatalf("request body should preserve string input, got %s", body)
	}
}

func testGigaChatEmbeddingConverterMapsArrayInput(t *testing.T) {
	t.Parallel()

	request := &schemas.BifrostEmbeddingRequest{
		Model: "EmbeddingsGigaR",
		Input: &schemas.EmbeddingInput{Texts: []string{"first", "second"}},
	}

	gigaChatReq, err := ToGigaChatEmbeddingRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatEmbeddingRequest returned error: %v", err)
	}
	if gigaChatReq.Model != "EmbeddingsGigaR" {
		t.Fatalf("model mismatch: got %q", gigaChatReq.Model)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"input":["first","second"]`) {
		t.Fatalf("request body should preserve array input, got %s", body)
	}
}

func testGigaChatEmbeddingConverterAcceptsEncodingFormat(t *testing.T) {
	t.Parallel()

	encodingFormat := "base64"
	request := testGigaChatEmbeddingRequest()
	request.Params = &schemas.EmbeddingParameters{
		EncodingFormat: &encodingFormat,
	}

	gigaChatReq, err := ToGigaChatEmbeddingRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatEmbeddingRequest returned error: %v", err)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if strings.Contains(string(body), "encoding_format") {
		t.Fatalf("GigaChat request body should not include encoding_format, got %s", body)
	}
}

func testGigaChatEmbeddingResponseAppliesBase64EncodingFormat(t *testing.T) {
	t.Parallel()

	encodingFormat := "base64"
	response := ToBifrostEmbeddingResponse(schemas.GigaChat, &GigaChatEmbeddingResponse{
		Object: "list",
		Model:  "Embeddings",
		Data: []GigaChatEmbeddingData{{
			Object:    "embedding",
			Index:     0,
			Embedding: []float64{0.1, 0.2},
		}},
	})

	if err := applyGigaChatEmbeddingEncodingFormat(response, &schemas.EmbeddingParameters{EncodingFormat: &encodingFormat}); err != nil {
		t.Fatalf("applyGigaChatEmbeddingEncodingFormat returned error: %v", err)
	}
	if response.Data[0].Embedding.EmbeddingArray != nil {
		t.Fatalf("expected base64 embedding string, got float array %#v", response.Data[0].Embedding.EmbeddingArray)
	}
	if response.Data[0].Embedding.EmbeddingStr == nil {
		t.Fatal("expected base64 embedding string, got nil")
	}

	decoded, err := base64.StdEncoding.DecodeString(*response.Data[0].Embedding.EmbeddingStr)
	if err != nil {
		t.Fatalf("failed to decode base64 embedding: %v", err)
	}
	if len(decoded) != 8 {
		t.Fatalf("decoded embedding byte length mismatch: got %d, want 8", len(decoded))
	}
	gotFirst := math.Float32frombits(binary.LittleEndian.Uint32(decoded[0:4]))
	gotSecond := math.Float32frombits(binary.LittleEndian.Uint32(decoded[4:8]))
	if gotFirst != float32(0.1) || gotSecond != float32(0.2) {
		t.Fatalf("decoded embedding mismatch: got [%v %v]", gotFirst, gotSecond)
	}
}

func testGigaChatEmbeddingRejectsUnsupportedParams(t *testing.T) {
	t.Parallel()

	encodingFormat := "base64"
	dimensions := 1024
	request := testGigaChatEmbeddingRequest()
	request.Params = &schemas.EmbeddingParameters{
		EncodingFormat: &encodingFormat,
		Dimensions:     &dimensions,
		ExtraParams: map[string]interface{}{
			"user": "user-id",
		},
	}

	_, err := ToGigaChatEmbeddingRequest(request)
	if err == nil {
		t.Fatal("expected unsupported params error, got nil")
	}
	for _, want := range []string{"dimensions", "user"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing unsupported param %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "encoding_format") {
		t.Fatalf("encoding_format should be accepted for OpenAI SDK compatibility, got %q", err.Error())
	}

	_, err = ToGigaChatEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "Embeddings",
		Input: &schemas.EmbeddingInput{Embedding: []int{1, 2, 3}},
	})
	if err == nil || !strings.Contains(err.Error(), "string or array-of-string") {
		t.Fatalf("expected unsupported input error, got %v", err)
	}
}

func testGigaChatEmbeddingExecutesWithOAuthToken(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var embeddingRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Basic super-secret-credentials" {
				t.Fatalf("token authorization header mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"embedding-access-token","expires_at":1893456000}`))
		case "/v1/embeddings":
			embeddingRequests.Add(1)
			if request.Method != http.MethodPost {
				t.Fatalf("method mismatch: got %s, want POST", request.Method)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer embedding-access-token" {
				t.Fatalf("embeddings authorization header mismatch: got %q", got)
			}
			if strings.Contains(request.Header.Get("Authorization"), "super-secret-credentials") {
				t.Fatal("embeddings request leaked OAuth credentials")
			}
			assertGigaChatEmbeddingRequestBody(t, request)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "embeddings-request-id")
			_, _ = w.Write([]byte(`{
				"object":"list",
				"data":[
					{"object":"embedding","embedding":[0.1,0.2],"index":0,"usage":{"prompt_tokens":5}},
					{"object":"embedding","embedding":[0.3,0.4],"index":1,"usage":{"prompt_tokens":7}}
				],
				"model":"Embeddings"
			}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawRequest = true
	provider.sendBackRawResponse = true

	response, bifrostErr := provider.Embedding(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "super-secret-credentials"), testGigaChatEmbeddingRequest())
	if bifrostErr != nil {
		t.Fatalf("Embedding returned error: %v", bifrostErr)
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("token request count mismatch: got %d, want 1", tokenRequests.Load())
	}
	if embeddingRequests.Load() != 1 {
		t.Fatalf("embedding request count mismatch: got %d, want 1", embeddingRequests.Load())
	}
	if response.Model != "Embeddings" || response.Object != "list" {
		t.Fatalf("response metadata mismatch: %#v", response)
	}
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
	}
	if len(response.Data) != 2 {
		t.Fatalf("embedding count mismatch: got %d, want 2", len(response.Data))
	}
	if got := response.Data[0].Embedding.EmbeddingArray; fmt.Sprint(got) != fmt.Sprint([]float64{0.1, 0.2}) {
		t.Fatalf("embedding mismatch: %#v", got)
	}
	if response.Usage == nil || response.Usage.PromptTokens != 12 || response.Usage.TotalTokens != 12 {
		t.Fatalf("usage mismatch: %#v", response.Usage)
	}
	if response.ExtraFields.RawRequest == nil || response.ExtraFields.RawResponse == nil {
		t.Fatalf("expected raw request and response, got request=%#v response=%#v", response.ExtraFields.RawRequest, response.ExtraFields.RawResponse)
	}
}

func testGigaChatEmbeddingMapsProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":123,"message":"bad embeddings request"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.Embedding(testBifrostContext(), testGigaChatAccessTokenKey("provider-error-token"), testGigaChatEmbeddingRequest())
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil {
		t.Fatal("expected provider error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad embeddings request" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "123" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatEmbeddingRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var embeddingRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"embedding-token-%d","expires_at":1893456000}`, tokenIndex)))
		case "/v1/embeddings":
			embeddingIndex := embeddingRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer embedding-token-%d", embeddingIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", embeddingIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if embeddingIndex == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"Embeddings"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.Embedding(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatEmbeddingRequest())
	if bifrostErr != nil {
		t.Fatalf("Embedding returned error: %v", bifrostErr)
	}
	if response == nil || len(response.Data) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if embeddingRequests.Load() != 2 {
		t.Fatalf("embedding request count mismatch: got %d, want 2", embeddingRequests.Load())
	}
}

func testGigaChatEmbeddingRequest() *schemas.BifrostEmbeddingRequest {
	return &schemas.BifrostEmbeddingRequest{
		Model: "Embeddings",
		Input: &schemas.EmbeddingInput{Texts: []string{"first", "second"}},
	}
}

func assertGigaChatEmbeddingRequestBody(t *testing.T, request *http.Request) {
	t.Helper()

	var body GigaChatEmbeddingRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode embeddings request body: %v", err)
	}
	if body.Model != "Embeddings" {
		t.Fatalf("model mismatch: got %q", body.Model)
	}
	if body.Input == nil || len(body.Input.Texts) != 2 || body.Input.Texts[0] != "first" || body.Input.Texts[1] != "second" {
		t.Fatalf("input mismatch: %#v", body.Input)
	}
	if body.Input.Text != nil || body.Input.Embedding != nil || body.Input.Embeddings != nil {
		t.Fatalf("unexpected non-text embedding input: %#v", body.Input)
	}
}
