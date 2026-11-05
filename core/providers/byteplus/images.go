package byteplus

import (
	"maps"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// seedreamMaxImages is the most images one Seedream sequential generation returns, counting reference images.
const seedreamMaxImages = 15

// ToBytePlusImageGenerationRequest converts a Bifrost image generation request to the Seedream /images/generations body.
func ToBytePlusImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) (*BytePlusImageGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || strings.TrimSpace(bifrostReq.Input.Prompt) == "" {
		return nil, providerUtils.InvalidRequestErrorf("prompt is required")
	}
	req := &BytePlusImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}
	params := bifrostReq.Params
	if params == nil {
		return req, nil
	}
	req.Seed = params.Seed
	req.ResponseFormat = toBytePlusImageResponseFormat(params.ResponseFormat)
	req.OutputFormat = params.OutputFormat
	req.Background = params.Background
	if params.Size != nil && *params.Size != "" && *params.Size != "auto" {
		req.Size = params.Size
	}
	for _, image := range params.InputImages {
		url, err := schemas.SanitizeImageURL(image)
		if err != nil {
			return nil, providerUtils.InvalidRequestErrorf("invalid input image: %v", err)
		}
		req.Image = append(req.Image, url)
	}
	applyBytePlusImageCount(req, params.N)
	if len(params.ExtraParams) > 0 {
		req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
		maps.Copy(req.ExtraParams, params.ExtraParams)
		liftBytePlusImageExtraParams(req)
	}
	return req, nil
}

// ToBytePlusImageEditRequest converts a Bifrost image edit request to the same Seedream endpoint, which edits when reference images are present.
func ToBytePlusImageEditRequest(bifrostReq *schemas.BifrostImageEditRequest) (*BytePlusImageGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || strings.TrimSpace(bifrostReq.Input.Prompt) == "" {
		return nil, providerUtils.InvalidRequestErrorf("prompt is required")
	}
	if len(bifrostReq.Input.Images) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("at least one image is required")
	}
	req := &BytePlusImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
		Image:  make([]string, 0, len(bifrostReq.Input.Images)),
	}
	for _, image := range bifrostReq.Input.Images {
		switch {
		case len(image.Image) > 0:
			req.Image = append(req.Image, providerUtils.FileBytesToBase64DataURL(image.Image))
		case image.URL != "":
			url, err := schemas.SanitizeImageURL(image.URL)
			if err != nil {
				return nil, providerUtils.InvalidRequestErrorf("invalid input image: %v", err)
			}
			req.Image = append(req.Image, url)
		}
	}
	if len(req.Image) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("at least one image is required")
	}
	params := bifrostReq.Params
	if params == nil {
		return req, nil
	}
	if len(params.Mask) > 0 {
		return nil, providerUtils.InvalidRequestErrorf("mask is not supported by byteplus image edit")
	}
	req.Seed = params.Seed
	req.ResponseFormat = toBytePlusImageResponseFormat(params.ResponseFormat)
	req.OutputFormat = params.OutputFormat
	req.Background = params.Background
	if params.Size != nil && *params.Size != "" && *params.Size != "auto" {
		req.Size = params.Size
	}
	applyBytePlusImageCount(req, params.N)
	if len(params.ExtraParams) > 0 {
		req.ExtraParams = make(map[string]interface{}, len(params.ExtraParams))
		maps.Copy(req.ExtraParams, params.ExtraParams)
		liftBytePlusImageExtraParams(req)
	}
	return req, nil
}

func toBytePlusImageResponseFormat(format *string) *string {
	if format == nil {
		return nil
	}
	switch *format {
	case "b64_json", "base64":
		return schemas.Ptr("b64_json")
	case "url":
		return schemas.Ptr("url")
	}
	return nil
}

// applyBytePlusImageCount maps n onto Seedream's sequential generation, which is how it returns more than one image.
func applyBytePlusImageCount(req *BytePlusImageGenerationRequest, n *int) {
	if n == nil || *n <= 1 {
		return
	}
	req.SequentialImageGeneration = schemas.Ptr("auto")
	req.SequentialImageGenerationOptions = &BytePlusSequentialImageOptions{MaxImages: min(*n, seedreamMaxImages)}
}

// liftBytePlusImageExtraParams moves the Seedream fields Bifrost has typed slots for out of the passthrough map.
func liftBytePlusImageExtraParams(req *BytePlusImageGenerationRequest) {
	if v, ok := req.ExtraParams["watermark"].(bool); ok {
		req.Watermark = &v
		delete(req.ExtraParams, "watermark")
	}
	if v, ok := req.ExtraParams["sequential_image_generation"].(string); ok && v != "" {
		req.SequentialImageGeneration = &v
		delete(req.ExtraParams, "sequential_image_generation")
	}
	if raw, ok := req.ExtraParams["sequential_image_generation_options"]; ok {
		if opts, err := schemas.ConvertViaJSON[BytePlusSequentialImageOptions](raw); err == nil && opts.MaxImages > 0 {
			req.SequentialImageGenerationOptions = &opts
			delete(req.ExtraParams, "sequential_image_generation_options")
		} else if req.SequentialImageGenerationOptions != nil {
			// An unusable value must not overwrite the options already derived from n at merge time.
			delete(req.ExtraParams, "sequential_image_generation_options")
		}
	}
}

// ToBifrostImageGenerationResponse converts a Seedream response to Bifrost format.
func ToBifrostImageGenerationResponse(resp *BytePlusImageGenerationResponse, model string) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if resp == nil {
		return nil, providerUtils.NewBifrostOperationError("image generation response is nil", nil)
	}
	if resp.Error != nil && resp.Error.Message != "" {
		bifrostErr := providerUtils.NewProviderAPIError(resp.Error.Message, nil, 400, schemas.Ptr("invalid_request_error"), nil)
		bifrostErr.Error.Code = schemas.Ptr(resp.Error.Code)
		return nil, bifrostErr
	}
	data := make([]schemas.ImageData, 0, len(resp.Data))
	var firstErr *BytePlusError
	for _, image := range resp.Data {
		if image.URL == "" && image.B64JSON == "" {
			if image.Error != nil && firstErr == nil {
				firstErr = image.Error
			}
			continue
		}
		data = append(data, schemas.ImageData{URL: image.URL, B64JSON: image.B64JSON, Index: len(data)})
	}
	if len(data) == 0 {
		message := "byteplus returned no images"
		if firstErr != nil && firstErr.Message != "" {
			message = firstErr.Message
		}
		bifrostErr := providerUtils.NewProviderAPIError(message, nil, 400, schemas.Ptr("content_filter"), nil)
		if firstErr != nil && firstErr.Code != "" {
			bifrostErr.Error.Code = schemas.Ptr(firstErr.Code)
		}
		return nil, bifrostErr
	}
	created := resp.Created
	if created == 0 {
		created = resp.CreatedAt
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if resp.Model != "" {
		model = resp.Model
	}
	bifrostResp := &schemas.BifrostImageGenerationResponse{
		Created: created,
		Model:   model,
		Data:    data,
	}
	// The produced size, not the requested tier ("2K"), decides Seedream's pixel-tiered price.
	for _, image := range resp.Data {
		if image.Size != "" && (image.URL != "" || image.B64JSON != "") {
			bifrostResp.ImageGenerationResponseParameters = &schemas.ImageGenerationResponseParameters{Size: image.Size}
			break
		}
	}
	bifrostResp.Usage = ToBifrostImageUsage(resp.Usage)
	return bifrostResp, nil
}

// ToBifrostImageUsage converts Seedream usage to Bifrost image usage.
func ToBifrostImageUsage(usage *BytePlusImageUsage) *schemas.ImageUsage {
	if usage == nil {
		return nil
	}
	return &schemas.ImageUsage{
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
		OutputTokensDetails: &schemas.ImageTokenDetails{
			NImages:     usage.GeneratedImages,
			ImageTokens: usage.OutputTokens,
		},
	}
}

// bytePlusImageStreamError builds the error for a failed stream event. A per-image failure is a moderation
// refusal unless Ark reports an internal error, the only per-image failure that aborts the stream; an error
// event without an image index rejects the whole request.
func bytePlusImageStreamError(failure *BytePlusError, perImage bool) *schemas.BifrostError {
	message := "byteplus returned no images"
	var code string
	if failure != nil {
		code = failure.Code
		if failure.Message != "" {
			message = failure.Message
		}
	}
	status, errType := 400, "invalid_request_error"
	if perImage {
		errType = "content_filter"
	}
	if code == bytePlusInternalServiceError {
		status, errType = 500, "server_error"
	}
	bifrostErr := providerUtils.NewProviderAPIError(message, nil, status, schemas.Ptr(errType), nil)
	if code != "" {
		bifrostErr.Error.Code = schemas.Ptr(code)
	}
	return bifrostErr
}

// bytePlusInternalServiceError is the per-image error code after which Ark stops generating.
const bytePlusInternalServiceError = "InternalServiceError"
