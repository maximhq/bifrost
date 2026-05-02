package streaming

import (
	"fmt"
	"maps"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// processPassthroughStreamingResponse handles accumulation of passthrough streaming responses.
// Passthrough responses carry raw bytes, so we accumulate the body and metadata across chunks.
func (a *Accumulator) processPassthroughStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	// Extract accumulator ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
	}

	_, provider, requestedModel, resolvedModel := bifrost.GetResponseFields(result, bifrostErr)
	isFinalChunk := bifrost.IsFinalChunk(ctx)

	// Get or create accumulator for this request
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()

	// Initialize headers map on first chunk
	if accumulator.PassthroughHeaders == nil {
		accumulator.PassthroughHeaders = make(map[string]string)
	}

	// Update status code if present in this chunk
	if result != nil && result.PassthroughResponse != nil && result.PassthroughResponse.StatusCode != 0 {
		accumulator.PassthroughStatusCode = result.PassthroughResponse.StatusCode
	}

	// Accumulate headers if they are present in this chunk (some providers may send headers in multiple chunks)
	if result != nil && result.PassthroughResponse != nil && len(result.PassthroughResponse.Headers) > 0 {
		maps.Copy(accumulator.PassthroughHeaders, result.PassthroughResponse.Headers)
	}

	// Accumulate the body bytes from this chunk
	if result != nil && result.PassthroughResponse != nil && len(result.PassthroughResponse.Body) > 0 {
		// Make a copy of the body bytes to avoid referencing pooled memory
		bodyBytes := make([]byte, len(result.PassthroughResponse.Body))
		copy(bodyBytes, result.PassthroughResponse.Body)
		accumulator.PassthroughBody = append(accumulator.PassthroughBody, bodyBytes...)
	}

	// For non-final chunks, return early without processing
	if !isFinalChunk {
		return &ProcessedStreamResponse{
			RequestID:      requestID,
			StreamType:     StreamTypePassthrough,
			RequestedModel: requestedModel,
			ResolvedModel:  resolvedModel,
			Provider:       provider,
			Data:           nil,
		}, nil
	}

	// Mark accumulator as complete
	if !accumulator.IsComplete {
		accumulator.IsComplete = true
		accumulator.FinalTimestamp = time.Now()
	}

	// Build the accumulated passthrough response
	passthroughResp := &schemas.BifrostPassthroughResponse{
		StatusCode: accumulator.PassthroughStatusCode,
		Headers:    accumulator.PassthroughHeaders,
		Body:       accumulator.PassthroughBody,
	}

	// Build accumulated data with the passthrough response
	data := &AccumulatedData{
		RequestID:         requestID,
		Model:             requestedModel,
		Status:            "success",
		Stream:            true,
		StartTimestamp:    accumulator.StartTimestamp,
		EndTimestamp:      accumulator.FinalTimestamp,
		PassthroughOutput: passthroughResp,
	}

	// Set error status if there was an error
	if bifrostErr != nil {
		data.Status = "error"
		data.ErrorDetails = bifrostErr
	}

	// Set latency and other metadata from final response
	if result != nil {
		extraFields := result.GetExtraFields()
		if extraFields.Latency > 0 {
			data.Latency = extraFields.Latency
		}
		if extraFields.RawResponse != nil {
			rawResp := fmt.Sprintf("%v", extraFields.RawResponse)
			data.RawResponse = &rawResp
		}
		// Populate extra fields on the passthrough response
		passthroughResp.ExtraFields = *extraFields
	}

	var rawRequest interface{}
	if result != nil && result.PassthroughResponse != nil && result.PassthroughResponse.ExtraFields.RawRequest != nil {
		rawRequest = result.PassthroughResponse.ExtraFields.RawRequest
	}

	return &ProcessedStreamResponse{
		RequestID:      requestID,
		StreamType:     StreamTypePassthrough,
		RequestedModel: requestedModel,
		ResolvedModel:  resolvedModel,
		Provider:       provider,
		Data:           data,
		RawRequest:     &rawRequest,
	}, nil
}
