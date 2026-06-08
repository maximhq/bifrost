package schemas

import (
	"encoding/json"
	"testing"
)

// Cohere's Rerank API and vLLM both accept either a bare string or an object
// with a "text" field for each document. These tests pin the dual-shape
// behaviour added in RerankDocument.UnmarshalJSON.

func TestRerankDocument_UnmarshalString(t *testing.T) {
	var d RerankDocument
	if err := json.Unmarshal([]byte(`"hello"`), &d); err != nil {
		t.Fatalf("unmarshal bare string: %v", err)
	}
	if d.Text != "hello" {
		t.Fatalf("Text = %q, want %q", d.Text, "hello")
	}
	if d.ID != nil {
		t.Fatalf("ID = %v, want nil", d.ID)
	}
	if d.Meta != nil {
		t.Fatalf("Meta = %v, want nil", d.Meta)
	}
}

func TestRerankDocument_UnmarshalObject(t *testing.T) {
	var d RerankDocument
	if err := json.Unmarshal([]byte(`{"text":"hello"}`), &d); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	if d.Text != "hello" {
		t.Fatalf("Text = %q, want %q", d.Text, "hello")
	}
}

func TestRerankDocument_UnmarshalObjectWithIDAndMeta(t *testing.T) {
	var d RerankDocument
	body := `{"text":"hello","id":"doc-1","meta":{"source":"unit-test"}}`
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("unmarshal object with id/meta: %v", err)
	}
	if d.Text != "hello" {
		t.Fatalf("Text = %q, want %q", d.Text, "hello")
	}
	if d.ID == nil || *d.ID != "doc-1" {
		t.Fatalf("ID = %v, want doc-1", d.ID)
	}
	if got, ok := d.Meta["source"].(string); !ok || got != "unit-test" {
		t.Fatalf("Meta[source] = %v, want unit-test", d.Meta["source"])
	}
}

func TestRerankDocuments_UnmarshalStringSlice(t *testing.T) {
	var docs []RerankDocument
	if err := json.Unmarshal([]byte(`["a","b"]`), &docs); err != nil {
		t.Fatalf("unmarshal string slice: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("len = %d, want 2", len(docs))
	}
	if docs[0].Text != "a" || docs[1].Text != "b" {
		t.Fatalf("docs = %+v, want [{Text:a} {Text:b}]", docs)
	}
}

func TestRerankDocuments_UnmarshalMixedSlice(t *testing.T) {
	// Belt-and-suspenders: a slice with both shapes interleaved is something
	// no real client sends, but the decoder should still tolerate it.
	var docs []RerankDocument
	body := `["a",{"text":"b"}]`
	if err := json.Unmarshal([]byte(body), &docs); err != nil {
		t.Fatalf("unmarshal mixed slice: %v", err)
	}
	if len(docs) != 2 || docs[0].Text != "a" || docs[1].Text != "b" {
		t.Fatalf("docs = %+v", docs)
	}
}
