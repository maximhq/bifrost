package gemini

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// GeminiInteractionsTarget is where and how a provider sends Interactions calls. Gemini and Vertex
// differ only in the collection URL and authentication, so both build one of these and share the
// calls below.
type GeminiInteractionsTarget struct {
	CollectionURL              string            // ".../interactions", no trailing slash or query
	AuthQuery                  string            // appended to every URL when set (e.g. "key=...")
	Headers                    map[string]string // authentication and fixed headers
	ExtraHeaders               map[string]schemas.SecretVar
	Client                     *fasthttp.Client
	StreamingClient            *fasthttp.Client
	StreamIdleTimeoutInSeconds int
	ProviderName               schemas.ModelProvider
	SendBackRawRequest         bool
	SendBackRawResponse        bool
	Logger                     schemas.Logger
	PollAPIVersion             string // version segment job accounting's passthrough poll reads the run under ("" for Gemini, whose base URL carries it)
}

// interactionJob names the stored run an Interactions response reports on, for job accounting to
// settle once. created marks the request that started the run.
func (t *GeminiInteractionsTarget) interactionJob(resp *schemas.BifrostResponsesResponse, status string, usage *GeminiInteractionsUsage, serviceTier *string, created bool) *schemas.BifrostPassthroughJob {
	if resp == nil || resp.ID == nil || *resp.ID == "" {
		return nil
	}
	info := InteractionsPassthroughInfo{ID: *resp.ID, Status: status}
	if usage != nil {
		info.Usage = interactionsUsage(&geminiInteractionsUsage{
			TotalTokens:        usage.TotalTokens,
			InputTokens:        usage.TotalInputTokens,
			OutputTokens:       usage.TotalOutputTokens,
			ThoughtTokens:      usage.TotalThoughtTokens,
			CachedTokens:       usage.TotalCachedTokens,
			ToolUseTokens:      usage.TotalToolUseTokens,
			GroundingToolCount: usage.GroundingToolCount,
		}, serviceTier, resp.Model)
	}
	return &schemas.BifrostPassthroughJob{
		ID:         info.ID,
		Status:     status,
		Terminal:   info.terminal(),
		Created:    created,
		PollPath:   geminiInteractionsPath + "/" + info.ID,
		APIVersion: t.PollAPIVersion,
		Usage:      info.Usage,
	}
}

func (t *GeminiInteractionsTarget) url(suffix string, query url.Values) string {
	u := t.CollectionURL + suffix
	q := query.Encode()
	if t.AuthQuery != "" {
		if q != "" {
			q += "&"
		}
		q += t.AuthQuery
	}
	if q != "" {
		u += "?" + q
	}
	return u
}

func (t *GeminiInteractionsTarget) resourceSuffix(id string) (string, *schemas.BifrostError) {
	if isGenerateContentResponseID(id) {
		return "", providerUtils.NewBifrostBadRequestError(fmt.Sprintf("response id %q ran on generateContent, which stores no interaction; only interaction ids can be retrieved, cancelled, deleted or listed", id))
	}
	escaped, err := providerUtils.EscapeResourceID(id, "interaction id")
	if err != nil {
		return "", err
	}
	return "/" + escaped, nil
}

// buildGeminiInteractionsBody returns the bytes to send for a create: the caller's own body on the
// raw path, with the model or agent field set to the resolved model and Bifrost's own fields
// removed, else the converted request.
func buildGeminiInteractionsBody(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, stream bool) ([]byte, *schemas.BifrostError) {
	// A large payload streams the caller's body past Bifrost unparsed, so the request struct has no
	// input to convert and the body cannot be normalized for this API. Refuse it rather than send
	// Google a request without its input.
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil, providerUtils.NewBifrostBadRequestError("large payload requests are not supported on the interactions API; send a smaller body or use generateContent")
	}
	body, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (*GeminiInteractionsRequest, error) {
			native, err := ToGeminiInteractionsRequest(request)
			if err != nil {
				return nil, err
			}
			if stream {
				native.Stream = schemas.Ptr(true)
			}
			return native, nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if _, raw := providerUtils.CheckAndGetRawRequestBody(ctx, request); !raw || len(body) == 0 {
		return body, nil
	}
	body, bifrostErr = normalizeRawGeminiInteractionsBody(body, request.Model)
	if bifrostErr != nil || !stream {
		return body, bifrostErr
	}
	// Google picks SSE from the body's stream field, which a raw caller may have left out.
	streamed, err := providerUtils.SetRawJSONField(body, "stream", []byte("true"))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	return streamed, nil
}

// normalizeRawGeminiInteractionsBody fixes up a caller's Interactions body for the upstream: the
// model (or agent) loses its provider prefix, and the Bifrost-only fallbacks field is dropped. The
// field follows the resolved target, so an agent request that falls back to a model sends "model"
// (and the reverse sends "agent"); agent_config cannot go to a model. The input is never modified.
func normalizeRawGeminiInteractionsBody(body []byte, model string) ([]byte, *schemas.BifrostError) {
	out := body
	var err error
	field := "model"
	if !providerUtils.JSONFieldExists(body, "model") && providerUtils.JSONFieldExists(body, "agent") {
		field = "agent"
	}
	if model != "" {
		target := "model"
		if IsGeminiInteractionsAgent(model) {
			target = "agent"
		}
		if target != field {
			if target == "model" && providerUtils.JSONFieldExists(body, "agent_config") {
				return nil, providerUtils.NewBifrostBadRequestError("agent_config cannot be sent to the model " + model + "; remove it or fall back to an agent")
			}
			if out, err = providerUtils.DeleteJSONField(out, field); err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
			}
			field = target
		}
		if providerUtils.GetJSONField(out, field).String() != model {
			if out, err = providerUtils.SetJSONField(out, field, model); err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
			}
		}
	}
	if providerUtils.JSONFieldExists(out, "fallbacks") {
		if out, err = providerUtils.DeleteJSONField(out, "fallbacks"); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
		}
	}
	return out, nil
}

// executeGeminiInteractionsUnary sends one non-streaming Interactions call and returns the body
// of a successful response.
func executeGeminiInteractionsUnary(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, method, requestURL string, body []byte) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, t.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(method)
	req.Header.SetContentType("application/json")
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	applyGeminiInteractionsAuthPrecedence(req, t)
	if len(body) > 0 {
		req.SetBody(body)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, t.Client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, nil, providerUtils.EnrichError(ctx, bifrostErr, body, nil, t.SendBackRawRequest, t.SendBackRawResponse, latency)
	}
	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, latency, headers, providerUtils.EnrichError(ctx, parseGeminiDecisionError(resp), body, nil, t.SendBackRawRequest, t.SendBackRawResponse, latency)
	}
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, headers, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), body, nil, t.SendBackRawRequest, t.SendBackRawResponse, latency)
	}
	return append([]byte(nil), respBody...), latency, headers, nil
}

type interactionJobKind int

const (
	jobNone interactionJobKind = iota
	jobRead
	jobCreate
)

// decodeGeminiInteraction parses an Interaction body into a Responses response, with raw request
// and response attached as configured.
//
// job says how the response reports on the stored run: jobNone for none, jobRead for a read or
// cancel (which may advance or settle a run Bifrost saw created), jobCreate for a create. A create
// that finished in the foreground is billed on its own row and reports no run; one still running
// (a background run) is recorded so the sweeper bills it once it ends.
func decodeGeminiInteraction(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, requestBody, respBody []byte, latency time.Duration, headers map[string]string, job interactionJobKind) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	var interaction GeminiInteraction
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &interaction, requestBody, t.SendBackRawRequest, t.SendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, requestBody, respBody, t.SendBackRawRequest, t.SendBackRawResponse, latency)
	}
	resp := interaction.ToBifrostResponsesResponse()
	switch job {
	case jobRead:
		resp.ProviderJob = t.interactionJob(resp, interaction.Status, interaction.Usage, interaction.ServiceTier, false)
	case jobCreate:
		if run := t.interactionJob(resp, interaction.Status, interaction.Usage, interaction.ServiceTier, true); run != nil && !run.Terminal {
			resp.ProviderJob = run
		}
	}
	resp.ExtraFields.Latency = latency.Milliseconds()
	resp.ExtraFields.ProviderResponseHeaders = headers
	if t.SendBackRawRequest && len(requestBody) > 0 {
		resp.ExtraFields.RawRequest = rawRequest
	}
	if t.SendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}
	return resp, nil
}

// InteractionsResponses runs a Responses request on POST /interactions.
func InteractionsResponses(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	body, bifrostErr := buildGeminiInteractionsBody(ctx, request, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	respBody, latency, headers, bifrostErr := executeGeminiInteractionsUnary(ctx, t, http.MethodPost, t.url("", nil), body)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	resp, bifrostErr := decodeGeminiInteraction(ctx, t, body, respBody, latency, headers, jobCreate)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.Model == "" {
		resp.Model = request.Model
	}
	return resp, nil
}

// InteractionsRetrieve reads a stored interaction (GET /interactions/{id}).
func InteractionsRetrieve(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, req *schemas.BifrostResponsesRetrieveRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostBadRequestError("interaction id is required")
	}
	suffix, bifrostErr := t.resourceSuffix(req.ResponseID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	respBody, latency, headers, bifrostErr := executeGeminiInteractionsUnary(ctx, t, http.MethodGet, t.url(suffix, nil), nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return decodeGeminiInteraction(ctx, t, nil, respBody, latency, headers, jobRead)
}

// InteractionsCancel stops a background interaction (POST /interactions/{id}/cancel).
func InteractionsCancel(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, req *schemas.BifrostResponsesCancelRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostBadRequestError("interaction id is required")
	}
	suffix, bifrostErr := t.resourceSuffix(req.ResponseID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	respBody, latency, headers, bifrostErr := executeGeminiInteractionsUnary(ctx, t, http.MethodPost, t.url(suffix+"/cancel", nil), []byte("{}"))
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return decodeGeminiInteraction(ctx, t, nil, respBody, latency, headers, jobRead)
}

// InteractionsDelete deletes a stored interaction (DELETE /interactions/{id}).
func InteractionsDelete(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, req *schemas.BifrostResponsesDeleteRequest) (*schemas.BifrostResponsesDeleteResponse, *schemas.BifrostError) {
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostBadRequestError("interaction id is required")
	}
	suffix, bifrostErr := t.resourceSuffix(req.ResponseID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	respBody, latency, headers, bifrostErr := executeGeminiInteractionsUnary(ctx, t, http.MethodDelete, t.url(suffix, nil), nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	out := &schemas.BifrostResponsesDeleteResponse{ID: req.ResponseID, Object: "response", Deleted: true}
	out.ExtraFields.Latency = latency.Milliseconds()
	out.ExtraFields.ProviderResponseHeaders = headers
	if t.SendBackRawResponse && len(respBody) > 0 {
		out.ExtraFields.RawResponse = string(respBody)
	}
	return out, nil
}

// InteractionsInputItems lists the input side of a stored interaction: the user_input and
// function_result steps a GET returns alongside the model's.
func InteractionsInputItems(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, req *schemas.BifrostResponsesInputItemsRequest) (*schemas.BifrostResponsesInputItemsResponse, *schemas.BifrostError) {
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostBadRequestError("interaction id is required")
	}
	suffix, bifrostErr := t.resourceSuffix(req.ResponseID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	respBody, latency, headers, bifrostErr := executeGeminiInteractionsUnary(ctx, t, http.MethodGet, t.url(suffix, nil), nil)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	var interaction GeminiInteraction
	if _, _, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &interaction, nil, false, false); bifrostErr != nil {
		return nil, bifrostErr
	}
	out := &schemas.BifrostResponsesInputItemsResponse{Object: "list", Data: []schemas.ResponsesMessage{}}
	for idx, step := range interaction.Steps {
		if step.Type != GeminiInteractionsStepUserInput && step.Type != GeminiInteractionsStepFunctionResult {
			continue
		}
		msgs, err := geminiInteractionsStepToResponses(step, true)
		if err != nil {
			continue
		}
		for j := range msgs {
			id := geminiInteractionItemID(interaction.ID, idx)
			msgs[j].ID = &id
		}
		out.Data = append(out.Data, msgs...)
	}
	out.Data, out.HasMore = pageGeminiInteractionsInputItems(out.Data, req.Order, req.After, req.Limit)
	if len(out.Data) > 0 {
		out.FirstID = *out.Data[0].ID
		out.LastID = *out.Data[len(out.Data)-1].ID
	}
	out.ExtraFields.Latency = latency.Milliseconds()
	out.ExtraFields.ProviderResponseHeaders = headers
	return out, nil
}

// InteractionsResponsesStream runs a Responses request as a streamed create on POST
// /interactions.
func InteractionsResponsesStream(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	body, bifrostErr := buildGeminiInteractionsBody(ctx, request, true)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	query := url.Values{"alt": []string{"sse"}}
	// Only a background run may end its stream before it finishes; it is settled from a later read.
	background := providerUtils.GetJSONField(body, "background").Bool()
	return streamGeminiInteractions(ctx, t, http.MethodPost, t.url("", query), body, request.Model, background, postHookRunner, postHookSpanFinalizer)
}

// InteractionsRetrieveStream replays a stored interaction as a stream (GET /interactions/{id}
// ?stream=true), resuming after LastEventID when it is set.
func InteractionsRetrieveStream(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), req *schemas.BifrostResponsesRetrieveRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if req == nil || req.ResponseID == "" {
		return nil, providerUtils.NewBifrostBadRequestError("interaction id is required")
	}
	suffix, bifrostErr := t.resourceSuffix(req.ResponseID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	query := url.Values{"stream": []string{"true"}, "alt": []string{"sse"}}
	if req.LastEventID != nil && *req.LastEventID != "" {
		query.Set("last_event_id", *req.LastEventID)
	}
	return streamGeminiInteractions(ctx, t, http.MethodGet, t.url(suffix, query), nil, "", true, postHookRunner, postHookSpanFinalizer)
}

// streamGeminiInteractions sends one streaming Interactions call and converts its SSE events into
// Responses stream chunks. On the native path (raw responses requested) every upstream event is
// attached to exactly one chunk, and an event with no Responses counterpart rides a raw-only
// carrier chunk, so the route can replay Google's stream byte for byte. allowEarlyEnd is set for a
// background run or a replay of a stored one: their stream may close before the run finishes, and
// the run is closed as it stands. A foreground stream that closes early is an error.
func streamGeminiInteractions(ctx *schemas.BifrostContext, t *GeminiInteractionsTarget, method, requestURL string, body []byte, model string, allowEarlyEnd bool, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, t.StreamIdleTimeoutInSeconds)
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(method)
	req.SetRequestURI(requestURL)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, t.ExtraHeaders, nil)
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	applyGeminiInteractionsAuthPrecedence(req, t)
	if len(body) > 0 {
		req.SetBody(body)
	}

	sendBackRawRequest, sendBackRawResponse, logger := t.SendBackRawRequest, t.SendBackRawResponse, t.Logger
	startTime := time.Now()
	doErr := providerUtils.DoStreamingRequest(ctx, t.StreamingClient, req, resp)
	latency := time.Since(startTime)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(doErr, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error:          &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: doErr},
			}, body, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(doErr, fasthttp.ErrTimeout) || errors.Is(doErr, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, doErr), body, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, doErr), body, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseGeminiDecisionError(resp), body, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, body)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, body)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.RecoverStreamPanic(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer)
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		if resp.BodyStream() == nil {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner,
				providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("Provider returned an empty response", fmt.Errorf("provider returned an empty response")), body, nil, sendBackRawRequest, sendBackRawResponse),
				responseChan, logger, postHookSpanFinalizer)
			return
		}

		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		sseReader := providerUtils.GetSSEDataReader(ctx, bufio.NewReaderSize(reader, 64*1024))
		state := NewGeminiInteractionsStreamState(model)
		streamUsage := &schemas.BifrostLLMUsage{}
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, streamUsage)

		chunkIndex, sequenceNumber := 0, 0
		lastChunkTime := startTime
		send := func(event *schemas.BifrostResponsesStreamResponse, raw []byte, last bool) {
			event.SequenceNumber = sequenceNumber
			event.ExtraFields = schemas.BifrostResponseExtraFields{ChunkIndex: chunkIndex, Latency: time.Since(lastChunkTime).Milliseconds()}
			if sendBackRawResponse && raw != nil {
				event.ExtraFields.RawResponse = string(raw)
			}
			chunkIndex++
			sequenceNumber++
			lastChunkTime = time.Now()
			if last {
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&event.ExtraFields, body)
				}
				event.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, event, nil, nil, nil), responseChan, postHookSpanFinalizer)
		}

		for {
			if ctx.Err() != nil {
				return
			}
			data, readErr := sseReader.ReadDataLine()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				logger.Warn("Error reading interactions stream: %v", readErr)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, logger, postHookSpanFinalizer)
				return
			}
			trimmed := strings.TrimSpace(string(data))
			if trimmed == "" || trimmed == "[DONE]" {
				continue
			}
			parseStart := time.Now()
			event, err := ParseGeminiInteractionsEvent(data)
			schemas.AddStreamParse(ctx, time.Since(parseStart))
			if err != nil {
				logger.Warn("Failed to parse interactions event: %v", err)
				continue
			}
			convStart := time.Now()
			events, streamErr := state.ToBifrostResponsesStream(event)
			schemas.AddStreamConvert(ctx, time.Since(convStart))
			if state.Usage != nil {
				if usage := state.Usage.toResponsesUsage(state.Model); usage != nil {
					*streamUsage = *usage.ToBifrostLLMUsage()
				}
			}
			if streamErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, streamErr, body, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger, postHookSpanFinalizer)
				return
			}
			raw := data
			if len(events) == 0 {
				if sendBackRawResponse {
					// An event with no Responses counterpart still reaches a native caller.
					send(&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeProviderRawEvent}, raw, false)
				}
				continue
			}
			rawIdx := providerUtils.ResponsesStreamRawIndex(events, 0)
			for i, ev := range events {
				var attach []byte
				if i == rawIdx {
					attach = raw
				}
				terminal := ev.Type == schemas.ResponsesStreamResponseTypeCompleted || ev.Type == schemas.ResponsesStreamResponseTypeFailed || ev.Type == schemas.ResponsesStreamResponseTypeIncomplete
				send(ev, attach, terminal)
				if terminal {
					return
				}
			}
		}

		// The stream ended without interaction.completed. A foreground run was cut off, so report
		// it as interrupted; the usage seen so far stays on the context for logging.
		if !state.Completed && !allowEarlyEnd {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner,
				providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("interactions stream ended before interaction.completed", fmt.Errorf("upstream closed the stream early")), body, nil, sendBackRawRequest, sendBackRawResponse),
				responseChan, logger, postHookSpanFinalizer)
			return
		}
		// A background run streamed until the connection closed while it was still running. Hand
		// it off with a non-terminal event naming the run, so post-hooks and logging finish and the
		// client knows to poll; a run whose last status was terminal is closed as it stands.
		if !state.Completed {
			var final []*schemas.BifrostResponsesStreamResponse
			switch state.Status {
			case "", GeminiInteractionStatusInProgress, "queued":
				final = state.Handoff()
				// A create's stream reports its run, as the unary create does, so job accounting
				// settles a run the client stopped watching. A replay reads a run already reported.
				if method == http.MethodPost && len(final) > 0 {
					if last := final[len(final)-1].Response; last != nil {
						status := state.Status
						if status == "" {
							status = GeminiInteractionStatusInProgress
						}
						last.ProviderJob = t.interactionJob(last, status, state.Usage, nil, true)
						// The usage so far moves onto the run, which is billed once when it ends;
						// left on the create row it would be billed twice.
						if last.ProviderJob != nil {
							last.Usage = nil
						}
					}
				}
			default:
				final, _ = state.ToBifrostResponsesStream(&GeminiInteractionsEvent{EventType: GeminiInteractionsEventCompleted})
			}
			for i, ev := range final {
				send(ev, nil, i == len(final)-1)
			}
		}
	}()
	return responseChan, nil
}

// geminiInteractionsInputItemsPageSize is the Responses input-items default page size.
const geminiInteractionsInputItemsPageSize = 20

// pageGeminiInteractionsInputItems applies a list request's order, cursor and limit. Google
// returns the whole interaction, so paging happens here. As in the Responses contract, the default
// order is newest first ("asc" keeps the run's own order) and the default page is 20 items. after
// is an item ID: the page starts past its last occurrence, and an unknown cursor yields an empty
// page.
func pageGeminiInteractionsInputItems(items []schemas.ResponsesMessage, order, after string, limit *int) ([]schemas.ResponsesMessage, bool) {
	if order != "asc" {
		reversed := make([]schemas.ResponsesMessage, len(items))
		for i, item := range items {
			reversed[len(items)-1-i] = item
		}
		items = reversed
	}
	if after != "" {
		start := -1
		for i, item := range items {
			if item.ID != nil && *item.ID == after {
				start = i + 1
			}
		}
		if start < 0 {
			return []schemas.ResponsesMessage{}, false
		}
		items = items[start:]
	}
	pageSize := geminiInteractionsInputItemsPageSize
	if limit != nil && *limit > 0 {
		pageSize = *limit
	}
	if len(items) > pageSize {
		return items[:pageSize], true
	}
	return items, false
}

// applyGeminiInteractionsAuthPrecedence keeps one credential on the request. A target that signs
// with its own Bearer token (Vertex OAuth) keeps it and drops any x-goog-api-key an extra header
// added, since Vertex Interactions refuses API keys; otherwise an API key wins over an Authorization
// header, as on the Gemini API.
func applyGeminiInteractionsAuthPrecedence(req *fasthttp.Request, t *GeminiInteractionsTarget) {
	if _, bearer := t.Headers["Authorization"]; bearer {
		req.Header.Del("x-goog-api-key")
		return
	}
	if len(req.Header.Peek("x-goog-api-key")) > 0 {
		req.Header.Del("Authorization")
	}
}
