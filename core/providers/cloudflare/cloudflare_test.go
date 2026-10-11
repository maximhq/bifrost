package cloudflare_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/cloudflare"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestCloudflare(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")) == "" || strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")) == "" {
		t.Skip("Skipping Cloudflare tests because CLOUDFLARE_API_TOKEN or CLOUDFLARE_ACCOUNT_ID is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Cloudflare,
		ChatModel:      "@cf/meta/llama-4-scout-17b-16e-instruct",
		TextModel:      "", // Cloudflare's OpenAI-compatible API has no text completions
		EmbeddingModel: "@cf/baai/bge-large-en-v1.5",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             true,
			ListModels:            true,
		},
	}

	t.Run("CloudflareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

const chatResponseBody = `{"id":"id-1","object":"chat.completion","created":1,"model":"@cf/meta/llama-4-scout-17b-16e-instruct","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

func newTestProvider(t *testing.T, baseURL string) *cloudflare.CloudflareProvider {
	t.Helper()
	provider, err := cloudflare.NewCloudflareProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewCloudflareProvider: %v", err)
	}
	return provider
}

func newTestKey(accountID string) schemas.Key {
	return schemas.Key{
		Value:               schemas.SecretVar{Val: "test-token"},
		Models:              []string{"*"},
		CloudflareKeyConfig: &schemas.CloudflareKeyConfig{AccountID: schemas.SecretVar{Val: accountID}},
	}
}

func newChatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Cloudflare,
		Model:    "@cf/meta/llama-4-scout-17b-16e-instruct",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func TestChatCompletion_UsesKeyAccountURL(t *testing.T) {
	var capturedPath, capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, chatResponseBody)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.ChatCompletion(ctx, newTestKey("acct-1"), newChatRequest()); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/accounts/acct-1/ai/v1/chat/completions" {
		t.Errorf("path = %q, want /accounts/acct-1/ai/v1/chat/completions", capturedPath)
	}
	if capturedAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", capturedAuth)
	}
}

func TestEmbedding_UsesKeyAccountURL(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"@cf/baai/bge-large-en-v1.5","usage":{"prompt_tokens":1,"total_tokens":1}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.Cloudflare,
		Model:    "@cf/baai/bge-large-en-v1.5",
		Input:    schemas.EmbeddingInput{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("hi")}}}},
	}
	if _, bifrostErr := provider.Embedding(ctx, newTestKey("acct-2"), request); bifrostErr != nil {
		t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
	}

	if capturedPath != "/accounts/acct-2/ai/v1/embeddings" {
		t.Errorf("path = %q, want /accounts/acct-2/ai/v1/embeddings", capturedPath)
	}
}

func TestChatCompletion_MissingAccountIDIsConfigError(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-token"}}

	_, bifrostErr := provider.ChatCompletion(ctx, key, newChatRequest())
	if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "cloudflare_key_config.account_id") {
		t.Fatalf("expected a missing account ID error, got %+v", bifrostErr)
	}
}

func TestChatCompletion_RejectsAccountIDThatIsNotOnePathSegment(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	for _, accountID := range []string{"acct-1/../other", "acct-1?x=1", ".."} {
		_, bifrostErr := provider.ChatCompletion(ctx, newTestKey(accountID), newChatRequest())
		if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "invalid cloudflare_key_config.account_id") {
			t.Errorf("account ID %q: expected an invalid account ID error, got %+v", accountID, bifrostErr)
		}
	}
}

func TestChatCompletion_ParsesErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"errors":[{"message":"AiError: No such model: No such model @cf/meta/nope or task (req-1)","code":5007},{"code":"invalid_type","message":"Expected array, received string"}],"success":false,"result":{},"messages":[]}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.ChatCompletion(ctx, newTestKey("acct-1"), newChatRequest())
	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatal("expected an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", bifrostErr.StatusCode)
	}
	want := "AiError: No such model: No such model @cf/meta/nope or task (req-1); Expected array, received string"
	if bifrostErr.Error.Message != want {
		t.Errorf("message = %q, want %q", bifrostErr.Error.Message, want)
	}
}

func TestListModels_PagesAndFiltersTasks(t *testing.T) {
	// A full first page forces a second request; the second page carries the
	// models that must be filtered out.
	firstPage := make([]string, 0, 100)
	for i := range 100 {
		firstPage = append(firstPage, fmt.Sprintf(`{"name":"@cf/test/chat-%d","task":{"name":"Text Generation"}}`, i))
	}
	secondPage := []string{
		`{"name":"@cf/baai/bge-large-en-v1.5","description":"embeddings","task":{"name":"Text Embeddings"}}`,
		`{"name":"@cf/cloudflare/clef-flash","task":{"name":"Text Generation"}}`,
		`{"name":"@cf/black-forest-labs/flux-1-schnell","task":{"name":"Text-to-Image"}}`,
	}

	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acct-1/ai/models/search" {
			t.Errorf("path = %q, want /accounts/acct-1/ai/models/search", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		result := firstPage
		if page == "2" {
			result = secondPage
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"success":true,"errors":[],"result":[%s]}`, strings.Join(result, ","))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{newTestKey("acct-1")}, &schemas.BifrostListModelsRequest{Provider: schemas.Cloudflare})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}

	if strings.Join(pages, ",") != "1,2" {
		t.Errorf("pages requested = %v, want [1 2]", pages)
	}
	if len(resp.Data) != 101 {
		t.Fatalf("models = %d, want 101 (100 chat + 1 embedding)", len(resp.Data))
	}
	byID := make(map[string]schemas.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	if _, ok := byID["cloudflare/@cf/baai/bge-large-en-v1.5"]; !ok {
		t.Fatal("embedding model missing")
	}
	for _, excluded := range []string{"cloudflare/@cf/cloudflare/clef-flash", "cloudflare/@cf/black-forest-labs/flux-1-schnell"} {
		if _, ok := byID[excluded]; ok {
			t.Errorf("%s must not be listed", excluded)
		}
	}
}

// TestListModels_DecodesCompressedPages pins that a gzip-encoded model page is decoded
// before it is parsed, as the other response paths do.
func TestListModels_DecodesCompressedPages(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[{"name":"@cf/baai/bge-large-en-v1.5","task":{"name":"Text Embeddings"}}]}`))
	_ = writer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{newTestKey("acct-1")}, &schemas.BifrostListModelsRequest{Provider: schemas.Cloudflare})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %v", bifrostErr.Error.Message)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "cloudflare/@cf/baai/bge-large-en-v1.5" {
		t.Fatalf("models = %+v, want the one embedding model", resp.Data)
	}
}
