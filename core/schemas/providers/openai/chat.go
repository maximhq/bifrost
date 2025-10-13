package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostRequest converts an OpenAI chat request to Bifrost format
func (request *OpenAIChatRequest) ToBifrostRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    request.Messages,
		Params:   &request.ChatParameters,
	}

	return bifrostReq
}

// ToOpenAIChatRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatRequest(bifrostReq *schemas.BifrostChatRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	openaiReq := &OpenAIChatRequest{
		Model:    bifrostReq.Model,
		Messages: bifrostReq.Input,
	}

	if bifrostReq.Params != nil {
		openaiReq.ChatParameters = *bifrostReq.Params
	}

	return openaiReq
}
