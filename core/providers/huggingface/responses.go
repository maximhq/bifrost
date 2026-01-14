package huggingface

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToHuggingFaceResponsesRequest converts a Bifrost Responses request into the Hugging Face
// chat-completions payload that the provider already understands.
func ToHuggingFaceResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*HuggingFaceChatRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	chatReq := bifrostReq.ToChatRequest()
	if chatReq == nil {
		return nil, fmt.Errorf("failed to convert responses request to chat request")
	}

	hfReq, err := ToHuggingFaceChatCompletionRequest(chatReq)
	if err != nil {
		return nil, err
	}
	if hfReq == nil {
		return nil, fmt.Errorf("failed to convert chat request to Hugging Face request")
	}

	return hfReq, nil
}

// ToBifrostResponsesResponseFromHuggingFace converts a Bifrost chat response into the
// Bifrost Responses response shape, preserving provider metadata.
func ToBifrostResponsesResponseFromHuggingFace(resp *schemas.BifrostChatResponse, requestedModel string) (*schemas.BifrostResponsesResponse, error) {
	if resp == nil {
		return nil, nil
	}

	// Ensure model is set
	if resp.Model == "" {
		resp.Model = requestedModel
	}

	responsesResp := resp.ToBifrostResponsesResponse()
	if responsesResp != nil {
		responsesResp.ExtraFields.Provider = schemas.HuggingFace
		responsesResp.ExtraFields.ModelRequested = requestedModel
		responsesResp.ExtraFields.RequestType = schemas.ResponsesRequest
	}

	return responsesResp, nil
}
