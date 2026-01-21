package integrations

import (
	"errors"
	"fmt"
	"io"
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
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if anthropicReq, ok := req.(*anthropic.AnthropicTextRequest); ok {
				return &schemas.BifrostRequest{
					TextCompletionRequest: anthropicReq.ToBifrostTextCompletionRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		TextResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
			if shouldUsePassthrough(ctx, resp.ExtraFields.Provider, resp.ExtraFields.ModelRequested, resp.ExtraFields.ModelDeployment) {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return anthropic.ToAnthropicTextCompletionResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
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
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					return &schemas.BifrostRequest{
						ResponsesRequest: anthropicReq.ToBifrostResponsesRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				if isClaudeModel(resp.ExtraFields.ModelRequested, resp.ExtraFields.ModelDeployment, string(resp.ExtraFields.Provider)) {
					if resp.ExtraFields.RawResponse != nil {
						return resp.ExtraFields.RawResponse, nil
					}
				}
				return anthropic.ToAnthropicResponsesResponse(ctx, resp), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
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
					anthropicResponse := anthropic.ToAnthropicResponsesStreamResponse(ctx, resp)
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
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
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
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
					return &schemas.BifrostRequest{
						ListModelsRequest: listModelsReq,
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ListModelsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
				return anthropic.ToAnthropicListModelsResponse(resp), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			PreCallback: extractAnthropicListModelsParams,
		},
	}
}

// checkAnthropicPassthrough pre-callback checks if the request is for a claude model.
// If it is, it attaches the raw request body for direct use by the provider.
// It also checks for anthropic oauth headers and sets the bifrost context.
func checkAnthropicPassthrough(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
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
				bifrostCtx.SetValue(schemas.BifrostContextKeyUserAgent, "claude-cli")
			}
		}
	}

	// Check if anthropic oauth headers are present
	if shouldUsePassthrough(bifrostCtx, provider, model, "") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		if !isAnthropicAPIKeyAuth(ctx) && (provider == schemas.Anthropic || provider == "") {
			url := extractExactPath(ctx)
			if !strings.HasPrefix(url, "/") {
				url = "/" + url
			}
			bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, headers)
			bifrostCtx.SetValue(schemas.BifrostContextKeyURLPath, url)
			bifrostCtx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
		}
	}
	return nil
}

func shouldUsePassthrough(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, deployment string) bool {
	isClaudeCode := false
	if userAgent, ok := ctx.Value(schemas.BifrostContextKeyUserAgent).(string); ok {
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
func extractAnthropicListModelsParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
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

// CreateAnthropicCountTokensRouteConfigs creates route configurations for Anthropic count tokens endpoint.
func CreateAnthropicCountTokensRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	return []RouteConfig{
		{
			Type:   RouteConfigTypeAnthropic,
			Path:   pathPrefix + "/v1/messages/count_tokens",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &anthropic.AnthropicMessageRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					return &schemas.BifrostRequest{
						CountTokensRequest: anthropicReq.ToBifrostResponsesRequest(ctx),
					}, nil
				}
				return nil, errors.New("invalid request type for Anthropic count tokens")
			},
			CountTokensResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostCountTokensResponse) (interface{}, error) {
				return anthropic.ToAnthropicCountTokensResponse(resp), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
		},
	}
}

// CreateAnthropicBatchRouteConfigs creates route configurations for Anthropic Batch API endpoints.
func CreateAnthropicBatchRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig
	// Create batch endpoint - POST /v1/messages/batches
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/messages/batches",
		Method: "POST",
		GetRequestTypeInstance: func() any {
			return &anthropic.AnthropicBatchCreateRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req any) (*BatchRequest, error) {
			if anthropicReq, ok := req.(*anthropic.AnthropicBatchCreateRequest); ok {
				// Convert Anthropic batch request items to Bifrost format
				isNonAnthropicProvider := false
				var provider schemas.ModelProvider
				var ok bool
				if provider, ok = ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider); ok && provider != schemas.Anthropic {
					isNonAnthropicProvider = true
				}
				var model *string
				requests := make([]schemas.BatchRequestItem, len(anthropicReq.Requests))
				for i, r := range anthropicReq.Requests {
					if isNonAnthropicProvider {
						requestModel, ok := r.Params["model"].(string)
						if !ok {
							return nil, errors.New("model is required")
						}
						if model == nil {
							model = schemas.Ptr(requestModel)
						} else if *model != requestModel {
							return nil, errors.New("for non-Anthropic providers, model must be the same for all requests")
						}
					}
					requests[i] = schemas.BatchRequestItem{
						CustomID: r.CustomID,
						Params:   r.Params,
					}
				}
				br := &BatchRequest{
					Type: schemas.BatchCreateRequest,
					CreateRequest: &schemas.BifrostBatchCreateRequest{
						Model:    model,
						Provider: provider,
						Requests: requests,
					},
				}
				// If provider is openai, we need to generate endpoint too
				if provider == schemas.OpenAI {
					// Confirm if all requests have the same url
					var url string
					for _, request := range requests {
						if urlParam, ok := request.Params["url"].(string); ok {
							if url == "" {
								url = urlParam
							} else if url != urlParam {
								return nil, errors.New("for OpenAI batch API, all requests must have the same url")
							}
						}
					}
					br.CreateRequest.Endpoint = schemas.BatchEndpoint(url)
				}
				return br, nil
			}
			return nil, errors.New("invalid batch create request type")
		},
		BatchCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCreateResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini {
				resp.ID = strings.Replace(resp.ID, "batches/", "batches-", 1)
			}
			return anthropic.ToAnthropicBatchCreateResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicBatchCreateParams,
	})

	// List batches endpoint - GET /v1/messages/batches
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/messages/batches",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicBatchListRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if listReq, ok := req.(*anthropic.AnthropicBatchListRequest); ok {
				provider, ok := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if !ok {
					return nil, errors.New("provider not found in context")
				}
				return &BatchRequest{
					Type: schemas.BatchListRequest,
					ListRequest: &schemas.BifrostBatchListRequest{
						Provider:  provider,
						PageSize:  listReq.PageSize,
						PageToken: listReq.PageToken,
					},
				}, nil
			}
			return nil, errors.New("invalid batch list request type")
		},
		BatchListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchListResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.Anthropic {
				return resp.ExtraFields.RawResponse, nil
			}
			if resp.ExtraFields.Provider == schemas.Gemini {
				for i, batch := range resp.Data {
					resp.Data[i].ID = strings.Replace(batch.ID, "batches/", "batches-", 1)
				}
			}
			return anthropic.ToAnthropicBatchListResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicBatchListQueryParams,
	})

	// Retrieve batch endpoint - GET /v1/messages/batches/{batch_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/messages/batches/{batch_id}",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicBatchRetrieveRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if retrieveReq, ok := req.(*anthropic.AnthropicBatchRetrieveRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if provider == schemas.Gemini {
					retrieveReq.BatchID = strings.Replace(retrieveReq.BatchID, "batches-", "batches/", 1)
				}
				return &BatchRequest{
					Type: schemas.BatchRetrieveRequest,
					RetrieveRequest: &schemas.BifrostBatchRetrieveRequest{
						BatchID:  retrieveReq.BatchID,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid batch retrieve request type")
		},
		BatchRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Gemini {
				resp.ID = strings.Replace(resp.ID, "batches/", "batches-", 1)
			}
			return anthropic.ToAnthropicBatchRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicBatchIDFromPath,
	})

	// Cancel batch endpoint - POST /v1/messages/batches/{batch_id}/cancel
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/messages/batches/{batch_id}/cancel",
		Method: "POST",
		GetRequestTypeInstance: func() any {
			return &anthropic.AnthropicBatchCancelRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if cancelReq, ok := req.(*anthropic.AnthropicBatchCancelRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if provider == schemas.Gemini {
					cancelReq.BatchID = strings.Replace(cancelReq.BatchID, "batches-", "batches/", 1)
				}
				return &BatchRequest{
					Type: schemas.BatchCancelRequest,
					CancelRequest: &schemas.BifrostBatchCancelRequest{
						BatchID:  cancelReq.BatchID,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid batch cancel request type")
		},
		BatchCancelResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCancelResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return anthropic.ToAnthropicBatchCancelResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicBatchIDFromPath,
	})

	// Get batch results endpoint - GET /v1/messages/batches/{batch_id}/results
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/messages/batches/{batch_id}/results",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicBatchResultsRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if resultsReq, ok := req.(*anthropic.AnthropicBatchResultsRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if provider == schemas.Gemini {
					resultsReq.BatchID = strings.Replace(resultsReq.BatchID, "batches-", "batches/", 1)
				}
				return &BatchRequest{
					Type: schemas.BatchResultsRequest,
					ResultsRequest: &schemas.BifrostBatchResultsRequest{
						BatchID:  resultsReq.BatchID,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid batch results request type")
		},
		BatchResultsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchResultsResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicBatchIDFromPath,
	})

	return routes
}

// extractAnthropicBatchCreateParams extracts provider from header for batch create requests
func extractAnthropicBatchCreateParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	// Extract provider from header, default to Anthropic
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Anthropic)
	}
	// Store provider in context for batch create converter to use
	bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
	return nil
}

// extractAnthropicBatchListQueryParams extracts provider from header and query parameters for Anthropic batch list requests
func extractAnthropicBatchListQueryParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	if listReq, ok := req.(*anthropic.AnthropicBatchListRequest); ok {
		// Extract provider from header, default to Anthropic
		provider := string(ctx.Request.Header.Peek("x-model-provider"))
		if provider == "" {
			provider = string(schemas.Anthropic)
		}
		bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
		// Printing all query parameters
		// Extract limit from query parameters
		if limitStr := string(ctx.QueryArgs().Peek("page_size")); limitStr != "" {
			if limit, err := strconv.Atoi(limitStr); err == nil {
				listReq.PageSize = limit
			} else {
				listReq.PageSize = 30
			}
		}
		// Extract before_id cursor
		if pageToken := string(ctx.QueryArgs().Peek("page_token")); pageToken != "" {
			listReq.PageToken = &pageToken
		}
	}
	return nil
}

// extractAnthropicBatchIDFromPath extracts provider from header and batch_id from path parameters
func extractAnthropicBatchIDFromPath(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	// Extract provider from header, default to Anthropic
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Anthropic)
	}
	bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
	batchID := ctx.UserValue("batch_id")
	if batchID == nil {
		return errors.New("batch_id is required")
	}
	batchIDStr, ok := batchID.(string)
	if !ok || batchIDStr == "" {
		return errors.New("batch_id must be a non-empty string")
	}
	switch r := req.(type) {
	case *anthropic.AnthropicBatchRetrieveRequest:
		r.BatchID = batchIDStr
	case *anthropic.AnthropicBatchCancelRequest:
		r.BatchID = batchIDStr
	case *anthropic.AnthropicBatchResultsRequest:
		r.BatchID = batchIDStr
	}
	return nil
}

// extractAnthropicFileUploadParams extracts provider from header for file upload requests
func extractAnthropicFileUploadParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Anthropic)
	}
	bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
	return nil
}

// extractAnthropicFileListQueryParams extracts provider from header and query parameters for Anthropic file list requests
func extractAnthropicFileListQueryParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	if listReq, ok := req.(*anthropic.AnthropicFileListRequest); ok {
		// Extract provider from header, default to Anthropic
		provider := string(ctx.Request.Header.Peek("x-model-provider"))
		if provider == "" {
			provider = string(schemas.Anthropic)
		}

		bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))

		// Extract limit from query parameters
		if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
			if limit, err := strconv.Atoi(limitStr); err == nil {
				listReq.Limit = limit
			} else {
				// We are keeping default as 30
				listReq.Limit = 30
			}
		}

		// Extract after_id cursor
		if afterID := string(ctx.QueryArgs().Peek("after_id")); afterID != "" {
			listReq.After = &afterID
		}
	}

	return nil
}

// extractAnthropicFileIDFromPath extracts provider from header and file_id from path parameters
func extractAnthropicFileIDFromPath(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	// Extract provider from header, default to Anthropic
	provider := string(ctx.Request.Header.Peek("x-model-provider"))
	if provider == "" {
		provider = string(schemas.Anthropic)
	}
	bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
	fileID := ctx.UserValue("file_id")
	if fileID == nil {
		return errors.New("file_id is required")
	}

	fileIDStr, ok := fileID.(string)
	if !ok || fileIDStr == "" {
		return errors.New("file_id must be a non-empty string")
	}

	switch r := req.(type) {
	case *anthropic.AnthropicFileRetrieveRequest:
		r.FileID = fileIDStr

	case *anthropic.AnthropicFileDeleteRequest:
		r.FileID = fileIDStr

	case *anthropic.AnthropicFileContentRequest:
		r.FileID = fileIDStr

	}
	return nil
}

// CreateAnthropicFilesRouteConfigs creates route configurations for Anthropic Files API endpoints.
func CreateAnthropicFilesRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Upload file endpoint - POST /v1/files
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/files",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicFileUploadRequest{}
		},
		RequestParser: func(ctx *fasthttp.RequestCtx, req interface{}) error {
			uploadReq, ok := req.(*anthropic.AnthropicFileUploadRequest)
			if !ok {
				return errors.New("invalid request type for file upload")
			}
			providerHeader := string(ctx.Request.Header.Peek("x-model-provider"))
			if providerHeader == "" {
				providerHeader = string(schemas.Anthropic)
			}
			provider := schemas.ModelProvider(providerHeader)
			// Parse multipart form
			form, err := ctx.MultipartForm()
			if err != nil {
				return err
			}
			// Extract purpose (required)
			purposeValues := form.Value["purpose"]
			if len(purposeValues) > 0 && purposeValues[0] != "" {
				uploadReq.Purpose = purposeValues[0]
			} else if provider == schemas.OpenAI && uploadReq.Purpose == "" {
				uploadReq.Purpose = "batch"
			}
			// Extract file (required)
			fileHeaders := form.File["file"]
			if len(fileHeaders) == 0 {
				return errors.New("file field is required")
			}
			// Read file content
			fileHeader := fileHeaders[0]
			file, err := fileHeader.Open()
			if err != nil {
				return err
			}
			defer file.Close()
			// Read file data
			fileData, err := io.ReadAll(file)
			if err != nil {
				return err
			}
			uploadReq.File = fileData
			uploadReq.Filename = fileHeader.Filename
			return nil
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req any) (*FileRequest, error) {
			if uploadReq, ok := req.(*anthropic.AnthropicFileUploadRequest); ok {
				// Here if provider is OpenAI and purpose is empty then we override it with "batch"
				provider, ok := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if !ok {
					return nil, errors.New("provider not found in context")
				}
				if provider == schemas.OpenAI && uploadReq.Purpose == "" {
					uploadReq.Purpose = "batch"
				}
				return &FileRequest{
					Type: schemas.FileUploadRequest,
					UploadRequest: &schemas.BifrostFileUploadRequest{
						File:     uploadReq.File,
						Filename: uploadReq.Filename,
						Purpose:  schemas.FilePurpose(uploadReq.Purpose),
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid file upload request type")
		},
		FileUploadResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			if resp.ExtraFields.Provider == schemas.Gemini {
				// Here we will convert fileId to replace files/ with files-
				resp.ID = strings.Replace(resp.ID, "files/", "files-", 1)
			}
			return anthropic.ToAnthropicFileUploadResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicFileUploadParams,
	})

	// List files endpoint - GET /v1/files
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/files",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicFileListRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if listReq, ok := req.(*anthropic.AnthropicFileListRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				return &FileRequest{
					Type: schemas.FileListRequest,
					ListRequest: &schemas.BifrostFileListRequest{
						Limit:    listReq.Limit,
						After:    listReq.After,
						Order:    listReq.Order,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid file list request type")
		},
		FileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			if resp.ExtraFields.Provider == schemas.Gemini {
				// Here we will convert fileId to replace files/ with files-
				for i, file := range resp.Data {
					resp.Data[i].ID = strings.Replace(file.ID, "files/", "files-", 1)
				}
			}
			return anthropic.ToAnthropicFileListResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicFileListQueryParams,
	})

	// Retrieve file endpoint - GET /v1/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/files/{file_id}/content",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicFileRetrieveRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if retrieveReq, ok := req.(*anthropic.AnthropicFileRetrieveRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				// Handle file id conversion for Gemini
				if provider == schemas.Gemini {
					retrieveReq.FileID = strings.Replace(retrieveReq.FileID, "files-", "files/", 1)
				}
				return &FileRequest{
					Type: schemas.FileRetrieveRequest,
					RetrieveRequest: &schemas.BifrostFileRetrieveRequest{
						FileID:   retrieveReq.FileID,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid file retrieve request type")
		},
		FileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return anthropic.ToAnthropicFileRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicFileIDFromPath,
	})

	// Delete file endpoint - DELETE /v1/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/files/{file_id}",
		Method: "DELETE",
		GetRequestTypeInstance: func() interface{} {
			return &anthropic.AnthropicFileDeleteRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if deleteReq, ok := req.(*anthropic.AnthropicFileDeleteRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				if provider == schemas.Gemini {
					// Here we will convert fileId to replace files/ with files-
					deleteReq.FileID = strings.Replace(deleteReq.FileID, "files-", "files/", 1)
				}
				return &FileRequest{
					Type: schemas.FileDeleteRequest,
					DeleteRequest: &schemas.BifrostFileDeleteRequest{
						FileID:   deleteReq.FileID,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid file delete request type")
		},
		FileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
			if resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return anthropic.ToAnthropicFileDeleteResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: extractAnthropicFileIDFromPath,
	})
	return routes
}

// NewAnthropicRouter creates a new AnthropicRouter with the given bifrost client.
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *AnthropicRouter {
	routes := CreateAnthropicRouteConfigs("/anthropic")
	routes = append(routes, CreateAnthropicListModelsRouteConfigs("/anthropic", handlerStore)...)
	routes = append(routes, CreateAnthropicCountTokensRouteConfigs("/anthropic", handlerStore)...)
	routes = append(routes, CreateAnthropicBatchRouteConfigs("/anthropic", handlerStore)...)
	routes = append(routes, CreateAnthropicFilesRouteConfigs("/anthropic", handlerStore)...)

	return &AnthropicRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}
