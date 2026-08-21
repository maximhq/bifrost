package runware

import (
	"fmt"
	"net/url"
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

	caps := schemas.ResolveModelCaps(schemas.Runware, bifrostReq.Model)
	traits := runwareImageModelFor(caps)
	request := &RunwareInferenceRequest{
		TaskType:    taskType,
		TaskUUID:    uuid.New().String(),
		Model:       bifrostReq.Model,
		IncludeCost: new(true),
	}
	// Runware rejects any key a model does not declare, so the dimensions and the prompt are only
	// sent to models that accept them.
	if !traits.noPrompt {
		request.PositivePrompt = &bifrostReq.Input.Prompt
	}
	if !traits.noDimensions {
		width, height := defaultRunwareWidth, defaultRunwareHeight
		request.Width, request.Height = &width, &height
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.Size != nil && *params.Size != "" && request.Width != nil {
			*request.Width, *request.Height = parseRunwareSize(*params.Size)
		}
		request.NegativePrompt = params.NegativePrompt
		request.Steps = params.NumInferenceSteps
		request.Seed = params.Seed
		request.NumberResults = params.N
		request.OutputType = runwareOutputType(params.ResponseFormat)
		request.OutputFormat = runwareOutputFormat(params.OutputFormat)
		request.OutputQuality = params.OutputCompression

		request.ExtraParams = params.ExtraParams

		if v := request.ExtraParams["seedImage"]; v != nil {
			delete(request.ExtraParams, "seedImage")
			if s, ok := v.(string); ok && s != "" {
				request.SeedImage = &s
			}
		}

		// input_images drives image-to-image on the generation path. A seedImage supplied through
		// extra params is the provider-native form of the same thing and wins outright — sending
		// both would put two input keys on a request that accepts one.
		// Each entry is normalized the way the other image providers normalize theirs; empty ones are
		// dropped, since Runware rejects a blank input key.
		if request.SeedImage == nil {
			inputImages := make([]string, 0, len(params.InputImages))
			for _, img := range params.InputImages {
				reference, err := runwareImageReference(img)
				if err != nil {
					return nil, fmt.Errorf("invalid input image: %w", err)
				}
				if reference != "" {
					inputImages = append(inputImages, reference)
				}
			}
			if len(inputImages) > 0 {
				switch traits.inputForm {
				case runwareImageInputReferenceImages:
					request.Inputs = &RunwareInputs{ReferenceImages: inputImages}
				case runwareImageInputImageMask:
					request.Inputs = &RunwareInputs{Image: &inputImages[0]}
				default:
					request.SeedImage = &inputImages[0]
				}
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

	caps := schemas.ResolveModelCaps(schemas.Runware, bifrostReq.Model)
	traits := runwareImageModelFor(caps)
	request := &RunwareInferenceRequest{
		TaskType:    taskTypeImageInference,
		TaskUUID:    uuid.New().String(),
		Model:       bifrostReq.Model,
		IncludeCost: new(true),
	}
	// The erase and object-removal models take neither a prompt nor dimensions, and Runware rejects
	// any key a model does not declare, so both are omitted for them.
	if !traits.noPrompt {
		request.PositivePrompt = &bifrostReq.Input.Prompt
	}
	if !traits.noDimensions {
		width, height := defaultRunwareWidth, defaultRunwareHeight
		request.Width, request.Height = &width, &height
	}

	// The base image being edited, in the form this model declares. Only the referenceImages form
	// carries more than one image; the others take the first and drop the rest.
	seedImage := runwareImageInput(bifrostReq.Input.Images[0])
	switch traits.inputForm {
	case runwareImageInputReferenceImages:
		references := make([]string, 0, len(bifrostReq.Input.Images))
		for _, img := range bifrostReq.Input.Images {
			if reference := runwareImageInput(img); reference != "" {
				references = append(references, reference)
			}
		}
		request.Inputs = &RunwareInputs{ReferenceImages: references}
	case runwareImageInputImageMask:
		request.Inputs = &RunwareInputs{Image: &seedImage}
	default:
		request.SeedImage = &seedImage
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.Size != nil && *params.Size != "" && request.Width != nil {
			*request.Width, *request.Height = parseRunwareSize(*params.Size)
		}
		request.NegativePrompt = params.NegativePrompt
		request.Steps = params.NumInferenceSteps
		request.Seed = params.Seed
		request.NumberResults = params.N
		request.OutputType = runwareOutputType(params.ResponseFormat)
		request.OutputFormat = runwareOutputFormat(params.OutputFormat)
		request.OutputQuality = params.OutputCompression

		// Mask image enables inpainting (raw bytes -> base64 data URI). The referenceImages models
		// declare no mask key at all, so a mask sent to one of those is dropped.
		if len(params.Mask) > 0 {
			maskImage := providerUtils.FileBytesToBase64DataURL(params.Mask)
			switch traits.inputForm {
			case runwareImageInputImageMask:
				request.Inputs.Mask = &maskImage
			case runwareImageInputSeedImage:
				request.MaskImage = &maskImage
			}
		}

		request.ExtraParams = params.ExtraParams
	}

	return request, nil
}

// runwareImageInput resolves an input image to the reference Runware expects. A caller-supplied
// URL is normalized rather than round-tripped through the gateway as base64; raw bytes become a
// data URI. An unusable reference yields "", which callers treat as absent.
func runwareImageInput(img schemas.ImageInput) string {
	if img.URL != "" {
		reference, err := runwareImageReference(img.URL)
		if err != nil {
			return ""
		}
		return reference
	}
	if len(img.Image) == 0 {
		return ""
	}
	return providerUtils.FileBytesToBase64DataURL(img.Image)
}

// runwareImageReference normalizes a caller-supplied image reference. URLs and base64 payloads go
// through the same sanitizer the other image providers use, which validates data URLs and wraps
// bare base64 into one. A value carrying no URL scheme is left alone: Runware accepts its own asset
// UUIDs as inputs — the ids it returns on data[].id — and those would otherwise be rejected as
// schemeless URLs. Returns "" for an empty reference so callers can skip it.
func runwareImageReference(image string) (string, error) {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return "", nil
	}
	if sanitized, err := schemas.SanitizeImageURL(trimmed); err == nil {
		return sanitized, nil
	} else if parsed, parseErr := url.Parse(trimmed); parseErr != nil || parsed.Scheme != "" {
		// A scheme means it was meant to be a URL, so a sanitizer failure is a real error.
		return "", err
	}
	return trimmed, nil
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
	case "controlnet_preprocess", "controlnet", "preprocess":
		return taskTypeControlNetPreprocess
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
