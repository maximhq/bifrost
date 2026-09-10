package compat

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

func cloneBifrostReq(req *schemas.BifrostRequest) *schemas.BifrostRequest {
	if req == nil {
		return nil
	}

	cloned := *req

	if req.TextCompletionRequest != nil {
		textReq := *req.TextCompletionRequest
		cloned.TextCompletionRequest = &textReq
		if req.TextCompletionRequest.Params != nil {
			params := *req.TextCompletionRequest.Params
			cloned.TextCompletionRequest.Params = &params
		}
	}
	if req.ChatRequest != nil {
		chatReq := *req.ChatRequest
		cloned.ChatRequest = &chatReq
		if req.ChatRequest.Params != nil {
			cloned.ChatRequest.Params = cloneChatParameters(req.ChatRequest.Params)
		}
	}
	if req.ResponsesRequest != nil {
		responsesReq := *req.ResponsesRequest
		cloned.ResponsesRequest = &responsesReq
		if req.ResponsesRequest.Params != nil {
			cloned.ResponsesRequest.Params = cloneResponsesParameters(req.ResponsesRequest.Params)
		}
	}

	return &cloned
}

func cloneChatParameters(params *schemas.ChatParameters) *schemas.ChatParameters {
	cloned := *params
	if params.Reasoning != nil {
		reasoning := *params.Reasoning
		cloned.Reasoning = &reasoning
	}
	if params.ToolChoice != nil {
		toolChoice := *params.ToolChoice
		cloned.ToolChoice = &toolChoice
	}
	if params.Tools != nil {
		cloned.Tools = slices.Clone(params.Tools)
	}
	return &cloned
}

func cloneResponsesParameters(params *schemas.ResponsesParameters) *schemas.ResponsesParameters {
	cloned := *params
	if params.Reasoning != nil {
		reasoning := *params.Reasoning
		cloned.Reasoning = &reasoning
	}
	if params.ToolChoice != nil {
		toolChoice := *params.ToolChoice
		if params.ToolChoice.ResponsesToolChoiceStruct != nil {
			choiceStruct := *params.ToolChoice.ResponsesToolChoiceStruct
			choiceStruct.Tools = slices.Clone(choiceStruct.Tools)
			toolChoice.ResponsesToolChoiceStruct = &choiceStruct
		}
		cloned.ToolChoice = &toolChoice
	}
	if params.Tools != nil {
		cloned.Tools = slices.Clone(params.Tools)
	}
	return &cloned
}
