package bedrock_test

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driveBedrockTextStream replays the Converse event sequence AWS sends for a
// plain text answer ("Hello world") and returns every emitted Responses event
// plus the state the terminal event is finalized from.
func driveBedrockTextStream(t *testing.T) ([]*schemas.BifrostResponsesStreamResponse, *bedrock.BedrockResponsesStreamState) {
	t.Helper()

	state := bedrock.NewBedrockResponsesStreamState()
	state.Model = schemas.Ptr("anthropic.claude-3-5-sonnet-20241022-v2:0")

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0

	emit := func(chunk *bedrock.BedrockStreamEvent) {
		responses, bErr, _ := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bErr, "no event in a clean text stream may produce an error")
		all = append(all, responses...)
		seq += len(responses)
	}

	// messageStart
	emit(&bedrock.BedrockStreamEvent{Role: schemas.Ptr("assistant")})
	// contentBlockDelta x2 — these are the deltas the customer sees arrive fine
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta:             &bedrock.BedrockContentBlockDelta{Text: schemas.Ptr("Hello")},
	})
	emit(&bedrock.BedrockStreamEvent{
		ContentBlockIndex: schemas.Ptr(0),
		Delta:             &bedrock.BedrockContentBlockDelta{Text: schemas.Ptr(" world")},
	})
	// contentBlockStop
	emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), ContentBlockStop: true})
	// messageStop
	emit(&bedrock.BedrockStreamEvent{StopReason: schemas.Ptr("end_turn")})

	usage := &schemas.ResponsesResponseUsage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15}
	all = append(all, bedrock.FinalizeBedrockStream(state, seq, usage, nil)...)

	return all, state
}

// TestBedrockResponsesStreamCompletedCarriesOutput pins the OpenAI Responses
// contract that the terminal response.completed event embeds the full output
// array. Bedrock streams the text through output_text.delta / output_item.done
// but FinalizeBedrockStream builds its terminal BifrostResponsesResponse from
// ID/CreatedAt/Usage only, so Output is left nil and the assistant turn vanishes
// from the completed event.
func TestBedrockResponsesStreamCompletedCarriesOutput(t *testing.T) {
	all, _ := driveBedrockTextStream(t)

	var terminal *schemas.BifrostResponsesStreamResponse
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted {
			terminal = r
		}
	}
	require.NotNil(t, terminal, "stream must end with a response.completed event")
	require.NotNil(t, terminal.Response, "response.completed must embed a response object")

	// Sanity: the deltas really did stream, so this is not an empty generation.
	var streamedText string
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && r.Delta != nil {
			streamedText += *r.Delta
		}
	}
	require.Equal(t, "Hello world", streamedText, "precondition: text deltas must have streamed")

	require.NotNil(t, terminal.Response.Output,
		"response.completed must carry the output array, not nil (nil marshals to \"output\":null)")
	require.NotEmpty(t, terminal.Response.Output,
		"response.completed output must contain the assistant message that was streamed")

	msg := terminal.Response.Output[0]
	require.NotNil(t, msg.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *msg.Type)
	require.NotNil(t, msg.Content)
	require.NotNil(t, msg.Content.ContentBlocks)
	require.NotEmpty(t, msg.Content.ContentBlocks)
	require.NotNil(t, msg.Content.ContentBlocks[0].Text)
	assert.Equal(t, "Hello world", *msg.Content.ContentBlocks[0].Text,
		"the completed output must reproduce the streamed text")
}

// TestBedrockResponsesStreamCompletedWireShape pins the bytes an OpenAI client
// actually receives on /v1/responses. The route converter calls WithDefaults()
// before marshalling, so this asserts the serialized terminal event carries a
// populated JSON array under "output" — never null, and never an empty array
// that drops the assistant turn.
func TestBedrockResponsesStreamCompletedWireShape(t *testing.T) {
	all, _ := driveBedrockTextStream(t)

	var terminal *schemas.BifrostResponsesStreamResponse
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted {
			terminal = r
		}
	}
	require.NotNil(t, terminal)

	// Exactly what integrations/openai.go does for POST /v1/responses.
	converted := terminal.WithDefaults()
	require.NotNil(t, converted)

	wire, err := sonic.Marshal(converted)
	require.NoError(t, err)

	t.Logf("terminal response.completed wire bytes: %s", string(wire))

	var envelope struct {
		Type     string `json:"type"`
		Response *struct {
			Output []map[string]any `json:"output"`
		} `json:"response"`
	}
	require.NoError(t, sonic.Unmarshal(wire, &envelope))
	require.Equal(t, "response.completed", envelope.Type)
	require.NotNil(t, envelope.Response)

	assert.NotNil(t, envelope.Response.Output,
		"wire \"output\" must not be null - the OpenAI Agents SDK requires a list")
	assert.NotEmpty(t, envelope.Response.Output,
		"wire \"output\" must contain the streamed assistant message")
}
