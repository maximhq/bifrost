package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostRequest converts an OpenAI chat request to Bifrost format
func (r *OpenAIChatRequest) ToBifrostRequest() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	params := r.convertParameters()

	messages := sanitizeImageInputs(r.Messages)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &messages,
		},
		Params: filterParams(provider, params),
	}

	return bifrostReq
}

// ToOpenAIChatCompletionResponse converts a Bifrost response to OpenAI format
func ToOpenAIChatCompletionResponse(bifrostResp *schemas.BifrostResponse) *OpenAIChatResponse {
	if bifrostResp == nil {
		return nil
	}

	openaiResp := &OpenAIChatResponse{
		ID:                bifrostResp.ID,
		Object:            bifrostResp.Object,
		Created:           bifrostResp.Created,
		Model:             bifrostResp.Model,
		Choices:           bifrostResp.Choices,
		Usage:             bifrostResp.Usage,
		ServiceTier:       bifrostResp.ServiceTier,
		SystemFingerprint: bifrostResp.SystemFingerprint,
	}

	return openaiResp
}

// ToOpenAIChatCompletionRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatCompletionRequest(bifrostReq *schemas.BifrostRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input.ChatCompletionInput == nil {
		return nil
	}

	messages := *bifrostReq.Input.ChatCompletionInput
	params := bifrostReq.Params

	openaiReq := &OpenAIChatRequest{
		Model:           bifrostReq.Model,
		Messages:        messages,
		ModelParameters: params, // Directly embed the parameters
	}

	return openaiReq
}
