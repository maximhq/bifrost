package bedrock_test

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// incompleteTerminalEvent builds the response.incomplete terminal event that
// FinalizeBedrockStream emits when a Bedrock stream is truncated by max_tokens.
func incompleteTerminalEvent(t *testing.T) *schemas.BifrostResponsesStreamResponse {
	t.Helper()
	state := bedrock.NewBedrockResponsesStreamState()
	state.StopReason = schemas.Ptr("length") // mapped from bedrock's "max_tokens"
	usage := &schemas.ResponsesResponseUsage{InputTokens: 30, OutputTokens: 15, TotalTokens: 45}

	finalResponses := bedrock.FinalizeBedrockStream(state, 0, usage, nil)
	require.NotEmpty(t, finalResponses)
	terminal := finalResponses[len(finalResponses)-1]
	require.Equal(t, schemas.ResponsesStreamResponseTypeIncomplete, terminal.Type)
	return terminal
}

// Regression for #6081 (Anthropic-compatible surface): a max_tokens-truncated
// stream ends with a response.incomplete terminal event, which the Anthropic
// stream encoder silently dropped (no case in its switch), so clients never
// received message_delta (stop_reason + usage) or message_stop.
func TestIncompleteTerminalReachesAnthropicSurface(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	terminal := incompleteTerminalEvent(t)

	events := anthropic.ToAnthropicResponsesStreamResponse(ctx, terminal)
	require.NotEmpty(t, events, "response.incomplete terminal event must not be dropped by the Anthropic stream encoder")

	var messageDelta, messageStop *anthropic.AnthropicStreamEvent
	for _, ev := range events {
		switch ev.Type {
		case anthropic.AnthropicStreamEventTypeMessageDelta:
			messageDelta = ev
		case anthropic.AnthropicStreamEventTypeMessageStop:
			messageStop = ev
		}
	}

	require.NotNil(t, messageDelta, "message_delta must be emitted on truncation")
	require.NotNil(t, messageDelta.Delta)
	require.NotNil(t, messageDelta.Delta.StopReason)
	assert.Equal(t, anthropic.AnthropicStopReasonMaxTokens, *messageDelta.Delta.StopReason)
	require.NotNil(t, messageDelta.Usage, "usage must be carried on the terminal message_delta")
	require.NotNil(t, messageStop, "message_stop must be emitted on truncation")
}

// TestIncompleteTerminalWithoutStopReasonDefaultsToMaxTokens covers the
// defensive default: a response.incomplete terminal event whose Response
// carries no StopReason (a shape FinalizeBedrockStream never produces, but
// the chat-completions bridge or another producer could) must still surface
// max_tokens on both output surfaces instead of end_turn.
func TestIncompleteTerminalWithoutStopReasonDefaultsToMaxTokens(t *testing.T) {
	terminal := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeIncomplete,
		Response: &schemas.BifrostResponsesResponse{
			Status: schemas.Ptr(schemas.ResponsesResponseStatusIncomplete),
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{
				Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens,
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	events := anthropic.ToAnthropicResponsesStreamResponse(ctx, terminal)
	require.NotEmpty(t, events)
	var messageDelta *anthropic.AnthropicStreamEvent
	for _, ev := range events {
		if ev.Type == anthropic.AnthropicStreamEventTypeMessageDelta {
			messageDelta = ev
		}
	}
	require.NotNil(t, messageDelta)
	require.NotNil(t, messageDelta.Delta)
	require.NotNil(t, messageDelta.Delta.StopReason)
	assert.Equal(t, anthropic.AnthropicStopReasonMaxTokens, *messageDelta.Delta.StopReason,
		"missing StopReason on an incomplete terminal must default to max_tokens, not end_turn")

	event, err := bedrock.ToBedrockConverseStreamResponse(terminal)
	require.NoError(t, err)
	require.NotNil(t, event)
	require.NotNil(t, event.StopReason)
	assert.Equal(t, "max_tokens", *event.StopReason)

	// Same default must hold with no IncompleteDetails either, on both surfaces.
	terminal.Response.IncompleteDetails = nil
	events = anthropic.ToAnthropicResponsesStreamResponse(ctx, terminal)
	require.NotEmpty(t, events)
	messageDelta = nil
	for _, ev := range events {
		if ev.Type == anthropic.AnthropicStreamEventTypeMessageDelta {
			messageDelta = ev
		}
	}
	require.NotNil(t, messageDelta)
	require.NotNil(t, messageDelta.Delta)
	require.NotNil(t, messageDelta.Delta.StopReason)
	assert.Equal(t, anthropic.AnthropicStopReasonMaxTokens, *messageDelta.Delta.StopReason)

	event, err = bedrock.ToBedrockConverseStreamResponse(terminal)
	require.NoError(t, err)
	require.NotNil(t, event)
	require.NotNil(t, event.StopReason)
	assert.Equal(t, "max_tokens", *event.StopReason)
}

// Regression for #6081 (Bedrock-native ConverseStream surface): the sibling
// encoder had the same gap, dropping the terminal frame so clients never
// received messageStop{stopReason:"max_tokens"} + metadata usage.
func TestIncompleteTerminalReachesBedrockConverseSurface(t *testing.T) {
	terminal := incompleteTerminalEvent(t)

	event, err := bedrock.ToBedrockConverseStreamResponse(terminal)
	require.NoError(t, err)
	require.NotNil(t, event, "response.incomplete terminal event must not be dropped by the Bedrock stream encoder")

	require.NotNil(t, event.StopReason, "messageStop must carry stopReason")
	assert.Equal(t, "max_tokens", *event.StopReason)
	require.NotNil(t, event.Usage, "metadata usage must be emitted on truncation")
}
