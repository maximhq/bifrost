package streaming

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// processResponsesStreamingResponse processes a responses streaming response
func (a *Accumulator) processResponsesStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}

	requestType, provider, model := bifrost.GetRequestFields(result, bifrostErr)
	if requestType != schemas.ResponsesStreamRequest {
		return nil, fmt.Errorf("invalid request type for responses streaming: %s", requestType)
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getResponsesStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr

	if result != nil && result.ResponsesStreamResponse != nil {
		if cloned, err := cloneResponsesStreamResponse(result.ResponsesStreamResponse); err != nil {
			a.logger.Warn("[streaming] failed to clone responses stream event: %v", err)
			chunk.Event = result.ResponsesStreamResponse
		} else {
			chunk.Event = cloned
		}

		if usage := extractResponsesUsage(result, chunk.Event); usage != nil {
			chunk.TokenUsage = usage
		}
	}

	if result != nil && isFinalChunk {
		if a.pricingManager != nil {
			cost := a.pricingManager.CalculateCostWithCacheDebug(result)
			chunk.Cost = bifrost.Ptr(cost)
		}
		chunk.SemanticCacheDebug = result.ExtraFields.CacheDebug
	}

	object := string(StreamTypeResponses)
	if result != nil && result.Object != "" {
		object = result.Object
	}

	if err := a.addResponsesStreamChunk(requestID, chunk, object, isFinalChunk); err != nil {
		return nil, fmt.Errorf("failed to add responses stream chunk for request %s: %w", requestID, err)
	}

	if !isFinalChunk {
		return nil, nil
	}

	shouldProcess := false
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	accumulator.mu.Lock()
	if !accumulator.IsComplete {
		accumulator.IsComplete = true
		shouldProcess = true
	}
	accumulator.mu.Unlock()

	if !shouldProcess {
		return nil, nil
	}

	data, err := a.processAccumulatedResponsesStreamingChunks(requestID, model, bifrostErr, isFinalChunk)
	if err != nil {
		a.logger.Error("failed to process accumulated responses chunks for request %s: %v", requestID, err)
		return nil, err
	}

	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeFinal,
		RequestID:  requestID,
		StreamType: StreamTypeResponses,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}

func (a *Accumulator) processAccumulatedResponsesStreamingChunks(requestID, model string, respErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if isFinalChunk {
		defer a.cleanupStreamAccumulator(requestID)
	}

	data := &AccumulatedData{
		RequestID:      requestID,
		Model:          model,
		Status:         "success",
		Stream:         true,
		StartTimestamp: accumulator.StartTimestamp,
		EndTimestamp:   accumulator.FinalTimestamp,
		ErrorDetails:   respErr,
		Object:         accumulator.Object,
	}

	if data.Object == "" {
		data.Object = string(StreamTypeResponses)
	}

	if respErr != nil {
		data.Status = "error"
	}

	if !accumulator.StartTimestamp.IsZero() && !accumulator.FinalTimestamp.IsZero() {
		data.Latency = accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

	if len(accumulator.ResponsesStreamChunks) == 0 {
		return data, nil
	}

	lastChunk := accumulator.ResponsesStreamChunks[len(accumulator.ResponsesStreamChunks)-1]
	if lastChunk.TokenUsage != nil {
		data.TokenUsage = lastChunk.TokenUsage
	}
	if lastChunk.SemanticCacheDebug != nil {
		data.CacheDebug = lastChunk.SemanticCacheDebug
	}
	if lastChunk.Cost != nil {
		data.Cost = lastChunk.Cost
	}

	if lastChunk.Event != nil && lastChunk.Event.Response != nil {
		data.ResponsesOutput = lastChunk.Event.Response.ResponsesResponse
		if data.TokenUsage == nil {
			data.TokenUsage = convertResponsesUsage(lastChunk.Event.Response.Usage)
		}
	}

	// Derive output message/tool calls for logging convenience
	if data.ResponsesOutput != nil && len(data.ResponsesOutput.Output) > 0 {
		chatMessages := schemas.ToChatMessages(data.ResponsesOutput.Output)
		if len(chatMessages) > 0 {
			lastMessage := chatMessages[len(chatMessages)-1]
			data.OutputMessage = &lastMessage
			if lastMessage.ChatAssistantMessage != nil && lastMessage.ChatAssistantMessage.ToolCalls != nil {
				data.ToolCalls = lastMessage.ChatAssistantMessage.ToolCalls
			}
		}
	}

	return data, nil
}

func cloneResponsesStreamResponse(event *schemas.ResponsesStreamResponse) (*schemas.ResponsesStreamResponse, error) {
	if event == nil {
		return nil, nil
	}

	payload, err := sonic.Marshal(event)
	if err != nil {
		return nil, err
	}

	var cloned schemas.ResponsesStreamResponse
	if err := sonic.Unmarshal(payload, &cloned); err != nil {
		return nil, err
	}

	return &cloned, nil
}

func extractResponsesUsage(result *schemas.BifrostResponse, event *schemas.ResponsesStreamResponse) *schemas.LLMUsage {
	if result != nil && result.Usage != nil && result.Usage.TotalTokens > 0 {
		return result.Usage
	}

	if event != nil && event.Response != nil {
		return convertResponsesUsage(event.Response.Usage)
	}
	return nil
}

func convertResponsesUsage(usage *schemas.ResponsesResponseUsage) *schemas.LLMUsage {
	if usage == nil {
		return nil
	}

	llmUsage := &schemas.LLMUsage{
		ResponsesExtendedResponseUsage: usage.ResponsesExtendedResponseUsage,
	}

	if usage.ResponsesExtendedResponseUsage != nil {
		llmUsage.PromptTokens = usage.ResponsesExtendedResponseUsage.InputTokens
		llmUsage.CompletionTokens = usage.ResponsesExtendedResponseUsage.OutputTokens
	}

	if usage.TotalTokens != 0 {
		llmUsage.TotalTokens = usage.TotalTokens
	} else {
		llmUsage.TotalTokens = llmUsage.PromptTokens + llmUsage.CompletionTokens
	}

	return llmUsage
}
