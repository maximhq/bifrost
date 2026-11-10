package gemini

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The image converters run once per retry/fallback attempt on the same Bifrost
// request and remove the keys they map into typed fields from the outbound
// ExtraParams. They used to alias the source map, so from the second attempt on
// the typed fields were empty and the Bifrost request had lost the keys.
// Regression for issue #7826; the chat and Responses converters got the same
// fix in #7138.

func imageTestPNG(t *testing.T) []byte {
	t.Helper()
	pngPixel, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
	)
	require.NoError(t, err)
	return pngPixel
}

func geminiImageSafetySettings() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"category":  "HARM_CATEGORY_DANGEROUS_CONTENT",
			"threshold": "BLOCK_ONLY_HIGH",
		},
	}
}

func assertGeminiImageRequestCarriesExtraParams(t *testing.T, attempt int, geminiReq *GeminiGenerationRequest) {
	t.Helper()
	require.NotNil(t, geminiReq, "attempt %d", attempt)
	require.Len(t, geminiReq.SafetySettings, 1, "attempt %d: safetySettings", attempt)
	assert.Equal(t, "HARM_CATEGORY_DANGEROUS_CONTENT", geminiReq.SafetySettings[0].Category, "attempt %d", attempt)
	assert.Equal(t, "BLOCK_ONLY_HIGH", geminiReq.SafetySettings[0].Threshold, "attempt %d", attempt)
	assert.Equal(t, "cachedContents/abc123", geminiReq.CachedContent, "attempt %d: cachedContent", attempt)
	assert.Equal(t, map[string]string{"team": "platform"}, geminiReq.Labels, "attempt %d: labels", attempt)
	// Consumed keys must not leak onto the wire; unrelated passthrough params still do.
	assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, geminiReq.GetExtraParams(), "attempt %d: wire extra params", attempt)
}

func TestToGeminiImageGenerationRequest_ExtraParamsSurviveRetries(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"camelCase": {
			"safetySettings":     geminiImageSafetySettings(),
			"cachedContent":      "cachedContents/abc123",
			"labels":             map[string]interface{}{"team": "platform"},
			"custom_passthrough": "keep-me",
		},
		"snake_case": {
			"safety_settings":    geminiImageSafetySettings(),
			"cached_content":     "cachedContents/abc123",
			"labels":             map[string]interface{}{"team": "platform"},
			"custom_passthrough": "keep-me",
		},
	}
	for name, extraParams := range cases {
		t.Run(name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostImageGenerationRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
				Params:   &schemas.ImageGenerationParameters{ExtraParams: extraParams},
			}

			for attempt := 1; attempt <= 3; attempt++ {
				assertGeminiImageRequestCarriesExtraParams(t, attempt, ToGeminiImageGenerationRequest(bifrostReq))
			}

			// The Bifrost request itself must be left intact for the next attempt.
			assert.Len(t, bifrostReq.Params.ExtraParams, 4)
			assert.Contains(t, bifrostReq.Params.ExtraParams, "labels")
		})
	}
}

func TestToGeminiImageEditRequest_ExtraParamsSurviveRetries(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"camelCase": {
			"safetySettings":     geminiImageSafetySettings(),
			"cachedContent":      "cachedContents/abc123",
			"labels":             map[string]interface{}{"team": "platform"},
			"custom_passthrough": "keep-me",
		},
		"snake_case": {
			"safety_settings":    geminiImageSafetySettings(),
			"cached_content":     "cachedContents/abc123",
			"labels":             map[string]interface{}{"team": "platform"},
			"custom_passthrough": "keep-me",
		},
	}
	for name, extraParams := range cases {
		t.Run(name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostImageEditRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input: &schemas.ImageEditInput{
					Prompt: "make it pop",
					Images: []schemas.ImageInput{{Image: imageTestPNG(t)}},
				},
				Params: &schemas.ImageEditParameters{ExtraParams: extraParams},
			}

			for attempt := 1; attempt <= 3; attempt++ {
				assertGeminiImageRequestCarriesExtraParams(t, attempt, ToGeminiImageEditRequest(bifrostReq))
			}

			assert.Len(t, bifrostReq.Params.ExtraParams, 4)
			assert.Contains(t, bifrostReq.Params.ExtraParams, "labels")
		})
	}
}

func TestToImagenImageGenerationRequest_ExtraParamsSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.Vertex,
		Model:    "imagen-4.0-generate-001",
		Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
		Params: &schemas.ImageGenerationParameters{
			ExtraParams: map[string]interface{}{
				"addWatermark":       true,
				"sampleImageSize":    "2K",
				"aspectRatio":        "16:9",
				"personGeneration":   "allow_adult",
				"language":           "en",
				"enhancePrompt":      false,
				"safetySettings":     geminiImageSafetySettings(),
				"custom_passthrough": "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		req := ToImagenImageGenerationRequest(bifrostReq)
		require.NotNil(t, req, "attempt %d", attempt)
		p := req.Parameters
		require.NotNil(t, p.AddWatermark, "attempt %d: addWatermark", attempt)
		assert.True(t, *p.AddWatermark, "attempt %d", attempt)
		require.NotNil(t, p.SampleImageSize, "attempt %d: sampleImageSize", attempt)
		assert.Equal(t, "2K", *p.SampleImageSize, "attempt %d", attempt)
		require.NotNil(t, p.AspectRatio, "attempt %d: aspectRatio", attempt)
		assert.Equal(t, "16:9", *p.AspectRatio, "attempt %d", attempt)
		require.NotNil(t, p.PersonGeneration, "attempt %d: personGeneration", attempt)
		assert.Equal(t, "allow_adult", *p.PersonGeneration, "attempt %d", attempt)
		require.NotNil(t, p.Language, "attempt %d: language", attempt)
		assert.Equal(t, "en", *p.Language, "attempt %d", attempt)
		require.NotNil(t, p.EnhancePrompt, "attempt %d: enhancePrompt", attempt)
		assert.False(t, *p.EnhancePrompt, "attempt %d", attempt)
		require.Len(t, p.SafetySettings, 1, "attempt %d: safetySettings", attempt)
		assert.Equal(t, "HARM_CATEGORY_DANGEROUS_CONTENT", p.SafetySettings[0].Category, "attempt %d", attempt)
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, req.GetExtraParams(), "attempt %d: wire extra params", attempt)
	}

	assert.Len(t, bifrostReq.Params.ExtraParams, 8)
	assert.Contains(t, bifrostReq.Params.ExtraParams, "personGeneration")
}

func TestToImagenImageEditRequest_ExtraParamsSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostImageEditRequest{
		Provider: schemas.Vertex,
		Model:    "imagen-3.0-capability-001",
		Input: &schemas.ImageEditInput{
			Prompt: "replace the background with a beach",
			Images: []schemas.ImageInput{{Image: imageTestPNG(t)}},
		},
		Params: &schemas.ImageEditParameters{
			ExtraParams: map[string]interface{}{
				"maskMode":                "MASK_MODE_BACKGROUND",
				"dilation":                0.05,
				"maskClasses":             []interface{}{float64(1), float64(2)},
				"editMode":                "EDIT_MODE_BGSWAP",
				"guidanceScale":           60,
				"baseSteps":               35,
				"addWatermark":            false,
				"includeRaiReason":        true,
				"includeSafetyAttributes": true,
				"personGeneration":        "allow_adult",
				"language":                "en",
				"storageUri":              "gs://bucket/out/",
				"safetySettings":          geminiImageSafetySettings(),
				"custom_passthrough":      "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		req := ToImagenImageEditRequest(bifrostReq)
		require.NotNil(t, req, "attempt %d", attempt)

		require.Len(t, req.Instances, 1, "attempt %d", attempt)
		var mask *ImagenReferenceImage
		for i := range req.Instances[0].ReferenceImages {
			if req.Instances[0].ReferenceImages[i].ReferenceType == "REFERENCE_TYPE_MASK" {
				mask = &req.Instances[0].ReferenceImages[i]
			}
		}
		require.NotNil(t, mask, "attempt %d: mask reference from maskMode", attempt)
		require.NotNil(t, mask.MaskImageConfig, "attempt %d", attempt)
		assert.Equal(t, "MASK_MODE_BACKGROUND", mask.MaskImageConfig.MaskMode, "attempt %d: maskMode", attempt)
		require.NotNil(t, mask.MaskImageConfig.Dilation, "attempt %d: dilation", attempt)
		assert.InDelta(t, 0.05, *mask.MaskImageConfig.Dilation, 1e-9, "attempt %d", attempt)
		assert.Equal(t, []int{1, 2}, mask.MaskImageConfig.MaskClasses, "attempt %d: maskClasses", attempt)

		p := req.Parameters
		require.NotNil(t, p.EditMode, "attempt %d: editMode", attempt)
		assert.Equal(t, "EDIT_MODE_BGSWAP", *p.EditMode, "attempt %d", attempt)
		require.NotNil(t, p.GuidanceScale, "attempt %d: guidanceScale", attempt)
		assert.Equal(t, 60, *p.GuidanceScale, "attempt %d", attempt)
		require.NotNil(t, p.BaseSteps, "attempt %d: baseSteps", attempt)
		assert.Equal(t, 35, *p.BaseSteps, "attempt %d", attempt)
		require.NotNil(t, p.AddWatermark, "attempt %d: addWatermark", attempt)
		assert.False(t, *p.AddWatermark, "attempt %d", attempt)
		require.NotNil(t, p.IncludeRaiReason, "attempt %d: includeRaiReason", attempt)
		assert.True(t, *p.IncludeRaiReason, "attempt %d", attempt)
		require.NotNil(t, p.IncludeSafetyAttributes, "attempt %d: includeSafetyAttributes", attempt)
		assert.True(t, *p.IncludeSafetyAttributes, "attempt %d", attempt)
		require.NotNil(t, p.PersonGeneration, "attempt %d: personGeneration", attempt)
		assert.Equal(t, "allow_adult", *p.PersonGeneration, "attempt %d", attempt)
		require.NotNil(t, p.Language, "attempt %d: language", attempt)
		assert.Equal(t, "en", *p.Language, "attempt %d", attempt)
		require.NotNil(t, p.StorageUri, "attempt %d: storageUri", attempt)
		assert.Equal(t, "gs://bucket/out/", *p.StorageUri, "attempt %d", attempt)
		require.Len(t, p.SafetySettings, 1, "attempt %d: safetySettings", attempt)
		assert.Equal(t, "HARM_CATEGORY_DANGEROUS_CONTENT", p.SafetySettings[0].Category, "attempt %d", attempt)
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, req.GetExtraParams(), "attempt %d: wire extra params", attempt)
	}

	assert.Len(t, bifrostReq.Params.ExtraParams, 14)
	assert.Contains(t, bifrostReq.Params.ExtraParams, "maskMode")
}

const geminiImageMimeTypePath = "generationConfig.imageConfig.imageOutputOptions.mimeType"

// Bifrost output_format must reach the Vertex Gemini wire body as
// generationConfig.imageConfig.imageOutputOptions.mimeType.
func TestToGeminiImageGenerationRequest_OutputFormatMapsToMimeType(t *testing.T) {
	cases := map[string]struct {
		format string
		want   string
	}{
		"png":       {format: "png", want: "image/png"},
		"jpeg":      {format: "jpeg", want: "image/jpeg"},
		"jpg_upper": {format: "JPG", want: "image/jpeg"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			format := tc.format
			bifrostReq := &schemas.BifrostImageGenerationRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
				Params:   &schemas.ImageGenerationParameters{OutputFormat: &format},
			}
			body := geminiImageWireBody(t, ToGeminiImageGenerationRequest(bifrostReq))
			assert.Equal(t, tc.want, gjson.GetBytes(body, geminiImageMimeTypePath).String(), "body %s", body)
		})
	}
}

func TestToGeminiImageEditRequest_OutputFormatMapsToMimeType(t *testing.T) {
	cases := map[string]struct {
		format string
		want   string
	}{
		"png":       {format: "png", want: "image/png"},
		"jpeg":      {format: "jpeg", want: "image/jpeg"},
		"jpg_upper": {format: "JPG", want: "image/jpeg"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			format := tc.format
			bifrostReq := &schemas.BifrostImageEditRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input: &schemas.ImageEditInput{
					Prompt: "make it pop",
					Images: []schemas.ImageInput{{Image: imageTestPNG(t)}},
				},
				Params: &schemas.ImageEditParameters{OutputFormat: &format},
			}
			body := geminiImageWireBody(t, ToGeminiImageEditRequest(bifrostReq))
			assert.Equal(t, tc.want, gjson.GetBytes(body, geminiImageMimeTypePath).String(), "body %s", body)
		})
	}
}

// An absent or unmappable output_format (webp included) must not fail the request or emit an imageOutputOptions block.
func TestGeminiImageRequests_UnsupportedOrUnsetOutputFormatOmitsImageOutputOptions(t *testing.T) {
	tiff := "tiff"
	webp := "webp"
	cases := map[string]*string{
		"unset":       nil,
		"unsupported": &tiff,
		"webp":        &webp, // Gemini image models accept only image/png and image/jpeg
	}
	for name, format := range cases {
		t.Run("generation_"+name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostImageGenerationRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
				Params:   &schemas.ImageGenerationParameters{OutputFormat: format},
			}
			body := geminiImageWireBody(t, ToGeminiImageGenerationRequest(bifrostReq))
			assert.False(t, gjson.GetBytes(body, "generationConfig.imageConfig.imageOutputOptions").Exists(), "body %s", body)
		})
		t.Run("edit_"+name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostImageEditRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input: &schemas.ImageEditInput{
					Prompt: "make it pop",
					Images: []schemas.ImageInput{{Image: imageTestPNG(t)}},
				},
				Params: &schemas.ImageEditParameters{OutputFormat: format},
			}
			body := geminiImageWireBody(t, ToGeminiImageEditRequest(bifrostReq))
			assert.False(t, gjson.GetBytes(body, "generationConfig.imageConfig.imageOutputOptions").Exists(), "body %s", body)
		})
	}
}

// output_compression rides along as compressionQuality only with JPEG; it means nothing for PNG.
func TestGeminiImageRequests_OutputCompressionMapsToCompressionQuality(t *testing.T) {
	const qualityPath = "generationConfig.imageConfig.imageOutputOptions.compressionQuality"
	cases := map[string]struct {
		format      *string
		compression *int
		wantMime    string
		wantQuality *int64
	}{
		"jpeg_with_compression": {format: schemas.Ptr("jpeg"), compression: schemas.Ptr(60), wantMime: "image/jpeg", wantQuality: schemas.Ptr(int64(60))},
		"png_with_compression":  {format: schemas.Ptr("png"), compression: schemas.Ptr(60), wantMime: "image/png"},
		"compression_only":      {compression: schemas.Ptr(60)},
	}
	for name, tc := range cases {
		check := func(t *testing.T, body []byte) {
			assert.Equal(t, tc.wantMime, gjson.GetBytes(body, geminiImageMimeTypePath).String(), "body %s", body)
			quality := gjson.GetBytes(body, qualityPath)
			if tc.wantQuality == nil {
				assert.False(t, quality.Exists(), "body %s", body)
				return
			}
			assert.Equal(t, *tc.wantQuality, quality.Int(), "body %s", body)
		}
		t.Run("generation_"+name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostImageGenerationRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
				Params:   &schemas.ImageGenerationParameters{OutputFormat: tc.format, OutputCompression: tc.compression},
			}
			check(t, geminiImageWireBody(t, ToGeminiImageGenerationRequest(bifrostReq)))
		})
		t.Run("edit_"+name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostImageEditRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input: &schemas.ImageEditInput{
					Prompt: "make it pop",
					Images: []schemas.ImageInput{{Image: imageTestPNG(t)}},
				},
				Params: &schemas.ImageEditParameters{OutputFormat: tc.format, OutputCompression: tc.compression},
			}
			check(t, geminiImageWireBody(t, ToGeminiImageEditRequest(bifrostReq)))
		})
	}
}

// The Gemini API rejects imageConfig.imageOutputOptions (Google's SDK refuses output_mime_type
// outside Vertex), so the direct Gemini provider must never send it, while keeping the rest of imageConfig.
func TestGeminiProviderImageRequests_OmitImageOutputOptionsOnWire(t *testing.T) {
	const okBody = `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}}]}`
	cases := map[string]struct {
		aspectRatio *string
		wantConfig  bool
	}{
		"output_format_only":     {},
		"output_format_and_size": {aspectRatio: schemas.Ptr("3:2"), wantConfig: true},
	}
	for name, tc := range cases {
		for _, op := range []string{"generation", "edit"} {
			t.Run(op+"_"+name, func(t *testing.T) {
				var captured []byte
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					captured, _ = io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(okBody))
				}))
				defer ts.Close()

				provider := NewGeminiProvider(&schemas.ProviderConfig{
					NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
				}, testNoopLogger{})
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}

				var bifrostErr *schemas.BifrostError
				if op == "generation" {
					_, bifrostErr = provider.ImageGeneration(ctx, key, &schemas.BifrostImageGenerationRequest{
						Provider: schemas.Gemini,
						Model:    "gemini-2.5-flash-image",
						Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
						Params: &schemas.ImageGenerationParameters{
							OutputFormat: schemas.Ptr("jpeg"), OutputCompression: schemas.Ptr(60), AspectRatio: tc.aspectRatio,
						},
					})
				} else {
					_, bifrostErr = provider.ImageEdit(ctx, key, &schemas.BifrostImageEditRequest{
						Provider: schemas.Gemini,
						Model:    "gemini-2.5-flash-image",
						Input: &schemas.ImageEditInput{
							Prompt: "make it pop",
							Images: []schemas.ImageInput{{Image: imageTestPNG(t)}},
						},
						Params: &schemas.ImageEditParameters{
							OutputFormat: schemas.Ptr("jpeg"), OutputCompression: schemas.Ptr(60), AspectRatio: tc.aspectRatio,
						},
					})
				}
				require.Nil(t, bifrostErr)
				require.NotEmpty(t, captured)
				assert.False(t, gjson.GetBytes(captured, "generationConfig.imageConfig.imageOutputOptions").Exists(), "body %s", captured)
				assert.Equal(t, tc.wantConfig, gjson.GetBytes(captured, "generationConfig.imageConfig").Exists(), "body %s", captured)
				if tc.wantConfig {
					assert.Equal(t, "3:2", gjson.GetBytes(captured, "generationConfig.imageConfig.aspectRatio").String(), "body %s", captured)
				}
			})
		}
	}
}

// A GenAI-ingress request's imageConfig.imageOutputOptions must survive into Bifrost params so it
// reaches Vertex (or another provider's output_format) instead of being dropped.
func TestGeminiImageRequests_GenAIImageOutputOptionsMapToBifrostParams(t *testing.T) {
	quality := 70
	geminiReq := &GeminiGenerationRequest{
		Model:    "vertex/gemini-2.5-flash-image",
		Contents: []Content{{Role: "user", Parts: []*Part{{Text: "a red apple"}}}},
		GenerationConfig: GenerationConfig{
			ResponseModalities: []Modality{ModalityImage},
			ImageConfig: &GeminiImageConfig{
				ImageOutputOptions: &GeminiImageOutputOptions{MimeType: "image/jpeg", CompressionQuality: &quality},
			},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	genReq := geminiReq.ToBifrostImageGenerationRequest(ctx)
	require.NotNil(t, genReq)
	require.NotNil(t, genReq.Params.OutputFormat)
	assert.Equal(t, "jpeg", *genReq.Params.OutputFormat)
	require.NotNil(t, genReq.Params.OutputCompression)
	assert.Equal(t, 70, *genReq.Params.OutputCompression)

	editReq := geminiReq.ToBifrostImageEditRequest(ctx)
	require.NotNil(t, editReq)
	require.NotNil(t, editReq.Params.OutputFormat)
	assert.Equal(t, "jpeg", *editReq.Params.OutputFormat)
	require.NotNil(t, editReq.Params.OutputCompression)
	assert.Equal(t, 70, *editReq.Params.OutputCompression)

	// Round trip: the Bifrost params rebuild the same Vertex wire block.
	body := geminiImageWireBody(t, ToGeminiImageGenerationRequest(genReq))
	assert.Equal(t, "image/jpeg", gjson.GetBytes(body, geminiImageMimeTypePath).String(), "body %s", body)
	assert.Equal(t, int64(70), gjson.GetBytes(body, "generationConfig.imageConfig.imageOutputOptions.compressionQuality").Int(), "body %s", body)
}

func geminiImageWireBody(t *testing.T, geminiReq *GeminiGenerationRequest) []byte {
	t.Helper()
	require.NotNil(t, geminiReq)
	body, err := providerUtils.MarshalSorted(geminiReq)
	require.NoError(t, err)
	return body
}

var benchmarkGeminiImageRequest *GeminiGenerationRequest

func BenchmarkToGeminiImageGenerationRequest(b *testing.B) {
	jpeg := "jpeg"
	cases := []struct {
		name   string
		format *string
	}{
		{name: "unset"},
		{name: "jpeg", format: &jpeg},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			bifrostReq := &schemas.BifrostImageGenerationRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input:    &schemas.ImageGenerationInput{Prompt: "a red apple"},
				Params:   &schemas.ImageGenerationParameters{OutputFormat: tc.format},
			}
			var result *GeminiGenerationRequest
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				result = ToGeminiImageGenerationRequest(bifrostReq)
			}
			b.StopTimer()
			benchmarkGeminiImageRequest = result
		})
	}
}

func BenchmarkToGeminiImageEditRequest(b *testing.B) {
	pngPixel, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
	)
	if err != nil {
		b.Fatal(err)
	}
	jpeg := "jpeg"
	cases := []struct {
		name   string
		format *string
	}{
		{name: "unset"},
		{name: "jpeg", format: &jpeg},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			bifrostReq := &schemas.BifrostImageEditRequest{
				Provider: schemas.Vertex,
				Model:    "gemini-2.5-flash-image",
				Input: &schemas.ImageEditInput{
					Prompt: "make it pop",
					Images: []schemas.ImageInput{{Image: pngPixel}},
				},
				Params: &schemas.ImageEditParameters{OutputFormat: tc.format},
			}
			var result *GeminiGenerationRequest
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				result = ToGeminiImageEditRequest(bifrostReq)
			}
			b.StopTimer()
			benchmarkGeminiImageRequest = result
		})
	}
}
