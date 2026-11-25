package openai

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"

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
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Images == nil || bifrostReq.Input.Prompt == "" {
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

// hasImageURLReference reports whether any input image is given only by URL, which multipart cannot carry.
func hasImageURLReference(openaiReq *OpenAIImageEditRequest) bool {
	for _, img := range openaiReq.Input.Images {
		if len(img.Image) == 0 && img.URL != "" {
			return true
		}
	}
	return false
}

// ToOpenAIImageEditJSONRequest builds the application/json form of an image edit. Byte inputs
// become base64 data URLs so they can sit in the same images array as URL references.
func ToOpenAIImageEditJSONRequest(openaiReq *OpenAIImageEditRequest) *OpenAIImageEditJSONRequest {
	images := make([]OpenAIImageReference, 0, len(openaiReq.Input.Images))
	for _, img := range openaiReq.Input.Images {
		if len(img.Image) > 0 {
			images = append(images, OpenAIImageReference{ImageURL: providerUtils.FileBytesToBase64DataURL(img.Image)})
		} else {
			images = append(images, OpenAIImageReference{ImageURL: img.URL})
		}
	}
	jsonReq := &OpenAIImageEditJSONRequest{
		Model:             openaiReq.Model,
		Prompt:            openaiReq.Input.Prompt,
		Images:            images,
		Stream:            openaiReq.Stream,
		N:                 openaiReq.N,
		Size:              openaiReq.Size,
		ResponseFormat:    openaiReq.ResponseFormat,
		Quality:           openaiReq.Quality,
		Background:        openaiReq.Background,
		InputFidelity:     openaiReq.InputFidelity,
		PartialImages:     openaiReq.PartialImages,
		OutputFormat:      openaiReq.OutputFormat,
		OutputCompression: openaiReq.OutputCompression,
		User:              openaiReq.User,
		ExtraParams:       openaiReq.ExtraParams,
	}
	if len(openaiReq.Mask) > 0 {
		jsonReq.Mask = &OpenAIImageReference{ImageURL: providerUtils.FileBytesToBase64DataURL(openaiReq.Mask)}
	}
	return jsonReq
}

// buildImageEditBody encodes an image edit as multipart, or as JSON when an input image is a URL.
// It returns the body and its Content-Type.
func buildImageEditBody(ctx *schemas.BifrostContext, openaiReq *OpenAIImageEditRequest, providerName schemas.ModelProvider) ([]byte, string, *schemas.BifrostError) {
	if hasImageURLReference(openaiReq) {
		jsonBody, err := providerUtils.MarshalProviderRequest(ToOpenAIImageEditJSONRequest(openaiReq))
		if err != nil {
			return nil, "", providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		}
		if passthrough, _ := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams).(bool); passthrough && len(openaiReq.ExtraParams) > 0 {
			jsonBody, err = providerUtils.MergeExtraParamsIntoJSON(jsonBody, openaiReq.ExtraParams)
			if err != nil {
				return nil, "", providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
			}
		}
		return jsonBody, "application/json", nil
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if bifrostErr := parseImageEditFormDataBodyFromRequest(writer, openaiReq, providerName); bifrostErr != nil {
		return nil, "", bifrostErr
	}
	return body.Bytes(), writer.FormDataContentType(), nil
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
