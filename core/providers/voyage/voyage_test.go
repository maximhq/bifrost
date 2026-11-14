package voyage_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/voyage"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestVoyage runs the shared live scenario suite against the Voyage API: Voyage serves embeddings
// and reranking only. It is skipped when VOYAGE_API_KEY is not set.
func TestVoyage(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("VOYAGE_API_KEY")) == "" {
		t.Skip("Skipping Voyage tests because VOYAGE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Voyage,
		EmbeddingModel: "voyage-4-lite",
		RerankModel:    "rerank-3-lite",
		Scenarios: llmtests.TestScenarios{
			Embedding: true,
			Rerank:    true,
		},
	}

	t.Run("VoyageTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

var testKey = schemas.Key{Value: *schemas.NewSecretVar("voyage-test-key")}

// stub is a fake Voyage API: it records the last request and replies with a fixed status and body.
type stub struct {
	server *httptest.Server
	hits   atomic.Int32
	path   string
	auth   string
	body   map[string]any
}

func newStub(t *testing.T, status int, reply string) *stub {
	t.Helper()
	s := &stub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.path = r.URL.Path
		s.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		s.body = nil
		_ = json.Unmarshal(raw, &s.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) provider(t *testing.T) *voyage.VoyageProvider {
	t.Helper()
	p, err := voyage.NewVoyageProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: s.server.URL, DefaultRequestTimeoutInSeconds: 10},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewVoyageProvider: %v", err)
	}
	return p
}

func ctx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func textInput(texts ...string) []schemas.EmbeddingInputItem {
	items := make([]schemas.EmbeddingInputItem, len(texts))
	for i, text := range texts {
		items[i] = schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr(text)}}}
	}
	return items
}

func embeddingRequest(model string, input []schemas.EmbeddingInputItem, params *schemas.EmbeddingParameters) *schemas.BifrostEmbeddingRequest {
	return &schemas.BifrostEmbeddingRequest{Provider: schemas.Voyage, Model: model, Input: input, Params: params}
}

func extra(kv ...any) *schemas.EmbeddingParameters {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return &schemas.EmbeddingParameters{ExtraParams: m}
}

func imagePart(url, data, mime string) schemas.EmbeddingContentPart {
	media := &schemas.EmbeddingMediaPart{}
	if url != "" {
		media.URL = &url
	}
	if data != "" {
		media.Data = &data
	}
	if mime != "" {
		media.MIMEType = &mime
	}
	return schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeImage, Image: media}
}

func videoPart(url string) schemas.EmbeddingContentPart {
	return schemas.EmbeddingContentPart{Type: schemas.EmbeddingContentPartTypeVideo, Video: &schemas.EmbeddingMediaPart{URL: &url}}
}

// Requests that can never succeed must fail before anything is sent to Voyage.
func TestEmbeddingRejectsInvalidRequestsBeforeSending(t *testing.T) {
	t.Parallel()
	pngData := "iVBORw0KGgo="
	manyInputs := make([]string, 1001)
	for i := range manyInputs {
		manyInputs[i] = "x"
	}
	perItem := textInput("a")
	perItem[0].Params = &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(256)}

	tests := []struct {
		name    string
		request *schemas.BifrostEmbeddingRequest
		want    string
	}{
		{"missing model", embeddingRequest("", textInput("a"), nil), "no default model"},
		{"another provider's model", embeddingRequest("text-embedding-3-small", textInput("a"), nil), "not a Voyage model"},
		{"unknown voyage model", embeddingRequest("voyage-9-ultra", textInput("a"), nil), "not a Voyage model"},
		{"reranker as embedding model", embeddingRequest("rerank-3", textInput("a"), nil), "not an embedding model"},
		{"dimension outside the matryoshka set", embeddingRequest("voyage-4", textInput("a"), &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(768)}), "does not support dimensions=768"},
		{"dimension on the fixed-size multimodal model", embeddingRequest("voyage-multimodal-3", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("a banana")}}}}, &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(512)}), "does not support dimensions=512"},
		{"dimension on a fixed-size model", embeddingRequest("voyage-law-2", textInput("a"), &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(512)}), "does not support dimensions=512"},
		{"quantized output on a model without it", embeddingRequest("voyage-finance-2", textInput("a"), &schemas.EmbeddingParameters{EncodingFormat: schemas.Ptr("int8")}), "float vectors only"},
		{"input_type other than query/document", embeddingRequest("voyage-4", textInput("a"), extra("input_type", "search_document")), "input_type must be"},
		{"too many inputs", embeddingRequest("voyage-4", textInput(manyInputs...), nil), "at most 1000 inputs"},
		{"per-item params", embeddingRequest("voyage-4", perItem, nil), "own params"},
		{"image sent to a text model", embeddingRequest("voyage-4", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{imagePart("https://x/y.png", "", "")}}}, nil), "single text part"},
		{"contextualized without input_type", embeddingRequest("voyage-context-4", textInput("doc"), nil), "needs input_type"},
		{"contextualized pre-chunked document", embeddingRequest("voyage-context-4", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("chunk 1")},
			{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("chunk 2")},
		}}}, extra("input_type", "document")), "send one text per item"},
		{"contextualized quantized", embeddingRequest("voyage-context-3", textInput("doc"), &schemas.EmbeddingParameters{EncodingFormat: schemas.Ptr("int8"), ExtraParams: map[string]any{"input_type": "document"}}), "float vectors only"},
		{"multimodal mixing URL and base64", embeddingRequest("voyage-multimodal-3.5", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			imagePart("https://x/y.png", "", ""), imagePart("", pngData, "image/png"),
		}}}, nil), "URL or by base64"},
		{"multimodal base64 without media type", embeddingRequest("voyage-multimodal-3.5", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{imagePart("", pngData, "")}}}, nil), "mime_type"},
		{"multimodal unsupported image type", embeddingRequest("voyage-multimodal-3.5", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{imagePart("", pngData, "image/bmp")}}}, nil), "not supported by Voyage"},
		{"video on a model without video", embeddingRequest("voyage-multimodal-3", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{videoPart("https://x/y.mp4")}}}, nil), "does not accept video"},
		{"audio part", embeddingRequest("voyage-multimodal-3.5", []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeAudio, Audio: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://x/y.mp3")}},
		}}}, nil), "text, image and video parts only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, http.StatusOK, `{}`)
			_, bifrostErr := s.provider(t).Embedding(ctx(), testKey, tt.request)
			if bifrostErr == nil {
				t.Fatalf("want an error containing %q, got success", tt.want)
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %v, want 400", bifrostErr.StatusCode)
			}
			if msg := bifrostErr.Error.Message; !strings.Contains(msg, tt.want) {
				t.Errorf("message %q does not contain %q", msg, tt.want)
			}
			if n := s.hits.Load(); n != 0 {
				t.Errorf("%d request(s) reached the API; the request is invalid and must not be sent", n)
			}
		})
	}
}

// Voyage's auto-chunking accepts one string per document and document input_type only; sending a
// nested list or a query with it is a 400 from the API, so the body shape is part of the contract.
func TestContextualizedRequestShape(t *testing.T) {
	t.Parallel()
	const reply = `{"object":"list","data":[{"object":"list","index":0,"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0,"text":"chunk a"},{"object":"embedding","embedding":[0.3,0.4],"index":1,"text":"chunk b"}]},{"object":"list","index":1,"data":[{"object":"embedding","embedding":[0.5,0.6],"index":0,"text":"only chunk"}]}],"model":"voyage-context-4","usage":{"total_tokens":42}}`

	t.Run("document mode sends flat strings with auto-chunking", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, reply)
		resp, bifrostErr := s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-context-4", textInput("first doc", "second doc"), extra("input_type", "document")))
		if bifrostErr != nil {
			t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
		}
		if s.path != "/v1/contextualizedembeddings" {
			t.Errorf("path = %q", s.path)
		}
		if s.auth != "Bearer voyage-test-key" {
			t.Errorf("Authorization = %q", s.auth)
		}
		inputs, _ := s.body["inputs"].([]any)
		if len(inputs) != 2 || inputs[0] != "first doc" || inputs[1] != "second doc" {
			t.Errorf("inputs = %v, want the flat list of document strings", s.body["inputs"])
		}
		if s.body["enable_auto_chunking"] != true || s.body["chunk_size"] != float64(32000) || s.body["input_type"] != "document" {
			t.Errorf("auto-chunking fields wrong: %v", s.body)
		}
		if _, set := s.body["output_dimension"]; set {
			t.Errorf("output_dimension was sent though the caller did not ask: %v", s.body)
		}

		if len(resp.Data) != 2 {
			t.Fatalf("got %d entries, want one per document", len(resp.Data))
		}
		first := resp.Data[0]
		if len(first.Embedding.Embedding2DArray) != 2 || len(first.Chunks) != 2 || first.Chunks[1] != "chunk b" {
			t.Errorf("document 0 = %+v, want 2 vectors with their chunk texts", first)
		}
		if resp.Usage == nil || resp.Usage.TotalTokens != 42 || resp.Usage.PromptTokens != 42 {
			t.Errorf("usage = %+v", resp.Usage)
		}
	})

	t.Run("query mode sends single-query lists without chunking", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, `{"object":"list","data":[{"object":"list","index":0,"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}]}],"model":"voyage-context-3","usage":{"total_tokens":3}}`)
		params := extra("input_type", "query")
		params.Dimensions = schemas.Ptr(256)
		resp, bifrostErr := s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-context-3", textInput("what is a vector"), params))
		if bifrostErr != nil {
			t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
		}
		if raw, _ := json.Marshal(s.body["inputs"]); string(raw) != `[["what is a vector"]]` {
			t.Errorf("inputs = %s, want one single-query list", raw)
		}
		if _, set := s.body["enable_auto_chunking"]; set {
			t.Errorf("a query must not enable auto-chunking: %v", s.body)
		}
		if _, set := s.body["chunk_size"]; set {
			t.Errorf("a query must not set chunk_size: %v", s.body)
		}
		if s.body["output_dimension"] != float64(256) {
			t.Errorf("output_dimension = %v, want 256", s.body["output_dimension"])
		}
		if got := resp.Data[0].Embedding.EmbeddingArray; len(got) != 2 || len(resp.Data[0].Chunks) != 0 {
			t.Errorf("query entry = %+v, want one flat vector and no chunks", resp.Data[0])
		}
	})
}

// A response that cannot be priced must fail rather than bill nothing.
func TestResponsesWithoutUsageFailClosed(t *testing.T) {
	t.Parallel()
	embedReply := `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"m"}`
	tests := []struct {
		name string
		call func(p *voyage.VoyageProvider) *schemas.BifrostError
		body string
	}{
		{"text embedding", func(p *voyage.VoyageProvider) *schemas.BifrostError {
			_, err := p.Embedding(ctx(), testKey, embeddingRequest("voyage-4", textInput("a"), nil))
			return err
		}, embedReply},
		{"text embedding without total_tokens", func(p *voyage.VoyageProvider) *schemas.BifrostError {
			_, err := p.Embedding(ctx(), testKey, embeddingRequest("voyage-4", textInput("a"), nil))
			return err
		}, `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"m","usage":{}}`},
		{"contextualized", func(p *voyage.VoyageProvider) *schemas.BifrostError {
			_, err := p.Embedding(ctx(), testKey, embeddingRequest("voyage-context-4", textInput("a"), extra("input_type", "document")))
			return err
		}, `{"object":"list","data":[{"object":"list","index":0,"data":[{"object":"embedding","embedding":[0.1],"index":0}]}],"model":"m"}`},
		{"multimodal", func(p *voyage.VoyageProvider) *schemas.BifrostError {
			_, err := p.Embedding(ctx(), testKey, embeddingRequest("voyage-multimodal-3.5", textInput("a"), nil))
			return err
		}, `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"m","usage":{"text_tokens":1}}`},
		{"rerank", func(p *voyage.VoyageProvider) *schemas.BifrostError {
			_, err := p.Rerank(ctx(), testKey, &schemas.BifrostRerankRequest{Provider: schemas.Voyage, Model: "rerank-3", Query: "q", Documents: []schemas.RerankDocument{{Text: "a"}}})
			return err
		}, `{"object":"list","data":[{"index":0,"relevance_score":0.9}],"model":"rerank-3"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bifrostErr := tt.call(newStub(t, http.StatusOK, tt.body).provider(t))
			if bifrostErr == nil {
				t.Fatal("want an error for a response without usage, got success")
			}
			if !strings.Contains(bifrostErr.Error.Message, "cannot be billed") {
				t.Errorf("message = %q, want the billing explanation", bifrostErr.Error.Message)
			}
		})
	}
}

func TestVoyageErrorBodyIsParsed(t *testing.T) {
	t.Parallel()
	s := newStub(t, http.StatusBadRequest, `{"detail":"auto-chunking expects one string per document"}`)
	_, bifrostErr := s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-4", textInput("a"), nil))
	if bifrostErr == nil {
		t.Fatal("want an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400", bifrostErr.StatusCode)
	}
	if bifrostErr.Error.Message != "auto-chunking expects one string per document" {
		t.Errorf("message = %q, want the detail text", bifrostErr.Error.Message)
	}

	s = newStub(t, http.StatusUnauthorized, `{"detail":"Provided API key is invalid."}`)
	_, bifrostErr = s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-4", textInput("a"), nil))
	if bifrostErr == nil || bifrostErr.Error.Message != "Provided API key is invalid." {
		t.Errorf("401 error = %+v", bifrostErr)
	}
}

func TestEmbeddingOutputEncodings(t *testing.T) {
	t.Parallel()
	t.Run("int8 vectors keep their dtype", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, `{"object":"list","data":[{"object":"embedding","embedding":[-3,4,127],"index":0}],"model":"voyage-4","usage":{"total_tokens":2}}`)
		resp, bifrostErr := s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-4", textInput("a"), &schemas.EmbeddingParameters{EncodingFormat: schemas.Ptr("int8")}))
		if bifrostErr != nil {
			t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
		}
		if s.body["output_dtype"] != "int8" {
			t.Errorf("output_dtype = %v", s.body["output_dtype"])
		}
		if got := resp.Data[0]; len(got.Embedding.EmbeddingInt8Array) != 3 || got.EncodingFormat != schemas.EmbeddingEncodingInt8 {
			t.Errorf("entry = %+v", got)
		}
	})
	t.Run("base64 vectors stay strings", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, `{"object":"list","data":[{"object":"embedding","embedding":"AAAAAA==","index":0}],"model":"voyage-4","usage":{"total_tokens":2}}`)
		resp, bifrostErr := s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-4", textInput("a"), &schemas.EmbeddingParameters{EncodingFormat: schemas.Ptr("base64")}))
		if bifrostErr != nil {
			t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
		}
		if s.body["encoding_format"] != "base64" {
			t.Errorf("encoding_format = %v", s.body["encoding_format"])
		}
		if got := resp.Data[0].Embedding.EmbeddingStr; got == nil || *got != "AAAAAA==" {
			t.Errorf("entry = %+v", resp.Data[0])
		}
	})
}

func TestMultimodalRequestAndUsage(t *testing.T) {
	t.Parallel()
	s := newStub(t, http.StatusOK, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"voyage-multimodal-3.5","usage":{"text_tokens":5,"image_pixels":560000,"video_pixels":0,"total_tokens":1005}}`)
	input := []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
		{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("a banana")},
		imagePart("", "iVBORw0KGgo=", "image/png"),
	}}}
	resp, bifrostErr := s.provider(t).Embedding(ctx(), testKey, embeddingRequest("voyage-multimodal-3.5", input, extra("input_type", "document")))
	if bifrostErr != nil {
		t.Fatalf("Embedding: %v", bifrostErr.Error.Message)
	}
	if s.path != "/v1/multimodalembeddings" {
		t.Errorf("path = %q", s.path)
	}
	raw, _ := json.Marshal(s.body["inputs"])
	if want := `[{"content":[{"text":"a banana","type":"text"},{"image_base64":"data:image/png;base64,iVBORw0KGgo=","type":"image_base64"}]}]`; string(raw) != want {
		t.Errorf("inputs = %s\nwant     %s", raw, want)
	}
	if s.body["input_type"] != "document" {
		t.Errorf("input_type = %v", s.body["input_type"])
	}
	if u := resp.Usage; u == nil || u.PromptTokens != 5 || u.TotalTokens != 1005 || u.PromptTokensDetails == nil || u.PromptTokensDetails.ImageTokens != 1000 {
		t.Errorf("usage = %+v, want 5 text tokens, 1000 media tokens, 1005 total", u)
	}
}

func TestRerank(t *testing.T) {
	t.Parallel()
	docs := []schemas.RerankDocument{{Text: "cats", ID: schemas.Ptr("d0")}, {Text: "dogs", ID: schemas.Ptr("d1")}, {Text: "birds"}}
	valid := func() *schemas.BifrostRerankRequest {
		return &schemas.BifrostRerankRequest{Provider: schemas.Voyage, Model: "rerank-3", Query: "pets", Documents: docs}
	}

	t.Run("maps top_k and documents and sorts by score", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, `{"object":"list","data":[{"index":0,"relevance_score":0.2},{"index":1,"relevance_score":0.9}],"model":"rerank-3","usage":{"total_tokens":17}}`)
		request := valid()
		request.Params = &schemas.RerankParameters{TopN: schemas.Ptr(2), ReturnDocuments: schemas.Ptr(true)}
		resp, bifrostErr := s.provider(t).Rerank(ctx(), testKey, request)
		if bifrostErr != nil {
			t.Fatalf("Rerank: %v", bifrostErr.Error.Message)
		}
		if s.path != "/v1/rerank" || s.body["top_k"] != float64(2) || s.body["return_documents"] != true || s.body["model"] != "rerank-3" {
			t.Errorf("request = %s %v", s.path, s.body)
		}
		if len(resp.Results) != 2 || resp.Results[0].Index != 1 || resp.Results[0].RelevanceScore != 0.9 {
			t.Fatalf("results = %+v, want highest score first", resp.Results)
		}
		if got := resp.Results[0]; got.ID == nil || *got.ID != "d1" || got.Document == nil || got.Document.Text != "dogs" {
			t.Errorf("top result = %+v, want the caller's ID and document", got)
		}
		if resp.Usage == nil || resp.Usage.TotalTokens != 17 {
			t.Errorf("usage = %+v", resp.Usage)
		}
	})

	t.Run("omits top_k and return_documents when not asked", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, `{"object":"list","data":[{"index":2,"relevance_score":0.5}],"model":"rerank-3","usage":{"total_tokens":1}}`)
		if _, bifrostErr := s.provider(t).Rerank(ctx(), testKey, valid()); bifrostErr != nil {
			t.Fatalf("Rerank: %v", bifrostErr.Error.Message)
		}
		for _, field := range []string{"top_k", "return_documents"} {
			if _, set := s.body[field]; set {
				t.Errorf("%s was sent though the caller did not ask", field)
			}
		}
	})

	t.Run("index outside the documents sent", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, http.StatusOK, `{"object":"list","data":[{"index":7,"relevance_score":0.5}],"model":"rerank-3","usage":{"total_tokens":1}}`)
		if _, bifrostErr := s.provider(t).Rerank(ctx(), testKey, valid()); bifrostErr == nil {
			t.Fatal("want an error for an out-of-range index")
		}
	})

	invalid := map[string]struct {
		mutate func(r *schemas.BifrostRerankRequest)
		want   string
	}{
		"missing model":            {func(r *schemas.BifrostRerankRequest) { r.Model = "" }, "no default model"},
		"another provider's model": {func(r *schemas.BifrostRerankRequest) { r.Model = "rerank-v3.5" }, "not a Voyage model"},
		"embedding model":          {func(r *schemas.BifrostRerankRequest) { r.Model = "voyage-4" }, "not a rerank model"},
		"empty query":              {func(r *schemas.BifrostRerankRequest) { r.Query = " " }, "needs a query"},
		"no documents":             {func(r *schemas.BifrostRerankRequest) { r.Documents = nil }, "at least one document"},
		"structured-only document": {func(r *schemas.BifrostRerankRequest) {
			r.Documents = []schemas.RerankDocument{{Data: map[string]any{"title": "x"}}}
		}, "no text"},
		"top_n below one": {func(r *schemas.BifrostRerankRequest) { r.Params = &schemas.RerankParameters{TopN: schemas.Ptr(0)} }, "top_n must be at least 1"},
	}
	for name, tt := range invalid {
		t.Run("rejects "+name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, http.StatusOK, `{}`)
			request := valid()
			tt.mutate(request)
			_, bifrostErr := s.provider(t).Rerank(ctx(), testKey, request)
			if bifrostErr == nil || !strings.Contains(bifrostErr.Error.Message, tt.want) {
				t.Fatalf("error = %+v, want message containing %q", bifrostErr, tt.want)
			}
			if s.hits.Load() != 0 {
				t.Error("an invalid request reached the API")
			}
		})
	}
}

// In large payload mode the request body is streamed through unparsed, so the response converters
// see a request with no input, documents or params. A good reply must still convert.
func TestResponsesConvertWhenRequestWasNotParsed(t *testing.T) {
	t.Parallel()
	bare := &schemas.BifrostEmbeddingRequest{Provider: schemas.Voyage, Model: "voyage-4"}

	t.Run("embedding with a base64 vector", func(t *testing.T) {
		t.Parallel()
		response := &voyage.VoyageEmbeddingResponse{
			Data:  []voyage.VoyageEmbeddingItem{{Object: "embedding", Embedding: json.RawMessage(`"AAAAAA=="`), Index: 0}},
			Model: "voyage-4",
			Usage: &voyage.VoyageUsage{TotalTokens: schemas.Ptr(5)},
		}
		converted, err := response.ToBifrostEmbeddingResponse(bare, nil)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		if got := converted.Data[0].Embedding.EmbeddingStr; got == nil || *got != "AAAAAA==" {
			t.Errorf("vector = %+v, want the base64 string kept", converted.Data[0].Embedding)
		}
	})

	t.Run("rerank takes documents from the reply", func(t *testing.T) {
		t.Parallel()
		response := &voyage.VoyageRerankResponse{
			Data:  []voyage.VoyageRerankEntry{{Index: 3, RelevanceScore: schemas.Ptr(0.8), Document: schemas.Ptr("a dog")}},
			Model: "rerank-3",
			Usage: &voyage.VoyageUsage{TotalTokens: schemas.Ptr(9)},
		}
		converted, err := response.ToBifrostRerankResponse(&schemas.BifrostRerankRequest{Provider: schemas.Voyage, Model: "rerank-3"})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		if got := converted.Results[0]; got.Index != 3 || got.Document == nil || got.Document.Text != "a dog" {
			t.Errorf("result = %+v", got)
		}
	})
}

func BenchmarkToVoyageEmbeddingRequest(b *testing.B) {
	request := embeddingRequest("voyage-4", textInput("first passage", "second passage", "third passage"), &schemas.EmbeddingParameters{Dimensions: schemas.Ptr(512), ExtraParams: map[string]any{"input_type": "document"}})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := voyage.ToVoyageEmbeddingRequest(request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkToVoyageRerankRequest(b *testing.B) {
	request := &schemas.BifrostRerankRequest{Provider: schemas.Voyage, Model: "rerank-3", Query: "which animal barks", Documents: []schemas.RerankDocument{{Text: "a cat"}, {Text: "a dog"}, {Text: "a bird"}}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := voyage.ToVoyageRerankRequest(request); err != nil {
			b.Fatal(err)
		}
	}
}
