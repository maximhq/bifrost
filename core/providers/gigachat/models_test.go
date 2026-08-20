package gigachat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func testGigaChatListModels(t *testing.T) {
	t.Parallel()

	t.Run("ConverterMapsResponse", testGigaChatListModelsConverterMapsResponse)
	t.Run("ConverterFiltersAndAliases", testGigaChatListModelsConverterFiltersAndAliases)
	t.Run("ExecutesWithOAuthToken", testGigaChatListModelsExecutesWithOAuthToken)
	t.Run("MapsProviderErrors", testGigaChatListModelsMapsProviderErrors)
	t.Run("RefreshesTokenAfterUnauthorized", testGigaChatListModelsRefreshesTokenAfterUnauthorized)
}

func TestGigaChatSupportedMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		modelType string
		want      []string
	}{
		{
			name:      "chat includes responses",
			modelType: "chat",
			want: []string{
				string(schemas.ChatCompletionRequest),
				string(schemas.ChatCompletionStreamRequest),
				string(schemas.ResponsesRequest),
				string(schemas.ResponsesStreamRequest),
			},
		},
		{
			name:      "embedder only supports embeddings",
			modelType: "embedder",
			want:      []string{string(schemas.EmbeddingRequest)},
		},
		{
			name:      "unknown has no advertised methods",
			modelType: "reranker",
			want:      nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := toGigaChatSupportedMethods(tt.modelType); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("toGigaChatSupportedMethods(%q) = %#v, want %#v", tt.modelType, got, tt.want)
			}
		})
	}
}

func testGigaChatListModelsConverterMapsResponse(t *testing.T) {
	t.Parallel()

	response := &GigaChatListModelsResponse{
		Object: "list",
		Data: []GigaChatModel{
			{ID: "GigaChat", Object: "model", OwnedBy: "salutedevices", Type: "chat"},
			{ID: "Embeddings", Object: "model", OwnedBy: "salutedevices", Type: "embedder"},
			{ID: " ", Object: "model", OwnedBy: "salutedevices", Type: "chat"},
		},
	}

	converted := response.ToBifrostListModelsResponse(schemas.GigaChat, schemas.WhiteList{"*"}, nil, nil, false)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if len(converted.Data) != 2 {
		t.Fatalf("model count mismatch: got %d, want 2", len(converted.Data))
	}
	if converted.Data[0].ID != "gigachat/GigaChat" {
		t.Fatalf("model id mismatch: got %q", converted.Data[0].ID)
	}
	if converted.Data[0].OwnedBy == nil || *converted.Data[0].OwnedBy != "salutedevices" {
		t.Fatalf("owned_by mismatch: %#v", converted.Data[0].OwnedBy)
	}
	wantMethods := []string{
		string(schemas.ChatCompletionRequest),
		string(schemas.ChatCompletionStreamRequest),
		string(schemas.ResponsesRequest),
		string(schemas.ResponsesStreamRequest),
	}
	if fmt.Sprint(converted.Data[0].SupportedMethods) != fmt.Sprint(wantMethods) {
		t.Fatalf("supported methods mismatch: got %#v, want %#v", converted.Data[0].SupportedMethods, wantMethods)
	}
	wantEmbeddingMethods := []string{string(schemas.EmbeddingRequest)}
	if fmt.Sprint(converted.Data[1].SupportedMethods) != fmt.Sprint(wantEmbeddingMethods) {
		t.Fatalf("embedder supported methods mismatch: got %#v, want %#v", converted.Data[1].SupportedMethods, wantEmbeddingMethods)
	}
}

func testGigaChatListModelsConverterFiltersAndAliases(t *testing.T) {
	t.Parallel()

	response := &GigaChatListModelsResponse{
		Data: []GigaChatModel{
			{ID: "GigaChat", Object: "model", OwnedBy: "salutedevices", Type: "chat"},
			{ID: "GigaChat-Pro", Object: "model", OwnedBy: "salutedevices", Type: "chat"},
		},
	}

	converted := response.ToBifrostListModelsResponse(
		schemas.GigaChat,
		schemas.WhiteList{"pro-alias"},
		nil,
		schemas.KeyAliases{"pro-alias": {ModelID: "GigaChat-Pro"}},
		false,
	)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if len(converted.Data) != 1 {
		t.Fatalf("model count mismatch: got %d, want 1", len(converted.Data))
	}
	if converted.Data[0].ID != "gigachat/pro-alias" {
		t.Fatalf("alias model id mismatch: got %q", converted.Data[0].ID)
	}
	if converted.Data[0].Alias == nil || *converted.Data[0].Alias != "GigaChat-Pro" {
		t.Fatalf("alias mismatch: %#v", converted.Data[0].Alias)
	}
}

func testGigaChatListModelsExecutesWithOAuthToken(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var modelRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Basic super-secret-credentials" {
				t.Fatalf("token authorization header mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"models-access-token","expires_at":1893456000}`))
		case "/v1/models":
			modelRequests.Add(1)
			if request.Method != http.MethodGet {
				t.Fatalf("method mismatch: got %s, want GET", request.Method)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer models-access-token" {
				t.Fatalf("models authorization header mismatch: got %q", got)
			}
			if strings.Contains(request.Header.Get("Authorization"), "super-secret-credentials") {
				t.Fatal("models request leaked OAuth credentials")
			}
			if got := request.Header.Get("Accept"); got != "application/json" {
				t.Fatalf("accept header mismatch: got %q", got)
			}
			if got := request.Header.Get(gigaChatUserAgentHeader); got != gigaChatUserAgent {
				t.Fatalf("user-agent header mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "models-request-id")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"GigaChat","object":"model","owned_by":"salutedevices","type":"chat"},{"id":"GigaChat-Pro","object":"model","owned_by":"salutedevices","type":"chat"}]}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawResponse = true
	key := testGigaChatOAuthKey(server.URL+"/oauth", "", "super-secret-credentials")
	key.ID = "gigachat-key"
	key.Models = schemas.WhiteList{"*"}

	ctx := testBifrostContext()
	response, bifrostErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr)
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("token request count mismatch: got %d, want 1", tokenRequests.Load())
	}
	if modelRequests.Load() != 1 {
		t.Fatalf("model request count mismatch: got %d, want 1", modelRequests.Load())
	}
	if len(response.Data) != 2 {
		t.Fatalf("model count mismatch: got %d, want 2", len(response.Data))
	}
	if response.Data[0].ID != "gigachat/GigaChat" || response.Data[1].ID != "gigachat/GigaChat-Pro" {
		t.Fatalf("models mismatch: %#v", response.Data)
	}
	if response.ExtraFields.RawResponse == nil {
		t.Fatal("expected raw response to be preserved")
	}
	if len(response.KeyStatuses) != 1 || response.KeyStatuses[0].Status != schemas.KeyStatusSuccess {
		t.Fatalf("key status mismatch: %#v", response.KeyStatuses)
	}
	if got := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders); got == nil {
		t.Fatal("provider response headers were not stored in context")
	}
}

func testGigaChatListModelsMapsProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":123,"message":"bad models request"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	key := testGigaChatAccessTokenKey("provider-error-token")
	key.ID = "gigachat-key"
	key.Models = schemas.WhiteList{"*"}

	response, bifrostErr := provider.ListModels(testBifrostContext(), []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.GigaChat})
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil {
		t.Fatal("expected provider error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad models request" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "123" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
	if len(bifrostErr.ExtraFields.KeyStatuses) != 1 || bifrostErr.ExtraFields.KeyStatuses[0].Status != schemas.KeyStatusListModelsFailed {
		t.Fatalf("key status mismatch: %#v", bifrostErr.ExtraFields.KeyStatuses)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatListModelsRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var modelRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"models-token-%d","expires_at":1893456000}`, tokenIndex)))
		case "/v1/models":
			modelIndex := modelRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer models-token-%d", modelIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", modelIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if modelIndex == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"GigaChat","object":"model","owned_by":"salutedevices","type":"chat"}]}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	key := testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials")
	key.ID = "gigachat-key"
	key.Models = schemas.WhiteList{"*"}

	response, bifrostErr := provider.ListModels(testBifrostContext(), []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("ListModels returned error: %v", bifrostErr)
	}
	if response == nil || len(response.Data) != 1 || response.Data[0].ID != "gigachat/GigaChat" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if modelRequests.Load() != 2 {
		t.Fatalf("model request count mismatch: got %d, want 2", modelRequests.Load())
	}
}
