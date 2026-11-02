package machgen

import (
	"encoding/base64"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToMachGenImageGenerationRequest converts a Bifrost image generation request to a MachGen T2I
// request, or I2I when input images are given.
func ToMachGenImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*MachGenGenerateRequest, error) {
	if bifrostReq.Input == nil {
		return nil, providerUtils.InvalidRequestErrorf("input is required")
	}

	request := &MachGenGenerateRequest{
		Model:       bifrostReq.Model,
		Prompt:      bifrostReq.Input.Prompt,
		ImageConfig: &MachGenImageConfig{},
	}

	if params := bifrostReq.Params; params != nil {
		if params.N != nil && *params.N > 1 {
			return nil, providerUtils.InvalidRequestErrorf("MachGen generates one image per request; n must be 1")
		}
		if err := applyImageGeometry(request.ImageConfig, params.Size, params.AspectRatio); err != nil {
			return nil, err
		}
		request.ImageConfig.InferSteps = params.NumInferenceSteps
		request.Seed = params.Seed
		request.SrcImageURLs = append(request.SrcImageURLs, params.InputImages...)
		if err := applyMachGenExtraParams(request, params.ExtraParams); err != nil {
			return nil, err
		}
	}

	if request.TaskType == "" {
		request.TaskType = taskTypeT2I
		if len(request.SrcImageURLs) > 0 {
			request.TaskType = taskTypeI2I
		}
	}
	return request, nil
}

// ToMachGenImageEditRequest converts a Bifrost image edit request to a MachGen I2I request.
// MachGen's async API only accepts sources by URL, so uploaded image bytes are rejected.
func ToMachGenImageEditRequest(bifrostReq *schemas.BifrostImageEditRequest) (*MachGenGenerateRequest, error) {
	if bifrostReq.Input == nil {
		return nil, providerUtils.InvalidRequestErrorf("input is required")
	}

	request := &MachGenGenerateRequest{
		Model:       bifrostReq.Model,
		TaskType:    taskTypeI2I,
		Prompt:      bifrostReq.Input.Prompt,
		ImageConfig: &MachGenImageConfig{},
	}

	for _, img := range bifrostReq.Input.Images {
		if img.URL == "" {
			return nil, providerUtils.InvalidRequestErrorf("MachGen image edit needs each image as a public URL; uploaded image bytes are not supported")
		}
		request.SrcImageURLs = append(request.SrcImageURLs, img.URL)
	}

	if params := bifrostReq.Params; params != nil {
		if params.N != nil && *params.N > 1 {
			return nil, providerUtils.InvalidRequestErrorf("MachGen generates one image per request; n must be 1")
		}
		if err := applyImageGeometry(request.ImageConfig, params.Size, params.AspectRatio); err != nil {
			return nil, err
		}
		request.ImageConfig.InferSteps = params.NumInferenceSteps
		request.Seed = params.Seed
		if err := applyMachGenExtraParams(request, params.ExtraParams); err != nil {
			return nil, err
		}
	}
	return request, nil
}

// applyImageGeometry sets height and aspect ratio from size; an explicit aspect_ratio wins.
func applyImageGeometry(config *MachGenImageConfig, size *string, aspectRatio *string) error {
	if size != nil && *size != "" && *size != "auto" {
		height, aspect, err := parseSize(*size)
		if err != nil {
			return err
		}
		config.Height = height
		config.AspectRatio = aspect
	}
	if aspectRatio != nil && *aspectRatio != "" {
		config.AspectRatio = aspectRatio
	}
	return nil
}

// ToBifrostImageGenerationResponse builds the Bifrost image response for a completed MachGen task.
// MachGen asset URLs need the caller's API key, so the image is returned inline as base64.
func ToBifrostImageGenerationResponse(task *MachGenTaskStatusResponse, image []byte) *schemas.BifrostImageGenerationResponse {
	response := &schemas.BifrostImageGenerationResponse{
		ID: task.TaskID,
		Data: []schemas.ImageData{{
			B64JSON: base64.StdEncoding.EncodeToString(image),
			Index:   0,
		}},
	}
	if task.Metadata != nil {
		response.Model = task.Metadata.Model
	}
	return response
}
