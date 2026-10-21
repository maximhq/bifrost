// Package generic implements the "generic" custom-provider base type: a raw HTTP relay to a
// non-LLM API (Tavily, AWS Textract, Azure TTS, ...). It serves only Passthrough and
// PassthroughStream; every typed operation is unsupported. Bifrost still selects and injects the
// key, strips caller credentials, pins the upstream host, logs the call, runs plugins, and reports
// usage so governance counts and charges it.
package generic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// genericPassthroughMaxResponseBytes bounds a response the unary relay will buffer. A package
// variable so tests can lower it; the streaming relay never buffers a body.
var genericPassthroughMaxResponseBytes int64 = 64 << 20

// PassthroughProvider is what the HTTP transport asserts a resolved provider against before
// dispatching a /passthrough/{provider}/... request to it.
type PassthroughProvider interface {
	GenericConfig() *schemas.GenericProviderConfig
	Streams(method, path, accept string, bodyStream bool) bool
}

// GenericProvider relays raw HTTP requests to one configured upstream.
type GenericProvider struct {
	logger               schemas.Logger
	client               *http.Client                  // unary client; Timeout bounds the whole response
	streamingClient      *http.Client                  // streaming client; no whole-response timeout
	networkConfig        schemas.NetworkConfig         // base URL, extra headers, timeouts
	customProviderConfig *schemas.CustomProviderConfig // name, allowed requests, generic config
	cfg                  *schemas.GenericProviderConfig
	usage                resolvedUsage // provider-level usage mapping
	routes               []compiledRoute
	streamGlobs          []routeGlob
	sendBackRawRequest   bool
	sendBackRawResponse  bool
}

var _ schemas.Provider = (*GenericProvider)(nil)

// NewGenericProvider builds a relay for config. base_url is required: it is the only thing that
// says where requests go. Routes and globs are compiled here, once per provider (re)build.
func NewGenericProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*GenericProvider, error) {
	config.CheckAndSetDefaults()
	if strings.TrimSpace(config.NetworkConfig.BaseURL) == "" {
		return nil, errors.New("generic provider requires network_config.base_url")
	}
	var cfg *schemas.GenericProviderConfig
	if config.CustomProviderConfig != nil && config.CustomProviderConfig.GenericConfig != nil {
		cfg = config.CustomProviderConfig.GenericConfig.Clone()
	} else {
		cfg = &schemas.GenericProviderConfig{}
	}
	if cfg.Auth.Type == "" {
		cfg.Auth.Type = schemas.GenericAuthBearer
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("generic provider config: %w", err)
	}

	client, err := providerUtils.NewNetHTTPClient(config)
	if err != nil {
		return nil, err
	}

	return &GenericProvider{
		logger:               logger,
		client:               client,
		streamingClient:      providerUtils.BuildStreamingHTTPClient(client),
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		cfg:                  cfg,
		usage:                resolveUsage(cfg.Usage, nil),
		routes:               compileRoutes(cfg),
		streamGlobs:          compileStreamGlobs(cfg),
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the custom provider's name.
func (provider *GenericProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Generic, provider.customProviderConfig)
}

// GenericConfig returns the relay settings this instance was built from.
func (provider *GenericProvider) GenericConfig() *schemas.GenericProviderConfig {
	return provider.cfg
}

// Streams decides whether a request is served by the streaming relay: a route's explicit flag,
// then the streaming.paths globs, then the Accept header when enabled, then the body's stream
// flag, then a path that names a stream.
func (provider *GenericProvider) Streams(method, path, accept string, bodyStream bool) bool {
	if route := provider.matchRoute(method, path); route != nil && route.stream != nil {
		return *route.stream
	}
	for _, glob := range provider.streamGlobs {
		if glob.match(path) {
			return true
		}
	}
	if provider.cfg.Streaming != nil && provider.cfg.Streaming.DetectFromAcceptHeader && acceptsEventStream(accept) {
		return true
	}
	return bodyStream || strings.Contains(strings.ToLower(path), "stream")
}

func acceptsEventStream(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mediaType, _, _ := strings.Cut(part, ";")
		if strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream") {
			return true
		}
	}
	return false
}

func isEventStream(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

// resolve returns the usage mapping and billing flag for a request: the matching route's, or the
// provider defaults (billable).
func (provider *GenericProvider) resolve(method, path string) (resolvedUsage, bool, *compiledRoute) {
	if route := provider.matchRoute(method, path); route != nil {
		return route.usage, route.billable, route
	}
	return provider.usage, true, nil
}

// buildRequest turns a passthrough request into the upstream net/http request: host-pinned URL,
// caller headers minus credentials, operator extra headers, then the configured auth.
func (provider *GenericProvider) buildRequest(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (*http.Request, *schemas.BifrostError) {
	rawQuery := provider.appendAuthQuery(provider.stripCallerQueryCredential(req.RawQuery), key)
	requestURL, err := providerUtils.BuildPassthroughURL(provider.networkConfig.BaseURL, req.Path, rawQuery)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError(err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, requestURL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err)
	}

	providerUtils.SetExtraHeadersHTTP(ctx, httpReq, provider.networkConfig.ExtraHeaders, nil)
	providerUtils.StripCallerAuthForInsecureURL(requestURL, req.SafeHeaders)
	authHeader := provider.authHeader()
	for k, v := range req.SafeHeaders {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "content-length") || (authHeader != "" && strings.EqualFold(k, authHeader)) {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	if bifrostErr := provider.applyAuth(ctx, httpReq, key); bifrostErr != nil {
		return nil, bifrostErr
	}
	return httpReq, nil
}

// Passthrough relays one request and buffers the upstream answer. A successful call on a billable
// route always reports usage (possibly empty) so governance counts it; non-2xx reports none.
func (provider *GenericProvider) Passthrough(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Generic, provider.customProviderConfig, schemas.PassthroughRequest); err != nil {
		return nil, err
	}
	httpReq, bifrostErr := provider.buildRequest(ctx, key, req)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	usageMapping, billable, route := provider.resolve(req.Method, req.Path)
	streamingRoute := route != nil && route.stream != nil && *route.stream

	client := provider.client
	if streamingRoute {
		client = provider.streamingClient
	}
	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(client, httpReq)
	latency := time.Since(startTime)
	if err != nil {
		return nil, passthroughTransportError(err, latency)
	}
	defer resp.Body.Close()

	headers := providerUtils.ExtractPassthroughProviderResponseHeadersFromHTTP(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	var reader io.Reader = resp.Body
	if streamingRoute {
		providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
		idleReader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()
		reader = idleReader
	}
	body, err := io.ReadAll(io.LimitReader(reader, genericPassthroughMaxResponseBytes+1))
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError("error reading response body", err), latency)
	}
	if int64(len(body)) > genericPassthroughMaxResponseBytes {
		tooLarge := providerUtils.NewBifrostOperationError(fmt.Sprintf("generic passthrough response is too large to buffer (limit %d bytes); use the streaming passthrough", genericPassthroughMaxResponseBytes), nil)
		tooLarge.StatusCode = schemas.Ptr(http.StatusBadGateway)
		return nil, providerUtils.SetErrorLatency(tooLarge, latency)
	}

	var usage *schemas.BifrostPassthroughUsage
	if billable && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		usage = extractUsage(usageMapping, resp.Header.Get("Content-Type"), body)
	}

	response := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         req.Path,
		},
		PassthroughUsage: usage,
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = string(req.Body)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = string(body)
	}
	return response, nil
}

// PassthroughStream relays one request and forwards the upstream body to the caller as it
// arrives, byte for byte. SSE bodies are framed so usage can be observed per event; anything else
// is forwarded unframed and reports the route's default usage when it completes.
func (provider *GenericProvider) PassthroughStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Generic, provider.customProviderConfig, schemas.PassthroughStreamRequest); err != nil {
		return nil, err
	}
	httpReq, bifrostErr := provider.buildRequest(ctx, key, req)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	usageMapping, billable, _ := provider.resolve(req.Method, req.Path)

	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.streamingClient, httpReq)
	if err != nil {
		return nil, passthroughTransportError(err, time.Since(startTime))
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeadersFromHTTP(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	params := providerUtils.PassthroughStreamParams{
		StatusCode:       resp.StatusCode,
		Headers:          headers,
		Path:             req.Path,
		RawRequest:       req.Body,
		CancellationBody: req.Body,
		StartTime:        startTime,
		SkipFraming:      !isEventStream(resp.Header.Get("Content-Type")),
		Logger:           provider.logger,
	}
	if billable {
		params.DefaultUsage = usageMapping.defaultUsage()
		if !params.SkipFraming {
			params.HasUsage = usageMapping.hasUsageEvent
			params.Observe = usageMapping.observeUsage
		}
	}
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	return providerUtils.StreamPassthroughHTTP(ctx, postHookRunner, postHookSpanFinalizer, resp.Body, params), nil
}
