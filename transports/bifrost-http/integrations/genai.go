package integrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// uploadSession stores metadata for resumable upload sessions
type uploadSession struct {
	Filename  string
	MimeType  string
	SizeBytes int64
	Provider  schemas.ModelProvider
	CreatedAt time.Time
}

// uploadSessions stores active upload sessions keyed by session ID
var uploadSessions = sync.Map{}

// ErrResumableUploadInit is a sentinel error indicating the resumable upload init response was sent
var ErrResumableUploadInit = errors.New("resumable upload init handled")

// Context key for flagging that response was already written
type contextKeyResponseWritten struct{}

// Context key for storing original filename for resumable uploads
type contextKeyOriginalFilename struct{}

// generateSessionID creates a unique session ID for resumable uploads
func generateSessionID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// cleanupExpiredSessions removes sessions older than 1 hour
func init() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			now := time.Now()
			uploadSessions.Range(func(key, value interface{}) bool {
				if session, ok := value.(*uploadSession); ok {
					if now.Sub(session.CreatedAt) > time.Hour {
						uploadSessions.Delete(key)
					}
				}
				return true
			})
		}
	}()
}

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	*GenericRouter
}

// CreateGenAIRouteConfigs creates a route configurations for GenAI endpoints.
func CreateGenAIRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// Chat completions endpoint
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models/{model:*}",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiGenerationRequest{}
		},
		RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiGenerationRequest); ok {
				if geminiReq.IsEmbedding {
					return &schemas.BifrostRequest{
						EmbeddingRequest: geminiReq.ToBifrostEmbeddingRequest(),
					}, nil
				} else if geminiReq.IsSpeech {
					return &schemas.BifrostRequest{
						SpeechRequest: geminiReq.ToBifrostSpeechRequest(),
					}, nil
				} else if geminiReq.IsTranscription {
					return &schemas.BifrostRequest{
						TranscriptionRequest: geminiReq.ToBifrostTranscriptionRequest(),
					}, nil
				} else {
					return &schemas.BifrostRequest{
						ResponsesRequest: geminiReq.ToBifrostResponsesRequest(),
					}, nil
				}
			}
			return nil, errors.New("invalid request type")
		},
		EmbeddingResponseConverter: func(ctx *context.Context, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
			return gemini.ToGeminiEmbeddingResponse(resp), nil
		},
		ResponsesResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
			return gemini.ToGeminiResponsesResponse(resp), nil
		},
		SpeechResponseConverter: func(ctx *context.Context, resp *schemas.BifrostSpeechResponse) (interface{}, error) {
			return gemini.ToGeminiSpeechResponse(resp), nil
		},
		TranscriptionResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
			return gemini.ToGeminiTranscriptionResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		StreamConfig: &StreamConfig{
			ResponsesStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
				geminiResponse := gemini.ToGeminiResponsesStreamResponse(resp)
				// Skip lifecycle events with no Gemini equivalent
				if geminiResponse == nil {
					return "", nil, nil
				}
				return "", geminiResponse, nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return gemini.ToGeminiError(err)
			},
		},
		PreCallback: extractAndSetModelFromURL,
	})

	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &schemas.BifrostListModelsRequest{}
		},
		RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
			if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
				return &schemas.BifrostRequest{
					ListModelsRequest: listModelsReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ListModelsResponseConverter: func(ctx *context.Context, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
			return gemini.ToGeminiListModelsResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiListModelsParams,
	})

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
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiFileUploadRequest{}
		},
		RequestParser: parseGeminiFileUploadRequest,
		FileRequestConverter: func(ctx *context.Context, req interface{}) (*FileRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiFileUploadRequest); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}
				// Convert Gemini request to Bifrost request
				bifrostReq := &schemas.BifrostFileUploadRequest{
					Provider: provider,
					File:     geminiReq.File,
					Filename: geminiReq.Filename,
					Purpose:  schemas.FilePurpose(geminiReq.Purpose),
				}
				return &FileRequest{
					Type:          schemas.FileUploadRequest,
					UploadRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid file upload request type")
		},
		FileUploadResponseConverter: func(ctx *context.Context, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileUploadResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileUploadParams,
	})

	// Resumable upload continuation endpoint - POST /upload/v1beta/files/resumable/{session_id}
	// This handles phase 2 of resumable uploads where actual file content is sent
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/upload/v1beta/files/resumable/{session_id}",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiFileUploadRequest{}
		},
		RequestParser: parseGeminiResumableUploadPhase2,
		FileRequestConverter: func(ctx *context.Context, req interface{}) (*FileRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiFileUploadRequest); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}
				// Convert Gemini request to Bifrost request
				bifrostReq := &schemas.BifrostFileUploadRequest{
					Provider: provider,
					File:     geminiReq.File,
					Filename: geminiReq.Filename,
					Purpose:  geminiReq.Purpose,
				}
				return &FileRequest{
					Type:          schemas.FileUploadRequest,
					UploadRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid file upload request type")
		},
		FileUploadResponseConverter: func(ctx *context.Context, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				fmt.Printf("[DEBUG] FileUploadResponseConverter (phase2 POST): using raw response\n")
				return resp.ExtraFields.RawResponse, nil
			}
			result := gemini.ToGeminiFileUploadResponse(resp)
			// If displayName is empty, use the original filename from context
			if result.File.DisplayName == "" {
				if originalFilename := (*ctx).Value(contextKeyOriginalFilename{}); originalFilename != nil {
					if filename, ok := originalFilename.(string); ok && filename != "" {
						result.File.DisplayName = filename
						fmt.Printf("[DEBUG] FileUploadResponseConverter (phase2 POST): set displayName from context=%s\n", filename)
					}
				}
			}
			fmt.Printf("[DEBUG] FileUploadResponseConverter (phase2 POST): converted response=%+v\n", result)
			return result, nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback:  extractGeminiResumableUploadParams,
		PostCallback: setResumableUploadFinalStatus,
	})

	// Resumable upload continuation endpoint - PUT /upload/v1beta/files/resumable/{session_id}
	// Some clients may use PUT instead of POST for resumable uploads
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/upload/v1beta/files/resumable/{session_id}",
		Method: "PUT",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiFileUploadRequest{}
		},
		RequestParser: parseGeminiResumableUploadPhase2,
		FileRequestConverter: func(ctx *context.Context, req interface{}) (*FileRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiFileUploadRequest); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}
				// Convert Gemini request to Bifrost request
				bifrostReq := &schemas.BifrostFileUploadRequest{
					Provider: provider,
					File:     geminiReq.File,
					Filename: geminiReq.Filename,
					Purpose:  geminiReq.Purpose,
				}
				return &FileRequest{
					Type:          schemas.FileUploadRequest,
					UploadRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid file upload request type")
		},
		FileUploadResponseConverter: func(ctx *context.Context, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			result := gemini.ToGeminiFileUploadResponse(resp)
			// If displayName is empty, use the original filename from context
			if result.File.DisplayName == "" {
				if originalFilename := (*ctx).Value(contextKeyOriginalFilename{}); originalFilename != nil {
					if filename, ok := originalFilename.(string); ok && filename != "" {
						result.File.DisplayName = filename
					}
				}
			}
			return result, nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback:  extractGeminiResumableUploadParams,
		PostCallback: setResumableUploadFinalStatus,
	})

	// List files endpoint - GET /v1beta/files
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiFileListRequest{}
		},
		FileRequestConverter: func(ctx *context.Context, req interface{}) (*FileRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiFileListRequest); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}
				// Convert Gemini request to Bifrost request
				bifrostReq := &schemas.BifrostFileListRequest{
					Provider: provider,
					Limit:    geminiReq.Limit,
					After:    geminiReq.After,
					Order:    geminiReq.Order,
				}
				return &FileRequest{
					Type:        schemas.FileListRequest,
					ListRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid file list request type")
		},
		FileListResponseConverter: func(ctx *context.Context, resp *schemas.BifrostFileListResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileListResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileListQueryParams,
	})

	// Retrieve file endpoint - GET /v1beta/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files/{file_id}",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiFileRetrieveRequest{}
		},
		FileRequestConverter: func(ctx *context.Context, req interface{}) (*FileRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiFileRetrieveRequest); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}
				// Convert Gemini request to Bifrost request
				bifrostReq := &schemas.BifrostFileRetrieveRequest{
					Provider: provider,
					FileID:   geminiReq.FileID,
				}
				return &FileRequest{
					Type:            schemas.FileRetrieveRequest,
					RetrieveRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid file retrieve request type")
		},
		FileRetrieveResponseConverter: func(ctx *context.Context, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileRetrieveParams,
	})

	// Delete file endpoint - DELETE /v1beta/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files/{file_id}",
		Method: "DELETE",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiFileDeleteRequest{}
		},
		FileRequestConverter: func(ctx *context.Context, req interface{}) (*FileRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiFileDeleteRequest); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}
				// Convert Gemini request to Bifrost request
				bifrostReq := &schemas.BifrostFileDeleteRequest{
					Provider: provider,
					FileID:   geminiReq.FileID,
				}
				return &FileRequest{
					Type:          schemas.FileDeleteRequest,
					DeleteRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid file delete request type")
		},
		FileDeleteResponseConverter: func(ctx *context.Context, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return map[string]interface{}{}, nil // Gemini returns empty response on delete
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiFileDeleteParams,
	})

	return routes
}

// CreateGenAIBatchRouteConfigs creates route configurations for Gemini Batch API endpoints.
func CreateGenAIBatchRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Create batch endpoint - POST /v1beta/models/{model}:batchGenerateContent
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models/{model}:batchGenerateContent",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiBatchCreateRequestSDK{}
		},
		BatchCreateRequestConverter: func(ctx *context.Context, req interface{}) (*BatchRequest, error) {
			if sdkReq, ok := req.(*gemini.GeminiBatchCreateRequestSDK); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}

				bifrostReq := &schemas.BifrostBatchCreateRequest{
					Provider: provider,
					Model:    sdkReq.Model,
				}

				// Handle src field - can be string (file reference) or array (inline requests)
				switch src := sdkReq.Src.(type) {
				case string:
					// File-based input: src="files/display_name"
					// TrimPrefix is safe even if prefix doesn't exist
					bifrostReq.InputFileID = strings.TrimPrefix(src, "files/")
				case []interface{}:
					// Inline requests: src=[{contents: [...], config: {...}}]
					requests := make([]schemas.BatchRequestItem, 0, len(src))
					for i, item := range src {
						if itemMap, ok := item.(map[string]interface{}); ok {
							customID := fmt.Sprintf("request-%d", i)
							requests = append(requests, schemas.BatchRequestItem{
								CustomID: customID,
								Body:     itemMap,
							})
						}
					}
					bifrostReq.Requests = requests
				}

				return &BatchRequest{
					Type:          schemas.BatchCreateRequest,
					CreateRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid batch create request type")
		},
		BatchCreateResponseConverter: func(ctx *context.Context, resp *schemas.BifrostBatchCreateResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiBatchJobResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchCreateParams,
	})

	// List batches endpoint - GET /v1beta/batches
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiBatchListRequestSDK{}
		},
		BatchCreateRequestConverter: func(ctx *context.Context, req interface{}) (*BatchRequest, error) {
			if sdkReq, ok := req.(*gemini.GeminiBatchListRequestSDK); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}

				bifrostReq := &schemas.BifrostBatchListRequest{
					Provider: provider,
					PageSize: sdkReq.PageSize,
				}
				if sdkReq.PageToken != "" {
					bifrostReq.PageToken = &sdkReq.PageToken
				}
				return &BatchRequest{
					Type:        schemas.BatchListRequest,
					ListRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid batch list request type")
		},
		BatchListResponseConverter: func(ctx *context.Context, resp *schemas.BifrostBatchListResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiBatchListResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchListQueryParams,
	})

	// Retrieve batch endpoint - GET /v1beta/batches/{batch_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches/{batch_id}",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiBatchRetrieveRequestSDK{}
		},
		BatchCreateRequestConverter: func(ctx *context.Context, req interface{}) (*BatchRequest, error) {
			if sdkReq, ok := req.(*gemini.GeminiBatchRetrieveRequestSDK); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}

				return &BatchRequest{
					Type: schemas.BatchRetrieveRequest,
					RetrieveRequest: &schemas.BifrostBatchRetrieveRequest{
						Provider: provider,
						BatchID:  sdkReq.Name,
					},
				}, nil
			}
			return nil, errors.New("invalid batch retrieve request type")
		},
		BatchRetrieveResponseConverter: func(ctx *context.Context, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiBatchRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchIDFromPath,
	})

	// Cancel batch endpoint - POST /v1beta/batches/{batch_id}:cancel
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches/{batch_id}:cancel",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiBatchCancelRequestSDK{}
		},
		BatchCreateRequestConverter: func(ctx *context.Context, req interface{}) (*BatchRequest, error) {
			if sdkReq, ok := req.(*gemini.GeminiBatchCancelRequestSDK); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}

				return &BatchRequest{
					Type: schemas.BatchCancelRequest,
					CancelRequest: &schemas.BifrostBatchCancelRequest{
						Provider: provider,
						BatchID:  sdkReq.Name,
					},
				}, nil
			}
			return nil, errors.New("invalid batch cancel request type")
		},
		BatchCancelResponseConverter: func(ctx *context.Context, resp *schemas.BifrostBatchCancelResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiBatchCancelResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchIDFromPathCancel,
	})

	// Delete batch endpoint - DELETE /v1beta/batches/{batch_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/batches/{batch_id}",
		Method: "DELETE",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiBatchDeleteRequestSDK{}
		},
		BatchCreateRequestConverter: func(ctx *context.Context, req interface{}) (*BatchRequest, error) {
			if sdkReq, ok := req.(*gemini.GeminiBatchDeleteRequestSDK); ok {
				// Get provider from context
				provider := schemas.Gemini
				if p := (*ctx).Value(bifrostContextKeyProvider); p != nil {
					provider = p.(schemas.ModelProvider)
				}

				return &BatchRequest{
					Type: schemas.BatchDeleteRequest,
					DeleteRequest: &schemas.BifrostBatchDeleteRequest{
						Provider: provider,
						BatchID:  sdkReq.Name,
					},
				}, nil
			}
			return nil, errors.New("invalid batch delete request type")
		},
		BatchDeleteResponseConverter: func(ctx *context.Context, resp *schemas.BifrostBatchDeleteResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			// Return empty object on successful delete
			return map[string]interface{}{}, nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiBatchIDFromPath,
	})

	return routes
}

// extractGeminiBatchCreateParams extracts provider from header and model from URL for batch create
func extractGeminiBatchCreateParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	// Extract model from URL path
	model := ctx.UserValue("model")
	if model != nil {
		modelStr := model.(string)
		// Remove :batchGenerateContent suffix if present
		modelStr = strings.TrimSuffix(modelStr, ":batchGenerateContent")
		if sdkReq, ok := req.(*gemini.GeminiBatchCreateRequestSDK); ok {
			sdkReq.Model = modelStr
		}
	}

	return nil
}

// extractGeminiBatchListQueryParams extracts query parameters for batch list requests
func extractGeminiBatchListQueryParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	if listReq, ok := req.(*gemini.GeminiBatchListRequestSDK); ok {
		// Extract pageSize from query parameters
		if pageSizeStr := string(ctx.QueryArgs().Peek("pageSize")); pageSizeStr != "" {
			if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
				listReq.PageSize = pageSize
			}
		}

		// Extract pageToken from query parameters
		if pageToken := string(ctx.QueryArgs().Peek("pageToken")); pageToken != "" {
			listReq.PageToken = pageToken
		}
	}

	return nil
}

// extractGeminiBatchIDFromPath extracts batch_id from path parameters
func extractGeminiBatchIDFromPath(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	batchID := ctx.UserValue("batch_id")
	if batchID == nil {
		return errors.New("batch_id is required")
	}

	batchIDStr, ok := batchID.(string)
	if !ok || batchIDStr == "" {
		return errors.New("batch_id must be a non-empty string")
	}

	// Ensure batch ID has proper format (batches/xxx)
	if !strings.HasPrefix(batchIDStr, "batches/") {
		batchIDStr = "batches/" + batchIDStr
	}

	switch r := req.(type) {
	case *gemini.GeminiBatchRetrieveRequestSDK:
		r.Name = batchIDStr
	case *gemini.GeminiBatchDeleteRequestSDK:
		r.Name = batchIDStr
	}

	return nil
}

// extractGeminiBatchIDFromPathCancel extracts batch_id from path for cancel requests
func extractGeminiBatchIDFromPathCancel(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	batchID := ctx.UserValue("batch_id")
	if batchID == nil {
		return errors.New("batch_id is required")
	}

	batchIDStr, ok := batchID.(string)
	if !ok || batchIDStr == "" {
		return errors.New("batch_id must be a non-empty string")
	}

	// Remove :cancel suffix if present (from URL pattern matching)
	batchIDStr = strings.TrimSuffix(batchIDStr, ":cancel")

	// Ensure batch ID has proper format (batches/xxx)
	if !strings.HasPrefix(batchIDStr, "batches/") {
		batchIDStr = "batches/" + batchIDStr
	}

	if cancelReq, ok := req.(*gemini.GeminiBatchCancelRequestSDK); ok {
		cancelReq.Name = batchIDStr
	}

	return nil
}

// parseGeminiFileUploadRequest parses file upload requests from the Google GenAI SDK.
// It handles both standard multipart uploads and resumable uploads by intercepting
// and converting them into a standard in-memory payload.
func parseGeminiFileUploadRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	uploadReq, ok := req.(*gemini.GeminiFileUploadRequest)
	if !ok {
		return errors.New("invalid request type for file upload")
	}
	contentType := string(ctx.Request.Header.ContentType())
	// Check for resumable upload protocol (Google GenAI SDK uses this)
	uploadProtocol := string(ctx.Request.Header.Peek("X-Goog-Upload-Protocol"))

	fmt.Printf("[DEBUG] parseGeminiFileUploadRequest: contentType=%s, uploadProtocol=%s, path=%s\n", contentType, uploadProtocol, string(ctx.Path()))

	if uploadProtocol == "resumable" || uploadProtocol == "multipart" {
		// Handle Google GenAI SDK resumable/multipart upload format
		return parseGeminiResumableUpload(ctx, uploadReq, contentType)
	}

	// Standard multipart/form-data upload
	if strings.HasPrefix(contentType, "multipart/") {
		return parseGeminiMultipartUpload(ctx, uploadReq)
	}

	// Raw body upload (single file content)
	return parseGeminiRawUpload(ctx, uploadReq)
}

// parseGeminiResumableUpload handles Google GenAI SDK resumable upload format.
// The SDK sends requests with X-Goog-Upload-Protocol header and may include
// metadata and file content in a multipart related format.
func parseGeminiResumableUpload(ctx *fasthttp.RequestCtx, uploadReq *gemini.GeminiFileUploadRequest, contentType string) error {
	body := ctx.Request.Body()

	fmt.Printf("[DEBUG] parseGeminiResumableUpload: contentType=%s, bodyLen=%d\n", contentType, len(body))

	// Check if it's multipart/related (metadata + file content)
	if strings.HasPrefix(contentType, "multipart/related") {
		fmt.Printf("[DEBUG] parseGeminiResumableUpload: handling multipart/related\n")
		return parseGeminiMultipartRelated(ctx, uploadReq, body, contentType)
	}

	// Check if this is just metadata (start of resumable upload)
	if strings.HasPrefix(contentType, "application/json") {
		fmt.Printf("[DEBUG] parseGeminiResumableUpload: handling JSON metadata, body=%s\n", string(body))
		// This is the initial request with just metadata
		// Parse the metadata - Google GenAI SDK sends snake_case fields
		var metadata struct {
			File struct {
				DisplayName string `json:"display_name"`
				MimeType    string `json:"mime_type"`
				SizeBytes   int64  `json:"size_bytes"`
			} `json:"file"`
		}
		if err := sonic.Unmarshal(body, &metadata); err == nil {
			fmt.Printf("[DEBUG] parseGeminiResumableUpload: parsed metadata - displayName=%s, mimeType=%s, sizeBytes=%d\n", metadata.File.DisplayName, metadata.File.MimeType, metadata.File.SizeBytes)
			uploadReq.Filename = metadata.File.DisplayName
			uploadReq.MimeType = metadata.File.MimeType

			// Create a session to store metadata for the second request
			sessionID := generateSessionID()
			fmt.Printf("[DEBUG] parseGeminiResumableUpload: created session ID=%s\n", sessionID)

			session := &uploadSession{
				Filename:  metadata.File.DisplayName,
				MimeType:  metadata.File.MimeType,
				SizeBytes: metadata.File.SizeBytes,
				CreatedAt: time.Now(),
			}
			uploadSessions.Store(sessionID, session)

			// Store session ID on request to signal special response handling in PreCallback
			uploadReq.ResumableSessionID = sessionID
		} else {
			fmt.Printf("[DEBUG] parseGeminiResumableUpload: failed to parse metadata: %v\n", err)
		}
		// For initial metadata-only request, file content will come in subsequent request
		return nil
	}

	fmt.Printf("[DEBUG] parseGeminiResumableUpload: handling raw file content\n")
	// Assume raw file content
	uploadReq.File = make([]byte, len(body))
	copy(uploadReq.File, body)
	return nil
}

// parseGeminiMultipartRelated parses multipart/related format used by Google GenAI SDK.
// Format: boundary-separated parts with metadata JSON and file content.
func parseGeminiMultipartRelated(ctx *fasthttp.RequestCtx, uploadReq *gemini.GeminiFileUploadRequest, body []byte, contentType string) error {
	// Extract boundary from content type
	boundary := ""
	for _, param := range strings.Split(contentType, ";") {
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "boundary=") {
			boundary = strings.TrimPrefix(param, "boundary=")
			boundary = strings.Trim(boundary, "\"")
			break
		}
	}

	if boundary == "" {
		return errors.New("missing boundary in multipart/related content type")
	}

	// Split body by boundary
	delimiter := "--" + boundary
	parts := strings.Split(string(body), delimiter)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "--" {
			continue
		}

		// Split headers from content
		headerEnd := strings.Index(part, "\r\n\r\n")
		if headerEnd == -1 {
			headerEnd = strings.Index(part, "\n\n")
			if headerEnd == -1 {
				continue
			}
		}

		headers := part[:headerEnd]
		content := part[headerEnd:]
		content = strings.TrimPrefix(content, "\r\n\r\n")
		content = strings.TrimPrefix(content, "\n\n")

		// Check content type of this part
		headersLower := strings.ToLower(headers)
		if strings.Contains(headersLower, "application/json") {
			// This is metadata - Google GenAI SDK sends snake_case fields
			var metadata struct {
				File struct {
					DisplayName string `json:"display_name"`
					MimeType    string `json:"mime_type"`
				} `json:"file"`
			}
			if err := sonic.Unmarshal([]byte(content), &metadata); err == nil {
				if metadata.File.DisplayName != "" {
					uploadReq.Filename = metadata.File.DisplayName
				}
				if metadata.File.MimeType != "" {
					uploadReq.MimeType = metadata.File.MimeType
				}
			}
		} else {
			// This is file content
			uploadReq.File = []byte(content)
		}
	}

	return nil
}

// parseGeminiMultipartUpload handles standard multipart/form-data uploads.
func parseGeminiMultipartUpload(ctx *fasthttp.RequestCtx, uploadReq *gemini.GeminiFileUploadRequest) error {
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Parse metadata field if present (JSON with displayName)
	if metadataValues := form.Value["metadata"]; len(metadataValues) > 0 {
		var metadata struct {
			File struct {
				DisplayName string `json:"displayName"`
			} `json:"file"`
		}
		if err := sonic.Unmarshal([]byte(metadataValues[0]), &metadata); err == nil {
			if metadata.File.DisplayName != "" {
				uploadReq.Filename = metadata.File.DisplayName
			}
		}
	}

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		return errors.New("file field is required")
	}

	fileHeader := fileHeaders[0]
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	// Read file data
	fileData, err := io.ReadAll(file)
	if err != nil {
		return err
	}

	uploadReq.File = fileData
	if uploadReq.Filename == "" {
		uploadReq.Filename = fileHeader.Filename
	}

	return nil
}

// parseGeminiRawUpload handles raw body uploads (file content only).
func parseGeminiRawUpload(ctx *fasthttp.RequestCtx, uploadReq *gemini.GeminiFileUploadRequest) error {
	body := ctx.Request.Body()
	if len(body) == 0 {
		return errors.New("file content is required")
	}

	uploadReq.File = make([]byte, len(body))
	copy(uploadReq.File, body)

	// Try to get filename from Content-Disposition header
	contentDisposition := string(ctx.Request.Header.Peek("Content-Disposition"))
	if contentDisposition != "" {
		for _, param := range strings.Split(contentDisposition, ";") {
			param = strings.TrimSpace(param)
			if strings.HasPrefix(param, "filename=") {
				filename := strings.TrimPrefix(param, "filename=")
				filename = strings.Trim(filename, "\"")
				uploadReq.Filename = filename
				break
			}
		}
	}

	return nil
}

// parseGeminiResumableUploadPhase2 handles phase 2 of resumable uploads where actual file content is sent
func parseGeminiResumableUploadPhase2(ctx *fasthttp.RequestCtx, req interface{}) error {
	fmt.Printf("[DEBUG] parseGeminiResumableUploadPhase2: called, path=%s\n", string(ctx.Path()))

	uploadReq, ok := req.(*gemini.GeminiFileUploadRequest)
	if !ok {
		return errors.New("invalid request type for file upload")
	}

	// Get session ID from URL path
	sessionID := ctx.UserValue("session_id")
	fmt.Printf("[DEBUG] parseGeminiResumableUploadPhase2: sessionID from path=%v\n", sessionID)
	if sessionID == nil {
		return errors.New("session_id is required")
	}

	sessionIDStr, ok := sessionID.(string)
	if !ok || sessionIDStr == "" {
		return errors.New("session_id must be a non-empty string")
	}

	// Retrieve session metadata
	sessionVal, ok := uploadSessions.Load(sessionIDStr)
	fmt.Printf("[DEBUG] parseGeminiResumableUploadPhase2: session found=%v\n", ok)
	if !ok {
		return errors.New("upload session not found or expired")
	}

	session, ok := sessionVal.(*uploadSession)
	if !ok {
		return errors.New("invalid session data")
	}

	// Get file content from request body
	body := ctx.Request.Body()
	fmt.Printf("[DEBUG] parseGeminiResumableUploadPhase2: bodyLen=%d, filename=%s, provider=%s\n", len(body), session.Filename, session.Provider)
	if len(body) == 0 {
		return errors.New("file content is required")
	}

	// Populate the upload request with session metadata and file content
	uploadReq.File = make([]byte, len(body))
	copy(uploadReq.File, body)
	uploadReq.Filename = session.Filename
	uploadReq.MimeType = session.MimeType
	uploadReq.Purpose = "batch" // Default purpose for file uploads via GenAI API

	// Store session ID for provider extraction in PreCallback
	// NOTE: Don't delete session here - PreCallback needs to read provider from it
	uploadReq.ResumableSessionID = sessionIDStr

	fmt.Printf("[DEBUG] parseGeminiResumableUploadPhase2: successfully prepared upload request\n")
	return nil
}

// setResumableUploadFinalStatus sets the X-Goog-Upload-Status header to "final" for phase 2 responses
func setResumableUploadFinalStatus(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
	// Set the upload status to final to signal completion of resumable upload
	ctx.Response.Header.Set("X-Goog-Upload-Status", "final")

	// Log the response for debugging
	respJSON, _ := sonic.Marshal(resp)
	fmt.Printf("[DEBUG] setResumableUploadFinalStatus: set X-Goog-Upload-Status=final, response body=%s\n", string(respJSON))

	// Also log the full response headers for debugging
	fmt.Printf("[DEBUG] setResumableUploadFinalStatus: status code=%d\n", ctx.Response.StatusCode())

	return nil
}

// extractGeminiResumableUploadParams extracts provider from session for resumable upload phase 2
func extractGeminiResumableUploadParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Get session ID from URL path
	sessionID := ctx.UserValue("session_id")
	if sessionID == nil {
		return errors.New("session_id is required")
	}

	sessionIDStr, ok := sessionID.(string)
	if !ok || sessionIDStr == "" {
		return errors.New("session_id must be a non-empty string")
	}

	// Get provider and filename from session (stored during phase 1)
	provider := schemas.Gemini
	var originalFilename string
	if sessionVal, ok := uploadSessions.Load(sessionIDStr); ok {
		if session, ok := sessionVal.(*uploadSession); ok {
			if session.Provider != "" {
				provider = session.Provider
			}
			originalFilename = session.Filename
		}
		// Clean up the session now that we've extracted the data
		uploadSessions.Delete(sessionIDStr)
	}

	fmt.Printf("[DEBUG] extractGeminiResumableUploadParams: sessionID=%s, provider=%s, filename=%s\n", sessionIDStr, provider, originalFilename)
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, provider)
	// Store original filename in context for response converter
	*bifrostCtx = context.WithValue(*bifrostCtx, contextKeyOriginalFilename{}, originalFilename)
	return nil
}

// extractGeminiFileUploadParams extracts provider from header for file upload requests
// and handles resumable upload init by returning the upload URL
func extractGeminiFileUploadParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	fmt.Printf("[DEBUG] extractGeminiFileUploadParams: provider=%s\n", provider)

	// Check if this is a resumable upload init (metadata-only request)
	if uploadReq, ok := req.(*gemini.GeminiFileUploadRequest); ok {
		fmt.Printf("[DEBUG] extractGeminiFileUploadParams: resumableSessionID=%s, fileLen=%d\n", uploadReq.ResumableSessionID, len(uploadReq.File))
		if uploadReq.ResumableSessionID != "" {
			// Update the session with the provider
			if sessionVal, ok := uploadSessions.Load(uploadReq.ResumableSessionID); ok {
				if session, ok := sessionVal.(*uploadSession); ok {
					session.Provider = schemas.ModelProvider(provider)
				}
			}

			// Build the upload URL for phase 2
			// Use the request's host and scheme to build the URL
			scheme := "http"
			if ctx.IsTLS() {
				scheme = "https"
			}
			host := string(ctx.Host())
			uploadURL := fmt.Sprintf("%s://%s/genai/upload/v1beta/files/resumable/%s", scheme, host, uploadReq.ResumableSessionID)

			fmt.Printf("[DEBUG] extractGeminiFileUploadParams: returning upload URL=%s\n", uploadURL)

			// Send the upload URL response
			ctx.Response.Header.Set("X-Goog-Upload-URL", uploadURL)
			ctx.Response.Header.Set("X-Goog-Upload-Status", "active")
			ctx.Response.Header.SetContentType("application/json")
			ctx.SetStatusCode(200)

			// Return empty JSON object as response body
			ctx.SetBody([]byte("{}"))

			// Mark that response was written
			*bifrostCtx = context.WithValue(*bifrostCtx, contextKeyResponseWritten{}, true)

			// Return sentinel error to signal router to skip further processing
			return ErrResumableUploadInit
		}
	}

	return nil
}

// extractGeminiFileListQueryParams extracts query parameters for Gemini file list requests
func extractGeminiFileListQueryParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	if listReq, ok := req.(*gemini.GeminiFileListRequest); ok {
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

// extractGeminiFileRetrieveParams extracts file_id and provider for file retrieve requests
func extractGeminiFileRetrieveParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	fileID := ctx.UserValue("file_id")
	if fileID == nil {
		return errors.New("file_id is required")
	}

	fileIDStr, ok := fileID.(string)
	if !ok || fileIDStr == "" {
		return errors.New("file_id must be a non-empty string")
	}

	if retrieveReq, ok := req.(*gemini.GeminiFileRetrieveRequest); ok {
		retrieveReq.FileID = fileIDStr
	}

	return nil
}

// extractGeminiFileDeleteParams extracts file_id and provider for file delete requests
func extractGeminiFileDeleteParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	// Extract provider from header, default to Gemini
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Gemini)
	}
	*bifrostCtx = context.WithValue(*bifrostCtx, bifrostContextKeyProvider, schemas.ModelProvider(provider))

	fileID := ctx.UserValue("file_id")
	if fileID == nil {
		return errors.New("file_id is required")
	}

	fileIDStr, ok := fileID.(string)
	if !ok || fileIDStr == "" {
		return errors.New("file_id must be a non-empty string")
	}

	if deleteReq, ok := req.(*gemini.GeminiFileDeleteRequest); ok {
		deleteReq.FileID = fileIDStr
	}

	return nil
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *GenAIRouter {
	routes := CreateGenAIRouteConfigs("/genai")
	routes = append(routes, CreateGenAIFileRouteConfigs("/genai", handlerStore)...)
	routes = append(routes, CreateGenAIBatchRouteConfigs("/genai", handlerStore)...)

	return &GenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}

var embeddingPaths = []string{
	":embedContent",
	":batchEmbedContents",
	":predict",
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractAndSetModelFromURL(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return fmt.Errorf("model parameter is required")
	}

	modelStr := model.(string)

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

	// Remove Google GenAI API endpoint suffixes if present
	for _, sfx := range []string{
		":streamGenerateContent",
		":generateContent",
		":countTokens",
		":embedContent",
		":batchEmbedContents",
		":predict",
	} {
		modelStr = strings.TrimSuffix(modelStr, sfx)
	}

	// Remove trailing colon if present
	if len(modelStr) > 0 && modelStr[len(modelStr)-1] == ':' {
		modelStr = modelStr[:len(modelStr)-1]
	}

	// Set the model and flags in the request
	if geminiReq, ok := req.(*gemini.GeminiGenerationRequest); ok {
		geminiReq.Model = modelStr
		geminiReq.Stream = isStreaming
		geminiReq.IsEmbedding = isEmbedding

		// Detect if this is a speech or transcription request by examining the request body
		// Speech detection takes priority over transcription
		geminiReq.IsSpeech = isSpeechRequest(geminiReq)
		geminiReq.IsTranscription = isTranscriptionRequest(geminiReq)

		return nil
	}

	return fmt.Errorf("invalid request type for GenAI")
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

// extractGeminiListModelsParams extracts query parameters for list models request
func extractGeminiListModelsParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
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
