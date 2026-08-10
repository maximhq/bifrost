package vertex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestCanonicalVertexModelID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"gemini-embedding-2", "gemini-embedding-2"},
		{"models/gemini-embedding-2", "gemini-embedding-2"},
		{"Models/gemini-embedding-2", "gemini-embedding-2"},
		{"  models/gemini-embedding-001  ", "gemini-embedding-001"},
		{"google/gemini-embedding-2", "gemini-embedding-2"},
		{"text-embedding-004", "text-embedding-004"},
		{"models/text-embedding-004", "text-embedding-004"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := canonicalVertexModelID(tc.in); got != tc.want {
				t.Fatalf("canonicalVertexModelID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

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
		{"google/gemini-embedding-2", true},
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
		// Client may pass models/…; URL + body must both use the bare id.
		Model: "models/gemini-embedding-2",
		Input: &schemas.EmbeddingInput{Text: &text},
		Params: &schemas.EmbeddingParameters{
			Dimensions: &dims,
		},
	}
	modelID := canonicalVertexModelID(req.Model)

	batch := ToVertexGeminiBatchEmbeddingRequest(req, "my-project", "europe-west1", modelID)
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
	if strings.Contains(batch.Requests[0].Model, "models/models/") {
		t.Fatalf("double models/ prefix in FQ model: %q", batch.Requests[0].Model)
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

func TestGeminiBatchEmbeddingURL_UsesCanonicalModelID(t *testing.T) {
	t.Parallel()

	// Mirrors Embedding(): both URL path and body FQ name use the same modelID.
	for _, clientModel := range []string{
		"gemini-embedding-2",
		"models/gemini-embedding-2",
		"google/gemini-embedding-2",
	} {
		modelID := canonicalVertexModelID(clientModel)
		url := getVertexModelAwarePublisherModelURL(
			"europe-west1", "v1", "proj", "google", modelID, ":batchEmbedContents",
			false, nil,
		)
		if !strings.HasSuffix(url, "/publishers/google/models/gemini-embedding-2:batchEmbedContents") {
			t.Fatalf("clientModel=%q modelID=%q URL=%q", clientModel, modelID, url)
		}
		if strings.Contains(url, "/models/models/") {
			t.Fatalf("double models/ in URL for clientModel=%q: %q", clientModel, url)
		}

		text := "x"
		req := &schemas.BifrostEmbeddingRequest{
			Model: clientModel,
			Input: &schemas.EmbeddingInput{Text: &text},
		}
		batch := ToVertexGeminiBatchEmbeddingRequest(req, "proj", "europe-west1", modelID)
		wantFQ := "projects/proj/locations/europe-west1/publishers/google/models/gemini-embedding-2"
		if batch.Requests[0].Model != wantFQ {
			t.Fatalf("clientModel=%q body model=%q want %q", clientModel, batch.Requests[0].Model, wantFQ)
		}
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

// TestGeminiBatchEmbedContentsResponse_PreservesOrder is a fixture regression
// for the new :batchEmbedContents response path (CodeRabbit nit on #6016).
// Each embeddings[] item must map to BifrostEmbeddingResponse.Data in source
// order with values and indexes preserved.
func TestGeminiBatchEmbedContentsResponse_PreservesOrder(t *testing.T) {
	t.Parallel()

	// Wire fixture matching Vertex/Google AI Studio batchEmbedContents JSON.
	const fixture = `{
  "embeddings": [
    {
      "values": [0.11, 0.22, 0.33],
      "statistics": { "tokenCount": 3 }
    },
    {
      "values": [0.44, 0.55],
      "statistics": { "tokenCount": 2 }
    },
    {
      "values": [0.66]
    }
  ],
  "metadata": {
    "billableCharacterCount": 12
  }
}`

	var geminiResp gemini.GeminiEmbeddingResponse
	if err := json.Unmarshal([]byte(fixture), &geminiResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(geminiResp.Embeddings) != 3 {
		t.Fatalf("fixture embeddings len = %d, want 3", len(geminiResp.Embeddings))
	}

	// Same converter used by VertexProvider.Embedding for the Gemini branch.
	got := gemini.ToBifrostEmbeddingResponse(&geminiResp, "gemini-embedding-2")
	if got == nil {
		t.Fatal("ToBifrostEmbeddingResponse returned nil")
	}
	if got.Model != "gemini-embedding-2" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.Object != "list" {
		t.Fatalf("object = %q", got.Object)
	}
	if len(got.Data) != 3 {
		t.Fatalf("Data len = %d, want 3", len(got.Data))
	}

	wantVectors := [][]float64{
		{0.11, 0.22, 0.33},
		{0.44, 0.55},
		{0.66},
	}
	for i, want := range wantVectors {
		item := got.Data[i]
		if item.Index != i {
			t.Fatalf("Data[%d].Index = %d, want %d", i, item.Index, i)
		}
		if item.Object != "embedding" {
			t.Fatalf("Data[%d].Object = %q", i, item.Object)
		}
		gotVec := item.Embedding.EmbeddingArray
		if len(gotVec) != len(want) {
			t.Fatalf("Data[%d] len = %d, want %d (%v)", i, len(gotVec), len(want), gotVec)
		}
		for j := range want {
			if gotVec[j] != want[j] {
				t.Fatalf("Data[%d][%d] = %v, want %v", i, j, gotVec[j], want[j])
			}
		}
	}

	// Usage falls back to first embedding's tokenCount when present.
	if got.Usage == nil || got.Usage.PromptTokens != 3 || got.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %#v, want prompt/total 3 from first embedding statistics", got.Usage)
	}
}
