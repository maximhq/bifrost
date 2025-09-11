package mistral

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func ToMistralEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *MistralEmbeddingRequest {
	if bifrostReq == nil || (bifrostReq.Input.Text == nil && bifrostReq.Input.Texts == nil) {
		return nil
	}

	var texts []string
	if bifrostReq.Input.Text != nil {
		texts = []string{*bifrostReq.Input.Text}
	} else {
		texts = bifrostReq.Input.Texts
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
			if user, ok := bifrostReq.Params.ExtraParams["user"].(*string); ok {
				mistralReq.User = user
			}
		}
	}

	return mistralReq
}
