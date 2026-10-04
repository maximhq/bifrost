package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Translated upstreams report a natural stop on tool-call turns: Gemini maps
// finishReason STOP to "stop", and OpenAI-shaped Responses providers send no stop
// reason at all. Anthropic clients dispatch tools on stop_reason == "tool_use", so
// the Anthropic egress must report tool_use for such turns, both streaming and not,
// without overriding truncation or refusal.

// stopReasonTestFunctionCall returns a completed get_weather function_call output item.
func stopReasonTestFunctionCall() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr("call_1"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    schemas.Ptr("call_1"),
			Name:      schemas.Ptr("get_weather"),
			Arguments: schemas.Ptr(`{"city":"Paris"}`),
		},
	}
}

// stopReasonTestText returns an assistant text output message with no tool call.
func stopReasonTestText() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr("msg_1"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr("Let me check."),
			}},
		},
	}
}

// maxOutputTokensDetails returns incomplete_details for a max_output_tokens truncation.
func maxOutputTokensDetails() *schemas.ResponsesResponseIncompleteDetails {
	return &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens}
}

// TestToAnthropicResponsesResponse_ToolCallTurnReportsToolUse checks the non-streaming
// egress promotes a natural stop on a tool-call turn to tool_use while leaving
// truncation and pause_turn untouched.
func TestToAnthropicResponsesResponse_ToolCallTurnReportsToolUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stopReason *string
		incomplete *schemas.ResponsesResponseIncompleteDetails
		output     []schemas.ResponsesMessage
		want       AnthropicStopReason
	}{
		{
			name:       "gemini-shaped: explicit stop with function call",
			stopReason: schemas.Ptr("stop"),
			output:     []schemas.ResponsesMessage{stopReasonTestText(), stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonToolUse,
		},
		{
			name:   "openai-shaped: no stop reason with function call",
			output: []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:   AnthropicStopReasonToolUse,
		},
		{
			name:       "explicit stop without tool call stays end_turn",
			stopReason: schemas.Ptr("stop"),
			output:     []schemas.ResponsesMessage{stopReasonTestText()},
			want:       AnthropicStopReasonEndTurn,
		},
		{
			name:       "length with function call stays max_tokens",
			stopReason: schemas.Ptr("length"),
			output:     []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "incomplete max_output_tokens with function call stays max_tokens",
			incomplete: maxOutputTokensDetails(),
			output:     []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "pause_turn with function call is preserved",
			stopReason: schemas.Ptr(string(AnthropicStopReasonPauseTurn)),
			output:     []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:       AnthropicStopReasonPauseTurn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			result := ToAnthropicResponsesResponse(ctx, &schemas.BifrostResponsesResponse{
				ID:                schemas.Ptr("resp_1"),
				Model:             "gemini-2.5-pro",
				StopReason:        tt.stopReason,
				IncompleteDetails: tt.incomplete,
				Output:            tt.output,
			})
			if result.StopReason != tt.want {
				t.Errorf("stop_reason = %q, want %q", result.StopReason, tt.want)
			}
		})
	}
}

// TestToAnthropicResponsesStreamResponse_ToolCallTurnReportsToolUse checks the
// streaming message_delta reports tool_use when a tool_use block was streamed or the
// completed output carries a tool call, without overriding truncation.
func TestToAnthropicResponsesStreamResponse_ToolCallTurnReportsToolUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		streamTool bool // stream a function_call item before response.completed
		stopReason *string
		incomplete *schemas.ResponsesResponseIncompleteDetails
		output     []schemas.ResponsesMessage // Output carried on response.completed
		want       AnthropicStopReason
	}{
		{
			name:       "gemini-shaped: streamed function call, completed with stop",
			provider:   schemas.Gemini,
			streamTool: true,
			stopReason: schemas.Ptr("stop"),
			want:       AnthropicStopReasonToolUse,
		},
		{
			name:       "openai-shaped: streamed function call, completed without stop reason",
			provider:   schemas.OpenAI,
			streamTool: true,
			want:       AnthropicStopReasonToolUse,
		},
		{
			name:     "openai-shaped: function call only in completed output",
			provider: schemas.OpenAI,
			output:   []schemas.ResponsesMessage{stopReasonTestFunctionCall()},
			want:     AnthropicStopReasonToolUse,
		},
		{
			name:       "text-only turn stays end_turn",
			provider:   schemas.Gemini,
			stopReason: schemas.Ptr("stop"),
			output:     []schemas.ResponsesMessage{stopReasonTestText()},
			want:       AnthropicStopReasonEndTurn,
		},
		{
			name:       "length with streamed function call stays max_tokens",
			provider:   schemas.Gemini,
			streamTool: true,
			stopReason: schemas.Ptr("length"),
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "incomplete max_output_tokens with streamed function call stays max_tokens",
			provider:   schemas.OpenAI,
			streamTool: true,
			incomplete: maxOutputTokensDetails(),
			want:       AnthropicStopReasonMaxTokens,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			extra := schemas.BifrostResponseExtraFields{Provider: tt.provider}
			var frames []*schemas.BifrostResponsesStreamResponse
			if tt.streamTool {
				item := stopReasonTestFunctionCall()
				item.Arguments = nil
				frames = append(frames,
					&schemas.BifrostResponsesStreamResponse{
						Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
						OutputIndex: schemas.Ptr(0),
						Item:        &item,
						ExtraFields: extra,
					},
					&schemas.BifrostResponsesStreamResponse{
						Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
						OutputIndex: schemas.Ptr(0),
						ItemID:      item.ID,
						Delta:       schemas.Ptr(`{"city":"Paris"}`),
						ExtraFields: extra,
					},
					&schemas.BifrostResponsesStreamResponse{
						Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
						OutputIndex: schemas.Ptr(0),
						Item:        &item,
						ExtraFields: extra,
					},
				)
			}
			frames = append(frames, &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeCompleted,
				Response: &schemas.BifrostResponsesResponse{
					ID:                schemas.Ptr("resp_1"),
					StopReason:        tt.stopReason,
					IncompleteDetails: tt.incomplete,
					Output:            tt.output,
				},
				ExtraFields: extra,
			})

			var messageDelta *AnthropicStreamEvent
			for _, event := range driveAnthropicEgress(t, frames) {
				if event != nil && event.Type == AnthropicStreamEventTypeMessageDelta {
					messageDelta = event
				}
			}
			if messageDelta == nil || messageDelta.Delta == nil || messageDelta.Delta.StopReason == nil {
				t.Fatalf("expected a message_delta carrying stop_reason, got %#v", messageDelta)
			}
			if got := *messageDelta.Delta.StopReason; got != tt.want {
				t.Errorf("stop_reason = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestToAnthropicResponsesStreamResponse_CompletedStopSequenceIsNull guards the
// message_delta wire shape: with no stop sequence matched, Anthropic sends
// "stop_sequence": null, never an empty string.
func TestToAnthropicResponsesStreamResponse_CompletedStopSequenceIsNull(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1")},
	})
	if len(events) != 2 || events[0].Type != AnthropicStreamEventTypeMessageDelta {
		t.Fatalf("expected message_delta + message_stop, got %#v", events)
	}
	raw, err := json.Marshal(events[0].Delta)
	if err != nil {
		t.Fatalf("marshal message_delta delta: %v", err)
	}
	if !strings.Contains(string(raw), `"stop_sequence":null`) {
		t.Errorf("message_delta delta = %s, want \"stop_sequence\":null", raw)
	}
}

// Issue #4065 routes /anthropic/v1/messages to an Azure OpenAI gpt-4o deployment.
// Azure serves that model either from its native /openai/v1/responses route
// (decoded straight into the Bifrost Responses schema, like OpenAI) or, for
// deployments without a Responses endpoint, from chat completions converted to
// Responses. The tests below replay both shapes from the Azure wire JSON through
// the same converters the Azure provider uses, into the Anthropic egress.

// azureResponsesToolCallStreamEvents is a gpt-4o tool-call turn as streamed by
// Azure's /openai/v1/responses route: response.completed carries the function_call
// output and status "completed" but no stop reason.
var azureResponsesToolCallStreamEvents = []string{
	`{"type":"response.created","sequence_number":0,"response":{"id":"resp_az1","object":"response","created_at":1759500000,"status":"in_progress","model":"gpt-4o","output":[]}}`,
	`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_az1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_az1","name":"get_weather"}}`,
	`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_az1","output_index":0,"delta":"{\"city\":"}`,
	`{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_az1","output_index":0,"delta":"\"Paris\"}"}`,
	`{"type":"response.function_call_arguments.done","sequence_number":4,"item_id":"fc_az1","output_index":0,"arguments":"{\"city\":\"Paris\"}"}`,
	`{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"id":"fc_az1","type":"function_call","status":"completed","arguments":"{\"city\":\"Paris\"}","call_id":"call_az1","name":"get_weather"}}`,
	`{"type":"response.completed","sequence_number":6,"response":{"id":"resp_az1","object":"response","created_at":1759500000,"status":"completed","model":"gpt-4o","output":[{"id":"fc_az1","type":"function_call","status":"completed","arguments":"{\"city\":\"Paris\"}","call_id":"call_az1","name":"get_weather"}],"usage":{"input_tokens":60,"output_tokens":16,"total_tokens":76}}}`,
}

// azureResponsesToolCallBody is the non-streaming /openai/v1/responses body for
// the same turn.
const azureResponsesToolCallBody = `{"id":"resp_az1","object":"response","created_at":1759500000,"status":"completed","model":"gpt-4o","output":[{"id":"fc_az1","type":"function_call","status":"completed","arguments":"{\"city\":\"Paris\"}","call_id":"call_az1","name":"get_weather"}],"usage":{"input_tokens":60,"output_tokens":16,"total_tokens":76}}`

// azureChatToolCallChunks is the same turn streamed by Azure chat completions,
// finishing with finish_reason "tool_calls".
var azureChatToolCallChunks = []string{
	`{"id":"chatcmpl-az1","object":"chat.completion.chunk","created":1759500000,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_az1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
	`{"id":"chatcmpl-az1","object":"chat.completion.chunk","created":1759500000,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":null}]}`,
	`{"id":"chatcmpl-az1","object":"chat.completion.chunk","created":1759500000,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
}

// azureChatToolCallBody is the non-streaming Azure chat completion for the turn.
const azureChatToolCallBody = `{"id":"chatcmpl-az1","object":"chat.completion","created":1759500000,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_az1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":60,"completion_tokens":16,"total_tokens":76}}`

// azureResponsesFramesFromWire decodes Azure Responses stream events the way the
// shared OpenAI Responses stream handler does and stamps the Azure provider.
func azureResponsesFramesFromWire(t *testing.T, events []string) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	frames := make([]*schemas.BifrostResponsesStreamResponse, 0, len(events))
	for _, raw := range events {
		var frame schemas.BifrostResponsesStreamResponse
		if err := sonic.UnmarshalString(raw, &frame); err != nil {
			t.Fatalf("decode azure responses event %s: %v", raw, err)
		}
		frame.ExtraFields.Provider = schemas.Azure
		frames = append(frames, &frame)
	}
	return frames
}

// azureChatFramesFromWire decodes Azure chat completion chunks and converts them to
// Responses stream events, as the chat-completions fallback of ResponsesStream does.
func azureChatFramesFromWire(t *testing.T, chunks []string) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	var frames []*schemas.BifrostResponsesStreamResponse
	for _, raw := range chunks {
		var chunk schemas.BifrostChatResponse
		if err := sonic.UnmarshalString(raw, &chunk); err != nil {
			t.Fatalf("decode azure chat chunk %s: %v", raw, err)
		}
		chunk.ExtraFields.Provider = schemas.Azure
		frames = append(frames, chunk.ToBifrostResponsesStreamResponse(state)...)
	}
	return frames
}

// assertToolUseMessageDelta checks that the Anthropic stream opened a tool_use
// block and that the terminal message_delta reports stop_reason "tool_use".
func assertToolUseMessageDelta(t *testing.T, events []*AnthropicStreamEvent) {
	t.Helper()
	var sawToolUseBlock bool
	var messageDelta *AnthropicStreamEvent
	for _, event := range events {
		if event == nil {
			continue
		}
		switch event.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if event.ContentBlock != nil && event.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
				sawToolUseBlock = true
			}
		case AnthropicStreamEventTypeMessageDelta:
			messageDelta = event
		}
	}
	if !sawToolUseBlock {
		t.Fatal("expected a tool_use content_block_start")
	}
	if messageDelta == nil || messageDelta.Delta == nil || messageDelta.Delta.StopReason == nil {
		t.Fatalf("expected a message_delta carrying stop_reason, got %#v", messageDelta)
	}
	if got := *messageDelta.Delta.StopReason; got != AnthropicStopReasonToolUse {
		t.Errorf("stop_reason = %q, want %q", got, AnthropicStopReasonToolUse)
	}
}

// TestAzureOpenAIToolCallReportsToolUse_Stream reproduces #4065: a streamed Azure
// OpenAI tool-call turn served through /anthropic/v1/messages must end with
// message_delta stop_reason "tool_use" on both Azure upstream routes.
func TestAzureOpenAIToolCallReportsToolUse_Stream(t *testing.T) {
	t.Parallel()

	t.Run("responses route", func(t *testing.T) {
		t.Parallel()
		assertToolUseMessageDelta(t, driveAnthropicEgress(t, azureResponsesFramesFromWire(t, azureResponsesToolCallStreamEvents)))
	})

	t.Run("responses route, completed without output", func(t *testing.T) {
		t.Parallel()
		// Some Responses streams do not repeat the output on response.completed;
		// the opened tool_use block alone must still drive tool_use.
		events := append([]string(nil), azureResponsesToolCallStreamEvents[:len(azureResponsesToolCallStreamEvents)-1]...)
		events = append(events, `{"type":"response.completed","sequence_number":6,"response":{"id":"resp_az1","object":"response","created_at":1759500000,"status":"completed","model":"gpt-4o"}}`)
		assertToolUseMessageDelta(t, driveAnthropicEgress(t, azureResponsesFramesFromWire(t, events)))
	})

	t.Run("chat completions fallback", func(t *testing.T) {
		t.Parallel()
		assertToolUseMessageDelta(t, driveAnthropicEgress(t, azureChatFramesFromWire(t, azureChatToolCallChunks)))
	})
}

// TestAzureOpenAIToolCallReportsToolUse_NonStream covers the non-streaming side of
// #4065 on both Azure upstream routes.
func TestAzureOpenAIToolCallReportsToolUse_NonStream(t *testing.T) {
	t.Parallel()

	t.Run("responses route", func(t *testing.T) {
		t.Parallel()
		var resp schemas.BifrostResponsesResponse
		if err := sonic.UnmarshalString(azureResponsesToolCallBody, &resp); err != nil {
			t.Fatalf("decode azure responses body: %v", err)
		}
		resp.ExtraFields.Provider = schemas.Azure
		assertToolUseResponse(t, &resp)
	})

	t.Run("chat completions fallback", func(t *testing.T) {
		t.Parallel()
		var chat schemas.BifrostChatResponse
		if err := sonic.UnmarshalString(azureChatToolCallBody, &chat); err != nil {
			t.Fatalf("decode azure chat body: %v", err)
		}
		chat.ExtraFields.Provider = schemas.Azure
		assertToolUseResponse(t, chat.ToBifrostResponsesResponse())
	})
}

// assertToolUseResponse converts a Responses result to an Anthropic message and
// checks it carries a tool_use block with stop_reason "tool_use".
func assertToolUseResponse(t *testing.T, resp *schemas.BifrostResponsesResponse) {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	result := ToAnthropicResponsesResponse(ctx, resp)
	var sawToolUseBlock bool
	for _, block := range result.Content {
		if block.Type == AnthropicContentBlockTypeToolUse {
			sawToolUseBlock = true
		}
	}
	if !sawToolUseBlock {
		t.Fatalf("expected a tool_use content block, got %#v", result.Content)
	}
	if result.StopReason != AnthropicStopReasonToolUse {
		t.Errorf("stop_reason = %q, want %q", result.StopReason, AnthropicStopReasonToolUse)
	}
}
