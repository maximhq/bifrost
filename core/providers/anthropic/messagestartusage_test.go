package anthropic

import (
	"context"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// TestToAnthropicResponsesStreamResponse_EmitsZeroUsageWhenUnknown is the regression test
// for #5885. Bedrock Converse (and every other non-Anthropic provider, which reaches this
// converter through providerUtils.SendCreatedEventResponsesChunk's empty
// BifrostResponsesResponse) has no usage figures at message_start time. #5821 responded by
// omitting the field entirely; because AnthropicMessageResponse.Usage is a pointer tagged
// omitempty, that drops the key off the wire rather than emitting null, and
// @ai-sdk/anthropic — whose message_start schema marks usage and usage.input_tokens as
// required while id/model/role are nullish (dist/index.js in every published 4.0.6→4.0.32)
// — aborts the stream on the very first frame.
//
// Zeros rather than Anthropic's own output_tokens:1 placeholder: message_start usage is
// provisional by contract (the docs call message_delta's counts cumulative and
// authoritative), but Bifrost's Bedrock message_delta reports absolute totals, so a client
// that sums the two would over-count by one. Zero is neutral under both readings, and
// Bifrost's own passthrough accumulator is a max-merge, so a zero can never displace a real
// figure later in the stream.
func TestToAnthropicResponsesStreamResponse_EmitsZeroUsageWhenUnknown(t *testing.T) {
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
	if events[0].Message.Usage == nil {
		t.Fatalf("expected Usage to be present (all-zero) when unknown, got nil")
	}
	if got := events[0].Message.Usage.InputTokens; got != 0 {
		t.Errorf("expected InputTokens=0, got %d", got)
	}
	if got := events[0].Message.Usage.OutputTokens; got != 0 {
		t.Errorf("expected OutputTokens=0, got %d", got)
	}
}

// TestToAnthropicResponsesStreamResponse_MessageStartUsageSerializes asserts on the marshaled
// frame, not just the struct: the #5885 break lived entirely in the `omitempty` interaction,
// so a nil-check alone would not have caught it. These are exactly the keys
// @ai-sdk/anthropic's zod schema requires to be present numbers.
func TestToAnthropicResponsesStreamResponse_MessageStartUsageSerializes(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
		Response: &schemas.BifrostResponsesResponse{
			ID:    schemas.Ptr("resp_1"),
			Model: "claude-sonnet-4-6",
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, resp)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	raw, err := providerUtils.MarshalSorted(events[0])
	if err != nil {
		t.Fatalf("marshal message_start: %v", err)
	}

	usage := gjson.GetBytes(raw, "message.usage")
	if !usage.Exists() {
		t.Fatalf("message.usage key must be present on the wire, got frame: %s", raw)
	}
	inputTokens := gjson.GetBytes(raw, "message.usage.input_tokens")
	if inputTokens.Type != gjson.Number {
		t.Errorf("message.usage.input_tokens must be a number, got %q in frame: %s", inputTokens.Raw, raw)
	}
	if inputTokens.Int() != 0 {
		t.Errorf("expected input_tokens=0, got %d", inputTokens.Int())
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
