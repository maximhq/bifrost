package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for issue #6334: Claude Code traffic on /anthropic/v1/messages routed to
// Gemini/Vertex fails with an upstream HTTP 400.
//
// Two normalizations that the Bedrock converter already performs are missing on the Gemini
// path (ToGeminiResponsesRequest -> convertResponsesMessagesToGeminiContents):
//
//  1. A trailing assistant turn (Claude Code's prefill) is mapped straight to a Gemini
//     role:"model" content. Gemini's Content.role is documented as "Must be either 'user'
//     or 'model'" for multi-turn conversations, and generateContent rejects a history whose
//     final turn is "model" with 400 ("Please ensure that multiturn requests ends with a
//     user role or a function response"). Bedrock trims this in ToBedrockResponsesRequest.
//
//  2. A mid-conversation role:"system" turn is hoisted into systemInstruction rather than
//     inlined in place. Gemini has no message-level system role, so the Bedrock/Anthropic
//     fallback is to render it as a user turn wrapped in <system-reminder> (see
//     convertBifrostSystemReminderToBedrockUserMessage / inlineMidConversationSystem).
//     Hoisting moves the text ahead of every message and collapses the cached prefix.

// buildGeminiRequestFromAnthropic mirrors the transport path: Anthropic Messages ingress ->
// Bifrost Responses -> Gemini generateContent.
func buildGeminiRequestFromAnthropic(t *testing.T, msgs []anthropic.AnthropicMessage, system *anthropic.AnthropicContent) *GeminiGenerationRequest {
	t.Helper()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	in := &anthropic.AnthropicMessageRequest{
		Model:     "gemini/gemini-3.7-flash",
		MaxTokens: 1024,
		Messages:  msgs,
		System:    system,
	}

	bifrostReq := in.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.Gemini

	out, err := ToGeminiResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)
	return out
}

func anthropicTextMessage(role anthropic.AnthropicMessageRole, text string) anthropic.AnthropicMessage {
	return anthropic.AnthropicMessage{
		Role:    role,
		Content: anthropic.AnthropicContent{ContentStr: schemas.Ptr(text)},
	}
}

// A trailing assistant prefill must not reach Gemini as the final role:"model" turn.
func TestGeminiResponses_TrailingAssistantPrefillIsTrimmed(t *testing.T) {
	out := buildGeminiRequestFromAnthropic(t, []anthropic.AnthropicMessage{
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "What is 2+2?"),
		anthropicTextMessage(anthropic.AnthropicMessageRoleAssistant, "The answer is"),
	}, nil)

	require.NotEmpty(t, out.Contents, "expected at least one content turn")
	last := out.Contents[len(out.Contents)-1]
	assert.NotEqual(t, "model", last.Role,
		"Gemini rejects a conversation ending on a model turn; the trailing assistant prefill must be trimmed")
}

// Consecutive trailing assistant turns must all be trimmed, not just the last one.
func TestGeminiResponses_MultipleTrailingAssistantTurnsTrimmed(t *testing.T) {
	out := buildGeminiRequestFromAnthropic(t, []anthropic.AnthropicMessage{
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "What is 2+2?"),
		anthropicTextMessage(anthropic.AnthropicMessageRoleAssistant, "Let me think."),
		anthropicTextMessage(anthropic.AnthropicMessageRoleAssistant, "The answer is"),
	}, nil)

	require.NotEmpty(t, out.Contents, "expected at least one content turn")
	last := out.Contents[len(out.Contents)-1]
	assert.NotEqual(t, "model", last.Role,
		"every trailing model turn must be trimmed, not only the final one")
}

// A mid-conversation system turn must be inlined in place as a user turn, not hoisted into
// systemInstruction ahead of the whole conversation.
func TestGeminiResponses_MidConversationSystemInlinedAsUserTurn(t *testing.T) {
	const leadingSystem = "You are Claude Code."
	const reminder = "The user opened a new file."

	out := buildGeminiRequestFromAnthropic(t, []anthropic.AnthropicMessage{
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "Hello"),
		anthropicTextMessage(anthropic.AnthropicMessageRoleAssistant, "Hi there."),
		anthropicTextMessage(anthropic.AnthropicMessageRoleSystem, reminder),
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "What changed?"),
	}, &anthropic.AnthropicContent{ContentStr: schemas.Ptr(leadingSystem)})

	// The leading system prompt still belongs in systemInstruction.
	require.NotNil(t, out.SystemInstruction, "leading system prompt must stay in systemInstruction")
	var systemText strings.Builder
	for _, p := range out.SystemInstruction.Parts {
		systemText.WriteString(p.Text)
	}
	assert.Contains(t, systemText.String(), leadingSystem)
	assert.NotContains(t, systemText.String(), reminder,
		"a mid-conversation system turn must not be hoisted into systemInstruction; hoisting moves it ahead of the conversation and invalidates the cached prefix")

	// It must appear as a user turn, in place, inside contents.
	found := false
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, reminder) {
				found = true
				assert.Equal(t, "user", c.Role,
					"an inlined mid-conversation system reminder must be carried as a user turn")
			}
		}
	}
	assert.True(t, found, "mid-conversation system reminder must be inlined into contents as a user turn")
}

// The trim must not eat a trailing turn that carries a thought signature. Gemini's thinking guide
// requires thought blocks to be resent exactly as received, so a signed reasoning turn is history,
// not a prefill.
func TestGeminiResponses_TrailingReasoningTurnIsNotTrimmed(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	reasoningType := schemas.ResponsesMessageTypeReasoning
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	userRole := schemas.ResponsesInputMessageRoleUser

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.7-flash",
		Input: []schemas.ResponsesMessage{
			{
				Role: &userRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Think about this."),
				},
			},
			{
				Role: &assistantRole,
				Type: &reasoningType,
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{
						Type: "summary_text",
						Text: "Working through it.",
					}},
				},
			},
		},
	}

	out, err := ToGeminiResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)

	var reasoningText string
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			reasoningText += p.Text
		}
	}
	assert.Contains(t, reasoningText, "Working through it.",
		"a trailing reasoning turn must survive the prefill trim; Gemini requires thought blocks to be replayed verbatim")
}

// A trailing assistant turn holding a function call is an unanswered tool round, not a prefill.
func TestGeminiResponses_TrailingFunctionCallIsNotTrimmed(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	callType := schemas.ResponsesMessageTypeFunctionCall
	userRole := schemas.ResponsesInputMessageRoleUser

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.7-flash",
		Input: []schemas.ResponsesMessage{
			{
				Role: &userRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("What is the weather?"),
				},
			},
			{
				Type: &callType,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      schemas.Ptr("get_weather"),
					CallID:    schemas.Ptr("call_1"),
					Arguments: schemas.Ptr(`{"city":"SF"}`),
				},
			},
		},
	}

	out, err := ToGeminiResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)

	found := false
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name == "get_weather" {
				found = true
			}
		}
	}
	assert.True(t, found, "a trailing function call must survive the prefill trim")
}

// Chat Completions path parity: the same trailing prefill must be trimmed there too.
func TestGeminiChat_TrailingAssistantPrefillIsTrimmed(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.7-flash",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("What is 2+2?"),
				},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("The answer is"),
				},
			},
		},
	}

	out, err := ToGeminiChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotEmpty(t, out.Contents)
	assert.NotEqual(t, "model", out.Contents[len(out.Contents)-1].Role,
		"the Chat Completions path must trim the trailing prefill too")
}

// An assistant turn carrying tool calls is history the model must see replayed, not a prefill.
func TestGeminiChat_TrailingAssistantToolCallIsNotTrimmed(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.7-flash",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("What is the weather?"),
				},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:   schemas.Ptr("call_1"),
						Type: schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("get_weather"),
							Arguments: `{"city":"SF"}`,
						},
					}},
				},
			},
		},
	}

	out, err := ToGeminiChatCompletionRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)

	found := false
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name == "get_weather" {
				found = true
			}
		}
	}
	assert.True(t, found, "a trailing assistant tool call must survive the prefill trim")
}

// ── Review findings on PR #6363 (CodeRabbit) ──────────────────────────────────

// assistantMessageWithBlocks builds a trailing assistant turn of type "message" whose
// content is carried as blocks rather than a plain string.
func assistantMessageWithBlocks(blocks []schemas.ResponsesMessageContentBlock) schemas.ResponsesMessage {
	msgType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleAssistant
	return schemas.ResponsesMessage{
		Role:    &role,
		Type:    &msgType,
		Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks},
	}
}

func geminiRequestWithTrailingAssistant(t *testing.T, trailing schemas.ResponsesMessage) *GeminiGenerationRequest {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	userRole := schemas.ResponsesInputMessageRoleUser
	out, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.7-flash",
		Input: []schemas.ResponsesMessage{
			{Role: &userRole, Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Think about this.")}},
			trailing,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	return out
}

// A trailing assistant message whose blocks include a reasoning block is model history,
// not a prefill: convertContentBlockToGeminiPart turns it into Part{Thought:true}, and
// Gemini requires thought blocks to be replayed verbatim.
func TestGeminiResponses_TrailingAssistantReasoningBlockIsNotTrimmed(t *testing.T) {
	out := geminiRequestWithTrailingAssistant(t, assistantMessageWithBlocks(
		[]schemas.ResponsesMessageContentBlock{{
			Type: schemas.ResponsesOutputMessageContentTypeReasoning,
			Text: schemas.Ptr("Deliberating carefully."),
		}},
	))

	var seen string
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			seen += p.Text
		}
	}
	assert.Contains(t, seen, "Deliberating carefully.",
		"an assistant message carrying a reasoning block is thought history and must survive the prefill trim")
}

// Same hazard by a second route: a plain TEXT block carrying a signature becomes a
// Part.ThoughtSignature, so trimming it drops a signature Gemini needs replayed.
func TestGeminiResponses_TrailingAssistantSignedTextBlockIsNotTrimmed(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte("thought-sig-6334"))
	out := geminiRequestWithTrailingAssistant(t, assistantMessageWithBlocks(
		[]schemas.ResponsesMessageContentBlock{{
			Type:      schemas.ResponsesInputMessageContentBlockTypeText,
			Text:      schemas.Ptr("Partial answer"),
			Signature: schemas.Ptr(sig),
		}},
	))

	found := false
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			if len(p.ThoughtSignature) > 0 {
				found = true
			}
		}
	}
	assert.True(t, found,
		"a signed assistant text block carries a thought signature and must survive the prefill trim")
}

// ContentStr and ContentBlocks are mutually exclusive sources: when ContentStr is
// non-nil it is the sole source, matching convertBifrostSystemReminderToBedrockUserMessage.
func TestInlineGeminiSystemReminder_ContentStrIsSoleSource(t *testing.T) {
	msg := schemas.ResponsesMessage{
		Content: &schemas.ResponsesMessageContent{
			ContentStr: schemas.Ptr(""),
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: schemas.Ptr("block text that must not be read"),
			}},
		},
	}

	content, err := inlineGeminiSystemReminder(&msg)
	require.NoError(t, err)

	var seen string
	if content != nil {
		for _, p := range content.Parts {
			seen += p.Text
		}
	}
	assert.NotContains(t, seen, "block text that must not be read",
		"ContentBlocks must not be read when ContentStr is non-nil; the two are mutually exclusive")
}

// Mirrors the harness anti-over-trim guard at unit level. Asserting only that
// "get_weather" appears somewhere in contents is vacuous: the trailing tool_result
// becomes a functionResponse carrying the same name, so the assertion would hold even
// if the assistant tool_use turn had been trimmed. The invariant that actually
// distinguishes the two is a MODEL turn carrying a functionCall.
func TestGeminiResponses_TrailingToolUseKeepsModelFunctionCallTurn(t *testing.T) {
	out := buildGeminiRequestFromAnthropic(t, []anthropic.AnthropicMessage{
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "What is the weather in SF?"),
		{
			Role: anthropic.AnthropicMessageRoleAssistant,
			Content: anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{{
				Type:  anthropic.AnthropicContentBlockTypeToolUse,
				ID:    schemas.Ptr("toolu_bf6334"),
				Name:  schemas.Ptr("get_weather"),
				Input: json.RawMessage(`{"city":"SF"}`),
			}}},
		},
		{
			Role: anthropic.AnthropicMessageRoleUser,
			Content: anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{{
				Type:      anthropic.AnthropicContentBlockTypeToolResult,
				ToolUseID: schemas.Ptr("toolu_bf6334"),
				Content:   &anthropic.AnthropicContent{ContentStr: schemas.Ptr("sunny, 20C")},
			}}},
		},
	}, nil)

	// Keyed on functionCall vs functionResponse, NOT on role: Gemini's Content.role is
	// omitempty and this converter leaves the function-call turn's role unset, so a
	// role=="model" check would fail for a reason unrelated to trimming.
	var toolCall *Part
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name == "get_weather" {
				toolCall = p
			}
		}
	}
	require.NotNil(t, toolCall,
		"the functionCall must survive; matching get_weather anywhere would also match the tool_result functionResponse")
}
