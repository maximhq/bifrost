package gemini

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// FromBifrostEmbeddingRequest converts a BifrostRequest with embedding input to Gemini's embedding request format
func ToGeminiEmbeddingRequest(bifrostReq *schemas.BifrostRequest) *GeminiEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input.EmbeddingInput == nil {
		return nil
	}

	embeddingInput := bifrostReq.Input.EmbeddingInput

	// Get the text to embed
	var text string
	if embeddingInput.Text != nil {
		text = *embeddingInput.Text
	} else if len(embeddingInput.Texts) > 0 {
		// Take the first text if multiple texts are provided
		text = embeddingInput.Texts[0]
	}

	if text == "" {
		return nil
	}

	// Create the Gemini embedding request
	request := &GeminiEmbeddingRequest{
		Model: bifrostReq.Model,
		Content: &CustomContent{
			Parts: []*CustomPart{
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
			if taskType, ok := bifrostReq.Params.ExtraParams["taskType"].(string); ok {
				request.TaskType = &taskType
			}
			if title, ok := bifrostReq.Params.ExtraParams["title"].(string); ok {
				request.Title = &title
			}
		}
	}

	return request
}
