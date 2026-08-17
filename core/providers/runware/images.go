package runware

import (
	"fmt"
	"strings"

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

	// Text-to-SVG runs as its own task type on this endpoint; everything else is imageInference.
	taskType := taskTypeImageInference
	if bifrostReq.Params != nil && bifrostReq.Params.Type != nil &&
		strings.EqualFold(strings.TrimSpace(*bifrostReq.Params.Type), "vectorize") {
		taskType = taskTypeVectorize
	}

	width, height := defaultRunwareWidth, defaultRunwareHeight
	request := &RunwareInferenceRequest{
		TaskType:       taskType,
		TaskUUID:       uuid.New().String(),
		Model:          bifrostReq.Model,
		PositivePrompt: &bifrostReq.Input.Prompt,
		Width:          &width,
		Height:         &height,
		IncludeCost:    new(true),
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
	if len(bifrostReq.Input.Images) == 0 || runwareImageInput(bifrostReq.Input.Images[0]) == "" {
		return nil, fmt.Errorf("at least one input image is required")
	}

	if taskType := runwareImageEditTaskType(bifrostReq.Params); taskType != "" {
		return toRunwareImageToolRequest(taskType, bifrostReq)
	}

	width, height := defaultRunwareWidth, defaultRunwareHeight
	request := &RunwareInferenceRequest{
		TaskType:       taskTypeImageInference,
		TaskUUID:       uuid.New().String(),
		Model:          bifrostReq.Model,
		PositivePrompt: &bifrostReq.Input.Prompt,
		Width:          &width,
		Height:         &height,
		IncludeCost:    new(true),
	}

	// Seed image: the base image being edited (raw bytes -> base64 data URI).
	seedImage := runwareImageInput(bifrostReq.Input.Images[0])
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

// runwareImageInput resolves an input image to the reference Runware expects. A caller-supplied
// URL passes through untouched — Runware accepts UUIDs and URLs natively, so forwarding it avoids
// round-tripping the asset through the gateway as base64 — while raw bytes become a data URI.
func runwareImageInput(img schemas.ImageInput) string {
	if img.URL != "" {
		return img.URL
	}
	if len(img.Image) == 0 {
		return ""
	}
	return providerUtils.FileBytesToBase64DataURL(img.Image)
}

// runwareImageEditTaskType maps the neutral edit type onto a Runware tool task type. An empty
// result means the edit runs as a regular imageInference task (image-to-image, inpainting,
// outpainting).
func runwareImageEditTaskType(params *schemas.ImageEditParameters) string {
	if params == nil || params.Type == nil {
		return ""
	}
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(*params.Type)), "-", "_") {
	case "upscale":
		return taskTypeUpscale
	case "background_removal", "remove_background", "remove_bg":
		return taskTypeRemoveBackground
	case "mask", "segmentation":
		return taskTypeImageMasking
	case "vectorize":
		return taskTypeVectorize
	}
	return ""
}

// runwareResultAssets resolves a task result's output family. Runware names an artifact after what
// the task produces, so masking returns maskImage* and ControlNet preprocessing returns
// guideImage* rather than reusing image*; reading only image* would silently drop the output.
func runwareResultAssets(result *RunwareResult) (id string, url string, base64Data string, dataURI string) {
	switch {
	case result.ImageURL != "", result.ImageBase64Data != "", result.ImageDataURI != "":
		return result.ImageUUID, result.ImageURL, result.ImageBase64Data, result.ImageDataURI
	case result.MaskImageURL != "", result.MaskImageBase64Data != "", result.MaskImageDataURI != "":
		return result.MaskImageUUID, result.MaskImageURL, result.MaskImageBase64Data, result.MaskImageDataURI
	case result.GuideImageURL != "", result.GuideImageBase64Data != "", result.GuideImageDataURI != "":
		return result.GuideImageUUID, result.GuideImageURL, result.GuideImageBase64Data, result.GuideImageDataURI
	}
	return result.ImageUUID, "", "", ""
}

// toRunwareImageToolRequest builds a Runware single-image tool task (upscale, removeBackground).
// These share one envelope: the image is nested under "inputs", model tuning goes in "settings"
// and "providerSettings", and none of the imageInference fields (prompt, dimensions, steps) apply,
// so they are left unset. Runware-native fields are read from extra params under their own names.
func toRunwareImageToolRequest(taskType string, bifrostReq *schemas.BifrostImageEditRequest) (*RunwareInferenceRequest, error) {
	image := runwareImageInput(bifrostReq.Input.Images[0])
	request := &RunwareInferenceRequest{
		TaskType:    taskType,
		TaskUUID:    uuid.New().String(),
		Model:       bifrostReq.Model,
		Inputs:      &RunwareInputs{Image: &image},
		IncludeCost: new(true),
	}

	if bifrostReq.Params == nil {
		return request, nil
	}
	params := bifrostReq.Params

	request.OutputType = runwareOutputType(params.ResponseFormat)
	request.OutputFormat = runwareOutputFormat(params.OutputFormat)
	request.OutputQuality = params.OutputCompression
	request.ExtraParams = params.ExtraParams

	// Consume the fields promoted to typed properties so they are not also re-sent verbatim
	// when extra-param passthrough is enabled.
	if v, ok := runwareSettings(request.ExtraParams["settings"]); ok {
		delete(request.ExtraParams, "settings")
		request.Settings = v
	}
	if v, ok := runwareSettings(request.ExtraParams["providerSettings"]); ok {
		delete(request.ExtraParams, "providerSettings")
		request.ProviderSettings = v
	}

	request.UpscaleFactor = params.UpscaleFactor
	request.TargetMegapixels = params.TargetMegapixels

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
	var totalCost float64
	for i, img := range resp.Data {
		data := schemas.ImageData{Index: i}
		id, url, base64Data, dataURI := runwareResultAssets(&img)
		// Runware accepts these UUIDs as inputs, so surfacing them lets callers chain tasks
		// (mask then inpaint, upscale then remove background) without re-uploading the asset.
		data.ID = id
		switch {
		case url != "":
			data.URL = url
		case base64Data != "":
			data.B64JSON = base64Data
		case dataURI != "":
			data.URL = dataURI
		}
		// Masking models report the regions they located alongside the mask itself.
		for _, d := range img.Detections {
			data.Detections = append(data.Detections, schemas.ImageDetection{
				XMin: d.XMin, YMin: d.YMin, XMax: d.XMax, YMax: d.YMax,
			})
		}
		bifrostResp.Data = append(bifrostResp.Data, data)
		if img.Seed != nil {
			seeds = append(seeds, *img.Seed)
		}
		totalCost += img.Cost
	}

	if len(seeds) > 0 {
		bifrostResp.ImageGenerationResponseParameters = &schemas.ImageGenerationResponseParameters{Seeds: seeds}
	}

	// Runware reports the exact task cost (only when the request sets includeCost). Surface it as
	// the provider-reported cost so pricing uses it verbatim instead of the datasheet estimate.
	if totalCost > 0 {
		bifrostResp.Usage = &schemas.ImageUsage{Cost: &schemas.BifrostCost{TotalCost: totalCost}}
	}

	return bifrostResp, nil
}
