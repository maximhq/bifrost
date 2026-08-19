package ollama

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostEmbeddingRequest converts an OllamaEmbedRequest to a BifrostEmbeddingRequest.
func (req *OllamaEmbedRequest) ToBifrostEmbeddingRequest() (*schemas.BifrostEmbeddingRequest, error) {
	if req == nil {
		return nil, nil
	}

	provider, model := schemas.ParseModelString(req.Model, schemas.Ollama)

	var input schemas.EmbeddingInput
	if len(req.Input) == 1 {
		s := req.Input[0]
		input.Text = &s
	} else if len(req.Input) > 1 {
		input.Texts = []string(req.Input)
	}

	var params *schemas.EmbeddingParameters
	if req.Dimensions != nil || req.Truncate != nil || req.KeepAlive != "" {
		params = &schemas.EmbeddingParameters{}
		extra := make(map[string]any)
		if req.Dimensions != nil {
			extra["dimensions"] = *req.Dimensions
		}
		if req.Truncate != nil {
			extra["truncate"] = *req.Truncate
		}
		if req.KeepAlive != "" {
			extra["keep_alive"] = req.KeepAlive
		}
		if len(extra) > 0 {
			params.ExtraParams = extra
		}
	}

	return &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input:    &input,
		Params:   params,
	}, nil
}

// ToOllamaEmbedResponse converts a BifrostEmbeddingResponse to an OllamaEmbedResponse.
func ToOllamaEmbedResponse(resp *schemas.BifrostEmbeddingResponse) *OllamaEmbedResponse {
	embeddings := make([][]float64, 0, len(resp.Data))
	for _, d := range resp.Data {
		embeddings = append(embeddings, d.Embedding.EmbeddingArray)
	}
	return &OllamaEmbedResponse{
		Model:      resp.Model,
		Embeddings: embeddings,
	}
}
