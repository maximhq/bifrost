package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToGeminiVideoGenerationRequest_PutsReferenceImagesAndLastFrameOnInstance(t *testing.T) {
	seconds := "8"
	referenceImage := gemini.VideoReferenceImage{
		Image: gemini.VideoImageData{
			BytesBase64Encoded: schemas.Ptr("cmVmZXJlbmNl"),
			MimeType:           schemas.Ptr("image/png"),
		},
		ReferenceType: "asset",
	}
	lastFrame := &gemini.VideoImageData{
		BytesBase64Encoded: schemas.Ptr("bGFzdA=="),
		MimeType:           schemas.Ptr("image/jpeg"),
	}

	got, err := gemini.ToGeminiVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "veo-3.1-generate-001",
		Input: &schemas.VideoGenerationInput{
			Prompt: "make the reference subject run through a snowy garden",
		},
		Params: &schemas.VideoGenerationParameters{
			Seconds: &seconds,
			ExtraParams: map[string]any{
				"aspectRatio":     "16:9",
				"referenceImages": []gemini.VideoReferenceImage{referenceImage},
				"lastFrame":       lastFrame,
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, got.Instances, 1)
	assert.Equal(t, []gemini.VideoReferenceImage{referenceImage}, got.Instances[0].ReferenceImages)
	assert.Equal(t, lastFrame, got.Instances[0].LastFrame)
	require.NotNil(t, got.Parameters)
	assert.Equal(t, schemas.Ptr(8), got.Parameters.DurationSeconds)
	assert.Equal(t, schemas.Ptr("16:9"), got.Parameters.AspectRatio)
	assert.Empty(t, got.Parameters.ReferenceImages)
	assert.Nil(t, got.Parameters.LastFrame)
}

func TestToGeminiVideoGenerationRequest_DecodesGenericReferenceImagesAndLastFrame(t *testing.T) {
	got, err := gemini.ToGeminiVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "veo-3.1-generate-001",
		Input: &schemas.VideoGenerationInput{
			Prompt: "use the provided reference image",
		},
		Params: &schemas.VideoGenerationParameters{
			ExtraParams: map[string]any{
				"referenceImages": []map[string]any{
					{
						"image": map[string]any{
							"bytesBase64Encoded": "cmVmZXJlbmNl",
							"mimeType":           "image/png",
						},
						"referenceType": "style",
					},
				},
				"lastFrame": map[string]any{
					"bytesBase64Encoded": "bGFzdA==",
					"mimeType":           "image/jpeg",
				},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, got.Instances, 1)
	require.Len(t, got.Instances[0].ReferenceImages, 1)
	assert.Equal(t, "style", got.Instances[0].ReferenceImages[0].ReferenceType)
	assert.Equal(t, schemas.Ptr("cmVmZXJlbmNl"), got.Instances[0].ReferenceImages[0].Image.BytesBase64Encoded)
	assert.Equal(t, schemas.Ptr("image/png"), got.Instances[0].ReferenceImages[0].Image.MimeType)
	require.NotNil(t, got.Instances[0].LastFrame)
	assert.Equal(t, schemas.Ptr("bGFzdA=="), got.Instances[0].LastFrame.BytesBase64Encoded)
	assert.Equal(t, schemas.Ptr("image/jpeg"), got.Instances[0].LastFrame.MimeType)
	require.NotNil(t, got.Parameters)
	assert.Empty(t, got.Parameters.ReferenceImages)
	assert.Nil(t, got.Parameters.LastFrame)
}

func TestToGeminiVideoGenerationRequest_IgnoresInvalidGenericReferenceImages(t *testing.T) {
	got, err := gemini.ToGeminiVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
		Model: "veo-3.1-generate-001",
		Input: &schemas.VideoGenerationInput{
			Prompt: "use the provided reference image",
		},
		Params: &schemas.VideoGenerationParameters{
			ExtraParams: map[string]any{
				"referenceImages": []map[string]any{
					{"unexpected": "value"},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, got.Instances, 1)
	assert.Nil(t, got.Instances[0].ReferenceImages)
	require.NotNil(t, got.Parameters)
	assert.Empty(t, got.Parameters.ReferenceImages)
}

func TestGeminiVideoGenerationRequest_RoundTripsInstanceReferenceFields(t *testing.T) {
	durationSeconds := 8
	in := &gemini.GeminiVideoGenerationRequest{
		Model: "gemini/veo-3.1-generate-001",
		Instances: []gemini.GeminiVideoGenerationInstance{
			{
				Prompt: "use the provided reference image",
				ReferenceImages: []gemini.VideoReferenceImage{
					{
						Image: gemini.VideoImageData{
							BytesBase64Encoded: schemas.Ptr("cmVmZXJlbmNl"),
							MimeType:           schemas.Ptr("image/png"),
						},
						ReferenceType: "asset",
					},
				},
				LastFrame: &gemini.VideoImageData{
					BytesBase64Encoded: schemas.Ptr("bGFzdA=="),
					MimeType:           schemas.Ptr("image/jpeg"),
				},
			},
		},
		Parameters: &gemini.VideoGenerationParameters{
			DurationSeconds: &durationSeconds,
		},
	}

	bifrostReq, err := in.ToBifrostVideoGenerationRequest(nil)
	require.NoError(t, err)

	got, err := gemini.ToGeminiVideoGenerationRequest(bifrostReq)
	require.NoError(t, err)

	require.Len(t, got.Instances, 1)
	assert.Equal(t, in.Instances[0].ReferenceImages, got.Instances[0].ReferenceImages)
	assert.Equal(t, in.Instances[0].LastFrame, got.Instances[0].LastFrame)
	require.NotNil(t, got.Parameters)
	assert.Equal(t, &durationSeconds, got.Parameters.DurationSeconds)
	assert.Empty(t, got.Parameters.ReferenceImages)
	assert.Nil(t, got.Parameters.LastFrame)
}
