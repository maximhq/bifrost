package openai

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIImageGenerationRequest converts a Bifrost Image Request to OpenAI format
func ToOpenAIImageGenerationRequest(bifrostReq *schemas.BifrostImageGenerationRequest) *OpenAIImageGenerationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil
	}

	req := &OpenAIImageGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	if bifrostReq.Params != nil {
		req.ImageGenerationParameters = *bifrostReq.Params
	}

	switch bifrostReq.Provider {
	case schemas.XAI:
		filterXAISpecificParameters(req)
	case schemas.OpenAI, schemas.Azure:
		filterOpenAISpecificParameters(req)
	}
	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}
	return req
}

func filterXAISpecificParameters(req *OpenAIImageGenerationRequest) {
	req.ImageGenerationParameters.Quality = nil
	req.ImageGenerationParameters.Style = nil
	req.ImageGenerationParameters.Size = nil
	req.ImageGenerationParameters.OutputCompression = nil
}

func filterOpenAISpecificParameters(req *OpenAIImageGenerationRequest) {
	req.ImageGenerationParameters.Seed = nil
	req.NumInferenceSteps = nil
	req.NegativePrompt = nil
}

// ToBifrostImageGenerationRequest converts an OpenAI image generation request to Bifrost format
func (request *OpenAIImageGenerationRequest) ToBifrostImageGenerationRequest(ctx *schemas.BifrostContext) *schemas.BifrostImageGenerationRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, "")

	return &schemas.BifrostImageGenerationRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.ImageGenerationInput{
			Prompt: request.Prompt,
		},
		Params:    &request.ImageGenerationParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

func (request *OpenAIImageEditRequest) ToBifrostImageEditRequest(ctx *schemas.BifrostContext) *schemas.BifrostImageEditRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, "")

	return &schemas.BifrostImageEditRequest{
		Provider:  provider,
		Model:     model,
		Input:     request.Input,
		Params:    &request.ImageEditParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

func (request *OpenAIImageVariationRequest) ToBifrostImageVariationRequest(ctx *schemas.BifrostContext) *schemas.BifrostImageVariationRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, "")

	return &schemas.BifrostImageVariationRequest{
		Provider:  provider,
		Model:     model,
		Input:     request.Input,
		Params:    &request.ImageVariationParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

func ToOpenAIImageEditRequest(bifrostReq *schemas.BifrostImageEditRequest) *OpenAIImageEditRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil
	}

	req := &OpenAIImageEditRequest{
		Model: bifrostReq.Model,
		Input: bifrostReq.Input,
	}

	if bifrostReq.Params != nil {
		req.ImageEditParameters = *bifrostReq.Params
	}

	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return req
}

// encodeOpenAIImageEditRequest selects the wire format without fetching image URLs.
// OpenAI URL inputs use JSON references; uploaded bytes remain multipart.
// Model support is validated by the upstream API.
func encodeOpenAIImageEditRequest(
	ctx *schemas.BifrostContext,
	request *OpenAIImageEditRequest,
	providerName schemas.ModelProvider,
) ([]byte, string, *schemas.BifrostError) {
	if request == nil || request.Input == nil || len(request.Input.Images) == 0 {
		return nil, "", providerUtils.NewBifrostBadRequestError("at least one image is required for image edits")
	}

	hasURL, hasBytes := false, false
	for i, image := range request.Input.Images {
		switch {
		case image.URL != "" && len(image.Image) > 0:
			return nil, "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf("image %d contains both a URL and image bytes", i))
		case strings.TrimSpace(image.URL) != "":
			hasURL = true
		case len(image.Image) > 0:
			hasBytes = true
		default:
			return nil, "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf("image %d is empty; provide a URL or image bytes", i))
		}
	}
	if hasURL && hasBytes {
		return nil, "", providerUtils.NewBifrostBadRequestError("image edits cannot mix URL references and image bytes; upload all images or provide only URLs")
	}
	if hasURL {
		if providerName != schemas.OpenAI {
			return nil, "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf(
				"json image URL edits are not enabled for provider %q in this adapter; upload image bytes using multipart instead",
				providerName,
			))
		}
		if len(request.Mask) > 0 {
			return nil, "", providerUtils.NewBifrostBadRequestError("image URL edits with a byte mask are not supported; upload all images and the mask using multipart instead")
		}
		if request.ResponseFormat != nil && *request.ResponseFormat != "b64_json" {
			return nil, "", providerUtils.NewBifrostBadRequestError("this adapter only accepts an omitted response_format or b64_json for JSON image edits")
		}
		jsonRequest := toOpenAIImageEditJSONRequest(request)
		body, err := providerUtils.MarshalSorted(jsonRequest)
		if err == nil && ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			body, err = providerUtils.MergeExtraParamsIntoJSON(body, request.ExtraParams)
		}
		if err != nil {
			return nil, "", providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		}
		return body, "application/json", nil
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := parseImageEditFormDataBodyFromRequest(writer, request, providerName); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func toOpenAIImageEditJSONRequest(request *OpenAIImageEditRequest) *OpenAIImageEditJSONRequest {
	images := make([]OpenAIImageEditReference, len(request.Input.Images))
	for i, image := range request.Input.Images {
		images[i] = OpenAIImageEditReference{ImageURL: image.URL}
	}
	return &OpenAIImageEditJSONRequest{
		Model:             request.Model,
		Images:            images,
		Prompt:            request.Input.Prompt,
		Background:        request.Background,
		InputFidelity:     request.InputFidelity,
		N:                 request.N,
		OutputCompression: request.OutputCompression,
		OutputFormat:      request.OutputFormat,
		PartialImages:     request.PartialImages,
		Quality:           request.Quality,
		Size:              request.Size,
		User:              request.User,
		Stream:            request.Stream,
	}
}

func parseImageEditFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAIImageEditRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add model field (required)
	if err := writer.WriteField("model", openaiReq.Model); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write model field", err)
	}

	// Add prompt field (required)
	if err := writer.WriteField("prompt", openaiReq.Input.Prompt); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write prompt field", err)
	}

	// Add stream field when requesting streaming
	if openaiReq.Stream != nil && *openaiReq.Stream {
		if err := writer.WriteField("stream", "true"); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write stream field", err)
		}
	}

	// Add optional parameters before file parts so routing metadata arrives first upstream.
	if openaiReq.N != nil {
		if err := writer.WriteField("n", strconv.Itoa(*openaiReq.N)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write n field", err)
		}
	}

	if openaiReq.Size != nil {
		if err := writer.WriteField("size", *openaiReq.Size); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write size field", err)
		}
	}

	if openaiReq.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *openaiReq.ResponseFormat); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write response_format field", err)
		}
	}

	if openaiReq.Quality != nil {
		if err := writer.WriteField("quality", *openaiReq.Quality); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write quality field", err)
		}
	}

	if openaiReq.Background != nil {
		if err := writer.WriteField("background", *openaiReq.Background); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write background field", err)
		}
	}

	if openaiReq.InputFidelity != nil {
		if err := writer.WriteField("input_fidelity", *openaiReq.InputFidelity); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write input_fidelity field", err)
		}
	}

	if openaiReq.PartialImages != nil {
		if err := writer.WriteField("partial_images", strconv.Itoa(*openaiReq.PartialImages)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write partial_images field", err)
		}
	}

	if openaiReq.OutputFormat != nil {
		if err := writer.WriteField("output_format", *openaiReq.OutputFormat); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write output_format field", err)
		}
	}

	if openaiReq.OutputCompression != nil {
		if err := writer.WriteField("output_compression", strconv.Itoa(*openaiReq.OutputCompression)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write output_compression field", err)
		}
	}

	if openaiReq.User != nil {
		if err := writer.WriteField("user", *openaiReq.User); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write user field", err)
		}
	}

	// Add image[] fields (one for each image)
	for i, imageInput := range openaiReq.Input.Images {
		fieldName := "image[]"

		// Detect and validate MIME type
		mimeType := http.DetectContentType(imageInput.Image)
		// Fallback to PNG if content type is undetectable or generic
		if mimeType == "" || mimeType == "application/octet-stream" {
			mimeType = "image/png"
		}

		// Determine filename based on MIME type
		var filename string
		switch mimeType {
		case "image/jpeg":
			filename = fmt.Sprintf("image%d.jpg", i)
		case "image/webp":
			filename = fmt.Sprintf("image%d.webp", i)
		default:
			filename = fmt.Sprintf("image%d.png", i)
		}

		// Create form part with proper Content-Type header (not CreateFormFile which defaults to application/octet-stream)
		part, err := writer.CreatePart(map[string][]string{
			"Content-Disposition": {fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, filename)},
			"Content-Type":        {mimeType},
		})
		if err != nil {
			return providerUtils.NewBifrostOperationError(fmt.Sprintf("failed to create form part for image %d", i), err)
		}
		if _, err := part.Write(imageInput.Image); err != nil {
			return providerUtils.NewBifrostOperationError(fmt.Sprintf("failed to write image %d data", i), err)
		}
	}

	// Add mask if present
	if len(openaiReq.Mask) > 0 {
		// Detect MIME type for mask
		maskMimeType := http.DetectContentType(openaiReq.Mask)
		if maskMimeType != "image/png" && maskMimeType != "image/jpeg" && maskMimeType != "image/webp" {
			maskMimeType = "image/png"
		}

		var maskFilename string
		switch maskMimeType {
		case "image/jpeg":
			maskFilename = "mask.jpg"
		case "image/webp":
			maskFilename = "mask.webp"
		default:
			maskFilename = "mask.png"
		}

		// Create form part with proper Content-Type header
		maskPart, err := writer.CreatePart(map[string][]string{
			"Content-Disposition": {`form-data; name="mask"; filename="` + maskFilename + `"`},
			"Content-Type":        {maskMimeType},
		})
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to create mask form part", err)
		}
		if _, err := maskPart.Write(openaiReq.Mask); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write mask data", err)
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	return nil
}

func ToOpenAIImageVariationRequest(bifrostReq *schemas.BifrostImageVariationRequest) *OpenAIImageVariationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Image.Image == nil || len(bifrostReq.Input.Image.Image) == 0 {
		return nil
	}

	req := &OpenAIImageVariationRequest{
		Model: bifrostReq.Model,
		Input: bifrostReq.Input,
	}

	if bifrostReq.Params != nil {
		req.ImageVariationParameters = *bifrostReq.Params
	}

	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return req
}

func parseImageVariationFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAIImageVariationRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add model field (required)
	if err := writer.WriteField("model", openaiReq.Model); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write model field", err)
	}

	// Add image file (required)
	if openaiReq.Input == nil || openaiReq.Input.Image.Image == nil || len(openaiReq.Input.Image.Image) == 0 {
		return providerUtils.NewBifrostOperationError("image is required", nil)
	}

	// Add optional parameters before the image part so metadata arrives first upstream.
	if openaiReq.N != nil {
		if err := writer.WriteField("n", strconv.Itoa(*openaiReq.N)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write n field", err)
		}
	}

	if openaiReq.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *openaiReq.ResponseFormat); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write response_format field", err)
		}
	}

	if openaiReq.Size != nil {
		if err := writer.WriteField("size", *openaiReq.Size); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write size field", err)
		}
	}

	if openaiReq.User != nil {
		if err := writer.WriteField("user", *openaiReq.User); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write user field", err)
		}
	}

	// Detect MIME type
	mimeType := http.DetectContentType(openaiReq.Input.Image.Image)
	// If still not detected, default to PNG
	if mimeType == "application/octet-stream" || mimeType == "" {
		mimeType = "image/png"
	}

	filename := "image"
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="image"; filename="%s"`, filename)},
		"Content-Type":        {mimeType},
	})
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to create image part", err)
	}

	if _, err := part.Write(openaiReq.Input.Image.Image); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write image data", err)
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	return nil
}
