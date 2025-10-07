package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostRequest converts an OpenAI embedding request to Bifrost format
func (r *OpenAIEmbeddingRequest) ToBifrostRequest() *schemas.BifrostEmbeddingRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input:    r.Input,
		Params:   &r.EmbeddingParameters,
	}

	return bifrostReq
}

// ToOpenAIEmbeddingRequest converts a Bifrost embedding request to OpenAI format
func ToOpenAIEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *OpenAIEmbeddingRequest {
	if bifrostReq == nil {
		return nil
	}

	openaiReq := AcquireEmbeddingRequest()
	openaiReq.Model = bifrostReq.Model
	openaiReq.Input = bifrostReq.Input // schemas.EmbeddingInput - not pooled per user instruction

	// Map parameters
	if bifrostReq.Params != nil {
		openaiReq.EmbeddingParameters = *bifrostReq.Params
	}

	return openaiReq
}
