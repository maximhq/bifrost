package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToAnthropicResponsesStreamResponse_OmitsUsageWhenUnknown is a regression test:
// Bedrock Converse only reports usage on its terminal event (unlike native Anthropic,
// which puts input_tokens on message_start too — see the doc comment on
// bedrock/invoke.go's toAnthropicInvokeStreamBytes). Before the fix, a nil
// bifrostResp.Response.Usage at message_start time was papered over with a fabricated
// all-zero AnthropicUsage, which misrepresents cost/usage telemetry to clients that
// read input_tokens off message_start (as Claude Code does against real Anthropic).
// Anthropic's own documented streaming contract tolerates usage being entirely absent
// from message_start (see the "Streaming request with thinking" example at
// https://platform.claude.com/docs/en/build-with-claude/streaming), and Bifrost's own
// sibling code path (bedrock/invoke.go toAnthropicInvokeStreamBytes) already omits it
// for the identical situation — this test brings responses.go in line.
func TestToAnthropicResponsesStreamResponse_OmitsUsageWhenUnknown(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
		Response: &schemas.BifrostResponsesResponse{
			ID:    schemas.Ptr("resp_1"),
			Model: "claude-sonnet-4-6",
			// Usage intentionally nil — the Bedrock-backed "unknown yet" case.
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, resp)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Message == nil {
		t.Fatalf("expected a message_start event with a Message, got nil")
	}
	if events[0].Message.Usage != nil {
		t.Errorf("expected Usage to be omitted (nil) when unknown, got %+v", events[0].Message.Usage)
	}
}

// TestToAnthropicResponsesStreamResponse_PreservesRealUsage guards against
// regressing the native-Anthropic case while fixing the above: when usage IS known
// at message_start time (e.g. real Anthropic, which tokenizes input before it starts
// streaming), the real values must still pass through unchanged.
func TestToAnthropicResponsesStreamResponse_PreservesRealUsage(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
		Response: &schemas.BifrostResponsesResponse{
			ID:    schemas.Ptr("resp_1"),
			Model: "claude-sonnet-4-6",
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  42,
				OutputTokens: 1,
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, resp)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Message == nil || events[0].Message.Usage == nil {
		t.Fatalf("expected a message_start event with real Usage, got %+v", events[0].Message)
	}
	if events[0].Message.Usage.InputTokens != 42 {
		t.Errorf("expected InputTokens=42, got %d", events[0].Message.Usage.InputTokens)
	}
}
