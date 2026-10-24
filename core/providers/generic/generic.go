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

// RoutePlan is the outcome of matching one request against a provider's routes.
type RoutePlan struct {
	ModelPath string // request body path the model is read from, "" for none
	Streams   bool   // serve with the streaming relay
	provider  *GenericProvider
	route     *compiledRoute // nil when no route matched
}

type routePlanContextKey struct{}

// WithPlan stores a plan on the request context for the relay to pick up. It is a small handle
// (two pointers), never request-sized data.
func WithPlan(ctx *schemas.BifrostContext, plan RoutePlan) {
	ctx.SetValue(routePlanContextKey{}, plan)
}

// Plan matches the request against the routes once and answers everything the transport needs
// to dispatch it: the model path and the streaming decision. The transport stores the plan on
// the request context with WithPlan so the relay does not match the routes a second time.
func (provider *GenericProvider) Plan(method, path, accept string) RoutePlan {
	route := provider.matchRoute(method, path)
	usage := provider.usage
	if route != nil {
		usage = route.usage
	}
	return RoutePlan{
		ModelPath: usage.effectiveModelPath(),
		Streams:   provider.streams(route, path, accept),
		provider:  provider,
		route:     route,
	}
}

// GenericProvider relays raw HTTP requests to one configured upstream.
type GenericProvider struct {
	logger               schemas.Logger
	client               *http.Client                  // unary client; Timeout bounds the whole response
	streamingClient      *http.Client                  // streaming client; no whole-response timeout
	networkConfig        schemas.NetworkConfig         // base URL, extra headers, timeouts
	extraHeaderNames     map[string]struct{}           // lower-cased names of the operator's extra headers, which a caller may not replace
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
	// A relay never follows redirects: the caller gets the 3xx and decides. Following one would
	// send the injected credential to whatever host the upstream named (Go strips only
	// Authorization and Cookie across hosts) and would step around the base URL host pinning.
	// This matches the fasthttp passthroughs, whose Do never follows redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	// ignore_provider_cost means "do not trust what the upstream reports": the cost_path is dropped
	// while the operator's own flat fee and unit pricing still apply. The transport keeps generic
	// providers off the pricing engine's ignore list so that number is not thrown away there.
	usage := resolveUsage(cfg.Usage, nil)
	usage.ignoreUpstreamCost = config.IgnoreProviderCost
	usage.eventPaths = usage.collectEventPaths()
	routes := compileRoutes(cfg)
	for i := range routes {
		routes[i].usage.ignoreUpstreamCost = config.IgnoreProviderCost
		routes[i].usage.eventPaths = routes[i].usage.collectEventPaths()
	}

	return &GenericProvider{
		logger:               logger,
		client:               client,
		streamingClient:      providerUtils.BuildStreamingHTTPClient(client),
		networkConfig:        config.NetworkConfig,
		extraHeaderNames:     lowerHeaderNames(config.NetworkConfig.ExtraHeaders),
		customProviderConfig: config.CustomProviderConfig,
		cfg:                  cfg,
		usage:                usage,
		routes:               routes,
		streamGlobs:          compileStreamGlobs(cfg),
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the custom provider's name.
func (provider *GenericProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Generic, provider.customProviderConfig)
}

// streams decides whether a request is served by the streaming relay: a route's explicit flag,
// then the streaming.paths globs, then the Accept header when enabled. The path's wording is
// never a signal: unlike an LLM API, a generic API with "stream" in a
// path (/v1/streams/{id}) usually answers with plain JSON, and the streaming relay forwards a
// non-SSE body unread, so the response-side usage mapping would be silently skipped.
func (provider *GenericProvider) streams(route *compiledRoute, path, accept string) bool {
	if route != nil && route.stream != nil {
		return *route.stream
	}
	for _, glob := range provider.streamGlobs {
		if glob.match(path) {
			return true
		}
	}
	return provider.cfg.Streaming != nil && provider.cfg.Streaming.DetectFromAcceptHeader && acceptsEventStream(accept)
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
// provider defaults (see billableByDefault for which methods count without a route). The route is
// taken from the plan the transport stored on the context when it was made for this provider
// instance, so the routes are matched once per request; the Go SDK path, which stores none,
// matches here.
func (provider *GenericProvider) resolve(ctx *schemas.BifrostContext, method, path string) (resolvedUsage, bool, *compiledRoute) {
	var route *compiledRoute
	if plan, ok := ctx.Value(routePlanContextKey{}).(RoutePlan); ok && plan.provider == provider {
		route = plan.route
	} else {
		route = provider.matchRoute(method, path)
	}
	if route != nil {
		return route.usage, route.billable, route
	}
	return provider.usage, billableByDefault(method, nil), nil
}

// lowerHeaderNames is the set of configured extra header names, lower-cased for the caller
// header loop. Built once per provider.
func lowerHeaderNames(extra map[string]schemas.SecretVar) map[string]struct{} {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(extra))
	for name := range extra {
		out[strings.ToLower(name)] = struct{}{}
	}
	return out
}

// buildRequest turns a passthrough request into the upstream net/http request: host-pinned URL,
// caller headers minus credentials, operator extra headers, then the configured auth.
func (provider *GenericProvider) buildRequest(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (*http.Request, *schemas.BifrostError) {
	rawQuery, err := provider.stripCallerQueryCredential(req.RawQuery)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError(err.Error())
	}
	requestURL, err := providerUtils.BuildPassthroughURL(provider.networkConfig.BaseURL, req.Path, provider.appendAuthQuery(rawQuery, key))
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
		// A header the operator configured (a tenant, an API version) is the operator's to set; a
		// caller's copy of it is dropped rather than replacing it.
		if _, operator := provider.extraHeaderNames[strings.ToLower(k)]; operator {
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
	usageMapping, billable, route := provider.resolve(ctx, req.Method, req.Path)
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
	redactKeyFromHeaders(headers, provider.authQuery(), key.Value.GetValue())
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
	usageMapping, billable, _ := provider.resolve(ctx, req.Method, req.Path)

	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.streamingClient, httpReq)
	if err != nil {
		return nil, passthroughTransportError(err, time.Since(startTime))
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeadersFromHTTP(resp)
	redactKeyFromHeaders(headers, provider.authQuery(), key.Value.GetValue())
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
			params.Observe = usageMapping.streamObserver()
		}
	}
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	return providerUtils.StreamPassthroughHTTP(ctx, postHookRunner, postHookSpanFinalizer, resp.Body, params), nil
}
