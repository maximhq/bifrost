package gigachat

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestGigaChatCountTokens(t *testing.T) {
	t.Parallel()

	t.Run("ConverterMapsTextInput", testGigaChatCountTokensConverterMapsTextInput)
	t.Run("ConverterRejectsEmptyText", testGigaChatCountTokensConverterRejectsEmptyText)
	t.Run("ConverterRejectsImageContent", testGigaChatCountTokensConverterRejectsImageContent)
	t.Run("ConverterRejectsFileContent", testGigaChatCountTokensConverterRejectsFileContent)
	t.Run("ConverterRejectsAudioContent", testGigaChatCountTokensConverterRejectsAudioContent)
	t.Run("ResponseMapsTokenSums", testGigaChatCountTokensResponseMapsTokenSums)
	t.Run("ResponseAcceptsDataWrapper", testGigaChatCountTokensResponseAcceptsDataWrapper)
	t.Run("ExecutesWithOAuthToken", testGigaChatCountTokensExecutesWithOAuthToken)
	t.Run("MapsProviderErrors", testGigaChatCountTokensMapsProviderErrors)
	t.Run("RefreshesTokenAfterUnauthorized", testGigaChatCountTokensRefreshesTokenAfterUnauthorized)
}

func testGigaChatCountTokensConverterMapsTextInput(t *testing.T) {
	t.Parallel()

	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("first")},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesInputMessageContentBlockTypeText,
						Text: schemas.Ptr("second"),
					},
					{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: schemas.Ptr("third"),
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						},
					},
					{
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						},
					},
				}},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: schemas.Ptr("assistant output"),
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					},
				}}},
			},
		},
	}

	gigaChatReq, err := ToGigaChatCountTokensRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatCountTokensRequest returned error: %v", err)
	}
	if gigaChatReq.Model != "GigaChat" {
		t.Fatalf("model mismatch: got %q", gigaChatReq.Model)
	}
	wantInput := []string{"first", "second", "third", "assistant output"}
	if !equalStringSlices(gigaChatReq.Input, wantInput) {
		t.Fatalf("input mismatch: got %#v, want %#v", gigaChatReq.Input, wantInput)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal count tokens request: %v", err)
	}
	if !strings.Contains(string(body), `"model":"GigaChat"`) || !strings.Contains(string(body), `"input":["first","second","third","assistant output"]`) {
		t.Fatalf("unexpected request body: %s", body)
	}
}

func testGigaChatCountTokensConverterRejectsEmptyText(t *testing.T) {
	t.Parallel()

	_, err := ToGigaChatCountTokensRequest(&schemas.BifrostResponsesRequest{
		Model: "GigaChat",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("   ")},
		}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatCountTokensConverterRejectsImageContent(t *testing.T) {
	t.Parallel()

	_, err := ToGigaChatCountTokensRequest(&schemas.BifrostResponsesRequest{
		Model: "GigaChat",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeImage,
				ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
					ImageURL: schemas.Ptr("https://example.com/image.png"),
				},
			}}},
		}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "file, image, or audio") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatCountTokensConverterRejectsFileContent(t *testing.T) {
	t.Parallel()

	_, err := ToGigaChatCountTokensRequest(&schemas.BifrostResponsesRequest{
		Model: "GigaChat",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: schemas.Ptr("file-data"),
				},
			}}},
		}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "file, image, or audio") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatCountTokensConverterRejectsAudioContent(t *testing.T) {
	t.Parallel()

	_, err := ToGigaChatCountTokensRequest(&schemas.BifrostResponsesRequest{
		Model: "GigaChat",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeAudio,
				Audio: &schemas.ResponsesInputMessageContentBlockAudio{
					Format: "mp3",
					Data:   "audio-data",
				},
			}}},
		}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "file, image, or audio") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatCountTokensResponseMapsTokenSums(t *testing.T) {
	t.Parallel()

	response := ToBifrostCountTokensResponse(schemas.GigaChat, &GigaChatCountTokensResponse{
		Items: []GigaChatCountTokensItem{
			{Tokens: 3, Characters: 12},
			{Tokens: 5, Characters: 20},
		},
	}, "GigaChat")
	if response == nil {
		t.Fatal("response is nil")
	}
	if response.Object != "response.input_tokens" {
		t.Fatalf("object mismatch: got %q", response.Object)
	}
	if response.Model != "GigaChat" {
		t.Fatalf("model mismatch: got %q", response.Model)
	}
	if response.InputTokens != 8 {
		t.Fatalf("input tokens mismatch: got %d, want 8", response.InputTokens)
	}
	if response.TotalTokens == nil || *response.TotalTokens != 8 {
		t.Fatalf("total tokens mismatch: %#v", response.TotalTokens)
	}
	if !equalIntSlices(response.Tokens, []int{3, 5}) {
		t.Fatalf("tokens mismatch: got %#v", response.Tokens)
	}
	if response.OutputTokens != nil {
		t.Fatalf("expected nil output tokens, got %#v", response.OutputTokens)
	}
	if response.InputTokensDetails == nil || response.InputTokensDetails.TextTokens != 8 {
		t.Fatalf("input token details mismatch: %#v", response.InputTokensDetails)
	}
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q", response.ExtraFields.Provider)
	}
}

func testGigaChatCountTokensResponseAcceptsDataWrapper(t *testing.T) {
	t.Parallel()

	var gigaChatResponse GigaChatCountTokensResponse
	if err := json.Unmarshal([]byte(`{"data":[{"tokens":2,"characters":7},{"tokens":4,"characters":13}]}`), &gigaChatResponse); err != nil {
		t.Fatalf("failed to unmarshal data wrapper response: %v", err)
	}

	response := ToBifrostCountTokensResponse(schemas.GigaChat, &gigaChatResponse, "GigaChat")
	if response == nil || response.InputTokens != 6 || !equalIntSlices(response.Tokens, []int{2, 4}) {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func testGigaChatCountTokensExecutesWithOAuthToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"count-tokens-access-token","expires_at":1893456000}`))
		case "/v1/tokens/count":
			assertGigaChatCountTokensHTTPShape(t, request, "Bearer count-tokens-access-token")
			assertGigaChatCountTokensRequestBody(t, request, []string{"first", "second"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"tokens":3,"characters":5},{"tokens":4,"characters":6}]`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatCountTokensProvider(t, server.URL)
	response, bifrostErr := provider.CountTokens(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatCountTokensRequest())
	if bifrostErr != nil {
		t.Fatalf("CountTokens returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("response is nil")
	}
	if response.InputTokens != 7 {
		t.Fatalf("input tokens mismatch: got %d, want 7", response.InputTokens)
	}
	if response.TotalTokens == nil || *response.TotalTokens != 7 {
		t.Fatalf("total tokens mismatch: %#v", response.TotalTokens)
	}
	if !equalIntSlices(response.Tokens, []int{3, 4}) {
		t.Fatalf("tokens mismatch: got %#v", response.Tokens)
	}
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q", response.ExtraFields.Provider)
	}
}

func testGigaChatCountTokensMapsProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/tokens/count" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":"bad_count","message":"bad count tokens request"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatCountTokensProvider(t, server.URL)
	response, bifrostErr := provider.CountTokens(testBifrostContext(), testGigaChatAccessTokenKey("count-tokens-error-token"), testGigaChatCountTokensRequest())
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil {
		t.Fatal("expected provider error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad count tokens request" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "bad_count" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatCountTokensRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var countTokensRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"count-tokens-token-%d","expires_at":1893456000}`, tokenIndex)))
		case "/v1/tokens/count":
			countTokensIndex := countTokensRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer count-tokens-token-%d", countTokensIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", countTokensIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if countTokensIndex == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`[{"tokens":5,"characters":11}]`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatCountTokensProvider(t, server.URL)
	response, bifrostErr := provider.CountTokens(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatCountTokensRequest())
	if bifrostErr != nil {
		t.Fatalf("CountTokens returned error: %v", bifrostErr)
	}
	if response == nil || response.InputTokens != 5 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if countTokensRequests.Load() != 2 {
		t.Fatalf("count tokens request count mismatch: got %d, want 2", countTokensRequests.Load())
	}
}

func testGigaChatCountTokensRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Model: "GigaChat",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("first")},
			},
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("second")},
			},
		},
	}
}

func newTestGigaChatCountTokensProvider(t *testing.T, baseURL string) *GigaChatProvider {
	t.Helper()

	provider := newTestGigaChatChatProvider(t, baseURL)
	dialer := &net.Dialer{}
	provider.client.Dial = func(addr string) (net.Conn, error) {
		return dialer.Dial("tcp", addr)
	}
	provider.client.DialTimeout = nil
	return provider
}

func assertGigaChatCountTokensHTTPShape(t *testing.T, request *http.Request, wantAuthorization string) {
	t.Helper()

	if request.Method != http.MethodPost {
		t.Fatalf("method mismatch: got %s", request.Method)
	}
	if contentType := request.Header.Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content type mismatch: got %q", contentType)
	}
	if accept := request.Header.Get("Accept"); accept != "application/json" {
		t.Fatalf("accept mismatch: got %q", accept)
	}
	if userAgent := request.Header.Get("User-Agent"); userAgent != gigaChatUserAgent {
		t.Fatalf("user-agent mismatch: got %q", userAgent)
	}
	if auth := request.Header.Get("Authorization"); auth != wantAuthorization {
		t.Fatalf("authorization mismatch: got %q, want %q", auth, wantAuthorization)
	}
}

func assertGigaChatCountTokensRequestBody(t *testing.T, request *http.Request, wantInput []string) {
	t.Helper()

	var body GigaChatCountTokensRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode count tokens request body: %v", err)
	}
	if body.Model != "GigaChat" {
		t.Fatalf("model mismatch: got %q", body.Model)
	}
	if !equalStringSlices(body.Input, wantInput) {
		t.Fatalf("input mismatch: got %#v, want %#v", body.Input, wantInput)
	}
}

func equalStringSlices(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func equalIntSlices(got []int, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
