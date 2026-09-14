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

func TestVertexGeminiEmbedTexts_TextOrTextsNotBoth(t *testing.T) {
	t.Parallel()

	a, b, empty := "a", "b", ""
	// Text takes precedence over Texts (legacy parity).
	req := &schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{
			Text:  &a,
			Texts: []string{b, "c"},
		},
	}
	got := vertexGeminiEmbedTexts(req)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("Text precedence: got %v, want [a]", got)
	}

	// Empty entries in Texts are skipped.
	req = &schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{Texts: []string{"x", empty, "y", ""}},
	}
	got = vertexGeminiEmbedTexts(req)
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("empty filter: got %v, want [x y]", got)
	}

	// Empty single Text → no inputs.
	req = &schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{Text: &empty},
	}
	if got = vertexGeminiEmbedTexts(req); len(got) != 0 {
		t.Fatalf("empty Text: got %v", got)
	}
}

func TestToVertexGeminiEmbedContentRequest_BodyShapeAndExtraParams(t *testing.T) {
	t.Parallel()

	text := "Hello world"
	dims := 768
	extra := map[string]interface{}{
		"taskType":    "RETRIEVAL_DOCUMENT",
		"title":       "doc",
		"dimensions":  dims, // should be consumed (typed field wins via Params.Dimensions)
		"custom_flag": true, // should remain for passthrough
	}
	req := &schemas.BifrostEmbeddingRequest{
		Model: "models/gemini-embedding-2",
		Input: &schemas.EmbeddingInput{Text: &text},
		Params: &schemas.EmbeddingParameters{
			Dimensions:  &dims,
			ExtraParams: extra,
		},
	}

	embedReq := ToVertexGeminiEmbedContentRequest(req, text)
	if embedReq == nil {
		t.Fatal("expected non-nil embedContent request")
	}
	if embedReq.Model != "" {
		t.Fatalf("model must be omitted from body, got %q", embedReq.Model)
	}
	if embedReq.Content == nil || len(embedReq.Content.Parts) != 1 || embedReq.Content.Parts[0].Text != text {
		t.Fatalf("content = %#v", embedReq.Content)
	}
	if embedReq.OutputDimensionality == nil || *embedReq.OutputDimensionality != dims {
		t.Fatalf("outputDimensionality = %#v", embedReq.OutputDimensionality)
	}
	if embedReq.TaskType == nil || *embedReq.TaskType != "RETRIEVAL_DOCUMENT" {
		t.Fatalf("taskType = %#v", embedReq.TaskType)
	}
	if embedReq.Title == nil || *embedReq.Title != "doc" {
		t.Fatalf("title = %#v", embedReq.Title)
	}
	// Consumed keys must not remain in ExtraParams (passthrough merge).
	for _, k := range []string{"taskType", "title", "dimensions", "outputDimensionality", "task_type"} {
		if _, ok := embedReq.ExtraParams[k]; ok {
			t.Fatalf("ExtraParams still has consumed key %q: %#v", k, embedReq.ExtraParams)
		}
	}
	if v, ok := embedReq.ExtraParams["custom_flag"].(bool); !ok || !v {
		t.Fatalf("custom_flag should remain for passthrough: %#v", embedReq.ExtraParams)
	}
	// Original map must be untouched.
	if _, ok := extra["taskType"]; !ok {
		t.Fatal("original ExtraParams was mutated")
	}

	raw, err := json.Marshal(embedReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, `"instances"`) || strings.Contains(s, `"requests"`) {
		t.Fatalf("body must not wrap instances/requests: %s", s)
	}
	if strings.Contains(s, `"model"`) {
		t.Fatalf("body must not set model: %s", s)
	}
}

func TestToVertexGeminiEmbedContentRequest_SnakeCaseTaskType(t *testing.T) {
	t.Parallel()

	text := "hi"
	req := &schemas.BifrostEmbeddingRequest{
		Input: &schemas.EmbeddingInput{Text: &text},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{"task_type": "SEMANTIC_SIMILARITY"},
		},
	}
	embedReq := ToVertexGeminiEmbedContentRequest(req, text)
	if embedReq.TaskType == nil || *embedReq.TaskType != "SEMANTIC_SIMILARITY" {
		t.Fatalf("taskType = %#v", embedReq.TaskType)
	}
	if _, ok := embedReq.ExtraParams["task_type"]; ok {
		t.Fatalf("task_type should be deleted from ExtraParams: %#v", embedReq.ExtraParams)
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

// Live Vertex :embedContent fixture — usage comes from usageMetadata, not statistics.
func TestVertexGeminiEmbedContentResponse_LiveUsageMetadata(t *testing.T) {
	t.Parallel()

	const fixture = `{
  "embedding": {
    "values": [0.11, 0.22, 0.33]
  },
  "usageMetadata": {
    "promptTokenCount": 2,
    "totalTokenCount": 2
  }
}`

	var embedResp vertexGeminiEmbedContentResponse
	if err := json.Unmarshal([]byte(fixture), &embedResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(embedResp.Embedding.Values) != 3 {
		t.Fatalf("values len = %d", len(embedResp.Embedding.Values))
	}
	if embedResp.UsageMetadata == nil {
		t.Fatal("expected usageMetadata on fixture")
	}

	got := bifrostEmbeddingFromVertexGeminiEmbedContent(&embedResp, "gemini-embedding-2", 0)
	if got == nil {
		t.Fatal("nil conversion")
	}
	if got.Model != "gemini-embedding-2" || len(got.Data) != 1 || got.Data[0].Index != 0 {
		t.Fatalf("got = %#v", got)
	}
	want := []float64{0.11, 0.22, 0.33}
	for i, v := range want {
		if got.Data[0].Embedding.EmbeddingArray[i] != v {
			t.Fatalf("values[%d] = %v, want %v", i, got.Data[0].Embedding.EmbeddingArray[i], v)
		}
	}
	// Live shape assertions (CodeRabbit #3 / #5).
	if got.Usage == nil {
		t.Fatal("expected Usage from usageMetadata")
	}
	if got.Usage.PromptTokens != 2 || got.Usage.TotalTokens != 2 {
		t.Fatalf("usage = %#v, want prompt=2 total=2", got.Usage)
	}
}

func TestVertexGeminiEmbedContentResponse_StatisticsFallback(t *testing.T) {
	t.Parallel()

	got := bifrostEmbeddingFromVertexGeminiEmbedContent(&vertexGeminiEmbedContentResponse{
		Embedding: gemini.GeminiEmbedding{
			Values:     []float64{1},
			Statistics: &gemini.ContentEmbeddingStatistics{TokenCount: 9},
		},
	}, "m", 0)
	if got == nil || got.Usage == nil || got.Usage.PromptTokens != 9 || got.Usage.TotalTokens != 9 {
		t.Fatalf("statistics fallback usage = %#v", got)
	}
}

func TestMergeBifrostEmbeddingResponses_OrderAndUsage(t *testing.T) {
	t.Parallel()

	p0 := bifrostEmbeddingFromVertexGeminiEmbedContent(&vertexGeminiEmbedContentResponse{
		Embedding: gemini.GeminiEmbedding{Values: []float64{1, 2}},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 3,
			TotalTokenCount:  3,
		},
	}, "gemini-embedding-2", 0)
	p1 := bifrostEmbeddingFromVertexGeminiEmbedContent(&vertexGeminiEmbedContentResponse{
		Embedding: gemini.GeminiEmbedding{Values: []float64{3, 4, 5}},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 4,
			TotalTokenCount:  4,
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
		t.Fatalf("merged usage = %#v, want 7/7 from usageMetadata parts", got.Usage)
	}
}

func TestVertexGeminiEmbedContentResponse_RejectsBatchShape(t *testing.T) {
	t.Parallel()

	const batchFixture = `{"embeddings":[{"values":[0.1,0.2]}]}`
	var embedResp vertexGeminiEmbedContentResponse
	if err := json.Unmarshal([]byte(batchFixture), &embedResp); err != nil {
		t.Fatalf("unexpected unmarshal err: %v", err)
	}
	if len(embedResp.Embedding.Values) != 0 {
		t.Fatalf("expected empty values for batch shape, got %v", embedResp.Embedding.Values)
	}
	if bifrostEmbeddingFromVertexGeminiEmbedContent(&embedResp, "m", 0) != nil {
		t.Fatal("batch-shaped body must not convert")
	}
}
