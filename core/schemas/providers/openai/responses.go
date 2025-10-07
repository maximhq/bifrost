package openai

import "github.com/maximhq/bifrost/core/schemas"

func (r *OpenAIResponsesRequest) ToBifrostRequest() *schemas.BifrostResponsesRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	input := r.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: r.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}

	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input:    input,
		Params:   &r.ResponsesParameters,
	}
}

func ToOpenAIResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}
	// Preparing final input
	input := OpenAIResponsesRequestInput{
		OpenAIResponsesRequestInputArray: bifrostReq.Input,
	}
	// Updating params
	params := bifrostReq.Params
	// Create the responses request with properly mapped parameters
	req := &OpenAIResponsesRequest{
		Model: bifrostReq.Model,
		Input: input,
	}

	if params != nil {
		req.ResponsesParameters = *params
	}

	return req
}
