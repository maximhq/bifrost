package huggingface

import (
	"context"
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

	hfReq := ToHuggingFaceChatCompletionRequest(chatReq)
	if hfReq == nil {
		return nil, fmt.Errorf("failed to convert chat request to Hugging Face request")
	}

	return hfReq, nil
}

// getRequestBodyForResponses prepares the JSON body for Hugging Face Responses and streaming calls.
// It mirrors the pattern used by other providers (e.g., Anthropic) to support raw payload passthrough
// and contextual overrides while reusing the chat converter for request construction.
func getRequestBodyForResponses(ctx context.Context, request *schemas.BifrostResponsesRequest, providerName schemas.ModelProvider, isStreaming bool) ([]byte, *schemas.BifrostError) {
	return providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			hfReq, err := ToHuggingFaceResponsesRequest(request)
			if err != nil {
				return nil, err
			}

			// Ensure streaming requests include usage to terminate the stream cleanly.
			if isStreaming {
				if hfReq.StreamOptions == nil {
					hfReq.StreamOptions = &HuggingFaceStreamOptions{}
				}
				hfReq.StreamOptions.IncludeUsage = schemas.Ptr(true)
			}

			return hfReq, nil
		},
		providerName,
	)
}

// ToBifrostResponsesResponseFromHuggingFace converts a Hugging Face chat response into the
// Bifrost Responses response shape, preserving provider metadata.
func ToBifrostResponsesResponseFromHuggingFace(resp *HuggingFaceChatResponse, requestedModel string) (*schemas.BifrostResponsesResponse, error) {
	if resp == nil {
		return nil, nil
	}

	chatResp, err := resp.ToBifrostChatResponse(requestedModel)
	if err != nil {
		return nil, err
	}

	responsesResp := chatResp.ToBifrostResponsesResponse()
	if responsesResp != nil {
		responsesResp.ExtraFields.Provider = schemas.HuggingFace
		responsesResp.ExtraFields.ModelRequested = requestedModel
		responsesResp.ExtraFields.RequestType = schemas.ResponsesRequest
	}

	return responsesResp, nil
}

// ToBifrostResponsesStreamResponses converts a Hugging Face streaming chat response chunk into
// Bifrost Responses streaming responses using the shared chat-to-responses converter.
func (resp *HuggingFaceChatStreamResponse) ToBifrostResponsesStreamResponses(state *schemas.ChatToResponsesStreamState) []*schemas.BifrostResponsesStreamResponse {
	if resp == nil {
		return nil
	}

	chatResp := resp.ToBifrostChatStreamResponse()
	if chatResp == nil {
		return nil
	}

	return chatResp.ToBifrostResponsesStreamResponse(state)
}
