package openai

import (
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIVideoGenerationRequest converts a Bifrost Video Request to OpenAI format
func ToOpenAIVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*OpenAIVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil, fmt.Errorf("bifrost request, input, or prompt is nil/empty")
	}

	req := &OpenAIVideoGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	if bifrostReq.Input.InputReference != nil {
		// convert base64 to bytes
		sanitizedURL, err := schemas.SanitizeImageURL(*bifrostReq.Input.InputReference)
		if err != nil {
			return nil, fmt.Errorf("invalid input reference: %w", err)
		}
		urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
		if urlInfo.DataURLWithoutPrefix != nil {
			bytes, err := base64.StdEncoding.DecodeString(*urlInfo.DataURLWithoutPrefix)
			if err != nil {
				return nil, fmt.Errorf("failed to decode base64 input reference: %w", err)
			}
			req.InputReference = bytes
		} else {
			return nil, fmt.Errorf("input_reference must be a base64 data URL (e.g. data:image/png;base64,...)")
		}
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.Seconds != nil {
			req.Seconds = bifrostReq.Params.Seconds
		}

		// Validate and set size
		if bifrostReq.Params.Size != "" {
			// Check if the provided size is valid
			if ValidOpenAIVideoSizes[bifrostReq.Params.Size] {
				req.Size = bifrostReq.Params.Size
			} else {
				// Invalid size provided, use default
				req.Size = string(DefaultOpenAIVideoSize)
			}
		} else {
			// No size provided, use default
			req.Size = string(DefaultOpenAIVideoSize)
		}

		req.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return req, nil
}

// ToOpenAIVideoEditRequest converts a Bifrost video edit request to OpenAI format. OpenAI takes the
// source video either as an uploaded file or as the ID of a completed video; a URL has no
// equivalent on their API, so it is rejected rather than silently dropped — it is the sole input.
func ToOpenAIVideoEditRequest(bifrostReq *schemas.BifrostVideoEditRequest) (*OpenAIVideoEditRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil, fmt.Errorf("bifrost request, input, or prompt is nil/empty")
	}

	req := &OpenAIVideoEditRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	source := bifrostReq.Input.Video
	switch {
	case len(source.Video) > 0:
		req.Video.Bytes = source.Video
	case source.ID != "":
		req.Video.ID = providerUtils.StripVideoIDProviderSuffix(source.ID, schemas.OpenAI)
	case source.URL != "":
		return nil, fmt.Errorf("openai video edit accepts an uploaded video or a video id, not a url")
	default:
		return nil, fmt.Errorf("source video is required")
	}

	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return req, nil
}

// ToBifrostVideoEditRequest converts an inbound OpenAI-format video edit request to Bifrost format.
func ToBifrostVideoEditRequest(openaiReq *OpenAIVideoEditRequest) *schemas.BifrostVideoEditRequest {
	if openaiReq == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(openaiReq.Model, "")

	return &schemas.BifrostVideoEditRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.VideoEditInput{
			Prompt: openaiReq.Prompt,
			Video: schemas.VideoInput{
				ID:    openaiReq.Video.ID,
				Video: openaiReq.Video.Bytes,
			},
		},
		Fallbacks: schemas.ParseFallbacks(openaiReq.Fallbacks),
	}
}

// parseVideoEditFormDataBodyFromRequest writes an uploaded-file video edit to a multipart form.
func parseVideoEditFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAIVideoEditRequest) *schemas.BifrostError {
	if err := writer.WriteField("prompt", openaiReq.Prompt); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write prompt field", err)
	}

	if openaiReq.Model != "" {
		if err := writer.WriteField("model", openaiReq.Model); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write model field", err)
		}
	}

	mimeType := http.DetectContentType(openaiReq.Video.Bytes)
	if !validOpenAIVideoEditMimeTypes[mimeType] {
		mimeType = "video/mp4"
	}
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name="video"; filename="video%s"`, openAIVideoEditExtensions[mimeType])},
		"Content-Type":        {mimeType},
	})
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to create form part for video", err)
	}
	if _, err := part.Write(openaiReq.Video.Bytes); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write video file data", err)
	}

	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	return nil
}

var validOpenAIVideoEditMimeTypes = map[string]bool{
	"video/mp4":       true,
	"video/webm":      true,
	"video/quicktime": true,
}

var openAIVideoEditExtensions = map[string]string{
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"video/quicktime": ".mov",
}

func ToOpenAIVideoRemixRequest(bifrostReq *schemas.BifrostVideoRemixRequest) (*OpenAIVideoRemixRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil, fmt.Errorf("bifrost request, input, or prompt is nil/empty")
	}

	req := &OpenAIVideoRemixRequest{
		Prompt: bifrostReq.Input.Prompt,
	}

	return req, nil
}

func ToBifrostVideoRemixRequest(openaiReq *OpenAIVideoRemixRequest) *schemas.BifrostVideoRemixRequest {
	if openaiReq == nil || openaiReq.Prompt == "" {
		return nil
	}

	provider := openaiReq.Provider
	if provider == "" {
		provider = schemas.OpenAI
	}

	return &schemas.BifrostVideoRemixRequest{
		ID:       openaiReq.ID,
		Provider: provider,
		Input: &schemas.VideoGenerationInput{
			Prompt: openaiReq.Prompt,
		},
	}
}

func (req *OpenAIVideoGenerationRequest) ToBifrostVideoGenerationRequest(ctx *schemas.BifrostContext) *schemas.BifrostVideoGenerationRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, "")

	input := &schemas.VideoGenerationInput{
		Prompt:   req.Prompt,
		VideoURI: req.VideoURI,
	}
	if req.InputReference != nil {
		input.InputReference = schemas.Ptr(providerUtils.FileBytesToBase64DataURL(req.InputReference))
	}

	return &schemas.BifrostVideoGenerationRequest{
		Provider:  provider,
		Model:     model,
		Input:     input,
		Params:    &req.VideoGenerationParameters,
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}
}

// parseVideoGenerationFormDataBodyFromRequest parses the video generation request and writes it to the multipart form.
func parseVideoGenerationFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAIVideoGenerationRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add prompt field (required)
	if openaiReq.Prompt == "" {
		return providerUtils.NewBifrostOperationError("prompt is required", nil)
	}
	if err := writer.WriteField("prompt", openaiReq.Prompt); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write prompt field", err)
	}

	// Add optional model field
	if openaiReq.Model != "" {
		if err := writer.WriteField("model", openaiReq.Model); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write model field", err)
		}
	}

	// Add optional seconds field
	if openaiReq.Seconds != nil {
		if err := writer.WriteField("seconds", *openaiReq.Seconds); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write seconds field", err)
		}
	}

	// Add optional size field
	if openaiReq.Size != "" {
		if err := writer.WriteField("size", openaiReq.Size); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write size field", err)
		}
	}

	// Add optional input_reference field (image or video file)
	if len(openaiReq.InputReference) > 0 {
		// Detect MIME type
		mimeType := http.DetectContentType(openaiReq.InputReference)

		// Validate and set proper MIME type
		validMimeTypes := map[string]bool{
			"image/jpeg": true,
			"image/png":  true,
			"image/webp": true,
			"video/mp4":  true,
		}

		if !validMimeTypes[mimeType] {
			// Default to image/png if not detected properly
			mimeType = "image/png"
		}

		// Determine filename based on MIME type
		var filename string
		switch mimeType {
		case "image/jpeg":
			filename = "input_reference.jpg"
		case "image/webp":
			filename = "input_reference.webp"
		case "video/mp4":
			filename = "input_reference.mp4"
		default:
			filename = "input_reference.png"
		}

		// Create form part with proper Content-Type header
		part, err := writer.CreatePart(map[string][]string{
			"Content-Disposition": {fmt.Sprintf(`form-data; name="input_reference"; filename="%s"`, filename)},
			"Content-Type":        {mimeType},
		})
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to create form part for input_reference", err)
		}
		if _, err := part.Write(openaiReq.InputReference); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write input_reference file data", err)
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	return nil
}
