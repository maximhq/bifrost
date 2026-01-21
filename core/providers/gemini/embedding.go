package gemini

import (
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToGeminiEmbeddingRequest converts a BifrostRequest with embedding input to Gemini's batch embedding request format
// GeminiGenerationRequest contains requests array for batch embed content endpoint
func ToGeminiEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *GeminiBatchEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || (bifrostReq.Input.Text == nil && bifrostReq.Input.Texts == nil) {
		return nil
	}

	embeddingInput := bifrostReq.Input

	// Collect all texts to embed
	var texts []string
	if embeddingInput.Text != nil {
		texts = append(texts, *embeddingInput.Text)
	}
	if len(embeddingInput.Texts) > 0 {
		texts = append(texts, embeddingInput.Texts...)
	}

	if len(texts) == 0 {
		return nil
	}

	// Create batch embedding request with one request per text
	batchRequest := &GeminiBatchEmbeddingRequest{
		Requests: make([]GeminiEmbeddingRequest, len(texts)),
	}

	// Create individual embedding requests for each text
	for i, text := range texts {
		embeddingReq := GeminiEmbeddingRequest{
			Model: "models/" + bifrostReq.Model,
			Content: &Content{
				Parts: []*Part{
					{
						Text: text,
					},
				},
			},
		}

		// Add parameters if available
		if bifrostReq.Params != nil {
			if bifrostReq.Params.Dimensions != nil {
				embeddingReq.OutputDimensionality = bifrostReq.Params.Dimensions
			}

			// Handle extra parameters
			if bifrostReq.Params.ExtraParams != nil {
				if taskType, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["taskType"]); ok {
					embeddingReq.TaskType = taskType
				}
				if title, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["title"]); ok {
					embeddingReq.Title = title
				}
			}
		}

		batchRequest.Requests[i] = embeddingReq
	}

	return batchRequest
}

// ToGeminiEmbeddingResponse converts a BifrostResponse with embedding data to Gemini's embedding response format
func ToGeminiEmbeddingResponse(bifrostResp *schemas.BifrostEmbeddingResponse) *GeminiEmbeddingResponse {
	if bifrostResp == nil || len(bifrostResp.Data) == 0 {
		return nil
	}

	geminiResp := &GeminiEmbeddingResponse{
		Embeddings: make([]GeminiEmbedding, len(bifrostResp.Data)),
	}

	// Convert each embedding from Bifrost format to Gemini format
	for i, embedding := range bifrostResp.Data {
		var values []float32

		// Extract embedding values from BifrostEmbeddingResponse
		if embedding.Embedding.EmbeddingArray != nil {
			values = embedding.Embedding.EmbeddingArray
		} else if len(embedding.Embedding.Embedding2DArray) > 0 {
			// If it's a 2D array, take the first array
			values = embedding.Embedding.Embedding2DArray[0]
		}

		geminiEmbedding := GeminiEmbedding{
			Values: values,
		}

		// Add statistics if available (token count from usage metadata)
		if bifrostResp.Usage != nil {
			geminiEmbedding.Statistics = &ContentEmbeddingStatistics{
				TokenCount: int32(bifrostResp.Usage.PromptTokens),
			}
		}

		geminiResp.Embeddings[i] = geminiEmbedding
	}

	// Set metadata if available (for Vertex API compatibility)
	if bifrostResp.Usage != nil {
		geminiResp.Metadata = &EmbedContentMetadata{
			BillableCharacterCount: int32(bifrostResp.Usage.PromptTokens),
		}
	}

	return geminiResp
}

// ToBifrostEmbeddingResponse converts a Gemini embedding response to BifrostEmbeddingResponse format
func ToBifrostEmbeddingResponse(geminiResp *GeminiEmbeddingResponse, model string) *schemas.BifrostEmbeddingResponse {
	if geminiResp == nil || len(geminiResp.Embeddings) == 0 {
		return nil
	}

	bifrostResp := &schemas.BifrostEmbeddingResponse{
		Data:   make([]schemas.EmbeddingData, len(geminiResp.Embeddings)),
		Model:  model,
		Object: "list",
	}

	// Convert each embedding from Gemini format to Bifrost format
	for i, geminiEmbedding := range geminiResp.Embeddings {
		embeddingData := schemas.EmbeddingData{
			Index:  i,
			Object: "embedding",
			Embedding: schemas.EmbeddingStruct{
				EmbeddingArray: geminiEmbedding.Values,
			},
		}

		bifrostResp.Data[i] = embeddingData
	}

	// Convert usage metadata if available
	if geminiResp.Metadata != nil || (len(geminiResp.Embeddings) > 0 && geminiResp.Embeddings[0].Statistics != nil) {
		bifrostResp.Usage = &schemas.BifrostLLMUsage{}

		// Use statistics from the first embedding if available
		if geminiResp.Embeddings[0].Statistics != nil {
			bifrostResp.Usage.PromptTokens = int(geminiResp.Embeddings[0].Statistics.TokenCount)
		} else if geminiResp.Metadata != nil {
			// Fall back to metadata if statistics are not available
			bifrostResp.Usage.PromptTokens = int(geminiResp.Metadata.BillableCharacterCount)
		}

		// Set total tokens same as prompt tokens for embeddings
		bifrostResp.Usage.TotalTokens = bifrostResp.Usage.PromptTokens
	}

	return bifrostResp
}

// ToBifrostEmbeddingRequest converts a GeminiGenerationRequest to BifrostEmbeddingRequest format
func (request *GeminiGenerationRequest) ToBifrostEmbeddingRequest(ctx *schemas.BifrostContext) *schemas.BifrostEmbeddingRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	// Create the embedding request
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}

	// sdk request contains multiple embedding requests with same parameters but different text fields
	if len(request.Requests) > 0 {
		var texts []string
		for _, req := range request.Requests {
			if req.Content != nil && len(req.Content.Parts) > 0 {
				for _, part := range req.Content.Parts {
					if part != nil && part.Text != "" {
						texts = append(texts, part.Text)
					}
				}
				if len(texts) > 0 {
					bifrostReq.Input = &schemas.EmbeddingInput{}
					if len(texts) == 1 {
						bifrostReq.Input.Text = &texts[0]
					} else {
						bifrostReq.Input.Texts = texts
					}
				}
			}
		}

		embeddingRequest := request.Requests[0]

		// Convert parameters
		if embeddingRequest.OutputDimensionality != nil || embeddingRequest.TaskType != nil || embeddingRequest.Title != nil {
			bifrostReq.Params = &schemas.EmbeddingParameters{}

			if embeddingRequest.OutputDimensionality != nil {
				bifrostReq.Params.Dimensions = embeddingRequest.OutputDimensionality
			}

			// Handle extra parameters
			if embeddingRequest.TaskType != nil || embeddingRequest.Title != nil {
				bifrostReq.Params.ExtraParams = make(map[string]interface{})
				if embeddingRequest.TaskType != nil {
					bifrostReq.Params.ExtraParams["taskType"] = embeddingRequest.TaskType
				}
				if embeddingRequest.Title != nil {
					bifrostReq.Params.ExtraParams["title"] = embeddingRequest.Title
				}
			}
		}
	}

	return bifrostReq
}
