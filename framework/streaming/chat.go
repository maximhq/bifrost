package streaming

import (
	"fmt"
	"sort"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// deepCopyChatStreamDelta creates a deep copy of ChatStreamResponseChoiceDelta
// to prevent shared data mutation between different chunks
func deepCopyChatStreamDelta(original *schemas.ChatStreamResponseChoiceDelta) *schemas.ChatStreamResponseChoiceDelta {
	if original == nil {
		return nil
	}

	copy := &schemas.ChatStreamResponseChoiceDelta{}

	if original.Role != nil {
		copyRole := *original.Role
		copy.Role = &copyRole
	}

	if original.Content != nil {
		copyContent := *original.Content
		copy.Content = &copyContent
	}

	if original.Refusal != nil {
		copyRefusal := *original.Refusal
		copy.Refusal = &copyRefusal
	}

	if original.Reasoning != nil {
		copyReasoning := *original.Reasoning
		copy.Reasoning = &copyReasoning
	}

	// Deep copy ReasoningDetails slice
	if original.ReasoningDetails != nil {
		copy.ReasoningDetails = make([]schemas.ChatReasoningDetails, len(original.ReasoningDetails))
		for i, rd := range original.ReasoningDetails {
			copyRd := schemas.ChatReasoningDetails{
				Index: rd.Index,
				Type:  rd.Type,
			}
			if rd.ID != nil {
				copyID := *rd.ID
				copyRd.ID = &copyID
			}
			if rd.Text != nil {
				copyText := *rd.Text
				copyRd.Text = &copyText
			}
			if rd.Signature != nil {
				copySig := *rd.Signature
				copyRd.Signature = &copySig
			}
			if rd.Summary != nil {
				copySummary := *rd.Summary
				copyRd.Summary = &copySummary
			}
			if rd.Data != nil {
				copyData := *rd.Data
				copyRd.Data = &copyData
			}
			copy.ReasoningDetails[i] = copyRd
		}
	}

	// Deep copy ToolCalls slice
	if original.ToolCalls != nil {
		copy.ToolCalls = make([]schemas.ChatAssistantMessageToolCall, len(original.ToolCalls))
		for i, tc := range original.ToolCalls {
			copyTc := schemas.ChatAssistantMessageToolCall{
				Index:    tc.Index,
				Function: tc.Function, // struct value, safe to copy directly
			}
			if tc.ID != nil {
				copyID := *tc.ID
				copyTc.ID = &copyID
			}
			if tc.Type != nil {
				copyType := *tc.Type
				copyTc.Type = &copyType
			}
			// Deep copy Function's Name pointer
			if tc.Function.Name != nil {
				copyName := *tc.Function.Name
				copyTc.Function.Name = &copyName
			}
			copy.ToolCalls[i] = copyTc
		}
	}

	// Deep copy Audio
	if original.Audio != nil {
		copy.Audio = &schemas.ChatAudioMessageAudio{
			ID:         original.Audio.ID,
			Data:       original.Audio.Data,
			ExpiresAt:  original.Audio.ExpiresAt,
			Transcript: original.Audio.Transcript,
		}
	}

	return copy
}

// buildCompleteMessageFromChunks builds a complete message from accumulated chunks.
// Uses strings.Builder for O(n) accumulation instead of O(n²) string concatenation.
func (a *Accumulator) buildCompleteMessageFromChatStreamChunks(chunks []*ChatStreamChunk) *schemas.ChatMessage {
	completeMessage := &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{},
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})

	// Builders for O(n) accumulation of large text fields
	var contentBuilder strings.Builder
	var refusalBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var audioDataBuilder strings.Builder
	var audioTranscriptBuilder strings.Builder
	hasContent, hasRefusal, hasReasoning := false, false, false

	// Reasoning details builders keyed by detail index
	type rdAccum struct {
		text, summary, data         strings.Builder
		hasText, hasSummary, hasData bool
		typ                         schemas.BifrostReasoningDetailsType
		id, signature               *string
	}
	var rdAccums map[int]*rdAccum

	// Tool call argument builders keyed by delta index
	type tcAccum struct {
		id   *string
		typ  *string
		name *string
		args strings.Builder
	}
	var tcAccums map[uint16]*tcAccum

	for _, chunk := range chunks {
		if chunk.Delta == nil {
			continue
		}
		// Handle role (usually in first chunk)
		if chunk.Delta.Role != nil {
			completeMessage.Role = schemas.ChatMessageRole(*chunk.Delta.Role)
		}
		// Append content delta
		if chunk.Delta.Content != nil && *chunk.Delta.Content != "" {
			contentBuilder.WriteString(*chunk.Delta.Content)
			hasContent = true
		}
		// Handle refusal delta
		if chunk.Delta.Refusal != nil && *chunk.Delta.Refusal != "" {
			refusalBuilder.WriteString(*chunk.Delta.Refusal)
			hasRefusal = true
		}
		// Handle reasoning delta
		if chunk.Delta.Reasoning != nil && *chunk.Delta.Reasoning != "" {
			reasoningBuilder.WriteString(*chunk.Delta.Reasoning)
			hasReasoning = true
		}
		// Handle reasoning details delta
		for _, rd := range chunk.Delta.ReasoningDetails {
			if rdAccums == nil {
				rdAccums = make(map[int]*rdAccum)
			}
			acc, ok := rdAccums[rd.Index]
			if !ok {
				acc = &rdAccum{typ: rd.Type}
				rdAccums[rd.Index] = acc
			}
			if rd.Text != nil && *rd.Text != "" {
				acc.text.WriteString(*rd.Text)
				acc.hasText = true
			}
			if rd.Summary != nil && *rd.Summary != "" {
				acc.summary.WriteString(*rd.Summary)
				acc.hasSummary = true
			}
			if rd.Data != nil && *rd.Data != "" {
				acc.data.WriteString(*rd.Data)
				acc.hasData = true
			}
			if rd.Signature != nil {
				sigCopy := *rd.Signature
				acc.signature = &sigCopy
			}
			if rd.Type != "" {
				acc.typ = rd.Type
			}
			if rd.ID != nil {
				idCopy := *rd.ID
				acc.id = &idCopy
			}
		}
		// Handle audio data
		if chunk.Delta.Audio != nil {
			if completeMessage.ChatAssistantMessage == nil {
				completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
			}
			if completeMessage.ChatAssistantMessage.Audio == nil {
				completeMessage.ChatAssistantMessage.Audio = &schemas.ChatAudioMessageAudio{}
			}
			if chunk.Delta.Audio.Data != "" {
				audioDataBuilder.WriteString(chunk.Delta.Audio.Data)
			}
			if chunk.Delta.Audio.Transcript != "" {
				audioTranscriptBuilder.WriteString(chunk.Delta.Audio.Transcript)
			}
			if chunk.Delta.Audio.ID != "" {
				completeMessage.ChatAssistantMessage.Audio.ID = chunk.Delta.Audio.ID
			}
			if chunk.Delta.Audio.ExpiresAt != 0 {
				completeMessage.ChatAssistantMessage.Audio.ExpiresAt = chunk.Delta.Audio.ExpiresAt
			}
		}
		// Accumulate tool calls by index
		for _, deltaToolCall := range chunk.Delta.ToolCalls {
			if tcAccums == nil {
				tcAccums = make(map[uint16]*tcAccum)
			}
			idx := deltaToolCall.Index
			acc, ok := tcAccums[idx]
			if !ok {
				acc = &tcAccum{}
				tcAccums[idx] = acc
			}
			if deltaToolCall.ID != nil {
				v := *deltaToolCall.ID
				acc.id = &v
			}
			if deltaToolCall.Type != nil {
				t := *deltaToolCall.Type
				acc.typ = &t
			}
			if deltaToolCall.Function.Name != nil {
				n := *deltaToolCall.Function.Name
				acc.name = &n
			}
			if args := deltaToolCall.Function.Arguments; args != "" && args != "{}" {
				acc.args.WriteString(args)
			}
		}
	}

	// Finalize content
	if hasContent {
		str := contentBuilder.String()
		completeMessage.Content.ContentStr = &str
	}

	// Finalize refusal
	if hasRefusal {
		if completeMessage.ChatAssistantMessage == nil {
			completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		str := refusalBuilder.String()
		completeMessage.ChatAssistantMessage.Refusal = &str
	}

	// Finalize reasoning
	if hasReasoning {
		if completeMessage.ChatAssistantMessage == nil {
			completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		str := reasoningBuilder.String()
		completeMessage.ChatAssistantMessage.Reasoning = &str
	}

	// Finalize reasoning details
	if len(rdAccums) > 0 {
		if completeMessage.ChatAssistantMessage == nil {
			completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		// Sort by index for deterministic output
		indices := make([]int, 0, len(rdAccums))
		for idx := range rdAccums {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			acc := rdAccums[idx]
			rd := schemas.ChatReasoningDetails{
				Index:     idx,
				Type:      acc.typ,
				ID:        acc.id,
				Signature: acc.signature,
			}
			if acc.hasText {
				str := acc.text.String()
				rd.Text = &str
			}
			if acc.hasSummary {
				str := acc.summary.String()
				rd.Summary = &str
			}
			if acc.hasData {
				str := acc.data.String()
				rd.Data = &str
			}
			completeMessage.ChatAssistantMessage.ReasoningDetails = append(
				completeMessage.ChatAssistantMessage.ReasoningDetails, rd)
		}
	}

	// Finalize audio
	if completeMessage.ChatAssistantMessage != nil && completeMessage.ChatAssistantMessage.Audio != nil {
		completeMessage.ChatAssistantMessage.Audio.Data = audioDataBuilder.String()
		completeMessage.ChatAssistantMessage.Audio.Transcript = audioTranscriptBuilder.String()
	}

	// Finalize tool calls — sort by original index for deterministic output
	if len(tcAccums) > 0 {
		if completeMessage.ChatAssistantMessage == nil {
			completeMessage.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
		}
		tcIndices := make([]int, 0, len(tcAccums))
		for idx := range tcAccums {
			tcIndices = append(tcIndices, int(idx))
		}
		sort.Ints(tcIndices)
		toolCalls := make([]schemas.ChatAssistantMessageToolCall, 0, len(tcIndices))
		for _, idx := range tcIndices {
			acc := tcAccums[uint16(idx)]
			toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
				Index: uint16(idx),
				ID:    acc.id,
				Type:  acc.typ,
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      acc.name,
					Arguments: acc.args.String(),
				},
			})
		}
		completeMessage.ChatAssistantMessage.ToolCalls = toolCalls
	}

	return completeMessage
}

// processAccumulatedChunks processes all accumulated chunks in order
func (a *Accumulator) processAccumulatedChatStreamingChunks(requestID string, respErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	// Note: Cleanup is handled by CleanupStreamAccumulator when refcount reaches 0
	// This is called from completeDeferredSpan after streaming ends

	// Calculate Time to First Token (TTFT) in milliseconds
	var ttft int64
	if !accumulator.StartTimestamp.IsZero() && !accumulator.FirstChunkTimestamp.IsZero() {
		ttft = accumulator.FirstChunkTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

	// Initialize accumulated data
	data := &AccumulatedData{
		RequestID:        requestID,
		Status:           "success",
		Stream:           true,
		StartTimestamp:   accumulator.StartTimestamp,
		EndTimestamp:     accumulator.FinalTimestamp,
		Latency:          0,
		TimeToFirstToken: ttft,
		OutputMessage:    nil,
		ToolCalls:        nil,
		ErrorDetails:     nil,
		TokenUsage:       nil,
		CacheDebug:       nil,
		Cost:             nil,
	}
	// Build complete message from accumulated chunks
	completeMessage := a.buildCompleteMessageFromChatStreamChunks(accumulator.ChatStreamChunks)
	if !isFinalChunk {
		data.OutputMessage = completeMessage
		return data, nil
	}
	// Update database with complete message
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
	data.OutputMessage = completeMessage
	if data.OutputMessage.ChatAssistantMessage != nil && data.OutputMessage.ChatAssistantMessage.ToolCalls != nil {
		data.ToolCalls = data.OutputMessage.ChatAssistantMessage.ToolCalls
	}
	data.ErrorDetails = respErr
	// Update metadata from the chunk with highest index (contains TokenUsage, Cost, FinishReason)
	if lastChunk := accumulator.getLastChatChunkLocked(); lastChunk != nil {
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = lastChunk.TokenUsage
		}
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
		data.FinishReason = lastChunk.FinishReason
	}
	// Accumulate raw response
	if len(accumulator.ChatStreamChunks) > 0 {
		// Sort chunks by chunk index
		sort.Slice(accumulator.ChatStreamChunks, func(i, j int) bool {
			return accumulator.ChatStreamChunks[i].ChunkIndex < accumulator.ChatStreamChunks[j].ChunkIndex
		})
		for _, chunk := range accumulator.ChatStreamChunks {
			if chunk.RawResponse != nil {
				if data.RawResponse == nil {
					data.RawResponse = bifrost.Ptr(*chunk.RawResponse)
				} else {
					*data.RawResponse += "\n\n" + *chunk.RawResponse
				}
			}
		}
	}
	return data, nil
}

// processChatStreamingResponse processes a chat streaming response
func (a *Accumulator) processChatStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	a.logger.Debug("[streaming] processing chat streaming response")
	// Extract accumulator ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
	}
	requestType, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	streamType := StreamTypeChat
	if requestType == schemas.TextCompletionStreamRequest {
		streamType = StreamTypeText
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getChatStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr
	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.TextCompletionResponse != nil {
		// Handle text completion response directly
		if len(result.TextCompletionResponse.Choices) > 0 {
			choice := result.TextCompletionResponse.Choices[0]

			if choice.TextCompletionResponseChoice != nil {
				deltaCopy := choice.TextCompletionResponseChoice.Text
				chunk.Delta = &schemas.ChatStreamResponseChoiceDelta{
					Content: deltaCopy,
				}
				chunk.FinishReason = choice.FinishReason
			}
		}
		// Extract token usage
		if result.TextCompletionResponse.Usage != nil && result.TextCompletionResponse.Usage.TotalTokens > 0 {
			chunk.TokenUsage = result.TextCompletionResponse.Usage
		}
		chunk.ChunkIndex = result.TextCompletionResponse.ExtraFields.ChunkIndex
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	} else if result != nil && result.ChatResponse != nil {
		// Extract delta and other information
		if len(result.ChatResponse.Choices) > 0 {
			choice := result.ChatResponse.Choices[0]
			if choice.ChatStreamResponseChoice != nil {
				// Deep copy delta to prevent shared data mutation between chunks
				chunk.Delta = deepCopyChatStreamDelta(choice.ChatStreamResponseChoice.Delta)
				chunk.FinishReason = choice.FinishReason
			}
		}
		// Extract token usage
		if result.ChatResponse.Usage != nil && result.ChatResponse.Usage.TotalTokens > 0 {
			chunk.TokenUsage = result.ChatResponse.Usage
		}
		chunk.ChunkIndex = result.ChatResponse.ExtraFields.ChunkIndex
		if result.ChatResponse.ExtraFields.RawResponse != nil {
			chunk.RawResponse = bifrost.Ptr(fmt.Sprintf("%v", result.ChatResponse.ExtraFields.RawResponse))
		}
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}
	if addErr := a.addChatStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add stream chunk for request %s: %w", requestID, addErr)
	}
	// If this is the final chunk, process accumulated chunks
	// Always return data on final chunk - multiple plugins may need the result
	if isFinalChunk {
		// Get the accumulator and mark as complete (idempotent)
		accumulator := a.getOrCreateStreamAccumulator(requestID)
		accumulator.mu.Lock()
		if !accumulator.IsComplete {
			accumulator.IsComplete = true
		}
		accumulator.mu.Unlock()

		// Always process and return data on final chunk
		// Multiple plugins can call this - the processing is idempotent
		data, processErr := a.processAccumulatedChatStreamingChunks(requestID, bifrostErr, isFinalChunk)
		if processErr != nil {
			a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
			return nil, processErr
		}
		var rawRequest interface{}
		if result != nil && result.ChatResponse != nil && result.ChatResponse.ExtraFields.RawRequest != nil {
			rawRequest = result.ChatResponse.ExtraFields.RawRequest
		} else if result != nil && result.TextCompletionResponse != nil && result.TextCompletionResponse.ExtraFields.RawRequest != nil {
			rawRequest = result.TextCompletionResponse.ExtraFields.RawRequest
		}
		return &ProcessedStreamResponse{
			RequestID:  requestID,
			StreamType: streamType,
			Provider:   provider,
			Model:      model,
			Data:       data,
			RawRequest: &rawRequest,
		}, nil
	}
	// This is going to be a delta response
	data, processErr := a.processAccumulatedChatStreamingChunks(requestID, bifrostErr, isFinalChunk)
	if processErr != nil {
		a.logger.Error("failed to process accumulated chunks for request %s: %v", requestID, processErr)
		return nil, processErr
	}
	// This is not the final chunk, so we will send back the delta
	return &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: streamType,
		Provider:   provider,
		Model:      model,
		Data:       data,
	}, nil
}
