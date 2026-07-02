package schemas

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

// TestNormalizeRerankDocuments_String verifies the Cohere-spec string-array
// shape (`["a","b"]`) decodes correctly. THE FIX — before this, sonic's
// reflective decoder rejected this shape at the array-element type-check
// with "Mismatch type schemas.RerankDocument with value string".
func TestNormalizeRerankDocuments_String(t *testing.T) {
	docs, err := NormalizeRerankDocuments(json.RawMessage(`["a","b"]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
	if docs[0].Text != "a" || docs[1].Text != "b" {
		t.Fatalf("expected texts [a, b], got [%q, %q]", docs[0].Text, docs[1].Text)
	}
	if docs[0].ID != nil || docs[0].Meta != nil {
		t.Fatalf("string-form docs should leave ID + Meta unset")
	}
}

// TestNormalizeRerankDocuments_Object verifies the existing object-array
// shape (`[{"text":"a"},...]`) still works (regression guard).
func TestNormalizeRerankDocuments_Object(t *testing.T) {
	docs, err := NormalizeRerankDocuments(json.RawMessage(`[{"text":"a"},{"text":"b"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
	if docs[0].Text != "a" || docs[1].Text != "b" {
		t.Fatalf("expected texts [a, b], got [%q, %q]", docs[0].Text, docs[1].Text)
	}
}

// TestNormalizeRerankDocuments_ObjectWithMeta verifies optional fields on
// the object-array shape (id, meta) are preserved.
func TestNormalizeRerankDocuments_ObjectWithMeta(t *testing.T) {
	docs, err := NormalizeRerankDocuments(json.RawMessage(
		`[{"text":"a","id":"doc-1","meta":{"k":"v"}}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].Text != "a" {
		t.Fatalf("text not preserved: %+v", docs)
	}
	if docs[0].ID == nil || *docs[0].ID != "doc-1" {
		t.Fatalf("id not preserved: %+v", docs[0].ID)
	}
	if docs[0].Meta["k"] != "v" {
		t.Fatalf("meta not preserved: %+v", docs[0].Meta)
	}
}

// TestNormalizeRerankDocuments_Empty verifies the empty-input error.
// Covers nil, zero-length raw, and the literal JSON `null` (which sonic
// would otherwise unmarshal into a nil slice silently).
func TestNormalizeRerankDocuments_Empty(t *testing.T) {
	if _, err := NormalizeRerankDocuments(nil); err == nil {
		t.Fatalf("expected error on nil documents")
	}
	if _, err := NormalizeRerankDocuments(json.RawMessage(``)); err == nil {
		t.Fatalf("expected error on empty documents")
	}
	if _, err := NormalizeRerankDocuments(json.RawMessage(`null`)); err == nil {
		t.Fatalf("expected error on null documents")
	}
}

// TestNormalizeRerankDocuments_EmptyArray verifies that the valid JSON
// empty array `[]` parses cleanly via the string-array branch and
// returns a zero-length slice with no error. The caller's downstream
// `len(docs) == 0` guard then surfaces "documents are required for
// rerank" — the error path is intentionally caller-owned here so the
// rerank-specific phrasing comes from the request handler, not from
// this shared helper.
func TestNormalizeRerankDocuments_EmptyArray(t *testing.T) {
	docs, err := NormalizeRerankDocuments(json.RawMessage(`[]`))
	if err != nil {
		t.Fatalf("unexpected error on empty array: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected empty docs for `[]`, got %d entries", len(docs))
	}
}

// TestNormalizeRerankDocuments_Invalid verifies an unrecognized shape
// (neither string-array nor object-array) returns a clear error.
func TestNormalizeRerankDocuments_Invalid(t *testing.T) {
	_, err := NormalizeRerankDocuments(json.RawMessage(`{"not":"an array"}`))
	if err == nil {
		t.Fatalf("expected error on non-array documents")
	}
	if !strings.Contains(err.Error(), "documents must be") {
		t.Fatalf("expected error message to mention valid shapes, got: %v", err)
	}
}

// TestBifrostRerankRequest_UnmarshalJSON exercises the schema-tier
// UnmarshalJSON via sonic — the path any direct SDK consumer of
// BifrostRerankRequest follows. Validates that the alias-shadow
// pattern correctly preserves non-Documents fields while normalizing
// Documents through both shapes.
func TestBifrostRerankRequest_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		wantDocs []string
		wantQ    string
		wantM    string
	}{
		{
			name:     "string_array_cohere_spec",
			body:     `{"provider":"openai","model":"m","query":"q","documents":["a","b"]}`,
			wantDocs: []string{"a", "b"},
			wantQ:    "q",
			wantM:    "m",
		},
		{
			name:     "object_array_legacy",
			body:     `{"provider":"openai","model":"m","query":"q","documents":[{"text":"a"},{"text":"b"}]}`,
			wantDocs: []string{"a", "b"},
			wantQ:    "q",
			wantM:    "m",
		},
		{
			// Null is decoded to nil Documents — callers apply their
			// own "documents required" check.
			name:     "null_documents_decodes_to_nil",
			body:     `{"provider":"openai","model":"m","query":"q","documents":null}`,
			wantDocs: []string{},
			wantQ:    "q",
			wantM:    "m",
		},
		{
			// Missing Documents field also decodes to nil.
			name:     "missing_documents_field",
			body:     `{"provider":"openai","model":"m","query":"q"}`,
			wantDocs: []string{},
			wantQ:    "q",
			wantM:    "m",
		},
		{
			name:    "invalid_shape",
			body:    `{"provider":"openai","model":"m","query":"q","documents":{"not":"array"}}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r BifrostRerankRequest
			err := sonic.Unmarshal([]byte(tc.body), &r)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (r=%+v)", r)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Query != tc.wantQ {
				t.Fatalf("Query = %q, want %q", r.Query, tc.wantQ)
			}
			if r.Model != tc.wantM {
				t.Fatalf("Model = %q, want %q", r.Model, tc.wantM)
			}
			if len(r.Documents) != len(tc.wantDocs) {
				t.Fatalf("expected %d docs, got %d", len(tc.wantDocs), len(r.Documents))
			}
			for i, want := range tc.wantDocs {
				if r.Documents[i].Text != want {
					t.Fatalf("doc[%d].Text = %q, want %q", i, r.Documents[i].Text, want)
				}
			}
		})
	}
}
