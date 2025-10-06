package cohere

import "github.com/maximhq/bifrost/core/schemas"

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

// ToBifrostResponse converts a Cohere embedding response to Bifrost format
func (cohereResp *CohereEmbeddingResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if cohereResp == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostResponse{
		ID:     cohereResp.ID,
		Object: "list",
	}

	// Convert embeddings data
	if cohereResp.Embeddings != nil {
		var bifrostEmbeddings []schemas.BifrostEmbedding

		// Handle different embedding types - prioritize float embeddings
		if cohereResp.Embeddings.Float != nil {
			for i, embedding := range cohereResp.Embeddings.Float {
				bifrostEmbedding := schemas.BifrostEmbedding{
					Object: "embedding",
					Index:  i,
					Embedding: schemas.BifrostEmbeddingResponse{
						EmbeddingArray: embedding,
					},
				}
				bifrostEmbeddings = append(bifrostEmbeddings, bifrostEmbedding)
			}
		} else if cohereResp.Embeddings.Base64 != nil {
			// Handle base64 embeddings as strings
			for i, embedding := range cohereResp.Embeddings.Base64 {
				bifrostEmbedding := schemas.BifrostEmbedding{
					Object: "embedding",
					Index:  i,
					Embedding: schemas.BifrostEmbeddingResponse{
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
	if cohereResp.Meta != nil {
		if cohereResp.Meta.Tokens != nil {
			usage := &schemas.LLMUsage{}
			if cohereResp.Meta.Tokens.InputTokens != nil {
				usage.PromptTokens = int(*cohereResp.Meta.Tokens.InputTokens)
			}
			if cohereResp.Meta.Tokens.OutputTokens != nil {
				usage.CompletionTokens = int(*cohereResp.Meta.Tokens.OutputTokens)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			bifrostResponse.Usage = usage
		}

		// Convert billed usage
		if cohereResp.Meta.BilledUnits != nil {
			if bifrostResponse.ExtraFields.BilledUsage == nil {
				bifrostResponse.ExtraFields.BilledUsage = &schemas.BilledLLMUsage{}
			}
			if cohereResp.Meta.BilledUnits.InputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.PromptTokens = cohereResp.Meta.BilledUnits.InputTokens
			}
			if cohereResp.Meta.BilledUnits.OutputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.CompletionTokens = cohereResp.Meta.BilledUnits.OutputTokens
			}
		}
	}

	return bifrostResponse
}
