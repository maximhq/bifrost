package openai

import "github.com/maximhq/bifrost/core/schemas"

func (r *OpenAIResponsesRequest) ToBifrostRequest() *schemas.BifrostResponsesRequest {
	if r == nil {
		return nil
	}

	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    r.Model,
		Input:    r.Input,
		Params:   &r.ResponsesParameters,
	}
}

func ToOpenAIResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	params := bifrostReq.Params

	// Create the responses request with properly mapped parameters
	req := &OpenAIResponsesRequest{
		Model: bifrostReq.Model,
		Input: bifrostReq.Input,
	}

	if params != nil {
		req.ResponsesParameters = *params
	}

	return req
}
