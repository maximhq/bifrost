package runway

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeVideoReq(model string, extraParams map[string]interface{}) *schemas.BifrostVideoGenerationRequest {
	return &schemas.BifrostVideoGenerationRequest{
		Model: model,
		Input: &schemas.VideoGenerationInput{
			Prompt: "test prompt",
		},
		Params: &schemas.VideoGenerationParameters{
			ExtraParams: extraParams,
		},
	}
}

func TestToRunwayVideoGenerationRequest_References(t *testing.T) {
	t.Run("direct_typed_references", func(t *testing.T) {
		refs := []Reference{{Type: "image", URI: "https://example.com/img.jpg"}}
		req := makeVideoReq("gen3", map[string]interface{}{
			"references": refs,
		})

		result, err := ToRunwayVideoGenerationRequest(req)
		require.NoError(t, err)
		require.Len(t, result.References, 1)
		assert.Equal(t, "image", result.References[0].Type)
		assert.Equal(t, "https://example.com/img.jpg", result.References[0].URI)
		assert.NotContains(t, result.ExtraParams, "references")
	})

	t.Run("map_fallback_references", func(t *testing.T) {
		// Simulates what happens when references arrive via JSON deserialization
		req := makeVideoReq("gen3", map[string]interface{}{
			"references": []interface{}{
				map[string]interface{}{"type": "image", "uri": "https://example.com/img.jpg"},
			},
		})

		result, err := ToRunwayVideoGenerationRequest(req)
		require.NoError(t, err)
		require.Len(t, result.References, 1, "ConvertViaJSON fallback should convert map-based references")
		assert.Equal(t, "image", result.References[0].Type)
		assert.Equal(t, "https://example.com/img.jpg", result.References[0].URI)
		assert.NotContains(t, result.ExtraParams, "references")
	})
}

func TestToRunwayVideoGenerationRequest_ReferenceImages(t *testing.T) {
	t.Run("direct_typed_reference_images", func(t *testing.T) {
		refImages := []ReferenceImage{{URI: "https://example.com/ref.jpg", Tag: "style"}}
		req := makeVideoReq("gen3", map[string]interface{}{
			"reference_images": refImages,
		})

		result, err := ToRunwayVideoGenerationRequest(req)
		require.NoError(t, err)
		require.Len(t, result.ReferenceImages, 1)
		assert.Equal(t, "https://example.com/ref.jpg", result.ReferenceImages[0].URI)
		assert.Equal(t, "style", result.ReferenceImages[0].Tag)
		assert.NotContains(t, result.ExtraParams, "reference_images")
	})

	t.Run("map_fallback_reference_images", func(t *testing.T) {
		req := makeVideoReq("gen3", map[string]interface{}{
			"reference_images": []interface{}{
				map[string]interface{}{"uri": "https://example.com/ref.jpg", "tag": "style"},
			},
		})

		result, err := ToRunwayVideoGenerationRequest(req)
		require.NoError(t, err)
		require.Len(t, result.ReferenceImages, 1, "ConvertViaJSON fallback should convert map-based reference images")
		assert.Equal(t, "https://example.com/ref.jpg", result.ReferenceImages[0].URI)
		assert.Equal(t, "style", result.ReferenceImages[0].Tag)
		assert.NotContains(t, result.ExtraParams, "reference_images")
	})
}

func TestToRunwayVideoGenerationRequest_ContentModeration(t *testing.T) {
	// ContentModeration handling only applies to veo models
	t.Run("pointer_content_moderation", func(t *testing.T) {
		cm := &ContentModeration{PublicFigureThreshold: schemas.Ptr("high")}
		req := makeVideoReq("veo-model", map[string]interface{}{
			"content_moderation": cm,
		})

		result, err := ToRunwayVideoGenerationRequest(req)
		require.NoError(t, err)
		require.NotNil(t, result.ContentModeration)
		require.NotNil(t, result.ContentModeration.PublicFigureThreshold)
		assert.Equal(t, "high", *result.ContentModeration.PublicFigureThreshold)
		assert.NotContains(t, result.ExtraParams, "content_moderation")
	})

	t.Run("map_fallback_content_moderation", func(t *testing.T) {
		req := makeVideoReq("veo-model", map[string]interface{}{
			"content_moderation": map[string]interface{}{
				"public_figure_threshold": "high",
			},
		})

		result, err := ToRunwayVideoGenerationRequest(req)
		require.NoError(t, err)
		require.NotNil(t, result.ContentModeration, "ConvertViaJSON fallback should convert map-based content moderation")
		require.NotNil(t, result.ContentModeration.PublicFigureThreshold)
		assert.Equal(t, "high", *result.ContentModeration.PublicFigureThreshold)
		assert.NotContains(t, result.ExtraParams, "content_moderation")
	})
}
