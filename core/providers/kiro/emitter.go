package kiro

import (
	"context"
	"time"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// chunkEmitter turns assembled outputs into chat completion chunks and sends them through the
// post-hook pipeline. When the request is a Responses stream served through chat, every chunk
// is converted into Responses stream events instead.
type chunkEmitter struct {
	ctx            *schemas.BifrostContext
	postHookRunner schemas.PostHookRunner
	finalizer      func(context.Context)
	responseChan   chan *schemas.BifrostStreamChunk
	logger         schemas.Logger

	id            string
	model         string
	created       int
	chunkIndex    int
	start         time.Time
	lastChunkTime time.Time
	// roleSent is set once a chunk carried the assistant role. The role rides on the first chunk
	// with real output instead of being sent eagerly: core turns only the FIRST chunk of a stream
	// into a synchronous error (retry, key rotation, fallback), and Kiro reports throttling and
	// quota refusals as exception frames inside an HTTP 200 stream.
	roleSent bool
	// responsesState is non-nil when chunks are re-assembled into Responses events.
	responsesState *schemas.ChatToResponsesStreamState
}

func newChunkEmitter(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	finalizer func(context.Context),
	responseChan chan *schemas.BifrostStreamChunk,
	logger schemas.Logger,
	model string,
	headerLatency time.Duration,
) *chunkEmitter {
	now := time.Now()
	e := &chunkEmitter{
		ctx:            ctx,
		postHookRunner: postHookRunner,
		finalizer:      finalizer,
		responseChan:   responseChan,
		logger:         logger,
		id:             "chatcmpl-" + uuid.NewString(),
		model:          model,
		created:        int(now.Unix()),
		start:          now.Add(-headerLatency),
		lastChunkTime:  now,
	}
	if fallback, ok := ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback).(bool); ok && fallback {
		e.responsesState = schemas.AcquireChatToResponsesStreamState()
	}
	return e
}

func (e *chunkEmitter) release() {
	if e.responsesState != nil {
		schemas.ReleaseChatToResponsesStreamState(e.responsesState)
		e.responsesState = nil
	}
}

// withRole attaches the assistant role to the first chunk that is sent.
func (e *chunkEmitter) withRole(delta *schemas.ChatStreamResponseChoiceDelta) {
	if !e.roleSent {
		delta.Role = schemas.Ptr(string(schemas.ChatMessageRoleAssistant))
		e.roleSent = true
	}
}

// sendOutput emits one assembled output as a delta chunk.
func (e *chunkEmitter) sendOutput(out streamOutput, raw string) bool {
	delta := &schemas.ChatStreamResponseChoiceDelta{}
	switch out.kind {
	case outputText:
		if out.text == "" {
			return true
		}
		text := out.text
		delta.Content = &text
	case outputReasoning:
		if out.text == "" {
			return true
		}
		text := out.text
		delta.Reasoning = &text
		delta.ReasoningDetails = []schemas.ChatReasoningDetails{{Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Text: &text}}
	case outputReasoningSignature:
		signature := out.text
		delta.ReasoningDetails = []schemas.ChatReasoningDetails{{Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Signature: &signature}}
	case outputReasoningRedacted:
		data := out.text
		delta.ReasoningDetails = []schemas.ChatReasoningDetails{{Index: 0, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Data: &data}}
	case outputToolStart:
		delta.ToolCalls = []schemas.ChatAssistantMessageToolCall{{
			Index:    uint16(out.toolIndex),
			Type:     schemas.Ptr("function"),
			ID:       schemas.Ptr(out.toolID),
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(out.toolName)},
		}}
	case outputToolArgs:
		delta.ToolCalls = []schemas.ChatAssistantMessageToolCall{{
			Index:    uint16(out.toolIndex),
			Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: out.text},
		}}
	}
	e.withRole(delta)
	return e.send(delta, nil, nil, raw, false, nil)
}

// sendFinal emits the terminal chunk carrying the finish reason and usage. A stream that produced
// no output carries the role here.
func (e *chunkEmitter) sendFinal(finishReason string, usage *schemas.BifrostLLMUsage, rawRequest []byte) {
	final := *usage
	delta := &schemas.ChatStreamResponseChoiceDelta{}
	e.withRole(delta)
	e.send(delta, &finishReason, &final, "", true, rawRequest)
}

// send builds one chunk and hands it (or its Responses events) to the post-hook pipeline. It
// returns false when the stream must stop.
func (e *chunkEmitter) send(delta *schemas.ChatStreamResponseChoiceDelta, finishReason *string, usage *schemas.BifrostLLMUsage, raw string, final bool, rawRequest []byte) bool {
	now := time.Now()
	response := &schemas.BifrostChatResponse{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Created: e.created,
		Model:   e.model,
		Usage:   usage,
		Choices: []schemas.BifrostResponseChoice{{
			Index:                    0,
			FinishReason:             finishReason,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
		}},
	}
	response.ExtraFields.ChunkIndex = e.chunkIndex
	response.ExtraFields.Latency = now.Sub(e.lastChunkTime).Milliseconds()
	if final {
		response.ExtraFields.Latency = now.Sub(e.start).Milliseconds()
	}
	e.chunkIndex++
	e.lastChunkTime = now

	if e.responsesState == nil {
		if raw != "" {
			response.ExtraFields.RawResponse = raw
		}
		if final {
			if len(rawRequest) > 0 {
				providerUtils.ParseAndSetRawRequest(&response.ExtraFields, rawRequest)
			}
			e.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		}
		providerUtils.ProcessAndSendResponse(e.ctx, e.postHookRunner,
			providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), e.responseChan, e.finalizer)
		return true
	}

	// One chat chunk spreads into several Responses events. The upstream frame is reported once,
	// on the first event of the batch, and the raw request only on the terminal event.
	for i, event := range response.ToBifrostResponsesStreamResponse(e.responsesState) {
		if event.Type == schemas.ResponsesStreamResponseTypeError {
			bErr := &schemas.BifrostError{
				Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
				IsBifrostError: false,
				Error:          &schemas.ErrorField{},
			}
			if event.Message != nil {
				bErr.Error.Message = *event.Message
			}
			if event.Code != nil {
				bErr.Error.Code = event.Code
			}
			e.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(e.ctx, e.postHookRunner, bErr, e.responseChan, e.logger, e.finalizer)
			return false
		}
		event.ExtraFields.ChunkIndex = event.SequenceNumber
		if i == 0 && raw != "" {
			event.ExtraFields.RawResponse = raw
		}
		if event.Type == schemas.ResponsesStreamResponseTypeCompleted || event.Type == schemas.ResponsesStreamResponseTypeIncomplete {
			event.ExtraFields.Latency = now.Sub(e.start).Milliseconds()
			if len(rawRequest) > 0 {
				providerUtils.ParseAndSetRawRequest(&event.ExtraFields, rawRequest)
			}
			e.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		}
		providerUtils.ProcessAndSendResponse(e.ctx, e.postHookRunner,
			providerUtils.GetBifrostResponseForStreamResponse(nil, nil, event, nil, nil, nil), e.responseChan, e.finalizer)
	}
	return true
}
