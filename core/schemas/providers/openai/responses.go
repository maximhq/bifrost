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
		// Filter out tools that OpenAI doesn't support
		req.filterUnsupportedTools()
	}

	return req
}

// filterUnsupportedTools removes tool types that OpenAI doesn't support
func (req *OpenAIResponsesRequest) filterUnsupportedTools() {
	if len(req.Tools) == 0 {
		return
	}

	// Define OpenAI-supported tool types
	supportedTypes := map[schemas.ResponsesToolType]bool{
		schemas.ResponsesToolTypeFunction:           true,
		schemas.ResponsesToolTypeFileSearch:         true,
		schemas.ResponsesToolTypeComputerUsePreview: true,
		schemas.ResponsesToolTypeWebSearch:          true,
		schemas.ResponsesToolTypeMCP:                true,
		schemas.ResponsesToolTypeCodeInterpreter:    true,
		schemas.ResponsesToolTypeImageGeneration:    true,
		schemas.ResponsesToolTypeLocalShell:         true,
		schemas.ResponsesToolTypeCustom:             true,
		schemas.ResponsesToolTypeWebSearchPreview:   true,
	}

	// Filter tools to only include supported types
	filteredTools := make([]schemas.ResponsesTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		if supportedTypes[tool.Type] {
			filteredTools = append(filteredTools, tool)
		}
	}
	req.Tools = filteredTools
}
