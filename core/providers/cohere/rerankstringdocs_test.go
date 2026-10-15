package cohere

// Regression tests for issue #6640: since the #6328 rerank rework, /v1/rerank
// requests reached the Cohere v2 wire with documents as objects
// ([{"text":"doc 1"},...]). Cohere v2 defines the request's documents as an
// array of STRINGS (the object form was v1), so strict Cohere-v2-compatible
// servers (custom providers with base_provider_type: cohere) rejected the
// payload with 422 "Input should be a valid string". Request documents must
// marshal as bare strings; response documents keep the object form.

import (
	"encoding/json"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func wireDocuments(t *testing.T, req *CohereRerankRequest) []interface{} {
	t.Helper()
	body, err := providerUtils.MarshalProviderRequest(req)
	if err != nil {
		t.Fatalf("marshal wire body: %v", err)
	}
	var wire struct {
		Documents []interface{} `json:"documents"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal wire body: %v (body: %s)", err, body)
	}
	return wire.Documents
}

func TestRerankStringDocumentsStayStringsOnCohereWire(t *testing.T) {
	// The exact caller shape from the issue: plain string documents on
	// /v1/rerank, ingested via the neutral schema's lenient unmarshal.
	var docs []schemas.RerankDocument
	if err := json.Unmarshal([]byte(`["doc 1","doc 2","doc 3"]`), &docs); err != nil {
		t.Fatalf("ingress unmarshal: %v", err)
	}

	cohereReq := ToCohereRerankRequest(&schemas.BifrostRerankRequest{
		Model:     "rerank-v3.5",
		Query:     "what is bifrost?",
		Documents: docs,
	})

	wire := wireDocuments(t, cohereReq)
	if len(wire) != 3 {
		t.Fatalf("expected 3 wire documents, got %d", len(wire))
	}
	for i, want := range []string{"doc 1", "doc 2", "doc 3"} {
		s, ok := wire[i].(string)
		if !ok {
			t.Fatalf("documents[%d] is %T (%v), want JSON string per Cohere v2 rerank API", i, wire[i], wire[i])
		}
		if s != want {
			t.Errorf("documents[%d] = %q, want %q", i, s, want)
		}
	}
}

func TestRerankStructuredDocumentsMarshalRankedTextString(t *testing.T) {
	// Structured neutral documents (text + id + metadata) also go out as the
	// single ranked string; IDs are restored response-side from the request's
	// documents slice, not from the provider echo.
	id := "doc-a"
	cohereReq := ToCohereRerankRequest(&schemas.BifrostRerankRequest{
		Model: "rerank-v3.5",
		Query: "q",
		Documents: []schemas.RerankDocument{
			{Text: "structured text", ID: &id, Meta: map[string]interface{}{"k": "v"}},
		},
	})

	wire := wireDocuments(t, cohereReq)
	if len(wire) != 1 {
		t.Fatalf("expected 1 wire document, got %d", len(wire))
	}
	if s, ok := wire[0].(string); !ok || s != "structured text" {
		t.Fatalf("documents[0] = %T (%v), want the ranked text as a JSON string", wire[0], wire[0])
	}
}

func TestRerankRequestDocumentUnmarshalStaysLenient(t *testing.T) {
	// The /cohere integration ingress decodes into CohereRerankRequest; both
	// the string and object document forms must still parse.
	var req CohereRerankRequest
	payload := `{"model":"rerank-v3.5","query":"q","documents":["plain",{"text":"obj","id":"d1","extra":"ride-along"}]}`
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Documents) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(req.Documents))
	}
	if req.Documents[0].Text != "plain" {
		t.Errorf("string document text = %q, want %q", req.Documents[0].Text, "plain")
	}
	if req.Documents[1].Text != "obj" || req.Documents[1].ID == nil || *req.Documents[1].ID != "d1" {
		t.Errorf("object document not parsed leniently: %+v", req.Documents[1])
	}
	if req.Documents[1].Metadata["extra"] != "ride-along" {
		t.Errorf("ride-along key lost: %+v", req.Documents[1].Metadata)
	}
}

func TestRerankResponseDocumentKeepsObjectForm(t *testing.T) {
	// Responses always use the object form; the request-side string marshal
	// must not leak into CohereRerankResult.Document.
	result := CohereRerankResult{
		Index:          0,
		RelevanceScore: 0.9,
		Document:       &CohereRerankDocument{Text: "doc 1"},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Document map[string]interface{} `json:"document"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("response document is not an object: %v (body: %s)", err, data)
	}
	if decoded.Document["text"] != "doc 1" {
		t.Errorf("response document text lost: %s", data)
	}
}
