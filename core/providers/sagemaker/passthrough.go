package sagemaker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	invocationsOp            = "invocations"
	invocationsStreamOp      = "invocations-response-stream"
	inferenceComponentHeader = "X-Amzn-SageMaker-Inference-Component"
	streamContentTypeHeader  = "X-Amzn-SageMaker-Content-Type"
)

// passthroughForwardedHeaders are the only client headers that reach SageMaker. Credentials, Host and
// other x-amz-* names are never forwarded, so the request is authenticated and signed by Bifrost alone.
var passthroughForwardedHeaders = []string{
	"Content-Type",
	"Accept",
	"X-Amzn-SageMaker-Custom-Attributes",
	"X-Amzn-SageMaker-Target-Model",
	"X-Amzn-SageMaker-Target-Variant",
	"X-Amzn-SageMaker-Target-Container-Hostname",
	"X-Amzn-SageMaker-Inference-Id",
	"X-Amzn-SageMaker-Enable-Explanations",
	"X-Amzn-SageMaker-Session-Id",
}

// matchPassthroughRoute accepts only POST InvokeEndpoint and InvokeEndpointWithResponseStream paths.
func matchPassthroughRoute(method, path string) (passthroughRoute, bool) {
	if method != http.MethodPost {
		return passthroughRoute{}, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var route passthroughRoute
	switch {
	case len(parts) == 3 && parts[0] == "endpoints":
		route.id.endpoint = parts[1]
	case len(parts) == 5 && parts[0] == "endpoints" && parts[2] == "inference-components":
		route.id.endpoint, route.id.ic = parts[1], parts[3]
		if !isResourceName(route.id.ic) {
			return passthroughRoute{}, false
		}
	default:
		return passthroughRoute{}, false
	}
	if !isResourceName(route.id.endpoint) {
		return passthroughRoute{}, false
	}
	switch parts[len(parts)-1] {
	case invocationsOp:
	case invocationsStreamOp:
		route.streaming = true
	default:
		return passthroughRoute{}, false
	}
	return route, true
}

// IsPassthroughStreamPath reports whether a passthrough request is InvokeEndpointWithResponseStream.
func IsPassthroughStreamPath(method, path string) bool {
	route, ok := matchPassthroughRoute(method, path)
	return ok && route.streaming
}

// PassthroughModel returns the model id a passthrough path addresses ("endpoint" or
// "endpoint/inference-component"), the same shape keys and virtual keys allow; "" when the path is not a passthrough route.
func PassthroughModel(path string) string {
	route, ok := matchPassthroughRoute(http.MethodPost, path)
	if !ok {
		return ""
	}
	if route.id.ic != "" {
		return route.id.endpoint + "/" + route.id.ic
	}
	return route.id.endpoint
}

// buildPassthroughRequest validates the route and fills a SigV4-signed request to the runtime host.
// An inference component travels in a header: /invocations has no inference-component path form.
func (provider *SageMakerProvider) buildPassthroughRequest(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest, fasthttpReq *fasthttp.Request) (passthroughRoute, *schemas.BifrostError) {
	route, ok := matchPassthroughRoute(req.Method, req.Path)
	if !ok {
		return route, providerUtils.NewBifrostBadRequestError("sagemaker passthrough only forwards POST /endpoints/{endpoint}[/inference-components/{component}]/invocations and /invocations-response-stream")
	}
	if key.Value.GetValue() != "" {
		return route, providerUtils.NewBifrostBadRequestError("sagemaker passthrough needs AWS credentials to sign the request; a bearer token only works on the OpenAI-compatible route")
	}
	region, bErr := resolveRegion(ctx, key, modelID{})
	if bErr != nil {
		return route, bErr
	}
	creds, bErr := provider.awsCredentials(ctx, key, region)
	if bErr != nil {
		return route, bErr
	}

	op := invocationsOp
	if route.streaming {
		op = invocationsStreamOp
	}
	url := "https://" + runtimeHost(key.SageMakerKeyConfig, region) + "/endpoints/" + route.id.endpoint + "/" + op
	if req.RawQuery != "" {
		url += "?" + req.RawQuery
	}

	headers := map[string]string{}
	for _, name := range passthroughForwardedHeaders {
		for k, v := range req.SafeHeaders {
			if strings.EqualFold(k, name) && v != "" {
				headers[name] = v
			}
		}
	}
	if headers["Content-Type"] == "" && len(req.Body) > 0 {
		headers["Content-Type"] = "application/json"
	}
	if route.id.ic != "" {
		headers[inferenceComponentHeader] = route.id.ic
	}
	signed, bErr := signRequest(ctx, creds, region, http.MethodPost, url, req.Body, headers)
	if bErr != nil {
		return route, bErr
	}

	fasthttpReq.Header.SetMethod(http.MethodPost)
	fasthttpReq.SetRequestURI(url)
	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)
	for name, values := range signed {
		fasthttpReq.Header.Set(name, values[0])
	}
	fasthttpReq.SetBody(req.Body)
	return route, nil
}

// Passthrough forwards InvokeEndpoint to the runtime host and returns the container's status, headers and body verbatim.
func (provider *SageMakerProvider) Passthrough(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(fasthttpReq)
	defer fasthttp.ReleaseResponse(resp)

	route, bErr := provider.buildPassthroughRequest(ctx, key, req, fasthttpReq)
	if bErr != nil {
		return nil, bErr
	}
	if route.streaming {
		return nil, providerUtils.NewBifrostBadRequestError("invocations-response-stream is a streaming operation; use the streaming passthrough")
	}

	latency, bErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, fasthttpReq, resp)
	defer wait()
	if bErr != nil {
		return nil, bErr
	}
	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode response body", err)
	}
	return &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       append([]byte(nil), body...),
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         req.Path,
		},
	}, nil
}

// PassthroughStream forwards InvokeEndpointWithResponseStream and relays the container's own bytes
// (SSE, JSON lines, ...) as they arrive: the AWS event-stream framing is unpacked, so any HTTP client can read it.
func (provider *SageMakerProvider) PassthroughStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	fasthttpReq := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(fasthttpReq)
	route, bErr := provider.buildPassthroughRequest(ctx, key, req, fasthttpReq)
	if bErr != nil {
		return nil, bErr
	}
	if !route.streaming {
		return nil, providerUtils.NewBifrostBadRequestError("invocations is not a streaming operation; use the non-streaming passthrough")
	}

	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.streamingClient, resp)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	startTime := time.Now()
	if err := providerUtils.DoStreamingRequest(ctx, activeClient, fasthttpReq, resp); err != nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		latency := time.Since(startTime)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err},
			}, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), latency)
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	rawBodyStream := resp.BodyStream()
	if rawBodyStream == nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.NewBifrostOperationError("provider returned an empty stream body", nil)
	}
	// A failed call answers with a plain JSON error, not an event stream; relay that as is.
	body := rawBodyStream
	if resp.StatusCode() == fasthttp.StatusOK {
		body = &eventStreamPayloadReader{src: rawBodyStream}
		for k, v := range headers {
			if strings.EqualFold(k, streamContentTypeHeader) && v != "" {
				headers["Content-Type"] = v
			}
		}
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	return providerUtils.StreamPassthrough(ctx, postHookRunner, postHookSpanFinalizer, resp, body, providerUtils.PassthroughStreamParams{
		StatusCode:       resp.StatusCode(),
		Headers:          headers,
		Path:             req.Path,
		RawRequest:       req.Body,
		CancellationBody: req.Body,
		StartTime:        startTime,
		SkipFraming:      true, // the container's format is unknown, so there is no usage to observe
		Logger:           provider.logger,
	}), nil
}

func (r *eventStreamPayloadReader) Read(p []byte) (int, error) {
	if r.decoder == nil {
		r.decoder = eventstream.NewDecoder()
	}
	for len(r.pending) == 0 {
		msg, err := r.decoder.Decode(r.src, nil)
		if err != nil {
			return 0, err
		}
		if msgType := msg.Headers.Get(":message-type"); msgType != nil && msgType.String() != "event" {
			name := "stream error"
			if v := msg.Headers.Get(":exception-type"); v != nil {
				name = v.String()
			}
			return 0, fmt.Errorf("sagemaker %s: %s", name, msg.Payload)
		}
		if v := msg.Headers.Get(":event-type"); v != nil && v.String() == "PayloadPart" {
			r.pending = msg.Payload
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// CloseWithError closes the underlying fasthttp stream (which has no Close) so cancellation and the
// idle timer unblock a pending read.
func (r *eventStreamPayloadReader) CloseWithError(err error) error {
	if c, ok := r.src.(interface{ CloseWithError(error) error }); ok {
		return c.CloseWithError(err)
	}
	if c, ok := r.src.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// awsCredentials resolves the key's AWS credentials once per credential tuple and region, and
// reuses them until shortly before they expire.
func (provider *SageMakerProvider) awsCredentials(ctx *schemas.BifrostContext, key schemas.Key, region string) (aws.Credentials, *schemas.BifrostError) {
	entry, bErr := provider.creds.Get(ctx, "creds:"+tokenCacheKey(key.SageMakerKeyConfig, region), func(mintCtx context.Context, _ *tokencache.Entry[aws.Credentials]) (*tokencache.Entry[aws.Credentials], *schemas.BifrostError) {
		bctx := schemas.NewBifrostContext(mintCtx, schemas.NoDeadline)
		defer bctx.Cancel()
		creds, bErr := bedrock.ResolveAWSCredentials(bctx, bedrockKeyConfig(key.SageMakerKeyConfig), region)
		if bErr != nil {
			return nil, bErr
		}
		now := time.Now()
		expires := now.Add(defaultTokenTTL)
		if creds.CanExpire && creds.Expires.Before(expires) {
			expires = creds.Expires
		}
		return &tokencache.Entry[aws.Credentials]{Value: creds, ExpiresAt: expires, RefreshAt: expires.Add(-credsExpiryMargin)}, nil
	})
	if bErr != nil {
		return aws.Credentials{}, bErr
	}
	return entry.Value, nil
}

// signRequest SigV4-signs a request for the sagemaker service and returns the headers to send.
func signRequest(ctx context.Context, creds aws.Credentials, region, method, url string, body []byte, headers map[string]string) (http.Header, *schemas.BifrostError) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to build sagemaker request", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hash := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(hash[:]), "sagemaker", region, time.Now()); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to sign sagemaker request", err)
	}
	return req.Header, nil
}
