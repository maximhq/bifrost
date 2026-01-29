package huggingface

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToHuggingFaceEmbeddingRequest converts a Bifrost embedding request to HuggingFace format
func ToHuggingFaceEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) (*HuggingFaceEmbeddingRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(bifrostReq.Model)
	if nameErr != nil {
		return nil, nameErr
	}

	var hfReq *HuggingFaceEmbeddingRequest
	if inferenceProvider != hfInference {
		hfReq = &HuggingFaceEmbeddingRequest{
			Model:    schemas.Ptr(modelName),
			Provider: schemas.Ptr(string(inferenceProvider)),
		}
	} else {
		hfReq = &HuggingFaceEmbeddingRequest{}
	}

	// Convert input
	if bifrostReq.Input != nil {
		var input InputsCustomType
		if bifrostReq.Input.Text != nil {
			input = InputsCustomType{Text: bifrostReq.Input.Text}
		} else if bifrostReq.Input.Texts != nil {
			input = InputsCustomType{Texts: bifrostReq.Input.Texts}
		}
		if inferenceProvider == hfInference {
			hfReq.Inputs = &input
		} else {
			hfReq.Input = &input
		}
	}

	// Map parameters
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		// Map standard parameters
		if params.EncodingFormat != nil {
			encodingType := EncodingType(*params.EncodingFormat)
			hfReq.EncodingFormat = &encodingType
		}
		if params.Dimensions != nil {
			hfReq.Dimensions = params.Dimensions
		}

		// Check for HuggingFace-specific parameters in ExtraParams
		if params.ExtraParams != nil {
			if normalize, ok := params.ExtraParams["normalize"].(bool); ok {
				hfReq.Normalize = &normalize
			}
			if promptName, ok := params.ExtraParams["prompt_name"].(string); ok {
				hfReq.PromptName = &promptName
			}
			if truncate, ok := params.ExtraParams["truncate"].(bool); ok {
				hfReq.Truncate = &truncate
			}
			if truncationDirection, ok := params.ExtraParams["truncation_direction"].(string); ok {
				hfReq.TruncationDirection = &truncationDirection
			}
		}
	}

	return hfReq, nil
}

// UnmarshalHuggingFaceEmbeddingResponse unmarshals HuggingFace API response directly into BifrostEmbeddingResponse
// Handles multiple formats: standard object, 2D array, or 1D array
func UnmarshalHuggingFaceEmbeddingResponse(data []byte, model string) (*schemas.BifrostEmbeddingResponse, error) {
	if data == nil {
		return nil, fmt.Errorf("response data is nil")
	}

	// Try standard object format first
	type tempResponse struct {
		Data  []schemas.EmbeddingData  `json:"data,omitempty"`
		Model *string                  `json:"model,omitempty"`
		Usage *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	}
	var obj tempResponse
	if err := sonic.Unmarshal(data, &obj); err == nil {
		if obj.Data != nil || obj.Model != nil || obj.Usage != nil {
			bifrostResponse := &schemas.BifrostEmbeddingResponse{
				Data:   obj.Data,
				Model:  model,
				Object: "list",
			}
			if obj.Model != nil {
				bifrostResponse.Model = *obj.Model
			}
			if obj.Usage != nil {
				bifrostResponse.Usage = obj.Usage
			} else {
				bifrostResponse.Usage = &schemas.BifrostLLMUsage{
					PromptTokens:     0,
					CompletionTokens: 0,
					TotalTokens:      0,
				}
			}
			return bifrostResponse, nil
		}
	}

	// Try 2D array: [[num, ...], ...]
	var arr2D [][]float64
	if err := sonic.Unmarshal(data, &arr2D); err == nil {
		embeddings := make([]schemas.EmbeddingData, len(arr2D))
		for idx, embedding := range arr2D {
			conv := make([]float32, len(embedding))
			for i, v := range embedding {
				conv[i] = float32(v)
			}
			embeddings[idx] = schemas.EmbeddingData{
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: conv},
				Index:     idx,
				Object:    "embedding",
			}
		}
		return &schemas.BifrostEmbeddingResponse{
			Data:   embeddings,
			Model:  model,
			Object: "list",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     0,
				CompletionTokens: 0,
				TotalTokens:      0,
			},
		}, nil
	}

	// Try 1D array: [num, ...]
	var arr1D []float64
	if err := sonic.Unmarshal(data, &arr1D); err == nil {
		conv := make([]float32, len(arr1D))
		for i, v := range arr1D {
			conv[i] = float32(v)
		}
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{{
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: conv},
				Index:     0,
				Object:    "embedding",
			}},
			Model:  model,
			Object: "list",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     0,
				CompletionTokens: 0,
				TotalTokens:      0,
			},
		}, nil
	}

	return nil, fmt.Errorf("failed to unmarshal HuggingFace embedding response: unexpected structure")
}
