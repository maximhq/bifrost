package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"

	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/tidwall/sjson"
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.TextCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
			if shouldUsePassthrough(ctx, resp.ExtraFields.Provider, resp.ExtraFields.OriginalModelRequested, resp.ExtraFields.ResolvedModelUsed) {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return anthropic.ToAnthropicTextCompletionResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
		PreCallback: checkAnthropicPassthrough,
	}
}

// anthropicRefuseThreadContinue is the ShortCircuit for the `/v1/messages` routes
// implementing Bifrost's stateless handling of Anthropic server-side threads.
// Thread state is bound to the upstream account that created it, and Bifrost's
// per-request key selection, retries, and fallbacks cannot keep a continuation
// on that account. A `thread: {"type": "continue"}` request carries only the
// conversation delta, which Bifrost cannot serve, so it is refused with the
// `thread_unsupported_request` error code; the client then resends the turn in
// full and drops the thread field for the rest of the session. Create requests
// pass through here and have the field stripped on the provider's raw-body path
// (BuildAnthropicResponsesRequestBody), since they carry the full conversation.
func anthropicRefuseThreadContinue(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) (bool, error) {
	anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest)
	if !ok {
		return false, nil
	}
	// The parser captures unknown top-level fields as compacted RawMessage. In
	// large-payload mode the body is never parsed, so the enterprise metadata
	// extractor surfaces the thread type through the routing metadata instead
	// (already resolved and cached by the hydration in checkAnthropicPassthrough).
	// An extractor that does not populate ThreadType leaves behavior unchanged.
	threadType := ""
	if threadRaw, ok := anthropicReq.ExtraParams["thread"].(json.RawMessage); ok {
		threadType = gjson.GetBytes(threadRaw, "type").String()
	} else if isLargePayload, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); isLargePayload {
		if metadata := resolveLargePayloadMetadata(bifrostCtx); metadata != nil {
			threadType = metadata.ThreadType
		}
	}
	if threadType != "continue" {
		return false, nil
	}
	// The count_tokens endpoint has its own static route without this hook; this
	// guard keeps the wildcard /v1/messages/{path:*} route from refusing token
	// counting if route registration ever changes. Token counting keeps working
	// on full-replay bodies either way.
	if strings.HasSuffix(string(ctx.Path()), "/count_tokens") {
		return false, nil
	}
	payload, err := providerUtils.MarshalSorted(&anthropic.AnthropicMessageError{
		Type: "error",
		Error: anthropic.AnthropicMessageErrorStruct{
			Type:    "invalid_request_error",
			Message: `Bifrost does not keep Anthropic thread state; replay the full conversation with thread: {"type": "create"}.`,
			Details: &anthropic.AnthropicMessageErrorDetails{ErrorCode: "thread_unsupported_request"},
		},
	})
	if err != nil {
		return true, err
	}
	ctx.SetStatusCode(fasthttp.StatusBadRequest)
	ctx.SetContentType("application/json")
	ctx.SetBody(payload)
	return true, nil
}

// validateAnthropicMessageRequest enforces the parts of the Messages API contract
// that the Responses conversion would otherwise paper over: a non-positive
// max_tokens is dropped and replaced by the model's full output budget, an empty
// messages array surfaces as an untyped server error, and unknown roles are
// coerced downstream. max_tokens is only checked when the client sent it, since
// omitting it is accepted and defaulted on egress. rawBody is used solely to
// detect that presence (the parsed field is a plain int, so absent and 0 collide).
func validateAnthropicMessageRequest(req *anthropic.AnthropicMessageRequest, rawBody []byte) *schemas.BifrostError {
	if maxTokens := gjson.GetBytes(rawBody, "max_tokens"); maxTokens.Exists() && maxTokens.Type != gjson.Null && req.MaxTokens < 1 {
		return newAnthropicInvalidRequestError("max_tokens: Input should be greater than or equal to 1")
	}
	if len(req.Messages) == 0 {
		return newAnthropicInvalidRequestError("messages: at least one message is required")
	}
	for i, msg := range req.Messages {
		switch msg.Role {
		case anthropic.AnthropicMessageRoleUser, anthropic.AnthropicMessageRoleAssistant, anthropic.AnthropicMessageRoleSystem:
			// role "system" is a mid-conversation system message, handled on egress.
		default:
			return newAnthropicInvalidRequestError(fmt.Sprintf("messages.%d.role: Input should be 'user' or 'assistant'", i))
		}
	}
	return nil
}

func newAnthropicInvalidRequestError(message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(fasthttp.StatusBadRequest),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr("invalid_request_error"),
			Message: message,
		},
	}
}

// anthropicMessagesShortCircuit rejects malformed /v1/messages requests with a
// 400 invalid_request_error before they reach the Bifrost flow, then applies the
// stateless thread handling. Large-payload requests are never parsed, so their
// (empty) parsed struct is not validated; count_tokens has no max_tokens and is
// guarded the same way as in anthropicRefuseThreadContinue.
func anthropicMessagesShortCircuit(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) (bool, error) {
	anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest)
	isLargePayload, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if ok && !isLargePayload && !strings.HasSuffix(string(ctx.Path()), "/count_tokens") {
		if bifrostErr := validateAnthropicMessageRequest(anthropicReq, ctx.Request.Body()); bifrostErr != nil {
			payload, err := sonic.Marshal(anthropic.ToAnthropicChatCompletionError(bifrostErr))
			if err != nil {
				return true, err
			}
			ctx.SetStatusCode(*bifrostErr.StatusCode)
			ctx.SetContentType("application/json")
			ctx.SetBody(payload)
			return true, nil
		}
	}
	return anthropicRefuseThreadContinue(ctx, bifrostCtx, req)
}

// createAnthropicMessagesRouteConfig creates a route configuration for the `/v1/messages` endpoint.
func createAnthropicMessagesRouteConfig(pathPrefix string, logger schemas.Logger) []RouteConfig {
	var routes []RouteConfig
	for _, path := range []string{
		"/v1/messages",
		"/v1/messages/{path:*}",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeAnthropic,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ResponsesRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &anthropic.AnthropicMessageRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					bifrostReq := anthropicReq.ToBifrostResponsesRequest(ctx)
					normalizeBifrostInputContentBlocks(bifrostReq)
					// Input is still owned here. Strip once before hooks/fallbacks
					// share it, avoiding a conversation-slice copy per GPT attempt.
					bifrostReq.ExtractAnthropicBillingHeader()
					return &schemas.BifrostRequest{
						ResponsesRequest: bifrostReq,
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				soToolName, _ := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string)
				if soToolName == "" && isClaudeModel(ctx, resp.ExtraFields.OriginalModelRequested, resp.ExtraFields.ResolvedModelUsed, string(resp.ExtraFields.Provider)) {
					if resp.ExtraFields.RawResponse != nil {
						passthrough, _ := ctx.Value(schemas.BifrostContextKeyPassthroughOverridesPresent).(bool)
						raw, isRawJSON := resp.ExtraFields.RawResponse.(json.RawMessage)
						if passthrough || !isRawJSON {
							return resp.ExtraFields.RawResponse, nil
						}
						// Raw capture was requested: echo extra_fields as the converted path does, leaving Anthropic's bytes intact.
						extraFields, err := sonic.Marshal(resp.ExtraFields)
						if err != nil {
							return nil, fmt.Errorf("marshal anthropic extra_fields: %w", err)
						}
						withExtraFields, err := sjson.SetRawBytes(raw, "extra_fields", extraFields)
						if err != nil {
							return nil, fmt.Errorf("attach anthropic extra_fields: %w", err)
						}
						return json.RawMessage(withExtraFields), nil
					}
				}
				return anthropic.ToAnthropicResponsesResponse(ctx, resp), nil
			},
			AsyncResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.AsyncJobResponse, responsesResponseConverter ResponsesResponseConverter) (interface{}, map[string]string, error) {
				if resp.Status == schemas.AsyncJobStatusCompleted {
					responsesResp, ok := resp.Result.(*schemas.BifrostResponsesResponse)
					if !ok {
						return nil, nil, errors.New("invalid responses response type")
					}
					response, err := responsesResponseConverter(ctx, responsesResp)
					if err != nil {
						return nil, nil, err
					}
					return response, nil, nil
				}
				return &anthropic.AnthropicMessageResponse{
					ID: resp.ID,
				}, nil, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return anthropic.ToAnthropicChatCompletionError(err)
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
					soToolName, _ := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string)
					if soToolName == "" && shouldUsePassthrough(ctx, resp.ExtraFields.Provider, resp.ExtraFields.OriginalModelRequested, resp.ExtraFields.ResolvedModelUsed) {
						anthropic.SetResponsesStreamPassthrough(ctx)
						// Skip passthrough for ContentPartAdded: it's a synthetic bifrost event whose
						// RawResponse carries the parent content_block_start already emitted by OutputItemAdded.
						// Passing through here would produce a duplicate content_block_start that causes
						// the Anthropic SDK to error and drop all subsequent content_block_delta events.
						//
						// Also skip passthrough for frames the converter must renumber (see
						// mustConvertInPassthrough): server tools (advisor, web_search, web_fetch,
						// code_execution) expand one Responses item into several Anthropic content
						// blocks, so forwarding their raw frames verbatim yields content_block indices
						// the client never opened ("Content block not found" on strict clients).
						if resp.ExtraFields.RawResponse != nil &&
							resp.Type != schemas.ResponsesStreamResponseTypeContentPartAdded &&
							!mustConvertInPassthrough(resp) {
							raw, ok := resp.ExtraFields.RawResponse.(string)
							if !ok {
								return "", nil, fmt.Errorf("expected RawResponse string, got %T", resp.ExtraFields.RawResponse)
							}
							if t := gjson.Get(raw, "type"); t.Exists() {
								return t.String(), raw, nil
							}
						}
						// Fallback: if RawResponse is not available, use bifrost-to-anthropic conversion
						// instead of silently dropping all events
					}
					anthropicResponse := anthropic.ToAnthropicResponsesStreamResponse(ctx, resp)
					// Can happen for openai lifecycle events
					if len(anthropicResponse) == 0 {
						return "", nil, nil
					}
					if len(anthropicResponse) > 1 {
						var combinedContent strings.Builder
						for _, event := range anthropicResponse {
							responseJSON, err := sonic.Marshal(event)
							if err != nil {
								logger.Error("failed to marshal anthropic streaming message: %v", err)
								continue
							}
							fmt.Fprintf(&combinedContent, "event: %s\ndata: %s\n\n", event.Type, responseJSON)
						}
						return "", combinedContent.String(), nil
					}
					return string(anthropicResponse[0].Type), anthropicResponse[0], nil
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return anthropic.ToAnthropicResponsesStreamError(err)
				},
			},
			PreCallback:  checkAnthropicPassthrough,
			ShortCircuit: anthropicMessagesShortCircuit,
		})
	}
	return routes
}

// CreateAnthropicRouteConfigs creates route configurations for Anthropic endpoints.
func CreateAnthropicRouteConfigs(pathPrefix string, logger schemas.Logger) []RouteConfig {
	return append([]RouteConfig{
		createAnthropicCompleteRouteConfig(pathPrefix),
	}, createAnthropicMessagesRouteConfig(pathPrefix, logger)...)
}

// passthroughSafeHeaders is a whitelist of headers that are safe to pass through
var passthroughSafeHeaders = map[string]bool{
	"anthropic-beta": true,
	"anthropic-dangerous-direct-browser-access": true,
	"anthropic-version":                         true,
}

func hasPromptCachingScopeBetaHeader(headers map[string][]string) bool {
	for k, v := range headers {
		if strings.ToLower(k) == anthropic.AnthropicBetaHeader {
			for _, headerValue := range v {
				if strings.Contains(headerValue, anthropic.AnthropicPromptCachingScopeBetaHeader) {
					return true
				}
			}
		}
	}
	return false
}

func hasFastModeBetaHeader(headers map[string][]string) bool {
	for k, v := range headers {
		if strings.ToLower(k) != anthropic.AnthropicBetaHeader {
			continue
		}
		for _, headerValue := range v {
			for beta := range strings.SplitSeq(headerValue, ",") {
				if strings.HasPrefix(strings.TrimSpace(beta), anthropic.AnthropicFastModeBetaHeaderPrefix) {
					return true
				}
			}
		}
	}
	return false
}

// hasOutputConfigFormat reports whether the parsed request contains output_config.format
func hasOutputConfigFormat(req any) bool {
	r, ok := req.(*anthropic.AnthropicMessageRequest)
	if !ok {
		return false
	}
	if r.OutputConfig != nil && len(r.OutputConfig.Format) > 0 {
		return true
	}
	return len(r.OutputFormat) > 0
}

// extractPassthroughHeaders filters headers to only include those in the safe whitelist.
// Header matching is case-insensitive. Provider-aware beta-header filtering happens
// downstream at each provider's wire layer (e.g. anthropic.go, vertex.go), where
// networkConfig.BetaHeaderOverrides is in scope.
func extractPassthroughHeaders(allHeaders map[string][]string) map[string][]string {
	filtered := make(map[string][]string)
	for k, v := range allHeaders {
		if passthroughSafeHeaders[strings.ToLower(k)] {
			filtered[strings.ToLower(k)] = v
		}
	}

	return filtered
}

func CreateAnthropicListModelsRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	return []RouteConfig{
		{
			Type:   RouteConfigTypeAnthropic,
			Path:   pathPrefix + "/v1/models",
			Method: "GET",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ListModelsRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
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

func hydrateAnthropicRequestFromLargePayloadMetadata(bifrostCtx *schemas.BifrostContext, req interface{}) {
	if bifrostCtx == nil {
		return
	}
	isLargePayload, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if !isLargePayload {
		return
	}
	metadata := resolveLargePayloadMetadata(bifrostCtx)
	if metadata == nil {
		return
	}

	switch r := req.(type) {
	case *anthropic.AnthropicTextRequest:
		if r.Model == "" {
			r.Model = metadata.Model
		}
		if metadata.StreamRequested != nil && r.Stream == nil {
			r.Stream = schemas.Ptr(*metadata.StreamRequested)
		}
	case *anthropic.AnthropicMessageRequest:
		if r.Model == "" {
			r.Model = metadata.Model
		}
		if metadata.StreamRequested != nil && r.Stream == nil {
			r.Stream = schemas.Ptr(*metadata.StreamRequested)
		}
	}
}

// checkAnthropicPassthrough configures provider-native forwarding for Claude Code requests.
// Alongside the required auth, path, and raw-response settings, it registers an
// Anthropic-owned text rewriter so raw request bytes cannot bypass runtime redaction.
func checkAnthropicPassthrough(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req any) error {
	hydrateAnthropicRequestFromLargePayloadMetadata(bifrostCtx, req)

	var provider schemas.ModelProvider
	var model string
	isMessagesRequest := false

	switch r := req.(type) {
	case *anthropic.AnthropicTextRequest:
		provider, model = schemas.ParseModelString(r.Model, "")

	case *anthropic.AnthropicMessageRequest:
		provider, model = schemas.ParseModelString(r.Model, "")
		isMessagesRequest = true
	}

	headers := extractHeadersFromRequest(ctx)
	schemas.ExtractAndSetUserAgentFromHeaders(headers, bifrostCtx)

	// Check if anthropic oauth headers are present
	if shouldUsePassthrough(bifrostCtx, provider, model, "") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughOverridesPresent, true)
		bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
		if !isAnthropicAPIKeyAuth(ctx) && (provider == schemas.Anthropic || provider == "") {
			url := extractExactPath(ctx)
			if !strings.HasPrefix(url, "/") {
				url = "/" + url
			}
			bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, headers)
			bifrostCtx.SetValue(schemas.BifrostContextKeyURLPath, url)
			// This key is also used in IsClaudeCodeMaxMode
			// So if you are changing the behaviour of this key, make sure to change IsClaudeCodeMaxMode as well
			bifrostCtx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
		} else {
			// API key flow: pass only whitelisted safe headers (like anthropic-beta for feature detection)
			passthroughHeaders := extractPassthroughHeaders(headers)
			if len(passthroughHeaders) > 0 {
				bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, passthroughHeaders)
			}
		}
		// These providers convert output_config.format through Bifrost, including
		// their client-facing response events, so raw request and response text
		// rewriters do not apply.
		if anthropic.ProviderRequiresSyntheticStructuredOutput(provider) && hasOutputConfigFormat(req) {
			bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, false)
			return nil
		}
		if isMessagesRequest {
			// Native Messages response forwarding is independent of raw request
			// serialization. Vertex beta modes serialize the normalized request but
			// still return Anthropic SSE that Guardrails must synchronize.
			bifrostCtx.SetValue(
				schemas.BifrostContextKeyRawStreamTextCodec,
				schemas.RawStreamTextCodec(anthropicRawStreamTextCodec{}),
			)
			bifrostCtx.SetValue(
				schemas.BifrostContextKeyRawResponseTextTransformer,
				schemas.RawResponseTextTransformer(rewriteAnthropicRawResponseTransforms),
			)
		}
		if provider == schemas.Vertex && (hasPromptCachingScopeBetaHeader(headers) || hasFastModeBetaHeader(headers)) {
			bifrostCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, false)
			return nil
		}
		// Raw passthrough preserves native-only fields but skips normalized request
		// serialization, so Guardrails needs an integration-owned path rewriter.
		bifrostCtx.SetValue(
			schemas.BifrostContextKeyRawRequestBodyTextRewriter,
			schemas.RawRequestBodyTextRewriter(rewriteAnthropicRawRequestBody),
		)
		if isMessagesRequest {
			bifrostCtx.SetValue(
				schemas.BifrostContextKeyRawRequestBodyTextTransformer,
				schemas.RawRequestBodyTextTransformer(rewriteAnthropicRawRequestBodyTransforms),
			)
		}
	}
	return nil
}

// anthropicRawStreamTextCodec owns Anthropic's native text-delta JSON shape.
type anthropicRawStreamTextCodec struct{}

// Inspect exposes assistant text and tool-argument deltas while keeping reasoning and lifecycle events opaque.
func (anthropicRawStreamTextCodec) Inspect(rawResponse string) (schemas.RawStreamTextEvent, bool, error) {
	if !gjson.Valid(rawResponse) {
		return schemas.RawStreamTextEvent{}, false, fmt.Errorf("anthropic raw stream event is not valid JSON")
	}
	deltaType := gjson.Get(rawResponse, "delta.type").String()
	if gjson.Get(rawResponse, "type").String() != "content_block_delta" ||
		(deltaType != "text_delta" && deltaType != "input_json_delta") {
		return schemas.RawStreamTextEvent{}, false, nil
	}
	index := gjson.Get(rawResponse, "index")
	if !index.Exists() || index.Type != gjson.Number || index.Int() < 0 {
		return schemas.RawStreamTextEvent{}, false, fmt.Errorf("anthropic text_delta event has invalid index")
	}
	path := "delta.text"
	if deltaType == "input_json_delta" {
		path = "delta.partial_json"
	}
	text := gjson.Get(rawResponse, path)
	if !text.Exists() || text.Type != gjson.String {
		return schemas.RawStreamTextEvent{}, false, fmt.Errorf("anthropic text_delta event has invalid delta.text")
	}
	return schemas.RawStreamTextEvent{
		TargetID: strconv.FormatInt(index.Int(), 10),
		Text:     text.String(),
	}, true, nil
}

// Rewrite changes only the inspected text or argument fragment, preserving native event identity.
func (codec anthropicRawStreamTextCodec) Rewrite(rawResponse string, text string) (string, error) {
	if _, eligible, err := codec.Inspect(rawResponse); err != nil {
		return "", err
	} else if !eligible {
		return "", fmt.Errorf("anthropic raw stream event is not an eligible text_delta")
	}
	path := "delta.text"
	if gjson.Get(rawResponse, "delta.type").String() == "input_json_delta" {
		path = "delta.partial_json"
	}
	rewritten, err := sjson.Set(rawResponse, path, text)
	if err != nil {
		return "", fmt.Errorf("rewrite anthropic text_delta: %w", err)
	}
	return rewritten, nil
}

// rewriteAnthropicRawRequestBody applies runtime replacements only to Anthropic fields mirrored as mutable normalized text.
// Keeping the allowlist here preserves native metadata and other fields Guardrails did not inspect.
func rewriteAnthropicRawRequestBody(rawBody []byte, replacements map[string]string) ([]byte, error) {
	return rewriteRawRequestTextFields(rawBody, replacements, collectAnthropicRawRequestTextPaths)
}

// rewriteAnthropicRawRequestBodyTransforms applies provider-managed transformations to exact mutable Anthropic fields.
func rewriteAnthropicRawRequestBodyTransforms(rawBody []byte, rewrites []schemas.TextRewrite) ([]byte, error) {
	return rewriteRawJSONTextTargets(rawBody, rewrites, collectAnthropicRawRequestTextTargets)
}

// rewriteAnthropicRawResponseTransforms applies provider-managed transformations to an Anthropic native response.
func rewriteAnthropicRawResponseTransforms(rawResponse any, rewrites []schemas.TextRewrite) (any, error) {
	var rawJSON []byte
	wrap := func(rewritten []byte) any { return rewritten }
	switch value := rawResponse.(type) {
	case json.RawMessage:
		rawJSON = append([]byte(nil), value...)
		wrap = func(rewritten []byte) any { return json.RawMessage(rewritten) }
	case []byte:
		rawJSON = append([]byte(nil), value...)
	case string:
		rawJSON = []byte(value)
		wrap = func(rewritten []byte) any { return string(rewritten) }
	default:
		return nil, fmt.Errorf("Anthropic raw response has unsupported type %T", rawResponse)
	}
	rewritten, err := rewriteRawJSONTextTargets(rawJSON, rewrites, collectAnthropicRawResponseTextTargets)
	if err != nil {
		return nil, err
	}
	return wrap(rewritten), nil
}

// anthropicRawContentScope controls which native content-block text fields are guardrail-visible.
type anthropicRawContentScope uint8

const (
	anthropicRawContentScopeSystem anthropicRawContentScope = iota
	anthropicRawContentScopeMessage
	anthropicRawContentScopeToolResult
	anthropicRawContentScopeUserMessage
	anthropicRawContentScopeFirstSystemMessage
	anthropicRawContentScopeAssistantMessage
)

// collectAnthropicRawRequestTextPaths enumerates Anthropic fields mirrored into mutable Bifrost request text.
func collectAnthropicRawRequestTextPaths(root gjson.Result, replacements map[string]string) ([]string, error) {
	paths := make([]string, 0)
	if err := appendRawRequestStringPath(&paths, root.Get("prompt"), "prompt"); err != nil {
		return nil, err
	}
	if err := collectAnthropicRawContentPaths(&paths, root.Get("system"), "system", anthropicRawContentScopeSystem); err != nil {
		return nil, err
	}

	messages := root.Get("messages")
	if !messages.Exists() || messages.Type == gjson.Null {
		return paths, nil
	}
	if !messages.IsArray() {
		return nil, fmt.Errorf("raw Anthropic request messages must be an array")
	}
	var collectErr error
	messages.ForEach(func(index, message gjson.Result) bool {
		messagePath := rawRequestArrayPath("messages", int(index.Int()))
		collectErr = collectAnthropicRawContentPaths(
			&paths,
			message.Get("content"),
			rawRequestObjectPath(messagePath, "content"),
			anthropicRawContentScopeMessage,
		)
		return collectErr == nil
	})
	return paths, collectErr
}

// anthropicRawTransformTargets tracks normalized ordinals separately from writable
// native fields: ingress-generated user placeholders consume an ordinal but have
// no raw text destination. sawNormalizedItem mirrors billing extraction's first-item scope.
type anthropicRawTransformTargets struct {
	targets           []rawJSONTextTarget
	nextIndex         int
	sawNormalizedItem bool
}

// collectAnthropicRawRequestTextTargets maps native text for provider-managed guardrail redaction.
// It mirrors ingress normalization without changing billing metadata or the request.
// Generated placeholders reserve ordinals without writable destinations; reasoning
// and tool arguments retain their separate guardrail handling.
func collectAnthropicRawRequestTextTargets(root gjson.Result) ([]rawJSONTextTarget, error) {
	state := &anthropicRawTransformTargets{}
	if err := collectAnthropicRawTransformContentTargets(state, root.Get("system"), "system", anthropicRawContentScopeSystem); err != nil {
		return nil, err
	}
	messages := root.Get("messages")
	if !messages.Exists() || messages.Type == gjson.Null {
		return state.targets, nil
	}
	if !messages.IsArray() {
		return nil, fmt.Errorf("raw Anthropic request messages must be an array")
	}
	var collectErr error
	messages.ForEach(func(index, message gjson.Result) bool {
		scope := anthropicRawContentScopeMessage
		switch message.Get("role").String() {
		case "assistant":
			scope = anthropicRawContentScopeAssistantMessage
		case "user":
			scope = anthropicRawContentScopeUserMessage
		case "system":
			if !state.sawNormalizedItem {
				scope = anthropicRawContentScopeFirstSystemMessage
			}
		}
		content := message.Get("content")
		// Ungrouped ingress prepends reasoning items, making them the first item
		// even when a system text block precedes reasoning in the native array.
		if scope == anthropicRawContentScopeFirstSystemMessage && content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if (block.Get("type").String() == "thinking" && block.Get("thinking").Type == gjson.String) ||
					(block.Get("type").String() == "redacted_thinking" && block.Get("data").Type == gjson.String) {
					state.sawNormalizedItem = true
				}
				return true
			})
		}
		messagePath := rawRequestArrayPath("messages", int(index.Int()))
		collectErr = collectAnthropicRawTransformContentTargets(state, content, rawRequestObjectPath(messagePath, "content"), scope)
		return collectErr == nil
	})
	return state.targets, collectErr
}

// collectAnthropicRawResponseTextTargets enumerates writable native response text.
func collectAnthropicRawResponseTextTargets(root gjson.Result) ([]rawJSONTextTarget, error) {
	state := &anthropicRawTransformTargets{}
	content := root.Get("content")
	if !content.Exists() || content.Type == gjson.Null {
		return nil, nil
	}
	if !content.IsArray() {
		return nil, fmt.Errorf("raw Anthropic response content must be an array")
	}
	var collectErr error
	content.ForEach(func(index, block gjson.Result) bool {
		collectErr = collectAnthropicRawTransformContentBlockTarget(state, block, rawRequestArrayPath("content", int(index.Int())), anthropicRawContentScopeAssistantMessage)
		return collectErr == nil
	})
	return state.targets, collectErr
}

// collectAnthropicRawTransformContentTargets walks the native content union while
// preserving the distinction between message text, system metadata, and tool data.
func collectAnthropicRawTransformContentTargets(state *anthropicRawTransformTargets, content gjson.Result, path string, scope anthropicRawContentScope) error {
	if !content.Exists() || content.Type == gjson.Null {
		return nil
	}
	if content.Type == gjson.String {
		// An empty top-level system string produces no normalized system item.
		if scope == anthropicRawContentScopeSystem && content.String() == "" {
			return nil
		}
		return appendAnthropicRawTransformTextTarget(state, content, path, scope)
	}
	if content.IsObject() {
		return collectAnthropicRawTransformContentBlockTarget(state, content, path, scope)
	}
	if !content.IsArray() {
		return fmt.Errorf("raw Anthropic content path %q must be a string, object, or array", path)
	}
	var collectErr error
	content.ForEach(func(index, block gjson.Result) bool {
		collectErr = collectAnthropicRawTransformContentBlockTarget(state, block, rawRequestArrayPath(path, int(index.Int())), scope)
		return collectErr == nil
	})
	return collectErr
}

// collectAnthropicRawTransformContentBlockTarget mirrors ingress text targets and first-item state.
// Non-text items affect whether a later system header was removed at ingress;
// generated user placeholders reserve ordinals without writable native fields.
func collectAnthropicRawTransformContentBlockTarget(state *anthropicRawTransformTargets, block gjson.Result, path string, scope anthropicRawContentScope) error {
	if !block.IsObject() {
		return fmt.Errorf("raw Anthropic content block at %q must be an object", path)
	}
	blockType := block.Get("type")
	if !blockType.Exists() || blockType.Type == gjson.Null {
		return nil
	}
	if blockType.Type != gjson.String {
		return fmt.Errorf("raw Anthropic content block type at %q must be a string", path)
	}
	if scope == anthropicRawContentScopeSystem || scope == anthropicRawContentScopeToolResult {
		if blockType.String() == string(anthropic.AnthropicContentBlockTypeText) {
			return appendAnthropicRawTransformTextTarget(state, block.Get("text"), rawRequestObjectPath(path, "text"), scope)
		}
		return nil
	}
	switch anthropic.AnthropicContentBlockType(blockType.String()) {
	case anthropic.AnthropicContentBlockTypeText:
		return appendAnthropicRawTransformTextTarget(state, block.Get("text"), rawRequestObjectPath(path, "text"), scope)
	case anthropic.AnthropicContentBlockTypeToolResult:
		if block.Get("tool_use_id").Type != gjson.String {
			return nil
		}
		state.sawNormalizedItem = true
		return collectAnthropicRawTransformContentTargets(state, block.Get("content"), rawRequestObjectPath(path, "content"), anthropicRawContentScopeToolResult)
	case anthropic.AnthropicContentBlockTypeMCPToolResult:
		// LLM guardrails omit native MCP outputs; count their normalized item
		// for billing's first-item scope, but reserve no ordinary text target.
		if block.Get("tool_use_id").Type == gjson.String {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeFallback:
		state.sawNormalizedItem = true
		if scope == anthropicRawContentScopeUserMessage {
			state.nextIndex++
		}
	case anthropic.AnthropicContentBlockTypeCompaction:
		if block.Get("content").Exists() && block.Get("content").Type != gjson.Null {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeImage, anthropic.AnthropicContentBlockTypeDocument:
		if block.Get("source").IsObject() {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeContainerUpload:
		if block.Get("file_id").Type == gjson.String {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeToolUse, anthropic.AnthropicContentBlockTypeMCPToolUse:
		if block.Get("id").Type == gjson.String && block.Get("name").Type == gjson.String {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeServerToolUse:
		if anthropicRawServerToolUseEmitsItem(block, scope == anthropicRawContentScopeAssistantMessage) {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeThinking:
		if block.Get("thinking").Type == gjson.String {
			state.sawNormalizedItem = true
		}
	case anthropic.AnthropicContentBlockTypeRedactedThinking:
		if block.Get("data").Type == gjson.String {
			state.sawNormalizedItem = true
		}
	}
	return nil
}

// anthropicRawServerToolUseEmitsItem mirrors ungrouped ingress's name and role
// rules so ignored server-tool blocks cannot suppress first-item billing filtering.
// Recognized names do not require an ID because ingress also permits a missing ID.
func anthropicRawServerToolUseEmitsItem(block gjson.Result, isOutputMessage bool) bool {
	name := block.Get("name")
	if name.Type != gjson.String {
		return false
	}
	switch anthropic.AnthropicToolName(name.String()) {
	case anthropic.AnthropicToolNameToolSearchBM25, anthropic.AnthropicToolNameToolSearchRegex:
		return true
	case anthropic.AnthropicToolNameWebSearch, anthropic.AnthropicToolNameWebFetch,
		anthropic.AnthropicToolNameAdvisor, anthropic.AnthropicToolNameCodeExecution,
		anthropic.AnthropicToolNameBashCodeExecution, anthropic.AnthropicToolNameTextEditorCodeExecution:
		return isOutputMessage
	default:
		return false
	}
}

// appendAnthropicRawTransformTextTarget appends a writable native field at its normalized ordinal.
// Placeholder rows reserve IDs without destinations. Standalone billing metadata
// is excluded only where ExtractAnthropicBillingHeader already removes it from ingress.
func appendAnthropicRawTransformTextTarget(state *anthropicRawTransformTargets, field gjson.Result, path string, scope anthropicRawContentScope) error {
	if !field.Exists() || field.Type == gjson.Null {
		return nil
	}
	if field.Type != gjson.String {
		return fmt.Errorf("raw Anthropic text path %q must be a string", path)
	}
	firstSystem := scope == anthropicRawContentScopeSystem || (scope == anthropicRawContentScopeFirstSystemMessage && !state.sawNormalizedItem)
	state.sawNormalizedItem = true
	if scope == anthropicRawContentScopeUserMessage && strings.TrimSpace(field.String()) == "" {
		state.nextIndex++ // normalizeBifrostInputContentBlocks replaces this item with "...".
		return nil
	}
	if field.String() == "" {
		return nil
	}
	if firstSystem && isAnthropicRawBillingHeaderText(field.String()) {
		return nil
	}
	state.targets = append(state.targets, rawJSONTextTarget{ID: schemas.TextTargetIDForIndex(state.nextIndex), Path: path})
	state.nextIndex++
	return nil
}

// isAnthropicRawBillingHeaderText mirrors the standalone metadata check used by
// schemas.BifrostResponsesRequest.ExtractAnthropicBillingHeader so raw transform
// IDs exclude only system text omitted from normalized guardrail evaluation.
func isAnthropicRawBillingHeaderText(text string) bool {
	metadata, ok := strings.CutPrefix(strings.TrimSpace(text), "x-anthropic-billing-header:")
	if !ok || strings.ContainsAny(metadata, "\r\n") {
		return false
	}
	found := false
	for field := range strings.SplitSeq(metadata, ";") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || value == "" || strings.IndexFunc(field, unicode.IsSpace) >= 0 {
			return false
		}
		found = true
	}
	return found
}

// collectAnthropicRawContentPaths handles the string, block-array, and single-block Anthropic content union.
func collectAnthropicRawContentPaths(paths *[]string, content gjson.Result, path string, scope anthropicRawContentScope) error {
	if !content.Exists() || content.Type == gjson.Null {
		return nil
	}
	if content.Type == gjson.String {
		*paths = append(*paths, path)
		return nil
	}
	if content.IsObject() {
		return collectAnthropicRawContentBlockPaths(paths, content, path, scope)
	}
	if !content.IsArray() {
		return fmt.Errorf("raw Anthropic content path %q must be a string, object, or array", path)
	}

	var collectErr error
	content.ForEach(func(index, block gjson.Result) bool {
		collectErr = collectAnthropicRawContentBlockPaths(paths, block, rawRequestArrayPath(path, int(index.Int())), scope)
		return collectErr == nil
	})
	return collectErr
}

// collectAnthropicRawContentBlockPaths selects only block fields represented as mutable normalized text.
func collectAnthropicRawContentBlockPaths(paths *[]string, block gjson.Result, path string, scope anthropicRawContentScope) error {
	if !block.IsObject() {
		return fmt.Errorf("raw Anthropic content block at %q must be an object", path)
	}
	blockType := block.Get("type")
	if !blockType.Exists() || blockType.Type == gjson.Null {
		return nil
	}
	if blockType.Type != gjson.String {
		return fmt.Errorf("raw Anthropic content block type at %q must be a string", path)
	}

	switch scope {
	case anthropicRawContentScopeSystem, anthropicRawContentScopeToolResult:
		if blockType.String() == string(anthropic.AnthropicContentBlockTypeText) {
			return appendRawRequestStringPath(paths, block.Get("text"), rawRequestObjectPath(path, "text"))
		}
		return nil
	case anthropicRawContentScopeMessage:
		switch anthropic.AnthropicContentBlockType(blockType.String()) {
		case anthropic.AnthropicContentBlockTypeText:
			return appendRawRequestStringPath(paths, block.Get("text"), rawRequestObjectPath(path, "text"))
		case anthropic.AnthropicContentBlockTypeToolUse:
			return collectAnthropicArgumentStringPaths(paths, block.Get("input"), rawRequestObjectPath(path, "input"))
		case anthropic.AnthropicContentBlockTypeToolResult, anthropic.AnthropicContentBlockTypeMCPToolResult:
			return collectAnthropicRawContentPaths(
				paths,
				block.Get("content"),
				rawRequestObjectPath(path, "content"),
				anthropicRawContentScopeToolResult,
			)
		}
	}
	return nil
}

// shouldUsePassthrough checks if the request should be sent to the passthrough endpoint.
func shouldUsePassthrough(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, alias string) bool {
	return anthropic.IsClaudeCodeRequest(ctx) && isClaudeModel(ctx, model, alias, string(provider))
}

// serverToolSynthesizesResultBlock reports whether an item's output_item.done
// expands into a server_tool_use content_block_stop plus a synthesized
// *_tool_result block (its own start+stop): advisor, web_search, web_fetch and
// code_execution. For these the raw upstream result-block stop cannot be
// forwarded verbatim — the client never saw a matching start — so the converter
// must render the full sequence. Computer is excluded: it streams a single
// content block and never synthesizes a result block.
func serverToolSynthesizesResultBlock(item *schemas.ResponsesMessage) bool {
	if item == nil || item.Type == nil {
		return false
	}
	switch *item.Type {
	case schemas.ResponsesMessageTypeAdvisorCall,
		schemas.ResponsesMessageTypeWebSearchCall,
		schemas.ResponsesMessageTypeWebFetchCall,
		schemas.ResponsesMessageTypeCodeInterpreterCall:
		return true
	}
	return false
}

// mustConvertInPassthrough reports whether a bifrost stream response must be
// rendered via the normalized converter instead of forwarding its raw upstream
// frame, even on the Anthropic passthrough path.
//
// Server tools (advisor, web_search, web_fetch, code_execution) map one Responses
// item onto several Anthropic content blocks with re-numbered indices: the forward
// converter swallows their intermediate frames (server_tool_use input delta/stop,
// result-block start) and re-synthesizes the result block at output_item.done.
// Forwarding the surviving raw frames verbatim therefore emits content_block_stop/
// _delta for indices the client never opened, which strict clients (Claude Code)
// reject with "API Error: Content block not found".
//
// Routing the following through the converter keeps its block-index allocation
// authoritative and in lockstep with the surrounding raw frames:
//   - every output_item.added runs the converter's allocator, so the block counter
//     advances for each block in upstream order (server-tool starts otherwise took
//     the raw path and desynced the counter);
//   - server-tool output_item.done renders the server stop + synthesized result
//     block start/stop at matching indices;
//   - server-tool lifecycle events (no Anthropic equivalent) collapse to nothing,
//     dropping the duplicate raw content_block frame they would otherwise carry.
func mustConvertInPassthrough(resp *schemas.BifrostResponsesStreamResponse) bool {
	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		return true
	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		return serverToolSynthesizesResultBlock(resp.Item)
	case schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsAdded,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsCompleted,
		schemas.ResponsesStreamResponseTypeWebFetchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebFetchCallFetching,
		schemas.ResponsesStreamResponseTypeWebFetchCallCompleted,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallInterpreting,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone:
		return true
	}
	return false
}

// isClaudeModel reports whether this attempt talks to a native Anthropic Messages surface, which
// is what makes forwarding the upstream response verbatim safe. model is the caller-sent model and
// alias the resolved wire model, empty at ingress; provider is empty only when the caller sent no
// provider prefix.
//
// Bedrock Mantle, Vertex and Azure are multi-family, so they resolve the model family instead of
// sniffing names: an alias labelled "claude-opus" may well point at an OpenAI model, and passing an
// OpenAI Responses body back to an Anthropic client yields a malformed (HTTP 200) response.
func isClaudeModel(ctx *schemas.BifrostContext, model, alias, provider string) bool {
	switch provider {
	case string(schemas.Anthropic):
		return true
	case string(schemas.BedrockMantle), string(schemas.Vertex), string(schemas.Azure):
		// alias is empty at ingress, where the caller-sent model is all there is to resolve against.
		candidate := alias
		if candidate == "" {
			candidate = model
		}
		// Name check first so this stays a strict subset of the pre-family behavior: the family
		// resolution can only downgrade a Claude-looking name, never promote a non-Claude one.
		return (schemas.IsAnthropicModel(model) || schemas.IsAnthropicModel(alias)) &&
			schemas.IsAnthropicModelFamily(ctx, candidate)
	case "":
		return schemas.IsAnthropicModel(model) || schemas.IsAnthropicModel(alias)
	}
	return false
}

// extractAnthropicListModelsParams extracts query parameters for list models request
func extractAnthropicListModelsParams(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {

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
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.CountTokensRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &anthropic.AnthropicMessageRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if anthropicReq, ok := req.(*anthropic.AnthropicMessageRequest); ok {
					bifrostReq := anthropicReq.ToBifrostResponsesRequest(ctx)
					normalizeBifrostInputContentBlocks(bifrostReq)
					return &schemas.BifrostRequest{
						CountTokensRequest: bifrostReq,
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
			PreCallback: checkAnthropicPassthrough,
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchCreateRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
				mixedModels := false
				requests := make([]schemas.BatchRequestItem, len(anthropicReq.Requests))
				for i, r := range anthropicReq.Requests {
					requestModel, ok := r.Params["model"].(string)
					if !ok || requestModel == "" {
						// Only the non-Anthropic providers need a model to route at all.
						if isNonAnthropicProvider {
							return nil, errors.New("model is required")
						}
					} else if model == nil {
						model = schemas.Ptr(requestModel)
					} else if *model != requestModel {
						if isNonAnthropicProvider {
							return nil, errors.New("for non-Anthropic providers, model must be the same for all requests")
						}
						mixedModels = true
					}
					requests[i] = schemas.BatchRequestItem{
						CustomID: r.CustomID,
						Params:   r.Params,
					}
				}
				if mixedModels {
					model = nil
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchListRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchRetrieveRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchCancelRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.BatchResultsRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileUploadRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
			if contentTypeValues := form.Value["content_type"]; len(contentTypeValues) > 0 && contentTypeValues[0] != "" {
				uploadReq.ContentType = &contentTypeValues[0]
			} else if partContentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type")); partContentType != "" {
				uploadReq.ContentType = &partContentType
			}
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
						File:        uploadReq.File,
						Filename:    uploadReq.Filename,
						Purpose:     schemas.FilePurpose(uploadReq.Purpose),
						ContentType: uploadReq.ContentType,
						Provider:    provider,
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileListRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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

	// Retrieve file metadata endpoint - GET /v1/files/{file_id}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/files/{file_id}",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileRetrieveRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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

	// Download file content endpoint - GET /v1/files/{file_id}/content
	// No response converter: the router streams the raw bytes with the provider's content type.
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   pathPrefix + "/v1/files/{file_id}/content",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileContentRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &anthropic.AnthropicFileContentRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if contentReq, ok := req.(*anthropic.AnthropicFileContentRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				// Handle file id conversion for Gemini
				if provider == schemas.Gemini {
					contentReq.FileID = strings.Replace(contentReq.FileID, "files-", "files/", 1)
				}
				return &FileRequest{
					Type: schemas.FileContentRequest,
					ContentRequest: &schemas.BifrostFileContentRequest{
						FileID:   contentReq.FileID,
						Provider: provider,
					},
				}, nil
			}
			return nil, errors.New("invalid file content request type")
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
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.FileDeleteRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
func NewAnthropicRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *AnthropicRouter {
	routes := CreateAnthropicRouteConfigs("/anthropic", logger)
	routes = append(routes, CreateAnthropicListModelsRouteConfigs("/anthropic", handlerStore)...)
	routes = append(routes, CreateAnthropicCountTokensRouteConfigs("/anthropic", handlerStore)...)
	routes = append(routes, CreateAnthropicBatchRouteConfigs("/anthropic", handlerStore)...)
	routes = append(routes, CreateAnthropicFilesRouteConfigs("/anthropic", handlerStore)...)

	return &AnthropicRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, accessResolver, routes, nil, logger),
	}
}

// collectAnthropicArgumentStringPaths mirrors JSON argument values without changing tool metadata or object keys.
func collectAnthropicArgumentStringPaths(paths *[]string, value gjson.Result, path string) error {
	if value.Type == gjson.String {
		return appendRawRequestStringPath(paths, value, path)
	}
	var err error
	if value.IsObject() || value.IsArray() {
		value.ForEach(func(key, child gjson.Result) bool {
			childPath := rawRequestObjectPath(path, key.String())
			if value.IsArray() {
				childPath = rawRequestArrayPath(path, int(key.Int()))
			}
			err = collectAnthropicArgumentStringPaths(paths, child, childPath)
			return err == nil
		})
	}
	return err
}
