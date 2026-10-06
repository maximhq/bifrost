// Package antigravity implements the Google Antigravity provider: Gemini, Claude and
// GPT-OSS models served through Cloud Code Assist's v1internal API on an Antigravity
// (Google One AI) subscription.
//
// A key's value is the account's OAuth credential (see Credentials). Each request mints
// or reuses a short-lived access token, resolves the account's Cloud Code Assist project,
// and wraps a Gemini generateContent body in the v1internal envelope. Request and
// response conversion is delegated to the gemini package; this package adds the
// envelope, the backend's request restrictions and the OAuth lifecycle.
package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	generatePath       = "/v1internal:generateContent"
	streamGeneratePath = "/v1internal:streamGenerateContent?alt=sse"
	fetchModelsPath    = "/v1internal:fetchAvailableModels"

	// maxExchangeBodyBytes caps token, discovery and model-list responses.
	maxExchangeBodyBytes = 8 << 20
	// listModelsTimeout bounds live model discovery before falling back to the static list.
	listModelsTimeout = 8 * time.Second
)

// AntigravityProvider implements the Provider interface for Google Antigravity.
type AntigravityProvider struct {
	logger              schemas.Logger
	client              *fasthttp.Client
	streamingClient     *fasthttp.Client
	exchangeClient      *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
	credentialUpdater   schemas.KeyCredentialUpdater
}

// NewAntigravityProvider creates a new Antigravity provider instance. credentialUpdater,
// when set, receives a key's re-encoded credential after the provider learns something
// the stored value lacks (a discovered project, a rotated refresh token).
func NewAntigravityProvider(config *schemas.ProviderConfig, logger schemas.Logger, credentialUpdater schemas.KeyCredentialUpdater) (*AntigravityProvider, error) {
	config.CheckAndSetDefaults()
	if logger == nil {
		logger = noopLogger{}
	}

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	// Order is load-bearing: ConfigureDialer wraps the proxy's Dial, and the clones below
	// must copy the fully configured client.
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	// Token refresh, project discovery and model listing inherit the same proxy, dial
	// and TLS policy as inference, with a body cap enforced at read time.
	exchangeClient := providerUtils.CloneFastHTTPClientConfig(client)
	exchangeClient.MaxResponseBodySize = maxExchangeBodyBytes
	exchangeClient.StreamResponseBody = false

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AntigravityProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		exchangeClient:      exchangeClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
		credentialUpdater:   credentialUpdater,
	}, nil
}

// GetProviderKey returns the provider identifier.
func (p *AntigravityProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Antigravity
}

// setRequestHeaders sets the only headers Cloud Code Assist expects from the IDE.
func (p *AntigravityProvider) setRequestHeaders(ctx *schemas.BifrostContext, req *fasthttp.Request, url, accessToken string, body []byte) {
	providerUtils.SetExtraHeaders(ctx, req, p.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", userAgent)
	req.SetBody(body)
}

// ListModels lists the models each key's account can call. Live discovery is used when
// it answers; otherwise the static Antigravity catalog stands in.
func (p *AntigravityProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("antigravity: no keys configured")
	}
	if request == nil {
		request = &schemas.BifrostListModelsRequest{Provider: schemas.Antigravity}
	}
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, p.listModelsByKey)
}

func (p *AntigravityProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	models := p.fetchAvailableModels(ctx, key)
	if len(models) == 0 {
		models = staticModels
	}
	return toBifrostListModelsResponse(models, key, request.Unfiltered), nil
}

// fetchAvailableModels asks Cloud Code Assist which models the key's account may call.
// It returns nil on any failure so the caller can fall back to the static catalog.
func (p *AntigravityProvider) fetchAvailableModels(ctx *schemas.BifrostContext, key schemas.Key) []modelInfo {
	auth, bifrostErr := p.resolveAuth(ctx, key, "")
	if bifrostErr != nil {
		p.logger.Debug("antigravity: live model discovery skipped for key %s: %s", key.ID, bifrostErr.Error.Message)
		return nil
	}
	body, err := marshalJSON(map[string]string{"project": auth.projectID})
	if err != nil {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()
	status, respBody, err := fastHTTPDoer{client: p.exchangeClient}.do(listCtx, http.MethodPost, p.networkConfig.BaseURL+fetchModelsPath, map[string]string{
		"Authorization": "Bearer " + auth.accessToken,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"User-Agent":    userAgent,
	}, body)
	if err != nil || status != http.StatusOK {
		p.logger.Debug("antigravity: live model discovery failed for key %s (status %d): %v", key.ID, status, err)
		return nil
	}
	var available availableModelsResponse
	if err := sonic.Unmarshal(respBody, &available); err != nil {
		p.logger.Debug("antigravity: could not parse fetchAvailableModels response: %v", err)
		return nil
	}
	return available.toModelInfos()
}

// TextCompletion is not supported by Antigravity.
func (p *AntigravityProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.GetProviderKey())
}

// TextCompletionStream is not supported by Antigravity.
func (p *AntigravityProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, p.GetProviderKey())
}

// ChatCompletion performs a chat completion request through Cloud Code Assist.
func (p *AntigravityProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	plan, bifrostErr := buildRequestPlan(ctx, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	auth, bifrostErr := p.resolveAuth(ctx, key, "")
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)
	url := p.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, generatePath)

	for attempt := 0; ; attempt++ {
		body, err := plan.envelope(auth.projectID)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		}
		respBody, latency, status, bifrostErr := p.doUnary(ctx, url, auth.accessToken, body)
		if bifrostErr != nil {
			// One forced refresh on a rejected token: it may have been revoked or rotated
			// before its stated expiry.
			if status == http.StatusUnauthorized && attempt == 0 {
				refreshed, refreshErr := p.resolveAuth(ctx, key, auth.accessToken)
				if refreshErr != nil {
					return nil, providerUtils.EnrichError(ctx, refreshErr, body, nil, sendBackRawRequest, sendBackRawResponse, latency)
				}
				auth = refreshed
				continue
			}
			if status == http.StatusUnauthorized {
				invalidateToken(key, auth.accessToken)
			}
			return nil, providerUtils.EnrichError(ctx, bifrostErr, body, respBody, sendBackRawRequest, sendBackRawResponse, latency)
		}

		geminiResponse, bifrostErr := unwrapResponse(respBody)
		if bifrostErr != nil {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, body, respBody, sendBackRawRequest, sendBackRawResponse, latency)
		}

		convTracer, convHandle := providerUtils.StartResponseConvertorSpan(ctx)
		response := geminiResponse.ToBifrostChatResponse()
		if convTracer != nil {
			convTracer.EndSpan(convHandle, schemas.SpanStatusOk, "")
		}
		restoreToolNames(response, plan.names)
		if response.Model == "" {
			response.Model = request.Model
		}
		response.ExtraFields.Latency = latency.Milliseconds()
		if headers, ok := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string); ok {
			response.ExtraFields.ProviderResponseHeaders = headers
		}
		if sendBackRawRequest {
			providerUtils.ParseAndSetRawRequest(&response.ExtraFields, body)
		}
		if sendBackRawResponse {
			var raw any
			if err := sonic.Unmarshal(respBody, &raw); err == nil {
				response.ExtraFields.RawResponse = raw
			}
		}
		return response, nil
	}
}

// doUnary posts one request and returns the decoded body of a 200, or the mapped error
// and upstream status otherwise.
func (p *AntigravityProvider) doUnary(ctx *schemas.BifrostContext, url, accessToken string, body []byte) ([]byte, time.Duration, int, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	p.setRequestHeaders(ctx, req, url, accessToken, body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, p.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, 0, bifrostErr
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	status := resp.StatusCode()
	if status != fasthttp.StatusOK {
		respBody := append([]byte(nil), resp.Body()...)
		return respBody, latency, status, providerUtils.SetErrorLatency(parseAntigravityError(resp), latency)
	}
	decoded, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, status, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	return append([]byte(nil), decoded...), latency, status, nil
}

// responseEnvelope is the v1internal response wrapper around a Gemini response.
type responseEnvelope struct {
	Response json.RawMessage `json:"response"`
	Error    *upstreamError  `json:"error"`
}

// unwrapResponse unwraps {"response": {...}} into a Gemini response, surfacing an inline
// error object as a BifrostError.
func unwrapResponse(body []byte) (*gemini.GenerateContentResponse, *schemas.BifrostError) {
	var envelope responseEnvelope
	if err := sonic.Unmarshal(body, &envelope); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	if envelope.Error != nil {
		return nil, normalizeUpstreamError(nil, envelope.Error.Code, envelope.Error)
	}
	if len(envelope.Response) == 0 || string(envelope.Response) == "null" {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(http.StatusBadGateway),
			Error:          &schemas.ErrorField{Message: "antigravity response missing response wrapper"},
		}
	}
	var response gemini.GenerateContentResponse
	if err := sonic.Unmarshal(envelope.Response, &response); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	return &response, nil
}

// restoreToolNames maps encoded tool names in a response back to the caller's names.
func restoreToolNames(response *schemas.BifrostChatResponse, names *toolNameCodec) {
	if response == nil || names == nil || len(names.decoded) == 0 {
		return
	}
	for i := range response.Choices {
		choice := &response.Choices[i]
		var calls []schemas.ChatAssistantMessageToolCall
		switch {
		case choice.ChatNonStreamResponseChoice != nil && choice.Message != nil && choice.Message.ChatAssistantMessage != nil:
			calls = choice.Message.ChatAssistantMessage.ToolCalls
		case choice.ChatStreamResponseChoice != nil && choice.Delta != nil:
			calls = choice.Delta.ToolCalls
		}
		for j := range calls {
			if calls[j].Function.Name != nil {
				restored := names.decode(*calls[j].Function.Name)
				calls[j].Function.Name = &restored
			}
		}
	}
}

// ChatCompletionStream performs a streaming chat completion request through Cloud Code Assist.
func (p *AntigravityProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	plan, bifrostErr := buildRequestPlan(ctx, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	auth, bifrostErr := p.resolveAuth(ctx, key, "")
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return p.streamChat(ctx, postHookRunner, postHookSpanFinalizer, key, request, plan, auth)
}

// Responses performs a responses request by converting through chat completions.
func (p *AntigravityProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := p.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}
	return chatResponse.ToBifrostResponsesResponse(), nil
}

// ResponsesStream performs a streaming responses request by converting through chat completions.
func (p *AntigravityProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return p.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
}

// noopLogger stands in when the provider is constructed without a logger.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                                            {}
func (noopLogger) Info(string, ...any)                                             {}
func (noopLogger) Warn(string, ...any)                                             {}
func (noopLogger) Error(string, ...any)                                            {}
func (noopLogger) Fatal(string, ...any)                                            {}
func (noopLogger) SetLevel(schemas.LogLevel)                                       {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType)                          {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder { return nil }
