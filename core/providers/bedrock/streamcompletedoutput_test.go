package bedrock_test

import (
	"context"
	"slices"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

// response.function_call_arguments.done is {type, sequence_number, item_id, output_index,
// arguments}. Both Converse close paths attached a partial item with no type, which strict
// Responses clients (opencode v2) reject. The anthropic and bedrock checks pin that the
// reverse converters still rebuild each tool call once the done event stops carrying it.
func TestBedrockResponsesStreamFunctionCallArgumentsDoneShape(t *testing.T) {
	names := []string{"write_file", "read_file"}
	wantArgs := []string{`{"path":"/tmp/probe.txt","content":"OK"}`, `{"path":"/tmp/probe.txt"}`}

	// stopEachBlock false leaves the first tool block open, so the second block's start
	// closes it; the second block always closes on its own contentBlockStop.
	for name, stopEachBlock := range map[string]bool{
		"closed by contentBlockStop":        true,
		"closed by the next block starting": false,
	} {
		t.Run(name, func(t *testing.T) {
			state := bedrock.NewBedrockResponsesStreamState()
			state.Model = new("anthropic.claude-sonnet-4-5-20250929-v1:0")
			var events []*schemas.BifrostResponsesStreamResponse
			emit := func(chunk *bedrock.BedrockStreamEvent) {
				responses, bErr, _ := chunk.ToBifrostResponsesStream(len(events), state)
				require.Nil(t, bErr)
				events = append(events, responses...)
			}
			emit(&bedrock.BedrockStreamEvent{Role: new("assistant")})
			for i := range names {
				emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: new(i), Start: &bedrock.BedrockContentBlockStart{
					ToolUse: &bedrock.BedrockToolUseStart{ToolUseID: "tooluse_" + names[i], Name: names[i]},
				}})
				emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: new(i), Delta: &bedrock.BedrockContentBlockDelta{
					ToolUse: &bedrock.BedrockToolUseDelta{Input: wantArgs[i]},
				}})
				if stopEachBlock || i == len(names)-1 {
					emit(&bedrock.BedrockStreamEvent{ContentBlockIndex: new(i), ContentBlockStop: true})
				}
			}
			emit(&bedrock.BedrockStreamEvent{StopReason: new("tool_use")})
			events = append(events, bedrock.FinalizeBedrockStream(state, len(events), &schemas.ResponsesResponseUsage{InputTokens: 9, OutputTokens: 6, TotalTokens: 15}, nil)...)

			// /v1/responses marshals the event as is; /openai/v1/responses marshals WithDefaults().
			for route, render := range map[string]func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse{
				"v1 responses":        func(ev *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse { return ev },
				"openai v1 responses": (*schemas.BifrostResponsesStreamResponse).WithDefaults,
			} {
				addedIDs := map[int64]string{}
				var doneArgs []string
				for _, ev := range events {
					wire, err := schemas.Marshal(render(ev))
					require.NoError(t, err)
					if item := gjson.GetBytes(wire, "item"); item.Exists() {
						require.True(t, item.Get("type").Type == gjson.String && item.Get("type").Str != "", "%s: %s carries an item without a string type: %s", route, ev.Type, wire)
					}
					if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && gjson.GetBytes(wire, "item.type").Str == "function_call" {
						addedIDs[gjson.GetBytes(wire, "output_index").Int()] = gjson.GetBytes(wire, "item.id").Str
					}
					if ev.Type != schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone {
						continue
					}
					require.False(t, gjson.GetBytes(wire, "item").Exists(), "%s: function_call_arguments.done must not carry an item: %s", route, wire)
					index := gjson.GetBytes(wire, "output_index")
					require.True(t, index.Exists(), "%s: done event lacks output_index: %s", route, wire)
					require.NotEmpty(t, addedIDs[index.Int()], "%s: no output_item.added for %s", route, wire)
					require.Equal(t, addedIDs[index.Int()], gjson.GetBytes(wire, "item_id").Str, "%s: done item_id must match its output_item.added: %s", route, wire)
					doneArgs = append(doneArgs, gjson.GetBytes(wire, "arguments").Str)
				}
				assert.Equal(t, wantArgs, doneArgs, "%s: done arguments", route)
			}

			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			toolNames := map[int]string{}
			inputs := map[int]string{}
			var order []int
			for _, ev := range events {
				for _, out := range anthropic.ToAnthropicResponsesStreamResponse(ctx, ev) {
					if out.Index == nil {
						continue
					}
					if out.ContentBlock != nil && out.ContentBlock.Type == anthropic.AnthropicContentBlockTypeToolUse && out.ContentBlock.Name != nil {
						toolNames[*out.Index] = *out.ContentBlock.Name
						order = append(order, *out.Index)
					}
					if out.Delta != nil && out.Delta.PartialJSON != nil {
						inputs[*out.Index] += *out.Delta.PartialJSON
					}
				}
			}
			require.Len(t, order, len(names), "anthropic: tool_use blocks %v", toolNames)
			for i := range names {
				assert.Equal(t, names[i], toolNames[order[i]], "anthropic: tool_use %d name", i)
				assert.Equal(t, wantArgs[i], inputs[order[i]], "anthropic: tool_use %d input", i)
			}

			starts := map[int]string{}
			deltas := map[int]string{}
			for _, ev := range events {
				out, err := bedrock.ToBedrockConverseStreamResponse(ev)
				require.NoError(t, err)
				if out == nil || out.ContentBlockIndex == nil {
					continue
				}
				if out.Start != nil && out.Start.ToolUse != nil {
					starts[*out.ContentBlockIndex] = out.Start.ToolUse.Name
				}
				if out.Delta != nil && out.Delta.ToolUse != nil {
					deltas[*out.ContentBlockIndex] += out.Delta.ToolUse.Input
				}
			}
			require.Len(t, starts, len(names), "bedrock: toolUse starts %v", starts)
			for index, name := range starts {
				i := slices.Index(names, name)
				require.NotEqual(t, -1, i, "bedrock: unexpected toolUse %s", name)
				assert.Equal(t, wantArgs[i], deltas[index], "bedrock: toolUse %s input", name)
			}

			// /genai read the function name off the done event's item; it now comes from output_item.added.
			genaiState := gemini.NewBifrostToGeminiStreamState()
			var calls []*gemini.FunctionCall
			for _, ev := range events {
				out := gemini.ToGeminiResponsesStreamResponse(ev, genaiState)
				if out == nil || len(out.Candidates) == 0 || out.Candidates[0].Content == nil {
					continue
				}
				for _, part := range out.Candidates[0].Content.Parts {
					if part.FunctionCall != nil {
						calls = append(calls, part.FunctionCall)
					}
				}
			}
			require.Len(t, calls, len(names), "genai: functionCall parts %+v", calls)
			for i, call := range calls {
				assert.Equal(t, names[i], call.Name, "genai: functionCall %d name", i)
				assert.Equal(t, "tooluse_"+names[i], call.ID, "genai: functionCall %d id", i)
				assert.JSONEq(t, wantArgs[i], string(call.Args), "genai: functionCall %d args", i)
			}
		})
	}
}
