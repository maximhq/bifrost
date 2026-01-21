package openai

import (
	"mime/multipart"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostTranscriptionRequest converts an OpenAI transcription request to Bifrost format
func (request *OpenAITranscriptionRequest) ToBifrostTranscriptionRequest(ctx *schemas.BifrostContext) *schemas.BifrostTranscriptionRequest {
	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	return &schemas.BifrostTranscriptionRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.TranscriptionInput{
			File: request.File,
		},
		Params:    &request.TranscriptionParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

// ToOpenAITranscriptionRequest converts a Bifrost transcription request to OpenAI format
func ToOpenAITranscriptionRequest(bifrostReq *schemas.BifrostTranscriptionRequest) *OpenAITranscriptionRequest {
	if bifrostReq == nil || bifrostReq.Input.File == nil {
		return nil
	}

	transcriptionInput := bifrostReq.Input
	params := bifrostReq.Params

	openaiReq := &OpenAITranscriptionRequest{
		Model: bifrostReq.Model,
		File:  transcriptionInput.File,
	}

	if params != nil {
		openaiReq.TranscriptionParameters = *params
	}

	return openaiReq
}

// parseTranscriptionFormDataBodyFromRequest parses the transcription request and writes it to the multipart form.
func parseTranscriptionFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAITranscriptionRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add file field
	fileWriter, err := writer.CreateFormFile("file", "audio.mp3") // OpenAI requires a filename
	if err != nil {
		return utils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := fileWriter.Write(openaiReq.File); err != nil {
		return utils.NewBifrostOperationError("failed to write file data", err, providerName)
	}

	// Add model field
	if err := writer.WriteField("model", openaiReq.Model); err != nil {
		return utils.NewBifrostOperationError("failed to write model field", err, providerName)
	}

	// Add optional fields
	if openaiReq.Language != nil {
		if err := writer.WriteField("language", *openaiReq.Language); err != nil {
			return utils.NewBifrostOperationError("failed to write language field", err, providerName)
		}
	}

	if openaiReq.Prompt != nil {
		if err := writer.WriteField("prompt", *openaiReq.Prompt); err != nil {
			return utils.NewBifrostOperationError("failed to write prompt field", err, providerName)
		}
	}

	if openaiReq.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *openaiReq.ResponseFormat); err != nil {
			return utils.NewBifrostOperationError("failed to write response_format field", err, providerName)
		}
	}

	if openaiReq.Stream != nil && *openaiReq.Stream {
		if err := writer.WriteField("stream", "true"); err != nil {
			return utils.NewBifrostOperationError("failed to write stream field", err, providerName)
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return utils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	return nil
}
