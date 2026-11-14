package elevenlabs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

const (
	passthroughSpeechPrefix       = "/v1/text-to-speech/"
	passthroughDialogueStreamPath = "/v1/text-to-dialogue/stream"
)

// PassthroughSpeechPath reports whether an ElevenLabs request generates speech, which is billed by the
// characters it sends, and whether its response streams. Only POST generates, on the text-to-speech
// endpoints, which carry a voice in the path.
func PassthroughSpeechPath(method, path string) (generates, streams bool) {
	if method != http.MethodPost {
		return false, false
	}
	rest, ok := strings.CutPrefix(path, passthroughSpeechPrefix)
	if !ok {
		return false, false
	}
	voice, tail, _ := strings.Cut(rest, "/")
	if voice == "" {
		return false, false
	}
	switch tail {
	case "", "with-timestamps":
		return true, false
	case "stream", "stream/with-timestamps":
		return true, true
	}
	return false, false
}

// IsPassthroughStreamPath reports whether a passthrough request is answered as a stream.
func IsPassthroughStreamPath(method, path string) bool {
	if _, streams := PassthroughSpeechPath(method, path); streams {
		return true
	}
	// Text to dialogue streams too, but it is not billed from the request, so the speech paths do not name it.
	return method == http.MethodPost && (path == passthroughDialogueStreamPath || path == passthroughDialogueStreamPath+"/with-timestamps")
}

// ExtractPassthroughUsage reads the characters a text-to-speech request sends from the text of its body. It is
// nil for any other request, and for a body that sends no text.
func ExtractPassthroughUsage(method, path string, reqBody []byte) *schemas.BifrostPassthroughUsage {
	if generates, _ := PassthroughSpeechPath(method, path); !generates {
		return nil
	}
	text := providerUtils.GetJSONField(reqBody, "text")
	if text.Type != gjson.String || text.Str == "" {
		return nil
	}
	return &schemas.BifrostPassthroughUsage{AudioInputChars: utf8.RuneCountInString(text.Str)}
}

// preparePassthroughRequest addresses the request to the key's base URL and sets the key's credential in
// place of the client's.
func (provider *ElevenlabsProvider) preparePassthroughRequest(req *schemas.BifrostPassthroughRequest, key schemas.Key, ctx *schemas.BifrostContext, fasthttpReq *fasthttp.Request) *schemas.BifrostError {
	url, err := providerUtils.BuildPassthroughURL(provider.networkConfig.BaseURL, req.Path, req.RawQuery)
	if err != nil {
		return providerUtils.NewBifrostBadRequestError(err.Error())
	}
	fasthttpReq.Header.SetMethod(req.Method)
	fasthttpReq.SetRequestURI(url)

	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)
	for k, v := range req.SafeHeaders {
		fasthttpReq.Header.Set(k, v)
	}
	// The client's credential never goes upstream: the key selected for the request does.
	fasthttpReq.Header.Del("xi-api-key")
	if key.Value.GetValue() != "" {
		fasthttpReq.Header.Set("xi-api-key", key.Value.GetValue())
	}
	fasthttpReq.SetBody(req.Body)
	return nil
}

// Passthrough forwards a raw request to ElevenLabs on the key's base URL and returns its answer as it came,
// error bodies included, so a client's SDK parses ElevenLabs' own errors. The characters a speech request
// sends are reported as its usage.
func (provider *ElevenlabsProvider) Passthrough(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.PassthroughRequest); err != nil {
		return nil, err
	}

	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(fasthttpReq)
	defer fasthttp.ReleaseResponse(resp)

	if bifrostErr := provider.preparePassthroughRequest(req, key, ctx, fasthttpReq); bifrostErr != nil {
		return nil, bifrostErr
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, fasthttpReq, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	bifrostResponse := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         req.Path,
		},
	}
	if resp.StatusCode() >= 200 && resp.StatusCode() < 300 {
		bifrostResponse.PassthroughUsage = ExtractPassthroughUsage(req.Method, req.Path, req.Body)
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = string(req.Body)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = string(body)
	}
	return bifrostResponse, nil
}

// PassthroughStream is Passthrough for the streaming speech endpoints. The audio, or the JSON lines of the
// with-timestamps stream, is forwarded as it arrives and never parsed; the usage rides on the final chunk.
func (provider *ElevenlabsProvider) PassthroughStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.PassthroughStreamRequest); err != nil {
		return nil, err
	}

	startTime := time.Now()
	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(fasthttpReq)

	if bifrostErr := provider.preparePassthroughRequest(req, key, ctx, fasthttpReq); bifrostErr != nil {
		fasthttp.ReleaseResponse(resp)
		return nil, bifrostErr
	}

	err := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, fasthttpReq, resp)
	latency := time.Since(startTime)
	if err != nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), latency)
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.NewBifrostOperationError("provider returned an empty stream body", fmt.Errorf("provider returned an empty stream body"))
	}

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	usage := ExtractPassthroughUsage(req.Method, req.Path, req.Body)
	return providerUtils.StreamPassthrough(
		ctx, postHookRunner, postHookSpanFinalizer, resp, bodyStream,
		providerUtils.PassthroughStreamParams{
			StatusCode:       resp.StatusCode(),
			Headers:          headers,
			Path:             req.Path,
			RawRequest:       req.Body,
			CancellationBody: req.Body,
			StartTime:        startTime,
			Logger:           provider.logger,
			// Audio has no frames to read usage from; the request's own text is what is billed.
			SkipFraming: true,
			Finalize: func(final *schemas.BifrostPassthroughResponse) {
				final.PassthroughUsage = usage
			},
		},
	), nil
}
