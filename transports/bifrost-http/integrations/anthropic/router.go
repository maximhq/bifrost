package anthropic

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

// AnthropicRouter handles Anthropic-compatible API endpoints
type AnthropicRouter struct {
	*integrations.GenericRouter
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string) []integrations.RouteConfig {
	var routes []integrations.RouteConfig

	// Messages endpoint
	routes = append(routes, integrations.RouteConfig{
		Path:   pathPrefix + "/v1/messages",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &AnthropicMessageRequest{}
		},
		RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
			if anthropicReq, ok := req.(*AnthropicMessageRequest); ok {
				return anthropicReq.ConvertToBifrostRequest(), nil
			}
			return nil, errors.New("invalid request type")
		},
		ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
			return DeriveAnthropicFromBifrostResponse(resp), nil
		},
		ErrorConverter: func(err *schemas.BifrostError) interface{} {
			return DeriveAnthropicErrorFromBifrostError(err)
		},
		StreamConfig: &integrations.StreamConfig{
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveAnthropicStreamFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveAnthropicStreamFromBifrostError(err)
			},
		},
	})

	// Add management endpoints 
	routes = append(routes, createAnthropicManagementRoutes(pathPrefix)...)

	return routes
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *AnthropicRouter {
	return &AnthropicRouter{
		GenericRouter: integrations.NewGenericRouter(client, handlerStore, CreateAnthropicRouteConfigs("/anthropic")),
	}
}

// createAnthropicManagementRoutes creates route configurations for Anthropic management endpoints
func createAnthropicManagementRoutes(pathPrefix string) []integrations.RouteConfig {
	var routes []integrations.RouteConfig

	// Management endpoints - following the same for-loop pattern as other routes
	for _, path := range []string{
		// "/v1/models",
		// "/v1/usage",
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
				return DeriveAnthropicErrorFromBifrostError(err)
			},
			PreCallback: handleAnthropicManagementRequest,
		})
	}

	return routes
}

// handleAnthropicManagementRequest handles management endpoint requests by forwarding directly to Anthropic API
func handleAnthropicManagementRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
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
	switch {
	case strings.HasSuffix(path, "/v1/models"):
		endpoint = "/v1/models"
	case strings.HasSuffix(path, "/v1/usage"):
		endpoint = "/v1/usage"
	default:
		integrations.SendManagementError(ctx, fmt.Errorf("unknown management endpoint"), 404)
		return fmt.Errorf("unknown management endpoint")
	}

	// Create management client and forward the request
	client := integrations.NewManagementAPIClient()
	response, err := client.ForwardRequest(ctx, schemas.Anthropic, endpoint, apiKey, queryParams)
	if err != nil {
		integrations.SendManagementError(ctx, err, 500)
		return err
	}

	// Send the response
	integrations.SendManagementResponse(ctx, response.Data, response.StatusCode)
	return nil
}
