package anthropic

import (
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// AnthropicRouter handles Anthropic-compatible API endpoints
type AnthropicRouter struct {
	*integrations.GenericRouter
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string) []integrations.RouteConfig {
	createConfig := func(path string) integrations.RouteConfig {
		return integrations.RouteConfig{
			Path:   path,
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
				if resp.ExtraFields.RawResponse != nil {
					if rawBytes, ok := resp.ExtraFields.RawResponse.([]byte); ok {
						return rawBytes, nil
					}
				}
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
		}
	}

	return []integrations.RouteConfig{
		createConfig(pathPrefix + "/v1/messages"),
		createConfig(pathPrefix + "/v1/messages/{path:*}"),
	}
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *AnthropicRouter {
	return &AnthropicRouter{
		GenericRouter: integrations.NewGenericRouter(client, handlerStore, CreateAnthropicRouteConfigs("/anthropic")),
	}
}
