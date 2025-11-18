package integrations

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// AnthropicRouter handles Anthropic-compatible API endpoints
type AnthropicRouter struct {
	*GenericRouter
}

// createAnthropicCompleteRouteConfig creates a route configuration for the `/v1/complete` endpoint.
func createAnthropicCompleteRouteConfig(pathPrefix string) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/complete",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicTextRequest{}
		},
		RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
			if anthropicReq, ok := req.(*anthropic.AnthropicTextRequest); ok {
				return &schemas.BifrostRequest{
					TextCompletionRequest: anthropicReq.ToBifrostTextCompletionRequest(),
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
	}
}

// createAnthropicMessagesRouteConfig creates a route configuration for the `/v1/messages` endpoint.
func createAnthropicMessagesRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig
	for _, path := range []string{
		"/v1/messages",
		"/v1/messages/{path:*}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeAnthropic,
			Path:   pathPrefix + path,
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &anthropic.AnthropicMessageRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					provider, _ := schemas.ParseModelString(anthropicReq.Model, schemas.Anthropic)

					if handlerStore != nil {
						config, err := handlerStore.GetProviderConfig(provider)
						if err != nil {
							return nil, fmt.Errorf("failed to get config for provider %s: %w", provider, err)
						}

						if custom := config.CustomProviderConfig; custom != nil && custom.IsOperationAllowed != nil {
							if custom.IsOperationAllowed(schemas.ResponsesRequest) {
								return &schemas.BifrostRequest{ResponsesRequest: anthropicReq.ToBifrostResponsesRequest()}, nil
							}
							if custom.IsOperationAllowed(schemas.ChatCompletionRequest) {
								return &schemas.BifrostRequest{ChatRequest: anthropicReq.ToBifrostChatRequest()}, nil
							}
						}
					}

					// For builtin providers without explicit overrides, use sane defaults.
					if provider == schemas.Anthropic || provider == schemas.OpenAI {
						return &schemas.BifrostRequest{ResponsesRequest: anthropicReq.ToBifrostResponsesRequest()}, nil
					}

					return &schemas.BifrostRequest{ChatRequest: anthropicReq.ToBifrostChatRequest()}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ChatResponseConverter: func(resp *schemas.BifrostChatResponse) (interface{}, error) {
				// Convert chat response to Anthropic format
				return anthropic.ToAnthropicChatCompletionResponse(resp), nil
			},
			ResponsesResponseConverter: func(resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				// Only handles actual Responses API responses
				if resp.ExtraFields.Provider == schemas.Anthropic {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return anthropic.ToAnthropicResponsesResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(resp *schemas.BifrostResponsesStreamResponse) (interface{}, error) {
					// Only handles actual Responses API streaming responses
					return anthropic.ToAnthropicResponsesStreamResponse(resp), nil
				},
				ChatStreamResponseConverter: func(resp *schemas.BifrostChatResponse) (interface{}, error) {
					// Convert chat stream response to Anthropic format
					return anthropic.ToAnthropicChatCompletionStreamResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return anthropic.ToAnthropicResponsesStreamError(err)
				},
			},
			PreCallback: checkAnthropicPassthrough,
		})
	}
	return routes
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	return append([]RouteConfig{
		createAnthropicCompleteRouteConfig(pathPrefix),
	}, createAnthropicMessagesRouteConfig(pathPrefix, handlerStore)...)
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

// checkAnthropicPassthrough pre-callback checks if the request is for a claude model.
// If it is, it attaches the raw request body for direct use by the provider.
// It also checks for anthropic oauth headers and sets the bifrost context.
func checkAnthropicPassthrough(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	var provider schemas.ModelProvider
	var model string

	switch r := req.(type) {
	case *anthropic.AnthropicTextRequest:
		provider, model = schemas.ParseModelString(r.Model, "")
		// Check if model parameter explicitly has `anthropic/` prefix
		if provider == schemas.Anthropic {
			r.Model = model
		}

	case *anthropic.AnthropicMessageRequest:
		provider, model = schemas.ParseModelString(r.Model, "")
		// Check if model parameter explicitly has `anthropic/` prefix
		if provider == schemas.Anthropic {
			r.Model = model
		}
	}

	if !strings.Contains(model, "claude") || (provider != schemas.Anthropic && provider != "") {
		// Not a Claude model or not an Anthropic model, so we can continue
		return nil
	}

	// Check if anthropic oauth headers are present
	if !isAnthropicAPIKeyAuth(ctx) {
		headers := extractHeadersFromRequest(ctx)
		url := extractExactPath(ctx)
		if !strings.HasPrefix(url, "/") {
			url = "/" + url
		}

		*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyExtraHeaders, headers)
		*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyURLPath, url)
		*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeySkipKeySelection, true)
		*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyUseRawRequestBody, true)
	}
	return nil
}

// extractAnthropicListModelsParams extracts query parameters for list models request
func extractAnthropicListModelsParams(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
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
		GenericRouter: NewGenericRouter(client, handlerStore, append(CreateAnthropicRouteConfigs("/anthropic", handlerStore), CreateAnthropicListModelsRouteConfigs("/anthropic", handlerStore)...), logger),
	}
}
