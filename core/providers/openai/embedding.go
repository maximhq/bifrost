package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostEmbeddingRequest converts an OpenAI embedding request to Bifrost format
func (request *OpenAIEmbeddingRequest) ToBifrostEmbeddingRequest() *schemas.BifrostEmbeddingRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input:    request.Input,
		Params:   &request.EmbeddingParameters,
	}

	return bifrostReq
}

// ToOpenAIEmbeddingRequest converts a Bifrost embedding request to OpenAI format
func ToOpenAIEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *OpenAIEmbeddingRequest {
	if bifrostReq == nil {
		return nil
	}

	params := bifrostReq.Params

	openaiReq := &OpenAIEmbeddingRequest{
		Model: bifrostReq.Model,
		Input: bifrostReq.Input,
	}

	// Map parameters
	if params != nil {
		openaiReq.EmbeddingParameters = *params
	}

	return openaiReq
}
