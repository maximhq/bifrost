package streaming

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
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

	// Save path from the first chunk that carries it (set once in ExtraFields by the provider)
	if accumulator.PassthroughPath == "" && result != nil && result.PassthroughResponse != nil {
		accumulator.PassthroughPath = result.PassthroughResponse.ExtraFields.PassthroughPath
	}

	// Accumulate the body bytes from this chunk once: several plugins feed the same chunk, some from goroutines
	if result != nil && result.PassthroughResponse != nil && len(result.PassthroughResponse.Body) > 0 {
		chunkIndex := result.PassthroughResponse.ExtraFields.ChunkIndex
		if _, seen := accumulator.PassthroughChunks[chunkIndex]; !seen {
			if accumulator.PassthroughChunks == nil {
				accumulator.PassthroughChunks = make(map[int][]byte)
			}
			// Make a copy of the body bytes to avoid referencing pooled memory
			accumulator.PassthroughChunks[chunkIndex] = bytes.Clone(result.PassthroughResponse.Body)
		}
	}

	// For non-final chunks, return early without processing
	if !isFinalChunk {
		return &ProcessedStreamResponse{
			RequestID:      requestID,
			StreamType:     StreamTypePassthrough,
			RequestedModel: requestedModel,
			ResolvedModel:  resolvedModel,
			RoutingInfo:    bifrost.GetResponseRoutingInfo(result, bifrostErr),
			Provider:       provider,
			Data:           nil,
		}, nil
	}

	// Mark accumulator as complete
	if !accumulator.IsComplete {
		accumulator.IsComplete = true
		accumulator.FinalTimestamp = time.Now()
	}

	// PassthroughUsage is set by the provider on the final EOF chunk before any
	// plugin runs — read it from result rather than re-extracting here.
	var passthroughUsage *schemas.BifrostPassthroughUsage
	if result != nil && result.PassthroughResponse != nil {
		passthroughUsage = result.PassthroughResponse.PassthroughUsage
	}

	// Build the accumulated passthrough response
	passthroughResp := &schemas.BifrostPassthroughResponse{
		StatusCode:       accumulator.PassthroughStatusCode,
		Headers:          accumulator.PassthroughHeaders,
		Body:             accumulator.passthroughBody(),
		Path:             accumulator.PassthroughPath,
		PassthroughUsage: passthroughUsage,
	}

	// Build accumulated data with the passthrough response.
	// Populate TokenUsage from PassthroughUsage.LLMUsage so applyStreamingOutputToEntry sets
	// entry.TokenUsageParsed via the standard streaming token path.
	data := &AccumulatedData{
		RequestID:         requestID,
		Model:             requestedModel,
		Status:            "success",
		Stream:            true,
		StartTimestamp:    accumulator.StartTimestamp,
		EndTimestamp:      accumulator.FinalTimestamp,
		PassthroughOutput: passthroughResp,
	}
	if passthroughUsage != nil && passthroughUsage.LLMUsage != nil {
		data.TokenUsage = passthroughUsage.LLMUsage
	}
	if passthroughUsage != nil && passthroughUsage.ServiceTier != nil {
		data.ServiceTier = passthroughUsage.ServiceTier
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
		RoutingInfo:    bifrost.GetResponseRoutingInfo(result, bifrostErr),
		Provider:       provider,
		Data:           data,
		RawRequest:     &rawRequest,
	}, nil
}

// passthroughBody joins the accumulated passthrough chunks in ChunkIndex order, or returns nil when none arrived.
// Callers hold sa.mu.
func (sa *StreamAccumulator) passthroughBody() []byte {
	if len(sa.PassthroughChunks) == 0 {
		return nil
	}
	size := 0
	for _, chunk := range sa.PassthroughChunks {
		size += len(chunk)
	}
	body := make([]byte, 0, size)
	for _, chunkIndex := range slices.Sorted(maps.Keys(sa.PassthroughChunks)) {
		body = append(body, sa.PassthroughChunks[chunkIndex]...)
	}
	return body
}

// passthroughSnapshot returns the passthrough response accumulated so far, or nil when none has. Body and headers are
// copies, so the caller may read them while chunks keep arriving.
func (sa *StreamAccumulator) passthroughSnapshot() *schemas.BifrostResponse {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if len(sa.PassthroughChunks) == 0 {
		return nil
	}
	return &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
		StatusCode: sa.PassthroughStatusCode,
		Headers:    maps.Clone(sa.PassthroughHeaders),
		Body:       sa.passthroughBody(),
		Path:       sa.PassthroughPath,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:     schemas.PassthroughStreamRequest,
			PassthroughPath: sa.PassthroughPath,
		},
	}}
}
