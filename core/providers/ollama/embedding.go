// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains converters for embedding requests and responses.
package ollama

import (
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
			ollamaReq.Input = *bifrostReq.Input.Text
		} else if bifrostReq.Input.Texts != nil {
			ollamaReq.Input = bifrostReq.Input.Texts
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

// ToBifrostEmbeddingRequest converts an Ollama embedding request to Bifrost format.
// This is used for passthrough/reverse conversion scenarios.
func (r *OllamaEmbeddingRequest) ToBifrostEmbeddingRequest() *schemas.BifrostEmbeddingRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.Ollama)

	bifrostReq := &schemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
	}

	// Convert input to EmbeddingInput
	if r.Input != nil {
		input := &schemas.EmbeddingInput{}
		converted := false
		switch v := r.Input.(type) {
		case string:
			input.Text = &v
			converted = true
		case []string:
			input.Texts = v
			converted = true
		case []interface{}:
			ss := make([]string, 0, len(v))
			for _, it := range v {
				s, ok := it.(string)
				if !ok {
					converted = false
					break
				}
				ss = append(ss, s)
			}
			if len(ss) > 0 {
				input.Texts = ss
				converted = true
			}
		}
		if converted {
			bifrostReq.Input = input
		}
	}

	// Map Ollama-specific options back to extra params
	if r.Truncate != nil || r.KeepAlive != nil || (r.Options != nil && r.Options.NumCtx != nil) {
		bifrostReq.Params = &schemas.EmbeddingParameters{
			ExtraParams: make(map[string]interface{}),
		}
		if r.Truncate != nil {
			bifrostReq.Params.ExtraParams["truncate"] = *r.Truncate
		}
		if r.KeepAlive != nil {
			bifrostReq.Params.ExtraParams["keep_alive"] = *r.KeepAlive
		}
		if r.Options != nil && r.Options.NumCtx != nil {
			bifrostReq.Params.ExtraParams["num_ctx"] = *r.Options.NumCtx
		}
	}

	return bifrostReq
}
