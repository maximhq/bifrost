package openai

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
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
	*integrations.GenericRouter
}

func AzureEndpointPreHook(handlerStore lib.HandlerStore) func(ctx *fasthttp.RequestCtx, req interface{}) error {
	return func(ctx *fasthttp.RequestCtx, req interface{}) error {
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
			case *OpenAIChatRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *OpenAISpeechRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *OpenAITranscriptionRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
			case *OpenAIEmbeddingRequest:
				r.Model = setAzureModelName(r.Model, deploymentIDStr)
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
func CreateOpenAIRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []integrations.RouteConfig {
	var routes []integrations.RouteConfig

	// Chat completions endpoint
	for _, path := range []string{
		"/v1/chat/completions",
		"/chat/completions",
		"/openai/deployments/{deployment-id}/chat/completions",
	} {
		routes = append(routes, integrations.RouteConfig{
			Path:   pathPrefix + path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAIChatRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*OpenAIChatRequest); ok {
					return openaiReq.ConvertToBifrostRequest(pathPrefix != "/openai"), nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAIFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveOpenAIStreamFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveOpenAIStreamFromBifrostError(err)
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
		routes = append(routes, integrations.RouteConfig{
			Path:   pathPrefix + path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAIEmbeddingRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if embeddingReq, ok := req.(*OpenAIEmbeddingRequest); ok {
					return embeddingReq.ConvertToBifrostRequest(pathPrefix != "/openai"), nil
				}
				return nil, errors.New("invalid embedding request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAIEmbeddingFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
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
		routes = append(routes, integrations.RouteConfig{
			Path:   pathPrefix + path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAISpeechRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if speechReq, ok := req.(*OpenAISpeechRequest); ok {
					return speechReq.ConvertToBifrostRequest(pathPrefix != "/openai"), nil
				}
				return nil, errors.New("invalid speech request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				speechResp := DeriveOpenAISpeechFromBifrostResponse(resp)
				if speechResp == nil {
					return nil, errors.New("failed to convert speech response")
				}
				// For speech, we return the raw audio data directly
				return speechResp.Audio, nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveOpenAISpeechFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveOpenAIErrorFromBifrostError(err)
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
		routes = append(routes, integrations.RouteConfig{
			Path:   pathPrefix + path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAITranscriptionRequest{}
			},
			RequestParser: parseTranscriptionMultipartRequest, // Handle multipart form parsing
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if transcriptionReq, ok := req.(*OpenAITranscriptionRequest); ok {
					return transcriptionReq.ConvertToBifrostRequest(pathPrefix != "/openai"), nil
				}
				return nil, errors.New("invalid transcription request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAITranscriptionFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveOpenAIErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveOpenAITranscriptionFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveOpenAIErrorFromBifrostError(err)
				},
			},
			PreCallback: AzureEndpointPreHook(handlerStore),
		})
	}

	// Add management endpoints only for primary OpenAI integration
	if pathPrefix == "/openai" {
		routes = append(routes, createOpenAIManagementRoutes(pathPrefix)...)
	}

	return routes
}

// createOpenAIManagementRoutes creates route configurations for OpenAI management endpoints
func createOpenAIManagementRoutes(pathPrefix string) []integrations.RouteConfig {
	var routes []integrations.RouteConfig
	log.Printf("createOpenAIManagementRoutes called with pathPrefix: %s", pathPrefix)
	
	// Management endpoints - following the same for-loop pattern as other routes
	for _, path := range []string{
		"/v1/models",
		"/v1/organizations", 
		"/v1/usage",
		"/v1/models/{model}",
	} {
		fullPath := pathPrefix + path
		log.Printf("Creating management route: %s", fullPath)
		routes = append(routes, integrations.RouteConfig{
			Path:   fullPath,
			Method: "GET",
			GetRequestTypeInstance: func() interface{} {
				return &integrations.ManagementRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				// For management endpoints, we create a minimal BifrostRequest
				// The actual API call is handled by the PreCallback (handleOpenAIManagementRequest)
				return &schemas.BifrostRequest{
					Provider: schemas.OpenAI,
					Model:    "management", // Special model type for management requests
					Input:    schemas.RequestInput{}, // Empty input - management doesn't need chat data
				}, nil
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return map[string]interface{}{
					"object": "list",
					"data":   []interface{}{},
				}, nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return map[string]interface{}{
					"object": "list",
					"data":   []interface{}{},
				}
			},
			PreCallback: handleOpenAIManagementRequest,
		})
	}

	return routes
}

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *OpenAIRouter {
	return &OpenAIRouter{
		GenericRouter: integrations.NewGenericRouter(client, handlerStore, CreateOpenAIRouteConfigs("/openai", handlerStore)),
	}
}

// parseTranscriptionMultipartRequest is a RequestParser that handles multipart/form-data for transcription requests
func parseTranscriptionMultipartRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	transcriptionReq, ok := req.(*OpenAITranscriptionRequest)
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
		transcriptionReq.Language = &language
	}

	if promptValues := form.Value["prompt"]; len(promptValues) > 0 && promptValues[0] != "" {
		prompt := promptValues[0]
		transcriptionReq.Prompt = &prompt
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		responseFormat := responseFormatValues[0]
		transcriptionReq.ResponseFormat = &responseFormat
	}

	if temperatureValues := form.Value["temperature"]; len(temperatureValues) > 0 && temperatureValues[0] != "" {
		temp, err := strconv.ParseFloat(temperatureValues[0], 64)
		if err != nil {
			return errors.New("invalid temperature value")
		}
		transcriptionReq.Temperature = &temp
	}

	// Handle include[] array format used by OpenAI
	if includeValues := form.Value["include[]"]; len(includeValues) > 0 {
		transcriptionReq.Include = includeValues
	} else if includeValues := form.Value["include"]; len(includeValues) > 0 && includeValues[0] != "" {
		// Fallback: Handle comma-separated values for backwards compatibility
		includes := strings.Split(includeValues[0], ",")
		// Trim whitespace from each value
		for i, v := range includes {
			includes[i] = strings.TrimSpace(v)
		}
		transcriptionReq.Include = includes
	}

	// Handle timestamp_granularities[] array format used by OpenAI
	if timestampValues := form.Value["timestamp_granularities[]"]; len(timestampValues) > 0 {
		transcriptionReq.TimestampGranularities = timestampValues
	} else if timestampValues := form.Value["timestamp_granularities"]; len(timestampValues) > 0 && timestampValues[0] != "" {
		// Fallback: Handle comma-separated values for backwards compatibility
		granularities := strings.Split(timestampValues[0], ",")
		// Trim whitespace from each value
		for i, v := range granularities {
			granularities[i] = strings.TrimSpace(v)
		}
		transcriptionReq.TimestampGranularities = granularities
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


// handleOpenAIManagementRequest handles management endpoint requests by forwarding directly to OpenAI API
func handleOpenAIManagementRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	log.Printf("handleOpenAIManagementRequest called with path: %s", string(ctx.Path()))
	log.Printf("Request method: %s", string(ctx.Method()))
	
	// Extract API key from request
	apiKey, err := integrations.ExtractAPIKeyFromContext(ctx)
	if err != nil {
		integrations.SendManagementError(ctx, err, 401)
		ctx.SetUserValue("management_handled", true)
		return nil // Don't return error, we've handled the request
	}

	// Extract query parameters
	queryParams := integrations.ExtractQueryParams(ctx)

	// Determine the endpoint based on the path
	var endpoint string
	path := string(ctx.Path())
	
	// Remove the path prefix to get the actual endpoint
	if strings.HasPrefix(path, "/openai") {
		endpoint = strings.TrimPrefix(path, "/openai")
	} else if strings.HasPrefix(path, "/litellm") {
		endpoint = strings.TrimPrefix(path, "/litellm")
	} else if strings.HasPrefix(path, "/langchain") {
		endpoint = strings.TrimPrefix(path, "/langchain")
	} else {
		endpoint = path
	}
	
	log.Println("endpoint", endpoint)
	// Validate that it's a known management endpoint
	switch {
	case endpoint == "/v1/models":
	case strings.HasPrefix(endpoint, "/v1/models/"):
	case endpoint == "/v1/organizations":
	case endpoint == "/v1/usage":
	default:
		integrations.SendManagementError(ctx, fmt.Errorf("unknown management endpoint: %s", endpoint), 404)
		ctx.SetUserValue("management_handled", true)
		return nil // Don't return error, we've handled the request
	}
	log.Println("endpoint", endpoint)

	// Create management client and forward the request
	client := integrations.NewManagementAPIClient()
	response, err := client.ForwardRequest(ctx, schemas.OpenAI, endpoint, apiKey, queryParams)
	if err != nil {
		integrations.SendManagementError(ctx, err, 500)
		ctx.SetUserValue("management_handled", true)
		return nil
	}

	// Check if the response indicates an error (4xx, 5xx status codes)
	if response.StatusCode >= 400 {
		log.Printf("OpenAI API returned error status %d: %s", response.StatusCode, string(response.Data))
		integrations.SendManagementResponse(ctx, response.Data, response.StatusCode)
		ctx.SetUserValue("management_handled", true)
		return nil
	}

	// Send the successful response
	integrations.SendManagementResponse(ctx, response.Data, response.StatusCode)
	ctx.SetUserValue("management_handled", true)
	return nil
}
