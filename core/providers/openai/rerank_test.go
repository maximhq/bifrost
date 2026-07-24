package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

type testNoopLogger struct{}

func (testNoopLogger) Debug(string, ...any)                   {}
func (testNoopLogger) Info(string, ...any)                    {}
func (testNoopLogger) Warn(string, ...any)                    {}
func (testNoopLogger) Error(string, ...any)                   {}
func (testNoopLogger) Fatal(string, ...any)                   {}
func (testNoopLogger) SetLevel(schemas.LogLevel)              {}
func (testNoopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testNoopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestCustomOpenAIProviderRerankUsesGenericEndpoint(t *testing.T) {
	var upstreamCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/rerank" {
			t.Fatalf("expected /v1/rerank, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload struct {
			Model           string                   `json:"model"`
			Query           string                   `json:"query"`
			Documents       []schemas.RerankDocument `json:"documents"`
			TopN            *int                     `json:"top_n"`
			MaxTokensPerDoc *int                     `json:"max_tokens_per_doc"`
			ReturnDocuments *bool                    `json:"return_documents"`
			Truncate        string                   `json:"truncate"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to parse request body %s: %v", string(body), err)
		}
		if payload.Model != "zerank-2" {
			t.Fatalf("expected model zerank-2, got %q", payload.Model)
		}
		if payload.Query != "What is Bifrost?" {
			t.Fatalf("expected query to be forwarded, got %q", payload.Query)
		}
		if len(payload.Documents) != 2 || payload.Documents[0].Text != "Bifrost is an AI gateway." || payload.Documents[1].Text != "Paris is in France." {
			t.Fatalf("expected documents to be forwarded as document objects, got %#v", payload.Documents)
		}
		if payload.TopN == nil || *payload.TopN != 2 {
			t.Fatalf("expected top_n=2, got %#v", payload.TopN)
		}
		if payload.MaxTokensPerDoc == nil || *payload.MaxTokensPerDoc != 512 {
			t.Fatalf("expected max_tokens_per_doc=512, got %#v", payload.MaxTokensPerDoc)
		}
		if payload.ReturnDocuments != nil {
			t.Fatalf("return_documents is handled by Bifrost and should not be sent upstream")
		}
		if payload.Truncate != "END" {
			t.Fatalf("expected passthrough extra param truncate=END, got %q", payload.Truncate)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Test-Header", "present")
		_, _ = w.Write([]byte(`{
			"id":"rr-1",
			"results":[
				{"index":0,"relevance_score":0.2},
				{"index":1,"relevance_score":0.9}
			],
			"meta":{"tokens":{"input_tokens":11,"output_tokens":0}}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	topN := 2
	maxTokensPerDoc := 512
	returnDocuments := true
	response, bifrostErr := provider.Rerank(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostRerankRequest{
		Model: "zerank-2",
		Query: "What is Bifrost?",
		Documents: []schemas.RerankDocument{
			{Text: "Bifrost is an AI gateway."},
			{Text: "Paris is in France."},
		},
		Params: &schemas.RerankParameters{
			TopN:            &topN,
			MaxTokensPerDoc: &maxTokensPerDoc,
			ReturnDocuments: &returnDocuments,
			ExtraParams: map[string]interface{}{
				"truncate": "END",
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("expected rerank to succeed, got %v", bifrostErr)
	}
	if !upstreamCalled {
		t.Fatal("expected upstream rerank endpoint to be called")
	}
	if response == nil {
		t.Fatal("expected rerank response")
	}
	if response.ID != "rr-1" {
		t.Fatalf("expected response id rr-1, got %q", response.ID)
	}
	if response.Model != "zerank-2" {
		t.Fatalf("expected response model zerank-2, got %q", response.Model)
	}
	if len(response.Results) != 2 {
		t.Fatalf("expected two rerank results, got %#v", response.Results)
	}
	if response.Results[0].Index != 1 || response.Results[0].RelevanceScore != 0.9 {
		t.Fatalf("expected results sorted by relevance, got %#v", response.Results)
	}
	if response.Results[0].Document == nil || response.Results[0].Document.Text != "Paris is in France." {
		t.Fatalf("expected return_documents to backfill request document, got %#v", response.Results[0].Document)
	}
	if response.Usage == nil || response.Usage.PromptTokens != 11 || response.Usage.TotalTokens != 11 {
		t.Fatalf("expected usage from rerank meta tokens, got %#v", response.Usage)
	}
	if response.ExtraFields.ProviderResponseHeaders["X-Test-Header"] != "present" {
		t.Fatalf("expected provider response headers, got %#v", response.ExtraFields.ProviderResponseHeaders)
	}
}

func TestCustomOpenAIRerankPreservesUpstreamDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"rr-doc",
			"results":[
				{"index":0,"relevance_score":0.1},
				{"index":1,"relevance_score":0.8,"document":{"text":"upstream returned B"}}
			]
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	returnDocuments := true
	response, bifrostErr := provider.Rerank(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostRerankRequest{
		Model: "zerank-2",
		Query: "What is Bifrost?",
		Documents: []schemas.RerankDocument{
			{Text: "request doc A"},
			{Text: "request doc B"},
		},
		Params: &schemas.RerankParameters{
			ReturnDocuments: &returnDocuments,
		},
	})
	if bifrostErr != nil {
		t.Fatalf("expected rerank to succeed, got %v", bifrostErr)
	}
	if response == nil || len(response.Results) != 2 {
		t.Fatalf("expected two rerank results, got %#v", response)
	}
	// Highest score first — index 1, whose document the upstream returned itself.
	if response.Results[0].Index != 1 {
		t.Fatalf("expected index 1 first, got %#v", response.Results)
	}
	if response.Results[0].Document == nil || response.Results[0].Document.Text != "upstream returned B" {
		t.Fatalf("expected upstream-returned document to be preserved, got %#v", response.Results[0].Document)
	}
	// Index 0 had no upstream document — backfilled from the request.
	if response.Results[1].Document == nil || response.Results[1].Document.Text != "request doc A" {
		t.Fatalf("expected request document backfill for index 0, got %#v", response.Results[1].Document)
	}
}

func TestCustomOpenAIRerankLargeResponseThresholdReturnsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"rr-large",
			"results":[
				{"index":0,"relevance_score":0.3},
				{"index":1,"relevance_score":0.7}
			],
			"meta":{"tokens":{"input_tokens":5,"output_tokens":0}}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// A tiny threshold would route the structured JSON body onto the large-response
	// streaming path; rerank must parse it in-process regardless and still return results.
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1))

	response, bifrostErr := provider.Rerank(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostRerankRequest{
		Model: "zerank-2",
		Query: "What is Bifrost?",
		Documents: []schemas.RerankDocument{
			{Text: "Bifrost is an AI gateway."},
			{Text: "Paris is in France."},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("expected rerank to succeed, got %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected rerank response")
	}
	if len(response.Results) != 2 {
		t.Fatalf("expected two rerank results despite large-response threshold, got %#v", response.Results)
	}
	if response.Results[0].Index != 1 || response.Results[0].RelevanceScore != 0.7 {
		t.Fatalf("expected results sorted by relevance, got %#v", response.Results)
	}
	if response.Usage == nil || response.Usage.PromptTokens != 5 {
		t.Fatalf("expected usage parsed from body, got %#v", response.Usage)
	}
}

func TestOpenAIRerankUnsupportedForNativeProvider(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1"},
	}, testNoopLogger{})
	_, bifrostErr := provider.Rerank(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{}, &schemas.BifrostRerankRequest{Model: "rerank"})
	assertUnsupportedRerank(t, bifrostErr, schemas.OpenAI)
}

func TestCustomOpenAIRerankHonorsAllowedRequests(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1"},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{},
		},
	}, testNoopLogger{})
	_, bifrostErr := provider.Rerank(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{}, &schemas.BifrostRerankRequest{Model: "rerank"})
	assertUnsupportedRerank(t, bifrostErr, schemas.ModelProvider("hawk"))
}

func assertUnsupportedRerank(t *testing.T, bifrostErr *schemas.BifrostError, provider schemas.ModelProvider) {
	t.Helper()
	if bifrostErr == nil {
		t.Fatal("expected unsupported operation error")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation code, got %#v", bifrostErr)
	}
	if bifrostErr.ExtraFields.Provider != provider {
		t.Fatalf("expected provider %q, got %q", provider, bifrostErr.ExtraFields.Provider)
	}
	if bifrostErr.ExtraFields.RequestType != schemas.RerankRequest {
		t.Fatalf("expected rerank request type, got %q", bifrostErr.ExtraFields.RequestType)
	}
}

func TestCustomOpenAIRerankMapsSearchUnits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Cohere-shaped rerank billing: token counts are null; usage lives in
		// meta.billed_units.search_units.
		_, _ = w.Write([]byte(`{
			"id":"rr-su",
			"results":[
				{"index":0,"relevance_score":0.5},
				{"index":1,"relevance_score":0.9}
			],
			"meta":{
				"tokens":{"input_tokens":null,"output_tokens":null},
				"billed_units":{"search_units":3}
			}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Rerank(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostRerankRequest{
		Model: "zerank-2",
		Query: "What is Bifrost?",
		Documents: []schemas.RerankDocument{
			{Text: "Bifrost is an AI gateway."},
			{Text: "Paris is in France."},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("expected rerank to succeed, got %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected rerank response")
	}
	if response.Usage == nil {
		t.Fatal("expected usage from billed_units.search_units, got nil")
	}
	if response.Usage.TotalTokens != 3 {
		t.Fatalf("expected search_units surfaced as total tokens 3, got %#v", response.Usage)
	}
	if response.Usage.PromptTokens != 0 || response.Usage.CompletionTokens != 0 {
		t.Fatalf("expected zero token counts for search-unit billing, got %#v", response.Usage)
	}
}

func TestCustomOpenAIRerankLargePayloadStreamsBody(t *testing.T) {
	streamedBody := `{"model":"zerank-2","query":"stream me","documents":[{"text":"a"},{"text":"b"}]}`
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"rr-lp",
			"results":[
				{"index":0,"relevance_score":0.4},
				{"index":1,"relevance_score":0.6}
			]
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// Simulate the transport staging the request body as a stream (large-payload passthrough):
	// CheckContextAndGetRequestBody then returns nil jsonData and the handler must stream the
	// staged reader instead of sending an empty body.
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, strings.NewReader(streamedBody))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(streamedBody))

	response, bifrostErr := provider.Rerank(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostRerankRequest{
		Model: "zerank-2",
		Query: "stream me",
		Documents: []schemas.RerankDocument{
			{Text: "a"},
			{Text: "b"},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("expected rerank to succeed, got %v", bifrostErr)
	}
	if receivedBody != streamedBody {
		t.Fatalf("expected upstream to receive streamed passthrough body %q, got %q", streamedBody, receivedBody)
	}
	if response == nil || len(response.Results) != 2 {
		t.Fatalf("expected two rerank results from passthrough response, got %#v", response)
	}
}

// TestCustomOpenAIRerankSendsStringArrayToLlamaCppStyleUpstream reproduces
// https://github.com/maximhq/bifrost/issues/5258: a llama.cpp-hosted
// qwen3-reranker-4b (added to Bifrost as a custom OpenAI-compatible
// provider) rejects the {"text": "..."} object-array form Bifrost used to
// send, only accepting a plain string array for "documents" - matching
// Cohere v2 / vLLM's spec. Bifrost must send plain strings when a document
// carries no id/meta, and only fall back to the object form when it does.
func TestCustomOpenAIRerankSendsStringArrayToLlamaCppStyleUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		// llama.cpp-style strict decoding: "documents" MUST be []string.
		var payload struct {
			Documents []string `json:"documents"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"\"documents\" must be a non-empty string array"}}`))
			return
		}
		if len(payload.Documents) != 3 {
			t.Fatalf("expected 3 string documents, got %#v", payload.Documents)
		}
		if payload.Documents[0] != "Paris is the capital of France." {
			t.Fatalf("expected plain string document, got %q", payload.Documents[0])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"rr-llamacpp",
			"results":[
				{"index":0,"relevance_score":0.95},
				{"index":1,"relevance_score":0.1},
				{"index":2,"relevance_score":0.6}
			]
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "OrionServer",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})

	response, bifrostErr := provider.Rerank(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		schemas.Key{Value: *schemas.NewSecretVar("test-key")},
		&schemas.BifrostRerankRequest{
			Model: "qwen3-reranker-4b",
			Query: "What is the capital of France?",
			Documents: []schemas.RerankDocument{
				{Text: "Paris is the capital of France."},
				{Text: "Berlin is the capital of Germany."},
				{Text: "The Eiffel Tower is in Paris."},
			},
		},
	)
	if bifrostErr != nil {
		t.Fatalf("expected rerank against llama.cpp-style upstream to succeed, got %v", bifrostErr)
	}
	if response == nil || len(response.Results) != 3 {
		t.Fatalf("expected three rerank results, got %#v", response)
	}
	if response.Results[0].RelevanceScore != 0.95 {
		t.Fatalf("expected top result relevance 0.95, got %#v", response.Results[0])
	}
}

// TestCustomOpenAIRerankPreservesIDAndMetaAsObject ensures that once any
// document in the request carries an id or metadata, the whole "documents"
// array is sent as structured objects (never a mix of strings and objects),
// since the bare-string wire form can't carry that data and some strict
// string-array upstreams reject a "documents" array containing any
// non-string element.
func TestCustomOpenAIRerankPreservesIDAndMetaAsObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload struct {
			Documents []json.RawMessage `json:"documents"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if len(payload.Documents) != 2 {
			t.Fatalf("expected 2 documents, got %d", len(payload.Documents))
		}
		var first map[string]interface{}
		if err := json.Unmarshal(payload.Documents[0], &first); err != nil {
			t.Fatalf("expected first document to be an object (uniform with second), got %s: %v", payload.Documents[0], err)
		}
		if first["text"] != "plain document" {
			t.Fatalf("expected first document text to be preserved, got %#v", first)
		}
		var withID map[string]interface{}
		if err := json.Unmarshal(payload.Documents[1], &withID); err != nil {
			t.Fatalf("expected second document to be an object, got %s: %v", payload.Documents[1], err)
		}
		if withID["id"] != "doc-2" {
			t.Fatalf("expected id to be preserved on object-form document, got %#v", withID)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rr-mixed","results":[{"index":0,"relevance_score":0.5},{"index":1,"relevance_score":0.4}]}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Rerank: true},
		},
	}, testNoopLogger{})

	docID := "doc-2"
	_, bifrostErr := provider.Rerank(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		schemas.Key{Value: *schemas.NewSecretVar("test-key")},
		&schemas.BifrostRerankRequest{
			Model: "zerank-2",
			Query: "q",
			Documents: []schemas.RerankDocument{
				{Text: "plain document"},
				{Text: "document with id", ID: &docID},
			},
		},
	)
	if bifrostErr != nil {
		t.Fatalf("expected rerank to succeed, got %v", bifrostErr)
	}
}
