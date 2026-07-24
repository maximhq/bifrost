package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func buildResponsesRetrieveQuery(req *schemas.BifrostResponsesRetrieveRequest) string {
	if req == nil {
		return ""
	}
	v := url.Values{}
	for _, inc := range req.Include {
		if inc != "" {
			v.Add("include", inc)
		}
	}
	if req.StartingAfter != nil {
		v.Set("starting_after", strconv.Itoa(*req.StartingAfter))
	}
	if req.IncludeObfuscation != nil {
		v.Set("include_obfuscation", strconv.FormatBool(*req.IncludeObfuscation))
	}
	if req.Stream != nil {
		v.Set("stream", strconv.FormatBool(*req.Stream))
	}
	return v.Encode()
}

func buildResponsesInputItemsQuery(req *schemas.BifrostResponsesInputItemsRequest) string {
	if req == nil {
		return ""
	}
	v := url.Values{}
	if req.After != "" {
		v.Set("after", req.After)
	}
	for _, inc := range req.Include {
		if inc != "" {
			v.Add("include", inc)
		}
	}
	if req.Limit != nil {
		v.Set("limit", strconv.Itoa(*req.Limit))
	}
	if req.Order != "" {
		v.Set("order", req.Order)
	}
	return v.Encode()
}

// executeResponsesLifecycleUnary performs a unary HTTP call for Responses lifecycle endpoints.
func (provider *OpenAIProvider) executeResponsesLifecycleUnary(
	ctx *schemas.BifrostContext,
	method string,
	relativePath string,
	requestType schemas.RequestType,
	rawQuery string,
	key schemas.Key,
	body []byte,
) ([]byte, int64, map[string]string, *schemas.BifrostError) {
	effectiveBody := body
	fullURL := provider.buildRequestURL(ctx, relativePath, requestType)
	if rawQuery != "" {
		fullURL = fullURL + "?" + rawQuery
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	// Lifecycle JSON is always consumed in-process (no transport streaming). Skip
	// PrepareResponseStreaming so large-response threshold mode never leaves the body
	// on a stream-only path that finalizeOpenAIResponse would treat as unsupported here.
	activeClient := provider.client
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(fullURL)
	req.Header.SetMethod(method)
	req.Header.SetContentType("application/json")
	if len(body) > 0 {
		req.SetBody(body)
	} else if method == http.MethodPost {
		effectiveBody = []byte("{}")
		req.SetBody(effectiveBody)
	}

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, 0, nil, providerUtils.EnrichError(ctx, bifrostErr, effectiveBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, 0, providerResponseHeaders, providerUtils.EnrichError(ctx, ParseOpenAIError(resp), effectiveBody, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	bodyBytes, lpResult, finalErr := finalizeOpenAIResponse(ctx, resp, latency, provider.GetProviderKey(), provider.logger)
	respOwned = false
	if finalErr != nil {
		return nil, 0, providerResponseHeaders, providerUtils.EnrichError(ctx, finalErr, effectiveBody, nil, sendBackRawRequest, sendBackRawResponse)
	}
	if lpResult != nil {
		return nil, lpResult.Latency, providerResponseHeaders, providerUtils.NewBifrostOperationError(
			schemas.ErrProviderResponseEmpty,
			fmt.Errorf("responses lifecycle does not support large-response streaming mode"),
		)
	}

	return bodyBytes, latency.Milliseconds(), providerResponseHeaders, nil
}

// ResponsesRetrieve implements schemas.ResponsesLifecycleProvider.
func (provider *OpenAIProvider) ResponsesRetrieve(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesRetrieveRequest); err != nil {
		return nil, err
	}
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("response_id is required"))
	}

	path := "/v1/responses/" + url.PathEscape(req.ResponseID)
	bodyBytes, latencyMs, headers, bifrostErr := provider.executeResponsesLifecycleUnary(
		ctx, http.MethodGet, path, schemas.ResponsesRetrieveRequest, buildResponsesRetrieveQuery(req), key, nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostResponsesResponse{}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	_, rawResponse, err := providerUtils.HandleProviderResponse(bodyBytes, response, nil, sendBackRawRequest, sendBackRawResponse)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, nil, bodyBytes, sendBackRawRequest, sendBackRawResponse)
	}
	response.ExtraFields.Latency = latencyMs
	response.ExtraFields.ProviderResponseHeaders = headers
	response.ExtraFields.Provider = provider.GetProviderKey()
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ResponsesRetrieveStream implements schemas.ResponsesLifecycleProvider.
// It replays a stored response as an SSE stream (GET /v1/responses/{id}?stream=true).
// Self-contained SSE loop mirroring HandleOpenAIResponsesStreaming, adapted for a
// bodyless GET; the create-stream path is intentionally left untouched.
func (provider *OpenAIProvider) ResponsesRetrieveStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesRetrieveStreamRequest); err != nil {
		return nil, err
	}
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("response_id is required"))
	}
	// This method is only reached for streamed retrieval; force the stream query param.
	req.Stream = schemas.Ptr(true)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	fullURL := provider.buildRequestURL(ctx, "/v1/responses/"+url.PathEscape(req.ResponseID), schemas.ResponsesRetrieveStreamRequest)
	if rawQuery := buildResponsesRetrieveQuery(req); rawQuery != "" {
		fullURL = fullURL + "?" + rawQuery
	}

	httpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(httpReq)

	httpReq.Header.SetMethod(http.MethodGet)
	httpReq.SetRequestURI(fullURL)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	providerUtils.SetExtraHeaders(ctx, httpReq, provider.networkConfig.ExtraHeaders, nil)
	for k, v := range BearerAuthHeader(key) {
		httpReq.Header.Set(k, v)
	}

	// Use streaming-aware client when large payload optimization is active.
	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.streamingClient, resp)

	startTime := time.Now()
	err := providerUtils.DoStreamingRequest(ctx, activeClient, httpReq, resp)
	latency := time.Since(startTime)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, nil, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), nil, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), nil, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Store provider response headers before status check so error responses also forward them.
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp), nil, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Large payload streaming passthrough — pipe raw upstream SSE to client.
	if providerUtils.SetupStreamingPassthrough(ctx, resp) {
		responseChan := make(chan *schemas.BifrostStreamChunk)
		providerUtils.CloseStream(ctx, responseChan)
		return responseChan, nil
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, nil)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, nil)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		// Decompress gzip-encoded streams transparently (no-op for non-gzip).
		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		// Wrap reader with idle timeout to detect stalled streams.
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Close the raw network stream on ctx cancellation to unblock in-progress reads.
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		reader, drained := providerUtils.DrainNonSSEStreamReader(resp, reader)
		if drained {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, errors.New("provider returned non-SSE response for streaming request"), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}

		sseReader := providerUtils.GetSSEDataReader(ctx, reader)
		lastChunkTime := startTime

		for {
			if ctx.Err() != nil {
				return
			}
			data, readErr := sseReader.ReadDataLine()
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					provider.logger.Warn("Error reading stream: %v", readErr)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
				}
				break
			}
			jsonData := string(data)

			var response schemas.BifrostResponsesStreamResponse
			if err := sonic.UnmarshalString(jsonData, &response); err != nil {
				provider.logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			if sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.Type == schemas.ResponsesStreamResponseTypeError {
				bifrostErr := &schemas.BifrostError{
					Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
					IsBifrostError: false,
					Error:          &schemas.ErrorField{},
				}
				if response.Message != nil {
					bifrostErr.Error.Message = *response.Message
				}
				if response.Param != nil {
					bifrostErr.Error.Param = *response.Param
				}
				if response.Code != nil {
					bifrostErr.Error.Code = response.Code
				}
				if response.Error != nil {
					if response.Error.Message != "" && bifrostErr.Error.Message == "" {
						bifrostErr.Error.Message = response.Error.Message
					}
					if response.Error.Code != "" && (bifrostErr.Error.Code == nil || *bifrostErr.Error.Code == "") {
						bifrostErr.Error.Code = &response.Error.Code
					}
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, nil, []byte(jsonData), sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			// Some providers send response.failed on HTTP 200 streams instead of a pre-stream 4xx.
			if response.Type == schemas.ResponsesStreamResponseTypeFailed {
				bifrostErr := &schemas.BifrostError{
					Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeFailed)),
					IsBifrostError: false,
					Error:          &schemas.ErrorField{},
				}
				if response.Response != nil && response.Response.Error != nil {
					bifrostErr.Error.Message = response.Response.Error.Message
					bifrostErr.Error.Code = &response.Response.Error.Code
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, nil, []byte(jsonData), sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			response.ExtraFields.ChunkIndex = response.SequenceNumber
			if response.Type == schemas.ResponsesStreamResponseTypeCompleted || response.Type == schemas.ResponsesStreamResponseTypeIncomplete {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil, nil), responseChan, postHookSpanFinalizer)
				return
			}

			response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
			lastChunkTime = time.Now()

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil, nil), responseChan, postHookSpanFinalizer)
		}
	}()

	return responseChan, nil
}

// ResponsesDelete implements schemas.ResponsesLifecycleProvider.
func (provider *OpenAIProvider) ResponsesDelete(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesDeleteRequest) (*schemas.BifrostResponsesDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesDeleteRequest); err != nil {
		return nil, err
	}
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("response_id is required"))
	}

	path := "/v1/responses/" + url.PathEscape(req.ResponseID)
	bodyBytes, latencyMs, headers, bifrostErr := provider.executeResponsesLifecycleUnary(
		ctx, http.MethodDelete, path, schemas.ResponsesDeleteRequest, "", key, nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostResponsesDeleteResponse{}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	_, rawResponse, err := providerUtils.HandleProviderResponse(bodyBytes, response, nil, sendBackRawRequest, sendBackRawResponse)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, nil, bodyBytes, sendBackRawRequest, sendBackRawResponse)
	}
	response.ExtraFields.Latency = latencyMs
	response.ExtraFields.ProviderResponseHeaders = headers
	response.ExtraFields.Provider = provider.GetProviderKey()
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ResponsesCancel implements schemas.ResponsesLifecycleProvider.
func (provider *OpenAIProvider) ResponsesCancel(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesCancelRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesCancelRequest); err != nil {
		return nil, err
	}
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("response_id is required"))
	}

	path := "/v1/responses/" + url.PathEscape(req.ResponseID) + "/cancel"
	bodyBytes, latencyMs, headers, bifrostErr := provider.executeResponsesLifecycleUnary(
		ctx, http.MethodPost, path, schemas.ResponsesCancelRequest, "", key, nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostResponsesResponse{}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	cancelBody := []byte("{}")
	rawRequest, rawResponse, err := providerUtils.HandleProviderResponse(bodyBytes, response, cancelBody, sendBackRawRequest, sendBackRawResponse)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, cancelBody, bodyBytes, sendBackRawRequest, sendBackRawResponse)
	}
	response.ExtraFields.Latency = latencyMs
	response.ExtraFields.ProviderResponseHeaders = headers
	response.ExtraFields.Provider = provider.GetProviderKey()
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ResponsesInputItems implements schemas.ResponsesLifecycleProvider.
func (provider *OpenAIProvider) ResponsesInputItems(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesInputItemsRequest) (*schemas.BifrostResponsesInputItemsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesInputItemsRequest); err != nil {
		return nil, err
	}
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, fmt.Errorf("response_id is required"))
	}

	path := "/v1/responses/" + url.PathEscape(req.ResponseID) + "/input_items"
	bodyBytes, latencyMs, headers, bifrostErr := provider.executeResponsesLifecycleUnary(
		ctx, http.MethodGet, path, schemas.ResponsesInputItemsRequest, buildResponsesInputItemsQuery(req), key, nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostResponsesInputItemsResponse{}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	_, rawResponse, err := providerUtils.HandleProviderResponse(bodyBytes, response, nil, sendBackRawRequest, sendBackRawResponse)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, nil, bodyBytes, sendBackRawRequest, sendBackRawResponse)
	}
	response.ExtraFields.Latency = latencyMs
	response.ExtraFields.ProviderResponseHeaders = headers
	response.ExtraFields.Provider = provider.GetProviderKey()
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}
