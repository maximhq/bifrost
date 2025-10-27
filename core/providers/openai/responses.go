package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostResponsesRequest converts an OpenAI responses request to Bifrost format
func (request *OpenAIResponsesRequest) ToBifrostResponsesRequest() *schemas.BifrostResponsesRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	input := request.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: request.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}

	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input:    input,
		Params:   &request.ResponsesParameters,
	}
}

// ToOpenAIResponsesRequest converts a Bifrost responses request to OpenAI format
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
