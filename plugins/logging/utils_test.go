package logging

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/streaming"
)

// TestConvertToProcessedStreamResponseWebSocketResponsesRequest verifies that
// WebSocketResponsesRequest routes to StreamTypeResponses, not StreamTypeChat.
func TestConvertToProcessedStreamResponseWebSocketResponsesRequest(t *testing.T) {
	result := &schemas.StreamAccumulatorResult{
		RequestID:      "req-ws-001",
		RequestedModel: "gpt-4o",
		Provider:       schemas.OpenAI,
		Status:         "success",
	}

	got := convertToProcessedStreamResponse(result, schemas.WebSocketResponsesRequest)

	if got == nil {
		t.Fatal("expected non-nil ProcessedStreamResponse")
	}
	if got.StreamType != streaming.StreamTypeResponses {
		t.Errorf("StreamType = %q, want %q", got.StreamType, streaming.StreamTypeResponses)
	}
}

// TestConvertToProcessedStreamResponseResponsesStreamRequest verifies the existing
// ResponsesStreamRequest case still routes to StreamTypeResponses (regression guard).
func TestConvertToProcessedStreamResponseResponsesStreamRequest(t *testing.T) {
	result := &schemas.StreamAccumulatorResult{
		RequestID:      "req-resp-001",
		RequestedModel: "gpt-4o",
		Provider:       schemas.OpenAI,
		Status:         "success",
	}

	got := convertToProcessedStreamResponse(result, schemas.ResponsesStreamRequest)

	if got == nil {
		t.Fatal("expected non-nil ProcessedStreamResponse")
	}
	if got.StreamType != streaming.StreamTypeResponses {
		t.Errorf("StreamType = %q, want %q", got.StreamType, streaming.StreamTypeResponses)
	}
}

// TestConvertToProcessedStreamResponseWebSocketMatchesResponsesStream verifies that
// WebSocketResponsesRequest and ResponsesStreamRequest produce equivalent StreamType values.
func TestConvertToProcessedStreamResponseWebSocketMatchesResponsesStream(t *testing.T) {
	result := &schemas.StreamAccumulatorResult{
		RequestID:      "req-compare-001",
		RequestedModel: "gpt-4o",
		Provider:       schemas.OpenAI,
		Status:         "success",
	}

	wsResp := convertToProcessedStreamResponse(result, schemas.WebSocketResponsesRequest)
	httpResp := convertToProcessedStreamResponse(result, schemas.ResponsesStreamRequest)

	if wsResp == nil || httpResp == nil {
		t.Fatal("expected non-nil responses for both request types")
	}
	if wsResp.StreamType != httpResp.StreamType {
		t.Errorf("StreamType mismatch: WebSocketResponsesRequest=%q, ResponsesStreamRequest=%q",
			wsResp.StreamType, httpResp.StreamType)
	}
}

// TestConvertToProcessedStreamResponseNilResult verifies nil input returns nil output.
func TestConvertToProcessedStreamResponseNilResult(t *testing.T) {
	got := convertToProcessedStreamResponse(nil, schemas.WebSocketResponsesRequest)
	if got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

// TestConvertToProcessedStreamResponseDefaultFallback verifies unknown request types
// still route to StreamTypeChat (existing behaviour).
func TestConvertToProcessedStreamResponseDefaultFallback(t *testing.T) {
	result := &schemas.StreamAccumulatorResult{
		RequestID: "req-unknown-001",
	}

	got := convertToProcessedStreamResponse(result, schemas.RequestType("unknown_type"))

	if got == nil {
		t.Fatal("expected non-nil ProcessedStreamResponse")
	}
	if got.StreamType != streaming.StreamTypeChat {
		t.Errorf("StreamType = %q, want %q (default fallback)", got.StreamType, streaming.StreamTypeChat)
	}
}
