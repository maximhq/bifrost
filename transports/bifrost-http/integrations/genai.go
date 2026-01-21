package integrations

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

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
				} else {
					return &schemas.BifrostRequest{
						ResponsesRequest: geminiReq.ToBifrostResponsesRequest(ctx),
					}, nil
				}
			}
			return nil, errors.New("invalid request type")
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
		PreCallback: extractAndSetModelFromURL,
	})

	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
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
			return &schemas.BifrostFileUploadRequest{}
		},
		RequestParser: parseGeminiFileUploadRequest,
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if uploadReq, ok := req.(*schemas.BifrostFileUploadRequest); ok {
				uploadReq.Provider = schemas.Gemini
				return &FileRequest{
					Type:          schemas.FileUploadRequest,
					UploadRequest: uploadReq,
				}, nil
			}
			return nil, errors.New("invalid file upload request type")
		},
		FileUploadResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return gemini.ToGeminiFileUploadResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
	})

	// List files endpoint - GET /v1beta/files
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/files",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &schemas.BifrostFileListRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if listReq, ok := req.(*schemas.BifrostFileListRequest); ok {
				listReq.Provider = schemas.Gemini
				return &FileRequest{
					Type:        schemas.FileListRequest,
					ListRequest: listReq,
				}, nil
			}
			return nil, errors.New("invalid file list request type")
		},
		FileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
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
		GetRequestTypeInstance: func() interface{} {
			return &schemas.BifrostFileRetrieveRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if retrieveReq, ok := req.(*schemas.BifrostFileRetrieveRequest); ok {
				retrieveReq.Provider = schemas.Gemini
				return &FileRequest{
					Type:            schemas.FileRetrieveRequest,
					RetrieveRequest: retrieveReq,
				}, nil
			}
			return nil, errors.New("invalid file retrieve request type")
		},
		FileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
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
		GetRequestTypeInstance: func() interface{} {
			return &schemas.BifrostFileDeleteRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if deleteReq, ok := req.(*schemas.BifrostFileDeleteRequest); ok {
				deleteReq.Provider = schemas.Gemini
				return &FileRequest{
					Type:          schemas.FileDeleteRequest,
					DeleteRequest: deleteReq,
				}, nil
			}
			return nil, errors.New("invalid file delete request type")
		},
		FileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
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

// parseGeminiFileUploadRequest parses multipart/form-data for Gemini file upload requests
func parseGeminiFileUploadRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	uploadReq, ok := req.(*schemas.BifrostFileUploadRequest)
	if !ok {
		return errors.New("invalid request type for file upload")
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract metadata (optional JSON with displayName)
	if metadataValues := form.Value["metadata"]; len(metadataValues) > 0 && metadataValues[0] != "" {
		// Could parse JSON metadata to extract displayName
		// For now, just use filename from file header
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
	uploadReq.Filename = fileHeader.Filename

	return nil
}

// extractGeminiFileListQueryParams extracts query parameters for Gemini file list requests
func extractGeminiFileListQueryParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	if listReq, ok := req.(*schemas.BifrostFileListRequest); ok {
		listReq.Provider = schemas.Gemini

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
		r.Provider = schemas.Gemini
	case *schemas.BifrostFileDeleteRequest:
		r.FileID = fileIDStr
		r.Provider = schemas.Gemini
	}

	return nil
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *GenAIRouter {
	routes := CreateGenAIRouteConfigs("/genai")
	routes = append(routes, CreateGenAIFileRouteConfigs("/genai", handlerStore)...)

	return &GenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}

var embeddingPaths = []string{
	":embedContent",
	":batchEmbedContents",
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractAndSetModelFromURL(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return fmt.Errorf("model parameter is required")
	}

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
		r.IsImageGeneration = isImagenPredict || isImageGenerationRequest(r)

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

// isImageGenerationRequest checks if the request is for image generation
// Image generation is detected by:
// 1. responseModalities containing "IMAGE"
// 2. Model name containing "imagen"
func isImageGenerationRequest(req *gemini.GeminiGenerationRequest) bool {
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
