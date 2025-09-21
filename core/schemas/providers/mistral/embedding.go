package mistral

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ToMistralEmbeddingRequest(bifrostReq *schemas.BifrostRequest) *MistralEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input.EmbeddingInput == nil {
		return nil
	}

	texts := bifrostReq.Input.EmbeddingInput.Texts
	if len(texts) == 0 && bifrostReq.Input.EmbeddingInput.Text != nil {
		texts = []string{*bifrostReq.Input.EmbeddingInput.Text}
	}

	mistralReq := &MistralEmbeddingRequest{
		Model: bifrostReq.Model,
		Input: texts,
	}

	// Map parameters
	if bifrostReq.Params != nil {
		mistralReq.OutputDtype = bifrostReq.Params.EncodingFormat
		mistralReq.OutputDimension = bifrostReq.Params.Dimensions
		mistralReq.User = bifrostReq.Params.User
	}

	return mistralReq
}
