package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// pendingFinish is a finish reason that arrived without usage. Cloud Code Assist may send
// usage in a later usage-only frame, and the stream's final chunk must carry both.
type pendingFinish struct {
	reason    gemini.FinishReason
	grounding *gemini.GroundingMetadata
	index     int32
}

// deferrableFinish reports a successful finish reason that can wait for usage. Error
// finish reasons end the stream immediately through the converter.
func deferrableFinish(reason gemini.FinishReason) bool {
	return reason == gemini.FinishReasonStop || reason == gemini.FinishReasonMaxTokens
}

// finalFrame builds the closing Gemini frame from a deferred finish reason and the last
// usage seen.
func finalFrame(pending *pendingFinish, usage *gemini.GenerateContentResponseUsageMetadata) *gemini.GenerateContentResponse {
	candidate := &gemini.Candidate{FinishReason: gemini.FinishReasonStop}
	if pending != nil {
		candidate.FinishReason = pending.reason
		candidate.GroundingMetadata = pending.grounding
		candidate.Index = pending.index
	}
	if usage == nil {
		usage = &gemini.GenerateContentResponseUsageMetadata{}
	}
	return &gemini.GenerateContentResponse{Candidates: []*gemini.Candidate{candidate}, UsageMetadata: usage}
}

// isRoleOnlyDelta reports a stream chunk with nothing in it but the assistant role.
func isRoleOnlyDelta(response *schemas.BifrostChatResponse) bool {
	if response.Usage != nil {
		return false
	}
	for _, choice := range response.Choices {
		if choice.FinishReason != nil || choice.LogProbs != nil || choice.ChatStreamResponseChoice == nil {
			return false
		}
		d := choice.Delta
		if d == nil {
			continue
		}
		if d.Content != nil || d.Refusal != nil || d.Audio != nil || d.Reasoning != nil ||
			len(d.ReasoningDetails) > 0 || len(d.Annotations) > 0 || len(d.ToolCalls) > 0 || len(d.ExtraContent) > 0 {
			return false
		}
	}
	return true
}

// streamChat opens the SSE stream (refreshing the token once on a 401) and converts its
// {"response": ...} frames into Bifrost chat stream chunks.
func (p *AntigravityProvider) streamChat(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
	plan *requestPlan,
	auth *authSession,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, p.networkConfig.StreamIdleTimeoutInSeconds)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse)
	url := p.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, streamGeneratePath)

	var (
		resp      *fasthttp.Response
		jsonBody  []byte
		startTime time.Time
		latency   time.Duration
	)
	for attempt := 0; ; attempt++ {
		var err error
		jsonBody, err = plan.envelope(auth.projectID)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		}

		req := fasthttp.AcquireRequest()
		resp = fasthttp.AcquireResponse()
		resp.StreamBody = true
		p.setRequestHeaders(ctx, req, url, auth.accessToken, jsonBody)

		startTime = time.Now()
		doErr := providerUtils.DoStreamingRequest(ctx, p.streamingClient, req, resp)
		latency = time.Since(startTime)
		fasthttp.ReleaseRequest(req)
		if doErr != nil {
			providerUtils.ReleaseStreamingResponse(ctx, resp)
			if errors.Is(doErr, context.Canceled) {
				return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   doErr,
					},
				}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
			}
			if errors.Is(doErr, fasthttp.ErrTimeout) || errors.Is(doErr, context.DeadlineExceeded) {
				return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, doErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
			}
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, doErr), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}

		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

		if status := resp.StatusCode(); status != fasthttp.StatusOK {
			respBody := append([]byte(nil), resp.Body()...)
			bifrostErr := parseAntigravityError(resp)
			providerUtils.ReleaseStreamingResponse(ctx, resp)
			if status == http.StatusUnauthorized && attempt == 0 {
				refreshed, refreshErr := p.resolveAuth(ctx, key, auth.accessToken)
				if refreshErr != nil {
					return nil, providerUtils.EnrichError(ctx, refreshErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
				}
				auth = refreshed
				continue
			}
			if status == http.StatusUnauthorized {
				invalidateToken(key, auth.accessToken)
			}
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, respBody, sendBackRawRequest, sendBackRawResponse, latency)
		}
		break
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	logger := p.logger

	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		sendError := func(bifrostErr *schemas.BifrostError) {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, logger, postHookSpanFinalizer)
		}

		if resp.BodyStream() == nil {
			sendError(providerUtils.NewBifrostOperationError("Provider returned an empty response", fmt.Errorf("provider returned an empty response")))
			return
		}

		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		sseReader := providerUtils.GetSSEDataReader(ctx, reader)

		chunkIndex := 0
		lastChunkTime := startTime
		var responseID, modelName string
		streamState := gemini.NewGeminiStreamState()

		// Running usage so a mid-stream cancel or timeout still bills processed tokens;
		// usageMetadata is cumulative, so the latest copy is the running total.
		streamUsage := &schemas.BifrostLLMUsage{}
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, streamUsage)

		var pending *pendingFinish
		var lastUsage *gemini.GenerateContentResponseUsageMetadata

		// emit converts one Gemini frame and sends its deltas. It reports whether the
		// stream is over (final chunk or error sent).
		emit := func(frame *gemini.GenerateContentResponse, eventData []byte) bool {
			convStart := time.Now()
			responses, bifrostErr, isLastChunk := frame.ToBifrostChatCompletionStream(streamState)
			schemas.AddStreamConvert(ctx, time.Since(convStart))
			if bifrostErr != nil {
				sendError(bifrostErr)
				return true
			}
			for i, response := range responses {
				isLastDelta := isLastChunk && i == len(responses)-1
				// A delta carrying only the role (a frame whose parts converted to
				// nothing) is dropped: the first chunk of the stream must be real output
				// or an error, because only a first-chunk error is retried or rotated.
				if !isLastDelta && isRoleOnlyDelta(response) {
					continue
				}
				response.ID = responseID
				response.Model = modelName
				restoreToolNames(response, plan.names)
				if response.Usage != nil {
					*streamUsage = *response.Usage
				}
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					ChunkIndex: chunkIndex,
					Latency:    time.Since(lastChunkTime).Milliseconds(),
				}
				if sendBackRawResponse && eventData != nil && i == len(responses)-1 {
					response.ExtraFields.RawResponse = string(eventData)
				}
				lastChunkTime = time.Now()
				chunkIndex++

				if isLastDelta {
					if sendBackRawRequest {
						providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
					}
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
					return true
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
			return isLastChunk
		}

		for {
			if ctx.Err() != nil {
				return
			}
			eventData, readErr := sseReader.ReadDataLine()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				logger.Warn("antigravity: error reading stream: %v", readErr)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, logger, postHookSpanFinalizer)
				return
			}

			parseStart := time.Now()
			var envelope responseEnvelope
			if err := sonic.Unmarshal(eventData, &envelope); err != nil {
				schemas.AddStreamParse(ctx, time.Since(parseStart))
				logger.Warn("antigravity: failed to parse stream frame: %v", err)
				continue
			}
			if envelope.Error != nil {
				schemas.AddStreamParse(ctx, time.Since(parseStart))
				sendError(normalizeUpstreamError(nil, envelope.Error.Code, envelope.Error))
				return
			}
			if len(envelope.Response) == 0 || string(envelope.Response) == "null" {
				schemas.AddStreamParse(ctx, time.Since(parseStart))
				continue
			}
			var frame gemini.GenerateContentResponse
			err := sonic.Unmarshal(envelope.Response, &frame)
			schemas.AddStreamParse(ctx, time.Since(parseStart))
			if err != nil {
				logger.Warn("antigravity: failed to parse stream frame: %v", err)
				continue
			}

			if frame.ResponseID != "" && responseID == "" {
				responseID = frame.ResponseID
			}
			if frame.ModelVersion != "" && modelName == "" {
				modelName = frame.ModelVersion
			}
			if modelName == "" {
				modelName = request.Model
			}
			if frame.UsageMetadata != nil {
				lastUsage = frame.UsageMetadata
				if usage := gemini.ConvertGeminiUsageMetadataToChatUsage(frame.UsageMetadata); usage != nil {
					*streamUsage = *usage
				}
			}

			if len(frame.Candidates) > 0 && frame.Candidates[0] != nil {
				candidate := frame.Candidates[0]
				switch {
				case candidate.FinishReason != "" && frame.UsageMetadata == nil && deferrableFinish(candidate.FinishReason):
					pending = &pendingFinish{reason: candidate.FinishReason, grounding: candidate.GroundingMetadata, index: candidate.Index}
					candidate.FinishReason = ""
					candidate.GroundingMetadata = nil
				case candidate.FinishReason == "" && pending != nil && frame.UsageMetadata != nil:
					candidate.FinishReason = pending.reason
					candidate.GroundingMetadata = pending.grounding
					pending = nil
				}
			} else if frame.PromptFeedback == nil {
				// A usage-only frame closes a stream whose finish reason came earlier.
				if pending != nil && frame.UsageMetadata != nil {
					final := finalFrame(pending, frame.UsageMetadata)
					pending = nil
					if emit(final, eventData) {
						return
					}
				}
				continue
			}

			if emit(&frame, eventData) {
				return
			}
		}

		if ctx.Err() != nil {
			return
		}
		// The body ended without a frame carrying both finish reason and usage. A
		// finish reason or usage on its own still marks a complete answer; neither
		// means the stream was cut.
		if pending != nil || lastUsage != nil {
			if modelName == "" {
				modelName = request.Model
			}
			emit(finalFrame(pending, lastUsage), nil)
			return
		}
		sendError(&schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(http.StatusBadGateway),
			Error:          &schemas.ErrorField{Message: "antigravity stream ended without a terminal signal"},
		})
	}()

	return responseChan, nil
}
