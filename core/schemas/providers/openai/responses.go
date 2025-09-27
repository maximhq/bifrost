package openai

import "github.com/maximhq/bifrost/core/schemas"

func (r *OpenAIResponsesRequest) ToBifrostRequest() *schemas.BifrostRequest {
	if r == nil {
		return nil
	}

	return &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    r.Model,
		Input: schemas.RequestInput{
			ResponsesInput: &r.Input,
		},
		Params: &schemas.ModelParameters{
			CommonParameters:    r.CommonParameters,
			ResponsesParameters: r.ResponsesParameters,
		},
	}
}

func ToOpenAIResponsesRequest(bifrostReq *schemas.BifrostRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input.ResponsesInput == nil {
		return nil
	}

	params := bifrostReq.Params

	// Create the responses request with properly mapped parameters
	req := &OpenAIResponsesRequest{
		Model:               bifrostReq.Model,
		Input:               *bifrostReq.Input.ResponsesInput,
		CommonParameters:    params.CommonParameters,
		ResponsesParameters: params.ResponsesParameters,
	}

	return req
}
