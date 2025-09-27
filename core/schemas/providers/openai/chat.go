package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostRequest converts an OpenAI chat request to Bifrost format
func (r *OpenAIChatRequest) ToBifrostRequest() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	params := r.convertParameters()

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &r.Messages,
		},
		Params: filterParams(provider, params),
	}

	return bifrostReq
}

// ToOpenAIChatCompletionRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatCompletionRequest(bifrostReq *schemas.BifrostRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input.ChatCompletionInput == nil {
		return nil
	}

	params := bifrostReq.Params

	openaiReq := &OpenAIChatRequest{
		Model:            bifrostReq.Model,
		Messages:         *bifrostReq.Input.ChatCompletionInput,
		CommonParameters: params.CommonParameters,
		ChatParameters:   params.ChatParameters,
	}

	return openaiReq
}
