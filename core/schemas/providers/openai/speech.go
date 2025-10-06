package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostRequest converts an OpenAI speech request to Bifrost format
func (r *OpenAISpeechRequest) ToBifrostRequest() *schemas.BifrostSpeechRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostSpeechRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.SpeechInput{Input: r.Input},
		Params:   &r.SpeechParameters,
	}

	return bifrostReq
}

// ToOpenAISpeechResponse converts a Bifrost speech response to OpenAI format
func ToOpenAISpeechResponse(bifrostResp *schemas.BifrostResponse) *schemas.BifrostSpeech {
	if bifrostResp == nil || bifrostResp.Speech == nil {
		return nil
	}

	return bifrostResp.Speech
}

// ToOpenAISpeechRequest converts a Bifrost speech request to OpenAI format
func ToOpenAISpeechRequest(bifrostReq *schemas.BifrostSpeechRequest) *OpenAISpeechRequest {
	if bifrostReq == nil || bifrostReq.Input.Input == "" {
		return nil
	}

	speechInput := bifrostReq.Input
	params := bifrostReq.Params

	openaiReq := &OpenAISpeechRequest{
		Model: bifrostReq.Model,
		Input: speechInput.Input,
	}

	if params != nil {
		openaiReq.SpeechParameters = *params
	}

	return openaiReq
}
