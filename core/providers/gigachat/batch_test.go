package gigachat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestGigaChatBatchTypesJSON(t *testing.T) {
	t.Parallel()

	resultFileID := "file-result"
	raw, err := json.Marshal(GigaChatBatches{Data: []GigaChatBatch{{
		ID:           "batch-1",
		Object:       "batch",
		Method:       GigaChatBatchMethodChatCompletions,
		Status:       GigaChatBatchStatusCompleted,
		ResultFileID: &resultFileID,
		RequestCounts: &GigaChatBatchRequestCounts{
			Total:     3,
			Completed: 2,
			Failed:    1,
		},
	}}})
	if err != nil {
		t.Fatalf("marshal GigaChatBatches returned error: %v", err)
	}

	var decoded GigaChatBatches
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal GigaChatBatches returned error: %v", err)
	}
	if len(decoded.Data) != 1 {
		t.Fatalf("decoded %d batches, want 1", len(decoded.Data))
	}
	batch := decoded.Data[0]
	if batch.ID != "batch-1" || batch.Method != GigaChatBatchMethodChatCompletions || batch.Status != GigaChatBatchStatusCompleted {
		t.Fatalf("decoded batch mismatch: %#v", batch)
	}
	if batch.RequestCounts == nil || batch.RequestCounts.Total != 3 || batch.RequestCounts.Completed != 2 || batch.RequestCounts.Failed != 1 {
		t.Fatalf("request counts mismatch: %#v", batch.RequestCounts)
	}
}

func TestGigaChatBatchesUnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		wantLen int
		wantID  string
	}{
		{
			name:    "data wrapper",
			payload: `{"data":[{"id":"batch-wrapper","object":"batch","method":"chat_completions","status":"created"}]}`,
			wantLen: 1,
			wantID:  "batch-wrapper",
		},
		{
			name:    "root array",
			payload: `[{"id":"batch-array","object":"batch","method":"embedder","status":"completed"}]`,
			wantLen: 1,
			wantID:  "batch-array",
		},
		{
			name:    "empty root array",
			payload: `[]`,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var decoded GigaChatBatches
			if err := json.Unmarshal([]byte(tt.payload), &decoded); err != nil {
				t.Fatalf("unmarshal GigaChatBatches returned error: %v", err)
			}
			if len(decoded.Data) != tt.wantLen {
				t.Fatalf("decoded %d batches, want %d", len(decoded.Data), tt.wantLen)
			}
			if tt.wantLen > 0 && decoded.Data[0].ID != tt.wantID {
				t.Fatalf("decoded id %q, want %q", decoded.Data[0].ID, tt.wantID)
			}
		})
	}
}

func TestToGigaChatBatchMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint schemas.BatchEndpoint
		want     GigaChatBatchMethod
		wantErr  string
	}{
		{name: "chat completions", endpoint: schemas.BatchEndpointChatCompletions, want: GigaChatBatchMethodChatCompletions},
		{name: "chat completions without version", endpoint: "/chat/completions", want: GigaChatBatchMethodChatCompletions},
		{name: "responses", endpoint: schemas.BatchEndpointResponses, want: GigaChatBatchMethodResponses},
		{name: "responses without version", endpoint: "/responses", want: GigaChatBatchMethodResponses},
		{name: "embeddings", endpoint: schemas.BatchEndpointEmbeddings, want: GigaChatBatchMethodEmbedder},
		{name: "embeddings without version", endpoint: "/embeddings", want: GigaChatBatchMethodEmbedder},
		{name: "unknown", endpoint: schemas.BatchEndpointCompletions, wantErr: "do not support endpoint"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := toGigaChatBatchMethod(tt.endpoint)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("toGigaChatBatchMethod() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("toGigaChatBatchMethod() returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("toGigaChatBatchMethod() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToBifrostGigaChatBatchEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method GigaChatBatchMethod
		want   string
	}{
		{name: "chat completions", method: GigaChatBatchMethodChatCompletions, want: string(schemas.BatchEndpointChatCompletions)},
		{name: "responses", method: GigaChatBatchMethodResponses, want: string(schemas.BatchEndpointResponses)},
		{name: "embeddings", method: GigaChatBatchMethodEmbedder, want: string(schemas.BatchEndpointEmbeddings)},
		{name: "unknown", method: GigaChatBatchMethod("unknown"), want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := toBifrostGigaChatBatchEndpoint(tt.method); got != tt.want {
				t.Fatalf("toBifrostGigaChatBatchEndpoint(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestToBifrostGigaChatBatchStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status GigaChatBatchStatus
		want   schemas.BatchStatus
	}{
		{name: "created maps to validating", status: GigaChatBatchStatusCreated, want: schemas.BatchStatusValidating},
		{name: "in progress", status: GigaChatBatchStatusInProgress, want: schemas.BatchStatusInProgress},
		{name: "completed", status: GigaChatBatchStatusCompleted, want: schemas.BatchStatusCompleted},
		{name: "unknown preserved", status: GigaChatBatchStatus("queued"), want: schemas.BatchStatus("queued")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := toBifrostGigaChatBatchStatus(tt.status); got != tt.want {
				t.Fatalf("toBifrostGigaChatBatchStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}

	extra := toBifrostGigaChatBatchProviderExtraFields(GigaChatBatch{
		Method: GigaChatBatchMethodEmbedder,
		Status: GigaChatBatchStatus("queued"),
	})
	if extra["gigachat_batch_status"] != "queued" {
		t.Fatalf("raw unknown status was not preserved: %#v", extra)
	}
	if extra["gigachat_batch_method"] != string(GigaChatBatchMethodEmbedder) {
		t.Fatalf("batch method was not preserved: %#v", extra)
	}
}

func TestConvertGigaChatBatchInputJSONL(t *testing.T) {
	t.Parallel()

	t.Run("ChatCompletions", testConvertGigaChatBatchInputJSONLChatCompletions)
	t.Run("Responses", testConvertGigaChatBatchInputJSONLResponses)
	t.Run("Embeddings", testConvertGigaChatBatchInputJSONLEmbeddings)
	t.Run("InlineRequestItems", testConvertGigaChatBatchRequestItemsToJSONL)
	t.Run("UnsupportedEndpoint", testConvertGigaChatBatchInputJSONLUnsupportedEndpoint)
}

func TestGigaChatBatchesHTTP(t *testing.T) {
	t.Parallel()

	t.Run("CreateTransformsFileRows", testGigaChatBatchCreateTransformsFileRows)
	t.Run("CreateUsesKeyBaseURLAndRefreshesTokenAfterUnauthorized", testGigaChatBatchCreateUsesKeyBaseURLAndRefreshesTokenAfterUnauthorized)
	t.Run("ListParsesWrapper", testGigaChatBatchListParsesWrapper)
	t.Run("ListParsesEmptyRootArray", testGigaChatBatchListParsesEmptyRootArray)
	t.Run("RetrieveParsesSingleObject", testGigaChatBatchRetrieveParsesSingleObject)
	t.Run("RetrieveMapsResultFileID", testGigaChatBatchRetrieveMapsResultFileID)
	t.Run("PreservesResponsesEndpoint", testGigaChatBatchPreservesResponsesEndpoint)
	t.Run("ResultsDownloadsOutputFile", testGigaChatBatchResultsDownloadsOutputFile)
	t.Run("ResultsWithoutOutputFileID", testGigaChatBatchResultsWithoutOutputFileID)
	t.Run("UnsupportedEndpoint", testGigaChatBatchCreateUnsupportedEndpoint)
	t.Run("UnsupportedCompletionWindow", testGigaChatBatchCreateUnsupportedCompletionWindow)
}

func testConvertGigaChatBatchInputJSONLChatCompletions(t *testing.T) {
	t.Parallel()

	input := []byte(`{"custom_id":"chat-1","method":"POST","url":"/v1/chat/completions","body":{"model":"GigaChat","messages":[{"role":"user","content":"Hello"}],"max_tokens":64,"temperature":0.2}}` + "\n")
	output, err := convertGigaChatBatchInputJSONL(schemas.BatchEndpointChatCompletions, input)
	if err != nil {
		t.Fatalf("convertGigaChatBatchInputJSONL returned error: %v", err)
	}

	row := decodeGigaChatBatchTestRow(t, output)
	if row.ID != "chat-1" {
		t.Fatalf("row id mismatch: got %q", row.ID)
	}
	var request GigaChatChatRequest
	if err := json.Unmarshal(row.Request, &request); err != nil {
		t.Fatalf("unmarshal GigaChat chat request: %v", err)
	}
	if request.Model != "GigaChat" || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
		t.Fatalf("chat request mismatch: %#v", request)
	}
	if request.Messages[0].Content == nil || request.Messages[0].Content.ContentStr == nil || *request.Messages[0].Content.ContentStr != "Hello" {
		t.Fatalf("chat content mismatch: %#v", request.Messages[0].Content)
	}
	if request.MaxTokens == nil || *request.MaxTokens != 64 {
		t.Fatalf("max_tokens mismatch: %#v", request.MaxTokens)
	}
	if request.Temperature == nil || *request.Temperature != 0.2 {
		t.Fatalf("temperature mismatch: %#v", request.Temperature)
	}
	if request.Stream == nil || *request.Stream {
		t.Fatalf("stream mismatch: %#v", request.Stream)
	}
}

func testConvertGigaChatBatchInputJSONLResponses(t *testing.T) {
	t.Parallel()

	input := []byte(`{"custom_id":"resp-1","method":"POST","url":"/v1/responses","body":{"model":"GigaChat-2","input":"Summarize this","instructions":"Be concise.","max_output_tokens":32}}` + "\n")
	output, err := convertGigaChatBatchInputJSONL(schemas.BatchEndpointResponses, input)
	if err != nil {
		t.Fatalf("convertGigaChatBatchInputJSONL returned error: %v", err)
	}

	row := decodeGigaChatBatchTestRow(t, output)
	if row.ID != "resp-1" {
		t.Fatalf("row id mismatch: got %q", row.ID)
	}
	var request GigaChatResponsesRequest
	if err := json.Unmarshal(row.Request, &request); err != nil {
		t.Fatalf("unmarshal GigaChat responses request: %v", err)
	}
	if request.Model != "GigaChat-2" {
		t.Fatalf("model mismatch: got %q", request.Model)
	}
	if len(request.Messages) != 2 {
		t.Fatalf("message count mismatch: got %d", len(request.Messages))
	}
	if request.Messages[0].Role != "system" || request.Messages[0].Content[0].Text == nil || *request.Messages[0].Content[0].Text != "Be concise." {
		t.Fatalf("instruction message mismatch: %#v", request.Messages[0])
	}
	if request.Messages[1].Role != "user" || request.Messages[1].Content[0].Text == nil || *request.Messages[1].Content[0].Text != "Summarize this" {
		t.Fatalf("input message mismatch: %#v", request.Messages[1])
	}
	if request.ModelOptions == nil || request.ModelOptions.MaxTokens == nil || *request.ModelOptions.MaxTokens != 32 {
		t.Fatalf("max output tokens mismatch: %#v", request.ModelOptions)
	}
}

func testConvertGigaChatBatchInputJSONLEmbeddings(t *testing.T) {
	t.Parallel()

	input := []byte(`{"custom_id":"emb-1","method":"POST","url":"/v1/embeddings","body":{"model":"Embeddings","input":["first","second"]}}` + "\n")
	output, err := convertGigaChatBatchInputJSONL(schemas.BatchEndpointEmbeddings, input)
	if err != nil {
		t.Fatalf("convertGigaChatBatchInputJSONL returned error: %v", err)
	}

	row := decodeGigaChatBatchTestRow(t, output)
	if row.ID != "emb-1" {
		t.Fatalf("row id mismatch: got %q", row.ID)
	}
	var request GigaChatEmbeddingRequest
	if err := json.Unmarshal(row.Request, &request); err != nil {
		t.Fatalf("unmarshal GigaChat embedding request: %v", err)
	}
	if request.Model != "Embeddings" {
		t.Fatalf("model mismatch: got %q", request.Model)
	}
	if request.Input == nil || len(request.Input.Texts) != 2 || request.Input.Texts[0] != "first" || request.Input.Texts[1] != "second" {
		t.Fatalf("embedding input mismatch: %#v", request.Input)
	}
}

func testConvertGigaChatBatchRequestItemsToJSONL(t *testing.T) {
	t.Parallel()

	output, err := convertGigaChatBatchRequestItemsToJSONL(schemas.BatchEndpointChatCompletions, []schemas.BatchRequestItem{{
		CustomID: "inline-1",
		Body: map[string]interface{}{
			"model": "GigaChat",
			"messages": []map[string]string{
				{"role": "user", "content": "Hello from inline"},
			},
		},
	}})
	if err != nil {
		t.Fatalf("convertGigaChatBatchRequestItemsToJSONL returned error: %v", err)
	}

	row := decodeGigaChatBatchTestRow(t, output)
	if row.ID != "inline-1" {
		t.Fatalf("row id mismatch: got %q", row.ID)
	}
	var request GigaChatChatRequest
	if err := json.Unmarshal(row.Request, &request); err != nil {
		t.Fatalf("unmarshal GigaChat chat request: %v", err)
	}
	if len(request.Messages) != 1 || request.Messages[0].Content == nil || request.Messages[0].Content.ContentStr == nil || *request.Messages[0].Content.ContentStr != "Hello from inline" {
		t.Fatalf("inline request mismatch: %#v", request)
	}
}

func testConvertGigaChatBatchInputJSONLUnsupportedEndpoint(t *testing.T) {
	t.Parallel()

	input := []byte(`{"custom_id":"bad-1","method":"POST","url":"/v1/completions","body":{"model":"GigaChat","prompt":"Hello"}}` + "\n")
	_, err := convertGigaChatBatchInputJSONL(schemas.BatchEndpointCompletions, input)
	if err == nil {
		t.Fatal("expected unsupported endpoint error, got nil")
	}
	if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "do not support endpoint") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatBatchCreateTransformsFileRows(t *testing.T) {
	t.Parallel()

	inputJSONL := []byte(`{"custom_id":"chat-1","method":"POST","url":"/v1/chat/completions","body":{"model":"GigaChat","messages":[{"role":"user","content":"Hello"}]}}` + "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer batch-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}

		switch request.URL.Path {
		case "/v1/files/input-file/content":
			if request.Method != http.MethodGet {
				t.Fatalf("file content method mismatch: got %s", request.Method)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(inputJSONL)
		case "/v1/batches":
			if request.Method != http.MethodPost {
				t.Fatalf("batch create method mismatch: got %s", request.Method)
			}
			if got := request.URL.Query().Get("method"); got != string(GigaChatBatchMethodChatCompletions) {
				t.Fatalf("method query mismatch: got %q", got)
			}
			if got := request.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Fatalf("content type mismatch: got %q", got)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("ReadAll returned error: %v", err)
			}
			if !bytes.HasSuffix(body, []byte("\n")) {
				t.Fatalf("batch body must be JSONL with trailing newline, got %q", string(body))
			}
			row := decodeGigaChatBatchTestRow(t, body)
			if row.ID != "chat-1" {
				t.Fatalf("row id mismatch: got %q", row.ID)
			}
			var chatRequest GigaChatChatRequest
			if err := json.Unmarshal(row.Request, &chatRequest); err != nil {
				t.Fatalf("unmarshal GigaChat batch request: %v", err)
			}
			if chatRequest.Model != "GigaChat" || len(chatRequest.Messages) != 1 {
				t.Fatalf("unexpected chat request: %#v", chatRequest)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "batch-create-request-id")
			_, _ = w.Write([]byte(`{"id":"batch-1","object":"batch","method":"chat_completions","status":"created","input_file_id":"input-file","completion_window":"24h","created_at":1780306293,"request_counts":{"total":1}}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchCreate(testBifrostContext(), testGigaChatAccessTokenKey("batch-token"), &schemas.BifrostBatchCreateRequest{
		Provider:         schemas.GigaChat,
		InputFileID:      "input-file",
		Endpoint:         schemas.BatchEndpointChatCompletions,
		CompletionWindow: "24h",
	})
	if bifrostErr != nil {
		t.Fatalf("BatchCreate returned error: %v", bifrostErr)
	}
	if response.ID != "batch-1" || response.Status != schemas.BatchStatusValidating {
		t.Fatalf("unexpected create response: %#v", response)
	}
	if response.Endpoint != string(schemas.BatchEndpointChatCompletions) || response.InputFileID != "input-file" || response.CompletionWindow != "24h" {
		t.Fatalf("unexpected create response metadata: %#v", response)
	}
	if response.RequestCounts.Total != 1 {
		t.Fatalf("request counts mismatch: %#v", response.RequestCounts)
	}
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q", response.ExtraFields.Provider)
	}
	requestID := ""
	for key, value := range response.ExtraFields.ProviderResponseHeaders {
		if strings.EqualFold(key, "x-request-id") {
			requestID = value
			break
		}
	}
	if requestID != "batch-create-request-id" {
		t.Fatalf("provider headers mismatch: %#v", response.ExtraFields.ProviderResponseHeaders)
	}
}

func testGigaChatBatchCreateUsesKeyBaseURLAndRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	networkServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		t.Fatalf("network base_url server should not be used, got %s", request.URL.Path)
	}))
	defer networkServer.Close()

	var tokenRequests atomic.Int32
	var batchRequests atomic.Int32
	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"batch-token-` + formatInt32(tokenIndex) + `","expires_at":1893456000}`))
		case "/custom-api/v1/batches":
			batchIndex := batchRequests.Add(1)
			wantAuthorization := "Bearer batch-token-" + formatInt32(batchIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", batchIndex, got, wantAuthorization)
			}
			if got := request.Header.Get(gigaChatUserAgentHeader); got != gigaChatUserAgent {
				t.Fatalf("user-agent mismatch: got %q", got)
			}
			if request.Method != http.MethodPost {
				t.Fatalf("method mismatch: got %s, want POST", request.Method)
			}
			if got := request.URL.Query().Get("method"); got != string(GigaChatBatchMethodChatCompletions) {
				t.Fatalf("method query mismatch: got %q", got)
			}
			if got := request.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Fatalf("content type mismatch: got %q", got)
			}
			if batchIndex == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}

			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("ReadAll returned error: %v", err)
			}
			row := decodeGigaChatBatchTestRow(t, body)
			if row.ID != "inline-1" {
				t.Fatalf("row id mismatch: got %q", row.ID)
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"batch-refreshed","object":"batch","method":"chat_completions","status":"created","completion_window":"24h","request_counts":{"total":1}}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer keyServer.Close()

	provider := newTestGigaChatChatProvider(t, networkServer.URL)
	key := schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			Credentials: schemas.NewSecretVar("test-credentials"),
			AuthURL:     keyServer.URL + "/oauth",
			BaseURL:     keyServer.URL + "/custom-api",
		},
	}

	response, bifrostErr := provider.BatchCreate(testBifrostContext(), key, &schemas.BifrostBatchCreateRequest{
		Provider:         schemas.GigaChat,
		Endpoint:         schemas.BatchEndpointChatCompletions,
		CompletionWindow: "24h",
		Requests: []schemas.BatchRequestItem{{
			CustomID: "inline-1",
			Body: map[string]interface{}{
				"model": "GigaChat",
				"messages": []map[string]string{
					{"role": "user", "content": "Hello"},
				},
			},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("BatchCreate returned error: %v", bifrostErr)
	}
	if response.ID != "batch-refreshed" || response.Status != schemas.BatchStatusValidating {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if batchRequests.Load() != 2 {
		t.Fatalf("batch request count mismatch: got %d, want 2", batchRequests.Load())
	}
}

func testGigaChatBatchListParsesWrapper(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/batches" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s", request.Method)
		}
		if request.URL.RawQuery != "" {
			t.Fatalf("unexpected query: %s", request.URL.RawQuery)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer batch-list-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"batch-1","object":"batch","method":"chat_completions","status":"in_progress","created_at":1780306293,"request_counts":{"total":2,"completed":1}},{"id":"batch-2","object":"batch","method":"embedder","status":"completed","created_at":1780306294,"request_counts":{"total":1,"completed":1}}]}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchList(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("batch-list-token")}, &schemas.BifrostBatchListRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("BatchList returned error: %v", bifrostErr)
	}
	if response.Object != "list" || len(response.Data) != 2 {
		t.Fatalf("unexpected list response: %#v", response)
	}
	if response.Data[0].Status != schemas.BatchStatusInProgress || response.Data[0].Endpoint != string(schemas.BatchEndpointChatCompletions) {
		t.Fatalf("first batch mismatch: %#v", response.Data[0])
	}
	if response.Data[1].Status != schemas.BatchStatusCompleted || response.Data[1].Endpoint != string(schemas.BatchEndpointEmbeddings) {
		t.Fatalf("second batch mismatch: %#v", response.Data[1])
	}
	if response.FirstID == nil || *response.FirstID != "batch-1" || response.LastID == nil || *response.LastID != "batch-2" {
		t.Fatalf("list ids mismatch: first=%v last=%v", response.FirstID, response.LastID)
	}
}

func testGigaChatBatchListParsesEmptyRootArray(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/batches" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s", request.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchList(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("batch-list-token")}, &schemas.BifrostBatchListRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("BatchList returned error: %v", bifrostErr)
	}
	if response.Object != "list" || len(response.Data) != 0 {
		t.Fatalf("unexpected list response: %#v", response)
	}
	if response.FirstID != nil || response.LastID != nil {
		t.Fatalf("empty list should not set cursors: first=%v last=%v", response.FirstID, response.LastID)
	}
}

func testGigaChatBatchRetrieveParsesSingleObject(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/batches" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s", request.Method)
		}
		if got := request.URL.Query().Get("batch_id"); got != "batch-1" {
			t.Fatalf("batch_id query mismatch: got %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer batch-retrieve-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-1","object":"batch","method":"embedder","status":"completed","created_at":1780306293,"completed_at":1780306393,"output_file_id":"output-file","error_file_id":"error-file","request_counts":{"total":3,"completed":2,"failed":1}}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchRetrieve(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("batch-retrieve-token")}, &schemas.BifrostBatchRetrieveRequest{
		Provider: schemas.GigaChat,
		BatchID:  "batch-1",
	})
	if bifrostErr != nil {
		t.Fatalf("BatchRetrieve returned error: %v", bifrostErr)
	}
	if response.ID != "batch-1" || response.Status != schemas.BatchStatusCompleted || response.Endpoint != string(schemas.BatchEndpointEmbeddings) {
		t.Fatalf("unexpected retrieve response: %#v", response)
	}
	if response.OutputFileID == nil || *response.OutputFileID != "output-file" || response.ErrorFileID == nil || *response.ErrorFileID != "error-file" {
		t.Fatalf("file ids mismatch: output=%v error=%v", response.OutputFileID, response.ErrorFileID)
	}
	if response.RequestCounts.Total != 3 || response.RequestCounts.Completed != 2 || response.RequestCounts.Failed != 1 {
		t.Fatalf("request counts mismatch: %#v", response.RequestCounts)
	}
}

func testGigaChatBatchRetrieveMapsResultFileID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/batches" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s", request.Method)
		}
		if got := request.URL.Query().Get("batch_id"); got != "batch-1" {
			t.Fatalf("batch_id query mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-1","object":"batch","method":"chat_completions","status":"completed","result_file_id":"result-file","request_counts":{"total":1,"completed":1}}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchRetrieve(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("batch-token")}, &schemas.BifrostBatchRetrieveRequest{
		Provider: schemas.GigaChat,
		BatchID:  "batch-1",
	})
	if bifrostErr != nil {
		t.Fatalf("BatchRetrieve returned error: %v", bifrostErr)
	}
	if response.OutputFileID == nil || *response.OutputFileID != "result-file" {
		t.Fatalf("result_file_id was not mapped to output_file_id: %#v", response.OutputFileID)
	}
	if response.ProviderExtraFields["gigachat_result_file_id"] != "result-file" {
		t.Fatalf("result_file_id was not preserved in provider extra fields: %#v", response.ProviderExtraFields)
	}
}

func testGigaChatBatchPreservesResponsesEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/batches" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer batch-responses-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")

		switch request.Method {
		case http.MethodPost:
			if got := request.URL.Query().Get("method"); got != string(GigaChatBatchMethodResponses) {
				t.Fatalf("method query mismatch: got %q, want %q", got, GigaChatBatchMethodResponses)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("ReadAll returned error: %v", err)
			}
			row := decodeGigaChatBatchTestRow(t, body)
			if row.ID != "responses-1" {
				t.Fatalf("row id mismatch: got %q", row.ID)
			}
			_, _ = w.Write([]byte(`{"id":"batch-responses","object":"batch","method":"responses","status":"created","completion_window":"24h","request_counts":{"total":1}}`))
		case http.MethodGet:
			if got := request.URL.Query().Get("batch_id"); got == "" {
				_, _ = w.Write([]byte(`{"data":[{"id":"batch-responses","object":"batch","method":"responses","status":"in_progress","created_at":1780306293,"request_counts":{"total":1}}]}`))
			} else if got == "batch-responses" {
				_, _ = w.Write([]byte(`{"id":"batch-responses","object":"batch","method":"responses","status":"completed","created_at":1780306293,"request_counts":{"total":1,"completed":1}}`))
			} else {
				t.Fatalf("batch_id query mismatch: got %q", got)
			}
		default:
			t.Fatalf("method mismatch: got %s", request.Method)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	key := testGigaChatAccessTokenKey("batch-responses-token")
	createResponse, bifrostErr := provider.BatchCreate(testBifrostContext(), key, &schemas.BifrostBatchCreateRequest{
		Provider:         schemas.GigaChat,
		Endpoint:         schemas.BatchEndpointResponses,
		CompletionWindow: "24h",
		Requests: []schemas.BatchRequestItem{{
			CustomID: "responses-1",
			Body: map[string]interface{}{
				"model": "GigaChat-2",
				"input": "Summarize this",
			},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("BatchCreate returned error: %v", bifrostErr)
	}
	if createResponse.Endpoint != string(schemas.BatchEndpointResponses) {
		t.Fatalf("create endpoint mismatch: got %q", createResponse.Endpoint)
	}

	listResponse, bifrostErr := provider.BatchList(testBifrostContext(), []schemas.Key{key}, &schemas.BifrostBatchListRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("BatchList returned error: %v", bifrostErr)
	}
	if len(listResponse.Data) != 1 || listResponse.Data[0].Endpoint != string(schemas.BatchEndpointResponses) {
		t.Fatalf("list did not preserve responses endpoint: %#v", listResponse.Data)
	}

	retrieveResponse, bifrostErr := provider.BatchRetrieve(testBifrostContext(), []schemas.Key{key}, &schemas.BifrostBatchRetrieveRequest{
		Provider: schemas.GigaChat,
		BatchID:  "batch-responses",
	})
	if bifrostErr != nil {
		t.Fatalf("BatchRetrieve returned error: %v", bifrostErr)
	}
	if retrieveResponse.Endpoint != string(schemas.BatchEndpointResponses) {
		t.Fatalf("retrieve endpoint mismatch: got %q", retrieveResponse.Endpoint)
	}
}

func testGigaChatBatchResultsDownloadsOutputFile(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer batch-results-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		switch request.URL.Path {
		case "/v1/batches":
			if request.Method != http.MethodGet {
				t.Fatalf("method mismatch: got %s", request.Method)
			}
			if got := request.URL.Query().Get("batch_id"); got != "batch-1" {
				t.Fatalf("batch_id query mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"batch-1","object":"batch","method":"chat_completions","status":"completed","result_file_id":"result-file","request_counts":{"total":1,"completed":1}}`))
		case "/v1/files/result-file/content":
			if request.Method != http.MethodGet {
				t.Fatalf("method mismatch: got %s", request.Method)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte(`{"id":"row-1","response":{"status_code":200,"request_id":"req-1","body":{"id":"chatcmpl-1","object":"chat.completion"}}}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchResults(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("batch-results-token")}, &schemas.BifrostBatchResultsRequest{
		Provider: schemas.GigaChat,
		BatchID:  "batch-1",
	})
	if bifrostErr != nil {
		t.Fatalf("BatchResults returned error: %v", bifrostErr)
	}
	if response.BatchID != "batch-1" || len(response.Results) != 1 {
		t.Fatalf("unexpected batch results response: %#v", response)
	}
	result := response.Results[0]
	if result.CustomID != "row-1" || result.Response == nil || result.Response.StatusCode != 200 || result.Response.RequestID != "req-1" {
		t.Fatalf("unexpected result item: %#v", result)
	}
	if response.ProviderExtraFields["gigachat_result_file_id"] != "result-file" || response.ProviderExtraFields["gigachat_batch_output_file_id"] != "result-file" {
		t.Fatalf("provider extra fields mismatch: %#v", response.ProviderExtraFields)
	}
}

func testGigaChatBatchResultsWithoutOutputFileID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/batches" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"batch-1","object":"batch","method":"chat_completions","status":"in_progress","request_counts":{"total":1}}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.BatchResults(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("batch-results-token")}, &schemas.BifrostBatchResultsRequest{
		Provider: schemas.GigaChat,
		BatchID:  "batch-1",
	})
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil || !strings.Contains(bifrostErr.Error.Message, "did not return output_file_id or result_file_id") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
}

func testGigaChatBatchCreateUnsupportedEndpoint(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	response, bifrostErr := provider.BatchCreate(testBifrostContext(), testGigaChatAccessTokenKey("batch-token"), &schemas.BifrostBatchCreateRequest{
		Provider: schemas.GigaChat,
		Endpoint: schemas.BatchEndpointCompletions,
		Requests: []schemas.BatchRequestItem{{
			CustomID: "bad-1",
			Body: map[string]interface{}{
				"model":  "GigaChat",
				"prompt": "Hello",
			},
		}},
	})
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil || !strings.Contains(bifrostErr.Error.Message, "do not support endpoint") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
}

func testGigaChatBatchCreateUnsupportedCompletionWindow(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}

	response, bifrostErr := provider.BatchCreate(testBifrostContext(), testGigaChatAccessTokenKey("batch-token"), &schemas.BifrostBatchCreateRequest{
		Provider:         schemas.GigaChat,
		Endpoint:         schemas.BatchEndpointChatCompletions,
		CompletionWindow: "1h",
		Requests: []schemas.BatchRequestItem{{
			CustomID: "chat-1",
			Body: map[string]interface{}{
				"model": "GigaChat",
				"messages": []map[string]string{
					{"role": "user", "content": "Hello"},
				},
			},
		}},
	})
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil || !strings.Contains(bifrostErr.Error.Message, "completion_window=24h only") {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
}

func decodeGigaChatBatchTestRow(t *testing.T, output []byte) GigaChatBatchInputRow {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d JSONL lines, want 1: %q", len(lines), string(output))
	}
	var row GigaChatBatchInputRow
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("unmarshal GigaChat batch row: %v", err)
	}
	if len(row.Request) == 0 {
		t.Fatalf("row request is empty: %#v", row)
	}
	return row
}
