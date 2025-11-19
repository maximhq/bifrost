package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostTranscriptionRequest converts an OpenAI transcription request to Bifrost format
func (request *OpenAITranscriptionRequest) ToBifrostTranscriptionRequest() *schemas.BifrostTranscriptionRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

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
