package integrations

import (
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/anthropic"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// AnthropicRouter handles Anthropic-compatible API endpoints
type AnthropicRouter struct {
	*GenericRouter
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string) []RouteConfig {
	createConfig := func(path string) RouteConfig {
		return RouteConfig{
			Path:   path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &anthropic.AnthropicMessageRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					chatReq := anthropicReq.ToBifrostRequest()
					return &schemas.BifrostRequest{
						Provider:    chatReq.Provider,
						Model:       chatReq.Model,
						ChatRequest: chatReq,
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				if resp.ExtraFields.RawResponse != nil {
					if rawBytes, ok := resp.ExtraFields.RawResponse.([]byte); ok {
						return rawBytes, nil
					}
				}
				return anthropic.ToAnthropicChatCompletionResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			StreamConfig: &StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return anthropic.ToAnthropicChatCompletionStreamResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return anthropic.ToAnthropicChatCompletionStreamError(err)
				},
			},
		}
	}

	return []RouteConfig{
		createConfig(pathPrefix + "/v1/messages"),
		createConfig(pathPrefix + "/v1/messages/{path:*}"),
	}
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *AnthropicRouter {
	return &AnthropicRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateAnthropicRouteConfigs("/anthropic")),
	}
}
