package minimax

import (
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// miniMaxImageAspectRatios are the aspect ratios image-01 accepts, with their width/height ratio.
var miniMaxImageAspectRatios = []struct {
	name  string
	ratio float64
}{
	{"1:1", 1},
	{"16:9", 16.0 / 9},
	{"4:3", 4.0 / 3},
	{"3:2", 3.0 / 2},
	{"2:3", 2.0 / 3},
	{"3:4", 3.0 / 4},
	{"9:16", 9.0 / 16},
	{"21:9", 21.0 / 9},
}

// ToMiniMaxImageGenerationRequest converts a Bifrost image generation request to MiniMax's /v1/image_generation body.
func ToMiniMaxImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*MiniMaxImageGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || strings.TrimSpace(bifrostReq.Input.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	req := &MiniMaxImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}
	params := bifrostReq.Params
	if params == nil {
		return req, nil
	}
	req.N = params.N
	req.Seed = params.Seed
	if params.ResponseFormat != nil {
		switch *params.ResponseFormat {
		case "b64_json", "base64":
			req.ResponseFormat = schemas.Ptr("base64")
		case "url":
			req.ResponseFormat = schemas.Ptr("url")
		}
	}
	if params.AspectRatio != nil && *params.AspectRatio != "" {
		req.AspectRatio = params.AspectRatio
	} else if params.Size != nil && *params.Size != "" && *params.Size != "auto" {
		width, height, err := parseImageSize(*params.Size)
		if err != nil {
			return nil, err
		}
		if isValidMiniMaxDimension(width) && isValidMiniMaxDimension(height) {
			req.Width = &width
			req.Height = &height
		} else {
			req.AspectRatio = schemas.Ptr(nearestMiniMaxAspectRatio(width, height))
		}
	}
	if len(params.ExtraParams) > 0 {
		req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
		maps.Copy(req.ExtraParams, params.ExtraParams)
		if v, ok := req.ExtraParams["prompt_optimizer"].(bool); ok {
			req.PromptOptimizer = &v
			delete(req.ExtraParams, "prompt_optimizer")
		}
	}
	return req, nil
}

// ToBifrostImageGenerationResponse converts a MiniMax image generation response to Bifrost format.
func ToBifrostImageGenerationResponse(resp *MiniMaxImageGenerationResponse, model string) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if resp == nil {
		return nil, providerUtils.NewBifrostOperationError("image generation response is nil", nil)
	}
	if bifrostErr := miniMaxBaseRespError(resp.BaseResp); bifrostErr != nil {
		return nil, bifrostErr
	}
	data := make([]schemas.ImageData, 0, len(resp.Data.ImageURLs)+len(resp.Data.ImageBase64))
	for _, url := range resp.Data.ImageURLs {
		data = append(data, schemas.ImageData{URL: url, Index: len(data)})
	}
	for _, b64 := range resp.Data.ImageBase64 {
		data = append(data, schemas.ImageData{B64JSON: b64, Index: len(data)})
	}
	if len(data) == 0 {
		// Every image was blocked by content safety (metadata.failed_count > 0) or nothing came back.
		return nil, providerUtils.NewProviderAPIError("minimax returned no images (possibly blocked by content safety)", nil, 400, schemas.Ptr("content_filter"), nil)
	}
	return &schemas.BifrostImageGenerationResponse{
		ID:      resp.ID,
		Created: time.Now().Unix(),
		Model:   model,
		Data:    data,
	}, nil
}

func parseImageSize(size string) (int, int, error) {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid size %q, expected WIDTHxHEIGHT", size)
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid size %q, expected WIDTHxHEIGHT", size)
	}
	return width, height, nil
}

// isValidMiniMaxDimension reports whether image-01 accepts the dimension as an explicit width or height.
func isValidMiniMaxDimension(v int) bool {
	return v >= 512 && v <= 2048 && v%8 == 0
}

func nearestMiniMaxAspectRatio(width, height int) string {
	target := float64(width) / float64(height)
	best := miniMaxImageAspectRatios[0].name
	bestDiff := math.Inf(1)
	for _, ar := range miniMaxImageAspectRatios {
		if diff := math.Abs(math.Log(target / ar.ratio)); diff < bestDiff {
			best, bestDiff = ar.name, diff
		}
	}
	return best
}
