package gemini

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every Part field is omitempty, so a text part built from an empty string marshals to `{}`
// and Gemini/Vertex reject the whole request with 400 "Request contains an invalid argument".
// A Content or systemInstruction with no parts is rejected the same way. Anthropic clients
// legitimately send messages whose content is "" (e.g. an assistant turn that only carried a
// stripped block), so the request converters must drop such text instead of forwarding it.

// assertNoEmptyGeminiParts fails if any content (or the system instruction) is partless or
// carries a part that would serialize to `{}`.
func assertNoEmptyGeminiParts(t *testing.T, contents []Content, system *Content) {
	t.Helper()
	check := func(where string, c Content) {
		assert.NotEmpty(t, c.Parts, "%s has no parts", where)
		for _, p := range c.Parts {
			require.NotNil(t, p, "%s has a nil part", where)
			encoded, err := json.Marshal(p)
			require.NoError(t, err)
			assert.NotEqual(t, "{}", string(encoded), "%s has a part that marshals to {}", where)
		}
	}
	for i, c := range contents {
		check(fmt.Sprintf("contents[%d]", i), c)
	}
	if system != nil {
		check("systemInstruction", *system)
	}
}

// responsesTextMessage builds a Responses input message whose content is the plain string text.
func responsesTextMessage(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role:    schemas.Ptr(role),
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(text)},
	}
}

// TestConvertResponsesMessagesToGeminiContents_SkipsEmptyContentStr verifies that empty text
// in Responses messages is dropped, partless turns and system instructions are omitted, and
// function call/output parts are preserved.
func TestConvertResponsesMessagesToGeminiContents_SkipsEmptyContentStr(t *testing.T) {
	user := schemas.ResponsesInputMessageRoleUser
	assistant := schemas.ResponsesInputMessageRoleAssistant
	system := schemas.ResponsesInputMessageRoleSystem

	tests := []struct {
		name          string
		messages      []schemas.ResponsesMessage
		wantRoles     []string
		wantSystem    []string // nil means systemInstruction must be omitted
		wantFuncParts int
	}{
		{
			name: "empty assistant turn mid-conversation is dropped",
			messages: []schemas.ResponsesMessage{
				responsesTextMessage(user, "hi"),
				responsesTextMessage(assistant, ""),
				responsesTextMessage(user, "still there?"),
			},
			wantRoles: []string{"user", "user"},
		},
		{
			name: "empty user turn is dropped",
			messages: []schemas.ResponsesMessage{
				responsesTextMessage(user, ""),
				responsesTextMessage(assistant, "hello"),
				responsesTextMessage(user, "go on"),
			},
			wantRoles: []string{"model", "user"},
		},
		{
			name: "empty leading system prompt omits systemInstruction",
			messages: []schemas.ResponsesMessage{
				responsesTextMessage(system, ""),
				responsesTextMessage(user, "hi"),
			},
			wantRoles: []string{"user"},
		},
		{
			name: "empty system part is skipped but non-empty one is kept",
			messages: []schemas.ResponsesMessage{
				responsesTextMessage(system, ""),
				responsesTextMessage(system, "be terse"),
				responsesTextMessage(user, "hi"),
			},
			wantRoles:  []string{"user"},
			wantSystem: []string{"be terse"},
		},
		{
			name: "function call and response survive next to an empty assistant turn",
			messages: []schemas.ResponsesMessage{
				responsesTextMessage(user, "list files"),
				responsesTextMessage(assistant, ""),
				{
					Role: schemas.Ptr(assistant),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    schemas.Ptr("c1"),
						Name:      schemas.Ptr("bash"),
						Arguments: schemas.Ptr(`{"command":"ls"}`),
					},
				},
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr("c1"),
						Name:   schemas.Ptr("bash"),
						Output: &schemas.ResponsesToolMessageOutputStruct{
							ResponsesToolCallOutputStr: schemas.Ptr("a.txt"),
						},
					},
				},
			},
			wantRoles:     []string{"user", "model", "user"},
			wantFuncParts: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents, systemInstruction, err := convertResponsesMessagesToGeminiContents(tt.messages, "gemini-2.5-pro", schemas.Gemini)
			require.NoError(t, err)
			assertNoEmptyGeminiParts(t, contents, systemInstruction)

			var roles []string
			funcParts := 0
			for _, c := range contents {
				roles = append(roles, c.Role)
				for _, p := range c.Parts {
					if p.FunctionCall != nil || p.FunctionResponse != nil {
						funcParts++
					}
				}
			}
			assert.Equal(t, tt.wantRoles, roles)
			assert.Equal(t, tt.wantFuncParts, funcParts)

			if tt.wantSystem == nil {
				assert.Nil(t, systemInstruction)
				return
			}
			require.NotNil(t, systemInstruction)
			var texts []string
			for _, p := range systemInstruction.Parts {
				texts = append(texts, p.Text)
			}
			assert.Equal(t, tt.wantSystem, texts)
		})
	}
}

// TestToGeminiResponsesRequest_AnthropicEmptyStringContent checks the fix end to end through
// the Anthropic Messages ingress, which forwards content "" as ContentStr.
func TestToGeminiResponsesRequest_AnthropicEmptyStringContent(t *testing.T) {
	out := buildGeminiRequestFromAnthropic(t, []anthropic.AnthropicMessage{
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "hi"),
		anthropicTextMessage(anthropic.AnthropicMessageRoleAssistant, ""),
		anthropicTextMessage(anthropic.AnthropicMessageRoleUser, "are you there?"),
	}, &anthropic.AnthropicContent{ContentStr: schemas.Ptr("")})

	assertNoEmptyGeminiParts(t, out.Contents, out.SystemInstruction)
	assert.Nil(t, out.SystemInstruction)
	require.Len(t, out.Contents, 2)

	body, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "{}")
}

// TestToGeminiResponsesRequest_EmptyInstructionsOmitted verifies that an empty Instructions
// parameter does not produce a partless systemInstruction.
func TestToGeminiResponsesRequest_EmptyInstructionsOmitted(t *testing.T) {
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	out, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-pro",
		Input:    []schemas.ResponsesMessage{responsesTextMessage(schemas.ResponsesInputMessageRoleUser, "hi")},
		Params:   &schemas.ResponsesParameters{Instructions: schemas.Ptr("")},
	})
	require.NoError(t, err)
	assert.Nil(t, out.SystemInstruction)
}

// TestConvertBifrostMessagesToGemini_SkipsEmptyText verifies that the chat converter drops
// empty text blocks and omits the system instruction when it would have no parts.
func TestConvertBifrostMessagesToGemini_SkipsEmptyText(t *testing.T) {
	tests := []struct {
		name       string
		messages   []schemas.ChatMessage
		wantRoles  []string
		wantSystem bool
	}{
		{
			name: "empty text content block is dropped",
			messages: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
				{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("")}},
				}},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("again")}},
			},
			wantRoles: []string{"user", "user"},
		},
		{
			name: "empty system message omits systemInstruction",
			messages: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("")}},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
			wantRoles: []string{"user"},
		},
		{
			name: "non-empty system message is kept",
			messages: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("be terse")}},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
			wantRoles:  []string{"user"},
			wantSystem: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents, systemInstruction, err := convertBifrostMessagesToGemini(tt.messages)
			require.NoError(t, err)
			assertNoEmptyGeminiParts(t, contents, systemInstruction)

			var roles []string
			for _, c := range contents {
				roles = append(roles, c.Role)
			}
			assert.Equal(t, tt.wantRoles, roles)
			assert.Equal(t, tt.wantSystem, systemInstruction != nil)
		})
	}
}
