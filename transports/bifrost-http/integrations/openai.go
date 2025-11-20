package integrations

import (
	"context"
	"errors"
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

func AzureEndpointPreHook(handlerStore lib.HandlerStore) func(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
		azureKey := ctx.Request.Header.Peek("authorization")
		deploymentEndpoint := ctx.Request.Header.Peek("x-bf-azure-endpoint")
		deploymentID := ctx.UserValue("deployment-id")
		apiVersion := ctx.QueryArgs().Peek("api-version")

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
				key.Value = strings.TrimPrefix(azureKeyStr, "Bearer ")
				key.AzureKeyConfig.Endpoint = deploymentEndpointStr
				key.AzureKeyConfig.Deployments = map[string]string{deploymentIDStr: deploymentIDStr}
			}

			if apiVersionStr != "" {
				key.AzureKeyConfig.APIVersion = &apiVersionStr
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
			GetRequestTypeInstance: func() interface{} {
				return &openai.OpenAITextCompletionRequest{}
			},
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAITextCompletionRequest); ok {
					return &schemas.BifrostRequest{
						TextCompletionRequest: openaiReq.ToBifrostTextCompletionRequest(),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			TextResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				TextStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTextCompletionResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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
			GetRequestTypeInstance: func() interface{} {
				return &openai.OpenAIChatRequest{}
			},
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAIChatRequest); ok {
					return &schemas.BifrostRequest{
						ChatRequest: openaiReq.ToBifrostChatRequest(),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ChatResponseConverter: func(ctx *context.Context, resp *schemas.BifrostChatResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ChatStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostChatResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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
			GetRequestTypeInstance: func() interface{} {
				return &openai.OpenAIResponsesRequest{}
			},
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAIResponsesRequest); ok {
					return &schemas.BifrostRequest{
						ResponsesRequest: openaiReq.ToBifrostResponsesRequest(),
					}, nil

				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return string(resp.Type), resp.ExtraFields.RawResponse, nil
						}
					}
					return string(resp.Type), resp, nil
				},
				ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
					return err
				},
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
			GetRequestTypeInstance: func() interface{} {
				return &openai.OpenAIEmbeddingRequest{}
			},
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if embeddingReq, ok := req.(*openai.OpenAIEmbeddingRequest); ok {
					return &schemas.BifrostRequest{
						EmbeddingRequest: embeddingReq.ToBifrostEmbeddingRequest(),
					}, nil
				}
				return nil, errors.New("invalid embedding request type")
			},
			EmbeddingResponseConverter: func(ctx *context.Context, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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
			GetRequestTypeInstance: func() interface{} {
				return &openai.OpenAISpeechRequest{}
			},
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if speechReq, ok := req.(*openai.OpenAISpeechRequest); ok {
					return &schemas.BifrostRequest{
						SpeechRequest: speechReq.ToBifrostSpeechRequest(),
					}, nil
				}
				return nil, errors.New("invalid speech request type")
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				SpeechStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostSpeechStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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
			GetRequestTypeInstance: func() interface{} {
				return &openai.OpenAITranscriptionRequest{}
			},
			RequestParser: parseTranscriptionMultipartRequest, // Handle multipart form parsing
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if transcriptionReq, ok := req.(*openai.OpenAITranscriptionRequest); ok {
					return &schemas.BifrostRequest{
						TranscriptionRequest: transcriptionReq.ToBifrostTranscriptionRequest(),
					}, nil
				}
				return nil, errors.New("invalid transcription request type")
			},
			TranscriptionResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTranscriptionResponse) (interface{}, error) {
				if resp.ExtraFields.Provider == schemas.OpenAI {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return resp, nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				TranscriptionStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTranscriptionStreamResponse) (string, interface{}, error) {
					if resp.ExtraFields.Provider == schemas.OpenAI {
						if resp.ExtraFields.RawResponse != nil {
							return "", resp.ExtraFields.RawResponse, nil
						}
					}
					return "", resp, nil
				},
				ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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
				return openai.ToOpenAIListModelsResponse(resp), nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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

	return func(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
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

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *OpenAIRouter {
	return &OpenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, append(CreateOpenAIRouteConfigs("/openai", handlerStore), CreateOpenAIListModelsRouteConfigs("/openai", handlerStore)...), logger),
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
	fileData := make([]byte, fileHeader.Size)
	if _, err := file.Read(fileData); err != nil {
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
