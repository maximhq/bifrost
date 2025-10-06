package mistral

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ToMistralEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *MistralEmbeddingRequest {
	if bifrostReq == nil {
		return nil
	}

	var texts []string

	// Handle single Text input
	if bifrostReq.Input.Text != nil {
		// Treat empty string as nil/absent
		if *bifrostReq.Input.Text != "" {
			texts = []string{*bifrostReq.Input.Text}
		}
	}

	// Handle multiple Texts input (only if single Text wasn't valid)
	if len(texts) == 0 && bifrostReq.Input.Texts != nil {
		// Filter out empty strings from the slice
		for _, text := range bifrostReq.Input.Texts {
			if text != "" {
				texts = append(texts, text)
			}
		}
	}

	// Return nil immediately when no valid texts remain
	if len(texts) == 0 {
		return nil
	}

	mistralReq := &MistralEmbeddingRequest{
		Model: bifrostReq.Model,
		Input: texts,
	}

	// Map parameters
	if bifrostReq.Params != nil {
		mistralReq.OutputDtype = bifrostReq.Params.EncodingFormat
		mistralReq.OutputDimension = bifrostReq.Params.Dimensions
		if bifrostReq.Params.ExtraParams != nil {
			if user, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["user"]); ok {
				mistralReq.User = user
			}
		}
	}

	return mistralReq
}
