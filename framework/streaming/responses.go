package streaming

import (
	"context"
	"fmt"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// buildCompleteMessageFromResponsesStreamChunks builds complete messages from accumulated responses stream chunks
func (a *Accumulator) buildCompleteMessageFromResponsesStreamChunks(chunks []*ResponsesStreamChunk) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	// Sort chunks by sequence number to ensure correct processing order
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].StreamResponse == nil || chunks[j].StreamResponse == nil {
			return false
		}
		return chunks[i].StreamResponse.SequenceNumber < chunks[j].StreamResponse.SequenceNumber
	})

	for _, chunk := range chunks {
		if chunk.StreamResponse == nil {
			continue
		}

		resp := chunk.StreamResponse
		switch resp.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			// Always append new items - this fixes multiple function calls issue
			if resp.Item != nil {
				messages = append(messages, *resp.Item)
			}

		case schemas.ResponsesStreamResponseTypeContentPartAdded:
			// Add content part to the most recent message, create message if none exists
			if resp.Part != nil {
				if len(messages) == 0 {
					messages = append(messages, createNewMessage())
				}

				lastMsg := &messages[len(messages)-1]
				if lastMsg.Content == nil {
					lastMsg.Content = &schemas.ResponsesMessageContent{}
				}
				if lastMsg.Content.ContentBlocks == nil {
					lastMsg.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, 0)
				}
				lastMsg.Content.ContentBlocks = append(lastMsg.Content.ContentBlocks, *resp.Part)
			}

		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if len(messages) == 0 {
				messages = append(messages, createNewMessage())
			}
			// Append text delta to the most recent message
			if resp.Delta != nil && resp.ContentIndex != nil && len(messages) > 0 {
				a.appendTextDeltaToResponsesMessage(&messages[len(messages)-1], *resp.Delta, *resp.ContentIndex)
			}

		case schemas.ResponsesStreamResponseTypeRefusalDelta:
			if len(messages) == 0 {
				messages = append(messages, createNewMessage())
			}
			// Append refusal delta to the most recent message
			if resp.Refusal != nil && resp.ContentIndex != nil && len(messages) > 0 {
				a.appendRefusalDeltaToResponsesMessage(&messages[len(messages)-1], *resp.Refusal, *resp.ContentIndex)
			}

		case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
			if len(messages) == 0 {
				messages = append(messages, createNewMessage())
			}
			if resp.Item != nil {
				messages = append(messages, *resp.Item)
			}
			// Append arguments to the most recent message
			if resp.Delta != nil && len(messages) > 0 {
				a.appendFunctionArgumentsDeltaToResponsesMessage(&messages[len(messages)-1], *resp.Delta)
			}
		}
	}

	return messages
}

func createNewMessage() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: make([]schemas.ResponsesMessageContentBlock, 0),
		},
	}
}

// appendTextDeltaToResponsesMessage appends text delta to a responses message
func (a *Accumulator) appendTextDeltaToResponsesMessage(message *schemas.ResponsesMessage, delta string, contentIndex int) {
	if message.Content == nil {
		message.Content = &schemas.ResponsesMessageContent{}
	}

	// If we don't have content blocks yet, create them
	if message.Content.ContentBlocks == nil {
		message.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, contentIndex+1)
	}

	// Ensure we have enough content blocks
	for len(message.Content.ContentBlocks) <= contentIndex {
		message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{})
	}

	// Initialize the content block if needed
	if message.Content.ContentBlocks[contentIndex].Type == "" {
		message.Content.ContentBlocks[contentIndex].Type = schemas.ResponsesOutputMessageContentTypeText
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{}
	}

	// Append to existing text or create new text
	if message.Content.ContentBlocks[contentIndex].Text == nil {
		message.Content.ContentBlocks[contentIndex].Text = &delta
	} else {
		*message.Content.ContentBlocks[contentIndex].Text += delta
	}
}

// appendRefusalDeltaToResponsesMessage appends refusal delta to a responses message
func (a *Accumulator) appendRefusalDeltaToResponsesMessage(message *schemas.ResponsesMessage, refusal string, contentIndex int) {
	if message.Content == nil {
		message.Content = &schemas.ResponsesMessageContent{}
	}

	// If we don't have content blocks yet, create them
	if message.Content.ContentBlocks == nil {
		message.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, contentIndex+1)
	}

	// Ensure we have enough content blocks
	for len(message.Content.ContentBlocks) <= contentIndex {
		message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{})
	}

	// Initialize the content block if needed
	if message.Content.ContentBlocks[contentIndex].Type == "" {
		message.Content.ContentBlocks[contentIndex].Type = schemas.ResponsesOutputMessageContentTypeRefusal
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal = &schemas.ResponsesOutputMessageContentRefusal{}
	}

	// Append to existing refusal text
	if message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal == nil {
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal = &schemas.ResponsesOutputMessageContentRefusal{
			Refusal: refusal,
		}
	} else {
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal.Refusal += refusal
	}
}

// appendFunctionArgumentsDeltaToResponsesMessage appends function arguments delta to a responses message
func (a *Accumulator) appendFunctionArgumentsDeltaToResponsesMessage(message *schemas.ResponsesMessage, arguments string) {
	if message.ResponsesToolMessage == nil {
		message.ResponsesToolMessage = &schemas.ResponsesToolMessage{}
	}

	if message.ResponsesToolMessage.Arguments == nil {
		message.ResponsesToolMessage.Arguments = &arguments
	} else {
		*message.ResponsesToolMessage.Arguments += arguments
	}
}

// processAccumulatedResponsesStreamingChunks processes all accumulated responses streaming chunks in order
func (a *Accumulator) processAccumulatedResponsesStreamingChunks(requestID string, respErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer func() {
		accumulator.mu.Unlock()
		if isFinalChunk {
			// Before unlocking, we cleanup
			defer a.cleanupStreamAccumulator(requestID)
		}
	}()

	// Initialize accumulated data
	data := &AccumulatedData{
		RequestID:      requestID,
		Status:         "success",
		Stream:         true,
		StartTimestamp: accumulator.StartTimestamp,
		EndTimestamp:   accumulator.FinalTimestamp,
		Latency:        0,
		OutputMessages: nil,
		ToolCalls:      nil,
		ErrorDetails:   respErr,
		TokenUsage:     nil,
		CacheDebug:     nil,
		Cost:           nil,
		Object:         "",
	}

	// Build complete messages from accumulated chunks
	completeMessages := a.buildCompleteMessageFromResponsesStreamChunks(accumulator.ResponsesStreamChunks)

	if !isFinalChunk {
		data.OutputMessages = completeMessages
		return data, nil
	}

	// Update database with complete messages
	data.Status = "success"
	if respErr != nil {
		data.Status = "error"
	}

	if accumulator.StartTimestamp.IsZero() || accumulator.FinalTimestamp.IsZero() {
		data.Latency = 0
	} else {
		data.Latency = accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

	data.EndTimestamp = accumulator.FinalTimestamp
	data.OutputMessages = completeMessages

	// Extract tool calls from messages
	for _, msg := range completeMessages {
		if msg.ResponsesToolMessage != nil {
			// Add tool call info to accumulated data
			// This is simplified - you might want to extract specific tool call info
		}
	}

	data.ErrorDetails = respErr

	// Update token usage from final chunk if available
	if len(accumulator.ResponsesStreamChunks) > 0 {
		lastChunk := accumulator.ResponsesStreamChunks[len(accumulator.ResponsesStreamChunks)-1]
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = lastChunk.TokenUsage
		}
		// Handle cache debug
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
	}

	// Update cost from final chunk if available
	if len(accumulator.ResponsesStreamChunks) > 0 {
		lastChunk := accumulator.ResponsesStreamChunks[len(accumulator.ResponsesStreamChunks)-1]
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
		data.FinishReason = lastChunk.FinishReason
	}

	// Update object field from accumulator (stored once for the entire stream)
	if accumulator.Object != "" {
		data.Object = accumulator.Object
	}

	return data, nil
}

// processResponsesStreamingResponse processes a responses streaming response
func (a *Accumulator) processResponsesStreamingResponse(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	a.logger.Debug("[streaming] processing responses streaming response")

	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		return nil, fmt.Errorf("request-id not found in context or is empty")
	}

	_, provider, model := bifrost.GetRequestFields(result, bifrostErr)

	accumulator := a.getOrCreateStreamAccumulator(requestID)
	accumulator.mu.Lock()
	startTimestamp := accumulator.StartTimestamp
	endTimestamp := accumulator.FinalTimestamp
	accumulator.mu.Unlock()

	// For OpenAI provider, the last chunk already contains the whole accumulated response
	// so just return it as is
	if provider == "openai" {
		isFinalChunk := bifrost.IsFinalChunk(ctx)
		if isFinalChunk {
			// For OpenAI, the final chunk contains the complete response
			// Extract the complete response and return it
			if result != nil && result.ResponsesStreamResponse != nil {
				// Build the complete response from the final chunk
				data := &AccumulatedData{
					RequestID:      requestID,
					Status:         "success",
					Stream:         true,
					StartTimestamp: startTimestamp,
					EndTimestamp:   endTimestamp,
					Latency:        result.ExtraFields.Latency,
					ErrorDetails:   bifrostErr,
					Object:         result.Object,
				}

				if bifrostErr != nil {
					data.Status = "error"
				}

				// Extract the complete response from the stream response
				if result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.ResponsesResponse != nil {
					data.OutputMessages = result.ResponsesStreamResponse.Response.ResponsesResponse.Output
					if result.ResponsesStreamResponse.Response.Usage != nil {
						// Convert ResponsesResponseUsage to schemas.LLMUsage
						data.TokenUsage = &schemas.LLMUsage{
							ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
								InputTokens:  result.ResponsesStreamResponse.Response.Usage.InputTokens,
								OutputTokens: result.ResponsesStreamResponse.Response.Usage.OutputTokens,
							},
							TotalTokens: result.ResponsesStreamResponse.Response.Usage.TotalTokens,
						}
					}
				}

				if a.pricingManager != nil {
					cost := a.pricingManager.CalculateCostWithCacheDebug(result)
					data.Cost = bifrost.Ptr(cost)
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
		}

		// For non-final chunks from OpenAI, just pass through
		return &ProcessedStreamResponse{
			Type:       StreamResponseTypeDelta,
			RequestID:  requestID,
			StreamType: StreamTypeResponses,
			Provider:   provider,
			Model:      model,
			Data:       nil, // No accumulated data for delta responses
		}, nil
	}

	// For non-OpenAI providers, use the accumulation logic
	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getResponsesStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr

	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.ResponsesStreamResponse != nil {
		// Store the stream response
		chunk.StreamResponse = result.ResponsesStreamResponse

		// Extract token usage from stream response if available
		if result.ResponsesStreamResponse.Response != nil &&
			result.ResponsesStreamResponse.Response.Usage != nil {
			chunk.TokenUsage = &schemas.LLMUsage{
				ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
					InputTokens:  result.ResponsesStreamResponse.Response.Usage.InputTokens,
					OutputTokens: result.ResponsesStreamResponse.Response.Usage.OutputTokens,
				},
				TotalTokens: result.ResponsesStreamResponse.Response.Usage.TotalTokens,
			}
		}
	}

	// Add chunk to accumulator synchronously to maintain order
	object := ""
	if result != nil {
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.ExtraFields.CacheDebug
		}
		object = result.Object
	}

	if addErr := a.addResponsesStreamChunk(requestID, chunk, object, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add responses stream chunk for request %s: %w", requestID, addErr)
	}

	// If this is the final chunk, process accumulated chunks
	if isFinalChunk {
		shouldProcess := false
		// Get the accumulator to check if processing has already been triggered
		accumulator := a.getOrCreateStreamAccumulator(requestID)
		accumulator.mu.Lock()
		shouldProcess = !accumulator.IsComplete
		// Mark as complete when we're about to process
		if shouldProcess {
			accumulator.IsComplete = true
		}
		accumulator.mu.Unlock()

		if shouldProcess {
			data, processErr := a.processAccumulatedResponsesStreamingChunks(requestID, bifrostErr, isFinalChunk)
			if processErr != nil {
				a.logger.Error("failed to process accumulated responses chunks for request %s: %v", requestID, processErr)
				return nil, processErr
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
		return nil, nil
	}

	return &ProcessedStreamResponse{
		Type:       StreamResponseTypeDelta,
		RequestID:  requestID,
		StreamType: StreamTypeResponses,
		Provider:   provider,
		Model:      model,
		Data:       nil,
	}, nil
}
