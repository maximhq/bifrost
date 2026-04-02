package bedrock

import (
	"encoding/json"
	"fmt"
	"strings"

	cohere "github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockTitanEmbeddingRequest converts a Bifrost embedding request to Bedrock Titan format.
func ToBedrockTitanEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockTitanEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	if len(bifrostReq.Input) == 0 {
		return nil, fmt.Errorf("no input provided for Titan embedding")
	}

	if len(bifrostReq.Input) != 1 {
		return nil, fmt.Errorf("amazon Titan embedding models support exactly one content item per request; got %d", len(bifrostReq.Input))
	}

	var sb strings.Builder
	for _, part := range bifrostReq.Input[0] {
		if part.Type != schemas.EmbeddingContentPartTypeText || part.Text == nil {
			return nil, fmt.Errorf("amazon Titan embedding models only support text input")
		}
		if sb.Len() > 0 {
			sb.WriteString(" \n")
		}
		sb.WriteString(*part.Text)
	}

	titanReq := &BedrockTitanEmbeddingRequest{
		InputText: sb.String(),
	}

	if bifrostReq.Params != nil {
		titanReq.Dimensions = bifrostReq.Params.Dimensions
		if normalize, ok := bifrostReq.Params.ExtraParams["normalize"]; ok {
			if b, ok := normalize.(bool); ok {
				titanReq.Normalize = &b
			}
		}
		if len(bifrostReq.Params.ExtraParams) > 0 {
			extra := make(map[string]interface{})
			for k, v := range bifrostReq.Params.ExtraParams {
				if k != "normalize" {
					extra[k] = v
				}
			}
			if len(extra) > 0 {
				titanReq.ExtraParams = extra
			}
		}
	}

	return titanReq, nil
}

// ToBifrostEmbeddingResponse converts a Bedrock Titan embedding response to Bifrost format
func (response *BedrockTitanEmbeddingResponse) ToBifrostEmbeddingResponse() *schemas.BifrostEmbeddingResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		Data: []schemas.EmbeddingData{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.EmbeddingsByType{
					Float: response.Embedding,
				},
			},
		},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens: response.InputTextTokenCount,
			TotalTokens:  response.InputTextTokenCount,
		},
	}

	return bifrostResponse
}

// ToBedrockCohereEmbeddingRequest converts a Bifrost embedding request to Bedrock Cohere format.
// Reuses the Cohere converter since the format is identical.
func ToBedrockCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*cohere.CohereEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	return cohere.ToCohereEmbeddingRequest(bifrostReq)
}

// DetermineEmbeddingModelType determines the embedding model type from the model name
func DetermineEmbeddingModelType(model string) (string, error) {
	switch {
	case strings.Contains(model, "amazon.titan-embed-text"):
		return "titan", nil
	case strings.Contains(model, "cohere.embed"):
		return "cohere", nil
	default:
		return "", fmt.Errorf("unsupported embedding model: %s", model)
	}
}

// ToBifrostEmbeddingResponse converts a BedrockCohereEmbeddingResponse to Bifrost format.
// Bedrock returns embeddings as a raw [][]float32 when response_type is "embeddings_floats"
// (the default, when no embedding_types are requested), and as a typed object when
// response_type is "embeddings_by_type".
func (r *BedrockCohereEmbeddingResponse) ToBifrostEmbeddingResponse() (*schemas.BifrostEmbeddingResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("nil Bedrock Cohere embedding response")
	}

	bifrostResponse := &schemas.BifrostEmbeddingResponse{Object: "list"}

	switch r.ResponseType {
	case "embeddings_by_type":
		// Object form: {"float": [[...]], "int8": [[...]], "uint8": [[...]], "binary": [[...]], "ubinary": [[...]], "base64": [...]}
		var typed struct {
			Float   [][]float32 `json:"float"`
			Base64  []string    `json:"base64"`
			Int8    [][]int8    `json:"int8"`
			Uint8   [][]int32   `json:"uint8"` // int32 avoids []byte→base64 JSON issue
			Binary  [][]int8    `json:"binary"`
			Ubinary [][]int32   `json:"ubinary"` // int32 avoids []byte→base64 JSON issue
		}
		if err := json.Unmarshal(r.Embeddings, &typed); err != nil {
			return nil, fmt.Errorf("error parsing embeddings_by_type: %w", err)
		}

		// Determine document count from whichever type was returned.
		count := max(len(typed.Float), len(typed.Base64), len(typed.Int8), len(typed.Uint8), len(typed.Binary), len(typed.Ubinary))
		for i := range count {
			entry := schemas.EmbeddingData{Object: "embedding", Index: i}
			if i < len(typed.Float) {
				entry.Embedding.Float = make([]float64, len(typed.Float[i]))
				for j, v := range typed.Float[i] {
					entry.Embedding.Float[j] = float64(v)
				}
			}
			if i < len(typed.Base64) {
				s := typed.Base64[i]
				entry.Embedding.Base64 = &s
			}
			if i < len(typed.Int8) {
				entry.Embedding.Int8 = typed.Int8[i]
			}
			if i < len(typed.Binary) {
				entry.Embedding.Binary = typed.Binary[i]
			}
			if i < len(typed.Uint8) {
				entry.Embedding.Uint8 = make([]uint8, len(typed.Uint8[i]))
				for j, v := range typed.Uint8[i] {
					entry.Embedding.Uint8[j] = uint8(v)
				}
			}
			if i < len(typed.Ubinary) {
				entry.Embedding.Ubinary = make([]uint8, len(typed.Ubinary[i]))
				for j, v := range typed.Ubinary[i] {
					entry.Embedding.Ubinary[j] = uint8(v)
				}
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
		}

	default:
		// Default / "embeddings_floats": raw array form [[...], [...]]
		var floats [][]float32
		if err := json.Unmarshal(r.Embeddings, &floats); err != nil {
			return nil, fmt.Errorf("error parsing embeddings_floats: %w", err)
		}
		for i, emb := range floats {
			float64Emb := make([]float64, len(emb))
			for j, v := range emb {
				float64Emb[j] = float64(v)
			}
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: schemas.EmbeddingsByType{Float: float64Emb},
			})
		}
	}

	return bifrostResponse, nil
}
