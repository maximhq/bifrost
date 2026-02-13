package openai

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIVideoGenerationRequest converts a Bifrost Video Request to OpenAI format
func ToOpenAIVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*OpenAIVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil, fmt.Errorf("bifrost request or input is nil")
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

func ToOpenAIVideoRemixRequest(bifrostReq *schemas.BifrostVideoRemixRequest) *OpenAIVideoRemixRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil
	}

	req := &OpenAIVideoRemixRequest{
		Prompt: bifrostReq.Input.Prompt,
	}

	return req
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

func (request *OpenAIVideoGenerationRequest) ToBifrostVideoGenerationRequest(ctx *schemas.BifrostContext) *schemas.BifrostVideoGenerationRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	input := &schemas.VideoGenerationInput{
		Prompt: request.Prompt,
	}
	if request.InputReference != nil {
		input.InputReference = schemas.Ptr(providerUtils.FileBytesToBase64DataURL(request.InputReference))
	}

	return &schemas.BifrostVideoGenerationRequest{
		Provider:  provider,
		Model:     model,
		Input:     input,
		Params:    &request.VideoGenerationParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

func ToOpenaiVideoDownloadResponse(bifrostResp *schemas.BifrostVideoGenerationResponse) ([]byte, error) {
	if bifrostResp == nil {
		return nil, errors.New("video generation response is nil or no videos found")
	}

	if bifrostResp.Status != schemas.VideoStatusCompleted {
		return nil, ErrVideoNotReady
	}

	if len(bifrostResp.Videos) == 0 {
		return nil, errors.New("no videos found in response")
	}

	video := bifrostResp.Videos[0]

	// Case 1: Provider gave us base64 content directly
	if video.Base64Data != nil && *video.Base64Data != "" {
		data, err := base64.StdEncoding.DecodeString(*video.Base64Data)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 video: %w", err)
		}
		return data, nil
	}

	// Case 2: Provider gave us a URL — proxy-fetch the content
	if video.URL != nil && *video.URL != "" {
		content, bifrostErr := fetchVideoContent(video.URL)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		return content, nil
	}

	return nil, nil
}

func fetchVideoContent(videoURL *string) ([]byte, error) {
	if videoURL == nil || *videoURL == "" {
		return nil, errors.New("video URL is empty")
	}

	resp, err := http.Get(*videoURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download video: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download video: HTTP %d", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read video content: %w", err)
	}

	return content, nil
}

// parseVideoGenerationFormDataBodyFromRequest parses the video generation request and writes it to the multipart form.
func parseVideoGenerationFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAIVideoGenerationRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add prompt field (required)
	if openaiReq.Prompt == "" {
		return providerUtils.NewBifrostOperationError("prompt is required", nil, providerName)
	}
	if err := writer.WriteField("prompt", openaiReq.Prompt); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write prompt field", err, providerName)
	}

	// Add optional model field
	if openaiReq.Model != "" {
		if err := writer.WriteField("model", openaiReq.Model); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write model field", err, providerName)
		}
	}

	// Add optional seconds field
	if openaiReq.Seconds != nil {
		if err := writer.WriteField("seconds", *openaiReq.Seconds); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write seconds field", err, providerName)
		}
	}

	// Add optional size field
	if openaiReq.Size != "" {
		if err := writer.WriteField("size", openaiReq.Size); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write size field", err, providerName)
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
			return providerUtils.NewBifrostOperationError("failed to create form part for input_reference", err, providerName)
		}
		if _, err := part.Write(openaiReq.InputReference); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write input_reference file data", err, providerName)
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	return nil
}
