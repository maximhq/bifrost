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
	logger       schemas.Logger
	config       *lib.Config
}

// NewInferenceHandler creates a new completion handler instance
func NewInferenceHandler(client *bifrost.Bifrost, config *lib.Config, logger schemas.Logger) *CompletionHandler {
	return &CompletionHandler{
		client:       client,
		handlerStore: config,
		config:       config,
		logger:       logger,
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
	"reasoning_effort":      true,
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

// ResponsesRequestInput is a union of string and array of responses messages
type ResponsesRequestInput struct {
	ResponsesRequestInputStr   *string
	ResponsesRequestInputArray []schemas.ResponsesMessage
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

// Helper functions

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
func extractExtraParams(data []byte, knownFields map[string]bool) (map[string]interface{}, error) {
	// Parse JSON to extract unknown fields
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawData); err != nil {
		return nil, err
	}

	// Extract unknown fields
	extraParams := make(map[string]interface{})
	for key, value := range rawData {
		if !knownFields[key] {
			var v interface{}
			if err := json.Unmarshal(value, &v); err != nil {
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
func (h *CompletionHandler) RegisterRoutes(r *router.Router, middlewares ...lib.BifrostHTTPMiddleware) {
	// Completion endpoints
	r.POST("/v1/completions", lib.ChainMiddlewares(h.textCompletion, middlewares...))
	r.POST("/v1/chat/completions", lib.ChainMiddlewares(h.chatCompletion, middlewares...))
	r.POST("/v1/responses", lib.ChainMiddlewares(h.responses, middlewares...))
	r.POST("/v1/embeddings", lib.ChainMiddlewares(h.embeddings, middlewares...))
	r.POST("/v1/audio/speech", lib.ChainMiddlewares(h.speech, middlewares...))
	r.POST("/v1/audio/transcriptions", lib.ChainMiddlewares(h.transcription, middlewares...))
}

// textCompletion handles POST /v1/completions - Process text completion requests
func (h *CompletionHandler) textCompletion(ctx *fasthttp.RequestCtx) {
	var req TextRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}
	// Create BifrostTextCompletionRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format", h.logger)
		return
	}
	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
		return
	}
	if req.Prompt == nil || (req.Prompt.PromptStr == nil && req.Prompt.PromptArray == nil) {
		SendError(ctx, fasthttp.StatusBadRequest, "prompt is required for text completion", h.logger)
		return
	}
	// Extract extra params
	if req.TextCompletionParameters == nil {
		req.TextCompletionParameters = &schemas.TextCompletionParameters{}
	}
	extraParams, err := extractExtraParams(ctx.PostBody(), textParamsKnownFields)
	if err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to extract extra params: %v", err))
	} else {
		req.TextCompletionParameters.ExtraParams = extraParams
	}
	// Adding fallback context
	if h.config.ClientConfig.EnableLiteLLMFallbacks {
		ctx.SetUserValue(schemas.BifrostContextKey("x-litellm-fallback"), "true")
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
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}
	if req.Stream != nil && *req.Stream {
		h.handleStreamingTextCompletion(ctx, bifrostTextReq, bifrostCtx)
		return
	}
	resp, bifrostErr := h.client.TextCompletionRequest(*bifrostCtx, bifrostTextReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// chatCompletion handles POST /v1/chat/completions - Process chat completion requests
func (h *CompletionHandler) chatCompletion(ctx *fasthttp.RequestCtx) {
	var req ChatRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Create BifrostChatRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format", h.logger)
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
		return
	}

	if len(req.Messages) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "Messages is required for chat completion", h.logger)
		return
	}

	// Extract extra params
	if req.ChatParameters == nil {
		req.ChatParameters = &schemas.ChatParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), chatParamsKnownFields)
	if err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to extract extra params: %v", err))
	} else {
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
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	if req.Stream != nil && *req.Stream {
		h.handleStreamingChatCompletion(ctx, bifrostChatReq, bifrostCtx)
		return
	}

	resp, bifrostErr := h.client.ChatCompletionRequest(*bifrostCtx, bifrostChatReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// responses handles POST /v1/responses - Process responses requests
func (h *CompletionHandler) responses(ctx *fasthttp.RequestCtx) {
	var req ResponsesRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Create BifrostResponsesRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format", h.logger)
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
		return
	}

	if len(req.Input.ResponsesRequestInputArray) == 0 && req.Input.ResponsesRequestInputStr == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Input is required for responses", h.logger)
		return
	}

	// Extract extra params
	if req.ResponsesParameters == nil {
		req.ResponsesParameters = &schemas.ResponsesParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), responsesParamsKnownFields)
	if err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to extract extra params: %v", err))
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
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	if req.Stream != nil && *req.Stream {
		h.handleStreamingResponses(ctx, bifrostResponsesReq, bifrostCtx)
		return
	}

	resp, bifrostErr := h.client.ResponsesRequest(*bifrostCtx, bifrostResponsesReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// embeddings handles POST /v1/embeddings - Process embeddings requests
func (h *CompletionHandler) embeddings(ctx *fasthttp.RequestCtx) {
	var req EmbeddingRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Create BifrostEmbeddingRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format", h.logger)
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
		return
	}

	if req.Input == nil || (req.Input.Text == nil && req.Input.Texts == nil && req.Input.Embedding == nil && req.Input.Embeddings == nil) {
		SendError(ctx, fasthttp.StatusBadRequest, "Input is required for embeddings", h.logger)
		return
	}

	// Extract extra params
	if req.EmbeddingParameters == nil {
		req.EmbeddingParameters = &schemas.EmbeddingParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), embeddingParamsKnownFields)
	if err != nil {
		h.logger.Warn(fmt.Sprintf("Failed to extract extra params: %v", err))
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
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	resp, bifrostErr := h.client.EmbeddingRequest(*bifrostCtx, bifrostEmbeddingReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// speech handles POST /v1/audio/speech - Process speech completion requests
func (h *CompletionHandler) speech(ctx *fasthttp.RequestCtx) {
	var req SpeechRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Create BifrostSpeechRequest directly using segregated structure
	provider, modelName := schemas.ParseModelString(req.Model, "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format", h.logger)
		return
	}

	// Parse fallbacks using helper function
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
		return
	}

	if req.Input == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Input is required for speech completion", h.logger)
		return
	}

	if req.VoiceConfig == nil || (req.VoiceConfig.Voice == nil && len(req.VoiceConfig.MultiVoiceConfig) == 0) {
		SendError(ctx, fasthttp.StatusBadRequest, "Voice is required for speech completion", h.logger)
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
		h.logger.Warn(fmt.Sprintf("Failed to extract extra params: %v", err))
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
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	if req.StreamFormat != nil && *req.StreamFormat == "sse" {
		h.handleStreamingSpeech(ctx, bifrostSpeechReq, bifrostCtx)
		return
	}

	resp, bifrostErr := h.client.SpeechRequest(*bifrostCtx, bifrostSpeechReq)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	if resp.Audio == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Speech response is missing audio data", h.logger)
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
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to parse multipart form: %v", err), h.logger)
		return
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Model is required", h.logger)
		return
	}

	provider, modelName := schemas.ParseModelString(modelValues[0], "")
	if provider == "" || modelName == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "model should be in provider/model format", h.logger)
		return
	}

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "File is required", h.logger)
		return
	}

	fileHeader := fileHeaders[0]

	// // Validate file size and format
	// if err := h.validateAudioFile(fileHeader); err != nil {
	// 	SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
	// 	return
	// }

	file, err := fileHeader.Open()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to open uploaded file: %v", err), h.logger)
		return
	}
	defer file.Close()

	// Read file data
	fileData := make([]byte, fileHeader.Size)
	if _, err := file.Read(fileData); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to read uploaded file: %v", err), h.logger)
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
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream := streamValues[0]
		if stream == "true" {
			h.handleStreamingTranscriptionRequest(ctx, bifrostTranscriptionReq, bifrostCtx)
			return
		}
	}

	// Make transcription request
	resp, bifrostErr := h.client.TranscriptionRequest(*bifrostCtx, bifrostTranscriptionReq)

	// Handle response
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// handleStreamingTextCompletion handles streaming text completion requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingTextCompletion(ctx *fasthttp.RequestCtx, req *schemas.BifrostTextCompletionRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.TextCompletionStreamRequest(*bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, getStream)
}

// handleStreamingChatCompletion handles streaming chat completion requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingChatCompletion(ctx *fasthttp.RequestCtx, req *schemas.BifrostChatRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.ChatCompletionStreamRequest(*bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, getStream)
}

// handleStreamingResponses handles streaming responses requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingResponses(ctx *fasthttp.RequestCtx, req *schemas.BifrostResponsesRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.ResponsesStreamRequest(*bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, getStream)
}

// handleStreamingSpeech handles streaming speech requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingSpeech(ctx *fasthttp.RequestCtx, req *schemas.BifrostSpeechRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.SpeechStreamRequest(*bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, getStream)
}

// handleStreamingTranscriptionRequest handles streaming transcription requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingTranscriptionRequest(ctx *fasthttp.RequestCtx, req *schemas.BifrostTranscriptionRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.TranscriptionStreamRequest(*bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, getStream)
}

// handleStreamingResponse is a generic function to handle streaming responses using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingResponse(ctx *fasthttp.RequestCtx, getStream func() (chan *schemas.BifrostStream, *schemas.BifrostError)) {
	// Set SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")
	ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")

	// Get the streaming channel
	stream, bifrostErr := getStream()
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	var requestType schemas.RequestType

	// Use streaming response writer
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer w.Flush()

		// Process streaming responses
		for chunk := range stream {
			if chunk == nil {
				continue
			}

			includeEventType := false
			if chunk.BifrostResponsesStreamResponse != nil ||
				(chunk.BifrostError != nil && chunk.BifrostError.ExtraFields.RequestType == schemas.ResponsesStreamRequest) {
				includeEventType = true
			}

			// Convert response to JSON
			chunkJSON, err := sonic.Marshal(chunk)
			if err != nil {
				h.logger.Warn(fmt.Sprintf("Failed to marshal streaming response: %v", err))
				continue
			}

			// Send as SSE data
			if includeEventType {
				// For responses API, use OpenAI-compatible format with event line
				eventType := ""
				if chunk.BifrostResponsesStreamResponse != nil {
					eventType = string(chunk.BifrostResponsesStreamResponse.Type)
				} else if chunk.BifrostError != nil {
					eventType = string(schemas.ResponsesStreamResponseTypeError)
				}

				if eventType != "" {
					if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
						h.logger.Warn(fmt.Sprintf("Failed to write SSE event: %v", err))
						break
					}
				}

				if _, err := fmt.Fprintf(w, "data: %s\n\n", chunkJSON); err != nil {
					h.logger.Warn(fmt.Sprintf("Failed to write SSE data: %v", err))
					break
				}
			} else {
				// For other APIs, use standard format
				if _, err := fmt.Fprintf(w, "data: %s\n\n", chunkJSON); err != nil {
					h.logger.Warn(fmt.Sprintf("Failed to write SSE data: %v", err))
					break
				}
			}

			// Flush immediately to send the chunk
			if err := w.Flush(); err != nil {
				h.logger.Warn(fmt.Sprintf("Failed to flush SSE data: %v", err))
				break
			}
		}

		if requestType != schemas.ResponsesStreamRequest {
			// Send the [DONE] marker to indicate the end of the stream (only for non-responses APIs)
			if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
				h.logger.Warn(fmt.Sprintf("Failed to write SSE done marker: %v", err))
			}
		}
		// Note: OpenAI responses API doesn't use [DONE] marker, it ends when the stream closes
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
