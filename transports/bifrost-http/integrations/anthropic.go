package integrations

import (
	"errors"
	"fmt"
	"strconv"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/anthropic"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AnthropicRouter handles Anthropic-compatible API endpoints
type AnthropicRouter struct {
	*GenericRouter
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string) []RouteConfig {
	return []RouteConfig{
		{
			Type:   RouteConfigTypeAnthropic,
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

func CreateAnthropicListModelsRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	return []RouteConfig{
		{
			Type:   RouteConfigTypeAnthropic,
			Path:   pathPrefix + "/v1/models",
			Method: "GET",
			GetRequestTypeInstance: func() interface{} {
				return &schemas.BifrostListModelsRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
					return &schemas.BifrostRequest{
						ListModelsRequest: listModelsReq,
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ListModelsResponseConverter: func(resp *schemas.BifrostListModelsResponse) (interface{}, error) {
				return anthropic.ToAnthropicListModelsResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			PreCallback: extractAnthropicListModelsParams,
		},
	}
}

// extractAnthropicListModelsParams extracts query parameters for list models request
func extractAnthropicListModelsParams(ctx *fasthttp.RequestCtx, req interface{}) error {
	if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
		// Set provider to Anthropic
		listModelsReq.Provider = schemas.Anthropic

		// Extract limit from query parameters
		if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
			if limit, err := strconv.Atoi(limitStr); err == nil {
				listModelsReq.PageSize = limit
			} else {
				return fmt.Errorf("invalid limit parameter: %w", err)
			}
		}

		if beforeID := string(ctx.QueryArgs().Peek("before_id")); beforeID != "" {
			if listModelsReq.ExtraParams == nil {
				listModelsReq.ExtraParams = make(map[string]interface{})
			}
			listModelsReq.ExtraParams["before_id"] = beforeID
		}

		if afterID := string(ctx.QueryArgs().Peek("after_id")); afterID != "" {
			if listModelsReq.ExtraParams == nil {
				listModelsReq.ExtraParams = make(map[string]interface{})
			}
			listModelsReq.ExtraParams["after_id"] = afterID
		}

		return nil
	}
	return errors.New("invalid request type for Anthropic list models")
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *AnthropicRouter {
	return &AnthropicRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, append(CreateAnthropicRouteConfigs("/anthropic"), CreateAnthropicListModelsRouteConfigs("/anthropic", handlerStore)...), logger),
	}
}
