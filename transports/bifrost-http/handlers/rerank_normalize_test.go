package handlers

import (
	"testing"

	"github.com/bytedance/sonic"
)

// TestRerankRequest_HandlerBoundary exercises the full HTTP-decode chain:
// sonic.Unmarshal of a complete request body into RerankRequest (whose
// UnmarshalJSON delegates Documents normalization to
// schemas.NormalizeRerankDocuments). This is the path production code
// follows.
//
// Catches regressions where sonic might handle the alias-shadow
// UnmarshalJSON pattern differently than stdlib encoding/json (which
// was the source of the 2026-06-08 bifrost bug — the first patch
// implemented UnmarshalJSON on the element type RerankDocument, which
// passed stdlib unit tests but failed in production because sonic
// rejects type mismatches at the array-element level BEFORE invoking
// element-level UnmarshalJSON). Table-driven across the shapes a real
// client would send.
//
// The unit tests for schemas.NormalizeRerankDocuments live in
// core/schemas/rerank_test.go; this test guards the handler-tier
// decode boundary specifically.
func TestRerankRequest_HandlerBoundary(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		wantDocs []string // for the success cases — text of each doc, in order
	}{
		{
			name:     "string_array_cohere_spec",
			body:     `{"model":"m","query":"q","documents":["a","b"]}`,
			wantDocs: []string{"a", "b"},
		},
		{
			name:     "object_array_bifrost_legacy",
			body:     `{"model":"m","query":"q","documents":[{"text":"a"},{"text":"b"}]}`,
			wantDocs: []string{"a", "b"},
		},
		{
			name:     "object_array_with_id_and_meta",
			body:     `{"model":"m","query":"q","documents":[{"text":"a","id":"d1","meta":{"k":"v"}}]}`,
			wantDocs: []string{"a"},
		},
		{
			name:     "empty_array",
			body:     `{"model":"m","query":"q","documents":[]}`,
			wantDocs: []string{},
		},
		{
			// `null` documents and missing-documents-field surface via
			// the handler's `len(docs) == 0` guard, not via UnmarshalJSON;
			// here we only check the decode itself completes. Both null
			// and missing should produce a zero-length Documents slice
			// without erroring at the decode boundary.
			name:     "null_documents_decodes_to_empty",
			body:     `{"model":"m","query":"q","documents":null}`,
			wantDocs: []string{},
		},
		{
			name:     "missing_documents_field",
			body:     `{"model":"m","query":"q"}`,
			wantDocs: []string{},
		},
		{
			name:    "invalid_shape_object_not_array",
			body:    `{"model":"m","query":"q","documents":{"not":"array"}}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req RerankRequest
			err := sonic.Unmarshal([]byte(tc.body), &req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected sonic.Unmarshal error, got nil (docs=%+v)", req.Documents)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected sonic.Unmarshal error: %v", err)
			}
			if len(req.Documents) != len(tc.wantDocs) {
				t.Fatalf("expected %d docs, got %d", len(tc.wantDocs), len(req.Documents))
			}
			for i, want := range tc.wantDocs {
				if req.Documents[i].Text != want {
					t.Fatalf("doc[%d].Text = %q, want %q", i, req.Documents[i].Text, want)
				}
			}
			// Spot-check a passthrough field on the id+meta case
			if tc.name == "object_array_with_id_and_meta" {
				if req.Documents[0].ID == nil || *req.Documents[0].ID != "d1" {
					t.Fatalf("ID not preserved: %+v", req.Documents[0].ID)
				}
				if req.Documents[0].Meta["k"] != "v" {
					t.Fatalf("Meta not preserved: %+v", req.Documents[0].Meta)
				}
			}
		})
	}
}
