package gemini

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToGeminiEmbeddingRequest converts a BifrostRequest with embedding input to Gemini's embedding request format
func ToGeminiEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *GeminiEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input == nil || (bifrostReq.Input.Text == nil && bifrostReq.Input.Texts == nil) {
		return nil
	}
	embeddingInput := bifrostReq.Input
	// Get the text to embed
	var text string
	if embeddingInput.Text != nil {
		text = *embeddingInput.Text
	} else if len(embeddingInput.Texts) > 0 {
		// Take the first text if multiple texts are provided
		text = strings.Join(embeddingInput.Texts, " ")
	}
	if text == "" {
		return nil
	}
	// Create the Gemini embedding request
	request := &GeminiEmbeddingRequest{
		Model: bifrostReq.Model,
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
			request.OutputDimensionality = bifrostReq.Params.Dimensions
		}

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			if taskType, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["taskType"]); ok {
				request.TaskType = taskType
			}
			if title, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["title"]); ok {
				request.Title = title
			}
		}
	}
	return request
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

// ToBifrostEmbeddingRequest converts a GeminiGenerationRequest to BifrostEmbeddingRequest format
func (request *GeminiGenerationRequest) ToBifrostEmbeddingRequest() *schemas.BifrostEmbeddingRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, schemas.Gemini)

	// Create the embedding request
	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}

	if len(request.Requests) > 0 {
		embeddingRequest := request.Requests[0]
		if embeddingRequest.Content != nil {
			var texts []string
			for _, part := range embeddingRequest.Content.Parts {
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
