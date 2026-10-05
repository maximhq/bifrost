package bedrock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// bedrockPassthroughSegment is one path segment of an allow-listed operation: either a literal that must
// match exactly, or a caller-supplied identifier checked by its own rule.
type bedrockPassthroughSegment struct {
	literal  string
	validate func(string) bool
}

// bedrockPassthroughRoute is one operation the Bedrock passthrough will forward.
type bedrockPassthroughRoute struct {
	operation string
	service   bedrockService
	segments  []bedrockPassthroughSegment
	streaming bool // AWS answers with an event stream, so the response is forwarded as it arrives
}

func literal(s string) bedrockPassthroughSegment { return bedrockPassthroughSegment{literal: s} }

func param(validate func(string) bool) bedrockPassthroughSegment {
	return bedrockPassthroughSegment{validate: validate}
}

// Each identifier follows the character set AWS documents for that parameter. Length is capped
// generously and not at AWS's own maxima: AWS validates length itself, so the gateway cannot wrongly
// refuse an id if AWS ever lengthens them, while the character sets, which carry the safety, match.
var (
	// agentId, agentAliasId, knowledgeBaseId and guardrailIdentifier: [0-9a-zA-Z]+
	bedrockPassthroughAlnumRe = regexp.MustCompile(`^[0-9a-zA-Z]{1,128}$`)
	// sessionId: [0-9a-zA-Z._:-]+
	bedrockPassthroughSessionRe = regexp.MustCompile(`^[0-9a-zA-Z._:-]{1,128}$`)
	// guardrailVersion: ([1-9][0-9]{0,7})|DRAFT
	bedrockPassthroughVersionRe = regexp.MustCompile(`^(DRAFT|[1-9][0-9]{0,7})$`)
)

func isBedrockPassthroughAlnum(s string) bool { return bedrockPassthroughAlnumRe.MatchString(s) }

// A session id may contain dots, and a pattern that admits dots also admits "..", so a segment made
// only of dots is refused explicitly: it must never be able to climb out of the operation's path.
func isBedrockPassthroughSessionID(s string) bool {
	return bedrockPassthroughSessionRe.MatchString(s) && strings.Trim(s, ".") != ""
}

func isBedrockPassthroughGuardrailVersion(s string) bool {
	return bedrockPassthroughVersionRe.MatchString(s)
}

// The passthrough is deliberately not a general AWS proxy: it forwards exactly the Bedrock
// operations Bifrost has no native route for, to the service host derived from the key's region.
// Anything else is refused before a request is built. Knowledge-base and guardrail ARNs are not
// accepted in place of an id: an ARN contains "/", which would change the shape of the path.
var bedrockPassthroughRoutes = []bedrockPassthroughRoute{
	{operation: "InvokeAgent", service: bedrockServiceAgentRuntime, streaming: true, segments: []bedrockPassthroughSegment{literal("agents"), param(isBedrockPassthroughAlnum), literal("agentAliases"), param(isBedrockPassthroughAlnum), literal("sessions"), param(isBedrockPassthroughSessionID), literal("text")}},
	{operation: "Retrieve", service: bedrockServiceAgentRuntime, segments: []bedrockPassthroughSegment{literal("knowledgebases"), param(isBedrockPassthroughAlnum), literal("retrieve")}},
	{operation: "ApplyGuardrail", service: bedrockServiceRuntime, segments: []bedrockPassthroughSegment{literal("guardrail"), param(isBedrockPassthroughAlnum), literal("version"), param(isBedrockPassthroughGuardrailVersion), literal("apply")}},
}

// matchBedrockPassthroughRoute returns the allow-listed route for a method and stripped path.
func matchBedrockPassthroughRoute(method, path string) (bedrockPassthroughRoute, bool) {
	if method != http.MethodPost || path == "" {
		return bedrockPassthroughRoute{}, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, route := range bedrockPassthroughRoutes {
		if len(parts) != len(route.segments) {
			continue
		}
		matched := true
		for i, seg := range route.segments {
			if seg.validate != nil {
				if !seg.validate(parts[i]) {
					matched = false
					break
				}
				continue
			}
			if parts[i] != seg.literal {
				matched = false
				break
			}
		}
		if matched {
			return route, true
		}
	}
	return bedrockPassthroughRoute{}, false
}

// bedrockPassthroughMaxResponseBytes bounds a response the unary passthrough will buffer. A package
// variable so tests can lower it; the streaming passthrough never buffers a body.
var bedrockPassthroughMaxResponseBytes int64 = 64 << 20

// hasBedrockSigningCredentials reports whether a key carries AWS credentials it can SigV4-sign with:
// an access key and secret key, or a role to assume.
func hasBedrockSigningCredentials(cfg *schemas.BedrockKeyConfig) bool {
	if cfg == nil {
		return false
	}
	if cfg.AccessKey.GetValue() != "" && cfg.SecretKey.GetValue() != "" {
		return true
	}
	return cfg.RoleARN != nil && cfg.RoleARN.GetValue() != ""
}

// bedrockPassthroughForwardedHeaders are the only client headers that reach AWS. Credentials,
// Host and x-amz-* names are never forwarded, so the request is authenticated and signed by Bifrost alone.
var bedrockPassthroughForwardedHeaders = []string{"Content-Type", "Accept"}

// IsPassthroughStreamPath reports whether a request is an allow-listed operation whose AWS response is an
// event stream (InvokeAgent). The HTTP router uses it to pick the streaming passthrough for exactly those
// requests: the path and body of an InvokeAgent call carry no "stream" marker of their own.
func IsPassthroughStreamPath(method, path string) bool {
	route, ok := matchBedrockPassthroughRoute(method, path)
	return ok && route.streaming
}

// buildPassthroughRequest checks the request against the allow-list, resolves the service host from the
// key's region (or its endpoint overrides), and authenticates it.
func (provider *BedrockProvider) buildPassthroughRequest(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (*http.Request, bedrockPassthroughRoute, *schemas.BifrostError) {
	route, ok := matchBedrockPassthroughRoute(req.Method, req.Path)
	if !ok {
		return nil, route, providerUtils.NewBifrostBadRequestError("bedrock passthrough only forwards POST InvokeAgent, Retrieve and ApplyGuardrail requests")
	}

	config := key.BedrockKeyConfig
	region := resolveBedrockRegion(ctx, key, "")
	requestURL := fmt.Sprintf("https://%s%s", resolveBedrockHost(bedrockEndpoints(config), route.service, region), req.Path)
	if req.RawQuery != "" {
		requestURL += "?" + req.RawQuery
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, requestURL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, route, providerUtils.NewBifrostOperationError("error creating request", err)
	}

	providerUtils.SetExtraHeadersHTTP(ctx, httpReq, provider.networkConfig.ExtraHeaders, nil)
	for _, name := range bedrockPassthroughForwardedHeaders {
		for k, v := range req.SafeHeaders {
			if strings.EqualFold(k, name) && v != "" {
				httpReq.Header.Set(name, v)
			}
		}
	}
	if httpReq.Header.Get("Content-Type") == "" && len(req.Body) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// A Bedrock API key authenticates a bedrock-runtime operation with a bearer token. AWS documents
	// that API keys cannot be used with Agents for Amazon Bedrock Runtime operations, so InvokeAgent
	// and Retrieve are always SigV4-signed: with the key's AWS credentials when it has them, and the
	// request is refused when the key holds only an API key. A key with neither (an IAM role from
	// the default credential chain) is signed as well.
	apiKey := key.Value.GetValue()
	if apiKey != "" && route.service == bedrockServiceAgentRuntime && !hasBedrockSigningCredentials(config) {
		return nil, route, providerUtils.NewBifrostBadRequestError("bedrock " + route.operation + " cannot be called with a Bedrock API key; select a key that has AWS credentials (an access key and secret key, or a role)")
	}
	if apiKey != "" && route.service != bedrockServiceAgentRuntime {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	} else if bifrostErr := signAWSRequest(ctx, httpReq, config, region, bedrockSigningService); bifrostErr != nil {
		return nil, route, bifrostErr
	}
	return httpReq, route, nil
}

// passthroughTransportError maps a failure to reach AWS onto the error the caller sees.
func passthroughTransportError(err error, latency time.Duration) *schemas.BifrostError {
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return providerUtils.SetErrorLatency(&schemas.BifrostError{
			Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err},
		}, latency)
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
	}
	return providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err), latency)
}

// Passthrough forwards InvokeAgent, knowledge-base Retrieve and ApplyGuardrail requests to AWS,
// signed with the key's credentials, and returns the upstream status, headers and body verbatim.
//
// This is the buffered entry point. The HTTP router sends InvokeAgent to PassthroughStream, so it is
// forwarded as AWS produces it; an SDK caller that uses Passthrough for InvokeAgent still gets a
// bounded read: the streaming client (no whole-response timeout, so a long agent run is not cut off),
// the idle timeout, and a cap on the buffered body. Retrieve and ApplyGuardrail are plain
// request/response operations and keep the unary client's timeout.
func (provider *BedrockProvider) Passthrough(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.PassthroughRequest); err != nil {
		return nil, err
	}
	httpReq, route, bifrostErr := provider.buildPassthroughRequest(ctx, key, req)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client := provider.client
	if route.streaming {
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
	if route.streaming {
		providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
		idleReader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()
		reader = idleReader
	}
	body, err := io.ReadAll(io.LimitReader(reader, bedrockPassthroughMaxResponseBytes+1))
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError("error reading response body", err), latency)
	}
	if int64(len(body)) > bedrockPassthroughMaxResponseBytes {
		tooLarge := providerUtils.NewBifrostOperationError(fmt.Sprintf("bedrock passthrough response is too large to buffer (limit %d bytes); use the streaming passthrough", bedrockPassthroughMaxResponseBytes), nil)
		tooLarge.StatusCode = schemas.Ptr(http.StatusBadGateway)
		return nil, providerUtils.SetErrorLatency(tooLarge, latency)
	}

	return &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         req.Path,
		},
	}, nil
}

// PassthroughStream forwards an allow-listed operation and relays the AWS response to the caller as it
// arrives, byte for byte. It uses the streaming client, which has no whole-response timeout, so only
// a connection that goes quiet for the idle timeout ends a long agent run. Nothing is buffered.
func (provider *BedrockProvider) PassthroughStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.PassthroughStreamRequest); err != nil {
		return nil, err
	}
	httpReq, _, bifrostErr := provider.buildPassthroughRequest(ctx, key, req)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.streamingClient, httpReq)
	if err != nil {
		return nil, passthroughTransportError(err, time.Since(startTime))
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeadersFromHTTP(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	return providerUtils.StreamPassthroughHTTP(ctx, postHookRunner, postHookSpanFinalizer, resp.Body, providerUtils.PassthroughStreamParams{
		StatusCode:       resp.StatusCode,
		Headers:          headers,
		Path:             req.Path,
		RawRequest:       req.Body,
		CancellationBody: req.Body,
		StartTime:        startTime,
		// An AWS event stream is binary, not SSE: there is no usage to observe and nothing to frame.
		SkipFraming: true,
		Logger:      provider.logger,
	}), nil
}
