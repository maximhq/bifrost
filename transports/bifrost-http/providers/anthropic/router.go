package anthropic

import (
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/providers"
)

// AnthropicRouter holds route registrations for Anthropic endpoints.
// It supports standard chat completions and image-enabled vision capabilities.
type AnthropicRouter struct {
	*providers.ProviderRouter
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost) *AnthropicRouter {
	routes := []providers.RouteConfig{
		{
			Path:   "/anthropic/v1/messages",
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
		},
	}

	return &AnthropicRouter{
		ProviderRouter: providers.NewProviderRouter(client, routes),
	}
}
