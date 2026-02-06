package bedrock

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockTitanEmbeddingRequest converts a Bifrost embedding request to Bedrock Titan format
func ToBedrockTitanEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockTitanEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	// Validate that only single text input is provided for Titan models
	if bifrostReq.Input.Text == nil && len(bifrostReq.Input.Texts) == 0 {
		return nil, fmt.Errorf("no input text provided for embedding")
	}

	// Validate dimensions parameter - Titan models do not support it
	if bifrostReq.Params != nil && bifrostReq.Params.Dimensions != nil {
		return nil, fmt.Errorf("amazon Titan embedding models do not support custom dimensions parameter")
	}

	titanReq := &BedrockTitanEmbeddingRequest{}

	// Set input text
	if bifrostReq.Input.Text != nil {
		titanReq.InputText = *bifrostReq.Input.Text
	} else if len(bifrostReq.Input.Texts) > 0 {
		var embeddingText string
		for _, text := range bifrostReq.Input.Texts {
			embeddingText += text + " \n"
		}
		titanReq.InputText = embeddingText
	}
	if bifrostReq.Params != nil {
		titanReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return titanReq, nil
}

// ToBifrostEmbeddingResponse converts a Bedrock Titan embedding response to Bifrost format
func (response *BedrockTitanEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Data: []schemas.EmbeddingData{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.EmbeddingStruct{
					EmbeddingArray: response.Embedding,
				},
			},
		},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens: response.InputTextTokenCount,
			TotalTokens:  response.InputTextTokenCount,
		},
	}

	return bifrostResponse
}

// ToBedrockCohereEmbeddingRequest converts a Bifrost embedding request to Bedrock Cohere format
// Reuses the Cohere converter since the format is identical
func ToBedrockCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*cohere.CohereEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	// Reuse Cohere's converter - the format is identical for Bedrock
	cohereReq := cohere.ToCohereEmbeddingRequest(bifrostReq)
	if cohereReq == nil {
		return nil, fmt.Errorf("failed to convert to Cohere embedding request")
	}

	return cohereReq, nil
}

// DetermineEmbeddingModelType determines the embedding model type from the model name
func DetermineEmbeddingModelType(model string) (string, error) {
	switch {
	case strings.Contains(model, "amazon.titan-embed-text"):
		return "titan", nil
	case strings.Contains(model, "cohere.embed"):
		return "cohere", nil
	default:
		return "", fmt.Errorf("unsupported embedding model: %s", model)
	}
}
