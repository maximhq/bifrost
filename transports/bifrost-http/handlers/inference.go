// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains completion request handlers for text and chat completions.
package handlers

import (
	"bufio"
	"context"

	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CompletionHandler manages HTTP requests for completion operations
type CompletionHandler struct {
	client       *bifrost.Bifrost
	handlerStore lib.HandlerStore
	config       *lib.Config
}

// NewInferenceHandler creates a new completion handler instance
func NewInferenceHandler(client *bifrost.Bifrost, config *lib.Config) *CompletionHandler {
	return &CompletionHandler{
		client:       client,
		handlerStore: config,
		config:       config,
	}
}

// Known fields for CompletionRequest
var textParamsKnownFields = map[string]bool{
	"model":             true,
	"text":              true,
	"fallbacks":         true,
	"best_of":           true,
	"echo":              true,
	"frequency_penalty": true,
	"logit_bias":        true,
	"logprobs":          true,
	"max_tokens":        true,
	"n":                 true,
	"presence_penalty":  true,
	"seed":              true,
	"stop":              true,
	"suffix":            true,
	"temperature":       true,
	"top_p":             true,
	"user":              true,
}

// Known fields for CompletionRequest
var chatParamsKnownFields = map[string]bool{
	"model":                 true,
	"messages":              true,
	"fallbacks":             true,
	"stream":                true,
	"frequency_penalty":     true,
	"logit_bias":            true,
	"logprobs":              true,
	"max_completion_tokens": true,
	"metadata":              true,
	"modalities":            true,
	"parallel_tool_calls":   true,
	"presence_penalty":      true,
	"prompt_cache_key":      true,
	"reasoning":             true,
	"response_format":       true,
	"safety_identifier":     true,
	"service_tier":          true,
	"stream_options":        true,
	"store":                 true,
	"temperature":           true,
	"tool_choice":           true,
	"tools":                 true,
	"truncation":            true,
	"user":                  true,
	"verbosity":             true,
}

var responsesParamsKnownFields = map[string]bool{
	"model":                true,
	"input":                true,
	"fallbacks":            true,
	"stream":               true,
	"background":           true,
	"conversation":         true,
	"include":              true,
	"instructions":         true,
	"max_output_tokens":    true,
	"max_tool_calls":       true,
	"metadata":             true,
	"parallel_tool_calls":  true,
	"previous_response_id": true,
	"prompt_cache_key":     true,
	"reasoning":            true,
	"safety_identifier":    true,
	"service_tier":         true,
	"stream_options":       true,
	"store":                true,
	"temperature":          true,
	"text":                 true,
	"top_logprobs":         true,
	"top_p":                true,
	"tool_choice":          true,
	"tools":                true,
	"truncation":           true,
}

var embeddingParamsKnownFields = map[string]bool{
	"model":           true,
	"input":           true,
	"fallbacks":       true,
	"encoding_format": true,
	"dimensions":      true,
}

var speechParamsKnownFields = map[string]bool{
	"model":           true,
	"input":           true,
	"fallbacks":       true,
	"stream_format":   true,
	"voice":           true,
	"instructions":    true,
	"response_format": true,
	"speed":           true,
}

var imageParamsKnownFields = map[string]bool{
	"model":               true,
	"prompt":              true,
	"fallbacks":           true,
	"stream":              true,
	"n":                   true,
	"background":          true,
	"moderation":          true,
	"partial_images":      true,
	"size":                true,
	"quality":             true,
	"output_compression":  true,
	"output_format":       true,
	"style":               true,
	"response_format":     true,
	"seed":                true,
	"negative_prompt":     true,
	"num_inference_steps": true,
	"user":                true,
}

var transcriptionParamsKnownFields = map[string]bool{
	"model":           true,
	"file":            true,
	"fallbacks":       true,
	"stream":          true,
	"language":        true,
	"prompt":          true,
	"response_format": true,
	"file_format":     true,
}

var countTokensParamsKnownFields = map[string]bool{
	"model":        true,
	"messages":     true,
	"fallbacks":    true,
	"tools":        true,
	"instructions": true,
	"text":         true,
}

var batchCreateParamsKnownFields = map[string]bool{
	"model":             true,
	"input_file_id":     true,
	"requests":          true,
	"endpoint":          true,
	"completion_window": true,
	"metadata":          true,
}

var containerCreateParamsKnownFields = map[string]bool{
	"provider":      true,
	"name":          true,
	"expires_after": true,
	"file_ids":      true,
	"memory_limit":  true,
	"metadata":      true,
}

type BifrostParams struct {
	Model        string   `json:"model"`                   // Model to use in "provider/model" format
	Fallbacks    []string `json:"fallbacks"`               // Fallback providers and models in "provider/model" format
	Stream       *bool    `json:"stream"`                  // Whether to stream the response
	StreamFormat *string  `json:"stream_format,omitempty"` // For speech
}

type TextRequest struct {
	Prompt *schemas.TextCompletionInput `json:"prompt"`
	BifrostParams
	*schemas.TextCompletionParameters
}

type ChatRequest struct {
	Messages []schemas.ChatMessage `json:"messages"`
	BifrostParams
	*schemas.ChatParameters
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatRequest.
// This is needed because ChatParameters has a custom UnmarshalJSON method,
// which interferes with sonic's handling of the embedded BifrostParams struct.
func (cr *ChatRequest) UnmarshalJSON(data []byte) error {
	// First, unmarshal BifrostParams fields directly
	type bifrostAlias BifrostParams
	var bp bifrostAlias
	if err := sonic.Unmarshal(data, &bp); err != nil {
		return err
	}
	cr.BifrostParams = BifrostParams(bp)

	// Unmarshal messages
	var msgStruct struct {
		Messages []schemas.ChatMessage `json:"messages"`
	}
	if err := sonic.Unmarshal(data, &msgStruct); err != nil {
		return err
	}
	cr.Messages = msgStruct.Messages

	// Unmarshal ChatParameters (which has its own custom unmarshaller)
	if cr.ChatParameters == nil {
		cr.ChatParameters = &schemas.ChatParameters{}
	}
	if err := sonic.Unmarshal(data, cr.ChatParameters); err != nil {
		return err
	}

	return nil
}

// ResponsesRequestInput is a union of string and array of responses messages
type ResponsesRequestInput struct {
	ResponsesRequestInputStr   *string
	ResponsesRequestInputArray []schemas.ResponsesMessage
}

type ImageGenerationHTTPRequest struct {
	*schemas.ImageGenerationInput
	*schemas.ImageGenerationParameters
	BifrostParams
}

// UnmarshalJSON unmarshals the responses request input
func (r *ResponsesRequestInput) UnmarshalJSON(data []byte) error {
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		r.ResponsesRequestInputStr = &str
		r.ResponsesRequestInputArray = nil
		return nil
	}
	var array []schemas.ResponsesMessage
	if err := sonic.Unmarshal(data, &array); err == nil {
		r.ResponsesRequestInputStr = nil
		r.ResponsesRequestInputArray = array
		return nil
	}
	return fmt.Errorf("invalid responses request input")
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesRequest.
// This is needed because ResponsesParameters has a custom UnmarshalJSON method,
// which interferes with sonic's handling of the embedded BifrostParams struct.
func (rr *ResponsesRequest) UnmarshalJSON(data []byte) error {
	// First, unmarshal BifrostParams fields directly
	type bifrostAlias BifrostParams
	var bp bifrostAlias
	if err := sonic.Unmarshal(data, &bp); err != nil {
		return err
	}
	rr.BifrostParams = BifrostParams(bp)

	// Unmarshal messages
	var inputStruct struct {
		Input ResponsesRequestInput `json:"input"`
	}
	if err := sonic.Unmarshal(data, &inputStruct); err != nil {
		return err
	}
	rr.Input = inputStruct.Input

	// Unmarshal ResponsesParameters (which has its own custom unmarshaller)
	if rr.ResponsesParameters == nil {
		rr.ResponsesParameters = &schemas.ResponsesParameters{}
	}
	if err := sonic.Unmarshal(data, rr.ResponsesParameters); err != nil {
		return err
	}

	return nil
}

// ResponsesRequest is a bifrost responses request
type ResponsesRequest struct {
	Input ResponsesRequestInput `json:"input"`
	BifrostParams
	*schemas.ResponsesParameters
}

// EmbeddingRequest is a bifrost embedding request
type EmbeddingRequest struct {
	Input *schemas.EmbeddingInput `json:"input"`
	BifrostParams
	*schemas.EmbeddingParameters
}

type SpeechRequest struct {
	*schemas.SpeechInput
	BifrostParams
	*schemas.SpeechParameters
}

type TranscriptionRequest struct {
	*schemas.TranscriptionInput
	BifrostParams
	*schemas.TranscriptionParameters
}

type CountTokensRequest struct {
	Messages []schemas.ResponsesMessage `json:"messages"`
	Tools    []schemas.ResponsesTool    `json:"tools,omitempty"`
	BifrostParams
	*schemas.ResponsesParameters
}

// UnmarshalJSON implements custom JSON unmarshalling for CountTokensRequest.
// This is needed because ResponsesParameters has a custom UnmarshalJSON method,
// which interferes with sonic's handling of the embedded BifrostParams struct.
func (cr *CountTokensRequest) UnmarshalJSON(data []byte) error {
	// First, unmarshal BifrostParams fields directly
	type bifrostAlias BifrostParams
	var bp bifrostAlias
	if err := sonic.Unmarshal(data, &bp); err != nil {
		return err
	}
	cr.BifrostParams = BifrostParams(bp)

	// Unmarshal messages and tools
	var msgStruct struct {
		Messages []schemas.ResponsesMessage `json:"messages"`
		Tools    []schemas.ResponsesTool    `json:"tools,omitempty"`
	}
	if err := sonic.Unmarshal(data, &msgStruct); err != nil {
		return err
	}
	cr.Messages = msgStruct.Messages
	cr.Tools = msgStruct.Tools

	// Unmarshal ResponsesParameters (which has its own custom unmarshaller)
	if cr.ResponsesParameters == nil {
		cr.ResponsesParameters = &schemas.ResponsesParameters{}
	}
	if err := sonic.Unmarshal(data, cr.ResponsesParameters); err != nil {
		return err
	}

	return nil
}

// BatchCreateRequest is a bifrost batch create request
type BatchCreateRequest struct {
	Model            string                     `json:"model"`                       // Model in "provider/model" format
	InputFileID      string                     `json:"input_file_id,omitempty"`     // OpenAI-style file ID
	Requests         []schemas.BatchRequestItem `json:"requests,omitempty"`          // Anthropic-style inline requests
	Endpoint         string                     `json:"endpoint,omitempty"`          // e.g., "/v1/chat/completions"
	CompletionWindow string                     `json:"completion_window,omitempty"` // e.g., "24h"
	Metadata         map[string]string          `json:"metadata,omitempty"`
}

// BatchListRequest is a bifrost batch list request
type BatchListRequest struct {
	Provider string  `json:"provider"`         // Provider name
	Limit    int     `json:"limit,omitempty"`  // Maximum number of batches to return
	After    *string `json:"after,omitempty"`  // Cursor for pagination
	Before   *string `json:"before,omitempty"` // Cursor for pagination
}

// ContainerCreateRequest is a bifrost container create request
type ContainerCreateRequest struct {
	Provider     string                         `json:"provider"`                // Provider name
	Name         string                         `json:"name"`                    // Name of the container
	ExpiresAfter *schemas.ContainerExpiresAfter `json:"expires_after,omitempty"` // Expiration configuration
	FileIDs      []string                       `json:"file_ids,omitempty"`      // IDs of existing files to copy into this container
	MemoryLimit  string                         `json:"memory_limit,omitempty"`  // Memory limit (e.g., "1g", "4g")
	Metadata     map[string]string              `json:"metadata,omitempty"`      // User-provided metadata
}

// Helper functions

// enableRawRequestResponseForContainer sets context flags to always capture raw request/response
// for container operations. Container operations don't have model-specific content, so raw
// data is useful for debugging and should be enabled by default.
func enableRawRequestResponseForContainer(bifrostCtx *schemas.BifrostContext) {
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeyRawRequestResponseForLogging, true)
}

// parseFallbacks extracts fallbacks from string array and converts to Fallback structs
func parseFallbacks(fallbackStrings []string) ([]schemas.Fallback, error) {
	fallbacks := make([]schemas.Fallback, 0, len(fallbackStrings))
	for _, fallback := range fallbackStrings {
		fallbackProvider, fallbackModelName := schemas.ParseModelString(fallback, "")
		if fallbackProvider != "" && fallbackModelName != "" {
			fallbacks = append(fallbacks, schemas.Fallback{
				Provider: fallbackProvider,
				Model:    fallbackModelName,
			})
		}
	}
	return fallbacks, nil
}

// extractExtraParams processes unknown fields from JSON data into ExtraParams
func extractExtraParams(data []byte, knownFields map[string]bool) (map[string]any, error) {
	// Parse JSON to extract unknown fields
	var rawData map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &rawData); err != nil {
		return nil, err
	}

	// Extract unknown fields
	extraParams := make(map[string]any)
	for key, value := range rawData {
		if !knownFields[key] {
			var v any
			if err := sonic.Unmarshal(value, &v); err != nil {
				continue // Skip fields that can't be unmarshaled
			}
			extraParams[key] = v
		}
	}

	return extraParams, nil
}

const (
	// Maximum file size (25MB)
	MaxFileSize = 25 * 1024 * 1024

	// Primary MIME types for audio formats
	AudioMimeMP3   = "audio/mpeg"   // Covers MP3, MPEG, MPGA
	AudioMimeMP4   = "audio/mp4"    // MP4 audio
	AudioMimeM4A   = "audio/x-m4a"  // M4A specific
	AudioMimeOGG   = "audio/ogg"    // OGG audio
	AudioMimeWAV   = "audio/wav"    // WAV audio
	AudioMimeWEBM  = "audio/webm"   // WEBM audio
	AudioMimeFLAC  = "audio/flac"   // FLAC audio
	AudioMimeFLAC2 = "audio/x-flac" // Alternative FLAC
)

// RegisterRoutes registers all completion-related routes
func (h *CompletionHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Model endpoints
	r.GET("/v1/models", lib.ChainMiddlewares(h.listModels, middlewares...))

	// Completion endpoints
	r.POST("/v1/completions", lib.ChainMiddlewares(h.textCompletion, middlewares...))
	r.POST("/v1/chat/completions", lib.ChainMiddlewares(h.chatCompletion, middlewares...))
	r.POST("/v1/responses", lib.ChainMiddlewares(h.responses, middlewares...))
	r.POST("/v1/embeddings", lib.ChainMiddlewares(h.embeddings, middlewares...))
	r.POST("/v1/audio/speech", lib.ChainMiddlewares(h.speech, middlewares...))
	r.POST("/v1/audio/transcriptions", lib.ChainMiddlewares(h.transcription, middlewares...))
	r.POST("/v1/images/generations", lib.ChainMiddlewares(h.imageGeneration, middlewares...))
	r.POST("/v1/count_tokens", lib.ChainMiddlewares(h.countTokens, middlewares...))

	// Batch API endpoints
	r.POST("/v1/batches", lib.ChainMiddlewares(h.batchCreate, middlewares...))
	r.GET("/v1/batches", lib.ChainMiddlewares(h.batchList, middlewares...))
	r.GET("/v1/batches/{batch_id}", lib.ChainMiddlewares(h.batchRetrieve, middlewares...))
	r.POST("/v1/batches/{batch_id}/cancel", lib.ChainMiddlewares(h.batchCancel, middlewares...))
	r.GET("/v1/batches/{batch_id}/results", lib.ChainMiddlewares(h.batchResults, middlewares...))

	// File API endpoints
	r.POST("/v1/files", lib.ChainMiddlewares(h.fileUpload, middlewares...))
	r.GET("/v1/files", lib.ChainMiddlewares(h.fileList, middlewares...))
	r.GET("/v1/files/{file_id}", lib.ChainMiddlewares(h.fileRetrieve, middlewares...))
	r.DELETE("/v1/files/{file_id}", lib.ChainMiddlewares(h.fileDelete, middlewares...))
	r.GET("/v1/files/{file_id}/content", lib.ChainMiddlewares(h.fileContent, middlewares...))

	// Container API endpoints
	r.POST("/v1/containers", lib.ChainMiddlewares(h.containerCreate, middlewares...))
	r.GET("/v1/containers", lib.ChainMiddlewares(h.containerList, middlewares...))
	r.GET("/v1/containers/{container_id}", lib.ChainMiddlewares(h.containerRetrieve, middlewares...))
	r.DELETE("/v1/containers/{container_id}", lib.ChainMiddlewares(h.containerDelete, middlewares...))

	// Container Files API endpoints
	r.POST("/v1/containers/{container_id}/files", lib.ChainMiddlewares(h.containerFileCreate, middlewares...))
	r.GET("/v1/containers/{container_id}/files", lib.ChainMiddlewares(h.containerFileList, middlewares...))
	r.GET("/v1/containers/{container_id}/files/{file_id}", lib.ChainMiddlewares(h.containerFileRetrieve, middlewares...))
	r.GET("/v1/containers/{container_id}/files/{file_id}/content", lib.ChainMiddlewares(h.containerFileContent, middlewares...))
	r.DELETE("/v1/containers/{container_id}/files/{file_id}", lib.ChainMiddlewares(h.containerFileDelete, middlewares...))
}

// listModels handles GET /v1/models - Process list models requests
// If provider is not specified, lists all models from all configured providers
func (h *CompletionHandler) listModels(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel() // Ensure cleanup on function exit
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	var resp *schemas.BifrostListModelsResponse
	var bifrostErr *schemas.BifrostError

	pageSize := 0
	if pageSizeStr := ctx.QueryArgs().Peek("page_size"); len(pageSizeStr) > 0 {
		if n, err := strconv.Atoi(string(pageSizeStr)); err == nil && n >= 0 {
			pageSize = n
		}
	}
	pageToken := string(ctx.QueryArgs().Peek("page_token"))

	bifrostListModelsReq := &schemas.BifrostListModelsRequest{
		Provider:  schemas.ModelProvider(provider),
		PageSize:  pageSize,
		PageToken: pageToken,
	}

	// Pass-through unknown query params for provider-specific features
	extraParams := map[string]interface{}{}
	for k, v := range ctx.QueryArgs().All() {
		s := string(k)
		if s != "provider" && s != "page_size" && s != "page_token" {
			extraParams[s] = string(v)
		}
	}
	if len(extraParams) > 0 {
		bifrostListModelsReq.ExtraParams = extraParams
	}

	// If provider is empty, list all models from all providers
	if provider == "" {
		resp, bifrostErr = h.client.ListAllModels(bifrostCtx, bifrostListModelsReq)
	} else {
		resp, bifrostErr = h.client.ListModelsRequest(bifrostCtx, bifrostListModelsReq)
	}

	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Add pricing data to the response
	if len(resp.Data) > 0 && h.config.PricingManager != nil {
		for i, modelEntry := range resp.Data {
			provider, modelName := schemas.ParseModelString(modelEntry.ID, "")
			pricingEntry := h.config.PricingManager.GetPricingEntryForModel(modelName, provider)
			if pricingEntry == nil && modelEntry.Deployment != nil {
				// Retry with deployment
				pricingEntry = h.config.PricingManager.GetPricingEntryForModel(*modelEntry.Deployment, provider)
			}
			if pricingEntry != nil && modelEntry.Pricing == nil {
				pricing := &schemas.Pricing{
					Prompt:     bifrost.Ptr(fmt.Sprintf("%.10f", pricingEntry.InputCostPerToken)),
					Completion: bifrost.Ptr(fmt.Sprintf("%.10f", pricingEntry.OutputCostPerToken)),
				}
				if pricingEntry.InputCostPerImage != nil {
					pricing.Image = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.InputCostPerImage))
				}
				if pricingEntry.CacheReadInputTokenCost != nil {
					pricing.InputCacheRead = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.CacheReadInputTokenCost))
				}
				resp.Data[i].Pricing = pricing
			}
		}
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// textCompletion handles POST /v1/completions - Process text completion requests
func (h *CompletionHandler) textCompletion(ctx *fasthttp.RequestCtx) {
	var req TextRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}
	// Create BifrostTextCompletionRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}
	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if req.Prompt == nil || (req.Prompt.PromptStr == nil && req.Prompt.PromptArray == nil) {
		SendError(ctx, fasthttp.StatusBadRequest, "prompt is required for text completion")
		return
	}
	// Extract extra params
	if req.TextCompletionParameters == nil {
		req.TextCompletionParameters = &schemas.TextCompletionParameters{}
	}
	extraParams, err := extractExtraParams(ctx.PostBody(), textParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.TextCompletionParameters.ExtraParams = extraParams
	}
	// Create segregated BifrostTextCompletionRequest
	bifrostTextReq := &schemas.BifrostTextCompletionRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.Prompt,
		Params:    req.TextCompletionParameters,
		Fallbacks: fallbacks,
	}
	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	if req.Stream != nil && *req.Stream {
		h.handleStreamingTextCompletion(ctx, bifrostTextReq, bifrostCtx, cancel)
		return
	}

	// NOTE: these defers wont work as expected when a non-streaming request is cancelled on flight.
	// valyala/fasthttp does not support cancelling a request in the middle of a request.
	// This is a known issue of valyala/fasthttp. And will be fixed here once it is fixed upstream.
	defer cancel() // Ensure cleanup on function exit

	resp, bifrostErr := h.client.TextCompletionRequest(bifrostCtx, bifrostTextReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// chatCompletion handles POST /v1/chat/completions - Process chat completion requests
func (h *CompletionHandler) chatCompletion(ctx *fasthttp.RequestCtx) {
	req := ChatRequest{
		ChatParameters: &schemas.ChatParameters{},
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Create BifrostChatRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if len(req.Messages) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "Messages is required for chat completion")
		return
	}

	// Extract extra params
	if req.ChatParameters == nil {
		req.ChatParameters = &schemas.ChatParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), chatParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		// Handle max_tokens -> max_completion_tokens mapping after extracting extra params
		// If max_completion_tokens is nil and max_tokens is present in extra params, map it
		// This is to support the legacy max_tokens field, which is still used by some implementations.
		if req.ChatParameters.MaxCompletionTokens == nil {
			if maxTokensVal, exists := extraParams["max_tokens"]; exists {
				// Type check and convert to int
				// JSON numbers are unmarshaled as float64, so we need to handle that
				var maxTokens int
				if maxTokensFloat, ok := maxTokensVal.(float64); ok {
					maxTokens = int(maxTokensFloat)
					req.ChatParameters.MaxCompletionTokens = &maxTokens
					// Remove max_tokens from extra params since we've mapped it
					delete(extraParams, "max_tokens")
				} else if maxTokensInt, ok := maxTokensVal.(int); ok {
					req.ChatParameters.MaxCompletionTokens = &maxTokensInt
					// Remove max_tokens from extra params since we've mapped it
					delete(extraParams, "max_tokens")
				}
			}
		}
		req.ChatParameters.ExtraParams = extraParams
	}

	// Create segregated BifrostChatRequest
	bifrostChatReq := &schemas.BifrostChatRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.Messages,
		Params:    req.ChatParameters,
		Fallbacks: fallbacks,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	if req.Stream != nil && *req.Stream {
		h.handleStreamingChatCompletion(ctx, bifrostChatReq, bifrostCtx, cancel)
		return
	}
	defer cancel() // Ensure cleanup on function exit
	// Complete the request
	resp, bifrostErr := h.client.ChatCompletionRequest(bifrostCtx, bifrostChatReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// responses handles POST /v1/responses - Process responses requests
func (h *CompletionHandler) responses(ctx *fasthttp.RequestCtx) {
	var req ResponsesRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Create BifrostResponsesRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if len(req.Input.ResponsesRequestInputArray) == 0 && req.Input.ResponsesRequestInputStr == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Input is required for responses")
		return
	}

	// Extract extra params
	if req.ResponsesParameters == nil {
		req.ResponsesParameters = &schemas.ResponsesParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), responsesParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.ResponsesParameters.ExtraParams = extraParams
	}

	input := req.Input.ResponsesRequestInputArray
	if input == nil {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: req.Input.ResponsesRequestInputStr},
			},
		}
	}

	// Create segregated BifrostResponsesRequest
	bifrostResponsesReq := &schemas.BifrostResponsesRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     input,
		Params:    req.ResponsesParameters,
		Fallbacks: fallbacks,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	if req.Stream != nil && *req.Stream {
		h.handleStreamingResponses(ctx, bifrostResponsesReq, bifrostCtx, cancel)
		return
	}

	defer cancel() // Ensure cleanup on function exit

	resp, bifrostErr := h.client.ResponsesRequest(bifrostCtx, bifrostResponsesReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// embeddings handles POST /v1/embeddings - Process embeddings requests
func (h *CompletionHandler) embeddings(ctx *fasthttp.RequestCtx) {
	var req EmbeddingRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Create BifrostEmbeddingRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if req.Input == nil || (req.Input.Text == nil && req.Input.Texts == nil && req.Input.Embedding == nil && req.Input.Embeddings == nil) {
		SendError(ctx, fasthttp.StatusBadRequest, "Input is required for embeddings")
		return
	}

	// Extract extra params
	if req.EmbeddingParameters == nil {
		req.EmbeddingParameters = &schemas.EmbeddingParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), embeddingParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.EmbeddingParameters.ExtraParams = extraParams
	}

	// Create segregated BifrostEmbeddingRequest
	bifrostEmbeddingReq := &schemas.BifrostEmbeddingRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.Input,
		Params:    req.EmbeddingParameters,
		Fallbacks: fallbacks,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel() // Ensure cleanup on function exit
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.EmbeddingRequest(bifrostCtx, bifrostEmbeddingReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// speech handles POST /v1/audio/speech - Process speech completion requests
func (h *CompletionHandler) speech(ctx *fasthttp.RequestCtx) {
	var req SpeechRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Create BifrostSpeechRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if req.SpeechInput == nil || req.SpeechInput.Input == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Input is required for speech completion")
		return
	}

	if req.VoiceConfig == nil || (req.VoiceConfig.Voice == nil && len(req.VoiceConfig.MultiVoiceConfig) == 0) {
		SendError(ctx, fasthttp.StatusBadRequest, "Voice is required for speech completion")
		return
	}

	// Extract extra params
	if req.SpeechParameters == nil {
		req.SpeechParameters = &schemas.SpeechParameters{}
	}

	// Extract extra params
	if req.SpeechParameters == nil {
		req.SpeechParameters = &schemas.SpeechParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), speechParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.SpeechParameters.ExtraParams = extraParams
	}

	// Create segregated BifrostSpeechRequest
	bifrostSpeechReq := &schemas.BifrostSpeechRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.SpeechInput,
		Params:    req.SpeechParameters,
		Fallbacks: fallbacks,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	if req.StreamFormat != nil && *req.StreamFormat == "sse" {
		h.handleStreamingSpeech(ctx, bifrostSpeechReq, bifrostCtx, cancel)
		return
	}

	defer cancel() // Ensure cleanup on function exit

	resp, bifrostErr := h.client.SpeechRequest(bifrostCtx, bifrostSpeechReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	// When with_timestamps is true, Elevenlabs returns base64 encoded audio
	hasTimestamps := req.WithTimestamps != nil && *req.WithTimestamps

	if provider == schemas.Elevenlabs && hasTimestamps {
		ctx.Response.Header.Set("Content-Type", "application/json")
		SendJSON(ctx, resp)
		return
	}

	if resp.Audio == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Speech response is missing audio data")
		return
	}

	ctx.Response.Header.Set("Content-Type", "audio/mpeg")
	ctx.Response.Header.Set("Content-Disposition", "attachment; filename=speech.mp3")
	ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(resp.Audio)))
	ctx.Response.SetBody(resp.Audio)
}

// transcription handles POST /v1/audio/transcriptions - Process transcription requests
func (h *CompletionHandler) transcription(ctx *fasthttp.RequestCtx) {
	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to parse multipart form: %v", err))
		return
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Model is required")
		return
	}

	provider, modelName := schemas.ParseModelString(modelValues[0], "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "File is required")
		return
	}

	fileHeader := fileHeaders[0]

	// // Validate file size and format
	// if err := h.validateAudioFile(fileHeader); err != nil {
	// 	SendError(ctx, fasthttp.StatusBadRequest, err.Error())
	// 	return
	// }

	file, err := fileHeader.Open()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to open uploaded file: %v", err))
		return
	}
	defer file.Close()

	// Read file data
	fileData := make([]byte, fileHeader.Size)
	if _, err := file.Read(fileData); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to read uploaded file: %v", err))
		return
	}

	// Create transcription input
	transcriptionInput := &schemas.TranscriptionInput{
		File: fileData,
	}

	// Create transcription parameters
	transcriptionParams := &schemas.TranscriptionParameters{}

	// Extract optional parameters
	if languageValues := form.Value["language"]; len(languageValues) > 0 && languageValues[0] != "" {
		transcriptionParams.Language = &languageValues[0]
	}

	if promptValues := form.Value["prompt"]; len(promptValues) > 0 && promptValues[0] != "" {
		transcriptionParams.Prompt = &promptValues[0]
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		transcriptionParams.ResponseFormat = &responseFormatValues[0]
	}

	if transcriptionParams.ExtraParams == nil {
		transcriptionParams.ExtraParams = make(map[string]interface{})
	}

	for key, value := range form.Value {
		if len(value) > 0 && value[0] != "" && !transcriptionParamsKnownFields[key] {
			transcriptionParams.ExtraParams[key] = value[0]
		}
	}

	// Create BifrostTranscriptionRequest
	bifrostTranscriptionReq := &schemas.BifrostTranscriptionRequest{
		Model:    modelName,
		Provider: schemas.ModelProvider(provider),
		Input:    transcriptionInput,
		Params:   transcriptionParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream := streamValues[0]
		if stream == "true" {
			h.handleStreamingTranscriptionRequest(ctx, bifrostTranscriptionReq, bifrostCtx, cancel)
			return
		}
	}

	defer cancel() // Ensure cleanup on function exit

	// Make transcription request
	resp, bifrostErr := h.client.TranscriptionRequest(bifrostCtx, bifrostTranscriptionReq)

	// Handle response
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, resp)
}

// countTokens handles POST /v1/count_tokens - Process count tokens requests
func (h *CompletionHandler) countTokens(ctx *fasthttp.RequestCtx) {
	// Parse request body
	req := CountTokensRequest{
		ResponsesParameters: &schemas.ResponsesParameters{},
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	// Create BifrostResponsesRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Extract extra params
	if req.ResponsesParameters == nil {
		req.ResponsesParameters = &schemas.ResponsesParameters{}
	}
	extraParams, err := extractExtraParams(ctx.PostBody(), countTokensParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.ResponsesParameters.ExtraParams = extraParams
	}

	// Set tools if provided
	if len(req.Tools) > 0 {
		req.ResponsesParameters.Tools = req.Tools
	}

	// Create segregated BifrostResponsesRequest
	// Validate messages are present
	if len(req.Messages) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "messages is required for count tokens")
		return
	}

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.Messages,
		Params:    req.ResponsesParameters,
		Fallbacks: fallbacks,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	defer cancel() // Ensure cleanup on function exit

	// Make count tokens request
	response, bifrostErr := h.client.CountTokensRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send successful response
	SendJSON(ctx, response)
}

// handleStreamingTextCompletion handles streaming text completion requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingTextCompletion(ctx *fasthttp.RequestCtx, req *schemas.BifrostTextCompletionRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.TextCompletionStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// handleStreamingChatCompletion handles streaming chat completion requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingChatCompletion(ctx *fasthttp.RequestCtx, req *schemas.BifrostChatRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.ChatCompletionStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// handleStreamingResponses handles streaming responses requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingResponses(ctx *fasthttp.RequestCtx, req *schemas.BifrostResponsesRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.ResponsesStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// handleStreamingSpeech handles streaming speech requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingSpeech(ctx *fasthttp.RequestCtx, req *schemas.BifrostSpeechRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.SpeechStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// handleStreamingTranscriptionRequest handles streaming transcription requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingTranscriptionRequest(ctx *fasthttp.RequestCtx, req *schemas.BifrostTranscriptionRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.TranscriptionStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// handleStreamingResponse is a generic function to handle streaming responses using Server-Sent Events (SSE)
// The cancel function is called ONLY when client disconnects are detected via write errors.
// Bifrost handles cleanup internally for normal completion and errors, so we only cancel
// upstream streams when write errors indicate the client has disconnected.
func (h *CompletionHandler) handleStreamingResponse(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, getStream func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError), cancel context.CancelFunc) {
	// Set SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")

	// Get the streaming channel
	stream, bifrostErr := getStream()
	if bifrostErr != nil {
		// Cancel stream context since we're not proceeding
		cancel()
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Signal to tracing middleware that trace completion should be deferred
	// The streaming callback will complete the trace after the stream ends
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)

	// Get the trace completer function for use in the streaming callback
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func())

	// Get stream chunk interceptor for plugin hooks
	interceptor := h.config.GetStreamChunkInterceptor()
	var httpReq *schemas.HTTPRequest	
	if interceptor != nil {
		httpReq = lib.BuildHTTPRequestFromFastHTTP(ctx)
	}
	var includeEventType bool
	// Use streaming response writer
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			schemas.ReleaseHTTPRequest(httpReq)
			w.Flush()
			// Complete the trace after streaming finishes
			// This ensures all spans (including llm.call) are properly ended before the trace is sent to OTEL
			if traceCompleter != nil {
				traceCompleter()
			}
		}()

		var skipDoneMarker bool

		// Process streaming responses
		for chunk := range stream {
			if chunk == nil {
				continue
			}

			includeEventType = false
			if chunk.BifrostResponsesStreamResponse != nil ||
				chunk.BifrostImageGenerationStreamResponse != nil ||
				(chunk.BifrostError != nil && (chunk.BifrostError.ExtraFields.RequestType == schemas.ResponsesStreamRequest || chunk.BifrostError.ExtraFields.RequestType == schemas.ImageGenerationStreamRequest)) {
				includeEventType = true
			}

			// Image generation streams don't use [DONE] marker
			if chunk.BifrostImageGenerationStreamResponse != nil {
				skipDoneMarker = true
			}

			// Allow plugins to modify/filter the chunk via StreamChunkInterceptor
			if interceptor != nil {
				var err error
				chunk, err = interceptor.InterceptChunk(bifrostCtx, httpReq, chunk)
				if err != nil {
					if chunk == nil {
						errorJSON, marshalErr := sonic.Marshal(map[string]string{"error": err.Error()})
						if marshalErr != nil {
							cancel() // Client disconnected or payload invalid
							return
						}
						// Return error event and stopping the streaming
						if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", errorJSON); err != nil {
							cancel() // Client disconnected (write error), cancel upstream stream
							return
						}
						_ = w.Flush()
						cancel()
						return
					}
					// Else add warn log and continue
					logger.Warn("%v", err)
				}
				if chunk == nil {
					// Skip chunk if plugin wants to skip it
					continue
				}
			}

			// Convert response to JSON
			chunkJSON, err := sonic.Marshal(chunk)
			if err != nil {
				logger.Warn("Failed to marshal streaming response: %v", err)
				continue
			}

			// Send as SSE data
			if includeEventType {
				// For responses and image gen API, use OpenAI-compatible format with event line
				eventType := ""
				if chunk.BifrostResponsesStreamResponse != nil {
					eventType = string(chunk.BifrostResponsesStreamResponse.Type)
				} else if chunk.BifrostImageGenerationStreamResponse != nil {
					eventType = string(chunk.BifrostImageGenerationStreamResponse.Type)
				} else if chunk.BifrostError != nil {
					eventType = string(schemas.ResponsesStreamResponseTypeError)
				}
				if eventType != "" {
					if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
						cancel() // Client disconnected (write error), cancel upstream stream
						return
					}
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", chunkJSON); err != nil {
					cancel() // Client disconnected (write error), cancel upstream stream
					return
				}
			} else {
				// For other APIs, use standard format
				if _, err := fmt.Fprintf(w, "data: %s\n\n", chunkJSON); err != nil {
					cancel() // Client disconnected (write error), cancel upstream stream
					return
				}
			}

			// Flush immediately to send the chunk
			if err := w.Flush(); err != nil {
				cancel() // Client disconnected (write error), cancel upstream stream
				return
			}
		}

		if !includeEventType && !skipDoneMarker {
			// Send the [DONE] marker to indicate the end of the stream (only for non-responses/image-gen APIs)
			if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
				logger.Warn("Failed to write SSE [DONE] marker: %v", err)
				cancel() // Client disconnected (write error), cancel upstream stream
				return
			}
		}
		// Note: OpenAI responses API doesn't use [DONE] marker, it ends when the stream closes
		// Stream completed normally, Bifrost handles cleanup internally
		cancel()
	})
}

// validateAudioFile checks if the file size and format are valid
func (h *CompletionHandler) validateAudioFile(fileHeader *multipart.FileHeader) error {
	// Check file size
	if fileHeader.Size > MaxFileSize {
		return fmt.Errorf("file size exceeds maximum limit of %d MB", MaxFileSize/1024/1024)
	}

	// Get file extension
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))

	// Check file extension
	validExtensions := map[string]bool{
		".flac": true,
		".mp3":  true,
		".mp4":  true,
		".mpeg": true,
		".mpga": true,
		".m4a":  true,
		".ogg":  true,
		".wav":  true,
		".webm": true,
	}

	if !validExtensions[ext] {
		return fmt.Errorf("unsupported file format: %s. Supported formats: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm", ext)
	}

	// Open file to check MIME type
	file, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// Read first 512 bytes for MIME type detection
	buffer := make([]byte, 512)
	_, err = file.Read(buffer)
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read file header: %v", err)
	}

	// Check MIME type
	mimeType := http.DetectContentType(buffer)
	validMimeTypes := map[string]bool{
		// Primary MIME types
		AudioMimeMP3:   true, // Covers MP3, MPEG, MPGA
		AudioMimeMP4:   true,
		AudioMimeM4A:   true,
		AudioMimeOGG:   true,
		AudioMimeWAV:   true,
		AudioMimeWEBM:  true,
		AudioMimeFLAC:  true,
		AudioMimeFLAC2: true,

		// Alternative MIME types
		"audio/mpeg3":       true,
		"audio/x-wav":       true,
		"audio/vnd.wave":    true,
		"audio/x-mpeg":      true,
		"audio/x-mpeg3":     true,
		"audio/x-mpg":       true,
		"audio/x-mpegaudio": true,
	}

	if !validMimeTypes[mimeType] {
		return fmt.Errorf("invalid file type: %s. Supported audio formats: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm", mimeType)
	}

	// Reset file pointer for subsequent reads
	_, err = file.Seek(0, 0)
	if err != nil {
		return fmt.Errorf("failed to reset file pointer: %v", err)
	}

	return nil
}

// imageGeneration handles POST /v1/images/generations - Processes image generation requests
func (h *CompletionHandler) imageGeneration(ctx *fasthttp.RequestCtx) {

	var req ImageGenerationHTTPRequest

	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Parse model format provider/model
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format")
		return
	}

	if req.ImageGenerationInput == nil || req.Prompt == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "prompt cannot be empty")
		return
	}
	// Extract extra params
	if req.ImageGenerationParameters == nil {
		req.ImageGenerationParameters = &schemas.ImageGenerationParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), imageParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
		// Continue without extra params
	} else {
		req.ImageGenerationParameters.ExtraParams = extraParams
	}
	// Parse fallbacks
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Create Bifrost request
	bifrostReq := &schemas.BifrostImageGenerationRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.ImageGenerationInput,
		Params:    req.ImageGenerationParameters,
		Fallbacks: fallbacks,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	if bifrostCtx == nil {
		cancel()
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	// Handle streaming image generation
	if req.BifrostParams.Stream != nil && *req.BifrostParams.Stream {
		h.handleStreamingImageGeneration(ctx, bifrostReq, bifrostCtx, cancel)
		return
	}
	defer cancel()

	// Execute request
	resp, bifrostErr := h.client.ImageGenerationRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// handleStreamingImageGeneration handles streaming image generation requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingImageGeneration(ctx *fasthttp.RequestCtx, req *schemas.BifrostImageGenerationRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context
	// Pass the context directly instead of copying to avoid copying lock values

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.ImageGenerationStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// batchCreate handles POST /v1/batches - Create a new batch job
func (h *CompletionHandler) batchCreate(ctx *fasthttp.RequestCtx) {
	var req BatchCreateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Parse provider from model string
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format or provider must be specified")
		return
	}

	// Validate that at least one of InputFileID or Requests is provided
	if req.InputFileID == "" && len(req.Requests) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "either input_file_id or requests is required")
		return
	}

	// Extract extra params
	extraParams, err := extractExtraParams(ctx.PostBody(), batchCreateParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	}

	var model *string
	if modelName != "" {
		model = schemas.Ptr(modelName)
	}

	// Build Bifrost batch create request
	bifrostBatchReq := &schemas.BifrostBatchCreateRequest{
		Provider:         schemas.ModelProvider(provider),
		Model:            model,
		InputFileID:      req.InputFileID,
		Requests:         req.Requests,
		Endpoint:         schemas.BatchEndpoint(req.Endpoint),
		CompletionWindow: req.CompletionWindow,
		Metadata:         req.Metadata,
		ExtraParams:      extraParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchCreateRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// batchList handles GET /v1/batches - List batch jobs
func (h *CompletionHandler) batchList(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Parse limit parameter
	limit := 0
	if limitStr := ctx.QueryArgs().Peek("limit"); len(limitStr) > 0 {
		if n, err := strconv.Atoi(string(limitStr)); err == nil && n > 0 {
			limit = n
		}
	}

	// Parse pagination parameters
	var after, before *string
	if afterStr := ctx.QueryArgs().Peek("after"); len(afterStr) > 0 {
		s := string(afterStr)
		after = &s
	}
	if beforeStr := ctx.QueryArgs().Peek("before"); len(beforeStr) > 0 {
		s := string(beforeStr)
		before = &s
	}

	// Build Bifrost batch list request
	bifrostBatchReq := &schemas.BifrostBatchListRequest{
		Provider: schemas.ModelProvider(provider),
		Limit:    limit,
		After:    after,
		BeforeID: before,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchListRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// batchRetrieve handles GET /v1/batches/{batch_id} - Retrieve a batch job
func (h *CompletionHandler) batchRetrieve(ctx *fasthttp.RequestCtx) {
	// Get batch ID from URL parameter
	batchID, ok := ctx.UserValue("batch_id").(string)
	if !ok || batchID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "batch_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost batch retrieve request
	bifrostBatchReq := &schemas.BifrostBatchRetrieveRequest{
		Provider: schemas.ModelProvider(provider),
		BatchID:  batchID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchRetrieveRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// batchCancel handles POST /v1/batches/{batch_id}/cancel - Cancel a batch job
func (h *CompletionHandler) batchCancel(ctx *fasthttp.RequestCtx) {
	// Get batch ID from URL parameter
	batchID := ctx.UserValue("batch_id").(string)
	if batchID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "batch_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost batch cancel request
	bifrostBatchReq := &schemas.BifrostBatchCancelRequest{
		Provider: schemas.ModelProvider(provider),
		BatchID:  batchID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchCancelRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// batchResults handles GET /v1/batches/{batch_id}/results - Get batch results
func (h *CompletionHandler) batchResults(ctx *fasthttp.RequestCtx) {
	// Get batch ID from URL parameter
	batchID := ctx.UserValue("batch_id").(string)
	if batchID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "batch_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost batch results request
	bifrostBatchReq := &schemas.BifrostBatchResultsRequest{
		Provider: schemas.ModelProvider(provider),
		BatchID:  batchID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchResultsRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// fileUpload handles POST /v1/files - Upload a file
func (h *CompletionHandler) fileUpload(ctx *fasthttp.RequestCtx) {
	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to parse multipart form: %v", err))
		return
	}

	// Get provider from query parameters or header
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		// Try to get from header (for OpenAI SDK compatibility)
		provider = string(ctx.Request.Header.Peek("x-model-provider"))
		// Try to get from extra_body
		if provider == "" && len(form.Value["provider"]) > 0 {
			provider = string(form.Value["provider"][0])
		}
	}

	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter or x-model-provider header is required")
		return
	}

	// Extract purpose (required)
	purposeValues := form.Value["purpose"]
	if len(purposeValues) == 0 || purposeValues[0] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "purpose is required")
		return
	}
	purpose := purposeValues[0]

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "file is required")
		return
	}

	fileHeader := fileHeaders[0]

	// Open and read the file
	file, err := fileHeader.Open()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to open uploaded file: %v", err))
		return
	}
	defer file.Close()

	// Read file data
	fileData, err := io.ReadAll(file)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to read uploaded file: %v", err))
		return
	}

	// Build Bifrost file upload request
	bifrostFileReq := &schemas.BifrostFileUploadRequest{
		Provider: schemas.ModelProvider(provider),
		File:     fileData,
		Filename: fileHeader.Filename,
		Purpose:  schemas.FilePurpose(purpose),
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileUploadRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// fileList handles GET /v1/files - List files
func (h *CompletionHandler) fileList(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("x-model-provider"))
	if provider == "" {
		// Try to get from header
		provider = string(ctx.Request.Header.Peek("x-model-provider"))
		if provider == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "x-model-provider query parameter or x-model-provider header is required")
			return
		}
	}

	// Parse optional parameters
	purpose := string(ctx.QueryArgs().Peek("purpose"))

	limit := 0
	if limitStr := ctx.QueryArgs().Peek("limit"); len(limitStr) > 0 {
		if n, err := strconv.Atoi(string(limitStr)); err == nil && n > 0 {
			limit = n
		}
	}

	var after, order *string
	if afterStr := ctx.QueryArgs().Peek("after"); len(afterStr) > 0 {
		s := string(afterStr)
		after = &s
	}
	if orderStr := ctx.QueryArgs().Peek("order"); len(orderStr) > 0 {
		s := string(orderStr)
		order = &s
	}

	// Build Bifrost file list request
	bifrostFileReq := &schemas.BifrostFileListRequest{
		Provider: schemas.ModelProvider(provider),
		Purpose:  schemas.FilePurpose(purpose),
		Limit:    limit,
		After:    after,
		Order:    order,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileListRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// fileRetrieve handles GET /v1/files/{file_id} - Retrieve file metadata
func (h *CompletionHandler) fileRetrieve(ctx *fasthttp.RequestCtx) {
	// Get file ID from URL parameter
	fileID := ctx.UserValue("file_id").(string)
	if fileID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost file retrieve request
	bifrostFileReq := &schemas.BifrostFileRetrieveRequest{
		Provider: schemas.ModelProvider(provider),
		FileID:   fileID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileRetrieveRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// fileDelete handles DELETE /v1/files/{file_id} - Delete a file
func (h *CompletionHandler) fileDelete(ctx *fasthttp.RequestCtx) {
	// Get file ID from URL parameter
	fileID := ctx.UserValue("file_id").(string)
	if fileID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost file delete request
	bifrostFileReq := &schemas.BifrostFileDeleteRequest{
		Provider: schemas.ModelProvider(provider),
		FileID:   fileID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileDeleteRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// fileContent handles GET /v1/files/{file_id}/content - Download file content
func (h *CompletionHandler) fileContent(ctx *fasthttp.RequestCtx) {
	// Get file ID from URL parameter
	fileID := ctx.UserValue("file_id").(string)
	if fileID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost file content request
	bifrostFileReq := &schemas.BifrostFileContentRequest{
		Provider: schemas.ModelProvider(provider),
		FileID:   fileID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileContentRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Set appropriate headers for file download
	ctx.Response.Header.Set("Content-Type", resp.ContentType)
	ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(resp.Content)))
	ctx.Response.SetBody(resp.Content)
}

// containerCreate handles POST /v1/containers - Create a new container
func (h *CompletionHandler) containerCreate(ctx *fasthttp.RequestCtx) {
	var req ContainerCreateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	// Validate required fields
	if req.Provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider is required")
		return
	}

	if req.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "name is required")
		return
	}

	// Extract extra params
	extraParams, err := extractExtraParams(ctx.PostBody(), containerCreateParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	}

	// Build Bifrost container create request
	bifrostContainerReq := &schemas.BifrostContainerCreateRequest{
		Provider:     schemas.ModelProvider(req.Provider),
		Name:         req.Name,
		ExpiresAfter: req.ExpiresAfter,
		FileIDs:      req.FileIDs,
		MemoryLimit:  req.MemoryLimit,
		Metadata:     req.Metadata,
		ExtraParams:  extraParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerCreateRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// containerList handles GET /v1/containers - List containers
func (h *CompletionHandler) containerList(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Parse limit parameter
	limit := 0
	if limitStr := ctx.QueryArgs().Peek("limit"); len(limitStr) > 0 {
		if n, err := strconv.Atoi(string(limitStr)); err == nil && n > 0 {
			limit = n
		}
	}

	// Parse pagination parameters
	var after, order *string
	if afterStr := ctx.QueryArgs().Peek("after"); len(afterStr) > 0 {
		after = bifrost.Ptr(string(afterStr))
	}
	if orderStr := ctx.QueryArgs().Peek("order"); len(orderStr) > 0 {
		order = bifrost.Ptr(string(orderStr))
	}

	// Build Bifrost container list request
	bifrostContainerReq := &schemas.BifrostContainerListRequest{
		Provider: schemas.ModelProvider(provider),
		Limit:    limit,
		After:    after,
		Order:    order,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerListRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// containerRetrieve handles GET /v1/containers/{container_id} - Retrieve a container
func (h *CompletionHandler) containerRetrieve(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container retrieve request
	bifrostContainerReq := &schemas.BifrostContainerRetrieveRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerRetrieveRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// containerDelete handles DELETE /v1/containers/{container_id} - Delete a container
func (h *CompletionHandler) containerDelete(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container delete request
	bifrostContainerReq := &schemas.BifrostContainerDeleteRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerDeleteRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// =============================================================================
// CONTAINER FILES HANDLERS
// =============================================================================

// containerFileCreate handles POST /v1/containers/{container_id}/files - Create a file in a container
func (h *CompletionHandler) containerFileCreate(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container file create request
	bifrostContainerFileReq := &schemas.BifrostContainerFileCreateRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
	}

	// Check if this is a multipart request or JSON request
	contentType := string(ctx.Request.Header.ContentType())
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// Handle multipart file upload
		fileHeader, err := ctx.FormFile("file")
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "file is required for multipart upload")
			return
		}
		file, err := fileHeader.Open()
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to open uploaded file")
			return
		}
		defer file.Close()

		fileContent, err := io.ReadAll(file)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read uploaded file")
			return
		}
		bifrostContainerFileReq.File = fileContent
		// Extract optional file_path from multipart form
		if filePath := ctx.FormValue("file_path"); len(filePath) > 0 {
			bifrostContainerFileReq.Path = bifrost.Ptr(string(filePath))
		}
	} else {
		// Handle JSON request with file_id
		var reqBody struct {
			FileID   string `json:"file_id"`
			FilePath string `json:"file_path,omitempty"`
		}
		if err := sonic.Unmarshal(ctx.PostBody(), &reqBody); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON body")
			return
		}
		if reqBody.FileID == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "file_id is required in JSON body")
			return
		}
		bifrostContainerFileReq.FileID = bifrost.Ptr(reqBody.FileID)
		if reqBody.FilePath != "" {
			bifrostContainerFileReq.Path = bifrost.Ptr(reqBody.FilePath)
		}
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileCreateRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// containerFileList handles GET /v1/containers/{container_id}/files - List files in a container
func (h *CompletionHandler) containerFileList(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container file list request
	bifrostContainerFileReq := &schemas.BifrostContainerFileListRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
	}

	// Parse pagination parameters
	if limit := ctx.QueryArgs().Peek("limit"); len(limit) > 0 {
		if limitInt, err := strconv.Atoi(string(limit)); err == nil && limitInt > 0 {
			bifrostContainerFileReq.Limit = limitInt
		}
	}
	if after := string(ctx.QueryArgs().Peek("after")); after != "" {
		bifrostContainerFileReq.After = bifrost.Ptr(after)
	}
	if order := string(ctx.QueryArgs().Peek("order")); order != "" {
		bifrostContainerFileReq.Order = bifrost.Ptr(order)
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileListRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// containerFileRetrieve handles GET /v1/containers/{container_id}/files/{file_id} - Retrieve a file from a container
func (h *CompletionHandler) containerFileRetrieve(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get file ID from URL parameter
	fileID, ok := ctx.UserValue("file_id").(string)
	if !ok || fileID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container file retrieve request
	bifrostContainerFileReq := &schemas.BifrostContainerFileRetrieveRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
		FileID:      fileID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileRetrieveRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}

// containerFileContent handles GET /v1/containers/{container_id}/files/{file_id}/content - Retrieve file content from a container
func (h *CompletionHandler) containerFileContent(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get file ID from URL parameter
	fileID, ok := ctx.UserValue("file_id").(string)
	if !ok || fileID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container file content request
	bifrostContainerFileReq := &schemas.BifrostContainerFileContentRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
		FileID:      fileID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileContentRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Send binary content with appropriate content type
	ctx.SetContentType(resp.ContentType)
	ctx.SetBody(resp.Content)
}

// containerFileDelete handles DELETE /v1/containers/{container_id}/files/{file_id} - Delete a file from a container
func (h *CompletionHandler) containerFileDelete(ctx *fasthttp.RequestCtx) {
	// Get container ID from URL parameter
	containerID, ok := ctx.UserValue("container_id").(string)
	if !ok || containerID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "container_id is required")
		return
	}

	// Get file ID from URL parameter
	fileID, ok := ctx.UserValue("file_id").(string)
	if !ok || fileID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "file_id is required")
		return
	}

	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost container file delete request
	bifrostContainerFileReq := &schemas.BifrostContainerFileDeleteRequest{
		Provider:    schemas.ModelProvider(provider),
		ContainerID: containerID,
		FileID:      fileID,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys(), h.config.GetHeaderFilterConfig())
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileDeleteRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	SendJSON(ctx, resp)
}
