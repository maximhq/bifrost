// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains converters for embedding requests and responses.
package ollama

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

func ToOllamaEmbeddingRequest(bifrostReq *schemas.BifrostEmbeddingRequest) *OllamaEmbeddingRequest {
	if bifrostReq == nil {
		return nil
	}

	ollamaReq := &OllamaEmbeddingRequest{
		Model: bifrostReq.Model,
	}

	// Handle input - Bifrost uses EmbeddingInput type
	if bifrostReq.Input != nil {
		if bifrostReq.Input.Text != nil {
			s := *bifrostReq.Input.Text
			ollamaReq.Input = OllamaEmbeddingInput{Text: &s}
		} else if bifrostReq.Input.Texts != nil {
			ollamaReq.Input = OllamaEmbeddingInput{Texts: bifrostReq.Input.Texts}
		}
	}

	// Handle extra parameters from Params
	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		// Truncate option
		if truncate, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["truncate"]); ok {
			ollamaReq.Truncate = truncate
		}

		// Keep-alive duration
		if keepAlive, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["keep_alive"]); ok {
			ollamaReq.KeepAlive = keepAlive
		}

		// Dimensions for embedding
		if dimensions, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["dimensions"]); ok {
			ollamaReq.Dimensions = dimensions
		}

		// Model options
		options := &OllamaOptions{}
		hasOptions := false

		if numCtx, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_ctx"]); ok {
			options.NumCtx = numCtx
			hasOptions = true
		}

		if hasOptions {
			ollamaReq.Options = options
		}
	}

	return ollamaReq
}

func (in *OllamaEmbeddingInput) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		in.Text = &s
		in.Texts = nil
		return nil
	}

	var ss []string
	if err := json.Unmarshal(b, &ss); err == nil {
		in.Text = nil
		in.Texts = ss
		return nil
	}

	return fmt.Errorf("ollama embedding input must be string or []string")
}

func (in OllamaEmbeddingInput) MarshalJSON() ([]byte, error) {
	if in.Text != nil {
		return json.Marshal(*in.Text)
	}
	return json.Marshal(in.Texts)
}

// ==================== RESPONSE CONVERTERS ====================

// ToBifrostEmbeddingResponse converts an Ollama embedding response to Bifrost format.
func (r *OllamaEmbeddingResponse) ToBifrostEmbeddingResponse(model string) *schemas.BifrostEmbeddingResponse {
	if r == nil {
		return nil
	}

	response := &schemas.BifrostEmbeddingResponse{
		Model:  model,
		Object: "list",
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.EmbeddingRequest,
			Provider:    schemas.Ollama,
		},
	}

	// Convert embeddings to Bifrost format
	for i, embedding := range r.Embeddings {
		response.Data = append(response.Data, schemas.EmbeddingData{
			Object: "embedding",
			Embedding: schemas.EmbeddingStruct{
				EmbeddingArray: embedding,
			},
			Index: i,
		})
	}

	// Convert usage
	if r.PromptEvalCount != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens: *r.PromptEvalCount,
			TotalTokens:  *r.PromptEvalCount,
		}
	}

	return response
}
