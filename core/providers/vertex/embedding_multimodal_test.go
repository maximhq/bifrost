package vertex

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/tidwall/gjson"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
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

func TestToVertexGeminiEmbeddingRequestRejectsBatch(t *testing.T) {
	t1 := "first"
	t2 := "second"
	_, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t2}}},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "batch")
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

// Character counts and token counts are different observed billing units.
func TestEmbeddingCharacterMeasurements(t *testing.T) {
	cases := []struct {
		name   string
		wire   string
		chars  *int
		tokens *int
	}{
		{"empty vectors", `{"predictions":[],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
		{"characters", `{"predictions":[{"embeddings":{"values":[0.1,0.2]}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
		{"zero characters", `{"predictions":[{"embeddings":{"values":[0.1,0.2]}}],"metadata":{"billableCharacterCount":0}}`, schemas.Ptr(0), nil},
		{"absent character field", `{"predictions":[{"embeddings":{"values":[0.1,0.2]}}],"metadata":{}}`, nil, nil},
		{"null characters", `{"predictions":[{"embeddings":{"values":[0.1,0.2]}}],"metadata":{"billableCharacterCount":null}}`, nil, nil},
		{"independent tokens and characters", `{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":3}}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), schemas.Ptr(3)},
		{"empty token statistics", `{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{}}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
		{"explicit zero tokens", `{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":0}}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), schemas.Ptr(0)},
		{"partial batch statistics", `{"predictions":[{"embeddings":{"values":[0.1],"statistics":{"token_count":3}}},{"embeddings":{"values":[0.2]}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded VertexEmbeddingResponse
			require.NoError(t, schemas.Unmarshal([]byte(tc.wire), &decoded))
			got := decoded.ToBifrostEmbeddingResponse()
			require.NotNil(t, got)
			if tc.name == "empty vectors" {
				require.Empty(t, got.Data)
			}
			body, err := schemas.Marshal(got)
			require.NoError(t, err)
			chars := gjson.GetBytes(body, "usage.input_characters")
			if tc.chars == nil {
				require.False(t, chars.Exists(), string(body))
			} else {
				require.Equal(t, gjson.Number, chars.Type, string(body))
				require.EqualValues(t, *tc.chars, chars.Int(), string(body))
			}
			input, output, total := got.Usage.TokenMeasurements()
			require.Nil(t, output, string(body))
			if tc.tokens == nil {
				require.Nil(t, input, string(body))
				require.Nil(t, total, string(body))
			} else {
				require.NotNil(t, input, string(body))
				require.Equal(t, *tc.tokens, *input)
				require.NotNil(t, total, string(body))
				require.Equal(t, *tc.tokens, *total)
			}
		})
	}
}

func BenchmarkEmbeddingCharacterMeasurements(b *testing.B) {
	for _, metadata := range []string{"", `,"metadata":{"billableCharacterCount":17}`} {
		b.Run(fmt.Sprintf("characters=%t", metadata != ""), func(b *testing.B) {
			var decoded VertexEmbeddingResponse
			if err := schemas.Unmarshal([]byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":3}}}]`+metadata+`}`), &decoded); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = decoded.ToBifrostEmbeddingResponse()
			}
		})
	}
}

func TestEmbeddingCharacterHTTPReceipt(t *testing.T) {
	for _, model := range []string{"gemini-embedding-2-preview", "text-embedding-005"} {
		for _, chars := range []int{0, 17} {
			t.Run(fmt.Sprintf("%s/%d", model, chars), func(t *testing.T) {
				requests := make(chan string, 1)
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests <- r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					if model == "gemini-embedding-2-preview" {
						fmt.Fprintf(w, `{"embedding":{"values":[0.1,0.2]},"metadata":{"billableCharacterCount":%d}}`, chars)
					} else {
						fmt.Fprintf(w, `{"predictions":[{"embeddings":{"values":[0.1,0.2]}}],"metadata":{"billableCharacterCount":%d}}`, chars)
					}
				}))
				defer upstream.Close()
				config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10}}
				config.CheckAndSetDefaults()
				provider, err := NewVertexProvider(config, nil)
				require.NoError(t, err)
				// Redirect only this provider instance; no global transport or external OAuth call.
				provider.client.Dial = func(string) (net.Conn, error) { return net.Dial("tcp", upstream.Listener.Addr().String()) }
				provider.client.TLSConfig = &tls.Config{InsecureSkipVerify: true}
				text := "fixture"
				response, failure := provider.Embedding(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
					schemas.Key{Value: *schemas.NewSecretVar("test-key"), VertexKeyConfig: &schemas.VertexKeyConfig{ProjectID: *schemas.NewSecretVar("fixture-project"), Region: *schemas.NewSecretVar("us-central1")}},
					&schemas.BifrostEmbeddingRequest{Provider: schemas.Vertex, Model: model, Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}}})
				require.Nil(t, failure)
				require.NotNil(t, response)
				suffix := ":predict"
				if model == "gemini-embedding-2-preview" {
					suffix = ":embedContent"
				}
				require.Contains(t, <-requests, "/models/"+model+suffix)
				require.NotNil(t, response.Usage.InputCharacters)
				require.Equal(t, chars, *response.Usage.InputCharacters)
				input, output, total := response.Usage.TokenMeasurements()
				require.Nil(t, input)
				require.Nil(t, output)
				require.Nil(t, total)
			})
		}
	}
}

func BenchmarkEmbeddingDecodeUnits(b *testing.B) {
	for _, metadata := range []string{"", `,"metadata":{"billableCharacterCount":17}`} {
		b.Run(fmt.Sprintf("characters=%t", metadata != ""), func(b *testing.B) {
			raw := []byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":3}}}]` + metadata + `}`)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				var decoded VertexEmbeddingResponse
				if err := schemas.Unmarshal(raw, &decoded); err != nil {
					b.Fatal(err)
				}
				_ = decoded.ToBifrostEmbeddingResponse()
			}
		})
	}
}
