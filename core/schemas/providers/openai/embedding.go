package openai

import (
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostRequest converts an OpenAI embedding request to Bifrost format
func (r *OpenAIEmbeddingRequest) ToBifrostRequest() *schemas.BifrostEmbeddingRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	// Create embedding input
	embeddingInput := &schemas.EmbeddingInput{}

	// Cleaner coercion: marshal input and try to unmarshal into supported shapes
	if raw, err := sonic.Marshal(r.Input); err == nil {
		// 1) string
		var s string
		if err := sonic.Unmarshal(raw, &s); err == nil {
			embeddingInput.Text = &s
		} else {
			// 2) []string
			var ss []string
			if err := sonic.Unmarshal(raw, &ss); err == nil {
				embeddingInput.Texts = ss
			} else {
				// 3) []int
				var i []int
				if err := sonic.Unmarshal(raw, &i); err == nil {
					embeddingInput.Embedding = i
				} else {
					// 4) [][]int
					var ii [][]int
					if err := sonic.Unmarshal(raw, &ii); err == nil {
						embeddingInput.Embeddings = ii
					}
				}
			}
		}
	}

	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input:    *embeddingInput,
		Params:   &r.EmbeddingParameters,
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
		openaiReq.EncodingFormat = params.EncodingFormat
		openaiReq.Dimensions = params.Dimensions
	}

	return openaiReq
}
