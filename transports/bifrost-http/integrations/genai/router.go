package genai

import (
	"errors"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	*integrations.GenericRouter
}

// CreateGenAIRouteConfigs creates a route configurations for GenAI endpoints.
func CreateGenAIRouteConfigs(pathPrefix string) []integrations.RouteConfig {
	var routes []integrations.RouteConfig

	// Chat completions endpoint
	routes = append(routes, integrations.RouteConfig{
		Path:   pathPrefix + "/v1beta/models/{model:*}",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &GeminiChatRequest{}
		},
		RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
			if geminiReq, ok := req.(*GeminiChatRequest); ok {
				return geminiReq.ConvertToBifrostRequest(), nil
			}
			return nil, errors.New("invalid request type")
		},
		ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
			return DeriveGenAIFromBifrostResponse(resp), nil
		},
		ErrorConverter: func(err *schemas.BifrostError) interface{} {
			return DeriveGeminiErrorFromBifrostError(err)
		},
		StreamConfig: &integrations.StreamConfig{
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveGeminiStreamFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveGeminiStreamFromBifrostError(err)
			},
		},
		PreCallback: extractAndSetModelFromURL,
	})

	// Add management endpoints 
	routes = append(routes, createGenAIManagementRoutes(pathPrefix)...)

	return routes
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *GenAIRouter {
	return &GenAIRouter{
		GenericRouter: integrations.NewGenericRouter(client, handlerStore, CreateGenAIRouteConfigs("/genai")),
	}
}

var embeddingPaths = []string{
	":embedContent",
	":batchEmbedContents",
	":predict",
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractAndSetModelFromURL(ctx *fasthttp.RequestCtx, req interface{}) error {
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
	if geminiReq, ok := req.(*GeminiChatRequest); ok {
		geminiReq.Model = modelStr
		geminiReq.Stream = isStreaming
		geminiReq.IsEmbedding = isEmbedding
		return nil
	}

	return fmt.Errorf("invalid request type for GenAI")
}

// createGenAIManagementRoutes creates route configurations for Google GenAI management endpoints
func createGenAIManagementRoutes(pathPrefix string) []integrations.RouteConfig {
	var routes []integrations.RouteConfig

	// Management endpoints - following the same for-loop pattern as other routes
	for _, path := range []string{
		// "/v1beta/models",
		// "/v1beta/models/{model:*}",
	} {
		routes = append(routes, integrations.RouteConfig{
			Path:   pathPrefix + path,
			Method: "GET",
			GetRequestTypeInstance: func() interface{} {
				return &integrations.ManagementRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				
				return nil, nil
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				
				return nil, nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveGeminiErrorFromBifrostError(err)
			},
			PreCallback: handleGenAIManagementRequest,
		})
	}

	return routes
}

// handleGenAIManagementRequest handles management endpoint requests by forwarding directly to Google GenAI API
func handleGenAIManagementRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	// Extract API key from request
	apiKey, err := integrations.ExtractAPIKeyFromContext(ctx)
	if err != nil {
		integrations.SendManagementError(ctx, err, 401)
		return err
	}

	// Extract query parameters
	queryParams := integrations.ExtractQueryParams(ctx)

	// Determine the endpoint based on the path
	var endpoint string
	path := string(ctx.Path())
	
	if strings.HasSuffix(path, "/v1beta/models") && !strings.Contains(path, "/v1beta/models/") {
		// List models endpoint
		endpoint = "/v1beta/models"
	} else if strings.Contains(path, "/v1beta/models/") {
		// Model details endpoint - extract model from path
		model := ctx.UserValue("model")
		if model == nil {
			integrations.SendManagementError(ctx, fmt.Errorf("model parameter is required"), 400)
			return fmt.Errorf("model parameter is required")
		}
		modelStr := model.(string)
		endpoint = "/v1beta/models/" + modelStr
	} else {
		integrations.SendManagementError(ctx, fmt.Errorf("unknown management endpoint"), 404)
		return fmt.Errorf("unknown management endpoint")
	}

	// Create management client and forward the request
	client := integrations.NewManagementAPIClient()
	response, err := client.ForwardRequest(ctx, schemas.Gemini, endpoint, apiKey, queryParams)
	if err != nil {
		integrations.SendManagementError(ctx, err, 500)
		return err
	}

	// Send the response
	integrations.SendManagementResponse(ctx, response.Data, response.StatusCode)
	return nil
}
