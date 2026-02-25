package integrations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/vertex"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const isGeminiEmbedContentRequestContextKey schemas.BifrostContextKey = "bifrost-is-gemini-embed-content-request"

const isGeminiVideoGenerationRequestContextKey schemas.BifrostContextKey = "bifrost-is-gemini-video-generation-request"

const isGeminiBatchCreateRequestContextKey schemas.BifrostContextKey = "bifrost-is-gemini-batch-create-request"

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	*GenericRouter
}

// CreateGenAIRouteConfigs creates a route configurations for GenAI endpoints.
func CreateGenAIRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// Video operation retrieve endpoint
	// Example: /v1beta/models/veo-3.1-generate-preview/operations/{operation_id:*}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models/{model}/operations/{operation_id:*}",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.VideoRetrieveRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostVideoRetrieveRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if videoRetrieveReq, ok := req.(*schemas.BifrostVideoRetrieveRequest); ok {
				return &schemas.BifrostRequest{
					VideoRetrieveRequest: videoRetrieveReq,
				}, nil
			}
			return nil, errors.New("invalid video retrieve request type")
		},
		VideoGenerationResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostVideoGenerationResponse) (interface{}, error) {
			return gemini.ToGeminiVideoGenerationResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiVideoOperationFromPath,
	})

	// Chat completions endpoint
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models/{model:*}",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			_, requestType := extractModelAndRequestType(ctx)
			return requestType
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			if requestType, ok := ctx.Value(schemas.BifrostContextKeyHTTPRequestType).(schemas.RequestType); ok && requestType == schemas.EmbeddingRequest && ctx.Value(isGeminiEmbedContentRequestContextKey) != nil {
				return &gemini.GeminiEmbeddingRequest{}
			}
			if requestType, ok := ctx.Value(schemas.BifrostContextKeyHTTPRequestType).(schemas.RequestType); ok && requestType == schemas.VideoGenerationRequest && ctx.Value(isGeminiVideoGenerationRequestContextKey) != nil {
				return &gemini.GeminiVideoGenerationRequest{}
			}
			if requestType, ok := ctx.Value(schemas.BifrostContextKeyHTTPRequestType).(schemas.RequestType); ok && requestType == schemas.BatchCreateRequest && ctx.Value(isGeminiBatchCreateRequestContextKey) != nil {
				return &gemini.GeminiBatchCreateRequest{}
			}
			return &gemini.GeminiGenerationRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiGenerationRequest); ok {
				if geminiReq.IsCountTokens {
					return &schemas.BifrostRequest{
						CountTokensRequest: geminiReq.ToBifrostResponsesRequest(ctx),
					}, nil
				} else if geminiReq.IsEmbedding {
					return &schemas.BifrostRequest{
						EmbeddingRequest: geminiReq.ToBifrostEmbeddingRequest(ctx),
					}, nil
				} else if geminiReq.IsSpeech {
					return &schemas.BifrostRequest{
						SpeechRequest: geminiReq.ToBifrostSpeechRequest(ctx),
					}, nil
				} else if geminiReq.IsTranscription {
					transcriptionReq, err := geminiReq.ToBifrostTranscriptionRequest(ctx)
					if err != nil {
						return nil, err
					}
					return &schemas.BifrostRequest{TranscriptionRequest: transcriptionReq}, nil
				} else if geminiReq.IsImageGeneration {
					return &schemas.BifrostRequest{
						ImageGenerationRequest: geminiReq.ToBifrostImageGenerationRequest(ctx),
					}, nil
				} else if geminiReq.IsImageEdit {
					return &schemas.BifrostRequest{
						ImageEditRequest: geminiReq.ToBifrostImageEditRequest(ctx),
					}, nil
				} else {
					return &schemas.BifrostRequest{
						ResponsesRequest: geminiReq.ToBifrostResponsesRequest(ctx),
					}, nil
				}
			} else if geminiReq, ok := req.(*gemini.GeminiEmbeddingRequest); ok {
				req := &gemini.GeminiGenerationRequest{
					Model:    geminiReq.Model,
					Requests: []gemini.GeminiEmbeddingRequest{*geminiReq},
				}
				return &schemas.BifrostRequest{
					EmbeddingRequest: req.ToBifrostEmbeddingRequest(ctx),
				}, nil
			} else if geminiReq, ok := req.(*gemini.GeminiVideoGenerationRequest); ok {
				// convert to bifrost video generation request
				bifrostReq, err := geminiReq.ToBifrostVideoGenerationRequest(ctx)
				if err != nil {
					return nil, err
				}
				return &schemas.BifrostRequest{
					VideoGenerationRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiBatchCreateRequest); ok {
				// Get provider from context
				provider, ok := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if !ok {
					provider = schemas.Gemini
				}

				// Convert Gemini batch request items directly to Bifrost format
				var requests []schemas.BatchRequestItem
				if geminiReq.Batch.InputConfig.Requests != nil && len(geminiReq.Batch.InputConfig.Requests.Requests) > 0 {
					requests = make([]schemas.BatchRequestItem, len(geminiReq.Batch.InputConfig.Requests.Requests))
					for i, geminiItem := range geminiReq.Batch.InputConfig.Requests.Requests {
						requestMap := make(map[string]interface{})
						if geminiItem.Request.Contents != nil {
							requestMap["contents"] = geminiItem.Request.Contents
						}
						if geminiItem.Request.GenerationConfig != nil {
							requestMap["generationConfig"] = geminiItem.Request.GenerationConfig
						}
						if len(geminiItem.Request.SafetySettings) > 0 {
							requestMap["safetySettings"] = geminiItem.Request.SafetySettings
						}
						if geminiItem.Request.SystemInstruction != nil {
							requestMap["systemInstruction"] = geminiItem.Request.SystemInstruction
						}

						requests[i] = schemas.BatchRequestItem{
							CustomID: "",
							Body:     requestMap,
						}
						// Extract custom_id from metadata
						if geminiItem.Metadata != nil && geminiItem.Metadata.Key != "" {
							requests[i].CustomID = geminiItem.Metadata.Key
						}
					}
				}

				bifrostBatchReq := &schemas.BifrostBatchCreateRequest{
					Provider: provider,
					Requests: requests,
				}

				// Handle file-based input
				if geminiReq.Batch.InputConfig.FileName != "" {
					bifrostBatchReq.InputFileID = geminiReq.Batch.InputConfig.FileName
				}

				return &BatchRequest{
					Type:          schemas.BatchCreateRequest,
					CreateRequest: bifrostBatchReq,
				}, nil
			}
			return nil, errors.New("invalid batch create request type")
		},
		EmbeddingResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
			return gemini.ToGeminiEmbeddingResponse(resp), nil
		},
		ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
			return gemini.ToGeminiResponsesResponse(resp), nil
		},
		SpeechResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechResponse) (interface{}, error) {
			return gemini.ToGeminiSpeechResponse(resp), nil
		},
		TranscriptionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
			return gemini.ToGeminiTranscriptionResponse(resp), nil
		},
		CountTokensResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostCountTokensResponse) (interface{}, error) {
			return gemini.ToGeminiCountTokensResponse(resp), nil
		},
		ImageGenerationResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationResponse) (interface{}, error) {
			return gemini.ToGeminiImageGenerationResponse(ctx, resp)
		},
		VideoGenerationResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostVideoGenerationResponse) (interface{}, error) {
			return gemini.ToGeminiVideoGenerationResponse(resp), nil
		},
		BatchCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCreateResponse) (interface{}, error) {
			return gemini.ToGeminiBatchJobResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		StreamConfig: &StreamConfig{
			ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
				// Store state in context so it persists across chunks of the same stream
				const stateKey = "gemini_stream_state"
				var state *gemini.BifrostToGeminiStreamState

				if stateValue := ctx.Value(stateKey); stateValue != nil {
					state = stateValue.(*gemini.BifrostToGeminiStreamState)
				} else {
					state = gemini.NewBifrostToGeminiStreamState()
					ctx.SetValue(stateKey, state)
				}

				geminiResponse := gemini.ToGeminiResponsesStreamResponse(resp, state)
				if geminiResponse == nil {
					return "", nil, nil
				}
				return "", geminiResponse, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return gemini.ToGeminiError(err)
			},
		},
		PreCallback: extractAndSetModelAndRequestType,
	})

	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ListModelsRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostListModelsRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
				return &schemas.BifrostRequest{
					ListModelsRequest: listModelsReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ListModelsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
			return gemini.ToGeminiListModelsResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiListModelsParams,
	})

	routes = append(routes, createGenAIRerankRouteConfig(pathPrefix))

	return routes
}

// CreateGenAIFileRouteConfigs creates route configurations for Gemini Files API endpoints.
func CreateGenAIFileRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Upload file endpoint - POST /upload/v1beta/files
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/upload/v1beta/files",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileUploadRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &gemini.GeminiFileUploadHandlerReq{}
		},
		// RequestParser detects which step we are in and populates the request
		// object accordingly without trying to JSON-decode binary payloads.
		RequestParser: func(ctx *fasthttp.RequestCtx, req interface{}) error {
			r := req.(*gemini.GeminiFileUploadHandlerReq)
			uploadID := string(ctx.QueryArgs().Peek("upload_id"))
			if uploadID != "" {
				// Step 2: body is raw binary — do not JSON-decode.
				r.UploadID = uploadID
				r.FileData = ctx.Request.Body()
				return nil
			}
			// Step 1: body is JSON metadata.
			if body := ctx.Request.Body(); len(body) > 0 {
				return sonic.Unmarshal(body, r)
			}
			return nil
		},
		// ShortCircuit handles step 1 for non-Gemini providers: it acknowledges
		// the initiation by returning a synthetic upload URL and exits early so
		// that no Bifrost call is made.
		ShortCircuit: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) (bool, error) {
			r, ok := req.(*gemini.GeminiFileUploadHandlerReq)
			if !ok || r.UploadID != "" {
				return false, nil
			}

			uploadID, err := generateUploadID()
			if err != nil {
				return true, fmt.Errorf("failed to generate upload ID: %w", err)
			}

			kvStore := handlerStore.GetKVStore()
			if kvStore == nil {
				return true, errors.New("kvstore not initialized")
			}

			session := &gemini.GeminiResumableUploadSession{
				DisplayName: r.File.DisplayName,
				MimeType:    r.MimeType,
				Provider:    r.Provider,
			}
			if err := kvStore.Set(uploadID, session); err != nil {
				return true, fmt.Errorf("failed to store upload session: %w", err)
			}

			scheme := "http"
			if ctx.IsTLS() {
				scheme = "https"
			}
			uploadURL := scheme + "://" + string(ctx.Host()) + pathPrefix + "/upload/v1beta/files?upload_id=" + uploadID

			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.Response.Header.Set("X-Goog-Upload-URL", uploadURL)
			ctx.Response.Header.Set("X-Goog-Upload-Status", "active")
			ctx.SetContentType("application/json")
			ctx.SetBody([]byte("{}"))
			return true, nil
		},
		// FileRequestConverter handles step 2: retrieves the saved session from
		// the KV store and builds a full BifrostFileUploadRequest.
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			r, ok := req.(*gemini.GeminiFileUploadHandlerReq)
			if !ok {
				return nil, errors.New("invalid file upload request type")
			}
			if r.UploadID == "" {
				return nil, errors.New("upload_id missing — step 1 should have been short-circuited")
			}

			kvStore := handlerStore.GetKVStore()
			if kvStore == nil {
				return nil, errors.New("kvstore not initialized")
			}

			val, err := kvStore.GetAndDelete(r.UploadID)
			if err != nil {
				return nil, fmt.Errorf("upload session not found for id %q: %w", r.UploadID, err)
			}

			session, ok := val.(*gemini.GeminiResumableUploadSession)
			if !ok {
				return nil, errors.New("invalid upload session type in kvstore")
			}

			filename := session.DisplayName
			if filename == "" {
				filename = "upload"
			}
			contentType := session.MimeType

			return &FileRequest{
				Type: schemas.FileUploadRequest,
				UploadRequest: &schemas.BifrostFileUploadRequest{
					Provider:    session.Provider,
					File:        r.FileData,
					Filename:    filename,
					ContentType: &contentType,
					Purpose:     schemas.FilePurposeBatch,
				},
			}, nil
		},
		FileUploadResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileUploadResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileUploadParams,
		PostCallback: func(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
			ctx.Response.Header.Set("X-Goog-Upload-Status", "final")
			return nil
		},
	})

	// List files endpoint - GET /v1beta/files
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileListRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostFileListRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if listReq, ok := req.(*schemas.BifrostFileListRequest); ok {
				return &FileRequest{
					Type:        schemas.FileListRequest,
					ListRequest: listReq,
				}, nil
			}
			return nil, errors.New("invalid file list request type")
		},
		FileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileListResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileListQueryParams,
	})

	// Retrieve file endpoint - GET /v1beta/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files/{file_id}",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileRetrieveRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostFileRetrieveRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if retrieveReq, ok := req.(*schemas.BifrostFileRetrieveRequest); ok {
				return &FileRequest{
					Type:            schemas.FileRetrieveRequest,
					RetrieveRequest: retrieveReq,
				}, nil
			}
			return nil, errors.New("invalid file retrieve request type")
		},
		FileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileIDFromPath,
	})

	// Delete file endpoint - DELETE /v1beta/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files/{file_id}",
		Method: "DELETE",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileDeleteRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostFileDeleteRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if deleteReq, ok := req.(*schemas.BifrostFileDeleteRequest); ok {
				return &FileRequest{
					Type:          schemas.FileDeleteRequest,
					DeleteRequest: deleteReq,
				}, nil
			}
			return nil, errors.New("invalid file delete request type")
		},
		FileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return map[string]interface{}{}, nil // Gemini returns empty response on delete
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileIDFromPath,
	})

	return routes
}

func CreateGenAIBatchRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig
	// List batches endpoint - GET /v1beta/batches
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchListRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostBatchListRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if listReq, ok := req.(*schemas.BifrostBatchListRequest); ok {
				return &BatchRequest{
					Type:        schemas.BatchListRequest,
					ListRequest: listReq,
				}, nil
			}
			return nil, errors.New("invalid batch list request type")
		},
		BatchListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchListResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiBatchListResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileListQueryParams,
	})
	// Retrieve batch endpoint - GET /v1beta/batches/{batch_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches/{batch_id}",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchRetrieveRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostBatchRetrieveRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if retrieveReq, ok := req.(*schemas.BifrostBatchRetrieveRequest); ok {
				return &BatchRequest{
					Type:            schemas.BatchRetrieveRequest,
					RetrieveRequest: retrieveReq,
				}, nil
			}
			return nil, errors.New("invalid batch retrieve request type")
		},
		BatchRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiBatchRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchIDFromPath,
	})
	// Retrieve batch endpoint - POST /v1beta/batches/{batch_id}:cancel
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches/{batch_id}",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchCancelRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostBatchCancelRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if cancelReq, ok := req.(*schemas.BifrostBatchCancelRequest); ok {
				return &BatchRequest{
					Type:          schemas.BatchCancelRequest,
					CancelRequest: cancelReq,
				}, nil
			}
			return nil, errors.New("invalid batch cancel request type")
		},
		BatchCancelResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCancelResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return map[string]interface{}{}, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchIDFromPath,
	})
	// Delete batch endpoint - DELETE /v1beta/batches/{batch_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches/{batch_id}",
		Method: "DELETE",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchDeleteRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostBatchDeleteRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if deleteReq, ok := req.(*schemas.BifrostBatchDeleteRequest); ok {
				return &BatchRequest{
					Type:          schemas.BatchDeleteRequest,
					DeleteRequest: deleteReq,
				}, nil
			}
			return nil, errors.New("invalid batch delete request type")
		},
		BatchDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchDeleteResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return map[string]interface{}{}, nil // Gemini returns empty response on delete
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchIDFromPath,
	})

	return routes
}

// extractGeminiBatchIDFromPath extracts batch_id from path parameters for Gemini
func extractGeminiBatchIDFromPath(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	provider := getProviderFromHeader(ctx, schemas.Gemini)

	batchID := ctx.UserValue("batch_id")
	if batchID == nil {
		return errors.New("batch_id is required")
	}

	batchIDStr, ok := batchID.(string)
	if !ok || batchIDStr == "" {
		return errors.New("batch_id must be a non-empty string")
	}

	// strip :cancel from batchID
	batchIDStr = strings.TrimSuffix(batchIDStr, ":cancel")

	switch r := req.(type) {
	case *schemas.BifrostBatchCancelRequest:
		r.BatchID = batchIDStr
		r.Provider = provider
	case *schemas.BifrostBatchRetrieveRequest:
		r.BatchID = batchIDStr
		r.Provider = provider
	case *schemas.BifrostBatchDeleteRequest:
		r.BatchID = batchIDStr
		r.Provider = provider
	}

	return nil
}

// extractGeminiFileListQueryParams extracts query parameters for Gemini file list requests
func extractGeminiFileListQueryParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	provider := getProviderFromHeader(ctx, schemas.Gemini)

	if listReq, ok := req.(*schemas.BifrostFileListRequest); ok {
		listReq.Provider = provider

		// Extract pageSize from query parameters
		if pageSizeStr := string(ctx.QueryArgs().Peek("pageSize")); pageSizeStr != "" {
			if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
				listReq.Limit = pageSize
			}
		}

		// Extract pageToken from query parameters
		if pageToken := string(ctx.QueryArgs().Peek("pageToken")); pageToken != "" {
			listReq.After = &pageToken
		}
	} else if listReq, ok := req.(*schemas.BifrostBatchListRequest); ok {
		listReq.Provider = provider

		// Extract pageSize from query parameters
		if pageSizeStr := string(ctx.QueryArgs().Peek("pageSize")); pageSizeStr != "" {
			if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
				listReq.Limit = pageSize
			}
		}

		// Extract pageToken from query parameters
		if pageToken := string(ctx.QueryArgs().Peek("pageToken")); pageToken != "" {
			listReq.After = &pageToken
		}
	}

	return nil
}

// extractGeminiFileIDFromPath extracts file_id from path parameters for Gemini
func extractGeminiFileIDFromPath(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	provider := getProviderFromHeader(ctx, schemas.Gemini)

	fileID := ctx.UserValue("file_id")
	if fileID == nil {
		return errors.New("file_id is required")
	}

	fileIDStr, ok := fileID.(string)
	if !ok || fileIDStr == "" {
		return errors.New("file_id must be a non-empty string")
	}

	switch r := req.(type) {
	case *schemas.BifrostFileRetrieveRequest:
		r.FileID = fileIDStr
		r.Provider = provider
	case *schemas.BifrostFileDeleteRequest:
		r.FileID = fileIDStr
		r.Provider = provider
	}

	return nil
}

// extractGeminiFileUploadParams populates provider and MIME-type fields on the
// upload handler request from HTTP headers.
func extractGeminiFileUploadParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	provider := getProviderFromHeader(ctx, schemas.Gemini)

	if r, ok := req.(*gemini.GeminiFileUploadHandlerReq); ok {
		r.Provider = provider
		r.MimeType = string(ctx.Request.Header.Peek("x-goog-upload-header-content-type"))
	}

	return nil
}

// createGenAIRerankRouteConfig creates a route configuration for the GenAI/Vertex Rerank API endpoint
// Handles POST /genai/v1/rank
func createGenAIRerankRouteConfig(pathPrefix string) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1/rank",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.RerankRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &vertex.VertexRankRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if vertexReq, ok := req.(*vertex.VertexRankRequest); ok {
				return &schemas.BifrostRequest{
					RerankRequest: vertexReq.ToBifrostRerankRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid rerank request type")
		},
		RerankResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostRerankResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Vertex {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
	}
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *GenAIRouter {
	routes := CreateGenAIRouteConfigs("/genai")
	routes = append(routes, CreateGenAIFileRouteConfigs("/genai", handlerStore)...)
	routes = append(routes, CreateGenAIBatchRouteConfigs("/genai", handlerStore)...)

	passThroughCfg := &PassthroughConfig{
		Provider:         schemas.Gemini,
		ProviderDetector: detectProviderFromGenAIRequest,
		StripPrefix:      []string{"/genai/v1beta1", "/genai/v1beta", "/genai/v1", "/genai"},
	}

	return &GenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, passThroughCfg, logger),
	}
}

var embeddingPaths = []string{
	":embedContent",
	":batchEmbedContents",
}

// extractAndSetModelAndRequestType extracts model and request type from URL and request object and sets it in the request
func extractAndSetModelAndRequestType(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return fmt.Errorf("model parameter is required")
	}

	provider := getProviderFromHeader(ctx, schemas.Gemini)
	// set in context
	bifrostCtx.SetValue(bifrostContextKeyProvider, provider)

	modelStr := model.(string)

	// Check if this is a :predict endpoint (can be embedding or image generation)
	isPredict := strings.HasSuffix(modelStr, ":predict")

	// Check if this is an embedding request
	isEmbedding := false
	for _, path := range embeddingPaths {
		if strings.HasSuffix(modelStr, path) {
			isEmbedding = true
			break
		}
	}

	// Check if this is a streaming request
	isStreaming := strings.HasSuffix(modelStr, ":streamGenerateContent")

	// Check if this is a count tokens request
	isCountTokens := strings.HasSuffix(modelStr, ":countTokens")

	// Remove Google GenAI API endpoint suffixes if present
	for _, sfx := range gemini.GeminiRequestSuffixPaths {
		modelStr = strings.TrimSuffix(modelStr, sfx)
	}

	// Remove trailing colon if present
	if len(modelStr) > 0 && modelStr[len(modelStr)-1] == ':' {
		modelStr = modelStr[:len(modelStr)-1]
	}

	// Determine if :predict is for image generation (Imagen) or embedding
	// Imagen models use :predict for image generation
	isImagenPredict := isPredict && schemas.IsImagenModel(modelStr)
	if isPredict && !isImagenPredict {
		// :predict for non-Imagen models is embedding
		isEmbedding = true
	}

	// Set the model and flags in the request
	switch r := req.(type) {
	case *gemini.GeminiGenerationRequest:
		r.Model = modelStr
		r.Stream = isStreaming
		r.IsEmbedding = isEmbedding
		r.IsCountTokens = isCountTokens

		// Detect if this is a speech or transcription request by examining the request body
		// Speech detection takes priority over transcription
		r.IsSpeech = isSpeechRequest(r)
		r.IsTranscription = isTranscriptionRequest(r)

		// Detect if this is an image generation request
		// isImagenPredict takes precedence for :predict endpoints
		r.IsImageGeneration = (isImagenPredict && !isImageEditRequest(r)) || isImageGenerationRequest(r)
		r.IsImageEdit = isImageEditRequest(r)

		return nil
	case *gemini.GeminiEmbeddingRequest:
		if modelStr != "" {
			r.Model = modelStr
		}
		return nil
	case *gemini.GeminiVideoGenerationRequest:
		if modelStr != "" {
			r.Model = modelStr
		}
		return nil
	case *gemini.GeminiBatchCreateRequest:
		return nil
	}

	return fmt.Errorf("invalid request type for GenAI")
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractModelAndRequestType(ctx *fasthttp.RequestCtx) (string, schemas.RequestType) {
	model := ctx.UserValue("model")
	if model == nil {
		return "", ""
	}

	modelStr := model.(string)

	// Check if this is a count tokens request
	if strings.HasSuffix(modelStr, ":countTokens") {
		return modelStr, schemas.CountTokensRequest
	}

	isPredict := strings.HasSuffix(modelStr, ":predict")
	isVideoGeneration := strings.HasSuffix(modelStr, ":predictLongRunning")
	isBatchCreate := strings.HasSuffix(modelStr, ":batchGenerateContent")

	// Check if this is an embedding request
	isEmbedding := false
	for _, path := range embeddingPaths {
		if strings.HasSuffix(modelStr, path) {
			isEmbedding = true
			break
		}
	}
	if strings.HasSuffix(modelStr, ":embedContent") {
		ctx.SetUserValue(isGeminiEmbedContentRequestContextKey, true)
	}
	if isEmbedding {
		return modelStr, schemas.EmbeddingRequest
	}

	if isVideoGeneration {
		ctx.SetUserValue(isGeminiVideoGenerationRequestContextKey, true)
		return modelStr, schemas.VideoGenerationRequest
	}

	if isBatchCreate {
		ctx.SetUserValue(isGeminiBatchCreateRequestContextKey, true)
		return modelStr, schemas.BatchCreateRequest
	}

	// Remove Google GenAI API endpoint suffixes if present
	for _, sfx := range gemini.GeminiRequestSuffixPaths {
		modelStr = strings.TrimSuffix(modelStr, sfx)
	}

	// Remove trailing colon if present
	if len(modelStr) > 0 && modelStr[len(modelStr)-1] == ':' {
		modelStr = modelStr[:len(modelStr)-1]
	}

	// Determine if :predict is for image generation (Imagen) or embedding
	// Imagen models use :predict for image generation
	isImagenPredict := isPredict && schemas.IsImagenModel(modelStr)
	if isPredict && !isImagenPredict {
		// :predict for non-Imagen models is embedding
		isEmbedding = true
	}

	if isEmbedding {
		return modelStr, schemas.EmbeddingRequest
	}

	// Create a proper GeminiGenerationRequest to detect request type
	geminiReq := &gemini.GeminiGenerationRequest{}
	if err := sonic.Unmarshal(ctx.Request.Body(), geminiReq); err != nil {
		return modelStr, ""
	}

	// Set the model on the request so detection functions can use it
	geminiReq.Model = modelStr

	// Detect if this is a speech or transcription request by examining the request body
	// Speech detection takes priority over transcription
	if isSpeechRequest(geminiReq) {
		return modelStr, schemas.SpeechRequest
	}
	if isTranscriptionRequest(geminiReq) {
		return modelStr, schemas.TranscriptionRequest
	}
	if isImageGenerationRequest(geminiReq) {
		return modelStr, schemas.ImageGenerationRequest
	}
	if isImageEditRequest(geminiReq) {
		return modelStr, schemas.ImageEditRequest
	}

	return modelStr, schemas.ResponsesRequest
}

// isSpeechRequest checks if the request is for speech generation (text-to-speech)
// Speech is detected by the presence of responseModalities containing "AUDIO" or speechConfig
func isSpeechRequest(req *gemini.GeminiGenerationRequest) bool {
	// Check if responseModalities contains AUDIO
	for _, modality := range req.GenerationConfig.ResponseModalities {
		if modality == gemini.ModalityAudio {
			return true
		}
	}

	// Check if speechConfig is present
	if req.GenerationConfig.SpeechConfig != nil {
		return true
	}

	return false
}

// isTranscriptionRequest checks if the request is for audio transcription (speech-to-text)
// Transcription is detected by the presence of audio input in parts, but NOT if it's a speech request
func isTranscriptionRequest(req *gemini.GeminiGenerationRequest) bool {
	// If this is already detected as a speech request, it's not transcription
	// This handles the edge case of bidirectional audio (input + output)
	if isSpeechRequest(req) {
		return false
	}

	// Check all contents for audio input
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			// Check for inline audio data
			if part.InlineData != nil && isAudioMimeType(part.InlineData.MIMEType) {
				return true
			}

			// Check for file-based audio data
			if part.FileData != nil && isAudioMimeType(part.FileData.MIMEType) {
				return true
			}
		}
	}

	return false
}

// isAudioMimeType checks if a MIME type represents an audio format
// Supports: WAV, MP3, AIFF, AAC, OGG Vorbis, FLAC (as per Gemini docs)
func isAudioMimeType(mimeType string) bool {
	if mimeType == "" {
		return false
	}

	// Convert to lowercase for case-insensitive comparison
	mimeType = strings.ToLower(mimeType)

	// Remove any parameters (e.g., "audio/mp3; charset=utf-8" -> "audio/mp3")
	if idx := strings.Index(mimeType, ";"); idx != -1 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	// Check if it starts with "audio/"
	if strings.HasPrefix(mimeType, "audio/") {
		return true
	}

	return false
}

// isImageGenerationRequest checks if the request is for image generation
// Image generation is detected by:
// 1. responseModalities containing "IMAGE"
// 2. Model name containing "imagen"
func isImageGenerationRequest(req *gemini.GeminiGenerationRequest) bool {
	if isImageEditRequest(req) {
		return false
	}

	// Check if responseModalities contains IMAGE
	for _, modality := range req.GenerationConfig.ResponseModalities {
		if modality == gemini.ModalityImage {
			return true
		}
	}

	// Fallback: Check if model name is an Imagen model (for forward-compatibility)
	if schemas.IsImagenModel(req.Model) {
		return true
	}

	return false
}

// isImageEditRequest checks if the request is for image edit
// Image edit is detected by:
// 1. Model is an Imagen model and has reference images
// 2. Inline image data present in the first content part and response modalities contain IMAGE
func isImageEditRequest(req *gemini.GeminiGenerationRequest) bool {
	if schemas.IsImagenModel(req.Model) && len(req.Instances) > 0 && req.Instances[0].ReferenceImages != nil {
		return true
	}

	if len(req.Contents) > 0 && len(req.Contents[0].Parts) > 0 && req.Contents[0].Parts[0].InlineData != nil && strings.Contains(req.Contents[0].Parts[0].InlineData.MIMEType, "image") {
		for _, modality := range req.GenerationConfig.ResponseModalities {
			if modality == gemini.ModalityImage {
				return true
			}
		}
	}

	return false
}

// extractGeminiListModelsParams extracts query parameters for list models request
func extractGeminiListModelsParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
		// Set provider to Gemini
		listModelsReq.Provider = schemas.Gemini

		// Extract pageSize from query parameters (Gemini uses pageSize instead of limit)
		if pageSizeStr := string(ctx.QueryArgs().Peek("pageSize")); pageSizeStr != "" {
			if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
				listModelsReq.PageSize = pageSize
			}
		}

		// Extract pageToken from query parameters
		if pageToken := string(ctx.QueryArgs().Peek("pageToken")); pageToken != "" {
			listModelsReq.PageToken = pageToken
		}

		return nil
	}
	return errors.New("invalid request type for Gemini list models")
}

// extractGeminiVideoOperationFromPath extracts model and operation_id from path
// and maps them to a Bifrost video retrieve request.
func extractGeminiVideoOperationFromPath(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return errors.New("model is required")
	}

	operationID := ctx.UserValue("operation_id")
	if operationID == nil {
		return errors.New("operation_id is required")
	}
	operationIDStr, ok := operationID.(string)
	if !ok || operationIDStr == "" {
		return errors.New("operation_id must be a non-empty string")
	}

	// check provider from operation id suffix, id:provider, could be any provider
	parts := strings.Split(operationIDStr, ":")
	if len(parts) < 2 || parts[len(parts)-1] == "" {
		return errors.New("provider is required in operation_id format 'id:provider'")
	}
	provider := parts[len(parts)-1]

	modelStr, ok := model.(string)
	if !ok || modelStr == "" {
		modelStr = provider
	}

	// if its gemini, set r.ID in format models/model/operations/operation_id:provider
	// else set r.ID in format operation_id:provider

	switch r := req.(type) {
	case *schemas.BifrostVideoRetrieveRequest:
		r.Provider = schemas.ModelProvider(provider)

		if r.Provider == schemas.OpenAI || r.Provider == schemas.Azure {
			// set a context flag to have video download request after video retrieve request when incoming request is coming from genai integration
			bifrostCtx.SetValue(schemas.BifrostContextKeyVideoOutputRequested, true)
		}
		// Gemini provider expects an operation resource path (without /v1beta prefix).
		if provider == string(schemas.Gemini) {
			r.ID = "models/" + modelStr + "/operations/" + operationIDStr
		} else {
			r.ID = operationIDStr
		}
	default:
		return errors.New("invalid request type for Gemini video operation")
	}

	return nil
}

// detectProviderFromGenAIRequest determines if the request is for Vertex AI or Gemini
// based on URL path, model name format, and authorization type
func detectProviderFromGenAIRequest(ctx *fasthttp.RequestCtx) schemas.ModelProvider {
	path := string(ctx.Path())

	// Primary detection: URL contains /projects/{PROJECT_ID}/locations/{LOCATION}/
	if strings.Contains(path, "/projects/") && strings.Contains(path, "/locations/") {
		return schemas.Vertex
	}

	// Fallback detection: Check model name in request body
	body := ctx.Request.Body()
	if len(body) > 0 {
		var reqBody struct {
			Model string `json:"model,omitempty"`
		}
		if err := sonic.Unmarshal(body, &reqBody); err == nil {
			if reqBody.Model != "" && strings.Contains(reqBody.Model, "projects/") && strings.Contains(reqBody.Model, "/locations/") {
				return schemas.Vertex
			}
		}
	}

	// Tertiary check: OAuth2 token (ya29.) indicates Vertex
	authHeader := string(ctx.Request.Header.Peek("authorization"))
	if strings.HasPrefix(strings.TrimPrefix(authHeader, "Bearer "), "ya29.") {
		return schemas.Vertex
	}

	// Default to Gemini
	return schemas.Gemini
}

func generateUploadID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	return "bf_" + encoded, nil
}
