package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Issue #5936: Bedrock's stopReason is a closed enum (end_turn, tool_use,
// max_tokens, stop_sequence, guardrail_intervened, content_filtered). The
// IncompleteDetails fallback forwarded Bifrost's internal reasons
// ("max_output_tokens", "content_filter") verbatim, putting values outside the
// documented union on the wire for exactly the truncated/filtered turns where
// strict typed clients need a clean terminal signal.

// streamMessageStopReason encodes the chunks and returns the messageStop
// event's stopReason.
func streamMessageStopReason(t *testing.T, chunks []*schemas.BifrostResponsesStreamResponse) string {
	t.Helper()
	events := encodeConverseStream(t, chunks)
	for _, event := range events {
		if event.EventType != "messageStop" {
			continue
		}
		messageStop, ok := event.Payload.(BedrockMessageStopEvent)
		if !ok {
			t.Fatalf("messageStop payload has unexpected type %T", event.Payload)
		}
		return messageStop.StopReason
	}
	t.Fatal("messageStop event not found")
	return ""
}

func TestConverseStreamIncompleteReasonMapsToBedrockEnum(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{schemas.ResponsesResponseIncompleteReasonMaxOutputTokens, "max_tokens"},
		{schemas.ResponsesResponseIncompleteReasonContentFilter, "content_filtered"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			chunks := []*schemas.BifrostResponsesStreamResponse{
				{Type: schemas.ResponsesStreamResponseTypeCreated},
				{
					Type: schemas.ResponsesStreamResponseTypeCompleted,
					Response: &schemas.BifrostResponsesResponse{
						IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: tc.reason},
					},
				},
			}
			if got := streamMessageStopReason(t, chunks); got != tc.want {
				t.Errorf("messageStop stopReason: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestConverseIncompleteReasonMapsToBedrockEnum(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   string
	}{
		{schemas.ResponsesResponseIncompleteReasonMaxOutputTokens, "max_tokens"},
		{schemas.ResponsesResponseIncompleteReasonContentFilter, "content_filtered"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			bifrostResp := &schemas.BifrostResponsesResponse{
				IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: tc.reason},
			}
			bedrockResp, err := ToBedrockConverseResponse(bifrostResp)
			if err != nil {
				t.Fatalf("convert response: %v", err)
			}
			if bedrockResp == nil {
				t.Fatal("converted response is nil")
			}
			if bedrockResp.StopReason != tc.want {
				t.Errorf("stopReason: want %q, got %q", tc.want, bedrockResp.StopReason)
			}
		})
	}
}
