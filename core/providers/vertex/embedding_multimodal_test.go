package vertex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Vertex resolves gs:// through fileData.fileUri, the only way to embed media too large to
// inline. The embedding path used to run image URLs through the http/https-only allowlist,
// so a GCS URI failed inside the converter - and as a conversion fault, at HTTP 500.
func TestToVertexGeminiEmbeddingRequestAllowsGCSImages(t *testing.T) {
	t.Run("gs image url survives as fileData", func(t *testing.T) {
		req, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "gemini-embedding-2-preview",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("gs://my-bucket/xxx.png")}},
			}}},
		})
		require.NoError(t, err)
		require.Len(t, req.Content.Parts, 1)
		require.NotNil(t, req.Content.Parts[0].FileData)
		require.Equal(t, "gs://my-bucket/xxx.png", req.Content.Parts[0].FileData.FileURI)
	})

	t.Run("a scheme no one allows is a caller error, not a conversion fault", func(t *testing.T) {
		_, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model: "gemini-embedding-2-preview",
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("ftp://my-host/xxx.png")}},
			}}},
		})
		require.Error(t, err)
		require.True(t, providerUtils.IsInvalidRequestError(err), "expected a 400-class error, got %v", err)
	})

	// The other modalities never went through the image allowlist, so a gs:// URI already
	// reached Vertex for them. Pinned so the allowlist cannot be widened to cover them.
	t.Run("audio and video gs urls still pass through", func(t *testing.T) {
		for _, part := range []schemas.EmbeddingContentPart{
			{Type: schemas.EmbeddingContentPartTypeAudio, Audio: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("gs://my-bucket/clip.mp3")}},
			{Type: schemas.EmbeddingContentPartTypeVideo, Video: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("gs://my-bucket/clip.mp4")}},
		} {
			req, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
				Model: "gemini-embedding-2-preview",
				Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{part}}},
			})
			require.NoError(t, err, "part type %s", part.Type)
			require.NotNil(t, req.Content.Parts[0].FileData, "part type %s", part.Type)
		}
	})
}

func TestToVertexGeminiEmbeddingRequest(t *testing.T) {
	text := "hello"
	req, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeText, Text: &text},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/img.png")}},
		}},
		},
		Params: &schemas.EmbeddingParameters{
			TaskType:   schemas.Ptr("RETRIEVAL_DOCUMENT"),
			Dimensions: schemas.Ptr(128),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, req.Content)
	require.Len(t, req.Content.Parts, 2)
	require.Equal(t, "hello", req.Content.Parts[0].Text)
	require.NotNil(t, req.Content.Parts[1].FileData)
	require.Equal(t, 128, *req.OutputDimensionality)
}

// Vertex serves several gemini-embedding-2 inputs online via :batchEmbedContents; a single
// input stays on :embedContent. Vertex requires every entry to carry the same
// outputDimensionality/autoTruncate, which the request-level params fan out to.
func TestVertexGeminiEmbeddingBatch(t *testing.T) {
	type sent struct{ path, body string }
	call := func(t *testing.T, inputs ...string) (chan sent, *schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		t.Helper()
		received := make(chan sent, 1)
		provider, key := vertexOpenAIEndpointTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			received <- sent{r.URL.Path, string(data)}
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, ":batchEmbedContents") {
				_, _ = io.WriteString(w, `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],"usageMetadata":{"promptTokenCount":4,"totalTokenCount":4}}`)
				return
			}
			_, _ = io.WriteString(w, `{"embedding":{"values":[0.1,0.2]},"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1}}`)
		}))
		items := make([]schemas.EmbeddingInputItem, 0, len(inputs))
		for _, in := range inputs {
			items = append(items, schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr(in)}}})
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{
			Provider: schemas.Vertex, Model: "gemini-embedding-2", Input: items,
			Params: &schemas.EmbeddingParameters{TaskType: schemas.Ptr("RETRIEVAL_DOCUMENT"), Dimensions: schemas.Ptr(8), AutoTruncate: schemas.Ptr(true)},
		})
		return received, resp, bifrostErr
	}
	run := func(t *testing.T, inputs ...string) (sent, *schemas.BifrostEmbeddingResponse) {
		t.Helper()
		received, resp, bifrostErr := call(t, inputs...)
		require.Nil(t, bifrostErr)
		return <-received, resp
	}

	t.Run("multiple inputs use batchEmbedContents", func(t *testing.T) {
		call, resp := run(t, "hello", "world")
		require.True(t, strings.HasSuffix(call.path, "/publishers/google/models/gemini-embedding-2:batchEmbedContents"), call.path)
		requests := gjson.Get(call.body, "requests").Array()
		require.Len(t, requests, 2, call.body)
		for i, want := range []string{"hello", "world"} {
			require.Equal(t, want, requests[i].Get("content.parts.0.text").String(), call.body)
			require.Equal(t, int64(8), requests[i].Get("outputDimensionality").Int(), call.body)
			require.True(t, requests[i].Get("autoTruncate").Bool(), call.body)
			require.Equal(t, "RETRIEVAL_DOCUMENT", requests[i].Get("taskType").String(), call.body)
			require.False(t, requests[i].Get("model").Exists(), "vertex entries carry no model: %s", call.body)
		}
		require.Len(t, resp.Data, 2)
		require.Equal(t, 1, resp.Data[1].Index)
		require.Equal(t, []float64{0.3, 0.4}, resp.Data[1].Embedding.EmbeddingArray)
		require.Equal(t, 4, resp.Usage.PromptTokens)
	})

	t.Run("single input stays on embedContent", func(t *testing.T) {
		call, resp := run(t, "hello")
		require.True(t, strings.HasSuffix(call.path, ":embedContent"), call.path)
		require.Equal(t, "hello", gjson.Get(call.body, "content.parts.0.text").String(), call.body)
		require.Len(t, resp.Data, 1)
	})
}

// Request-level embedContentConfig belongs on each entry; at the batch top level Vertex rejects it.
func TestToVertexGeminiBatchEmbeddingRequestPlacesEmbedContentConfigPerEntry(t *testing.T) {
	extra := map[string]interface{}{"embedContentConfig": map[string]interface{}{"documentOcr": true}, "labels": map[string]interface{}{"team": "a"}}
	req, err := ToVertexGeminiBatchEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-2",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("a")}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: schemas.Ptr("b")}}},
		},
		Params: &schemas.EmbeddingParameters{ExtraParams: extra},
	})
	require.NoError(t, err)
	body, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)
	merged, err := providerUtils.MergeExtraParamsIntoJSON(body, req.ExtraParams)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(merged, "embedContentConfig").Exists(), string(merged))
	require.True(t, gjson.GetBytes(merged, "labels").Exists(), string(merged))
	require.True(t, gjson.GetBytes(merged, "requests.1.embedContentConfig.documentOcr").Bool(), string(merged))
	require.Contains(t, extra, "embedContentConfig", "caller's ExtraParams must not be mutated")
}

// :predict takes many instances, so the OpenAI batched form maps onto it directly.
func TestToVertexEmbeddingRequestBatch(t *testing.T) {
	t.Run("each input becomes its own instance", func(t *testing.T) {
		t1 := "Hello!"
		t2 := "World"
		req, err := ToVertexEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t2}}},
			},
			Params: &schemas.EmbeddingParameters{TaskType: schemas.Ptr("RETRIEVAL_DOCUMENT")},
		})
		require.NoError(t, err)
		require.Len(t, req.Instances, 2)
		require.Equal(t, "Hello!", req.Instances[0].Content)
		require.Equal(t, "World", req.Instances[1].Content)
		require.Equal(t, "RETRIEVAL_DOCUMENT", *req.Instances[1].TaskType)
	})

	t.Run("parts within one input still join into one instance", func(t *testing.T) {
		t1 := "first"
		t2 := "second"
		req, err := ToVertexEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: &t1},
				{Type: schemas.EmbeddingContentPartTypeText, Text: &t2},
			}}},
		})
		require.NoError(t, err)
		require.Len(t, req.Instances, 1)
		require.Equal(t, "first\nsecond", req.Instances[0].Content)
	})

	t.Run("a non-text part in any input is rejected", func(t *testing.T) {
		t1 := "fine"
		_, err := ToVertexEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/img.png")}}}},
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "only supports text parts")
	})
}

// documentOcr / audioTrackExtraction are only accepted inside embedContentConfig. Top-level
// keys are not rewritten (Vertex rejects them there), and the caller's params stay intact
// because retries and fallbacks reuse them.
func TestToVertexGeminiEmbeddingRequestForwardsEmbedContentConfig(t *testing.T) {
	text := "alpha"
	convert := func(extra map[string]interface{}) (*VertexGeminiEmbeddingRequest, []byte, []byte) {
		t.Helper()
		req, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Model:  "gemini-embedding-2",
			Input:  []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
			Params: &schemas.EmbeddingParameters{ExtraParams: extra},
		})
		require.NoError(t, err)
		body, err := providerUtils.MarshalSorted(req)
		require.NoError(t, err)
		merged, err := providerUtils.MergeExtraParamsIntoJSON(body, req.ExtraParams)
		require.NoError(t, err)
		return req, body, merged
	}

	t.Run("nested config reaches the wire", func(t *testing.T) {
		extra := map[string]interface{}{"embedContentConfig": map[string]interface{}{"documentOcr": true, "audioTrackExtraction": true}}
		_, body, merged := convert(extra)
		// Without passthrough only the struct is sent; with it the extras are merged on top.
		for _, wire := range [][]byte{body, merged} {
			var out map[string]interface{}
			require.NoError(t, schemas.Unmarshal(wire, &out))
			cfg, _ := out["embedContentConfig"].(map[string]interface{})
			require.Equal(t, true, cfg["documentOcr"], string(wire))
			require.Equal(t, true, cfg["audioTrackExtraction"], string(wire))
		}
		_, again, _ := convert(extra)
		require.Equal(t, string(body), string(again), "a second attempt (retry/fallback) must send the same body")
	})

	t.Run("top-level keys are not rewritten", func(t *testing.T) {
		extra := map[string]interface{}{"documentOcr": true}
		req, body, _ := convert(extra)
		require.Nil(t, req.EmbedContentConfig)
		require.NotContains(t, string(body), "documentOcr")
		require.Contains(t, extra, "documentOcr", "caller's ExtraParams must not be mutated")
	})
}
