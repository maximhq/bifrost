package streaming

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestProcessResponsesStreamingResponse_FinalChunkProducesUsage(t *testing.T) {
	acc := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelInfo))

	baseCtx := context.Background()
	baseCtx = context.WithValue(baseCtx, schemas.BifrostContextKeyRequestID, "req-123")

	// Simulate a non-final chunk
	nonFinalCtx := baseCtx
	resultChunk := &schemas.BifrostResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4.1",
			ChunkIndex:     1,
		},
		ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
			SequenceNumber: 1,
			Response:       &schemas.ResponsesStreamResponseStruct{},
		},
	}

	if resp, err := acc.processResponsesStreamingResponse(&nonFinalCtx, resultChunk, nil); err != nil {
		t.Fatalf("processResponsesStreamingResponse (non-final) returned error: %v", err)
	} else if resp != nil {
		t.Fatalf("expected nil response for non-final chunk, got %#v", resp)
	}

	// Simulate the final chunk with usage information
	finalCtx := context.WithValue(baseCtx, schemas.BifrostContextKeyStreamEndIndicator, true)
	finalUsage := &schemas.ResponsesResponseUsage{
		ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
			InputTokens:  5,
			OutputTokens: 7,
		},
		TotalTokens: 12,
	}
	finalResponse := &schemas.ResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("final answer"),
				},
			},
		},
		CreatedAt: int(time.Now().Unix()),
	}

	finalChunk := &schemas.BifrostResponse{
		Model: "gpt-4.1",
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       schemas.OpenAI,
			ModelRequested: "gpt-4.1",
			ChunkIndex:     2,
		},
		ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: 2,
			Response: &schemas.ResponsesStreamResponseStruct{
				ResponsesResponse: finalResponse,
				Usage:             finalUsage,
			},
		},
	}

	streamResp, err := acc.processResponsesStreamingResponse(&finalCtx, finalChunk, nil)
	if err != nil {
		t.Fatalf("processResponsesStreamingResponse (final) returned error: %v", err)
	}
	if streamResp == nil {
		t.Fatalf("expected processed stream response for final chunk, got nil")
	}
	if streamResp.Type != StreamResponseTypeFinal {
		t.Fatalf("expected final stream response type, got %s", streamResp.Type)
	}
	if streamResp.Data == nil {
		t.Fatalf("expected accumulated data in stream response")
	}
	if streamResp.Data.ResponsesOutput == nil {
		t.Fatalf("expected responses output in accumulated data")
	}
	if streamResp.Data.TokenUsage == nil {
		t.Fatalf("expected token usage in accumulated data")
	}
	if streamResp.Data.TokenUsage.TotalTokens != 12 {
		t.Fatalf("expected total tokens 12, got %d", streamResp.Data.TokenUsage.TotalTokens)
	}
	if streamResp.Data.OutputMessage == nil || streamResp.Data.OutputMessage.Content == nil || streamResp.Data.OutputMessage.Content.ContentStr == nil {
		t.Fatalf("expected output message to be derived from responses output")
	}

	// Ensure ToBifrostResponse materialises usage and responses payload
	bifrostResp := streamResp.ToBifrostResponse()
	if bifrostResp.Usage == nil || bifrostResp.Usage.TotalTokens != 12 {
		t.Fatalf("expected bifrost response usage total tokens to be 12")
	}
	if bifrostResp.ResponsesResponse == nil || len(bifrostResp.ResponsesResponse.Output) == 0 {
		t.Fatalf("expected bifrost response to contain responses output")
	}
}
