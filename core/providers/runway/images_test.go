package runway

import (
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeImageReq(model, prompt string, params *schemas.ImageGenerationParameters) *schemas.BifrostImageGenerationRequest {
	return &schemas.BifrostImageGenerationRequest{
		Model:  model,
		Input:  &schemas.ImageGenerationInput{Prompt: prompt},
		Params: params,
	}
}

func TestToRunwayImageGenerationRequest(t *testing.T) {
	t.Run("defaults_ratio_when_unset", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "a cat", nil))
		require.NoError(t, err)
		assert.Equal(t, "gen4_image", result.Model)
		assert.Equal(t, "a cat", result.PromptText)
		assert.Equal(t, "1024:1024", result.Ratio)
	})

	t.Run("defaults_ratio_auto_for_gpt_image_2", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gpt_image_2", "a cat", nil))
		require.NoError(t, err)
		assert.Equal(t, "auto", result.Ratio)
	})

	t.Run("nil_input_errors", func(t *testing.T) {
		_, err := ToRunwayImageGenerationRequest(&schemas.BifrostImageGenerationRequest{Model: "gen4_image"})
		require.Error(t, err)
	})

	t.Run("aspect_ratio_takes_precedence_over_size", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "a cat", &schemas.ImageGenerationParameters{
			AspectRatio: schemas.Ptr("1080:1920"),
			Size:        schemas.Ptr("1280x720"),
		}))
		require.NoError(t, err)
		assert.Equal(t, "1080:1920", result.Ratio)
	})

	t.Run("size_converted_to_ratio", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "a cat", &schemas.ImageGenerationParameters{
			Size: schemas.Ptr("1280x720"),
		}))
		require.NoError(t, err)
		assert.Equal(t, "1280:720", result.Ratio)
	})

	t.Run("seed_mapped", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "a cat", &schemas.ImageGenerationParameters{
			Seed: schemas.Ptr(42),
		}))
		require.NoError(t, err)
		require.NotNil(t, result.Seed)
		assert.Equal(t, 42, *result.Seed)
	})

	t.Run("input_images_mapped_to_reference_images", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "@ref a cat", &schemas.ImageGenerationParameters{
			InputImages: []string{"https://example.com/ref.jpg"},
		}))
		require.NoError(t, err)
		require.Len(t, result.ReferenceImages, 1)
		assert.Equal(t, "https://example.com/ref.jpg", result.ReferenceImages[0].URI)
		assert.Empty(t, result.ReferenceImages[0].Tag)
	})

	t.Run("explicit_reference_images_override_input_images", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "@logo a cat", &schemas.ImageGenerationParameters{
			InputImages: []string{"https://example.com/input.jpg"},
			ExtraParams: map[string]interface{}{
				"reference_images": []ReferenceImage{{URI: "https://example.com/ref.jpg", Tag: "logo"}},
			},
		}))
		require.NoError(t, err)
		require.Len(t, result.ReferenceImages, 1)
		assert.Equal(t, "https://example.com/ref.jpg", result.ReferenceImages[0].URI)
		assert.Equal(t, "logo", result.ReferenceImages[0].Tag)
		assert.NotContains(t, result.ExtraParams, "reference_images")
	})

	t.Run("map_fallback_content_moderation", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "a cat", &schemas.ImageGenerationParameters{
			ExtraParams: map[string]interface{}{
				"content_moderation": map[string]interface{}{"public_figure_threshold": "low"},
			},
		}))
		require.NoError(t, err)
		require.NotNil(t, result.ContentModeration)
		require.NotNil(t, result.ContentModeration.PublicFigureThreshold)
		assert.Equal(t, "low", *result.ContentModeration.PublicFigureThreshold)
		assert.NotContains(t, result.ExtraParams, "content_moderation")
	})

	t.Run("gpt_image_2_fields_attached", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gpt_image_2", "a cat", &schemas.ImageGenerationParameters{
			N:          schemas.Ptr(3),
			Quality:    schemas.Ptr("high"),
			Background: schemas.Ptr("opaque"),
			Seed:       schemas.Ptr(7),
		}))
		require.NoError(t, err)
		require.NotNil(t, result.OutputCount)
		assert.Equal(t, 3, *result.OutputCount)
		require.NotNil(t, result.Quality)
		assert.Equal(t, "high", *result.Quality)
		require.NotNil(t, result.Background)
		assert.Equal(t, "opaque", *result.Background)
		assert.Nil(t, result.Seed, "gpt_image_2 does not support seed")
	})

	t.Run("gen4_drops_unsupported_fields", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "a cat", &schemas.ImageGenerationParameters{
			N:          schemas.Ptr(3),
			Quality:    schemas.Ptr("high"),
			Background: schemas.Ptr("opaque"),
			Seed:       schemas.Ptr(7),
		}))
		require.NoError(t, err)
		assert.Nil(t, result.OutputCount, "gen4_image does not support outputCount")
		assert.Nil(t, result.Quality, "gen4_image does not support quality")
		assert.Nil(t, result.Background, "gen4_image does not support background")
		require.NotNil(t, result.Seed)
		assert.Equal(t, 7, *result.Seed)
	})

	t.Run("content_moderation_dropped_for_unsupported_model", func(t *testing.T) {
		result, err := ToRunwayImageGenerationRequest(makeImageReq("gpt_image_2", "a cat", &schemas.ImageGenerationParameters{
			ExtraParams: map[string]interface{}{
				"content_moderation": map[string]interface{}{"public_figure_threshold": "low"},
			},
		}))
		require.NoError(t, err)
		assert.Nil(t, result.ContentModeration, "gpt_image_2 does not support content moderation")
		assert.NotContains(t, result.ExtraParams, "content_moderation", "key must not leak into body")
	})

	t.Run("reference_subject_kept_for_gemini_stripped_otherwise", func(t *testing.T) {
		refImages := []ReferenceImage{{URI: "https://example.com/ref.jpg", Tag: "hero", Subject: "human"}}

		gemini, err := ToRunwayImageGenerationRequest(makeImageReq("gemini_image3_pro", "@hero a cat", &schemas.ImageGenerationParameters{
			ExtraParams: map[string]interface{}{"reference_images": refImages},
		}))
		require.NoError(t, err)
		require.Len(t, gemini.ReferenceImages, 1)
		assert.Equal(t, "human", gemini.ReferenceImages[0].Subject)

		gen4, err := ToRunwayImageGenerationRequest(makeImageReq("gen4_image", "@hero a cat", &schemas.ImageGenerationParameters{
			ExtraParams: map[string]interface{}{"reference_images": []ReferenceImage{{URI: "https://example.com/ref.jpg", Tag: "hero", Subject: "human"}}},
		}))
		require.NoError(t, err)
		require.Len(t, gen4.ReferenceImages, 1)
		assert.Empty(t, gen4.ReferenceImages[0].Subject, "non-gemini models must not send subject")
	})
}

func makeImageEditReq(model, prompt string, images [][]byte, params *schemas.ImageEditParameters) *schemas.BifrostImageEditRequest {
	imgs := make([]schemas.ImageInput, len(images))
	for i, b := range images {
		imgs[i] = schemas.ImageInput{Image: b}
	}
	return &schemas.BifrostImageEditRequest{
		Model:  model,
		Input:  &schemas.ImageEditInput{Images: imgs, Prompt: prompt},
		Params: params,
	}
}

func TestToRunwayImageEditRequest(t *testing.T) {
	t.Run("nil_input_errors", func(t *testing.T) {
		_, err := ToRunwayImageEditRequest(&schemas.BifrostImageEditRequest{Model: "gen4_image"})
		require.Error(t, err)
	})

	t.Run("maps_image_bytes_to_data_uri_reference_images", func(t *testing.T) {
		// minimal PNG header bytes so DetectContentType yields image/png
		png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
		result, err := ToRunwayImageEditRequest(makeImageEditReq("gen4_image", "make it blue", [][]byte{png}, nil))
		require.NoError(t, err)
		assert.Equal(t, "make it blue", result.PromptText)
		assert.Equal(t, "1024:1024", result.Ratio)
		require.Len(t, result.ReferenceImages, 1)
		assert.True(t, strings.HasPrefix(result.ReferenceImages[0].URI, "data:image/png;base64,"), "got %s", result.ReferenceImages[0].URI)
	})

	t.Run("skips_empty_images", func(t *testing.T) {
		result, err := ToRunwayImageEditRequest(makeImageEditReq("gen4_image", "x", [][]byte{{}, {0x89, 0x50, 0x4E, 0x47}}, nil))
		require.NoError(t, err)
		require.Len(t, result.ReferenceImages, 1)
	})

	t.Run("size_converted_to_ratio", func(t *testing.T) {
		result, err := ToRunwayImageEditRequest(makeImageEditReq("gen4_image", "x", [][]byte{{0x89, 0x50}}, &schemas.ImageEditParameters{
			Size: schemas.Ptr("1280x720"),
		}))
		require.NoError(t, err)
		assert.Equal(t, "1280:720", result.Ratio)
	})

	t.Run("explicit_reference_images_appended_after_edit_images", func(t *testing.T) {
		result, err := ToRunwayImageEditRequest(makeImageEditReq("gen4_image", "@logo x", [][]byte{{0x89, 0x50}}, &schemas.ImageEditParameters{
			ExtraParams: map[string]interface{}{
				"reference_images": []ReferenceImage{{URI: "https://example.com/logo.png", Tag: "logo"}},
			},
		}))
		require.NoError(t, err)
		require.Len(t, result.ReferenceImages, 2)
		assert.True(t, strings.HasPrefix(result.ReferenceImages[0].URI, "data:"))
		assert.Equal(t, "https://example.com/logo.png", result.ReferenceImages[1].URI)
		assert.Equal(t, "logo", result.ReferenceImages[1].Tag)
		assert.NotContains(t, result.ExtraParams, "reference_images")
	})

	t.Run("gen4_drops_unsupported_fields", func(t *testing.T) {
		result, err := ToRunwayImageEditRequest(makeImageEditReq("gen4_image", "x", [][]byte{{0x89, 0x50}}, &schemas.ImageEditParameters{
			N:          schemas.Ptr(3),
			Quality:    schemas.Ptr("high"),
			Background: schemas.Ptr("opaque"),
			Seed:       schemas.Ptr(7),
		}))
		require.NoError(t, err)
		assert.Nil(t, result.OutputCount)
		assert.Nil(t, result.Quality)
		assert.Nil(t, result.Background)
		require.NotNil(t, result.Seed)
		assert.Equal(t, 7, *result.Seed)
	})
}

func TestToBifrostImageGenerationResponse(t *testing.T) {
	t.Run("nil_task_details_errors", func(t *testing.T) {
		_, err := ToBifrostImageGenerationResponse(nil)
		require.NotNil(t, err)
	})

	t.Run("maps_output_urls", func(t *testing.T) {
		taskDetails := &RunwayTaskDetailsResponse{
			ID:        "task-123",
			Status:    RunwayTaskStatusSucceeded,
			CreatedAt: "2024-11-06T12:00:00Z",
			Output:    []string{"https://example.com/0.png", "https://example.com/1.png"},
		}
		resp, err := ToBifrostImageGenerationResponse(taskDetails)
		require.Nil(t, err)
		assert.Equal(t, "task-123", resp.ID)
		require.Len(t, resp.Data, 2)
		assert.Equal(t, "https://example.com/0.png", resp.Data[0].URL)
		assert.Equal(t, 0, resp.Data[0].Index)
		assert.Equal(t, "https://example.com/1.png", resp.Data[1].URL)
		assert.Equal(t, 1, resp.Data[1].Index)
		assert.NotZero(t, resp.Created)
	})
}
