package bedrock

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockTitanEmbeddingRequest converts a Bifrost embedding request to Bedrock Titan format
func ToBedrockTitanEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockTitanEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}

	// Validate that only single text input is provided for Titan models
	if bifrostReq.Input.Text == nil && len(bifrostReq.Input.Texts) == 0 {
		return nil, fmt.Errorf("no input text provided for embedding")
	}

	titanReq := &BedrockTitanEmbeddingRequest{}

	// Set input text
	if bifrostReq.Input.Text != nil {
		titanReq.InputText = *bifrostReq.Input.Text
	} else if len(bifrostReq.Input.Texts) > 0 {
		var embeddingText string
		for _, text := range bifrostReq.Input.Texts {
			embeddingText += text + " \n"
		}
		titanReq.InputText = embeddingText
	}

	if bifrostReq.Params != nil {
		titanReq.Dimensions = bifrostReq.Params.Dimensions
		if normalize, ok := bifrostReq.Params.ExtraParams["normalize"]; ok {
			if b, ok := normalize.(bool); ok {
				titanReq.Normalize = &b
			}
		}
		// Forward remaining extra params (excluding normalize which is now a first-class field)
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
				Embedding: schemas.EmbeddingStruct{
					EmbeddingArray: response.Embedding,
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
// Unlike the direct Cohere API, Bedrock does not accept a "model" field in the request body.
func ToBedrockCohereEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*BedrockCohereEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost embedding request is nil")
	}
	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("no input provided for embedding")
	}

	req := &BedrockCohereEmbeddingRequest{}

	// Map texts
	if bifrostReq.Input.Text != nil {
		req.Texts = []string{*bifrostReq.Input.Text}
	} else if len(bifrostReq.Input.Texts) > 0 {
		req.Texts = bifrostReq.Input.Texts
	}

	if bifrostReq.Params != nil {
		extra := make(map[string]interface{}, len(bifrostReq.Params.ExtraParams))
		for k, v := range bifrostReq.Params.ExtraParams {
			extra[k] = v
		}

		if v, ok := extra["input_type"]; ok {
			if s, ok := v.(string); ok {
				req.InputType = s
				delete(extra, "input_type")
			}
		}
		if v, ok := extra["truncate"]; ok {
			if s, ok := v.(string); ok {
				req.Truncate = &s
				delete(extra, "truncate")
			}
		}
		if v, ok := extra["embedding_types"]; ok {
			if ss, ok := v.([]string); ok {
				req.EmbeddingTypes = ss
				delete(extra, "embedding_types")
			}
		}
		if v, ok := extra["images"]; ok {
			if ss, ok := v.([]string); ok {
				req.Images = ss
				delete(extra, "images")
			}
		}
		if v, ok := extra["inputs"]; ok {
			if inputs, ok := v.([]BedrockCohereEmbeddingInput); ok {
				req.Inputs = inputs
				delete(extra, "inputs")
			}
		}
		if v, ok := extra["max_tokens"]; ok {
			switch n := v.(type) {
			case int:
				req.MaxTokens = &n
				delete(extra, "max_tokens")
			case float64:
				i := int(n)
				req.MaxTokens = &i
				delete(extra, "max_tokens")
			}
		}
		if bifrostReq.Params.Dimensions != nil {
			req.OutputDimension = bifrostReq.Params.Dimensions
		}
		if len(extra) > 0 {
			req.ExtraParams = extra
		}
	}

	return req, nil
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
			Uint8   [][]int32   `json:"uint8"`  // int32 avoids []byte→base64 JSON issue
			Binary  [][]int8    `json:"binary"`
			Ubinary [][]int32   `json:"ubinary"` // int32 avoids []byte→base64 JSON issue
		}
		if err := json.Unmarshal(r.Embeddings, &typed); err != nil {
			return nil, fmt.Errorf("error parsing embeddings_by_type: %w", err)
		}
		if typed.Float != nil {
			for i, emb := range typed.Float {
				float64Emb := make([]float64, len(emb))
				for j, v := range emb {
					float64Emb[j] = float64(v)
				}
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
					Object:    "embedding",
					Index:     i,
					Embedding: schemas.EmbeddingStruct{EmbeddingArray: float64Emb},
				})
			}
		}
		if typed.Base64 != nil {
			for i, emb := range typed.Base64 {
				e := emb
				bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
					Object:    "embedding",
					Index:     i,
					Embedding: schemas.EmbeddingStruct{EmbeddingStr: &e},
				})
			}
		}
		for i, emb := range typed.Int8 {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: schemas.EmbeddingStruct{EmbeddingInt8Array: emb},
			})
		}
		for i, emb := range typed.Binary {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: schemas.EmbeddingStruct{EmbeddingInt8Array: emb},
			})
		}
		for i, emb := range typed.Uint8 {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: schemas.EmbeddingStruct{EmbeddingInt32Array: emb},
			})
		}
		for i, emb := range typed.Ubinary {
			bifrostResponse.Data = append(bifrostResponse.Data, schemas.EmbeddingData{
				Object:    "embedding",
				Index:     i,
				Embedding: schemas.EmbeddingStruct{EmbeddingInt32Array: emb},
			})
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
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: float64Emb},
			})
		}
	}

	return bifrostResponse, nil
}
