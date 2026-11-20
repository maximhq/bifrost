package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToOpenAIImageGenerationRequest_XAIQuality(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		quality string
		want    *string
	}{
		{"low forwarded for grok-imagine-image-2.0", "grok-imagine-image-2.0", "low", schemas.Ptr("low")},
		{"medium forwarded for grok-imagine-image-2.0", "grok-imagine-image-2.0", "medium", schemas.Ptr("medium")},
		{"auto forwarded for grok-imagine-image-2.0", "grok-imagine-image-2.0", "auto", schemas.Ptr("auto")},
		{"high dropped, xAI does not accept it", "grok-imagine-image-2.0", "high", nil},
		{"dropped for models without quality support", "grok-imagine-image", "low", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ToOpenAIImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
				Provider: schemas.XAI,
				Model:    tc.model,
				Input:    &schemas.ImageGenerationInput{Prompt: "a lighthouse"},
				Params: &schemas.ImageGenerationParameters{
					Quality: schemas.Ptr(tc.quality),
					Size:    schemas.Ptr("1024x1024"),
				},
			})
			got := req.ImageGenerationParameters.Quality
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("quality = %v, want %v", got, tc.want)
			}
			if req.ImageGenerationParameters.Size != nil {
				t.Errorf("size = %q, want it dropped for xAI", *req.ImageGenerationParameters.Size)
			}
		})
	}
}
