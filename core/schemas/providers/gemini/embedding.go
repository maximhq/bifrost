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
