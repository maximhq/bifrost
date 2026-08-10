package vertex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestUsesGeminiEmbedContentAPI(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  bool
	}{
		{"gemini-embedding-2", true},
		{"gemini-embedding-001", true},
		{"Gemini-Embedding-2", true},
		{"models/gemini-embedding-2", true},
		{"text-embedding-004", false},
		{"text-embedding-005", false},
		{"text-multilingual-embedding-002", false},
		{"multimodalembedding@001", false},
		{"gemini-2.5-flash", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			if got := usesGeminiEmbedContentAPI(tc.model); got != tc.want {
				t.Fatalf("usesGeminiEmbedContentAPI(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestToVertexGeminiBatchEmbeddingRequest_FullyQualifiedModel(t *testing.T) {
	t.Parallel()

	text := "Hello world"
	dims := 768
	req := &schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-2",
		Input: &schemas.EmbeddingInput{Text: &text},
		Params: &schemas.EmbeddingParameters{
			Dimensions: &dims,
		},
	}

	batch := ToVertexGeminiBatchEmbeddingRequest(req, "my-project", "europe-west1")
	if batch == nil {
		t.Fatal("expected non-nil batch request")
	}
	if len(batch.Requests) != 1 {
		t.Fatalf("requests len = %d, want 1", len(batch.Requests))
	}

	wantModel := "projects/my-project/locations/europe-west1/publishers/google/models/gemini-embedding-2"
	if batch.Requests[0].Model != wantModel {
		t.Fatalf("model = %q, want %q", batch.Requests[0].Model, wantModel)
	}
	if batch.Requests[0].Content == nil || len(batch.Requests[0].Content.Parts) != 1 {
		t.Fatalf("expected one content part, got %#v", batch.Requests[0].Content)
	}
	if batch.Requests[0].Content.Parts[0].Text != text {
		t.Fatalf("text = %q, want %q", batch.Requests[0].Content.Parts[0].Text, text)
	}
	if batch.Requests[0].OutputDimensionality == nil || *batch.Requests[0].OutputDimensionality != dims {
		t.Fatalf("outputDimensionality = %#v, want %d", batch.Requests[0].OutputDimensionality, dims)
	}

	// Wire shape must not use legacy instances/parameters.
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, `"instances"`) {
		t.Fatalf("batch body must not contain instances: %s", s)
	}
	if !strings.Contains(s, `"requests"`) {
		t.Fatalf("batch body must contain requests: %s", s)
	}
	if !strings.Contains(s, wantModel) {
		t.Fatalf("batch body missing FQ model: %s", s)
	}
}

func TestToVertexEmbeddingRequest_LegacyInstancesShape(t *testing.T) {
	t.Parallel()

	text := "legacy"
	req := &schemas.BifrostEmbeddingRequest{
		Model: "text-embedding-004",
		Input: &schemas.EmbeddingInput{Text: &text},
	}
	vertexReq := ToVertexEmbeddingRequest(req)
	if vertexReq == nil {
		t.Fatal("expected non-nil legacy request")
	}
	if len(vertexReq.Instances) != 1 || vertexReq.Instances[0].Content != text {
		t.Fatalf("instances = %#v", vertexReq.Instances)
	}
	raw, err := json.Marshal(vertexReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"instances"`) {
		t.Fatalf("legacy body must contain instances: %s", raw)
	}
}

func TestGetVertexEmbeddingURL_MethodByModel(t *testing.T) {
	t.Parallel()

	// URL helpers used by Embedding(): gemini-embedding-* → :batchEmbedContents,
	// legacy → :predict (via getCompleteURLForGeminiEndpoint).
	geminiURL := getVertexModelAwarePublisherModelURL(
		"europe-west1", "v1", "proj", "google", "gemini-embedding-2", ":batchEmbedContents",
		false, nil,
	)
	if !strings.HasSuffix(geminiURL, "/publishers/google/models/gemini-embedding-2:batchEmbedContents") {
		t.Fatalf("gemini URL = %q", geminiURL)
	}
	if !strings.Contains(geminiURL, "aiplatform") {
		t.Fatalf("gemini URL missing aiplatform host: %q", geminiURL)
	}

	legacyURL := getCompleteURLForGeminiEndpoint("text-embedding-004", "europe-west1", "proj", "", ":predict")
	if !strings.HasSuffix(legacyURL, "/publishers/google/models/text-embedding-004:predict") {
		t.Fatalf("legacy URL = %q", legacyURL)
	}
}
