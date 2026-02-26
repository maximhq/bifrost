package bifrost

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// PUBLIC API METHODS

// ListModelsRequest sends a list models request to the specified provider.
func (bifrost *Bifrost) ListModelsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "list models request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ListModelsRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for list models request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ListModelsRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ListModelsRequest
	bifrostReq.ListModelsRequest = req

	resp, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}

	return resp.ListModelsResponse, nil
}

// ListAllModels lists all models from all configured providers.
// It accumulates responses from all providers with a limit of 1000 per provider to get all results.
func (bifrost *Bifrost) ListAllModels(ctx *schemas.BifrostContext, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if request == nil {
		request = &schemas.BifrostListModelsRequest{}
	}

	providerKeys, err := bifrost.GetConfiguredProviders()
	if err != nil {
		bfErr := schemas.AcquireBifrostError()
		bfErr.IsBifrostError = false
		bfErr.Error.Message = err.Error()
		if bfErr.ExtraFields == nil {
			bfErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		bfErr.ExtraFields.RequestType = schemas.ListModelsRequest
		return nil, bfErr
	}

	startTime := time.Now()

	// Result structure for collecting provider responses
	type providerResult struct {
		provider    schemas.ModelProvider
		models      []schemas.Model
		keyStatuses []schemas.KeyStatus
		err         *schemas.BifrostError
	}

	results := make(chan providerResult, len(providerKeys))
	var wg sync.WaitGroup

	// Launch concurrent requests for all providers
	for _, providerKey := range providerKeys {
		if strings.TrimSpace(string(providerKey)) == "" {
			continue
		}

		wg.Add(1)
		go func(providerKey schemas.ModelProvider) {
			defer wg.Done()

			providerModels := make([]schemas.Model, 0)
			var providerKeyStatuses []schemas.KeyStatus
			var providerErr *schemas.BifrostError

			// Create request for this provider with limit of 1000
			providerRequest := &schemas.BifrostListModelsRequest{
				Provider: providerKey,
				PageSize: schemas.DefaultPageSize,
			}

			iterations := 0
			for {
				// check for context cancellation
				select {
				case <-ctx.Done():
					bifrost.logger.Warn("context cancelled for provider %s", providerKey)
					return
				default:
				}

				iterations++
				if iterations > schemas.MaxPaginationRequests {
					bifrost.logger.Warn("reached maximum pagination requests (%d) for provider %s, please increase the page size", schemas.MaxPaginationRequests, providerKey)
					break
				}

				response, bifrostErr := bifrost.ListModelsRequest(ctx, providerRequest)
				if bifrostErr != nil {
					// Skip logging "no keys found" and "not supported" errors as they are expected when a provider is not configured
					if !strings.Contains(bifrostErr.Error.Message, "no keys found") &&
						!strings.Contains(bifrostErr.Error.Message, "not supported") {
						providerErr = bifrostErr
						bifrost.logger.Warn("failed to list models for provider %s: %s", providerKey, GetErrorMessage(bifrostErr))
					}
					// Collect key statuses from error (failure case)
					if len(bifrostErr.ExtraFields.KeyStatuses) > 0 {
						providerKeyStatuses = append(providerKeyStatuses, bifrostErr.ExtraFields.KeyStatuses...)
					}
					break
				}

				if response == nil || len(response.Data) == 0 {
					break
				}

				providerModels = append(providerModels, response.Data...)

				if len(response.KeyStatuses) > 0 {
					providerKeyStatuses = append(providerKeyStatuses, response.KeyStatuses...)
				}

				// Check if there are more pages
				if response.NextPageToken == "" {
					break
				}

				// Set the page token for the next request
				providerRequest.PageToken = response.NextPageToken
			}

			results <- providerResult{
				provider:    providerKey,
				models:      providerModels,
				keyStatuses: providerKeyStatuses,
				err:         providerErr,
			}
		}(providerKey)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(results)

	// Accumulate all models and key statuses from all providers
	allModels := make([]schemas.Model, 0)
	allKeyStatuses := make([]schemas.KeyStatus, 0)
	var firstError *schemas.BifrostError

	for result := range results {
		if len(result.models) > 0 {
			allModels = append(allModels, result.models...)
		}
		if len(result.keyStatuses) > 0 {
			allKeyStatuses = append(allKeyStatuses, result.keyStatuses...)
		}
		if result.err != nil && firstError == nil {
			firstError = result.err
		}
	}

	// If we couldn't get any models from any provider, return the first error
	if len(allModels) == 0 && firstError != nil {
		// Attach all key statuses to the error
		if firstError.ExtraFields == nil {
			firstError.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		firstError.ExtraFields.KeyStatuses = allKeyStatuses
		return nil, firstError
	}

	// Sort models alphabetically by ID
	sort.Slice(allModels, func(i, j int) bool {
		return allModels[i].ID < allModels[j].ID
	})

	// Return aggregated response with accumulated latency and key statuses
	response := &schemas.BifrostListModelsResponse{
		Data:        allModels,
		KeyStatuses: allKeyStatuses,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ListModelsRequest,
			Latency:     time.Since(startTime).Milliseconds(),
		},
	}

	response = response.ApplyPagination(request.PageSize, request.PageToken)

	return response, nil
}

// TextCompletionRequest sends a text completion request to the specified provider.
func (bifrost *Bifrost) TextCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "text completion request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TextCompletionRequest
		return nil, err
	}
	if req.Input == nil || (req.Input.PromptStr == nil && req.Input.PromptArray == nil) {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "prompt not provided for text completion request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TextCompletionRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	// Preparing request
	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.TextCompletionRequest
	bifrostReq.TextCompletionRequest = req
	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.TextCompletionResponse, nil
}

// TextCompletionStreamRequest sends a streaming text completion request to the specified provider.
func (bifrost *Bifrost) TextCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "text completion stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TextCompletionStreamRequest
		return nil, err
	}
	if req.Input == nil || (req.Input.PromptStr == nil && req.Input.PromptArray == nil) {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "prompt not provided for text completion stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TextCompletionStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.TextCompletionStreamRequest
	bifrostReq.TextCompletionRequest = req
	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

func (bifrost *Bifrost) makeChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "chat completion request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ChatCompletionRequest
		return nil, err
	}
	if req.Input == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "chats not provided for chat completion request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ChatCompletionRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	// Acquire bifrost request
	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ChatCompletionRequest
	bifrostReq.ChatRequest = req
	// Handling request
	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ChatResponse, nil
}

// ChatCompletionRequest sends a chat completion request to the specified provider.
func (bifrost *Bifrost) ChatCompletionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// If ctx is nil, use the bifrost context (defensive check for mcp agent mode)
	if ctx == nil {
		ctx = bifrost.ctx
	}

	response, err := bifrost.makeChatCompletionRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// Check if we should enter agent mode
	if bifrost.MCPManager != nil {
		return bifrost.MCPManager.CheckAndExecuteAgentForChatRequest(
			ctx,
			req,
			response,
			bifrost.makeChatCompletionRequest,
			bifrost.executeMCPToolWithHooks,
		)
	}

	return response, nil
}

// ChatCompletionStreamRequest sends a chat completion stream request to the specified provider.
func (bifrost *Bifrost) ChatCompletionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "chat completion stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
		return nil, err
	}
	if req.Input == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "chats not provided for chat completion stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ChatCompletionStreamRequest
	bifrostReq.ChatRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

func (bifrost *Bifrost) makeResponsesRequest(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "responses request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ResponsesRequest
		return nil, err
	}
	if req.Input == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "responses not provided for responses request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ResponsesRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ResponsesRequest
	bifrostReq.ResponsesRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ResponsesResponse, nil
}

// ResponsesRequest sends a responses request to the specified provider.
func (bifrost *Bifrost) ResponsesRequest(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// If ctx is nil, use the bifrost context (defensive check for mcp agent mode)
	if ctx == nil {
		ctx = bifrost.ctx
	}

	response, err := bifrost.makeResponsesRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// Check if we should enter agent mode
	if bifrost.MCPManager != nil {
		return bifrost.MCPManager.CheckAndExecuteAgentForResponsesRequest(
			ctx,
			req,
			response,
			bifrost.makeResponsesRequest,
			bifrost.executeMCPToolWithHooks,
		)
	}

	return response, nil
}

// ResponsesStreamRequest sends a responses stream request to the specified provider.
func (bifrost *Bifrost) ResponsesStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "responses stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ResponsesStreamRequest
		return nil, err
	}
	if req.Input == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "responses not provided for responses stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ResponsesStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ResponsesStreamRequest
	bifrostReq.ResponsesRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// CountTokensRequest sends a count tokens request to the specified provider.
func (bifrost *Bifrost) CountTokensRequest(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "count tokens request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.CountTokensRequest
		return nil, err
	}
	if req.Input == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "input not provided for count tokens request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.CountTokensRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.CountTokensRequest
	bifrostReq.CountTokensRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}

	return response.CountTokensResponse, nil
}

// EmbeddingRequest sends an embedding request to the specified provider.
func (bifrost *Bifrost) EmbeddingRequest(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "embedding request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.EmbeddingRequest
		return nil, err
	}
	if req.Input == nil || (req.Input.Text == nil && req.Input.Texts == nil && req.Input.Embedding == nil && req.Input.Embeddings == nil) {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "embedding input not provided for embedding request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.EmbeddingRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.EmbeddingRequest
	bifrostReq.EmbeddingRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.EmbeddingResponse, nil
}

// SpeechRequest sends a speech request to the specified provider.
func (bifrost *Bifrost) SpeechRequest(ctx *schemas.BifrostContext, req *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "speech request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.SpeechRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Input == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "speech input not provided for speech request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.SpeechRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.SpeechRequest
	bifrostReq.SpeechRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.SpeechResponse, nil
}

// SpeechStreamRequest sends a speech stream request to the specified provider.
func (bifrost *Bifrost) SpeechStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "speech stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.SpeechStreamRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Input == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "speech input not provided for speech stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.SpeechStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.SpeechStreamRequest
	bifrostReq.SpeechRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// TranscriptionRequest sends a transcription request to the specified provider.
func (bifrost *Bifrost) TranscriptionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "transcription request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TranscriptionRequest
		return nil, err
	}
	if req.Input == nil || req.Input.File == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "transcription input not provided for transcription request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TranscriptionRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.TranscriptionRequest
	bifrostReq.TranscriptionRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	//TODO: Release the response
	return response.TranscriptionResponse, nil
}

// TranscriptionStreamRequest sends a transcription stream request to the specified provider.
func (bifrost *Bifrost) TranscriptionStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "transcription stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TranscriptionStreamRequest
		return nil, err
	}
	if req.Input == nil || req.Input.File == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "transcription input not provided for transcription stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.TranscriptionStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.TranscriptionStreamRequest
	bifrostReq.TranscriptionRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// ImageGenerationRequest sends an image generation request to the specified provider.
func (bifrost *Bifrost) ImageGenerationRequest(ctx *schemas.BifrostContext,
	req *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "image generation request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageGenerationRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Prompt == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "prompt not provided for image generation request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageGenerationRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ImageGenerationRequest
	bifrostReq.ImageGenerationRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	if response == nil || response.ImageGenerationResponse == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "received nil response from provider"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageGenerationRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	return response.ImageGenerationResponse, nil
}

// ImageGenerationStreamRequest sends an image generation stream request to the specified provider.
func (bifrost *Bifrost) ImageGenerationStreamRequest(ctx *schemas.BifrostContext,
	req *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "image generation stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageGenerationStreamRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Prompt == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "prompt not provided for image generation stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageGenerationStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ImageGenerationStreamRequest
	bifrostReq.ImageGenerationRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// ImageEditRequest sends an image edit request to the specified provider.
func (bifrost *Bifrost) ImageEditRequest(ctx *schemas.BifrostContext, req *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "image edit request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageEditRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Images == nil || len(req.Input.Images) == 0 {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "images not provided for image edit request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageEditRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	// Prompt is not required when type is background_removal
	if req.Params == nil || req.Params.Type == nil || *req.Params.Type != "background_removal" {
		if req.Input.Prompt == "" {
			err := schemas.AcquireBifrostError()
			err.IsBifrostError = false
			err.Error.Message = "prompt not provided for image edit request"
			if err.ExtraFields == nil {
				err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			err.ExtraFields.RequestType = schemas.ImageEditRequest
			err.ExtraFields.Provider = req.Provider
			err.ExtraFields.ModelRequested = req.Model
			return nil, err
		}
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ImageEditRequest
	bifrostReq.ImageEditRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}

	if response == nil || response.ImageGenerationResponse == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "received nil response from provider"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageEditRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	return response.ImageGenerationResponse, nil
}

// ImageEditStreamRequest sends an image edit stream request to the specified provider.
func (bifrost *Bifrost) ImageEditStreamRequest(ctx *schemas.BifrostContext, req *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "image edit stream request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageEditStreamRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Images == nil || len(req.Input.Images) == 0 {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "images not provided for image edit stream request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageEditStreamRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	// Prompt is not required when type is background_removal
	if req.Params == nil || req.Params.Type == nil || *req.Params.Type != "background_removal" {
		if req.Input.Prompt == "" {
			err := schemas.AcquireBifrostError()
			err.IsBifrostError = false
			err.Error.Message = "prompt not provided for image edit stream request"
			if err.ExtraFields == nil {
				err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			err.ExtraFields.RequestType = schemas.ImageEditStreamRequest
			err.ExtraFields.Provider = req.Provider
			err.ExtraFields.ModelRequested = req.Model
			return nil, err
		}
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ImageEditStreamRequest
	bifrostReq.ImageEditRequest = req

	return bifrost.handleStreamRequest(ctx, bifrostReq)
}

// ImageVariationRequest sends an image variation request to the specified provider.
func (bifrost *Bifrost) ImageVariationRequest(ctx *schemas.BifrostContext, req *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "image variation request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageVariationRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Image.Image == nil || len(req.Input.Image.Image) == 0 {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "image not provided for image variation request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageVariationRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ImageVariationRequest
	bifrostReq.ImageVariationRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}

	if response == nil || response.ImageGenerationResponse == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "received nil response from provider"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ImageVariationRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	return response.ImageGenerationResponse, nil
}

// BatchCreateRequest creates a new batch job for asynchronous processing.
func (bifrost *Bifrost) BatchCreateRequest(ctx *schemas.BifrostContext, req *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch create request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCreateRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for batch create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCreateRequest
		return nil, err
	}
	if req.InputFileID == "" && len(req.Requests) == 0 {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "either input_file_id or requests is required for batch create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCreateRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	provider := bifrost.getProviderByKey(req.Provider)
	if provider == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider not found for batch create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCreateRequest
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.BatchCreateRequest
	bifrostReq.BatchCreateRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.BatchCreateResponse, nil
}

// BatchListRequest lists batch jobs for the specified provider.
func (bifrost *Bifrost) BatchListRequest(ctx *schemas.BifrostContext, req *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch list request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchListRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for batch list request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchListRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.BatchListRequest
	bifrostReq.BatchListRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.BatchListResponse, nil
}

// BatchRetrieveRequest retrieves a specific batch job.
func (bifrost *Bifrost) BatchRetrieveRequest(ctx *schemas.BifrostContext, req *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch retrieve request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchRetrieveRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for batch retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchRetrieveRequest
		return nil, err
	}
	if req.BatchID == "" {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "batch_id is required for batch retrieve request",
			},
		}
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.BatchRetrieveRequest
	bifrostReq.BatchRetrieveRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.BatchRetrieveResponse, nil
}

// BatchCancelRequest cancels a batch job.
func (bifrost *Bifrost) BatchCancelRequest(ctx *schemas.BifrostContext, req *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch cancel request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCancelRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for batch cancel request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCancelRequest
		return nil, err
	}
	if req.BatchID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch_id is required for batch cancel request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchCancelRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.BatchCancelRequest
	bifrostReq.BatchCancelRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.BatchCancelResponse, nil
}

// BatchResultsRequest retrieves results from a completed batch job.
func (bifrost *Bifrost) BatchResultsRequest(ctx *schemas.BifrostContext, req *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch results request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchResultsRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for batch results request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchResultsRequest
		return nil, err
	}
	if req.BatchID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "batch_id is required for batch results request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.BatchResultsRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.BatchResultsRequest
	bifrostReq.BatchResultsRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.BatchResultsResponse, nil
}

// FileUploadRequest uploads a file to the specified provider.
func (bifrost *Bifrost) FileUploadRequest(ctx *schemas.BifrostContext, req *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file upload request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileUploadRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for file upload request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileUploadRequest
		return nil, err
	}
	if len(req.File) == 0 {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file content is required for file upload request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileUploadRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.FileUploadRequest
	bifrostReq.FileUploadRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.FileUploadResponse, nil
}

// FileListRequest lists files from the specified provider.
func (bifrost *Bifrost) FileListRequest(ctx *schemas.BifrostContext, req *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file list request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileListRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for file list request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileListRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.FileListRequest
	bifrostReq.FileListRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.FileListResponse, nil
}

// FileRetrieveRequest retrieves file metadata from the specified provider.
func (bifrost *Bifrost) FileRetrieveRequest(ctx *schemas.BifrostContext, req *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file retrieve request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileRetrieveRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for file retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileRetrieveRequest
		return nil, err
	}
	if req.FileID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file_id is required for file retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileRetrieveRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.FileRetrieveRequest
	bifrostReq.FileRetrieveRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.FileRetrieveResponse, nil
}

// FileDeleteRequest deletes a file from the specified provider.
func (bifrost *Bifrost) FileDeleteRequest(ctx *schemas.BifrostContext, req *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file delete request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileDeleteRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for file delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileDeleteRequest
		return nil, err
	}
	if req.FileID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file_id is required for file delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileDeleteRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.FileDeleteRequest
	bifrostReq.FileDeleteRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.FileDeleteResponse, nil
}

// FileContentRequest downloads file content from the specified provider.
func (bifrost *Bifrost) FileContentRequest(ctx *schemas.BifrostContext, req *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file content request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileContentRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for file content request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileContentRequest
		return nil, err
	}
	if req.FileID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file_id is required for file content request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.FileContentRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.FileContentRequest
	bifrostReq.FileContentRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.FileContentResponse, nil
}

// ContainerCreateRequest creates a new container.
func (bifrost *Bifrost) ContainerCreateRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container create request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerCreateRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerCreateRequest
		return nil, err
	}
	if req.Name == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "name is required for container create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerCreateRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerCreateRequest
	bifrostReq.ContainerCreateRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerCreateResponse, nil
}

// ContainerListRequest lists containers.
func (bifrost *Bifrost) ContainerListRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container list request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerListRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container list request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerListRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerListRequest
	bifrostReq.ContainerListRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerListResponse, nil
}

// ContainerRetrieveRequest retrieves a specific container.
func (bifrost *Bifrost) ContainerRetrieveRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container retrieve request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerRetrieveRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerRetrieveRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerRetrieveRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerRetrieveRequest
	bifrostReq.ContainerRetrieveRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerRetrieveResponse, nil
}

// ContainerDeleteRequest deletes a container.
func (bifrost *Bifrost) ContainerDeleteRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container delete request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerDeleteRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerDeleteRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerDeleteRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerDeleteRequest
	bifrostReq.ContainerDeleteRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerDeleteResponse, nil
}

// ContainerFileCreateRequest creates a file in a container.
func (bifrost *Bifrost) ContainerFileCreateRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container file create request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileCreateRequest
		return nil, err

	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container file create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileCreateRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container file create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileCreateRequest
		return nil, err
	}
	if len(req.File) == 0 && (req.FileID == nil || strings.TrimSpace(*req.FileID) == "") && (req.Path == nil || strings.TrimSpace(*req.Path) == "") {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "one of file, file_id, or path is required for container file create request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileCreateRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerFileCreateRequest
	bifrostReq.ContainerFileCreateRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerFileCreateResponse, nil
}

// ContainerFileListRequest lists files in a container.
func (bifrost *Bifrost) ContainerFileListRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container file list request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileListRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container file list request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileListRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container file list request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileListRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerFileListRequest
	bifrostReq.ContainerFileListRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerFileListResponse, nil
}

// ContainerFileRetrieveRequest retrieves a file from a container.
func (bifrost *Bifrost) ContainerFileRetrieveRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container file retrieve request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileRetrieveRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container file retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileRetrieveRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container file retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileRetrieveRequest
		return nil, err
	}
	if req.FileID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file_id is required for container file retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileRetrieveRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerFileRetrieveRequest
	bifrostReq.ContainerFileRetrieveRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerFileRetrieveResponse, nil
}

// ContainerFileContentRequest retrieves the content of a file from a container.
func (bifrost *Bifrost) ContainerFileContentRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container file content request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileContentRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container file content request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileContentRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container file content request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileContentRequest
		return nil, err
	}
	if req.FileID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file_id is required for container file content request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileContentRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerFileContentRequest
	bifrostReq.ContainerFileContentRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerFileContentResponse, nil
}

// ContainerFileDeleteRequest deletes a file from a container.
func (bifrost *Bifrost) ContainerFileDeleteRequest(ctx *schemas.BifrostContext, req *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container file delete request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileDeleteRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for container file delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileDeleteRequest
		return nil, err
	}
	if req.ContainerID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "container_id is required for container file delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileDeleteRequest
		return nil, err
	}
	if req.FileID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "file_id is required for container file delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ContainerFileDeleteRequest
		return nil, err
	}
	if ctx == nil {
		ctx = bifrost.ctx
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.ContainerFileDeleteRequest
	bifrostReq.ContainerFileDeleteRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.ContainerFileDeleteResponse, nil
}

// RerankRequest sends a rerank request to the specified provider.
func (bifrost *Bifrost) RerankRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "rerank request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.RerankRequest
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "query not provided for rerank request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.RerankRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	if len(req.Documents) == 0 {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "documents not provided for rerank request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.RerankRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}
	for i, doc := range req.Documents {
		if strings.TrimSpace(doc.Text) == "" {
			err := schemas.AcquireBifrostError()
			err.IsBifrostError = false
			err.Error.Message = fmt.Sprintf("document text is empty at index %d", i)
			if err.ExtraFields == nil {
				err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			err.ExtraFields.RequestType = schemas.RerankRequest
			err.ExtraFields.Provider = req.Provider
			err.ExtraFields.ModelRequested = req.Model
			return nil, err
		}
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.RerankRequest
	bifrostReq.RerankRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.RerankResponse, nil
}

// VideoGenerationRequest sends a video generation request to the specified provider.
func (bifrost *Bifrost) VideoGenerationRequest(ctx *schemas.BifrostContext,
	req *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video generation request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoGenerationRequest
		return nil, err
	}
	if req.Input == nil || req.Input.Prompt == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "prompt not provided for video generation request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoGenerationRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.VideoGenerationRequest
	bifrostReq.VideoGenerationRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	if response == nil || response.VideoGenerationResponse == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "received nil response from provider"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoGenerationRequest
		err.ExtraFields.Provider = req.Provider
		err.ExtraFields.ModelRequested = req.Model
		return nil, err
	}

	return response.VideoGenerationResponse, nil
}

// VideoRetrieveRequest retrieves video generation status from the provider.
func (bifrost *Bifrost) VideoRetrieveRequest(ctx *schemas.BifrostContext, req *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video retrieve request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRetrieveRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for video retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRetrieveRequest
		return nil, err
	}
	if req.ID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video_id is required for video retrieve request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRetrieveRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.VideoRetrieveRequest
	bifrostReq.VideoRetrieveRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	if response == nil || response.VideoGenerationResponse == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "received nil response from provider"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRetrieveRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}
	return response.VideoGenerationResponse, nil
}

// VideoDownloadRequest downloads video content from the provider.
func (bifrost *Bifrost) VideoDownloadRequest(ctx *schemas.BifrostContext, req *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video download request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoDownloadRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for video download request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoDownloadRequest
		return nil, err
	}
	if req.ID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video_id is required for video download request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoDownloadRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.VideoDownloadRequest
	bifrostReq.VideoDownloadRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.VideoDownloadResponse, nil
}

// VideoRemixRequest sends a video remix request to the specified provider.
func (bifrost *Bifrost) VideoRemixRequest(ctx *schemas.BifrostContext, req *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video remix request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRemixRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for video remix request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRemixRequest
		return nil, err
	}
	if req.ID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video_id is required for video remix request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRemixRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}
	if req.Input == nil || req.Input.Prompt == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "prompt is required for video remix request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRemixRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.VideoRemixRequest
	bifrostReq.VideoRemixRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	if response == nil || response.VideoGenerationResponse == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "received nil response from provider"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoRemixRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}
	return response.VideoGenerationResponse, nil
}

// VideoListRequest lists video generations from the provider.
func (bifrost *Bifrost) VideoListRequest(ctx *schemas.BifrostContext, req *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video list request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoListRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for video list request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoListRequest
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.VideoListRequest
	bifrostReq.VideoListRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.VideoListResponse, nil
}

// VideoDeleteRequest deletes a video generation from the provider.
func (bifrost *Bifrost) VideoDeleteRequest(ctx *schemas.BifrostContext, req *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	if req == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video delete request is nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoDeleteRequest
		return nil, err
	}
	if req.Provider == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "provider is required for video delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoDeleteRequest
		return nil, err
	}
	if req.ID == "" {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "video_id is required for video delete request"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.VideoDeleteRequest
		err.ExtraFields.Provider = req.Provider
		return nil, err
	}

	bifrostReq := schemas.AcquireBifrostRequest()
	defer schemas.ReleaseBifrostRequest(bifrostReq)
	bifrostReq.RequestType = schemas.VideoDeleteRequest
	bifrostReq.VideoDeleteRequest = req

	response, err := bifrost.handleRequest(ctx, bifrostReq)
	if err != nil {
		return nil, err
	}
	return response.VideoDeleteResponse, nil
}
