// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains converters for chat completion requests and responses.
package ollama

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOllamaChatRequest converts a Bifrost chat request to Ollama native format.
func ToOllamaChatRequest(bifrostReq *schemas.BifrostChatRequest) *OllamaChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	ollamaReq := &OllamaChatRequest{
		Model:    bifrostReq.Model,
		Messages: convertMessagesToOllama(bifrostReq.Input),
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		options := &OllamaOptions{}
		hasOptions := false

		// Map standard parameters
		if bifrostReq.Params.MaxCompletionTokens != nil {
			options.NumPredict = bifrostReq.Params.MaxCompletionTokens
			hasOptions = true
		}
		if bifrostReq.Params.Temperature != nil {
			options.Temperature = bifrostReq.Params.Temperature
			hasOptions = true
		}
		if bifrostReq.Params.TopP != nil {
			options.TopP = bifrostReq.Params.TopP
			hasOptions = true
		}
		if bifrostReq.Params.PresencePenalty != nil {
			options.PresencePenalty = bifrostReq.Params.PresencePenalty
			hasOptions = true
		}
		if bifrostReq.Params.FrequencyPenalty != nil {
			options.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
			hasOptions = true
		}
		if bifrostReq.Params.Stop != nil {
			options.Stop = bifrostReq.Params.Stop
			hasOptions = true
		}
		if bifrostReq.Params.Seed != nil {
			options.Seed = bifrostReq.Params.Seed
			hasOptions = true
		}

		// Handle extra parameters for Ollama-specific fields
		if bifrostReq.Params.ExtraParams != nil {
			// Top-k sampling
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				options.TopK = topK
				hasOptions = true
			}

			// Context window size
			if numCtx, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_ctx"]); ok {
				options.NumCtx = numCtx
				hasOptions = true
			}

			// Repeat penalty
			if repeatPenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["repeat_penalty"]); ok {
				options.RepeatPenalty = repeatPenalty
				hasOptions = true
			}

			// Repeat last N
			if repeatLastN, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["repeat_last_n"]); ok {
				options.RepeatLastN = repeatLastN
				hasOptions = true
			}

			// Mirostat sampling
			if mirostat, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["mirostat"]); ok {
				options.Mirostat = mirostat
				hasOptions = true
			}
			if mirostatEta, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["mirostat_eta"]); ok {
				options.MirostatEta = mirostatEta
				hasOptions = true
			}
			if mirostatTau, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["mirostat_tau"]); ok {
				options.MirostatTau = mirostatTau
				hasOptions = true
			}

			// TFS-Z sampling
			if tfsZ, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["tfs_z"]); ok {
				options.TfsZ = tfsZ
				hasOptions = true
			}

			// Typical-P sampling
			if typicalP, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["typical_p"]); ok {
				options.TypicalP = typicalP
				hasOptions = true
			}

			// Performance options
			if numBatch, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_batch"]); ok {
				options.NumBatch = numBatch
				hasOptions = true
			}
			if numGPU, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_gpu"]); ok {
				options.NumGPU = numGPU
				hasOptions = true
			}
			if numThread, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["num_thread"]); ok {
				options.NumThread = numThread
				hasOptions = true
			}

			// Keep-alive duration
			if keepAlive, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["keep_alive"]); ok {
				ollamaReq.KeepAlive = keepAlive
			}

			// Enable thinking mode (for thinking-specific models)
			if think, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["think"]); ok {
				ollamaReq.Think = think
			}
		}

		if hasOptions {
			ollamaReq.Options = options
		}

		// Handle response format (JSON mode)
		if bifrostReq.Params.ResponseFormat != nil {
			if rf, ok := (*bifrostReq.Params.ResponseFormat).(map[string]interface{}); ok {
				if t, exists := rf["type"]; exists && t == "json_object" {
					ollamaReq.Format = "json"
				} else if schema, exists := rf["json_schema"]; exists {
					// Pass JSON schema directly for structured output
					ollamaReq.Format = schema
				}
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			ollamaReq.Tools = convertToolsToOllama(bifrostReq.Params.Tools)
		}
	}

	return ollamaReq
}

// ToBifrostChatRequest converts an Ollama chat request to Bifrost format.
// This is used for passthrough/reverse conversion scenarios.
func (r *OllamaChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, schemas.Ollama)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    convertMessagesFromOllama(r.Messages),
	}

	// Convert options to parameters
	if r.Options != nil {
		params := &schemas.ChatParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if r.Options.NumPredict != nil {
			params.MaxCompletionTokens = r.Options.NumPredict
		}
		if r.Options.Temperature != nil {
			params.Temperature = r.Options.Temperature
		}
		if r.Options.TopP != nil {
			params.TopP = r.Options.TopP
		}
		if r.Options.Stop != nil {
			params.Stop = r.Options.Stop
		}
		if r.Options.PresencePenalty != nil {
			params.PresencePenalty = r.Options.PresencePenalty
		}
		if r.Options.FrequencyPenalty != nil {
			params.FrequencyPenalty = r.Options.FrequencyPenalty
		}
		if r.Options.Seed != nil {
			params.Seed = r.Options.Seed
		}

		// Map Ollama-specific parameters to ExtraParams
		if r.Options.TopK != nil {
			params.ExtraParams["top_k"] = *r.Options.TopK
		}
		if r.Options.NumCtx != nil {
			params.ExtraParams["num_ctx"] = *r.Options.NumCtx
		}
		if r.Options.RepeatPenalty != nil {
			params.ExtraParams["repeat_penalty"] = *r.Options.RepeatPenalty
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if r.Tools != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ChatParameters{}
		}
		bifrostReq.Params.Tools = convertToolsFromOllama(r.Tools)
	}

	return bifrostReq
}
