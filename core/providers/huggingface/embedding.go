package huggingface

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToHuggingFaceEmbeddingRequest converts a Bifrost embedding request to HuggingFace format
func ToHuggingFaceEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*HuggingFaceEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(bifrostReq.Model)
	if nameErr != nil {
		return nil, nameErr
	}

	hfReq := &HuggingFaceEmbeddingRequest{
		Model:    modelName,
		Provider: string(inferenceProvider),
	}

	// Convert input
	if bifrostReq.Input != nil {
		if bifrostReq.Input.Text != nil {
			hfReq.Input = *bifrostReq.Input.Text
		} else if bifrostReq.Input.Texts != nil {
			hfReq.Input = bifrostReq.Input.Texts
		}
		// Note: HuggingFace embeddings API doesn't support embedding/embeddings (int arrays)
		// Those are typically used for reranking tasks
	}

	// Map parameters
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		// Check for HuggingFace-specific parameters in ExtraParams
		if params.ExtraParams != nil {
			if normalize, ok := params.ExtraParams["normalize"].(bool); ok {
				hfReq.Normalize = &normalize
			}
			if promptName, ok := params.ExtraParams["prompt_name"].(string); ok {
				hfReq.PromptName = &promptName
			}
			if truncate, ok := params.ExtraParams["truncate"].(bool); ok {
				hfReq.Truncate = &truncate
			}
			if truncationDirection, ok := params.ExtraParams["truncation_direction"].(string); ok {
				hfReq.TruncationDirection = &truncationDirection
			}
		}
	}

	return hfReq, nil
}

// ToBifrostEmbeddingResponse converts a HuggingFace embedding response to Bifrost format
func (response *HuggingFaceEmbeddingResponse) ToBifrostEmbeddingResponse(model string) (*schemas.BifrostEmbeddingResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("huggingface embedding response is nil")
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{
		Model:  model,
		Object: "list",
	}

	// Convert embeddings from HuggingFace format to Bifrost format
	bifrostEmbeddings := make([]schemas.EmbeddingData, 0, len(response.Data))

	for _, embeddingData := range response.Data {
		bifrostEmbedding := schemas.EmbeddingData{
			Object: embeddingData.Object,
			Index:  embeddingData.Index,
			Embedding: schemas.EmbeddingStruct{
				EmbeddingArray: embeddingData.Embedding,
			},
		}
		bifrostEmbeddings = append(bifrostEmbeddings, bifrostEmbedding)
	}

	bifrostResponse.Data = bifrostEmbeddings

	// Map usage information if available
	if response.Usage != nil {
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}
	} else {
		// Set empty usage if not provided
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		}
	}

	return bifrostResponse, nil
}
