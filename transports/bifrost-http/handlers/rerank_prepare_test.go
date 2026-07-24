package handlers

// Regression coverage for https://github.com/maximhq/bifrost/issues/5258:
// POST /v1/rerank rejected `documents` as a plain string array (the Cohere
// v2 / vLLM / llama.cpp shape) with "Invalid request payload", only
// accepting the structured {"text": ...} object-array form.

import (
	"testing"
)

func TestPrepareRerankRequest_StringArrayDocuments(t *testing.T) {
	SetLogger(&mockLogger{})
	body := `{
		"model": "custom/qwen3-reranker-4b",
		"query": "What is the capital of France?",
		"documents": [
			"Paris is the capital of France.",
			"Berlin is the capital of Germany.",
			"The Eiffel Tower is in Paris."
		]
	}`
	ctx := newTestRequestCtx(body)

	req, bifrostReq, err := prepareRerankRequest(ctx, nil)
	if err != nil {
		t.Fatalf("expected string-array documents to be accepted, got error: %v", err)
	}
	if len(req.Documents) != 3 {
		t.Fatalf("expected 3 documents, got %d", len(req.Documents))
	}
	if req.Documents[0].Text != "Paris is the capital of France." {
		t.Fatalf("expected first document text to be preserved, got %q", req.Documents[0].Text)
	}
	if bifrostReq.Documents[1].Text != "Berlin is the capital of Germany." {
		t.Fatalf("expected second document text on bifrost request, got %q", bifrostReq.Documents[1].Text)
	}
}

func TestPrepareRerankRequest_ObjectArrayDocuments(t *testing.T) {
	SetLogger(&mockLogger{})
	body := `{
		"model": "custom/qwen3-reranker-4b",
		"query": "What is the capital of France?",
		"documents": [
			{"text": "Paris is the capital of France.", "id": "doc-1"},
			{"text": "Berlin is the capital of Germany."}
		]
	}`
	ctx := newTestRequestCtx(body)

	req, _, err := prepareRerankRequest(ctx, nil)
	if err != nil {
		t.Fatalf("expected object-array documents to still be accepted, got error: %v", err)
	}
	if len(req.Documents) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(req.Documents))
	}
	if req.Documents[0].ID == nil || *req.Documents[0].ID != "doc-1" {
		t.Fatalf("expected id to be preserved on object-form document, got %#v", req.Documents[0].ID)
	}
}
