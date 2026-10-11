package gemini

import (
	"encoding/base64"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
