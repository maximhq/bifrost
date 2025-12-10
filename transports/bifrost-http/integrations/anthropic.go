package integrations

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
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
		RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
			if anthropicReq, ok := req.(*anthropic.AnthropicTextRequest); ok {
				return &schemas.BifrostRequest{
					TextCompletionRequest: anthropicReq.ToBifrostTextCompletionRequest(),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		TextResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
			if shouldUsePassthrough(ctx, resp.ExtraFields.Provider, resp.ExtraFields.ModelRequested, resp.ExtraFields.ModelDeployment) {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return anthropic.ToAnthropicTextCompletionResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
	}
}

// createAnthropicMessagesRouteConfig creates a route configuration for the `/v1/messages` endpoint.
func createAnthropicMessagesRouteConfig(pathPrefix string) []RouteConfig {
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
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					return &schemas.BifrostRequest{
						ResponsesRequest: anthropicReq.ToBifrostResponsesRequest(*ctx),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				if isClaudeModel(resp.ExtraFields.ModelRequested, resp.ExtraFields.ModelDeployment, string(resp.ExtraFields.Provider)) {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return anthropic.ToAnthropicResponsesResponse(resp), nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
					if shouldUsePassthrough(ctx, resp.ExtraFields.Provider, resp.ExtraFields.ModelRequested, resp.ExtraFields.ModelDeployment) {
						if resp.ExtraFields.RawResponse != nil {
							raw, ok := resp.ExtraFields.RawResponse.(string)
							if !ok {
								return "", nil, fmt.Errorf("expected RawResponse string, got %T", resp.ExtraFields.RawResponse)
							}
							var rawResponseJSON anthropic.AnthropicStreamEvent
							if err := sonic.Unmarshal([]byte(raw), &rawResponseJSON); err == nil {
								return string(rawResponseJSON.Type), raw, nil
							}
						}
						return "", nil, nil
					}
					anthropicResponse := anthropic.ToAnthropicResponsesStreamResponse(*ctx, resp)
					// Can happen for openai lifecycle events
					if len(anthropicResponse) == 0 {
						return "", nil, nil
					} else {
						if len(anthropicResponse) > 1 {
							combinedContent := ""
							for _, event := range anthropicResponse {
								responseJSON, err := sonic.Marshal(event)
								if err != nil {
									continue
								}
								combinedContent += fmt.Sprintf("event: %s\ndata: %s\n\n", event.Type, responseJSON)
							}
							return "", combinedContent, nil
						} else if len(anthropicResponse) == 1 {
							return string(anthropicResponse[0].Type), anthropicResponse[0], nil
						} else {
							return "", nil, nil
						}
					}
				},
				ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
					return anthropic.ToAnthropicResponsesStreamError(err)
				},
			},
			PreCallback: checkAnthropicPassthrough,
		})
	}
	return routes
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string) []RouteConfig {
	return append([]RouteConfig{
		createAnthropicCompleteRouteConfig(pathPrefix),
	}, createAnthropicMessagesRouteConfig(pathPrefix)...)
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
			RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
				if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
					return &schemas.BifrostRequest{
						ListModelsRequest: listModelsReq,
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ListModelsResponseConverter: func(ctx *context.Context, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
				return anthropic.ToAnthropicListModelsResponse(resp), nil
			},
			ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
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

	headers := extractHeadersFromRequest(ctx)
	if len(headers) > 0 {
		// Check for User-Agent header (case-insensitive)
		var userAgent []string
		for key, value := range headers {
			if strings.EqualFold(key, "user-agent") {
				userAgent = value
				break
			}
		}
		if len(userAgent) > 0 {
			// Check if it's claude code
			if strings.Contains(userAgent[0], "claude-cli") {
				*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyUserAgent, "claude-cli")
			}
		}
	}

	// Check if anthropic oauth headers are present
	if shouldUsePassthrough(bifrostCtx, provider, model, "") {
		*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyUseRawRequestBody, true)
		if !isAnthropicAPIKeyAuth(ctx) && (provider == schemas.Anthropic || provider == "") {
			url := extractExactPath(ctx)
			if !strings.HasPrefix(url, "/") {
				url = "/" + url
			}
			*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyExtraHeaders, headers)
			*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyURLPath, url)
			*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeySkipKeySelection, true)
		}
	}
	return nil
}

func shouldUsePassthrough(ctx *context.Context, provider schemas.ModelProvider, model string, deployment string) bool {
	isClaudeCode := false
	if userAgent, ok := (*ctx).Value(schemas.BifrostContextKeyUserAgent).(string); ok {
		if strings.Contains(userAgent, "claude-cli") {
			isClaudeCode = true
		}
	}
	return isClaudeCode && isClaudeModel(model, deployment, string(provider))
}

func isClaudeModel(model, deployment, provider string) bool {
	return (provider == string(schemas.Anthropic) ||
		(provider == "" && schemas.IsAnthropicModel(model))) ||
		(provider == string(schemas.Vertex) && (schemas.IsAnthropicModel(model) || schemas.IsAnthropicModel(deployment))) ||
		(provider == string(schemas.Azure) && (schemas.IsAnthropicModel(model) || schemas.IsAnthropicModel(deployment)))
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
		GenericRouter: NewGenericRouter(client, handlerStore, append(CreateAnthropicRouteConfigs("/anthropic"), CreateAnthropicListModelsRouteConfigs("/anthropic", handlerStore)...), logger),
	}
}
