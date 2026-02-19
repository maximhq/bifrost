package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// setAzureModelName sets the model name for Azure requests with proper prefix handling
// When deploymentID is present, it always takes precedence over the request body model
// to avoid deployment/model mismatches.
func setAzureModelName(currentModel, deploymentID string) string {
	if deploymentID != "" {
		return "azure/" + deploymentID
	} else if currentModel != "" && !strings.HasPrefix(currentModel, "azure/") {
		return "azure/" + currentModel
	}
	return currentModel
}

// OpenAIRouter holds route registrations for OpenAI endpoints.
// It supports standard chat completions, speech synthesis, audio transcription, and streaming capabilities with OpenAI-specific formatting.
type OpenAIRouter struct {
	*GenericRouter
}

func AzureEndpointPreHook(handlerStore lib.HandlerStore) func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Detect CLI user agent for all OpenAI routes
		DetectCLIUserAgent(ctx, bifrostCtx)

		azureKey := ctx.Request.Header.Peek("authorization")
		deploymentEndpoint := ctx.Request.Header.Peek("x-bf-azure-endpoint")
		deploymentID := ctx.UserValue("deployment-id")
		apiVersion := string(ctx.QueryArgs().Peek("api-version"))

		if deploymentID != nil {
			deploymentIDStr, ok := deploymentID.(string)
			if !ok {
				return errors.New("deployment-id is required in path")
			}

			switch r := req.(type) {
			case *openai.OpenAIChatRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAIResponsesRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAISpeechRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAITranscriptionRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAIEmbeddingRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAIImageGenerationRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAIImageEditRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *openai.OpenAIImageVariationRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *schemas.BifrostListModelsRequest:
				r.Provider = schemas.Azure
			}

			if deploymentEndpoint == nil || azureKey == nil || !handlerStore.ShouldAllowDirectKeys() {
				return nil
			}

			azureKeyStr := string(azureKey)
			deploymentEndpointStr := string(deploymentEndpoint)
			apiVersionStr := string(apiVersion)

			key := schemas.Key{
				ID:             uuid.New().String(),
				Models:         []string{},
				AzureKeyConfig: &schemas.AzureKeyConfig{},
			}

			if deploymentEndpointStr != "" && deploymentIDStr != "" && azureKeyStr != "" {
				key.Value = *schemas.NewEnvVar(strings.TrimPrefix(azureKeyStr, "Bearer "))
				key.AzureKeyConfig.Endpoint = *schemas.NewEnvVar(deploymentEndpointStr)
				key.AzureKeyConfig.Deployments = map[string]string{deploymentIDStr: deploymentIDStr}
			}

			if apiVersionStr != "" {
				key.AzureKeyConfig.APIVersion = schemas.NewEnvVar(apiVersionStr)
			}

			ctx.SetUserValue(string(schemas.BifrostContextKeyDirectKey), key)

			return nil
		}

		return nil
	}
}

// CreateOpenAIRouteConfigs creates route configurations for OpenAI endpoints.
func CreateOpenAIRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Text completions endpoint
	for _, path := range []string{
		"/v1/completions",
		"/completions",
		"/openai/deployments/{deployment-id}/completions",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.TextCompletionRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAITextCompletionRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAITextCompletionRequest); ok {
					return &schemas.BifrostRequest{
						TextCompletionRequest: openaiReq.ToBifrostTextCompletionRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			TextResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				TextStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Chat completions endpoint
	for _, path := range []string{
		"/v1/chat/completions",
		"/chat/completions",
		"/openai/deployments/{deployment-id}/chat/completions",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ChatCompletionRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIChatRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAIChatRequest); ok {
					return &schemas.BifrostRequest{
						ChatRequest: openaiReq.ToBifrostChatRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ChatResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ChatStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Responses endpoint
	for _, path := range []string{
		"/v1/responses",
		"/responses",
		"/openai/deployments/{deployment-id}/responses",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ResponsesRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIResponsesRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAIResponsesRequest); ok {
					return &schemas.BifrostRequest{
						ResponsesRequest: openaiReq.ToBifrostResponsesRequest(ctx),
					}, nil

				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp.WithDefaults(), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return string(resp.Type), resp.ExtraFields.RawResponse, nil
						}
					}
					converted := resp.WithDefaults()
					if converted == nil {
						return "", nil, nil
					}
					return string(resp.Type), converted, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Input tokens endpoint (for counting tokens in a request)
	for _, path := range []string{
		"/v1/responses/input_tokens",
		"/responses/input_tokens",
		"/openai/deployments/{deployment-id}/responses/input_tokens",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.CountTokensRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIResponsesRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAIResponsesRequest); ok {
					return &schemas.BifrostRequest{
						CountTokensRequest: openaiReq.ToBifrostResponsesRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid request type for input tokens")
			},
			CountTokensResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostCountTokensResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Embeddings endpoint
	for _, path := range []string{
		"/v1/embeddings",
		"/embeddings",
		"/openai/deployments/{deployment-id}/embeddings",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.EmbeddingRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIEmbeddingRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if embeddingReq, ok := req.(*openai.OpenAIEmbeddingRequest); ok {
					return &schemas.BifrostRequest{
						EmbeddingRequest: embeddingReq.ToBifrostEmbeddingRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid embedding request type")
			},
			EmbeddingResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Speech synthesis endpoint
	for _, path := range []string{
		"/v1/audio/speech",
		"/audio/speech",
		"/openai/deployments/{deployment-id}/audio/speech",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.SpeechRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAISpeechRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if speechReq, ok := req.(*openai.OpenAISpeechRequest); ok {
					return &schemas.BifrostRequest{
						SpeechRequest: speechReq.ToBifrostSpeechRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid speech request type")
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				SpeechStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostSpeechStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Audio transcription endpoint
	for _, path := range []string{
		"/v1/audio/transcriptions",
		"/audio/transcriptions",
		"/openai/deployments/{deployment-id}/audio/transcriptions",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.TranscriptionRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAITranscriptionRequest{}
			},
			RequestParser: parseTranscriptionMultipartRequest, // Handle multipart form parsing
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if transcriptionReq, ok := req.(*openai.OpenAITranscriptionRequest); ok {
					return &schemas.BifrostRequest{
						TranscriptionRequest: transcriptionReq.ToBifrostTranscriptionRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid transcription request type")
			},
			TranscriptionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				TranscriptionStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTranscriptionStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Image Generation endpoint
	for _, path := range []string{
		"/v1/images/generations",
		"/images/generations",
		"/openai/deployments/{deployment-id}/images/generations",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ImageGenerationRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIImageGenerationRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if imageGenReq, ok := req.(*openai.OpenAIImageGenerationRequest); ok {
					return &schemas.BifrostRequest{
						ImageGenerationRequest: imageGenReq.ToBifrostImageGenerationRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid image generation request type")
			},
			ImageGenerationResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ImageGenerationStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return string(resp.Type), resp.ExtraFields.RawResponse, nil
						}
					}
					return string(resp.Type), resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	for _, path := range []string{
		"/v1/images/edits",
		"/images/edits",
		"/openai/deployments/{deployment-id}/images/edits",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ImageEditRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIImageEditRequest{}
			},
			RequestParser: parseOpenAIImageEditMultipartRequest, // Handle multipart form parsing
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if imageEditReq, ok := req.(*openai.OpenAIImageEditRequest); ok {
					return &schemas.BifrostRequest{
						ImageEditRequest: imageEditReq.ToBifrostImageEditRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid image edit request type")
			},
			ImageGenerationResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ImageGenerationStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return string(resp.Type), resp.ExtraFields.RawResponse, nil
						}
					}
					return string(resp.Type), resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}
	for _, path := range []string{
		"/v1/images/variations",
		"/images/variations",
		"/openai/deployments/{deployment-id}/images/variations",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ImageVariationRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIImageVariationRequest{}
			},
			RequestParser: parseOpenAIImageVariationMultipartRequest,
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if imageVariationReq, ok := req.(*openai.OpenAIImageVariationRequest); ok {
					return &schemas.BifrostRequest{
						ImageVariationRequest: imageVariationReq.ToBifrostImageVariationRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid image variation request type")
			},
			ImageGenerationResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ImageGenerationStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostImageGenerationStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return string(resp.Type), resp.ExtraFields.RawResponse, nil
						}
					}
					return string(resp.Type), resp, nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}
	return routes
}

func CreateOpenAIListModelsRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Models endpoint
	for _, path := range []string{
		"/v1/models",
		"/models",
		"/openai/deployments/{deployment-id}/models",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
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
				return openai.ToOpenAIListModelsResponse(resp), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: setQueryParamsAndAzureEndpointPreHook(handlerStore),
		})
	}

	return routes
}

// setQueryParamsAndAzureEndpointPreHook creates a combined pre-callback for OpenAI list models
// that handles both Azure endpoint preprocessing and query parameter extraction
func setQueryParamsAndAzureEndpointPreHook(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// First run the Azure endpoint pre-hook if needed
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		// Then extract query parameters for list models
		if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
			// Set provider to OpenAI (may be overridden by Azure hook)
			if listModelsReq.Provider == "" {
				listModelsReq.Provider = schemas.OpenAI
			}

			return nil
		}

		return nil
	}
}

// CreateOpenAIBatchRouteConfigs creates route configurations for OpenAI Batch API endpoints.
func CreateOpenAIBatchRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Create batch endpoint - POST /v1/batches
	for _, path := range []string{
		"/v1/batches",
		"/batches",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.BatchCreateRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostBatchCreateRequest{}
			},
			BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
				if openaiReq, ok := req.(*schemas.BifrostBatchCreateRequest); ok {
					switch openaiReq.Provider {
					case schemas.Gemini:
						if openaiReq.InputFileID != "" {
							openaiReq.InputFileID = strings.Replace(openaiReq.InputFileID, "files-", "files/", 1)
						}
					case schemas.Bedrock:
						if openaiReq.InputFileID != "" {
							// Base64 decode the input field id if it's base64 encoded
							if decodedFileID, err := base64.StdEncoding.DecodeString(openaiReq.InputFileID); err == nil {
								openaiReq.InputFileID = string(decodedFileID)
							}
						}
					}
					return &BatchRequest{
						Type:          schemas.BatchCreateRequest,
						CreateRequest: openaiReq,
					}, nil
				}
				return nil, errors.New("invalid batch create request type")
			},
			BatchCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCreateResponse) (interface{}, error) {
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					resp.ID = strings.Replace(resp.ID, "batches/", "batches-", 1)
					resp.InputFileID = strings.Replace(resp.InputFileID, "files/", "files-", 1)
				case schemas.Bedrock:
					resp.ID = base64.StdEncoding.EncodeToString([]byte(resp.ID))
					resp.InputFileID = base64.StdEncoding.EncodeToString([]byte(resp.InputFileID))
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
				// Provider is parsed from JSON body (extra_body), default to OpenAI if not set
				if createReq, ok := req.(*schemas.BifrostBatchCreateRequest); ok {
					if createReq.Provider == "" {
						createReq.Provider = schemas.OpenAI
					}
					// For Bedrock, extract extra params from raw body
					// ExtraParams has json:"-" tag so it's not auto-populated
					if createReq.Provider == schemas.Bedrock {
						var extraFields map[string]interface{}
						if err := json.Unmarshal(ctx.Request.Body(), &extraFields); err == nil {
							if createReq.ExtraParams == nil {
								createReq.ExtraParams = make(map[string]interface{})
							}
							// Extract role_arn (required for Bedrock)
							if roleArn, ok := extraFields["role_arn"].(string); ok {
								createReq.ExtraParams["role_arn"] = roleArn
							}
							// Extract output_s3_uri (required for Bedrock)
							if outputS3Uri, ok := extraFields["output_s3_uri"].(string); ok {
								createReq.ExtraParams["output_s3_uri"] = outputS3Uri
							}
							// Extract job_name (optional, stored in Metadata)
							if jobName, ok := extraFields["job_name"].(string); ok {
								if createReq.Metadata == nil {
									createReq.Metadata = make(map[string]string)
								}
								createReq.Metadata["job_name"] = jobName
							}
						}
					}

					// For Anthropic, extract inline requests from raw body
					// Anthropic uses inline requests instead of file-based batching
					if createReq.Provider == schemas.Anthropic {
						var extraFields map[string]interface{}
						if err := json.Unmarshal(ctx.Request.Body(), &extraFields); err == nil {
							// Extract requests array for inline batching
							if requestsRaw, ok := extraFields["requests"].([]interface{}); ok {
								createReq.Requests = make([]schemas.BatchRequestItem, len(requestsRaw))
								for i, r := range requestsRaw {
									if reqMap, ok := r.(map[string]interface{}); ok {
										item := schemas.BatchRequestItem{}
										if customID, ok := reqMap["custom_id"].(string); ok {
											item.CustomID = customID
										}
										if params, ok := reqMap["params"].(map[string]interface{}); ok {
											item.Params = params
										}
										createReq.Requests[i] = item
									}
								}
							}
						}
					}
				}
				return AzureEndpointPreHook(handlerStore)(ctx, bifrostCtx, req)
			},
		})
	}

	// List batches endpoint - GET /v1/batches
	for _, path := range []string{
		"/v1/batches",
		"/batches",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.BatchListRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostBatchListRequest{}
			},
			BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
				if listReq, ok := req.(*schemas.BifrostBatchListRequest); ok {
					if listReq.Provider == "" {
						listReq.Provider = schemas.OpenAI
					}
					return &BatchRequest{
						Type:        schemas.BatchListRequest,
						ListRequest: listReq,
					}, nil
				}
				return nil, errors.New("invalid batch list request type")
			},
			BatchListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchListResponse) (interface{}, error) {
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					for i, batch := range resp.Data {
						resp.Data[i].ID = strings.Replace(batch.ID, "batches/", "batches-", 1)
						resp.Data[i].InputFileID = strings.Replace(batch.InputFileID, "files/", "files-", 1)
					}
				case schemas.Bedrock:
					for i, batch := range resp.Data {
						resp.Data[i].ID = base64.StdEncoding.EncodeToString([]byte(batch.ID))
						resp.Data[i].InputFileID = base64.StdEncoding.EncodeToString([]byte(batch.InputFileID))
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractBatchListQueryParams(handlerStore),
		})
	}

	// Retrieve batch endpoint - GET /v1/batches/{batch_id}
	for _, path := range []string{
		"/v1/batches/{batch_id}",
		"/batches/{batch_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.BatchRetrieveRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostBatchRetrieveRequest{}
			},
			BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
				if retrieveReq, ok := req.(*schemas.BifrostBatchRetrieveRequest); ok {
					if retrieveReq.Provider == "" {
						retrieveReq.Provider = schemas.OpenAI
					}
					switch retrieveReq.Provider {
					case schemas.Gemini:
						retrieveReq.BatchID = strings.Replace(retrieveReq.BatchID, "batches-", "batches/", 1)
					case schemas.Bedrock:
						// Base64 decode the batch ID (ARN) for Bedrock
						if decodedBatchID, err := base64.StdEncoding.DecodeString(retrieveReq.BatchID); err == nil {
							retrieveReq.BatchID = string(decodedBatchID)
						}
					}
					return &BatchRequest{
						Type:            schemas.BatchRetrieveRequest,
						RetrieveRequest: retrieveReq,
					}, nil
				}
				return nil, errors.New("invalid batch retrieve request type")
			},
			BatchRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error) {
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					resp.ID = strings.Replace(resp.ID, "batches/", "batches-", 1)
					resp.InputFileID = strings.Replace(resp.InputFileID, "files/", "files-", 1)
				case schemas.Bedrock:
					resp.ID = base64.StdEncoding.EncodeToString([]byte(resp.ID))
					resp.InputFileID = base64.StdEncoding.EncodeToString([]byte(resp.InputFileID))
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractBatchIDFromPath(handlerStore),
		})
	}

	// Cancel batch endpoint - POST /v1/batches/{batch_id}/cancel
	for _, path := range []string{
		"/v1/batches/{batch_id}/cancel",
		"/batches/{batch_id}/cancel",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.BatchCancelRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostBatchCancelRequest{}
			},
			BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
				if cancelReq, ok := req.(*schemas.BifrostBatchCancelRequest); ok {
					if cancelReq.Provider == "" {
						cancelReq.Provider = schemas.OpenAI
					}
					switch cancelReq.Provider {
					case schemas.Gemini:
						cancelReq.BatchID = strings.Replace(cancelReq.BatchID, "batches-", "batches/", 1)
					case schemas.Bedrock:
						// Base64 decode the batch ID (ARN) for Bedrock
						if decodedBatchID, err := base64.StdEncoding.DecodeString(cancelReq.BatchID); err == nil {
							cancelReq.BatchID = string(decodedBatchID)
						}
					}
					return &BatchRequest{
						Type:          schemas.BatchCancelRequest,
						CancelRequest: cancelReq,
					}, nil
				}
				return nil, errors.New("invalid batch cancel request type")
			},
			BatchCancelResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCancelResponse) (interface{}, error) {
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					resp.ID = strings.Replace(resp.ID, "batches/", "batches-", 1)
				case schemas.Bedrock:
					resp.ID = base64.StdEncoding.EncodeToString([]byte(resp.ID))
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractBatchIDFromPath(handlerStore),
		})
	}
	return routes
}

// CreateOpenAIFileRouteConfigs creates route configurations for OpenAI Files API endpoints.
func CreateOpenAIFileRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Upload file endpoint - POST /v1/files
	for _, path := range []string{
		"/v1/files",
		"/files",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.FileUploadRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostFileUploadRequest{}
			},
			RequestParser: parseOpenAIFileUploadMultipartRequest,
			FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
				if uploadReq, ok := req.(*schemas.BifrostFileUploadRequest); ok {
					return &FileRequest{
						Type:          schemas.FileUploadRequest,
						UploadRequest: uploadReq,
					}, nil
				}
				return nil, errors.New("invalid file upload request type")
			},
			FileUploadResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
				if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.OpenAI {
					return resp.ExtraFields.RawResponse, nil
				}
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					resp.ID = strings.Replace(resp.ID, "files/", "files-", 1)
				case schemas.Bedrock:
					resp.ID = base64.StdEncoding.EncodeToString([]byte(resp.ID))
				default:
					return resp, nil
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
				// Default to OpenAI if provider not set from extra_body
				if bifrostReq, ok := req.(*schemas.BifrostFileUploadRequest); ok {
					if bifrostReq.Provider == "" {
						bifrostReq.Provider = schemas.OpenAI
					}
				}
				return AzureEndpointPreHook(handlerStore)(ctx, bifrostCtx, req)
			},
		})
	}

	// List files endpoint - GET /v1/files
	for _, path := range []string{
		"/v1/files",
		"/files",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.FileListRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostFileListRequest{}
			},
			FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
				if listReq, ok := req.(*schemas.BifrostFileListRequest); ok {
					if listReq.Provider == "" {
						listReq.Provider = schemas.OpenAI
					}
					return &FileRequest{
						Type:        schemas.FileListRequest,
						ListRequest: listReq,
					}, nil
				}
				return nil, errors.New("invalid file list request type")
			},
			FileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error) {
				if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.OpenAI {
					return resp.ExtraFields.RawResponse, nil
				}
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					for i, file := range resp.Data {
						resp.Data[i].ID = strings.Replace(file.ID, "files/", "files-", 1)
					}
				case schemas.Bedrock:
					for i, file := range resp.Data {
						resp.Data[i].ID = base64.StdEncoding.EncodeToString([]byte(file.ID))
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractFileListQueryParams(handlerStore),
		})
	}

	// Retrieve file endpoint - GET /v1/files/{file_id}
	for _, path := range []string{
		"/v1/files/{file_id}",
		"/files/{file_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.FileRetrieveRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostFileRetrieveRequest{}
			},
			FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
				if retrieveReq, ok := req.(*schemas.BifrostFileRetrieveRequest); ok {
					if retrieveReq.Provider == "" {
						retrieveReq.Provider = schemas.OpenAI
					}
					if retrieveReq.Provider == schemas.Gemini {
						retrieveReq.FileID = strings.Replace(retrieveReq.FileID, "files-", "files/", 1)
					}
					return &FileRequest{
						Type:            schemas.FileRetrieveRequest,
						RetrieveRequest: retrieveReq,
					}, nil
				}
				return nil, errors.New("invalid file content request type")
			},
			FileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
				// Raw response is invalid even for OpenAI
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					resp.ID = strings.Replace(resp.ID, "files/", "files-", 1)
				case schemas.Bedrock:
					resp.ID = base64.StdEncoding.EncodeToString([]byte(resp.ID))
				default:
					return resp, nil
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractFileIDFromPath(handlerStore),
		})
	}

	// Delete file endpoint - DELETE /v1/files/{file_id}
	for _, path := range []string{
		"/v1/files/{file_id}",
		"/files/{file_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "DELETE",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.FileDeleteRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostFileDeleteRequest{}
			},
			FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
				if deleteReq, ok := req.(*schemas.BifrostFileDeleteRequest); ok {
					if deleteReq.Provider == "" {
						deleteReq.Provider = schemas.OpenAI
					}
					if deleteReq.Provider == schemas.Gemini {
						deleteReq.FileID = strings.Replace(deleteReq.FileID, "files-", "files/", 1)
					}
					return &FileRequest{
						Type:          schemas.FileDeleteRequest,
						DeleteRequest: deleteReq,
					}, nil
				}
				return nil, errors.New("invalid file delete request type")
			},
			FileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
				if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.OpenAI {
					return resp.ExtraFields.RawResponse, nil
				}
				switch resp.ExtraFields.Provider {
				case schemas.Gemini:
					resp.ID = strings.Replace(resp.ID, "files/", "files-", 1)
				case schemas.Bedrock:
					resp.ID = base64.StdEncoding.EncodeToString([]byte(resp.ID))
				default:
					return resp, nil
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractFileIDFromPath(handlerStore),
		})
	}

	// Get file content endpoint - GET /v1/files/{file_id}/content
	for _, path := range []string{
		"/v1/files/{file_id}/content",
		"/files/{file_id}/content",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.FileContentRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostFileContentRequest{}
			},
			FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
				if contentReq, ok := req.(*schemas.BifrostFileContentRequest); ok {
					if contentReq.Provider == "" {
						contentReq.Provider = schemas.OpenAI
					}
					switch contentReq.Provider {
					case schemas.Gemini:
						contentReq.FileID = strings.Replace(contentReq.FileID, "files-", "files/", 1)
					}
					return &FileRequest{
						Type:           schemas.FileContentRequest,
						ContentRequest: contentReq,
					}, nil
				}
				return nil, errors.New("invalid file content request type")
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractFileIDFromPath(handlerStore),
		})
	}

	return routes
}

// extractBatchListQueryParams extracts query parameters for batch list requests
func extractBatchListQueryParams(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}
		if listReq, ok := req.(*schemas.BifrostBatchListRequest); ok {
			// Extract provider from extra_query
			if provider := string(ctx.QueryArgs().Peek("provider")); provider != "" {
				listReq.Provider = schemas.ModelProvider(provider)
			}
			if listReq.Provider == "" {
				listReq.Provider = schemas.OpenAI
			}

			// Extract limit from query parameters
			if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
				if limit, err := strconv.Atoi(limitStr); err == nil {
					listReq.Limit = limit
				} else {
					// We are keeping default as 30
					listReq.Limit = 30
				}
			}

			// Extract after cursor
			if after := string(ctx.QueryArgs().Peek("after")); after != "" {
				listReq.After = &after
			}
		}

		return nil
	}
}

// extractBatchIDFromPath extracts batch_id from path parameters and provider from query params
func extractBatchIDFromPath(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}
		batchID := ctx.UserValue("batch_id")
		if batchID == nil {
			return errors.New("batch_id is required")
		}

		batchIDStr, ok := batchID.(string)
		if !ok || batchIDStr == "" {
			return errors.New("batch_id must be a non-empty string")
		}

		// Extract provider from extra_query (for GET requests)
		provider := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
		if provider == "" {
			provider = schemas.OpenAI
		}

		switch r := req.(type) {
		case *schemas.BifrostBatchRetrieveRequest:
			r.BatchID = batchIDStr
			r.Provider = provider
		case *schemas.BifrostBatchCancelRequest:
			r.BatchID = batchIDStr
			// For POST cancel, provider comes from body, only set if empty
			if r.Provider == "" {
				r.Provider = provider
			}
		case *schemas.BifrostBatchResultsRequest:
			r.BatchID = batchIDStr
			r.Provider = provider
		}

		return nil
	}
}

// extractFileListQueryParams extracts query parameters for file list requests
func extractFileListQueryParams(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		if listReq, ok := req.(*schemas.BifrostFileListRequest); ok {
			// Extract provider from extra_query
			if provider := string(ctx.QueryArgs().Peek("provider")); provider != "" {
				listReq.Provider = schemas.ModelProvider(provider)
			}
			if listReq.Provider == "" {
				listReq.Provider = schemas.OpenAI
			}

			// We extract S3 storage config from extra_query for Bedrock provider only.
			if listReq.Provider == schemas.Bedrock {
				// Extract S3 storage config from extra_query (bracket notation: storage_config[s3][bucket])
				if s3Bucket := string(ctx.QueryArgs().Peek("storage_config[s3][bucket]")); s3Bucket != "" {
					if listReq.StorageConfig == nil {
						listReq.StorageConfig = &schemas.FileStorageConfig{}
					}
					if listReq.StorageConfig.S3 == nil {
						listReq.StorageConfig.S3 = &schemas.S3StorageConfig{}
					}
					listReq.StorageConfig.S3.Bucket = s3Bucket
				}
				if s3Region := string(ctx.QueryArgs().Peek("storage_config[s3][region]")); s3Region != "" {
					if listReq.StorageConfig == nil {
						listReq.StorageConfig = &schemas.FileStorageConfig{}
					}
					if listReq.StorageConfig.S3 == nil {
						listReq.StorageConfig.S3 = &schemas.S3StorageConfig{}
					}
					listReq.StorageConfig.S3.Region = s3Region
				}
				if s3Prefix := string(ctx.QueryArgs().Peek("storage_config[s3][prefix]")); s3Prefix != "" {
					if listReq.StorageConfig == nil {
						listReq.StorageConfig = &schemas.FileStorageConfig{}
					}
					if listReq.StorageConfig.S3 == nil {
						listReq.StorageConfig.S3 = &schemas.S3StorageConfig{}
					}
					listReq.StorageConfig.S3.Prefix = s3Prefix
				}
			}

			// Extract purpose filter
			if purpose := string(ctx.QueryArgs().Peek("purpose")); purpose != "" {
				listReq.Purpose = schemas.FilePurpose(purpose)
			}

			// Extract limit
			if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
				if limit, err := strconv.Atoi(limitStr); err == nil {
					listReq.Limit = limit
				}
			}

			// Extract after cursor
			if after := string(ctx.QueryArgs().Peek("after")); after != "" {
				listReq.After = &after
			}

			// Extract order
			if order := string(ctx.QueryArgs().Peek("order")); order != "" {
				listReq.Order = &order
			}
		}

		return nil
	}
}

// extractFileIDFromPath extracts file_id from path parameters and provider/S3 config from query params
func extractFileIDFromPath(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		fileID := ctx.UserValue("file_id")
		if fileID == nil {
			return errors.New("file_id is required")
		}

		fileIDStr, ok := fileID.(string)
		if !ok || fileIDStr == "" {
			return errors.New("file_id must be a non-empty string")
		}

		// Extract provider from extra_query
		provider := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
		if provider == "" {
			provider = schemas.OpenAI
		}

		var storageConfig *schemas.FileStorageConfig
		if provider == schemas.Bedrock {
			// Check fileIDStr is base64 encoded
			if decodedFileID, err := base64.StdEncoding.DecodeString(fileIDStr); err == nil {
				fileIDStr = string(decodedFileID)
			}
			// First checking if fileIDStr starting with s3://
			if strings.HasPrefix(fileIDStr, "s3://") {
				bucket, key := parseS3URI(fileIDStr)
				storageConfig = &schemas.FileStorageConfig{
					S3: &schemas.S3StorageConfig{
						Bucket: bucket,
						Prefix: key,
					},
				}
			} else {
				// Extract S3 storage config from extra_query (bracket notation: storage_config[s3][bucket])
				s3Bucket := string(ctx.QueryArgs().Peek("storage_config[s3][bucket]"))
				s3Region := string(ctx.QueryArgs().Peek("storage_config[s3][region]"))
				s3Prefix := string(ctx.QueryArgs().Peek("storage_config[s3][prefix]"))
				if s3Bucket != "" || s3Region != "" || s3Prefix != "" {
					storageConfig = &schemas.FileStorageConfig{
						S3: &schemas.S3StorageConfig{
							Bucket: s3Bucket,
							Region: s3Region,
							Prefix: s3Prefix,
						},
					}
				}
			}
		}

		switch r := req.(type) {
		case *schemas.BifrostFileRetrieveRequest:
			r.FileID = fileIDStr
			r.Provider = provider
			if storageConfig != nil {
				r.StorageConfig = storageConfig
			}
		case *schemas.BifrostFileDeleteRequest:
			r.FileID = fileIDStr
			r.Provider = provider
			if storageConfig != nil {
				r.StorageConfig = storageConfig
			}
		case *schemas.BifrostFileContentRequest:
			r.FileID = fileIDStr
			r.Provider = provider
			if storageConfig != nil {
				r.StorageConfig = storageConfig
			}
		}

		return nil
	}
}

// parseOpenAIFileUploadMultipartRequest parses multipart/form-data for file upload requests
func parseOpenAIFileUploadMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	uploadReq, ok := req.(*schemas.BifrostFileUploadRequest)
	if !ok {
		return errors.New("invalid request type for file upload")
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract purpose (required)
	purposeValues := form.Value["purpose"]
	if len(purposeValues) == 0 || purposeValues[0] == "" {
		return errors.New("purpose field is required")
	}
	uploadReq.Purpose = schemas.FilePurpose(purposeValues[0])

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

	// Extract provider from extra_body (form field)
	if providerValues := form.Value["provider"]; len(providerValues) > 0 && providerValues[0] != "" {
		uploadReq.Provider = schemas.ModelProvider(providerValues[0])
	}

	// Extract S3 storage config from extra_body (form fields)
	// OpenAI client sends nested objects as bracket notation: storage_config[s3][bucket]
	if uploadReq.Provider == schemas.Bedrock {
		if s3BucketValues := form.Value["storage_config[s3][bucket]"]; len(s3BucketValues) > 0 && s3BucketValues[0] != "" {
			if uploadReq.StorageConfig == nil {
				uploadReq.StorageConfig = &schemas.FileStorageConfig{}
			}
			if uploadReq.StorageConfig.S3 == nil {
				uploadReq.StorageConfig.S3 = &schemas.S3StorageConfig{}
			}
			uploadReq.StorageConfig.S3.Bucket = s3BucketValues[0]
		}
		if s3RegionValues := form.Value["storage_config[s3][region]"]; len(s3RegionValues) > 0 && s3RegionValues[0] != "" {
			if uploadReq.StorageConfig == nil {
				uploadReq.StorageConfig = &schemas.FileStorageConfig{}
			}
			if uploadReq.StorageConfig.S3 == nil {
				uploadReq.StorageConfig.S3 = &schemas.S3StorageConfig{}
			}
			uploadReq.StorageConfig.S3.Region = s3RegionValues[0]
		}
		if s3PrefixValues := form.Value["storage_config[s3][prefix]"]; len(s3PrefixValues) > 0 && s3PrefixValues[0] != "" {
			if uploadReq.StorageConfig == nil {
				uploadReq.StorageConfig = &schemas.FileStorageConfig{}
			}
			if uploadReq.StorageConfig.S3 == nil {
				uploadReq.StorageConfig.S3 = &schemas.S3StorageConfig{}
			}
			uploadReq.StorageConfig.S3.Prefix = s3PrefixValues[0]
		}
	}

	return nil
}

// CreateOpenAIContainerRouteConfigs creates route configurations for OpenAI Containers API endpoints.
func CreateOpenAIContainerRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Create container endpoint - POST /v1/containers
	for _, path := range []string{
		"/v1/containers",
		"/containers",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerCreateRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerCreateRequest{}
			},
			ContainerRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if createReq, ok := req.(*schemas.BifrostContainerCreateRequest); ok {
					return &ContainerRequest{
						Type:          schemas.ContainerCreateRequest,
						CreateRequest: createReq,
					}, nil
				}
				return nil, errors.New("invalid container create request type")
			},
			ContainerCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerCreateResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
				if createReq, ok := req.(*schemas.BifrostContainerCreateRequest); ok {
					if createReq.Provider == "" {
						createReq.Provider = schemas.OpenAI
					}
				}
				return AzureEndpointPreHook(handlerStore)(ctx, bifrostCtx, req)
			},
		})
	}

	// List containers endpoint - GET /v1/containers
	for _, path := range []string{
		"/v1/containers",
		"/containers",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerListRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerListRequest{}
			},
			ContainerRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if listReq, ok := req.(*schemas.BifrostContainerListRequest); ok {
					if listReq.Provider == "" {
						listReq.Provider = schemas.OpenAI
					}
					return &ContainerRequest{
						Type:        schemas.ContainerListRequest,
						ListRequest: listReq,
					}, nil
				}
				return nil, errors.New("invalid container list request type")
			},
			ContainerListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerListResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerListQueryParams(handlerStore),
		})
	}

	// Retrieve container endpoint - GET /v1/containers/{container_id}
	for _, path := range []string{
		"/v1/containers/{container_id}",
		"/containers/{container_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerRetrieveRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerRetrieveRequest{}
			},
			ContainerRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if retrieveReq, ok := req.(*schemas.BifrostContainerRetrieveRequest); ok {
					if retrieveReq.Provider == "" {
						retrieveReq.Provider = schemas.OpenAI
					}
					return &ContainerRequest{
						Type:            schemas.ContainerRetrieveRequest,
						RetrieveRequest: retrieveReq,
					}, nil
				}
				return nil, errors.New("invalid container retrieve request type")
			},
			ContainerRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerRetrieveResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerIDFromPath(handlerStore),
		})
	}

	// Delete container endpoint - DELETE /v1/containers/{container_id}
	for _, path := range []string{
		"/v1/containers/{container_id}",
		"/containers/{container_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "DELETE",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerDeleteRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerDeleteRequest{}
			},
			ContainerRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if deleteReq, ok := req.(*schemas.BifrostContainerDeleteRequest); ok {
					if deleteReq.Provider == "" {
						deleteReq.Provider = schemas.OpenAI
					}
					return &ContainerRequest{
						Type:          schemas.ContainerDeleteRequest,
						DeleteRequest: deleteReq,
					}, nil
				}
				return nil, errors.New("invalid container delete request type")
			},
			ContainerDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerDeleteResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerIDFromPath(handlerStore),
		})
	}

	return routes
}

// extractContainerListQueryParams extracts query parameters for container list requests
func extractContainerListQueryParams(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}
		if listReq, ok := req.(*schemas.BifrostContainerListRequest); ok {
			// Extract provider from query
			if provider := string(ctx.QueryArgs().Peek("provider")); provider != "" {
				listReq.Provider = schemas.ModelProvider(provider)
			}
			if listReq.Provider == "" {
				listReq.Provider = schemas.OpenAI
			}

			// Extract limit
			if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
				if limit, err := strconv.Atoi(limitStr); err == nil {
					listReq.Limit = limit
				}
			}

			// Extract after cursor
			if after := string(ctx.QueryArgs().Peek("after")); after != "" {
				listReq.After = &after
			}

			// Extract order
			if order := string(ctx.QueryArgs().Peek("order")); order != "" {
				listReq.Order = &order
			}
		}

		return nil
	}
}

// extractContainerIDFromPath extracts container_id from path parameters and provider from query params
func extractContainerIDFromPath(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		containerID := ctx.UserValue("container_id")
		if containerID == nil {
			return errors.New("container_id is required")
		}

		containerIDStr, ok := containerID.(string)
		if !ok || containerIDStr == "" {
			return errors.New("container_id must be a non-empty string")
		}

		// Extract provider from query
		provider := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
		if provider == "" {
			provider = schemas.OpenAI
		}

		switch r := req.(type) {
		case *schemas.BifrostContainerRetrieveRequest:
			r.ContainerID = containerIDStr
			r.Provider = provider
		case *schemas.BifrostContainerDeleteRequest:
			r.ContainerID = containerIDStr
			r.Provider = provider
		}

		return nil
	}
}

// =============================================================================
// CONTAINER FILES API ROUTES
// =============================================================================

// CreateOpenAIContainerFileRouteConfigs creates route configurations for OpenAI Container Files API endpoints.
func CreateOpenAIContainerFileRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Create container file endpoint - POST /v1/containers/{container_id}/files
	for _, path := range []string{
		"/v1/containers/{container_id}/files",
		"/containers/{container_id}/files",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerFileCreateRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerFileCreateRequest{}
			},
			RequestParser: parseContainerFileCreateMultipartRequest,
			ContainerFileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerFileRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if createReq, ok := req.(*schemas.BifrostContainerFileCreateRequest); ok {
					return &ContainerFileRequest{
						Type:          schemas.ContainerFileCreateRequest,
						CreateRequest: createReq,
					}, nil
				}
				return nil, errors.New("invalid container file create request type")
			},
			ContainerFileCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileCreateResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerFileCreateParams(handlerStore),
		})
	}

	// List container files endpoint - GET /v1/containers/{container_id}/files
	for _, path := range []string{
		"/v1/containers/{container_id}/files",
		"/containers/{container_id}/files",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerFileListRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerFileListRequest{}
			},
			ContainerFileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerFileRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if listReq, ok := req.(*schemas.BifrostContainerFileListRequest); ok {
					return &ContainerFileRequest{
						Type:        schemas.ContainerFileListRequest,
						ListRequest: listReq,
					}, nil
				}
				return nil, errors.New("invalid container file list request type")
			},
			ContainerFileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileListResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerFileListQueryParams(handlerStore),
		})
	}

	// Retrieve container file endpoint - GET /v1/containers/{container_id}/files/{file_id}
	for _, path := range []string{
		"/v1/containers/{container_id}/files/{file_id}",
		"/containers/{container_id}/files/{file_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerFileRetrieveRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerFileRetrieveRequest{}
			},
			ContainerFileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerFileRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if retrieveReq, ok := req.(*schemas.BifrostContainerFileRetrieveRequest); ok {
					return &ContainerFileRequest{
						Type:            schemas.ContainerFileRetrieveRequest,
						RetrieveRequest: retrieveReq,
					}, nil
				}
				return nil, errors.New("invalid container file retrieve request type")
			},
			ContainerFileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileRetrieveResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerAndFileIDFromPath(handlerStore),
		})
	}

	// Retrieve container file content endpoint - GET /v1/containers/{container_id}/files/{file_id}/content
	for _, path := range []string{
		"/v1/containers/{container_id}/files/{file_id}/content",
		"/containers/{container_id}/files/{file_id}/content",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerFileContentRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerFileContentRequest{}
			},
			ContainerFileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerFileRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if contentReq, ok := req.(*schemas.BifrostContainerFileContentRequest); ok {
					return &ContainerFileRequest{
						Type:           schemas.ContainerFileContentRequest,
						ContentRequest: contentReq,
					}, nil
				}
				return nil, errors.New("invalid container file content request type")
			},
			ContainerFileContentResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileContentResponse) (interface{}, error) {
				return resp.Content, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerAndFileIDFromPath(handlerStore),
		})
	}

	// Delete container file endpoint - DELETE /v1/containers/{container_id}/files/{file_id}
	for _, path := range []string{
		"/v1/containers/{container_id}/files/{file_id}",
		"/containers/{container_id}/files/{file_id}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "DELETE",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ContainerFileDeleteRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &schemas.BifrostContainerFileDeleteRequest{}
			},
			ContainerFileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*ContainerFileRequest, error) {
				enableRawRequestResponseForContainer(ctx)
				if deleteReq, ok := req.(*schemas.BifrostContainerFileDeleteRequest); ok {
					return &ContainerFileRequest{
						Type:          schemas.ContainerFileDeleteRequest,
						DeleteRequest: deleteReq,
					}, nil
				}
				return nil, errors.New("invalid container file delete request type")
			},
			ContainerFileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostContainerFileDeleteResponse) (interface{}, error) {
				return resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			PreCallback: extractContainerAndFileIDFromPath(handlerStore),
		})
	}

	return routes
}

// extractContainerFileCreateParams extracts container_id from path and provider from query for file create
func extractContainerFileCreateParams(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		containerID := ctx.UserValue("container_id")
		if containerID == nil {
			return errors.New("container_id is required")
		}

		containerIDStr, ok := containerID.(string)
		if !ok || containerIDStr == "" {
			return errors.New("container_id must be a non-empty string")
		}

		provider := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
		if provider == "" {
			provider = schemas.OpenAI
		}

		if createReq, ok := req.(*schemas.BifrostContainerFileCreateRequest); ok {
			createReq.ContainerID = containerIDStr
			if createReq.Provider == "" {
				createReq.Provider = provider
			}
		}

		return nil
	}
}

// extractContainerFileListQueryParams extracts query parameters for container file list requests
func extractContainerFileListQueryParams(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		containerID := ctx.UserValue("container_id")
		if containerID == nil {
			return errors.New("container_id is required")
		}

		containerIDStr, ok := containerID.(string)
		if !ok || containerIDStr == "" {
			return errors.New("container_id must be a non-empty string")
		}

		if listReq, ok := req.(*schemas.BifrostContainerFileListRequest); ok {
			listReq.ContainerID = containerIDStr

			// Extract provider from query
			if provider := string(ctx.QueryArgs().Peek("provider")); provider != "" {
				listReq.Provider = schemas.ModelProvider(provider)
			}
			if listReq.Provider == "" {
				listReq.Provider = schemas.OpenAI
			}

			// Extract limit
			if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
				if limit, err := strconv.Atoi(limitStr); err == nil {
					listReq.Limit = limit
				}
			}

			// Extract after cursor
			if after := string(ctx.QueryArgs().Peek("after")); after != "" {
				listReq.After = &after
			}

			// Extract order
			if order := string(ctx.QueryArgs().Peek("order")); order != "" {
				listReq.Order = &order
			}
		}

		return nil
	}
}

// extractContainerAndFileIDFromPath extracts container_id and file_id from path parameters and provider from query params
func extractContainerAndFileIDFromPath(handlerStore lib.HandlerStore) PreRequestCallback {
	azureHook := AzureEndpointPreHook(handlerStore)

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		if azureHook != nil {
			if err := azureHook(ctx, bifrostCtx, req); err != nil {
				return err
			}
		}

		containerID := ctx.UserValue("container_id")
		if containerID == nil {
			return errors.New("container_id is required")
		}

		containerIDStr, ok := containerID.(string)
		if !ok || containerIDStr == "" {
			return errors.New("container_id must be a non-empty string")
		}

		fileID := ctx.UserValue("file_id")
		if fileID == nil {
			return errors.New("file_id is required")
		}

		fileIDStr, ok := fileID.(string)
		if !ok || fileIDStr == "" {
			return errors.New("file_id must be a non-empty string")
		}

		// Extract provider from query
		provider := schemas.ModelProvider(string(ctx.QueryArgs().Peek("provider")))
		if provider == "" {
			provider = schemas.OpenAI
		}

		switch r := req.(type) {
		case *schemas.BifrostContainerFileRetrieveRequest:
			r.ContainerID = containerIDStr
			r.FileID = fileIDStr
			r.Provider = provider
		case *schemas.BifrostContainerFileContentRequest:
			r.ContainerID = containerIDStr
			r.FileID = fileIDStr
			r.Provider = provider
		case *schemas.BifrostContainerFileDeleteRequest:
			r.ContainerID = containerIDStr
			r.FileID = fileIDStr
			r.Provider = provider
		}

		return nil
	}
}

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *OpenAIRouter {
	routes := CreateOpenAIRouteConfigs("/openai", handlerStore)
	routes = append(routes, CreateOpenAIListModelsRouteConfigs("/openai", handlerStore)...)
	routes = append(routes, CreateOpenAIBatchRouteConfigs("/openai", handlerStore)...)
	routes = append(routes, CreateOpenAIFileRouteConfigs("/openai", handlerStore)...)
	routes = append(routes, CreateOpenAIContainerRouteConfigs("/openai", handlerStore)...)
	routes = append(routes, CreateOpenAIContainerFileRouteConfigs("/openai", handlerStore)...)

	return &OpenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}

// parseTranscriptionMultipartRequest is a RequestParser that handles multipart/form-data for transcription requests
func parseTranscriptionMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	transcriptionReq, ok := req.(*openai.OpenAITranscriptionRequest)
	if !ok {
		return errors.New("invalid request type for transcription")
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return errors.New("model field is required")
	}
	transcriptionReq.Model = modelValues[0]

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
	transcriptionReq.File = fileData

	// Extract optional parameters
	if languageValues := form.Value["language"]; len(languageValues) > 0 && languageValues[0] != "" {
		language := languageValues[0]
		transcriptionReq.TranscriptionParameters.Language = &language
	}

	if promptValues := form.Value["prompt"]; len(promptValues) > 0 && promptValues[0] != "" {
		prompt := promptValues[0]
		transcriptionReq.TranscriptionParameters.Prompt = &prompt
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		responseFormat := responseFormatValues[0]
		transcriptionReq.TranscriptionParameters.ResponseFormat = &responseFormat
	}

	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream, err := strconv.ParseBool(streamValues[0])
		if err != nil {
			return errors.New("invalid stream value")
		}
		transcriptionReq.Stream = &stream
	}

	return nil
}

// parseOpenAIImageEditMultipartRequest is a RequestParser that handles multipart/form-data for image edit requests
func parseOpenAIImageEditMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	imageEditReq, ok := req.(*openai.OpenAIImageEditRequest)
	if !ok {
		return errors.New("invalid request type for image edit")
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return errors.New("model field is required")
	}
	imageEditReq.Model = modelValues[0]

	// Extract prompt (required)
	promptValues := form.Value["prompt"]
	if len(promptValues) == 0 || promptValues[0] == "" {
		return errors.New("prompt field is required")
	}
	prompt := promptValues[0]

	// Extract images (required) - handle both "image[]" and "image"
	var imageFiles []*multipart.FileHeader
	if imageFilesArray := form.File["image[]"]; len(imageFilesArray) > 0 {
		imageFiles = imageFilesArray
	} else if imageFilesSingle := form.File["image"]; len(imageFilesSingle) > 0 {
		imageFiles = imageFilesSingle
	}

	if len(imageFiles) == 0 {
		return errors.New("at least one image is required")
	}

	// Read all image files
	images := make([]schemas.ImageInput, 0, len(imageFiles))
	for _, fileHeader := range imageFiles {
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

		images = append(images, schemas.ImageInput{
			Image: fileData,
		})
	}

	// Create image edit input
	imageEditReq.Input = &schemas.ImageEditInput{
		Images: images,
		Prompt: prompt,
	}

	// Extract optional parameters
	if nValues := form.Value["n"]; len(nValues) > 0 && nValues[0] != "" {
		n, err := strconv.Atoi(nValues[0])
		if err != nil {
			return errors.New("invalid n value")
		}
		imageEditReq.N = &n
	}

	if sizeValues := form.Value["size"]; len(sizeValues) > 0 && sizeValues[0] != "" {
		size := sizeValues[0]
		imageEditReq.Size = &size
	}

	if qualityValues := form.Value["quality"]; len(qualityValues) > 0 && qualityValues[0] != "" {
		quality := qualityValues[0]
		imageEditReq.Quality = &quality
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		responseFormat := responseFormatValues[0]
		imageEditReq.ResponseFormat = &responseFormat
	}

	if backgroundValues := form.Value["background"]; len(backgroundValues) > 0 && backgroundValues[0] != "" {
		background := backgroundValues[0]
		imageEditReq.Background = &background
	}

	if inputFidelityValues := form.Value["input_fidelity"]; len(inputFidelityValues) > 0 && inputFidelityValues[0] != "" {
		inputFidelity := inputFidelityValues[0]
		imageEditReq.InputFidelity = &inputFidelity
	}

	if partialImagesValues := form.Value["partial_images"]; len(partialImagesValues) > 0 && partialImagesValues[0] != "" {
		partialImages, err := strconv.Atoi(partialImagesValues[0])
		if err != nil {
			return errors.New("invalid partial_images value")
		}
		imageEditReq.PartialImages = &partialImages
	}

	if outputFormatValues := form.Value["output_format"]; len(outputFormatValues) > 0 && outputFormatValues[0] != "" {
		outputFormat := outputFormatValues[0]
		imageEditReq.OutputFormat = &outputFormat
	}

	if numInferenceStepsValues := form.Value["num_inference_steps"]; len(numInferenceStepsValues) > 0 && numInferenceStepsValues[0] != "" {
		numInferenceSteps, err := strconv.Atoi(numInferenceStepsValues[0])
		if err != nil {
			return errors.New("invalid num_inference_steps value")
		}
		imageEditReq.NumInferenceSteps = &numInferenceSteps
	}

	if seedValues := form.Value["seed"]; len(seedValues) > 0 && seedValues[0] != "" {
		seed, err := strconv.Atoi(seedValues[0])
		if err != nil {
			return errors.New("invalid seed value")
		}
		imageEditReq.Seed = &seed
	}

	if outputCompressionValues := form.Value["output_compression"]; len(outputCompressionValues) > 0 && outputCompressionValues[0] != "" {
		outputCompression, err := strconv.Atoi(outputCompressionValues[0])
		if err != nil {
			return errors.New("invalid output_compression value")
		}
		imageEditReq.OutputCompression = &outputCompression
	}

	if negativePromptValues := form.Value["negative_prompt"]; len(negativePromptValues) > 0 && negativePromptValues[0] != "" {
		negativePrompt := negativePromptValues[0]
		imageEditReq.NegativePrompt = &negativePrompt
	}

	if userValues := form.Value["user"]; len(userValues) > 0 && userValues[0] != "" {
		user := userValues[0]
		imageEditReq.User = &user
	}

	// Extract type (required for Bedrock, optional for others)
	if typeValues := form.Value["type"]; len(typeValues) > 0 && typeValues[0] != "" {
		editType := typeValues[0]
		imageEditReq.Type = &editType
	}

	// Extract mask if present
	if maskFiles := form.File["mask"]; len(maskFiles) > 0 {
		maskFile := maskFiles[0]
		file, err := maskFile.Open()
		if err != nil {
			return err
		}
		defer file.Close()

		maskData, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		imageEditReq.Mask = maskData
	}

	// Extract stream parameter
	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream, err := strconv.ParseBool(streamValues[0])
		if err != nil {
			return errors.New("invalid stream value")
		}
		imageEditReq.Stream = &stream
	}

	// Extract fallbacks
	if fallbackValues := form.Value["fallbacks"]; len(fallbackValues) > 0 {
		imageEditReq.Fallbacks = fallbackValues
	}

	return nil
}

// parseOpenAIImageVariationMultipartRequest parses multipart/form-data for image variation requests
func parseOpenAIImageVariationMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	imageVariationReq, ok := req.(*openai.OpenAIImageVariationRequest)
	if !ok {
		return errors.New("invalid request type for image variation")
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		return errors.New("model field is required")
	}
	imageVariationReq.Model = modelValues[0]

	// Extract image (required) - handle both "image[]" and "image"
	var imageFiles []*multipart.FileHeader
	if imageFilesArray := form.File["image[]"]; len(imageFilesArray) > 0 {
		imageFiles = imageFilesArray
	} else if imageFilesSingle := form.File["image"]; len(imageFilesSingle) > 0 {
		imageFiles = imageFilesSingle
	}

	if len(imageFiles) == 0 {
		return errors.New("at least one image is required")
	}

	// Read first image file (image variation only uses the first image)
	fileHeader := imageFiles[0]
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

	// Create image variation input
	imageVariationReq.Input = &schemas.ImageVariationInput{
		Image: schemas.ImageInput{
			Image: fileData,
		},
	}

	// Extract optional parameters
	if nValues := form.Value["n"]; len(nValues) > 0 && nValues[0] != "" {
		n, err := strconv.Atoi(nValues[0])
		if err != nil {
			return errors.New("invalid n value")
		}
		imageVariationReq.N = &n
	}

	if sizeValues := form.Value["size"]; len(sizeValues) > 0 && sizeValues[0] != "" {
		size := sizeValues[0]
		imageVariationReq.Size = &size
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		responseFormat := responseFormatValues[0]
		imageVariationReq.ResponseFormat = &responseFormat
	}

	if userValues := form.Value["user"]; len(userValues) > 0 && userValues[0] != "" {
		user := userValues[0]
		imageVariationReq.User = &user
	}
	// Extract fallbacks
	if fallbackValues := form.Value["fallbacks"]; len(fallbackValues) > 0 {
		imageVariationReq.Fallbacks = fallbackValues
	}
	return nil
}

// enableRawRequestResponseForContainer sets context flags to always capture raw request/response
// for container operations. Container operations don't have model-specific content, so raw
// data is useful for debugging and should be enabled by default.
func enableRawRequestResponseForContainer(bifrostCtx *schemas.BifrostContext) {
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	bifrostCtx.SetValue(schemas.BifrostContextKeyRawRequestResponseForLogging, true)
}

// parseContainerFileCreateMultipartRequest is a RequestParser that handles multipart/form-data for container file create requests
func parseContainerFileCreateMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	createReq, ok := req.(*schemas.BifrostContainerFileCreateRequest)
	if !ok {
		return errors.New("invalid request type for container file create")
	}

	contentType := string(ctx.Request.Header.ContentType())
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		return nil // Let JSON parsing handle it
	}

	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		return err
	}

	// Extract file (optional for multipart - could be file_id instead)
	if fileHeaders := form.File["file"]; len(fileHeaders) > 0 {
		fileHeader := fileHeaders[0]
		file, err := fileHeader.Open()
		if err != nil {
			return err
		}
		defer file.Close()

		fileData, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		createReq.File = fileData
	}

	// Extract optional file_id
	if fileIDValues := form.Value["file_id"]; len(fileIDValues) > 0 && fileIDValues[0] != "" {
		fileID := fileIDValues[0]
		createReq.FileID = &fileID
	}

	// Extract optional file_path
	if filePathValues := form.Value["file_path"]; len(filePathValues) > 0 && filePathValues[0] != "" {
		filePath := filePathValues[0]
		createReq.Path = &filePath
	}

	// Extract optional provider
	if providerValues := form.Value["provider"]; len(providerValues) > 0 {
		if providerValue := strings.TrimSpace(providerValues[0]); providerValue != "" {
			createReq.Provider = schemas.ModelProvider(providerValue)
		}
	}

	return nil
}
