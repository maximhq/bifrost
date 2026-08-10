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

func TestToVertexGeminiEmbedContentRequest_FullyQualifiedModel(t *testing.T) {
	t.Parallel()

	text := "Hello world"
	dims := 768
	req := &schemas.BifrostEmbeddingRequest{
		Model: "models/gemini-embedding-2",
		Input: &schemas.EmbeddingInput{Text: &text},
		Params: &schemas.EmbeddingParameters{
			Dimensions: &dims,
		},
	}
	modelID := canonicalVertexModelID(req.Model)

	embedReq := ToVertexGeminiEmbedContentRequest(req, text, "my-project", "eu", modelID)
	if embedReq == nil {
		t.Fatal("expected non-nil embedContent request")
	}

	wantModel := "projects/my-project/locations/eu/publishers/google/models/gemini-embedding-2"
	if embedReq.Model != wantModel {
		t.Fatalf("model = %q, want %q", embedReq.Model, wantModel)
	}
	if strings.Contains(embedReq.Model, "models/models/") {
		t.Fatalf("double models/ prefix: %q", embedReq.Model)
	}
	if embedReq.Content == nil || len(embedReq.Content.Parts) != 1 || embedReq.Content.Parts[0].Text != text {
		t.Fatalf("content = %#v", embedReq.Content)
	}
	if embedReq.OutputDimensionality == nil || *embedReq.OutputDimensionality != dims {
		t.Fatalf("outputDimensionality = %#v", embedReq.OutputDimensionality)
	}

	raw, err := json.Marshal(embedReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, `"instances"`) || strings.Contains(s, `"requests"`) {
		t.Fatalf("single embedContent body must not wrap instances/requests: %s", s)
	}
	if !strings.Contains(s, `"content"`) || !strings.Contains(s, wantModel) {
		t.Fatalf("body = %s", s)
	}
}

func TestGeminiEmbedContentURL_UsesCanonicalModelID(t *testing.T) {
	t.Parallel()

	for _, clientModel := range []string{
		"gemini-embedding-2",
		"models/gemini-embedding-2",
		"google/gemini-embedding-2",
	} {
		modelID := canonicalVertexModelID(clientModel)
		url := getVertexModelAwarePublisherModelURL(
			"eu", "v1", "proj", "google", modelID, ":embedContent",
			false, nil,
		)
		if !strings.HasSuffix(url, "/publishers/google/models/gemini-embedding-2:embedContent") {
			t.Fatalf("clientModel=%q URL=%q", clientModel, url)
		}
		if strings.Contains(url, "/models/models/") {
			t.Fatalf("double models/ in URL: %q", url)
		}
		if strings.Contains(url, "batchEmbedContents") {
			t.Fatalf("must use :embedContent not batch: %q", url)
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

	geminiURL := getVertexModelAwarePublisherModelURL(
		"eu", "v1", "proj", "google", "gemini-embedding-2", ":embedContent",
		false, nil,
	)
	if !strings.HasSuffix(geminiURL, "/publishers/google/models/gemini-embedding-2:embedContent") {
		t.Fatalf("gemini URL = %q", geminiURL)
	}

	legacyURL := getCompleteURLForGeminiEndpoint("text-embedding-004", "europe-west1", "proj", "", ":predict")
	if !strings.HasSuffix(legacyURL, "/publishers/google/models/text-embedding-004:predict") {
		t.Fatalf("legacy URL = %q", legacyURL)
	}
}

// TestGeminiEmbedContentResponse_PreservesValues is a fixture regression for the
// Vertex :embedContent response path (live shape: embedding.values).
func TestGeminiEmbedContentResponse_PreservesValues(t *testing.T) {
	t.Parallel()

	// Live Vertex response for gemini-embedding-2:embedContent (trimmed).
	const fixture = `{
  "embedding": {
    "values": [0.11, 0.22, 0.33]
  },
  "usageMetadata": {
    "promptTokenCount": 2,
    "totalTokenCount": 2
  }
}`

	var embedResp gemini.GeminiEmbedContentResponse
	if err := json.Unmarshal([]byte(fixture), &embedResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(embedResp.Embedding.Values) != 3 {
		t.Fatalf("values len = %d", len(embedResp.Embedding.Values))
	}

	got := bifrostEmbeddingFromGeminiEmbedContent(&embedResp, "gemini-embedding-2", 0)
	if got == nil {
		t.Fatal("nil conversion")
	}
	if got.Model != "gemini-embedding-2" || len(got.Data) != 1 {
		t.Fatalf("got = %#v", got)
	}
	if got.Data[0].Index != 0 {
		t.Fatalf("index = %d", got.Data[0].Index)
	}
	want := []float64{0.11, 0.22, 0.33}
	for i, v := range want {
		if got.Data[0].Embedding.EmbeddingArray[i] != v {
			t.Fatalf("values[%d] = %v, want %v", i, got.Data[0].Embedding.EmbeddingArray[i], v)
		}
	}
}

func TestMergeBifrostEmbeddingResponses_OrderAndUsage(t *testing.T) {
	t.Parallel()

	p0 := bifrostEmbeddingFromGeminiEmbedContent(&gemini.GeminiEmbedContentResponse{
		Embedding: gemini.GeminiEmbedding{
			Values:     []float64{1, 2},
			Statistics: &gemini.ContentEmbeddingStatistics{TokenCount: 3},
		},
	}, "gemini-embedding-2", 0)
	p1 := bifrostEmbeddingFromGeminiEmbedContent(&gemini.GeminiEmbedContentResponse{
		Embedding: gemini.GeminiEmbedding{
			Values:     []float64{3, 4, 5},
			Statistics: &gemini.ContentEmbeddingStatistics{TokenCount: 4},
		},
	}, "gemini-embedding-2", 1)

	got := mergeBifrostEmbeddingResponses([]*schemas.BifrostEmbeddingResponse{p0, p1}, "gemini-embedding-2")
	if got == nil || len(got.Data) != 2 {
		t.Fatalf("got = %#v", got)
	}
	if got.Data[0].Index != 0 || got.Data[1].Index != 1 {
		t.Fatalf("indexes = %d,%d", got.Data[0].Index, got.Data[1].Index)
	}
	if len(got.Data[0].Embedding.EmbeddingArray) != 2 || len(got.Data[1].Embedding.EmbeddingArray) != 3 {
		t.Fatalf("vector lens wrong: %#v", got.Data)
	}
	if got.Usage == nil || got.Usage.PromptTokens != 7 || got.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v", got.Usage)
	}
}

// Ensure unmarshaling the batch-shaped response into EmbedContentResponse fails
// loudly (documents why we must not call :batchEmbedContents on Vertex).
func TestGeminiEmbedContentResponse_RejectsBatchShape(t *testing.T) {
	t.Parallel()

	const batchFixture = `{"embeddings":[{"values":[0.1,0.2]}]}`
	var embedResp gemini.GeminiEmbedContentResponse
	if err := json.Unmarshal([]byte(batchFixture), &embedResp); err != nil {
		t.Fatalf("unexpected unmarshal err: %v", err)
	}
	// JSON ignores unknown "embeddings" key — Values stays empty.
	if len(embedResp.Embedding.Values) != 0 {
		t.Fatalf("expected empty values when given batch shape, got %v", embedResp.Embedding.Values)
	}
	if bifrostEmbeddingFromGeminiEmbedContent(&embedResp, "m", 0) != nil {
		t.Fatal("batch-shaped body must not convert to a Bifrost embedding")
	}
}
