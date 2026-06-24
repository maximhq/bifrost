package runway

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// getRunwayEndpoint determines which Runway API endpoint to use based on the request parameters.
// Returns the appropriate endpoint path:
// - /v1/text_to_video: when only text prompt is provided
// - /v1/video_to_video: when video URI is provided
// - /v1/image_to_video: when image input reference is provided
func getRunwayEndpoint(req *schemas.BifrostVideoGenerationRequest) string {
	if req.Params != nil && req.Params.VideoURI != nil && *req.Params.VideoURI != "" {
		return "/v1/video_to_video"
	}
	if req.Input != nil && req.Input.InputReference != nil && *req.Input.InputReference != "" {
		return "/v1/image_to_video"
	}
	return "/v1/text_to_video"
}

// runwayImageModelCaps describes which optional fields a Runway image model accepts.
type runwayImageModelCaps struct {
	seed              bool
	contentModeration bool
	quality           bool
	background        bool
	outputCount       bool
	referenceSubject  bool
}

// runwayImageModelCapabilities returns the supported optional fields for a Runway image model.
// Unknown/custom models are treated permissively so new models work without code changes;
// Runway validates the final combination.
func runwayImageModelCapabilities(model string) runwayImageModelCaps {
	switch model {
	case "gen4_image", "gen4_image_turbo":
		return runwayImageModelCaps{seed: true, contentModeration: true}
	case "gpt_image_2":
		return runwayImageModelCaps{quality: true, background: true, outputCount: true}
	case "gemini_image3_pro", "gemini_image3.1_flash":
		return runwayImageModelCaps{outputCount: true, referenceSubject: true}
	case "gemini_2.5_flash":
		return runwayImageModelCaps{}
	default:
		return runwayImageModelCaps{seed: true, contentModeration: true, quality: true, background: true, outputCount: true, referenceSubject: true}
	}
}

// defaultRunwayImageRatio returns a sensible default ratio when the caller omits one.
// Valid ratios differ per model, so the default is model-aware.
func defaultRunwayImageRatio(model string) string {
	if model == "gpt_image_2" {
		return "auto"
	}
	// Valid for the gen4 family and every gemini image model.
	return "1024:1024"
}

func isRunwayGenModel(model string) bool {
	return strings.Contains(model, "gen")
}

func isRunwayVeoModel(model string) bool {
	return strings.Contains(model, "veo")
}

func supportsVideoToVideo(model string) bool {
	return model == "gen4_aleph"
}
