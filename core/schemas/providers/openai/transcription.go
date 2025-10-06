package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostRequest converts an OpenAI transcription request to Bifrost format
func (r *OpenAITranscriptionRequest) ToBifrostRequest() *schemas.BifrostTranscriptionRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostTranscriptionRequest{
		Provider: provider,
		Model:    model,
		Input: &schemas.TranscriptionInput{
			File: r.File,
		},
		Params: &r.TranscriptionParameters,
	}

	return bifrostReq
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
