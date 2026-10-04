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

// Every field on Part is omitempty, so a Part carrying no payload marshals to
// exactly `{}`. The provider harness observed one reach a client: asking
// gemini-2.5-pro to transcribe a synthetic tone returned
// {"candidates":[{"content":{"parts":[{}],"role":"model"}}]}.
//
// The producer of that particular part was not identified -- the two candidate
// sources in this package (thoughtSignatureFromEncryptedContent and
// convertContentBlockToGeminiPart) both already refuse to emit one, so the empty
// part may well have come from Google and been relayed. Either way a payload-free
// part is never a valid answer: it is noise a client will try to read, and it masks
// the contentless case by making the parts slice look non-empty. Filtering at the
// point the candidate is assembled covers both origins.
func TestDropEmptyGeminiParts(t *testing.T) {
	t.Run("a zero part marshals to the empty object it is filtered for", func(t *testing.T) {
		encoded, err := json.Marshal(&Part{})
		require.NoError(t, err)
		assert.Equal(t, "{}", string(encoded),
			"if this ever stops holding, the filter below is testing the wrong thing")
	})

	t.Run("drops payload-free parts", func(t *testing.T) {
		got := dropEmptyGeminiParts([]*Part{
			{Text: "keep me"},
			{},
			nil,
			{Thought: true},
			{ThoughtSignature: []byte{0x01}},
		})

		require.Len(t, got, 3)
		assert.Equal(t, "keep me", got[0].Text)
		assert.True(t, got[1].Thought, "a thought marker is a payload")
		assert.Equal(t, []byte{0x01}, got[2].ThoughtSignature)
	})

	t.Run("leaves a fully populated slice untouched", func(t *testing.T) {
		parts := []*Part{{Text: "a"}, {Text: "b"}}
		assert.Equal(t, parts, dropEmptyGeminiParts(parts))
	})

	t.Run("nil and empty input stay empty", func(t *testing.T) {
		assert.Empty(t, dropEmptyGeminiParts(nil))
		assert.Empty(t, dropEmptyGeminiParts([]*Part{}))
		assert.Empty(t, dropEmptyGeminiParts([]*Part{{}, nil, {}}))
	})

	// A zero-length signature is not the same value as a nil one, so comparing
	// against the zero Part keeps such a part -- but Part.MarshalJSON writes
	// ThoughtSignature through a `string` alias with omitempty, so an empty slice
	// base64-encodes to "" and the key disappears. The part therefore reaches the
	// wire as exactly the `{}` this filter exists to remove, having passed straight
	// through it. Emptiness has to be judged on what marshals, not on struct equality.
	t.Run("a zero-length thought signature counts as absent", func(t *testing.T) {
		encoded, err := json.Marshal(&Part{ThoughtSignature: []byte{}})
		require.NoError(t, err)
		require.Equal(t, "{}", string(encoded),
			"if an empty signature stops marshalling to {} this case no longer describes a real hazard")

		assert.Empty(t, dropEmptyGeminiParts([]*Part{{ThoughtSignature: []byte{}}}),
			"a part whose only field is a zero-length signature carries no payload")
	})

	t.Run("a non-empty thought signature is still kept", func(t *testing.T) {
		got := dropEmptyGeminiParts([]*Part{{ThoughtSignature: []byte{0x01}}})
		require.Len(t, got, 1)
		assert.Equal(t, []byte{0x01}, got[0].ThoughtSignature)
	})
}

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
