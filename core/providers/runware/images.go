package runware

import (
	"fmt"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToRunwareImageGenerationRequest converts a Bifrost image generation request to a Runware
// imageInference task. A "seedImage" supplied via extra params (a Runware image UUID, a public
// URL, or a base64/data-URI string) turns the request into an image-to-image generation.
func ToRunwareImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*RunwareInferenceRequest, error) {
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}

	width, height := defaultRunwareWidth, defaultRunwareHeight
	request := &RunwareInferenceRequest{
		TaskType:       taskTypeImageInference,
		TaskUUID:       uuid.New().String(),
		Model:          bifrostReq.Model,
		PositivePrompt: &bifrostReq.Input.Prompt,
		Width:          &width,
		Height:         &height,
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.Size != nil && *params.Size != "" {
			*request.Width, *request.Height = parseRunwareSize(*params.Size)
		}
		request.NegativePrompt = params.NegativePrompt
		request.Steps = params.NumInferenceSteps
		request.Seed = params.Seed
		request.NumberResults = params.N
		request.OutputType = runwareOutputType(params.ResponseFormat)
		request.OutputFormat = runwareOutputFormat(params.OutputFormat)

		request.ExtraParams = params.ExtraParams

		if v := request.ExtraParams["seedImage"]; v != nil {
			delete(request.ExtraParams, "seedImage")
			if s, ok := v.(string); ok && s != "" {
				request.SeedImage = &s
			}
		}
	}

	return request, nil
}

// ToRunwareImageEditRequest converts a Bifrost image edit request to a Runware imageInference task.
// The first input image is the seed image; an optional mask enables inpainting. Outpainting,
// strength, maskMargin and other operation-specific fields flow through via extra params.
func ToRunwareImageEditRequest(bifrostReq *schemas.BifrostImageEditRequest) (*RunwareInferenceRequest, error) {
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("input is required")
	}
	if len(bifrostReq.Input.Images) == 0 || len(bifrostReq.Input.Images[0].Image) == 0 {
		return nil, fmt.Errorf("at least one input image is required")
	}

	width, height := defaultRunwareWidth, defaultRunwareHeight
	request := &RunwareInferenceRequest{
		TaskType:       taskTypeImageInference,
		TaskUUID:       uuid.New().String(),
		Model:          bifrostReq.Model,
		PositivePrompt: &bifrostReq.Input.Prompt,
		Width:          &width,
		Height:         &height,
	}

	// Seed image: the base image being edited (raw bytes -> base64 data URI).
	seedImage := providerUtils.FileBytesToBase64DataURL(bifrostReq.Input.Images[0].Image)
	request.SeedImage = &seedImage

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.Size != nil && *params.Size != "" {
			*request.Width, *request.Height = parseRunwareSize(*params.Size)
		}
		request.NegativePrompt = params.NegativePrompt
		request.Steps = params.NumInferenceSteps
		request.Seed = params.Seed
		request.NumberResults = params.N
		request.OutputType = runwareOutputType(params.ResponseFormat)
		request.OutputFormat = runwareOutputFormat(params.OutputFormat)

		// Mask image enables inpainting (raw bytes -> base64 data URI).
		if len(params.Mask) > 0 {
			maskImage := providerUtils.FileBytesToBase64DataURL(params.Mask)
			request.MaskImage = &maskImage
		}

		request.ExtraParams = params.ExtraParams
	}

	return request, nil
}

// ToBifrostImageGenerationResponse converts a Runware response envelope to a Bifrost image response.
func ToBifrostImageGenerationResponse(resp *RunwareResponse) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if resp == nil {
		return nil, providerUtils.NewBifrostOperationError("runware response is nil", nil)
	}

	// Surface task-level failures returned alongside (or instead of) data.
	if len(resp.Data) == 0 {
		if msg := firstRunwareErrorMessage(resp.Errors); msg != "" {
			return nil, providerUtils.NewBifrostOperationError(msg, nil)
		}
		return nil, providerUtils.NewBifrostOperationError("runware returned no images", nil)
	}

	bifrostResp := &schemas.BifrostImageGenerationResponse{
		ID:   resp.Data[0].TaskUUID,
		Data: []schemas.ImageData{},
	}

	var seeds []int
	for i, img := range resp.Data {
		data := schemas.ImageData{Index: i}
		switch {
		case img.ImageURL != "":
			data.URL = img.ImageURL
		case img.ImageBase64Data != "":
			data.B64JSON = img.ImageBase64Data
		case img.ImageDataURI != "":
			data.URL = img.ImageDataURI
		}
		bifrostResp.Data = append(bifrostResp.Data, data)
		if img.Seed != nil {
			seeds = append(seeds, *img.Seed)
		}
	}

	if len(seeds) > 0 {
		bifrostResp.ImageGenerationResponseParameters = &schemas.ImageGenerationResponseParameters{Seeds: seeds}
	}

	return bifrostResp, nil
}
