package sgl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestRerankLive_HappyPath_ExtraParamsForwarded exercises the FULL Rerank() code
// path against an httptest server that mimics sglang's /v1/rerank wire shape.
//
// This is the live functional check for Fix 1: provider must set
// BifrostContextKeyPassthroughExtraParams=true so caller-supplied
// request.Params.ExtraParams are merged into the outgoing JSON body.
func TestRerankLive_HappyPath_ExtraParamsForwarded(t *testing.T) {
	t.Parallel()

	var (
		gotPath   string
		gotMethod string
		gotAuth   string
		gotCT     string
		gotBody   map[string]interface{}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read err", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &gotBody); err != nil {
			http.Error(w, "json err: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// sglang returns a bare JSON array, score (not relevance_score), no model field.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[
			{"index": 0, "score": 0.91, "document": {"text": "alpha"}},
			{"index": 1, "score": 0.42, "document": {"text": "beta"}},
			{"index": 2, "score": 0.77, "document": {"text": "gamma"}}
		]`)
	}))
	defer server.Close()

	provider := newTestSGLProvider()
	key := schemas.Key{
		ID:    "live-key",
		Value: schemas.EnvVar{Val: "live-api-key"},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.EnvVar{Val: server.URL},
		},
	}

	// Caller does NOT set BifrostContextKeyPassthroughExtraParams — Rerank() must set it itself.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	topN := 3
	returnDocs := true
	req := &schemas.BifrostRerankRequest{
		Provider:  schemas.SGL,
		Model:     "bge-reranker-v2-m3",
		Query:     "what is bifrost",
		Documents: []schemas.RerankDocument{{Text: "alpha"}, {Text: "beta"}, {Text: "gamma"}},
		Params: &schemas.RerankParameters{
			TopN:            &topN,
			ReturnDocuments: &returnDocs,
			ExtraParams: map[string]interface{}{
				"custom_sgl_flag": "should-make-it-through",
				"numeric_knob":    float64(7),
			},
		},
	}

	resp, bifrostErr := provider.Rerank(ctx, key, req)
	if bifrostErr != nil {
		t.Fatalf("Rerank returned error: %v", bifrostErr.Error.Message)
	}

	// --- Request-side assertions (Fix 1: ExtraParams forwarded) ---
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/rerank" {
		t.Fatalf("expected /v1/rerank, got %s", gotPath)
	}
	if gotAuth != "Bearer live-api-key" {
		t.Fatalf("expected Authorization header set, got %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Fatalf("expected JSON content-type, got %q", gotCT)
	}
	if _, hasModel := gotBody["model"]; hasModel {
		t.Fatalf("outgoing body must NOT include `model` (sglang rejects unknown fields); got: %v", gotBody)
	}
	if gotBody["query"] != "what is bifrost" {
		t.Fatalf("query missing/wrong in outgoing body: %v", gotBody["query"])
	}
	if gotBody["custom_sgl_flag"] != "should-make-it-through" {
		t.Fatalf("Fix 1 regression: ExtraParams.custom_sgl_flag missing from outgoing body. Got keys: %v", keysOf(gotBody))
	}
	if gotBody["numeric_knob"] != float64(7) {
		t.Fatalf("Fix 1 regression: ExtraParams.numeric_knob missing/wrong: %v", gotBody["numeric_knob"])
	}

	// --- Response-side assertions ---
	if resp == nil || len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %+v", resp)
	}
	// Sorted descending by score: 0.91, 0.77, 0.42 → indices 0, 2, 1
	wantOrder := []int{0, 2, 1}
	for i, want := range wantOrder {
		if resp.Results[i].Index != want {
			t.Fatalf("result[%d] index = %d, want %d (results not sorted by score desc)", i, resp.Results[i].Index, want)
		}
	}
	if resp.Results[0].RelevanceScore != 0.91 {
		t.Fatalf("top score = %v, want 0.91", resp.Results[0].RelevanceScore)
	}
	if resp.Results[0].Document == nil || resp.Results[0].Document.Text != "alpha" {
		t.Fatalf("expected returned document for top result, got %+v", resp.Results[0].Document)
	}
	if resp.Model != "bge-reranker-v2-m3" {
		t.Fatalf("response model = %q, want canonical request model", resp.Model)
	}
}

// TestRerankLive_GzipErrorPath exercises the FULL Rerank() error path against
// an httptest server that returns a gzip-encoded sglang error envelope with a
// 4xx status.
//
// This is the live functional check for Fix 2: ParseSGLError must read from
// the already-decoded body (ExtraFields.RawResponse) rather than re-snapshotting
// resp.Body() (which is still gzip-compressed at error time).
func TestRerankLive_GzipErrorPath(t *testing.T) {
	t.Parallel()

	// sglang flat error envelope, gzipped.
	envelope := `{"object":"error","message":"the input is longer than the model's context length","type":"invalid_request_error","code":400}`
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	if _, err := gw.Write([]byte(envelope)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(gzbuf.Bytes())
	}))
	defer server.Close()

	provider := newTestSGLProvider()
	key := schemas.Key{
		ID:    "live-key",
		Value: schemas.EnvVar{Val: "live-api-key"},
		SGLKeyConfig: &schemas.SGLKeyConfig{
			URL: schemas.EnvVar{Val: server.URL},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req := &schemas.BifrostRerankRequest{
		Provider:  schemas.SGL,
		Model:     "bge-reranker-v2-m3",
		Query:     "x",
		Documents: []schemas.RerankDocument{{Text: "a"}, {Text: "b"}},
	}

	resp, bifrostErr := provider.Rerank(ctx, key, req)
	if bifrostErr == nil {
		t.Fatalf("expected error from gzipped 400, got success: %+v", resp)
	}

	msg := bifrostErr.Error.Message
	if msg == "" {
		t.Fatal("Fix 2 regression: error message empty — gzipped body likely not decoded")
	}
	// Substring mapping should resolve to context_length_exceeded.
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "context_length_exceeded" {
		gotCode := "<nil>"
		if bifrostErr.Error.Code != nil {
			gotCode = *bifrostErr.Error.Code
		}
		t.Fatalf("expected code=context_length_exceeded from substring map, got code=%q msg=%q", gotCode, msg)
	}
	if !bytesContains(msg, "context length") {
		t.Fatalf("Fix 2 regression: decoded error message lost. Got: %q", msg)
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func bytesContains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
