package streaming

import (
	"context"
	"fmt"
	"sort"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// deepCopyResponsesStreamResponse creates a deep copy of BifrostResponsesStreamResponse
// to prevent shared data mutation between different plugin accumulators
func deepCopyResponsesStreamResponse(original *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse {
	if original == nil {
		return nil
	}

	copy := &schemas.BifrostResponsesStreamResponse{
		Type:           original.Type,
		SequenceNumber: original.SequenceNumber,
		ExtraFields:    original.ExtraFields, // ExtraFields can be safely shared as they're typically read-only
	}

	// Deep copy Response if present
	if original.Response != nil {
		copy.Response = &schemas.BifrostResponsesResponse{}
		*copy.Response = *original.Response // Shallow copy the struct

		// Deep copy the Output slice if present
		if original.Response.Output != nil {
			copy.Response.Output = make([]schemas.ResponsesMessage, len(original.Response.Output))
			for i, msg := range original.Response.Output {
				copy.Response.Output[i] = deepCopyResponsesMessage(msg)
			}
		}

		// Copy Usage if present (Usage can be shallow copied as it's typically immutable)
		if original.Response.Usage != nil {
			copyUsage := *original.Response.Usage
			copy.Response.Usage = &copyUsage
		}
	}

	// Copy pointer fields
	if original.OutputIndex != nil {
		copyOutputIndex := *original.OutputIndex
		copy.OutputIndex = &copyOutputIndex
	}

	if original.Item != nil {
		copyItem := deepCopyResponsesMessage(*original.Item)
		copy.Item = &copyItem
	}

	if original.ContentIndex != nil {
		copyContentIndex := *original.ContentIndex
		copy.ContentIndex = &copyContentIndex
	}

	if original.ItemID != nil {
		copyItemID := *original.ItemID
		copy.ItemID = &copyItemID
	}

	if original.Part != nil {
		copyPart := deepCopyResponsesMessageContentBlock(*original.Part)
		copy.Part = &copyPart
	}

	if original.Delta != nil {
		copyDelta := *original.Delta
		copy.Delta = &copyDelta
	}

	// Deep copy LogProbs slice if present
	if original.LogProbs != nil {
		copy.LogProbs = make([]schemas.ResponsesOutputMessageContentTextLogProb, len(original.LogProbs))
		for i, logProb := range original.LogProbs {
			copiedLogProb := schemas.ResponsesOutputMessageContentTextLogProb{
				LogProb: logProb.LogProb,
				Token:   logProb.Token,
			}
			// Deep copy Bytes slice
			if logProb.Bytes != nil {
				copiedLogProb.Bytes = make([]int, len(logProb.Bytes))
				for j, byteValue := range logProb.Bytes {
					copiedLogProb.Bytes[j] = byteValue
				}
			}
			// Deep copy TopLogProbs slice
			if logProb.TopLogProbs != nil {
				copiedLogProb.TopLogProbs = make([]schemas.LogProb, len(logProb.TopLogProbs))
				for j, topLogProb := range logProb.TopLogProbs {
					copiedLogProb.TopLogProbs[j] = schemas.LogProb{
						Bytes:   topLogProb.Bytes,
						LogProb: topLogProb.LogProb,
						Token:   topLogProb.Token,
					}
				}
			}
			copy.LogProbs[i] = copiedLogProb
		}
	}

	if original.Text != nil {
		copyText := *original.Text
		copy.Text = &copyText
	}

	if original.Refusal != nil {
		copyRefusal := *original.Refusal
		copy.Refusal = &copyRefusal
	}

	if original.Arguments != nil {
		copyArguments := *original.Arguments
		copy.Arguments = &copyArguments
	}

	if original.PartialImageB64 != nil {
		copyPartialImageB64 := *original.PartialImageB64
		copy.PartialImageB64 = &copyPartialImageB64
	}

	if original.PartialImageIndex != nil {
		copyPartialImageIndex := *original.PartialImageIndex
		copy.PartialImageIndex = &copyPartialImageIndex
	}

	if original.Annotation != nil {
		copyAnnotation := *original.Annotation
		copy.Annotation = &copyAnnotation
	}

	if original.AnnotationIndex != nil {
		copyAnnotationIndex := *original.AnnotationIndex
		copy.AnnotationIndex = &copyAnnotationIndex
	}

	if original.Code != nil {
		copyCode := *original.Code
		copy.Code = &copyCode
	}

	if original.Message != nil {
		copyMessage := *original.Message
		copy.Message = &copyMessage
	}

	if original.Param != nil {
		copyParam := *original.Param
		copy.Param = &copyParam
	}

	return copy
}

// deepCopyResponsesMessage creates a deep copy of a ResponsesMessage
func deepCopyResponsesMessage(original schemas.ResponsesMessage) schemas.ResponsesMessage {
	copy := schemas.ResponsesMessage{}

	if original.ID != nil {
		copyID := *original.ID
		copy.ID = &copyID
	}

	if original.Type != nil {
		copyType := *original.Type
		copy.Type = &copyType
	}

	if original.Role != nil {
		copyRole := *original.Role
		copy.Role = &copyRole
	}

	if original.Content != nil {
		copy.Content = &schemas.ResponsesMessageContent{}

		if original.Content.ContentStr != nil {
			copyContentStr := *original.Content.ContentStr
			copy.Content.ContentStr = &copyContentStr
		}

		if original.Content.ContentBlocks != nil {
			copy.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, len(original.Content.ContentBlocks))
			for i, block := range original.Content.ContentBlocks {
				copy.Content.ContentBlocks[i] = deepCopyResponsesMessageContentBlock(block)
			}
		}
	}

	if original.ResponsesToolMessage != nil {
		copy.ResponsesToolMessage = &schemas.ResponsesToolMessage{}

		// Deep copy primitive fields
		if original.ResponsesToolMessage.CallID != nil {
			copyCallID := *original.ResponsesToolMessage.CallID
			copy.ResponsesToolMessage.CallID = &copyCallID
		}

		if original.ResponsesToolMessage.Name != nil {
			copyName := *original.ResponsesToolMessage.Name
			copy.ResponsesToolMessage.Name = &copyName
		}

		if original.ResponsesToolMessage.Arguments != nil {
			copyArguments := *original.ResponsesToolMessage.Arguments
			copy.ResponsesToolMessage.Arguments = &copyArguments
		}

		if original.ResponsesToolMessage.Error != nil {
			copyError := *original.ResponsesToolMessage.Error
			copy.ResponsesToolMessage.Error = &copyError
		}

		// Deep copy Output
		if original.ResponsesToolMessage.Output != nil {
			copy.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

			if original.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				copyStr := *original.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
				copy.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = &copyStr
			}

			if original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
				copy.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = make([]schemas.ResponsesMessageContentBlock, len(original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks))
				for i, block := range original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
					copy.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks[i] = deepCopyResponsesMessageContentBlock(block)
				}
			}

			if original.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput != nil {
				copyOutput := *original.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput
				copy.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput = &copyOutput
			}
		}

		// Deep copy Action
		if original.ResponsesToolMessage.Action != nil {
			copy.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{}

			if original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction
				// Deep copy Path slice
				if copyAction.Path != nil {
					copyAction.Path = make([]schemas.ResponsesComputerToolCallActionPath, len(copyAction.Path))
					for i, path := range original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction.Path {
						copyAction.Path[i] = path // struct copy is fine for simple structs
					}
				}
				// Deep copy Keys slice
				if copyAction.Keys != nil {
					copyAction.Keys = make([]string, len(copyAction.Keys))
					for i, key := range original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction.Keys {
						copyAction.Keys[i] = key
					}
				}
				copy.ResponsesToolMessage.Action.ResponsesComputerToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
				copy.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction
				copy.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction
				copy.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction = &copyAction
			}
		}

		// Deep copy embedded tool call structs
		if original.ResponsesToolMessage.ResponsesFileSearchToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesFileSearchToolCall
			// Deep copy Queries slice
			if copyToolCall.Queries != nil {
				copyToolCall.Queries = make([]string, len(copyToolCall.Queries))
				for i, query := range original.ResponsesToolMessage.ResponsesFileSearchToolCall.Queries {
					copyToolCall.Queries[i] = query
				}
			}
			// Deep copy Results slice
			if copyToolCall.Results != nil {
				copyToolCall.Results = make([]schemas.ResponsesFileSearchToolCallResult, len(copyToolCall.Results))
				for i, result := range original.ResponsesToolMessage.ResponsesFileSearchToolCall.Results {
					copyResult := result
					// Deep copy Attributes map if present
					if result.Attributes != nil {
						copyAttrs := make(map[string]any, len(*result.Attributes))
						for k, v := range *result.Attributes {
							copyAttrs[k] = v
						}
						copyResult.Attributes = &copyAttrs
					}
					copyToolCall.Results[i] = copyResult
				}
			}
			copy.ResponsesToolMessage.ResponsesFileSearchToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesComputerToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesComputerToolCall
			// Deep copy PendingSafetyChecks slice
			if copyToolCall.PendingSafetyChecks != nil {
				copyToolCall.PendingSafetyChecks = make([]schemas.ResponsesComputerToolCallPendingSafetyCheck, len(copyToolCall.PendingSafetyChecks))
				for i, check := range original.ResponsesToolMessage.ResponsesComputerToolCall.PendingSafetyChecks {
					copyToolCall.PendingSafetyChecks[i] = check
				}
			}
			copy.ResponsesToolMessage.ResponsesComputerToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesComputerToolCallOutput != nil {
			copyOutput := *original.ResponsesToolMessage.ResponsesComputerToolCallOutput
			// Deep copy AcknowledgedSafetyChecks slice
			if copyOutput.AcknowledgedSafetyChecks != nil {
				copyOutput.AcknowledgedSafetyChecks = make([]schemas.ResponsesComputerToolCallAcknowledgedSafetyCheck, len(copyOutput.AcknowledgedSafetyChecks))
				for i, check := range original.ResponsesToolMessage.ResponsesComputerToolCallOutput.AcknowledgedSafetyChecks {
					copyOutput.AcknowledgedSafetyChecks[i] = check
				}
			}
			copy.ResponsesToolMessage.ResponsesComputerToolCallOutput = &copyOutput
		}

		if original.ResponsesToolMessage.ResponsesCodeInterpreterToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
			// Deep copy Outputs slice
			if copyToolCall.Outputs != nil {
				copyToolCall.Outputs = make([]schemas.ResponsesCodeInterpreterOutput, len(copyToolCall.Outputs))
				for i, output := range original.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs {
					copyToolCall.Outputs[i] = output
				}
			}
			copy.ResponsesToolMessage.ResponsesCodeInterpreterToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesMCPToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesMCPToolCall
			copy.ResponsesToolMessage.ResponsesMCPToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesCustomToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesCustomToolCall
			copy.ResponsesToolMessage.ResponsesCustomToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesImageGenerationCall != nil {
			copyCall := *original.ResponsesToolMessage.ResponsesImageGenerationCall
			copy.ResponsesToolMessage.ResponsesImageGenerationCall = &copyCall
		}

		if original.ResponsesToolMessage.ResponsesMCPListTools != nil {
			copyListTools := *original.ResponsesToolMessage.ResponsesMCPListTools
			// Deep copy Tools slice
			if copyListTools.Tools != nil {
				copyListTools.Tools = make([]schemas.ResponsesMCPTool, len(copyListTools.Tools))
				for i, tool := range original.ResponsesToolMessage.ResponsesMCPListTools.Tools {
					copyListTools.Tools[i] = tool
				}
			}
			copy.ResponsesToolMessage.ResponsesMCPListTools = &copyListTools
		}

		if original.ResponsesToolMessage.ResponsesMCPApprovalResponse != nil {
			copyApproval := *original.ResponsesToolMessage.ResponsesMCPApprovalResponse
			copy.ResponsesToolMessage.ResponsesMCPApprovalResponse = &copyApproval
		}
	}

	return copy
}

// deepCopyResponsesMessageContentBlock creates a deep copy of a ResponsesMessageContentBlock
func deepCopyResponsesMessageContentBlock(original schemas.ResponsesMessageContentBlock) schemas.ResponsesMessageContentBlock {
	copy := schemas.ResponsesMessageContentBlock{
		Type: original.Type,
	}

	if original.Text != nil {
		copyText := *original.Text
		copy.Text = &copyText
	}

	// Copy other specific content type fields as needed
	if original.ResponsesOutputMessageContentText != nil {
		t := *original.ResponsesOutputMessageContentText
		// Annotations
		if t.Annotations != nil {
			t.Annotations = append([]schemas.ResponsesOutputMessageContentTextAnnotation(nil), t.Annotations...)
		}
		// LogProbs (and their inner slices)
		if t.LogProbs != nil {
			newLP := make([]schemas.ResponsesOutputMessageContentTextLogProb, len(t.LogProbs))
			for i := range t.LogProbs {
				lp := t.LogProbs[i]
				if lp.Bytes != nil {
					lp.Bytes = append([]int(nil), lp.Bytes...)
				}
				if lp.TopLogProbs != nil {
					lp.TopLogProbs = append([]schemas.LogProb(nil), lp.TopLogProbs...)
				}
				newLP[i] = lp
			}
			t.LogProbs = newLP
		}
		copy.ResponsesOutputMessageContentText = &t
	}

	if original.ResponsesOutputMessageContentRefusal != nil {
		copyRefusal := schemas.ResponsesOutputMessageContentRefusal{
			Refusal: original.ResponsesOutputMessageContentRefusal.Refusal,
		}
		copy.ResponsesOutputMessageContentRefusal = &copyRefusal
	}

	return copy
}

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

	_, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	accumulator := a.getOrCreateStreamAccumulator(requestID)
	accumulator.mu.Lock()
	startTimestamp := accumulator.StartTimestamp
	endTimestamp := accumulator.FinalTimestamp
	accumulator.mu.Unlock()

	// For OpenAI-compatible providers, the last chunk already contains the whole accumulated response
	// so just return it as is
	if provider == schemas.OpenAI || provider == schemas.OpenRouter || provider == schemas.Azure {
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
					Latency:        result.GetExtraFields().Latency,
					ErrorDetails:   bifrostErr,
				}

				if bifrostErr != nil {
					data.Status = "error"
				}

				// Extract the complete response from the stream response
				if result.ResponsesStreamResponse.Response != nil {
					data.OutputMessages = result.ResponsesStreamResponse.Response.Output
					if result.ResponsesStreamResponse.Response.Usage != nil {
						// Convert ResponsesResponseUsage to schemas.LLMUsage
						data.TokenUsage = &schemas.BifrostLLMUsage{
							PromptTokens:     result.ResponsesStreamResponse.Response.Usage.InputTokens,
							CompletionTokens: result.ResponsesStreamResponse.Response.Usage.OutputTokens,
							TotalTokens:      result.ResponsesStreamResponse.Response.Usage.TotalTokens,
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
		// Store a deep copy of the stream response to prevent shared data mutation between plugins
		chunk.StreamResponse = deepCopyResponsesStreamResponse(result.ResponsesStreamResponse)

		// Extract token usage from stream response if available
		if result.ResponsesStreamResponse.Response != nil &&
			result.ResponsesStreamResponse.Response.Usage != nil {
			chunk.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     result.ResponsesStreamResponse.Response.Usage.InputTokens,
				CompletionTokens: result.ResponsesStreamResponse.Response.Usage.OutputTokens,
				TotalTokens:      result.ResponsesStreamResponse.Response.Usage.TotalTokens,
			}
		}
	}

	// Add chunk to accumulator synchronously to maintain order
	if result != nil {
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}

	if addErr := a.addResponsesStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
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
