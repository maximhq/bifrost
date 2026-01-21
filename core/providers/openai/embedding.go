package openai

import (
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostEmbeddingRequest converts an OpenAI embedding request to Bifrost format
func (request *OpenAIEmbeddingRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) *schemas.BifrostEmbeddingRequest {
	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	return &schemas.BifrostEmbeddingRequest{
		Provider:  provider,
		Model:     model,
		Input:     request.Input,
		Params:    &request.EmbeddingParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
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
