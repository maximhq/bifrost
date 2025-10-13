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
	return []RouteConfig{
		{
			Path:   pathPrefix + "/v1/complete",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &anthropic.AnthropicTextRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicTextRequest); ok {
					return &schemas.BifrostRequest{
						TextCompletionRequest: anthropicReq.ToBifrostRequest(),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			TextResponseConverter: func(resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
				return anthropic.ToAnthropicTextCompletionResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
		},
		{
			Path:   pathPrefix + "/v1/messages",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &anthropic.AnthropicMessageRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					return &schemas.BifrostRequest{
						ResponsesRequest: anthropicReq.ToBifrostResponsesRequest(),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				return anthropic.ToAnthropicResponsesResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(resp *schemas.BifrostResponsesStreamResponse) (interface{}, error) {
					return anthropic.ToAnthropicResponsesStreamResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return anthropic.ToAnthropicResponsesStreamError(err)
				},
			},
		},
	}
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *AnthropicRouter {
	return &AnthropicRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateAnthropicRouteConfigs("/anthropic"), logger),
	}
}
