package wafer_test

import (
	"bytes"
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
	"github.com/maximhq/bifrost/core/providers/wafer"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestWafer(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("WAFER_API_KEY")) == "" {
		t.Skip("Skipping Wafer tests because WAFER_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// NOTE: model IDs below follow the names surfaced in Wafer's docs (GLM-5.2,
	// Kimi-K2.6); confirm the exact strings against a live GET /v1/models call.
	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Wafer,
		ChatModel: "glm-5.2",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Wafer, Model: "glm-5.2"},
			{Provider: schemas.Wafer, Model: "kimi-k2.6"},
		},
		TextModel:      "glm-5.2",
		EmbeddingModel: "", // Wafer doesn't support embedding
		ReasoningModel: "glm-5.2",
		VisionModel:    "Kimi-K2.6",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        true,
			TextCompletionStream:  true,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             false,
			ListModels:            true,
			Reasoning:             true,
		},
	}

	t.Run("WaferTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// TestWaferFileUpload verifies the upload wire format against a stub upstream.
// Wafer takes raw bytes with metadata in headers, so this guards against a
// regression to OpenAI-style multipart encoding, and covers the response mapping
// (ISO 8601 timestamps and Wafer's own status vocabulary).
func TestWaferFileUpload(t *testing.T) {
	t.Parallel()

	fileBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}

	var mu sync.Mutex
	var gotMethod, gotPath, gotContentType, gotPurpose, gotFilename, gotAuth string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotPurpose = r.Header.Get("X-Wafer-Purpose")
		gotFilename = r.Header.Get("X-Wafer-Filename")
		gotAuth = r.Header.Get("Authorization")
		gotBody = body
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "file_8a3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e",
			"purpose": "vision",
			"filename": "screenshot.png",
			"mime_type": "image/png",
			"bytes": 10,
			"status": "ready",
			"created_at": "2026-07-17T10:00:00Z",
			"expires_at": "2026-08-16T10:00:00Z"
		}`))
	}))
	defer server.Close()

	provider, err := wafer.NewWaferProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/v1",
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewWaferProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}
	contentType := "image/png"

	response, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
		Provider:    schemas.Wafer,
		File:        fileBytes,
		Filename:    "screenshot.png",
		Purpose:     schemas.FilePurposeVision,
		ContentType: &contentType,
	})
	if bifrostErr != nil {
		t.Fatalf("FileUpload returned an error: %v", bifrostErr)
	}

	mu.Lock()
	defer mu.Unlock()

	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/v1/files" {
		t.Errorf("path: got %q, want %q", gotPath, "/v1/files")
	}
	if !bytes.Equal(gotBody, fileBytes) {
		t.Errorf("body: got %v, want the raw file bytes %v (multipart encoding would not match)", gotBody, fileBytes)
	}
	if gotContentType != "image/png" {
		t.Errorf("Content-Type: got %q, want %q", gotContentType, "image/png")
	}
	if gotPurpose != "vision" {
		t.Errorf("X-Wafer-Purpose: got %q, want %q", gotPurpose, "vision")
	}
	if gotFilename != "screenshot.png" {
		t.Errorf("X-Wafer-Filename: got %q, want %q", gotFilename, "screenshot.png")
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer test-key")
	}

	if response.ID != "file_8a3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e" {
		t.Errorf("ID: got %q", response.ID)
	}
	if response.Object != "file" {
		t.Errorf("Object: got %q, want %q", response.Object, "file")
	}
	if response.Bytes != 10 {
		t.Errorf("Bytes: got %d, want 10", response.Bytes)
	}
	if response.Purpose != schemas.FilePurposeVision {
		t.Errorf("Purpose: got %q, want %q", response.Purpose, schemas.FilePurposeVision)
	}
	if response.Status != schemas.FileStatusProcessed {
		t.Errorf("Status: got %q, want %q (Wafer's \"ready\")", response.Status, schemas.FileStatusProcessed)
	}
	if wantCreatedAt := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC).Unix(); response.CreatedAt != wantCreatedAt {
		t.Errorf("CreatedAt: got %d, want %d", response.CreatedAt, wantCreatedAt)
	}
	if response.ExpiresAt == nil {
		t.Error("ExpiresAt: got nil, want the parsed expiry")
	} else if wantExpiresAt := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC).Unix(); *response.ExpiresAt != wantExpiresAt {
		t.Errorf("ExpiresAt: got %d, want %d", *response.ExpiresAt, wantExpiresAt)
	}
}

// TestWaferFileUploadRequiredFields verifies the fields Wafer requires on every
// upload are rejected locally rather than defaulted onto the wire.
func TestWaferFileUploadRequiredFields(t *testing.T) {
	t.Parallel()

	contentType := "image/png"
	emptyContentType := "  "

	tests := []struct {
		name    string
		request *schemas.BifrostFileUploadRequest
	}{
		{"missing file", &schemas.BifrostFileUploadRequest{Purpose: schemas.FilePurposeVision, ContentType: &contentType}},
		{"missing purpose", &schemas.BifrostFileUploadRequest{File: []byte("x"), ContentType: &contentType}},
		{"missing content type", &schemas.BifrostFileUploadRequest{File: []byte("x"), Purpose: schemas.FilePurposeVision}},
		{"blank content type", &schemas.BifrostFileUploadRequest{File: []byte("x"), Purpose: schemas.FilePurposeVision, ContentType: &emptyContentType}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream was called; the request should have been rejected locally")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, err := wafer.NewWaferProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/v1",
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewWaferProvider: %v", err)
	}

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if _, bifrostErr := provider.FileUpload(ctx, key, tt.request); bifrostErr == nil {
				t.Fatal("FileUpload: got nil error, want a validation error")
			}
		})
	}
}

// TestWaferChatImageFileID covers referencing an uploaded file by ID in a chat
// request. Wafer nests the file_id inside image_url (not url), so this checks both
// directions: an inbound image_url.file_id survives the parse, and it reaches the
// upstream as {"image_url":{"file_id":...}} with no url key.
func TestWaferChatImageFileID(t *testing.T) {
	t.Parallel()

	const fileID = "file_8a3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e"

	// Inbound: a client posting the OpenAI-compatible file reference must parse
	// into ChatInputImage.FileID rather than being dropped.
	var block schemas.ChatContentBlock
	if err := json.Unmarshal([]byte(`{"type":"image_url","image_url":{"file_id":"`+fileID+`"}}`), &block); err != nil {
		t.Fatalf("unmarshal image_url block: %v", err)
	}
	if block.ImageURLStruct == nil || block.ImageURLStruct.FileID == nil {
		t.Fatalf("inbound parse dropped file_id: %+v", block.ImageURLStruct)
	}
	if got := *block.ImageURLStruct.FileID; got != fileID {
		t.Fatalf("inbound file_id: got %q, want %q", got, fileID)
	}

	// Outbound: the file_id must reach Wafer inside image_url, and no url key.
	var mu sync.Mutex
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "glm-5.2",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer server.Close()

	provider, err := wafer.NewWaferProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/v1",
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewWaferProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	key := schemas.Key{Models: []string{"*"}, Value: *schemas.NewSecretVar("test-key")}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Wafer,
		Model:    "glm-5.2",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type:           schemas.ChatContentBlockTypeImage,
					ImageURLStruct: &schemas.ChatInputImage{FileID: schemas.Ptr(fileID)},
				}},
			},
		}},
	}

	if _, bifrostErr := provider.ChatCompletion(ctx, key, request); bifrostErr != nil {
		t.Fatalf("ChatCompletion returned an error: %v", bifrostErr)
	}

	mu.Lock()
	defer mu.Unlock()

	// Navigate to messages[0].content[0].image_url and inspect its keys directly,
	// so the check is independent of key ordering and whitespace on the wire.
	var wire struct {
		Messages []struct {
			Content []struct {
				ImageURL map[string]json.RawMessage `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("unmarshal outbound body: %v; got: %s", err, gotBody)
	}
	if len(wire.Messages) != 1 || len(wire.Messages[0].Content) != 1 {
		t.Fatalf("unexpected outbound message shape; got: %s", gotBody)
	}

	imageURL := wire.Messages[0].Content[0].ImageURL
	var gotFileID string
	if raw, ok := imageURL["file_id"]; ok {
		_ = json.Unmarshal(raw, &gotFileID)
	}
	if gotFileID != fileID {
		t.Errorf("outbound image_url.file_id: got %q, want %q; body: %s", gotFileID, fileID, gotBody)
	}
	// The image carries no URL, so no url key should be emitted alongside file_id.
	if _, ok := imageURL["url"]; ok {
		t.Errorf("outbound image_url emitted a url key for a file_id-only image; body: %s", gotBody)
	}
}
