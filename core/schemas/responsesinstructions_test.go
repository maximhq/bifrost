package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the dropped `instructions` bug.
//
// The Responses API carries the system prompt in a top-level `instructions` field rather than as a
// message. BifrostResponsesRequest.ToChatRequest - the fallback used whenever a Responses-shaped
// request is served by a chat-completions provider - never referenced that field, so the prompt was
// silently discarded: the call returned 200 and the model simply never saw it.
//
// The harness caught it as "system prompt was dropped" on
//   47.8.D /openai/v1/responses -> deepseek/deepseek-v4-flash
// which returned 200 with a reply that ignored the instruction entirely.
//
// OpenAI defines `instructions` as a message inserted at the FRONT of the context, which is why
// these tests pin the position and not merely the presence.

const instrText = "You are Bifrost QA. Always begin your reply with the word ACK."

func userResponsesRequest(instructions *string) *BifrostResponsesRequest {
	return &BifrostResponsesRequest{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		Input: []ResponsesMessage{{
			Role: Ptr(ResponsesInputMessageRoleUser),
			Content: &ResponsesMessageContent{
				ContentStr: Ptr("Who are you?"),
			},
		}},
		Params: &ResponsesParameters{Instructions: instructions},
	}
}

func TestToChatRequest_CarriesInstructionsAsLeadingSystemMessage(t *testing.T) {
	chat := userResponsesRequest(Ptr(instrText)).ToChatRequest()

	require.NotNil(t, chat)
	require.GreaterOrEqual(t, len(chat.Input), 2, "instructions must add a message, not replace the user turn")

	first := chat.Input[0]
	assert.Equal(t, ChatMessageRoleSystem, first.Role, "instructions belong at the front of the context")
	require.NotNil(t, first.Content)
	require.NotNil(t, first.Content.ContentStr)
	assert.Equal(t, instrText, *first.Content.ContentStr)

	// The original turn must survive untouched.
	last := chat.Input[len(chat.Input)-1]
	assert.Equal(t, ChatMessageRoleUser, last.Role)
	require.NotNil(t, last.Content)
	require.NotNil(t, last.Content.ContentStr)
	assert.Equal(t, "Who are you?", *last.Content.ContentStr)
}

func TestToChatRequest_NoInstructionsAddsNothing(t *testing.T) {
	chat := userResponsesRequest(nil).ToChatRequest()

	require.NotNil(t, chat)
	require.Len(t, chat.Input, 1, "a nil instructions field must not synthesise a message")
	assert.Equal(t, ChatMessageRoleUser, chat.Input[0].Role)
}

// An empty string is not a system prompt. Prepending it would burn a message slot and, on the
// stricter providers, be rejected as an empty-content message.
func TestToChatRequest_EmptyInstructionsAddsNothing(t *testing.T) {
	chat := userResponsesRequest(Ptr("")).ToChatRequest()

	require.NotNil(t, chat)
	require.Len(t, chat.Input, 1)
	assert.Equal(t, ChatMessageRoleUser, chat.Input[0].Role)
}

// If the caller already sent a system turn in Input, instructions still lead: OpenAI documents it
// as prepended context, and demoting it below an existing system message would reorder precedence.
func TestToChatRequest_InstructionsPrecedeAnExistingSystemMessage(t *testing.T) {
	req := &BifrostResponsesRequest{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		Input: []ResponsesMessage{
			{
				Role:    Ptr(ResponsesInputMessageRoleSystem),
				Content: &ResponsesMessageContent{ContentStr: Ptr("inline system")},
			},
			{
				Role:    Ptr(ResponsesInputMessageRoleUser),
				Content: &ResponsesMessageContent{ContentStr: Ptr("Who are you?")},
			},
		},
		Params: &ResponsesParameters{Instructions: Ptr(instrText)},
	}

	chat := req.ToChatRequest()
	require.Len(t, chat.Input, 3)
	assert.Equal(t, ChatMessageRoleSystem, chat.Input[0].Role)
	require.NotNil(t, chat.Input[0].Content.ContentStr)
	assert.Equal(t, instrText, *chat.Input[0].Content.ContentStr)
	require.NotNil(t, chat.Input[1].Content.ContentStr)
	assert.Equal(t, "inline system", *chat.Input[1].Content.ContentStr)
}

// A nil Params must not panic on the new lookup.
func TestToChatRequest_NilParamsIsSafe(t *testing.T) {
	req := &BifrostResponsesRequest{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		Input: []ResponsesMessage{{
			Role:    Ptr(ResponsesInputMessageRoleUser),
			Content: &ResponsesMessageContent{ContentStr: Ptr("hi")},
		}},
	}

	chat := req.ToChatRequest()
	require.NotNil(t, chat)
	require.Len(t, chat.Input, 1)
}
