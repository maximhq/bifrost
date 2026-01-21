package cohere

import (
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereEmbeddingRequest converts a Bifrost embedding request to Cohere format
func ToCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *CohereEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || (bifrostReq.Input.Text == nil && bifrostReq.Input.Texts == nil) {
		return nil
	}

	embeddingInput := bifrostReq.Input
	cohereReq := &CohereEmbeddingRequest{
		Model: bifrostReq.Model,
	}

	texts := []string{}
	if embeddingInput.Text != nil {
		texts = append(texts, *embeddingInput.Text)
	} else {
		texts = embeddingInput.Texts
	}

	// Convert texts from Bifrost format
	if len(texts) > 0 {
		cohereReq.Texts = texts
	}

	// Set default input type if not specified in extra params
	cohereReq.InputType = "search_document" // Default value

	if bifrostReq.Params != nil {
		cohereReq.OutputDimension = bifrostReq.Params.Dimensions

		if bifrostReq.Params.ExtraParams != nil {
			if maxTokens, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["max_tokens"]); ok {
				cohereReq.MaxTokens = maxTokens
			}
		}
	}

	// Handle extra params
	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		// Input type
		if inputType, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["input_type"]); ok {
			cohereReq.InputType = inputType
		}

		// Embedding types
		if embeddingTypes, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["embedding_types"]); ok {
			if len(embeddingTypes) > 0 {
				cohereReq.EmbeddingTypes = embeddingTypes
			}
		}

		// Truncate
		if truncate, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["truncate"]); ok {
			cohereReq.Truncate = truncate
		}
	}

	return cohereReq
}

// ToBifrostEmbeddingRequest converts a Cohere embedding request to Bifrost format
func (req *CohereEmbeddingRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) *schemas.BifrostEmbeddingRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Cohere))

	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input:    &schemas.EmbeddingInput{},
		Params:   &schemas.EmbeddingParameters{},
	}

	// Convert texts
	if len(req.Texts) > 0 {
		if len(req.Texts) == 1 {
			bifrostReq.Input.Text = &req.Texts[0]
		} else {
			bifrostReq.Input.Texts = req.Texts
		}
	}

	// Convert parameters
	if req.OutputDimension != nil {
		bifrostReq.Params.Dimensions = req.OutputDimension
	}

	// Convert extra params
	extraParams := make(map[string]interface{})
	if req.InputType != "" {
		extraParams["input_type"] = req.InputType
	}
	if req.EmbeddingTypes != nil {
		extraParams["embedding_types"] = req.EmbeddingTypes
	}
	if req.Truncate != nil {
		extraParams["truncate"] = *req.Truncate
	}
	if req.MaxTokens != nil {
		extraParams["max_tokens"] = *req.MaxTokens
	}
	if len(extraParams) > 0 {
		bifrostReq.Params.ExtraParams = extraParams
	}

	return bifrostReq
}

// ToBifrostEmbeddingResponse converts a Cohere embedding response to Bifrost format
func (response *CohereEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{
		Object: "list",
	}

	// Convert embeddings data
	if response.Embeddings != nil {
		var bifrostEmbeddings []schemas.EmbeddingData

		// Handle different embedding types - prioritize float embeddings
		if response.Embeddings.Float != nil {
			for i, embedding := range response.Embeddings.Float {
				bifrostEmbedding := schemas.EmbeddingData{
					Object: "embedding",
					Index:  i,
					Embedding: schemas.EmbeddingStruct{
						EmbeddingArray: embedding,
					},
				}
				bifrostEmbeddings = append(bifrostEmbeddings, bifrostEmbedding)
			}
		} else if response.Embeddings.Base64 != nil {
			// Handle base64 embeddings as strings
			for i, embedding := range response.Embeddings.Base64 {
				bifrostEmbedding := schemas.EmbeddingData{
					Object: "embedding",
					Index:  i,
					Embedding: schemas.EmbeddingStruct{
						EmbeddingStr: &embedding,
					},
				}
				bifrostEmbeddings = append(bifrostEmbeddings, bifrostEmbedding)
			}
		}
		// Note: Int8, Uint8, Binary, Ubinary types would need special handling
		// depending on how Bifrost wants to represent them

		bifrostResponse.Data = bifrostEmbeddings
	}

	// Convert usage information
	if response.Meta != nil {
		if response.Meta.Tokens != nil {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{}
			if response.Meta.Tokens.InputTokens != nil {
				bifrostResponse.Usage.PromptTokens = int(*response.Meta.Tokens.InputTokens)
			}
			if response.Meta.Tokens.OutputTokens != nil {
				bifrostResponse.Usage.CompletionTokens = int(*response.Meta.Tokens.OutputTokens)
			}
			bifrostResponse.Usage.TotalTokens = bifrostResponse.Usage.PromptTokens + bifrostResponse.Usage.CompletionTokens
		} else if response.Meta.BilledUnits != nil {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{}
			if response.Meta.BilledUnits.InputTokens != nil {
				bifrostResponse.Usage.PromptTokens = int(*response.Meta.BilledUnits.InputTokens)
			}
			if response.Meta.BilledUnits.OutputTokens != nil {
				bifrostResponse.Usage.CompletionTokens = int(*response.Meta.BilledUnits.OutputTokens)
			}
			bifrostResponse.Usage.TotalTokens = bifrostResponse.Usage.PromptTokens + bifrostResponse.Usage.CompletionTokens
		}
	}

	return bifrostResponse
}
