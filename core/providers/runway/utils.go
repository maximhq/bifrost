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

func isRunwayGenModel(model string) bool {
	return strings.Contains(model, "gen")
}

func isRunwayVeoModel(model string) bool {
	return strings.Contains(model, "veo")
}

func supportsVideoToVideo(model string) bool {
	return model == "gen4_aleph"
}
