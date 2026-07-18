// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains completion request handlers for text and chat completions.
package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// forwardProviderHeaders forwards provider response headers to the HTTP response.
func forwardProviderHeaders(ctx *fasthttp.RequestCtx, headers map[string]string) {
	for key, value := range headers {
		ctx.Response.Header.Set(key, value)
	}
}

// forwardProviderHeadersFromContext extracts provider response headers from the bifrost context
// and forwards them to the HTTP response. This ensures error responses also include provider headers.
func forwardProviderHeadersFromContext(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) {
	if headers, ok := bifrostCtx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string); ok {
		forwardProviderHeaders(ctx, headers)
	}
}

// CompletionHandler manages HTTP requests for completion operations
type CompletionHandler struct {
	client *bifrost.Bifrost
	config *lib.Config
}

// NewInferenceHandler creates a new completion handler instance
func NewInferenceHandler(client *bifrost.Bifrost, config *lib.Config) *CompletionHandler {
	return &CompletionHandler{
		client: client,
		config: config,
	}
}

// resolveModelAndProvider parses the model string. An empty provider is allowed here —
// the ModelCatalogResolver built-in PreRequestHook plugin fills it in as the last routing
// layer when no other routing plugin (governance routing rules, governance VK LB, enterprise
// LB) picked one. The empty-provider validation in handleRequest/handleStreamRequest catches
// the case where catalog resolution also fails.
func resolveModelAndProvider(_ *fasthttp.RequestCtx, _ *lib.Config, model string) (schemas.ModelProvider, string, error) {
	provider, modelName := schemas.ParseModelString(model, "")
	return provider, modelName, nil
}

// prepareRequest is the generic entry point for all JSON-body prepare functions.
// It unmarshals the request body into T, resolves model+provider, parses
// fallbacks, and extracts extra params. Type-specific validation is left to
// the caller.
func prepareRequest[T baseRequest](ctx *fasthttp.RequestCtx, config *lib.Config, knownFields map[string]bool) (*T, *requestBase, error) {
	req := new(T)
	if err := sonic.Unmarshal(ctx.PostBody(), req); err != nil {
		return nil, nil, fmt.Errorf("Invalid request payload")
	}
	provider, modelName, err := resolveModelAndProvider(ctx, config, (*req).getModel())
	if err != nil {
		return nil, nil, err
	}
	fallbacks, err := parseFallbacks((*req).getFallbacks())
	if err != nil {
		return nil, nil, err
	}
	var extraParams map[string]any
	if knownFields != nil {
		ep, epErr := extractExtraParams(ctx.PostBody(), knownFields)
		if epErr != nil {
			logger.Warn("Failed to extract extra params: %v", epErr)
		} else {
			extraParams = ep
		}
	}
	return req, &requestBase{
		Provider:    provider,
		ModelName:   modelName,
		Fallbacks:   fallbacks,
		ExtraParams: extraParams,
	}, nil
}

// Known fields for CompletionRequest
var textParamsKnownFields = map[string]bool{
	"prompt":            true,
	"model":             true,
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
	"model":                  true,
	"messages":               true,
	"fallbacks":              true,
	"stream":                 true,
	"frequency_penalty":      true,
	"logit_bias":             true,
	"logprobs":               true,
	"max_completion_tokens":  true,
	"metadata":               true,
	"modalities":             true,
	"parallel_tool_calls":    true,
	"presence_penalty":       true,
	"prompt_cache_key":       true,
	"prompt_cache_retention": true,
	"reasoning":              true,
	"reasoning_effort":       true,
	"reasoning_max_tokens":   true,
	"response_format":        true,
	"safety_identifier":      true,
	"service_tier":           true,
	"stream_options":         true,
	"store":                  true,
	"temperature":            true,
	"tool_choice":            true,
	"tools":                  true,
	"truncation":             true,
	"user":                   true,
	"verbosity":              true,
}

var responsesParamsKnownFields = map[string]bool{
	"model":                  true,
	"input":                  true,
	"fallbacks":              true,
	"stream":                 true,
	"background":             true,
	"conversation":           true,
	"include":                true,
	"instructions":           true,
	"max_output_tokens":      true,
	"max_tool_calls":         true,
	"metadata":               true,
	"parallel_tool_calls":    true,
	"previous_response_id":   true,
	"prompt_cache_key":       true,
	"prompt_cache_retention": true,
	"reasoning":              true,
	"safety_identifier":      true,
	"service_tier":           true,
	"stream_options":         true,
	"store":                  true,
	"temperature":            true,
	"text":                   true,
	"top_logprobs":           true,
	"top_p":                  true,
	"tool_choice":            true,
	"tools":                  true,
	"truncation":             true,
}

var compactionParamsKnownFields = map[string]bool{
	"model":                  true,
	"input":                  true,
	"fallbacks":              true,
	"instructions":           true,
	"previous_response_id":   true,
	"prompt_cache_key":       true,
	"prompt_cache_retention": true,
	"service_tier":           true,
}

var embeddingParamsKnownFields = map[string]bool{
	"model":           true,
	"input":           true,
	"fallbacks":       true,
	"encoding_format": true,
	"dimensions":      true,
}

var rerankParamsKnownFields = map[string]bool{
	"model":              true,
	"query":              true,
	"documents":          true,
	"fallbacks":          true,
	"top_n":              true,
	"max_tokens_per_doc": true,
	"priority":           true,
	"return_documents":   true,
}

var ocrParamsKnownFields = map[string]bool{
	"model":                      true,
	"id":                         true,
	"document":                   true,
	"fallbacks":                  true,
	"include_image_base64":       true,
	"pages":                      true,
	"image_limit":                true,
	"image_min_size":             true,
	"table_format":               true,
	"extract_header":             true,
	"extract_footer":             true,
	"bbox_annotation_format":     true,
	"document_annotation_format": true,
	"document_annotation_prompt": true,
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

// imageGenerationParamsKnownFields contains known fields for image generation requests
// Based on ImageGenerationInput and ImageGenerationParameters structs
var imageGenerationParamsKnownFields = map[string]bool{
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
	"aspect_ratio":        true,
	"input_images":        true,
}

// imageEditParamsKnownFields contains known fields for image edit requests
// Based on ImageEditInput and ImageEditParameters structs
var imageEditParamsKnownFields = map[string]bool{
	"model":               true,
	"prompt":              true,
	"fallbacks":           true,
	"image":               true,
	"image[]":             true,
	"mask":                true,
	"type":                true,
	"background":          true,
	"input_fidelity":      true,
	"n":                   true,
	"output_compression":  true,
	"output_format":       true,
	"partial_images":      true,
	"quality":             true,
	"response_format":     true,
	"size":                true,
	"user":                true,
	"negative_prompt":     true,
	"seed":                true,
	"num_inference_steps": true,
	"aspect_ratio":        true,
	"stream":              true,
}

// imageVariationParamsKnownFields contains known fields for image variation requests
// Based on ImageVariationInput and ImageVariationParameters structs
var imageVariationParamsKnownFields = map[string]bool{
	"model":           true,
	"fallbacks":       true,
	"image":           true,
	"image[]":         true,
	"n":               true,
	"response_format": true,
	"size":            true,
	"user":            true,
}

// videoGenerationParamsKnownFields contains known fields for video generation requests
// Based on VideoGenerationInput and VideoGenerationParameters structs
var videoGenerationParamsKnownFields = map[string]bool{
	"model":           true,
	"prompt":          true,
	"input_reference": true,
	"seconds":         true,
	"size":            true,
	"negative_prompt": true,
	"seed":            true,
	"video_uri":       true,
	"audio":           true,
	"fallbacks":       true,
}

var videoRemixParamsKnownFields = map[string]bool{
	"prompt":    true,
	"fallbacks": true,
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

var batchCreateParamsKnownFields = map[string]bool{
	"model":             true,
	"input_file_id":     true,
	"input_blob":        true,
	"output_folder":     true,
	"display_name":      true,
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

func (b BifrostParams) getModel() string       { return b.Model }
func (b BifrostParams) getFallbacks() []string { return b.Fallbacks }

// baseRequest is satisfied by any type that embeds BifrostParams.
type baseRequest interface {
	getModel() string
	getFallbacks() []string
}

// requestBase holds the fields common to every JSON-body prepare function
// so that each type-specific prepareXRequest only handles validation.
type requestBase struct {
	Provider    schemas.ModelProvider
	ModelName   string
	Fallbacks   []schemas.Fallback
	ExtraParams map[string]any
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

type ImageEditHTTPRequest struct {
	*schemas.ImageEditInput
	*schemas.ImageEditParameters
	BifrostParams
}

type ImageVariationHTTPRequest struct {
	*schemas.ImageVariationInput
	*schemas.ImageVariationParameters
	BifrostParams
}

type VideoGenerationHTTPRequest struct {
	*schemas.VideoGenerationInput
	*schemas.VideoGenerationParameters
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

// CompactionHTTPRequest is a bifrost compaction request (subset of responses fields)
type CompactionHTTPRequest struct {
	Input                ResponsesRequestInput       `json:"input"`
	Instructions         *string                     `json:"instructions,omitempty"`
	PreviousResponseID   *string                     `json:"previous_response_id,omitempty"`
	PromptCacheKey       *string                     `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string                     `json:"prompt_cache_retention,omitempty"`
	ServiceTier          *schemas.BifrostServiceTier `json:"service_tier,omitempty"`
	BifrostParams
}

// EmbeddingRequest is a bifrost embedding request
type EmbeddingRequest struct {
	Input *schemas.EmbeddingInput `json:"input"`
	BifrostParams
	*schemas.EmbeddingParameters
}

// RerankRequest is a bifrost rerank request
type RerankRequest struct {
	Query     string                   `json:"query"`
	Documents []schemas.RerankDocument `json:"documents"`
	BifrostParams
	*schemas.RerankParameters
}

// OCRHandlerRequest is a bifrost OCR request
type OCRHandlerRequest struct {
	ID       *string             `json:"id,omitempty"`
	Document schemas.OCRDocument `json:"document"`
	BifrostParams
	*schemas.OCRParameters
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

type VideoGenerationRequest struct {
	*schemas.VideoGenerationInput
	BifrostParams
	*schemas.VideoGenerationParameters
}
type VideoRemixRequest struct {
	*schemas.VideoGenerationInput
	BifrostParams
	ExtraParams map[string]any `json:"extra_params,omitempty"`
}

// BatchCreateRequest is a bifrost batch create request
type BatchCreateRequest struct {
	Model            string                     `json:"model"`                       // Model in "provider/model" format
	InputFileID      string                     `json:"input_file_id,omitempty"`     // OpenAI-style file ID
	Requests         []schemas.BatchRequestItem `json:"requests,omitempty"`          // Anthropic-style inline requests
	InputBlob        *string                    `json:"input_blob,omitempty"`        // Azure-style blob storage input
	OutputFolder     *schemas.BatchOutputFolder `json:"output_folder,omitempty"`     // Azure-style output destination
	DisplayName      *string                    `json:"display_name,omitempty"`      // Human-readable job name (e.g. Vertex displayName)
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

// enableRawRequestResponseForContainer sets per-request overrides to always capture and
// send back raw request/response for container operations. Container operations don't have
// model-specific content, so raw data is useful for debugging and should be enabled by default.
func enableRawRequestResponseForContainer(bifrostCtx *schemas.BifrostContext) {
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeyStoreRawRequestResponse, true)
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

func effectiveStream(bodyStream *bool) bool {
	if bodyStream != nil {
		return *bodyStream
	}
	return false
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

// PathToTypeMapping maps exact paths to request types (only for non-parameterized paths)
// Parameterized paths are set per-route in RegisterRoutes
var PathToTypeMapping = map[string]schemas.RequestType{
	"/v1/completions":            schemas.TextCompletionRequest,
	"/v1/chat/completions":       schemas.ChatCompletionRequest,
	"/v1/responses":              schemas.ResponsesRequest,
	"/v1/embeddings":             schemas.EmbeddingRequest,
	"/v1/rerank":                 schemas.RerankRequest,
	"/v1/ocr":                    schemas.OCRRequest,
	"/v1/audio/speech":           schemas.SpeechRequest,
	"/v1/audio/transcriptions":   schemas.TranscriptionRequest,
	"/v1/images/generations":     schemas.ImageGenerationRequest,
	"/v1/responses/input_tokens": schemas.CountTokensRequest,
	"/v1/responses/compact":      schemas.CompactionRequest,
	"/v1/images/edits":           schemas.ImageEditRequest,
	"/v1/images/variations":      schemas.ImageVariationRequest,
	"/v1/models":                 schemas.ListModelsRequest,
}

// createRequestTypeMiddleware creates a middleware that sets the request type for a specific route
func createRequestTypeMiddleware(requestType schemas.RequestType) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.SetUserValue(schemas.BifrostContextKeyHTTPRequestType, requestType)
			next(ctx)
		}
	}
}

// RegisterRequestTypeMiddleware handles exact path matching for non-parameterized routes
func RegisterRequestTypeMiddleware(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		path := string(ctx.Path())
		if requestType, ok := PathToTypeMapping[path]; ok {
			ctx.SetUserValue(schemas.BifrostContextKeyHTTPRequestType, requestType)
		}
		next(ctx)
	}
}

// RegisterRoutes registers all completion-related routes
func (h *CompletionHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Base middlewares for all routes
	baseMiddlewares := append([]schemas.BifrostHTTPMiddleware{RegisterRequestTypeMiddleware}, middlewares...)

	// Model endpoints
	r.GET("/v1/models", lib.ChainMiddlewares(h.listModels, baseMiddlewares...))

	// Completion endpoints (non-parameterized)
	r.POST("/v1/completions", lib.ChainMiddlewares(h.textCompletion, baseMiddlewares...))
	r.POST("/v1/chat/completions", lib.ChainMiddlewares(h.chatCompletion, baseMiddlewares...))
	r.POST("/v1/responses", lib.ChainMiddlewares(h.responses, baseMiddlewares...))
	responsesRetrieveMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ResponsesRetrieveRequest)}, middlewares...)
	responsesDeleteMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ResponsesDeleteRequest)}, middlewares...)
	responsesCancelMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ResponsesCancelRequest)}, middlewares...)
	responsesInputItemsMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ResponsesInputItemsRequest)}, middlewares...)
	r.GET("/v1/responses/{response_id}", lib.ChainMiddlewares(h.responsesRetrieve, responsesRetrieveMW...))
	r.DELETE("/v1/responses/{response_id}", lib.ChainMiddlewares(h.responsesDelete, responsesDeleteMW...))
	r.POST("/v1/responses/{response_id}/cancel", lib.ChainMiddlewares(h.responsesCancel, responsesCancelMW...))
	r.GET("/v1/responses/{response_id}/input_items", lib.ChainMiddlewares(h.responsesInputItems, responsesInputItemsMW...))
	r.POST("/v1/embeddings", lib.ChainMiddlewares(h.embeddings, baseMiddlewares...))
	r.POST("/v1/rerank", lib.ChainMiddlewares(h.rerank, baseMiddlewares...))
	r.POST("/v1/ocr", lib.ChainMiddlewares(h.ocr, baseMiddlewares...))
	// ElevenLabs sound-effect models also flow through /v1/audio/speech; the
	// provider routes them to /v1/sound-generation by model id, keeping SDK and
	// transport APIs at parity (no separate sound-effects endpoint).
	r.POST("/v1/audio/speech", lib.ChainMiddlewares(h.speech, baseMiddlewares...))
	r.POST("/v1/audio/transcriptions", lib.ChainMiddlewares(h.transcription, baseMiddlewares...))
	r.POST("/v1/images/generations", lib.ChainMiddlewares(h.imageGeneration, baseMiddlewares...))
	r.POST("/v1/responses/input_tokens", lib.ChainMiddlewares(h.countTokens, baseMiddlewares...))
	r.POST("/v1/responses/compact", lib.ChainMiddlewares(h.compaction, baseMiddlewares...))
	r.POST("/v1/images/edits", lib.ChainMiddlewares(h.imageEdit, baseMiddlewares...))
	r.POST("/v1/images/variations", lib.ChainMiddlewares(h.imageVariation, baseMiddlewares...))
	r.POST("/v1/videos", lib.ChainMiddlewares(h.videoGeneration, baseMiddlewares...))

	// Video API endpoints (parameterized routes need explicit request type middleware)
	videoListMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.VideoListRequest)}, middlewares...)
	videoRetrieveMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.VideoRetrieveRequest)}, middlewares...)
	videoDownloadMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.VideoDownloadRequest)}, middlewares...)
	videoDeleteMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.VideoDeleteRequest)}, middlewares...)
	videoRemixMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.VideoRemixRequest)}, middlewares...)
	r.GET("/v1/videos", lib.ChainMiddlewares(h.videoList, videoListMW...))
	r.GET("/v1/videos/{video_id}", lib.ChainMiddlewares(h.videoRetrieve, videoRetrieveMW...))
	r.GET("/v1/videos/{video_id}/content", lib.ChainMiddlewares(h.videoDownload, videoDownloadMW...))
	r.DELETE("/v1/videos/{video_id}", lib.ChainMiddlewares(h.videoDelete, videoDeleteMW...))
	r.POST("/v1/videos/{video_id}/remix", lib.ChainMiddlewares(h.videoRemix, videoRemixMW...))

	// Batch API endpoints (parameterized routes need explicit request type middleware)
	batchCreateMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.BatchCreateRequest)}, middlewares...)
	batchListMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.BatchListRequest)}, middlewares...)
	batchRetrieveMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.BatchRetrieveRequest)}, middlewares...)
	batchCancelMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.BatchCancelRequest)}, middlewares...)
	batchResultsMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.BatchResultsRequest)}, middlewares...)

	r.POST("/v1/batches", lib.ChainMiddlewares(h.batchCreate, batchCreateMW...))
	r.GET("/v1/batches", lib.ChainMiddlewares(h.batchList, batchListMW...))
	r.GET("/v1/batches/{batch_id}", lib.ChainMiddlewares(h.batchRetrieve, batchRetrieveMW...))
	r.POST("/v1/batches/{batch_id}/cancel", lib.ChainMiddlewares(h.batchCancel, batchCancelMW...))
	r.GET("/v1/batches/{batch_id}/results", lib.ChainMiddlewares(h.batchResults, batchResultsMW...))

	// File API endpoints (parameterized routes need explicit request type middleware)
	fileUploadMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.FileUploadRequest)}, middlewares...)
	fileListMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.FileListRequest)}, middlewares...)
	fileRetrieveMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.FileRetrieveRequest)}, middlewares...)
	fileDeleteMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.FileDeleteRequest)}, middlewares...)
	fileContentMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.FileContentRequest)}, middlewares...)

	r.POST("/v1/files", lib.ChainMiddlewares(h.fileUpload, fileUploadMW...))
	r.GET("/v1/files", lib.ChainMiddlewares(h.fileList, fileListMW...))
	r.GET("/v1/files/{file_id}", lib.ChainMiddlewares(h.fileRetrieve, fileRetrieveMW...))
	r.DELETE("/v1/files/{file_id}", lib.ChainMiddlewares(h.fileDelete, fileDeleteMW...))
	r.GET("/v1/files/{file_id}/content", lib.ChainMiddlewares(h.fileContent, fileContentMW...))

	// Container API endpoints (parameterized routes need explicit request type middleware)
	containerCreateMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerCreateRequest)}, middlewares...)
	containerListMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerListRequest)}, middlewares...)
	containerRetrieveMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerRetrieveRequest)}, middlewares...)
	containerDeleteMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerDeleteRequest)}, middlewares...)

	r.POST("/v1/containers", lib.ChainMiddlewares(h.containerCreate, containerCreateMW...))
	r.GET("/v1/containers", lib.ChainMiddlewares(h.containerList, containerListMW...))
	r.GET("/v1/containers/{container_id}", lib.ChainMiddlewares(h.containerRetrieve, containerRetrieveMW...))
	r.DELETE("/v1/containers/{container_id}", lib.ChainMiddlewares(h.containerDelete, containerDeleteMW...))

	// Container Files API endpoints (parameterized routes need explicit request type middleware)
	containerFileCreateMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerFileCreateRequest)}, middlewares...)
	containerFileListMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerFileListRequest)}, middlewares...)
	containerFileRetrieveMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerFileRetrieveRequest)}, middlewares...)
	containerFileContentMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerFileContentRequest)}, middlewares...)
	containerFileDeleteMW := append([]schemas.BifrostHTTPMiddleware{createRequestTypeMiddleware(schemas.ContainerFileDeleteRequest)}, middlewares...)

	r.POST("/v1/containers/{container_id}/files", lib.ChainMiddlewares(h.containerFileCreate, containerFileCreateMW...))
	r.GET("/v1/containers/{container_id}/files", lib.ChainMiddlewares(h.containerFileList, containerFileListMW...))
	r.GET("/v1/containers/{container_id}/files/{file_id}", lib.ChainMiddlewares(h.containerFileRetrieve, containerFileRetrieveMW...))
	r.GET("/v1/containers/{container_id}/files/{file_id}/content", lib.ChainMiddlewares(h.containerFileContent, containerFileContentMW...))
	r.DELETE("/v1/containers/{container_id}/files/{file_id}", lib.ChainMiddlewares(h.containerFileDelete, containerFileDeleteMW...))
}

// listModels handles GET /v1/models - Process list models requests
// If provider is not specified, lists all models from all configured providers
func (h *CompletionHandler) listModels(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel() // Ensure cleanup on function exit
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	if provider == "" && !h.applyListModelsVirtualKeyProviderFilter(ctx, bifrostCtx) {
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
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}

	enrichListModelsResponse(resp, h.config.ModelCatalog)
	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	// Send successful response
	SendJSON(ctx, resp)
}

func enrichListModelsResponse(resp *schemas.BifrostListModelsResponse, catalog *modelcatalog.ModelCatalog) {
	if resp == nil || len(resp.Data) == 0 {
		return
	}

	if catalog == nil {
		return
	}

	for i := range resp.Data {
		modelEntry := resp.Data[i]
		provider, modelName := schemas.ParseModelString(modelEntry.ID, "")
		pricingEntry := catalog.GetPricingEntryForModel(modelName, provider)
		if pricingEntry == nil && modelEntry.Alias != nil {
			pricingEntry = catalog.GetPricingEntryForModel(*modelEntry.Alias, provider)
		}
		if pricingEntry != nil {
			modelEntry.IsDeprecated = modelEntry.IsDeprecated || pricingEntry.IsDeprecated
			if pricingEntry.BaseModel != "" && modelEntry.NormalizedName == nil {
				modelEntry.NormalizedName = bifrost.Ptr(providerUtils.NormalizeBaseModelSlug(pricingEntry.BaseModel))
			}
			if len(pricingEntry.AdditionalAttributes) > 0 && modelEntry.AdditionalAttributes == nil {
				modelEntry.AdditionalAttributes = pricingEntry.AdditionalAttributes
			}
			if pricingEntry.ContextLength != nil && modelEntry.ContextLength == nil {
				modelEntry.ContextLength = pricingEntry.ContextLength
			} else if pricingEntry.MaxInputTokens != nil && modelEntry.ContextLength == nil {
				modelEntry.ContextLength = pricingEntry.MaxInputTokens
			}
			if pricingEntry.MaxInputTokens != nil && modelEntry.MaxInputTokens == nil {
				modelEntry.MaxInputTokens = pricingEntry.MaxInputTokens
			}
			if pricingEntry.MaxOutputTokens != nil && modelEntry.MaxOutputTokens == nil {
				modelEntry.MaxOutputTokens = pricingEntry.MaxOutputTokens
			}
			if pricingEntry.Architecture != nil && modelEntry.Architecture == nil {
				modelEntry.Architecture = pricingEntry.Architecture
			}
			if modelEntry.Pricing == nil {
				pricing := &schemas.Pricing{}
				if pricingEntry.InputCostPerToken != nil {
					pricing.Prompt = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.InputCostPerToken))
				}
				if pricingEntry.OutputCostPerToken != nil {
					pricing.Completion = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.OutputCostPerToken))
				}
				if pricingEntry.InputCostPerImage != nil {
					pricing.Image = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.InputCostPerImage))
				}
				if pricingEntry.CacheReadInputTokenCost != nil {
					pricing.InputCacheRead = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.CacheReadInputTokenCost))
				}
				if pricingEntry.CacheCreationInputTokenCost != nil {
					pricing.InputCacheWrite = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.CacheCreationInputTokenCost))
				}
				if pricingEntry.SearchContextCostPerQuery != nil {
					pricing.WebSearch = bifrost.Ptr(fmt.Sprintf("%.10f", *pricingEntry.SearchContextCostPerQuery))
				}
				modelEntry.Pricing = pricing
			}
		}
		resp.Data[i] = modelEntry
	}
}

// prepareTextCompletionRequest prepares a BifrostTextCompletionRequest from the HTTP request body
func prepareTextCompletionRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*TextRequest, *schemas.BifrostTextCompletionRequest, error) {
	req, base, err := prepareRequest[TextRequest](ctx, config, textParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if req.Prompt == nil || (req.Prompt.PromptStr == nil && req.Prompt.PromptArray == nil) {
		return nil, nil, fmt.Errorf("prompt is required for text completion")
	}
	if req.TextCompletionParameters == nil {
		req.TextCompletionParameters = &schemas.TextCompletionParameters{}
	}
	req.TextCompletionParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostTextCompletionRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Input:     req.Prompt,
		Params:    req.TextCompletionParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// textCompletion handles POST /v1/completions - Process text completion requests
func (h *CompletionHandler) textCompletion(ctx *fasthttp.RequestCtx) {
	req, bifrostTextReq, err := prepareTextCompletionRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
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
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// prepareChatCompletionRequest prepares a BifrostChatRequest from a ChatRequest
func prepareChatCompletionRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*ChatRequest, *schemas.BifrostChatRequest, error) {
	req, base, err := prepareRequest[ChatRequest](ctx, config, chatParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if len(req.Messages) == 0 {
		return nil, nil, fmt.Errorf("messages is required for chat completion")
	}
	if req.ChatParameters == nil {
		req.ChatParameters = &schemas.ChatParameters{}
	}
	// Handle max_tokens -> max_completion_tokens mapping.
	// This supports the legacy max_tokens field still used by some implementations.
	if base.ExtraParams != nil {
		if maxTokensVal, exists := base.ExtraParams["max_tokens"]; exists {
			delete(base.ExtraParams, "max_tokens")
			if req.ChatParameters.MaxCompletionTokens == nil {
				if maxTokensFloat, ok := maxTokensVal.(float64); ok {
					maxTokens := int(maxTokensFloat)
					req.ChatParameters.MaxCompletionTokens = &maxTokens
				} else if maxTokensInt, ok := maxTokensVal.(int); ok {
					req.ChatParameters.MaxCompletionTokens = &maxTokensInt
				}
			}
		}
	}
	req.ChatParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostChatRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Input:     req.Messages,
		Params:    req.ChatParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// chatCompletion handles POST /v1/chat/completions - Process chat completion requests
func (h *CompletionHandler) chatCompletion(ctx *fasthttp.RequestCtx) {
	req, bifrostChatReq, err := prepareChatCompletionRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	if effectiveStream(req.Stream) {
		h.handleStreamingChatCompletion(ctx, bifrostChatReq, bifrostCtx, cancel)
		return
	}
	defer cancel() // Ensure cleanup on function exit
	// Complete the request
	resp, bifrostErr := h.client.ChatCompletionRequest(bifrostCtx, bifrostChatReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}
	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// prepareResponsesRequest prepares a BifrostResponsesRequest from a ResponsesRequest
func prepareResponsesRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*ResponsesRequest, *schemas.BifrostResponsesRequest, error) {
	req, base, err := prepareRequest[ResponsesRequest](ctx, config, responsesParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if len(req.Input.ResponsesRequestInputArray) == 0 && req.Input.ResponsesRequestInputStr == nil {
		return nil, nil, fmt.Errorf("input is required for responses")
	}
	if req.ResponsesParameters == nil {
		req.ResponsesParameters = &schemas.ResponsesParameters{}
	}
	req.ResponsesParameters.ExtraParams = base.ExtraParams

	input := req.Input.ResponsesRequestInputArray
	if input == nil {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: req.Input.ResponsesRequestInputStr},
			},
		}
	}
	return req, &schemas.BifrostResponsesRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Input:     input,
		Params:    req.ResponsesParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// responses handles POST /v1/responses - Process responses requests
func (h *CompletionHandler) responses(ctx *fasthttp.RequestCtx) {
	req, bifrostResponsesReq, err := prepareResponsesRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	if effectiveStream(req.Stream) {
		h.handleStreamingResponses(ctx, bifrostResponsesReq, bifrostCtx, cancel)
		return
	}

	defer cancel() // Ensure cleanup on function exit

	resp, bifrostErr := h.client.ResponsesRequest(bifrostCtx, bifrostResponsesReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// prepareEmbeddingRequest prepares a BifrostEmbeddingRequest from the HTTP request body
func prepareEmbeddingRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*EmbeddingRequest, *schemas.BifrostEmbeddingRequest, error) {
	req, base, err := prepareRequest[EmbeddingRequest](ctx, config, embeddingParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if req.Input == nil || (req.Input.Text == nil && req.Input.Texts == nil && req.Input.Embedding == nil && req.Input.Embeddings == nil) {
		return nil, nil, fmt.Errorf("input is required for embeddings")
	}
	if req.EmbeddingParameters == nil {
		req.EmbeddingParameters = &schemas.EmbeddingParameters{}
	}
	req.EmbeddingParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostEmbeddingRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Input:     req.Input,
		Params:    req.EmbeddingParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// embeddings handles POST /v1/embeddings - Process embeddings requests
func (h *CompletionHandler) embeddings(ctx *fasthttp.RequestCtx) {
	_, bifrostEmbeddingReq, err := prepareEmbeddingRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.EmbeddingRequest(bifrostCtx, bifrostEmbeddingReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// prepareRerankRequest prepares a BifrostRerankRequest from the HTTP request body
func prepareRerankRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*RerankRequest, *schemas.BifrostRerankRequest, error) {
	req, base, err := prepareRequest[RerankRequest](ctx, config, rerankParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, nil, fmt.Errorf("query is required for rerank")
	}
	if len(req.Documents) == 0 {
		return nil, nil, fmt.Errorf("documents are required for rerank")
	}
	for i, doc := range req.Documents {
		if strings.TrimSpace(doc.Text) == "" {
			return nil, nil, fmt.Errorf("document text is required for rerank at index %d", i)
		}
	}
	if req.RerankParameters == nil {
		req.RerankParameters = &schemas.RerankParameters{}
	}
	if req.RerankParameters.TopN != nil && *req.RerankParameters.TopN < 1 {
		return nil, nil, fmt.Errorf("top_n must be at least 1")
	}
	req.RerankParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostRerankRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Query:     req.Query,
		Documents: req.Documents,
		Params:    req.RerankParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// rerank handles POST /v1/rerank - Process rerank requests
func (h *CompletionHandler) rerank(ctx *fasthttp.RequestCtx) {
	_, bifrostRerankReq, err := prepareRerankRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.RerankRequest(bifrostCtx, bifrostRerankReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// prepareOCRRequest prepares a BifrostOCRRequest from the HTTP request body
func prepareOCRRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*OCRHandlerRequest, *schemas.BifrostOCRRequest, error) {
	req, base, err := prepareRequest[OCRHandlerRequest](ctx, config, ocrParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if req.Document.Type == "" {
		return nil, nil, fmt.Errorf("document type is required for ocr")
	}
	if req.Document.Type == schemas.OCRDocumentTypeDocumentURL && (req.Document.DocumentURL == nil || *req.Document.DocumentURL == "") {
		return nil, nil, fmt.Errorf("document_url is required when document type is document_url")
	}
	if req.Document.Type == schemas.OCRDocumentTypeImageURL && (req.Document.ImageURL == nil || *req.Document.ImageURL == "") {
		return nil, nil, fmt.Errorf("image_url is required when document type is image_url")
	}
	if req.OCRParameters == nil {
		req.OCRParameters = &schemas.OCRParameters{}
	}
	req.OCRParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostOCRRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		ID:        req.ID,
		Document:  req.Document,
		Params:    req.OCRParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// ocr handles POST /v1/ocr - Process OCR requests
func (h *CompletionHandler) ocr(ctx *fasthttp.RequestCtx) {
	_, bifrostOCRReq, err := prepareOCRRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.OCRRequest(bifrostCtx, bifrostOCRReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// prepareSpeechRequest prepares a BifrostSpeechRequest from the HTTP request body
func prepareSpeechRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*SpeechRequest, *schemas.BifrostSpeechRequest, error) {
	req, base, err := prepareRequest[SpeechRequest](ctx, config, speechParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if req.SpeechInput == nil || req.SpeechInput.Input == "" {
		return nil, nil, fmt.Errorf("input is required for speech completion")
	}
	// Voice is required for text-to-speech, but the transport layer cannot resolve
	// virtual-key aliases, so it cannot distinguish a voice-less ElevenLabs
	// sound-effect model (eleven_text_to_sound_*, including aliases that resolve to
	// one) from a text-to-speech model that simply omitted its voice. A name-based
	// check here (IsElevenlabsSoundModel(base.ModelName)) matches only literal
	// model ids, so an alias like "my-sfx" → "eleven_text_to_sound_v2" would be
	// wrongly rejected before reaching the provider. For ElevenLabs we therefore
	// defer the voice check to ElevenlabsProvider.Speech, which runs after alias
	// resolution: it routes sound models to /v1/sound-generation and still rejects
	// voice-less text-to-speech with "voice parameter is required". Every other
	// provider keeps the original handler-level 400, so TTS behavior is unchanged.
	if base.Provider != schemas.Elevenlabs {
		if req.SpeechParameters == nil || req.VoiceConfig == nil || (req.VoiceConfig.Voice == nil && len(req.VoiceConfig.MultiVoiceConfig) == 0) {
			return nil, nil, fmt.Errorf("voice is required for speech completion")
		}
	}
	if req.SpeechParameters == nil {
		req.SpeechParameters = &schemas.SpeechParameters{}
	}
	req.SpeechParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostSpeechRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Input:     req.SpeechInput,
		Params:    req.SpeechParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// speechAttachmentFilename derives the download filename from the requested
// audio format so non-MP3 responses aren't mislabeled as "speech.mp3". The
// format may be OpenAI-style ("opus", "wav", "flac", ...) or ElevenLabs-style
// with a codec/rate prefix ("mp3_22050_32", "pcm_16000", "ulaw_8000"). Unknown
// or empty formats fall back to mp3 (the API default).
func speechAttachmentFilename(responseFormat string) string {
	ext := "mp3"
	switch {
	case strings.HasPrefix(responseFormat, "opus"):
		ext = "opus"
	case strings.HasPrefix(responseFormat, "aac"):
		ext = "aac"
	case strings.HasPrefix(responseFormat, "flac"):
		ext = "flac"
	case strings.HasPrefix(responseFormat, "wav"):
		ext = "wav"
	case strings.HasPrefix(responseFormat, "pcm"):
		ext = "pcm"
	case strings.HasPrefix(responseFormat, "ulaw"):
		ext = "ulaw"
	case strings.HasPrefix(responseFormat, "alaw"):
		ext = "alaw"
	}
	return "speech." + ext
}

// speech handles POST /v1/audio/speech - Process speech completion requests.
// ElevenLabs sound-effect models (e.g. "eleven_text_to_sound_v2") also flow
// through here; the provider routes them to /v1/sound-generation by model id,
// so they reuse the speech request type and virtual-key governance unchanged.
func (h *CompletionHandler) speech(ctx *fasthttp.RequestCtx) {
	req, bifrostSpeechReq, err := prepareSpeechRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// Filename tracks the requested audio format (prepareSpeechRequest guarantees
	// req.SpeechParameters is non-nil, so ResponseFormat is safe to read here).
	attachmentFilename := speechAttachmentFilename(req.ResponseFormat)

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	if req.StreamFormat != nil && *req.StreamFormat == "sse" {
		h.handleStreamingSpeech(ctx, bifrostSpeechReq, bifrostCtx, cancel)
		return
	}

	defer cancel() // Ensure cleanup on function exit

	resp, bifrostErr := h.client.SpeechRequest(bifrostCtx, bifrostSpeechReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// Preserve the attachment header through the large-response shortcut; the
	// normal binary path sets this explicitly after the stream check.
	if !(bifrostSpeechReq.Provider == schemas.Elevenlabs && req.WithTimestamps != nil && *req.WithTimestamps) {
		bifrostCtx.SetValue(schemas.BifrostContextKeyLargeResponseContentDisposition, "attachment; filename="+attachmentFilename)
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}

	// Send successful response
	// When with_timestamps is true, Elevenlabs returns base64 encoded audio
	hasTimestamps := req.WithTimestamps != nil && *req.WithTimestamps

	if bifrostSpeechReq.Provider == schemas.Elevenlabs && hasTimestamps {
		ctx.Response.Header.Set("Content-Type", "application/json")
		SendJSON(ctx, resp)
		return
	}

	if resp.Audio == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Speech response is missing audio data")
		return
	}

	ctx.Response.Header.Set("Content-Type", "audio/mpeg")
	ctx.Response.Header.Set("Content-Disposition", "attachment; filename="+attachmentFilename)
	ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(resp.Audio)))
	ctx.Response.SetBody(resp.Audio)
}

// prepareTranscriptionRequest prepares a BifrostTranscriptionRequest from a multipart form.
// Returns the request, whether streaming was requested, and any error.
func prepareTranscriptionRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*schemas.BifrostTranscriptionRequest, bool, error) {
	form, err := ctx.MultipartForm()
	if err != nil {
		return nil, false, fmt.Errorf("failed to parse multipart form: %v", err)
	}
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return nil, false, fmt.Errorf("model is required")
	}
	provider, modelName, err := resolveModelAndProvider(ctx, config, modelValues[0])
	if err != nil {
		return nil, false, err
	}
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		return nil, false, fmt.Errorf("file is required")
	}
	fileHeader := fileHeaders[0]
	file, err := fileHeader.Open()
	if err != nil {
		return nil, false, fmt.Errorf("failed to open uploaded file: %v", err)
	}
	defer file.Close()
	fileData, err := io.ReadAll(file)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read uploaded file: %v", err)
	}
	transcriptionInput := &schemas.TranscriptionInput{
		File:     fileData,
		Filename: fileHeader.Filename,
	}
	transcriptionParams := &schemas.TranscriptionParameters{}
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
	stream := false
	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] == "true" {
		stream = true
	}
	fallbacks, err := parseFallbacks(form.Value["fallbacks"])
	if err != nil {
		return nil, false, err
	}
	bifrostTranscriptionReq := &schemas.BifrostTranscriptionRequest{
		Model:     modelName,
		Provider:  schemas.ModelProvider(provider),
		Input:     transcriptionInput,
		Params:    transcriptionParams,
		Fallbacks: fallbacks,
	}
	return bifrostTranscriptionReq, stream, nil
}

// transcription handles POST /v1/audio/transcriptions - Process transcription requests
func (h *CompletionHandler) transcription(ctx *fasthttp.RequestCtx) {
	bifrostTranscriptionReq, stream, err := prepareTranscriptionRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	if stream {
		h.handleStreamingTranscriptionRequest(ctx, bifrostTranscriptionReq, bifrostCtx, cancel)
		return
	}

	defer cancel()

	resp, bifrostErr := h.client.TranscriptionRequest(bifrostCtx, bifrostTranscriptionReq)

	// Handle response
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	// Send successful response
	SendJSON(ctx, resp)
}

// countTokens handles POST /v1/responses/input_tokens - Process count tokens requests
func (h *CompletionHandler) countTokens(ctx *fasthttp.RequestCtx) {
	_, bifrostResponsesReq, err := prepareResponsesRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	defer cancel()

	response, bifrostErr := h.client.CountTokensRequest(bifrostCtx, bifrostResponsesReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	forwardProviderHeaders(ctx, response.ExtraFields.ProviderResponseHeaders)
	// Send successful response
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, response)
}

func responsesLifecycleProviderFromQuery(ctx *fasthttp.RequestCtx) schemas.ModelProvider {
	p := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
	if p == "" {
		return schemas.OpenAI
	}
	return p
}

// prepareCompactionRequest prepares a BifrostCompactionRequest from the HTTP request body
func prepareCompactionRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*CompactionHTTPRequest, *schemas.BifrostCompactionRequest, error) {
	req, base, err := prepareRequest[CompactionHTTPRequest](ctx, config, compactionParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}

	if len(req.Input.ResponsesRequestInputArray) == 0 && req.Input.ResponsesRequestInputStr == nil && req.PreviousResponseID == nil {
		return nil, nil, fmt.Errorf("input or previous_response_id is required for compaction")
	}

	input := req.Input.ResponsesRequestInputArray
	if input == nil && req.Input.ResponsesRequestInputStr != nil {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: req.Input.ResponsesRequestInputStr},
			},
		}
	}

	return req, &schemas.BifrostCompactionRequest{
		Provider:             base.Provider,
		Model:                base.ModelName,
		Input:                input,
		Instructions:         req.Instructions,
		PreviousResponseID:   req.PreviousResponseID,
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
		ServiceTier:          req.ServiceTier,
		Fallbacks:            base.Fallbacks,
		ExtraParams:          base.ExtraParams,
	}, nil
}

// responsesRetrieve handles GET /v1/responses/{response_id}.
func (h *CompletionHandler) responsesRetrieve(ctx *fasthttp.RequestCtx) {
	responseID, ok := ctx.UserValue("response_id").(string)
	if !ok || responseID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "response_id is required")
		return
	}
	bifrostReq := &schemas.BifrostResponsesRetrieveRequest{
		Provider:   responsesLifecycleProviderFromQuery(ctx),
		ResponseID: responseID,
	}
	ctx.QueryArgs().VisitAll(func(key, value []byte) {
		switch string(key) {
		case "include":
			bifrostReq.Include = append(bifrostReq.Include, string(value))
		}
	})
	if raw := ctx.QueryArgs().Peek("starting_after"); len(raw) > 0 {
		n, err := strconv.Atoi(string(raw))
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "starting_after must be an integer")
			return
		}
		bifrostReq.StartingAfter = schemas.Ptr(n)
	}
	if raw := ctx.QueryArgs().Peek("include_obfuscation"); len(raw) > 0 {
		b, err := strconv.ParseBool(string(raw))
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "include_obfuscation must be a boolean")
			return
		}
		bifrostReq.IncludeObfuscation = &b
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	resp, bifrostErr := h.client.ResponsesRetrieveRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}
	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// compaction handles POST /v1/responses/compact - Compact a conversation context window
func (h *CompletionHandler) compaction(ctx *fasthttp.RequestCtx) {
	_, bifrostCompactionReq, err := prepareCompactionRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	response, bifrostErr := h.client.CompactionRequest(bifrostCtx, bifrostCompactionReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if response != nil && response.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, response.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, response)
}

// responsesDelete handles DELETE /v1/responses/{response_id}.
func (h *CompletionHandler) responsesDelete(ctx *fasthttp.RequestCtx) {
	responseID, ok := ctx.UserValue("response_id").(string)
	if !ok || responseID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "response_id is required")
		return
	}
	bifrostReq := &schemas.BifrostResponsesDeleteRequest{
		Provider:   responsesLifecycleProviderFromQuery(ctx),
		ResponseID: responseID,
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	resp, bifrostErr := h.client.ResponsesDeleteRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}
	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// responsesCancel handles POST /v1/responses/{response_id}/cancel.
func (h *CompletionHandler) responsesCancel(ctx *fasthttp.RequestCtx) {
	responseID, ok := ctx.UserValue("response_id").(string)
	if !ok || responseID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "response_id is required")
		return
	}
	bifrostReq := &schemas.BifrostResponsesCancelRequest{
		Provider:   responsesLifecycleProviderFromQuery(ctx),
		ResponseID: responseID,
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	resp, bifrostErr := h.client.ResponsesCancelRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}
	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// responsesInputItems handles GET /v1/responses/{response_id}/input_items.
func (h *CompletionHandler) responsesInputItems(ctx *fasthttp.RequestCtx) {
	responseID, ok := ctx.UserValue("response_id").(string)
	if !ok || responseID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "response_id is required")
		return
	}
	bifrostReq := &schemas.BifrostResponsesInputItemsRequest{
		Provider:   responsesLifecycleProviderFromQuery(ctx),
		ResponseID: responseID,
	}
	ctx.QueryArgs().VisitAll(func(key, value []byte) {
		switch string(key) {
		case "after":
			bifrostReq.After = string(value)
		case "include":
			bifrostReq.Include = append(bifrostReq.Include, string(value))
		case "order":
			bifrostReq.Order = string(value)
		}
	})
	if raw := ctx.QueryArgs().Peek("limit"); len(raw) > 0 {
		n, err := strconv.Atoi(string(raw))
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "limit must be an integer")
			return
		}
		bifrostReq.Limit = schemas.Ptr(n)
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	resp, bifrostErr := h.client.ResponsesInputItemsRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}
	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
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
	// Get the streaming channel — called BEFORE setting SSE headers so that
	// provider errors return proper HTTP status codes + JSON content type.
	stream, bifrostErr := getStream()
	if bifrostErr != nil {
		cancel()
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	// SSE headers set only after successful stream setup
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")

	// Forward provider response headers stored in context by streaming handlers
	if headers, ok := bifrostCtx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string); ok {
		forwardProviderHeaders(ctx, headers)
	}

	// Signal to tracing middleware that trace completion should be deferred
	// The streaming callback will complete the trace after the stream ends
	ctx.SetUserValue(schemas.BifrostContextKeyDeferTraceCompletion, true)

	// Pre-allocate atomic.Value slot for the transport post-hook completer.
	// TransportInterceptorMiddleware stores the completer into this slot after next(ctx)
	// returns. The goroutine reads from the closure-captured pointer, avoiding any ctx
	// access after the handler returns (fasthttp recycles RequestCtx).
	var completerSlot atomic.Value
	ctx.SetUserValue(schemas.BifrostContextKeyTransportPostHookCompleter, &completerSlot)

	// Get the trace completer function for use in the streaming callback.
	// Signature: func([]schemas.PluginLogEntry) — accepts transport plugin logs so it
	// never needs to read from ctx.UserValue (ctx may be recycled).
	traceCompleter, _ := ctx.UserValue(schemas.BifrostContextKeyTraceCompleter).(func([]schemas.PluginLogEntry))

	// Get stream chunk interceptor for plugin hooks
	interceptor := h.config.GetStreamChunkInterceptor()
	var httpReq *schemas.HTTPRequest
	if interceptor != nil {
		httpReq = lib.BuildHTTPRequestFromFastHTTP(ctx)
	}
	// Use SSEStreamReader to bypass fasthttp's internal pipe (fasthttputil.PipeConns)
	// which batches multiple SSE events into single TCP segments.
	// Each event is delivered individually via a channel, ensuring one HTTP chunk per event.
	reader := lib.NewSSEStreamReader()
	ctx.Response.SetBodyStream(reader, -1)

	// Producer goroutine: processes the stream channel, formats SSE events, sends to reader
	go func() {
		var transportLogs []schemas.PluginLogEntry
		completerRan := false
		// runCompleter invokes the transport post-hook completer at most once.
		// sendSSEOnError=true emits plugin errors as SSE "event: error" frames so the
		// client sees them (happy path, before [DONE]); =false logs server-side only
		// (early-return / defer fallback, after stream termination).
		runCompleter := func(sendSSEOnError bool) {
			if completerRan {
				return
			}
			// Bounded wait for TransportInterceptorMiddleware to publish the completer.
			// It calls slot.Store after next(ctx) returns, which races with this goroutine
			// on fast/empty streams. 100ms is ample — the store runs a few instructions
			// after the handler returns.
			var loaded any
			deadline := time.Now().Add(100 * time.Millisecond)
			for {
				if loaded = completerSlot.Load(); loaded != nil {
					break
				}
				if time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if loaded == nil {
				return
			}
			postHookCompleter, ok := loaded.(func() ([]schemas.PluginLogEntry, error))
			if !ok {
				return
			}
			completerRan = true
			logs, err := postHookCompleter()
			if err != nil {
				if sendSSEOnError {
					errorJSON, marshalErr := sonic.Marshal(map[string]string{"error": err.Error()})
					if marshalErr == nil {
						reader.SendError(errorJSON)
					}
				} else {
					logger.Warn("transport post-hook failed after stream terminated: %v", err)
				}
			}
			transportLogs = logs
		}

		defer func() {
			schemas.ReleaseHTTPRequest(httpReq)
			// Fallback: on early-return paths (client disconnect, interceptor error)
			// we never reached the pre-[DONE] invocation, so run it now. Any error is
			// logged server-side only — the stream is already closing.
			runCompleter(false)
			reader.Done()
			// Complete the trace after streaming finishes, passing transport plugin logs.
			// This ensures all spans (including llm.call) are properly ended before the trace is sent to OTEL.
			if traceCompleter != nil {
				traceCompleter(transportLogs)
			}
		}()

		var includeEventType bool
		var skipDoneMarker bool

		// Process streaming responses
		for chunk := range stream {
			if chunk == nil {
				continue
			}

			includeEventType = false
			if chunk.BifrostResponsesStreamResponse != nil ||
				chunk.BifrostImageGenerationStreamResponse != nil ||
				(chunk.BifrostError != nil && (chunk.BifrostError.ExtraFields.RequestType == schemas.ResponsesStreamRequest || chunk.BifrostError.ExtraFields.RequestType == schemas.ImageGenerationStreamRequest || chunk.BifrostError.ExtraFields.RequestType == schemas.ImageEditStreamRequest)) {
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
						var errorPayload interface{} = map[string]string{"error": err.Error()}
						var structuredErr *schemas.StreamInterceptionError
						if errors.As(err, &structuredErr) && structuredErr != nil {
							if sanitized := lib.SanitizeBifrostErrorForClient(structuredErr.BifrostError); sanitized != nil {
								errorPayload = sanitized
							}
						}
						errorJSON, marshalErr := sonic.Marshal(errorPayload)
						if marshalErr != nil {
							cancel() // Payload invalid
							for range stream {
							}
							return
						}
						// Return error event and stop streaming
						reader.SendError(errorJSON)
						cancel()
						for range stream {
						}
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

			// Format and send as SSE data
			var eventType string
			if includeEventType {
				// For responses and image gen API, use OpenAI-compatible format with event line
				if chunk.BifrostResponsesStreamResponse != nil {
					eventType = string(chunk.BifrostResponsesStreamResponse.Type)
				} else if chunk.BifrostImageGenerationStreamResponse != nil {
					eventType = string(chunk.BifrostImageGenerationStreamResponse.Type)
				} else if chunk.BifrostError != nil {
					eventType = string(schemas.ResponsesStreamResponseTypeError)
				}
			}

			if !reader.SendEvent(eventType, chunkJSON) {
				cancel() // Client disconnected, cancel upstream stream
				// Drain remaining chunks so the provider goroutine's defer
				// (HandleStreamCancellation -> PostLLMHook -> storeOrEnqueueEntry) finishes
				// before our own defer fires traceCompleter. Without this, Inject runs
				// against an empty pendingLogsToInject and the cancellation log is orphaned.
				for range stream {
				}
				return
			}
		}

		// Run the transport post-hook completer BEFORE the terminal [DONE] marker so
		// that any plugin error can still be delivered to the client as an SSE event.
		// Post-hooks emitted after [DONE] reach the wire but most clients stop reading
		// once they see [DONE], so they'd be silently dropped.
		runCompleter(true)

		if !includeEventType && !skipDoneMarker {
			// Send the [DONE] marker to indicate the end of the stream (only for non-responses/image-gen APIs)
			if !reader.SendDone() {
				cancel()
				return
			}
		}
		// Note: OpenAI responses API doesn't use [DONE] marker, it ends when the stream closes
		// Stream completed normally, Bifrost handles cleanup internally
		cancel()
	}()
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

// prepareImageGenerationRequest prepares a BifrostImageGenerationRequest from the HTTP request body
func prepareImageGenerationRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*ImageGenerationHTTPRequest, *schemas.BifrostImageGenerationRequest, error) {
	req, base, err := prepareRequest[ImageGenerationHTTPRequest](ctx, config, imageGenerationParamsKnownFields)
	if err != nil {
		return nil, nil, err
	}
	if req.ImageGenerationInput == nil || req.Prompt == "" {
		return nil, nil, fmt.Errorf("prompt cannot be empty")
	}
	if req.ImageGenerationParameters == nil {
		req.ImageGenerationParameters = &schemas.ImageGenerationParameters{}
	}
	req.ImageGenerationParameters.ExtraParams = base.ExtraParams
	return req, &schemas.BifrostImageGenerationRequest{
		Provider:  base.Provider,
		Model:     base.ModelName,
		Input:     req.ImageGenerationInput,
		Params:    req.ImageGenerationParameters,
		Fallbacks: base.Fallbacks,
	}, nil
}

// imageGeneration handles POST /v1/images/generations - Processes image generation requests
func (h *CompletionHandler) imageGeneration(ctx *fasthttp.RequestCtx) {
	req, bifrostReq, err := prepareImageGenerationRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		cancel()
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	// Handle streaming image generation
	if req.BifrostParams.Stream != nil && *req.BifrostParams.Stream {
		h.handleStreamingImageGeneration(ctx, bifrostReq, bifrostCtx, cancel)
		return
	}
	defer cancel()

	// Execute request
	resp, bifrostErr := h.client.ImageGenerationRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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

// prepareImageEditRequest prepares a BifrostImageEditRequest from a multipart form
func prepareImageEditRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*ImageEditHTTPRequest, *schemas.BifrostImageEditRequest, error) {
	var req ImageEditHTTPRequest
	form, err := ctx.MultipartForm()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse multipart form: %v", err)
	}
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return nil, nil, fmt.Errorf("model is required")
	}
	req.Model = modelValues[0]
	provider, modelName, err := resolveModelAndProvider(ctx, config, req.Model)
	if err != nil {
		return nil, nil, err
	}
	var editType string
	if typeValues := form.Value["type"]; len(typeValues) > 0 && typeValues[0] != "" {
		editType = typeValues[0]
	}
	promptValues := form.Value["prompt"]
	var imageFiles []*multipart.FileHeader
	if imageFilesArray := form.File["image[]"]; len(imageFilesArray) > 0 {
		imageFiles = imageFilesArray
	} else if imageFilesSingle := form.File["image"]; len(imageFilesSingle) > 0 {
		imageFiles = imageFilesSingle
	}
	if len(imageFiles) == 0 {
		return nil, nil, fmt.Errorf("at least one image is required")
	}
	images := make([]schemas.ImageInput, 0, len(imageFiles))
	for _, fh := range imageFiles {
		f, err := fh.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open uploaded file: %v", err)
		}
		fileData, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read uploaded file: %v", err)
		}
		images = append(images, schemas.ImageInput{Image: fileData})
	}
	prompt := ""
	if len(promptValues) > 0 && promptValues[0] != "" {
		prompt = promptValues[0]
	}
	req.ImageEditInput = &schemas.ImageEditInput{
		Images: images,
		Prompt: prompt,
	}
	req.ImageEditParameters = &schemas.ImageEditParameters{}
	if nValues := form.Value["n"]; len(nValues) > 0 && nValues[0] != "" {
		n, err := strconv.Atoi(nValues[0])
		if err != nil {
			return nil, nil, fmt.Errorf("invalid n value: %v", err)
		}
		req.ImageEditParameters.N = &n
	}
	if backgroundValues := form.Value["background"]; len(backgroundValues) > 0 && backgroundValues[0] != "" {
		req.ImageEditParameters.Background = &backgroundValues[0]
	}
	if inputFidelityValues := form.Value["input_fidelity"]; len(inputFidelityValues) > 0 && inputFidelityValues[0] != "" {
		req.ImageEditParameters.InputFidelity = &inputFidelityValues[0]
	}
	if partialImagesValues := form.Value["partial_images"]; len(partialImagesValues) > 0 && partialImagesValues[0] != "" {
		partialImages, err := strconv.Atoi(partialImagesValues[0])
		if err != nil {
			return nil, nil, fmt.Errorf("invalid partial_images value: %v", err)
		}
		req.ImageEditParameters.PartialImages = &partialImages
	}
	if sizeValues := form.Value["size"]; len(sizeValues) > 0 && sizeValues[0] != "" {
		req.ImageEditParameters.Size = &sizeValues[0]
	}
	if aspectRatioValues := form.Value["aspect_ratio"]; len(aspectRatioValues) > 0 && aspectRatioValues[0] != "" {
		req.ImageEditParameters.AspectRatio = &aspectRatioValues[0]
	}
	if qualityValues := form.Value["quality"]; len(qualityValues) > 0 && qualityValues[0] != "" {
		req.ImageEditParameters.Quality = &qualityValues[0]
	}
	if outputFormatValues := form.Value["output_format"]; len(outputFormatValues) > 0 && outputFormatValues[0] != "" {
		req.ImageEditParameters.OutputFormat = &outputFormatValues[0]
	}
	if numInferenceStepsValues := form.Value["num_inference_steps"]; len(numInferenceStepsValues) > 0 && numInferenceStepsValues[0] != "" {
		numInferenceSteps, err := strconv.Atoi(numInferenceStepsValues[0])
		if err != nil {
			return nil, nil, fmt.Errorf("invalid num_inference_steps value: %v", err)
		}
		req.ImageEditParameters.NumInferenceSteps = &numInferenceSteps
	}
	if seedValues := form.Value["seed"]; len(seedValues) > 0 && seedValues[0] != "" {
		seed, err := strconv.Atoi(seedValues[0])
		if err != nil {
			return nil, nil, fmt.Errorf("invalid seed value: %v", err)
		}
		req.ImageEditParameters.Seed = &seed
	}
	if outputCompressionValues := form.Value["output_compression"]; len(outputCompressionValues) > 0 && outputCompressionValues[0] != "" {
		outputCompression, err := strconv.Atoi(outputCompressionValues[0])
		if err != nil {
			return nil, nil, fmt.Errorf("invalid output_compression value: %v", err)
		}
		req.ImageEditParameters.OutputCompression = &outputCompression
	}
	if negativePromptValues := form.Value["negative_prompt"]; len(negativePromptValues) > 0 && negativePromptValues[0] != "" {
		req.ImageEditParameters.NegativePrompt = &negativePromptValues[0]
	}
	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		req.ImageEditParameters.ResponseFormat = &responseFormatValues[0]
	}
	if userValues := form.Value["user"]; len(userValues) > 0 && userValues[0] != "" {
		req.ImageEditParameters.User = &userValues[0]
	}
	if editType != "" {
		req.ImageEditParameters.Type = &editType
	}
	if maskFiles := form.File["mask"]; len(maskFiles) > 0 {
		maskFile := maskFiles[0]
		f, err := maskFile.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open mask file: %v", err)
		}
		maskData, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read mask file: %v", err)
		}
		req.ImageEditParameters.Mask = maskData
	}
	if req.ImageEditParameters.ExtraParams == nil {
		req.ImageEditParameters.ExtraParams = make(map[string]interface{})
	}
	for key, value := range form.Value {
		if len(value) > 0 && value[0] != "" && !imageEditParamsKnownFields[key] {
			req.ImageEditParameters.ExtraParams[key] = value[0]
		}
	}
	if fallbackValues := form.Value["fallbacks"]; len(fallbackValues) > 0 {
		req.Fallbacks = fallbackValues
	}
	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream := streamValues[0] == "true"
		req.Stream = &stream
	}
	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		return nil, nil, err
	}
	bifrostReq := &schemas.BifrostImageEditRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.ImageEditInput,
		Params:    req.ImageEditParameters,
		Fallbacks: fallbacks,
	}
	return &req, bifrostReq, nil
}

// imageEdit handles POST /v1/images/edits - Processes image edit requests
func (h *CompletionHandler) imageEdit(ctx *fasthttp.RequestCtx) {
	req, bifrostReq, err := prepareImageEditRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	// Handle streaming image edit
	if req.Stream != nil && *req.Stream {
		h.handleStreamingImageEditRequest(ctx, bifrostReq, bifrostCtx, cancel)
		return
	}
	defer cancel()

	// Execute request
	resp, bifrostErr := h.client.ImageEditRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// handleStreamingImageEditRequest handles streaming image edit requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingImageEditRequest(ctx *fasthttp.RequestCtx, req *schemas.BifrostImageEditRequest, bifrostCtx *schemas.BifrostContext, cancel context.CancelFunc) {
	// Use the cancellable context from ConvertToBifrostContext
	// See router.go for detailed explanation of why we need a cancellable context

	getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return h.client.ImageEditStreamRequest(bifrostCtx, req)
	}

	h.handleStreamingResponse(ctx, bifrostCtx, getStream, cancel)
}

// prepareImageVariationRequest prepares a BifrostImageVariationRequest from a multipart form
func prepareImageVariationRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (*schemas.BifrostImageVariationRequest, error) {
	rawBody := ctx.Request.Body()
	form, err := ctx.MultipartForm()
	if err != nil {
		return nil, fmt.Errorf("failed to parse multipart form: %v", err)
	}
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return nil, fmt.Errorf("model is required")
	}
	provider, modelName, err := resolveModelAndProvider(ctx, config, modelValues[0])
	if err != nil {
		return nil, err
	}
	var imageFiles []*multipart.FileHeader
	if imageFilesArray := form.File["image[]"]; len(imageFilesArray) > 0 {
		imageFiles = imageFilesArray
	} else if imageFilesSingle := form.File["image"]; len(imageFilesSingle) > 0 {
		imageFiles = imageFilesSingle
	}
	if len(imageFiles) == 0 {
		return nil, fmt.Errorf("at least one image is required")
	}
	images := make([][]byte, 0, len(imageFiles))
	for _, fileHeader := range imageFiles {
		file, err := fileHeader.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open uploaded file: %v", err)
		}
		fileData, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read uploaded file: %v", err)
		}
		images = append(images, fileData)
	}
	variationInput := &schemas.ImageVariationInput{
		Image: schemas.ImageInput{
			Image: images[0],
		},
	}
	variationParams := &schemas.ImageVariationParameters{}
	if nValues := form.Value["n"]; len(nValues) > 0 && nValues[0] != "" {
		n, err := strconv.Atoi(nValues[0])
		if err != nil {
			return nil, fmt.Errorf("invalid n value: %v", err)
		}
		variationParams.N = &n
	}
	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		variationParams.ResponseFormat = &responseFormatValues[0]
	}
	if sizeValues := form.Value["size"]; len(sizeValues) > 0 && sizeValues[0] != "" {
		variationParams.Size = &sizeValues[0]
	}
	if userValues := form.Value["user"]; len(userValues) > 0 && userValues[0] != "" {
		variationParams.User = &userValues[0]
	}
	if variationParams.ExtraParams == nil {
		variationParams.ExtraParams = make(map[string]interface{})
	}
	if len(images) > 1 {
		variationParams.ExtraParams["images"] = images[1:]
	}
	for key, value := range form.Value {
		if len(value) > 0 && value[0] != "" && !imageVariationParamsKnownFields[key] {
			variationParams.ExtraParams[key] = value[0]
		}
	}
	if fallbackValues := form.Value["fallbacks"]; len(fallbackValues) > 0 {
		fallbacks, err := parseFallbacks(fallbackValues)
		if err != nil {
			return nil, err
		}
		return &schemas.BifrostImageVariationRequest{
			Provider:       schemas.ModelProvider(provider),
			Model:          modelName,
			Input:          variationInput,
			Params:         variationParams,
			Fallbacks:      fallbacks,
			RawRequestBody: rawBody,
		}, nil
	}
	return &schemas.BifrostImageVariationRequest{
		Provider:       schemas.ModelProvider(provider),
		Model:          modelName,
		Input:          variationInput,
		Params:         variationParams,
		RawRequestBody: rawBody,
	}, nil
}

// imageVariation handles POST /v1/images/variations - Processes image variation requests
func (h *CompletionHandler) imageVariation(ctx *fasthttp.RequestCtx) {
	bifrostReq, err := prepareImageVariationRequest(ctx, h.config)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	defer cancel()

	// Execute request (no streaming for variations)
	resp, bifrostErr := h.client.ImageVariationRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// videoGeneration handles POST /v1/videos - Processes video generation requests
func (h *CompletionHandler) videoGeneration(ctx *fasthttp.RequestCtx) {
	var req VideoGenerationRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	provider, modelName, err := resolveModelAndProvider(ctx, h.config, req.Model)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	fallbacks, err := parseFallbacks(req.Fallbacks)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if req.VideoGenerationInput == nil || req.Prompt == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "prompt cannot be empty")
		return
	}

	if req.VideoGenerationParameters == nil {
		req.VideoGenerationParameters = &schemas.VideoGenerationParameters{}
	}

	extraParams, err := extractExtraParams(ctx.PostBody(), videoGenerationParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.VideoGenerationParameters.ExtraParams = extraParams
	}

	bifrostReq := &schemas.BifrostVideoGenerationRequest{
		Provider:  schemas.ModelProvider(provider),
		Model:     modelName,
		Input:     req.VideoGenerationInput,
		Params:    req.VideoGenerationParameters,
		Fallbacks: fallbacks,
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	if bifrostCtx == nil {
		cancel()
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	defer cancel()

	resp, bifrostErr := h.client.VideoGenerationRequest(bifrostCtx, bifrostReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// videoRetrieve handles GET /v1/videos/{video_id} - Retrieve a video generation job
func (h *CompletionHandler) videoRetrieve(ctx *fasthttp.RequestCtx) {
	// Get video ID from URL parameter
	videoID, ok := ctx.UserValue("video_id").(string)
	if !ok || videoID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id is required")
		return
	}

	// Decode URL-encoded video ID
	decodedID, err := url.PathUnescape(videoID)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid video_id encoding")
		return
	}
	idParts := strings.SplitN(decodedID, ":", 2)
	if len(idParts) != 2 || idParts[0] == "" || idParts[1] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id must be in id:provider format")
		return
	}

	provider := schemas.ModelProvider(idParts[1])

	// Build Bifrost video retrieve request
	bifrostVideoReq := &schemas.BifrostVideoRetrieveRequest{
		Provider: provider,
		ID:       idParts[0],
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.VideoRetrieveRequest(bifrostCtx, bifrostVideoReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// videoDownload handles GET /v1/videos/{video_id}/content - Download video content
func (h *CompletionHandler) videoDownload(ctx *fasthttp.RequestCtx) {
	// Get video ID from URL parameter
	videoID, ok := ctx.UserValue("video_id").(string)
	if !ok || videoID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id is required")
		return
	}

	// Decode URL-encoded video ID
	decodedID, err := url.PathUnescape(videoID)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid video_id encoding")
		return
	}
	idParts := strings.SplitN(decodedID, ":", 2)
	if len(idParts) != 2 || idParts[0] == "" || idParts[1] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id must be in id:provider format")
		return
	}

	// take variant from query parameters
	variant := string(ctx.QueryArgs().Peek("variant"))

	// Build Bifrost video download request
	bifrostVideoReq := &schemas.BifrostVideoDownloadRequest{
		Provider: schemas.ModelProvider(idParts[1]),
		ID:       idParts[0],
	}

	if variant != "" {
		bifrostVideoReq.Variant = schemas.Ptr(schemas.VideoDownloadVariant(variant))
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.VideoDownloadRequest(bifrostCtx, bifrostVideoReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}

	// Set appropriate headers for binary download
	ctx.Response.Header.Set("Content-Type", resp.ContentType)
	ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(resp.Content)))
	ctx.Response.SetBody(resp.Content)
}

// videoList handles GET /v1/videos - List video generation jobs
func (h *CompletionHandler) videoList(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter is required")
		return
	}

	// Build Bifrost video list request
	bifrostVideoReq := &schemas.BifrostVideoListRequest{
		Provider: schemas.ModelProvider(provider),
	}

	// Parse optional query parameters
	if afterBytes := ctx.QueryArgs().Peek("after"); len(afterBytes) > 0 {
		after := string(afterBytes)
		bifrostVideoReq.After = &after
	}

	if limitBytes := ctx.QueryArgs().Peek("limit"); len(limitBytes) > 0 {
		limit, err := strconv.Atoi(string(limitBytes))
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "invalid limit parameter")
			return
		}
		bifrostVideoReq.Limit = &limit
	}

	if orderBytes := ctx.QueryArgs().Peek("order"); len(orderBytes) > 0 {
		order := string(orderBytes)
		bifrostVideoReq.Order = &order
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.VideoListRequest(bifrostCtx, bifrostVideoReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// videoDelete handles DELETE /v1/videos/{video_id} - Delete a video generation job
func (h *CompletionHandler) videoDelete(ctx *fasthttp.RequestCtx) {
	// Get video ID from URL parameter
	videoID, ok := ctx.UserValue("video_id").(string)
	if !ok || videoID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id is required")
		return
	}

	// Decode URL-encoded video ID
	decodedID, err := url.PathUnescape(videoID)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid video_id encoding")
		return
	}
	idParts := strings.SplitN(decodedID, ":", 2)
	if len(idParts) != 2 || idParts[0] == "" || idParts[1] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id must be in id:provider format")
		return
	}

	// Build Bifrost video delete request
	bifrostVideoReq := &schemas.BifrostVideoDeleteRequest{
		Provider: schemas.ModelProvider(idParts[1]),
		ID:       idParts[0],
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.VideoDeleteRequest(bifrostCtx, bifrostVideoReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// videoRemix handles POST /v1/videos/{video_id}/remix - Remix an existing video
func (h *CompletionHandler) videoRemix(ctx *fasthttp.RequestCtx) {
	// Get video ID from URL parameter
	videoID, ok := ctx.UserValue("video_id").(string)
	if !ok || videoID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id is required")
		return
	}

	// Decode URL-encoded video ID
	decodedID, err := url.PathUnescape(videoID)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid video_id encoding")
		return
	}
	idParts := strings.SplitN(decodedID, ":", 2)
	if len(idParts) != 2 || idParts[0] == "" || idParts[1] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "video_id must be in id:provider format")
		return
	}

	// Parse request body
	var req VideoRemixRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	// Validate prompt
	if req.VideoGenerationInput == nil || req.VideoGenerationInput.Prompt == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "prompt is required")
		return
	}

	provider := schemas.ModelProvider(idParts[1])

	extraParams, err := extractExtraParams(ctx.PostBody(), videoRemixParamsKnownFields)
	if err != nil {
		logger.Warn("Failed to extract extra params: %v", err)
	} else {
		req.ExtraParams = extraParams
	}

	// Build Bifrost video remix request
	bifrostVideoReq := &schemas.BifrostVideoRemixRequest{
		Provider: provider,
		ID:       idParts[0],
		Input: &schemas.VideoGenerationInput{
			Prompt: req.VideoGenerationInput.Prompt,
		},
		ExtraParams: req.ExtraParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.VideoRemixRequest(bifrostCtx, bifrostVideoReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// resolveBatchProvider resolves the provider (and optional model) for a batch
// create request. Per the OpenAI spec, model is optional on POST /v1/batches —
// it lives inside each JSONL request body. When model is present it is parsed
// via resolveModelAndProvider; when absent the provider is taken from the
// ?provider= query param or x-model-provider header (same as fileUpload).
func resolveBatchProvider(ctx *fasthttp.RequestCtx, config *lib.Config, model string) (schemas.ModelProvider, string, error) {
	if model != "" {
		return resolveModelAndProvider(ctx, config, model)
	}
	p := string(ctx.QueryArgs().Peek("provider"))
	if p == "" {
		p = string(ctx.Request.Header.Peek("x-model-provider"))
	}
	if p == "" {
		return "", "", fmt.Errorf("provider query parameter or x-model-provider header is required when model is not specified")
	}
	return schemas.ModelProvider(p), "", nil
}

// batchCreate handles POST /v1/batches - Create a new batch job
func (h *CompletionHandler) batchCreate(ctx *fasthttp.RequestCtx) {
	var req BatchCreateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	// model is optional on POST /v1/batches per the OpenAI spec — the model lives
	// inside each JSONL request body. When omitted, resolve the provider from the
	// x-model-provider header or ?provider= query param (same as fileUpload).
	provider, modelName, err := resolveBatchProvider(ctx, h.config, req.Model)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// Validate that at least one of InputFileID or InputBlob or Requests is provided
	hasInputBlob := req.InputBlob != nil && strings.TrimSpace(*req.InputBlob) != ""
	if req.InputFileID == "" && len(req.Requests) == 0 && !hasInputBlob {
		SendError(ctx, fasthttp.StatusBadRequest, "either input_file_id, input_blob, or requests is required")
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
		InputBlob:        req.InputBlob,
		OutputFolder:     req.OutputFolder,
		DisplayName:      req.DisplayName,
		Requests:         req.Requests,
		Endpoint:         schemas.BatchEndpoint(req.Endpoint),
		CompletionWindow: req.CompletionWindow,
		Metadata:         req.Metadata,
		ExtraParams:      extraParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchCreateRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchListRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	// Decode percent-encoding so ARN ids (e.g. Bedrock job ARNs containing a
	// slash sent as %2F) reach the provider raw, not double-encoded.
	if decoded, err := url.PathUnescape(batchID); err == nil {
		batchID = decoded
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchRetrieveRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	// Decode percent-encoding so ARN ids (e.g. Bedrock job ARNs containing a
	// slash sent as %2F) reach the provider raw, not double-encoded.
	if decoded, err := url.PathUnescape(batchID); err == nil {
		batchID = decoded
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchCancelRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	// Decode percent-encoding so ARN ids (e.g. Bedrock job ARNs containing a
	// slash sent as %2F) reach the provider raw, not double-encoded.
	if decoded, err := url.PathUnescape(batchID); err == nil {
		batchID = decoded
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.BatchResultsRequest(bifrostCtx, bifrostBatchReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// encodeStorageFileID makes a storage-URI file id (gs://...) opaque and path-safe so
// callers can use it in retrieve/delete/content without percent-encoding slashes.
// Non-URI ids (OpenAI/Gemini/Anthropic) pass through unchanged. The response's
// storage_uri still carries the raw gs:// URI for direct use (e.g. inference).
func encodeStorageFileID(id string) string {
	if strings.HasPrefix(id, "gs://") {
		return base64.RawURLEncoding.EncodeToString([]byte(id))
	}
	return id
}

// decodeStorageFileID reverses encodeStorageFileID. PathUnescape first (harmless for
// the unreserved RawURL alphabet, and tolerant of callers that percent-encode), then
// base64-decode the opaque gs:// id. Raw or percent-encoded gs:// ids passed directly
// also work: PathUnescape yields the gs:// URI and the base64 step is skipped (a
// gs:// string is not valid base64).
func decodeStorageFileID(id string) string {
	if unescaped, err := url.PathUnescape(id); err == nil {
		id = unescaped
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(id); err == nil && strings.HasPrefix(string(decoded), "gs://") {
		return string(decoded)
	}
	return id
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

	// Extract file (optional for providers that support resumable uploads, e.g. Vertex/GCS;
	// when omitted, the provider mints an upload session URL instead of receiving bytes)
	var fileData []byte
	var filename string
	var filePartContentType string
	fileHeaders := form.File["file"]
	if len(fileHeaders) > 0 {
		fileHeader := fileHeaders[0]
		filename = fileHeader.Filename
		filePartContentType = strings.TrimSpace(fileHeader.Header.Get("Content-Type"))

		// Open and read the file
		file, err := fileHeader.Open()
		if err != nil {
			logger.Warn("Failed to open uploaded file: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
			return
		}
		defer file.Close()

		// Read file data
		fileData, err = io.ReadAll(file)
		if err != nil {
			logger.Warn("Failed to read uploaded file: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
			return
		}
	} else if len(form.Value["filename"]) > 0 {
		filename = form.Value["filename"][0]
	}

	// Extract content type (used for resumable upload sessions and stored object metadata)
	var contentType *string
	if len(form.Value["content_type"]) > 0 && form.Value["content_type"][0] != "" {
		contentType = &form.Value["content_type"][0]
	} else if filePartContentType != "" {
		contentType = &filePartContentType
	}

	// GCS storage location for Vertex uploads: sent as individual multipart fields,
	// parsed into the typed StorageConfig rather than passed opaquely via extra_params.
	var storageConfig *schemas.FileStorageConfig
	if len(form.Value["gcs_bucket"]) > 0 && form.Value["gcs_bucket"][0] != "" {
		gcs := &schemas.GCSStorageConfig{Bucket: form.Value["gcs_bucket"][0]}
		if len(form.Value["gcs_prefix"]) > 0 {
			gcs.Prefix = form.Value["gcs_prefix"][0]
		}
		storageConfig = &schemas.FileStorageConfig{GCS: gcs}
	}

	// Collect unknown form fields as extra params (multipart — cannot use extractExtraParams which expects JSON).
	// gcs_bucket/gcs_prefix are consumed into StorageConfig above; other providers (e.g. Bedrock s3_bucket) still flow through here.
	fileUploadKnownFields := map[string]bool{"file": true, "purpose": true, "provider": true, "filename": true, "content_type": true, "gcs_bucket": true, "gcs_prefix": true}
	extraParams := map[string]interface{}{}
	for k, vals := range form.Value {
		if !fileUploadKnownFields[k] && len(vals) > 0 && vals[0] != "" {
			extraParams[k] = vals[0]
		}
	}

	// Build Bifrost file upload request
	bifrostFileReq := &schemas.BifrostFileUploadRequest{
		Provider:      schemas.ModelProvider(provider),
		File:          fileData,
		Filename:      filename,
		Purpose:       schemas.FilePurpose(purpose),
		ContentType:   contentType,
		StorageConfig: storageConfig,
		ExtraParams:   extraParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileUploadRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil {
		resp.ID = encodeStorageFileID(resp.ID)
		if resp.ExtraFields.ProviderResponseHeaders != nil {
			forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
		}
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}

// fileList handles GET /v1/files - List files
func (h *CompletionHandler) fileList(ctx *fasthttp.RequestCtx) {
	// Get provider from query parameters or header; accept both ?provider= and
	// ?x-model-provider= for consistency with other file endpoints (#3963).
	provider := string(ctx.QueryArgs().Peek("provider"))
	if provider == "" {
		provider = string(ctx.QueryArgs().Peek("x-model-provider"))
	}
	if provider == "" {
		provider = string(ctx.Request.Header.Peek("x-model-provider"))
	}
	if provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "provider query parameter or x-model-provider header is required")
		return
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

	// GCS storage location for Vertex listing: parsed into the typed StorageConfig
	// rather than passed opaquely via extra_params.
	var storageConfig *schemas.FileStorageConfig
	if gcsBucket := string(ctx.QueryArgs().Peek("gcs_bucket")); gcsBucket != "" {
		storageConfig = &schemas.FileStorageConfig{GCS: &schemas.GCSStorageConfig{
			Bucket: gcsBucket,
			Prefix: string(ctx.QueryArgs().Peek("gcs_prefix")),
		}}
	}

	// Collect unknown query args as extra params. gcs_bucket/gcs_prefix are consumed into
	// StorageConfig above; other providers (e.g. Bedrock s3_bucket) still flow through here.
	fileListKnownArgs := map[string]bool{"provider": true, "x-model-provider": true, "purpose": true, "limit": true, "after": true, "order": true, "gcs_bucket": true, "gcs_prefix": true}
	extraParams := map[string]interface{}{}
	ctx.QueryArgs().VisitAll(func(k, v []byte) {
		if argKey := string(k); !fileListKnownArgs[argKey] && len(v) > 0 {
			extraParams[argKey] = string(v)
		}
	})

	// Build Bifrost file list request
	bifrostFileReq := &schemas.BifrostFileListRequest{
		Provider:      schemas.ModelProvider(provider),
		Purpose:       schemas.FilePurpose(purpose),
		Limit:         limit,
		After:         after,
		Order:         order,
		StorageConfig: storageConfig,
		ExtraParams:   extraParams,
	}

	// Convert context
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileListRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil {
		for i := range resp.Data {
			resp.Data[i].ID = encodeStorageFileID(resp.Data[i].ID)
		}
		if resp.ExtraFields.ProviderResponseHeaders != nil {
			forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
		}
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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

	// Vertex returns an opaque base64(gs://) id so callers don't percent-encode
	// slashes in the path; decode it back (falls back to percent-decoding for raw
	// or percent-encoded gs:// / s3:// ids passed directly).
	fileID = decodeStorageFileID(fileID)

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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileRetrieveRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil {
		resp.ID = encodeStorageFileID(resp.ID)
		if resp.ExtraFields.ProviderResponseHeaders != nil {
			forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
		}
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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

	// Vertex returns an opaque base64(gs://) id so callers don't percent-encode
	// slashes in the path; decode it back (falls back to percent-decoding for raw
	// or percent-encoded gs:// / s3:// ids passed directly).
	fileID = decodeStorageFileID(fileID)

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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileDeleteRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil {
		resp.ID = encodeStorageFileID(resp.ID)
		if resp.ExtraFields.ProviderResponseHeaders != nil {
			forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
		}
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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

	// Vertex returns an opaque base64(gs://) id so callers don't percent-encode
	// slashes in the path; decode it back (falls back to percent-decoding for raw
	// or percent-encoded gs:// / s3:// ids passed directly).
	fileID = decodeStorageFileID(fileID)

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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}

	resp, bifrostErr := h.client.FileContentRequest(bifrostCtx, bifrostFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerCreateRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerListRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerRetrieveRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerDeleteRequest(bifrostCtx, bifrostContainerReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
			SendError(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
			return
		}
		defer file.Close()

		fileContent, err := io.ReadAll(file)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileCreateRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileListRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileRetrieveRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileContentRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if streamLargeResponseIfActive(ctx, bifrostCtx) {
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
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.config)
	defer cancel()
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Failed to convert context")
		return
	}
	enableRawRequestResponseForContainer(bifrostCtx)

	resp, bifrostErr := h.client.ContainerFileDeleteRequest(bifrostCtx, bifrostContainerFileReq)
	if bifrostErr != nil {
		forwardProviderHeadersFromContext(ctx, bifrostCtx)
		SendBifrostError(ctx, bifrostErr)
		return
	}

	if resp != nil && resp.ExtraFields.ProviderResponseHeaders != nil {
		forwardProviderHeaders(ctx, resp.ExtraFields.ProviderResponseHeaders)
	}
	if streamLargeResponseIfActive(ctx, bifrostCtx) {
		return
	}
	SendJSON(ctx, resp)
}
