package mistral

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ConvertEmbeddingRequestToMistral(bifrostReq *schemas.BifrostRequest) *MistralEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input.EmbeddingInput == nil {
		return nil
	}

	mistralReq := &MistralEmbeddingRequest{
		Model: bifrostReq.Model,
		Input: bifrostReq.Input.EmbeddingInput.Texts,
	}

	// Map parameters
	if bifrostReq.Params != nil {
		mistralReq.OutputDtype = bifrostReq.Params.EncodingFormat
		mistralReq.OutputDimension = bifrostReq.Params.Dimensions
		mistralReq.User = bifrostReq.Params.User
	}

	return mistralReq
}