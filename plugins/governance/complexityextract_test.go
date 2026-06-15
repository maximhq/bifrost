package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBuildComplexityInput_ChatTextMessages(t *testing.T) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleSystem,
					Content: complexityChatString("Be concise"),
				},
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: complexityChatString("Explain vector clocks"),
				},
				{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: complexityChatString("Vector clocks track causal history."),
				},
				{
					Role: schemas.ChatMessageRoleUser,
					Content: complexityChatBlocks(
						complexityChatTextBlock("Compare them to Lamport clocks"),
					),
				},
			},
		},
	}

	input, ok := buildComplexityInput(req)
	require.True(t, ok)
	assert.Equal(t, "Compare them to Lamport clocks", input.LastUserText)
	assert.Equal(t, []string{"Explain vector clocks"}, input.PriorUserTexts)
	assert.Equal(t, "Be concise", input.SystemText)
}

func TestBuildComplexityInput_TextCompletionPrompt(t *testing.T) {
	prompt := "Write a short summary of this changelog"
	req := &schemas.BifrostRequest{
		RequestType: schemas.TextCompletionRequest,
		TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
			Input: &schemas.TextCompletionInput{PromptStr: &prompt},
		},
	}

	input, ok := buildComplexityInput(req)
	require.True(t, ok)
	assert.Equal(t, prompt, input.LastUserText)
}

func TestBuildComplexityInput_TextCompletionPromptArraySkipped(t *testing.T) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.TextCompletionRequest,
		TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
			Input: &schemas.TextCompletionInput{
				PromptArray: []string{
					"Summarize this short changelog",
					"Debug this distributed tracing timeout and propose fixes",
				},
			},
		},
	}

	input, ok := buildComplexityInput(req)
	require.False(t, ok)
	assert.Empty(t, input.LastUserText)
}

func TestBuildComplexityInput_ResponsesInputTextBlocks(t *testing.T) {
	systemRole := schemas.ResponsesInputMessageRoleSystem
	userRole := schemas.ResponsesInputMessageRoleUser
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	instructions := "Review carefully"

	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Params: &schemas.ResponsesParameters{Instructions: &instructions},
			Input: []schemas.ResponsesMessage{
				{
					Role:    &systemRole,
					Content: complexityResponsesString("Be concise"),
				},
				{
					Role: &userRole,
					Content: complexityResponsesBlocks(
						complexityResponsesTextBlock("I changed the retry policy and circuit breaker thresholds."),
					),
				},
				{
					Role: &assistantRole,
					Content: complexityResponsesBlocks(
						complexityResponsesOutputTextBlock("The patch retries idempotent requests and opens the breaker sooner."),
					),
				},
				{
					Role: &userRole,
					Content: complexityResponsesBlocks(
						complexityResponsesTextBlock("Can you explain the changes?"),
					),
				},
			},
		},
	}

	input, ok := buildComplexityInput(req)
	require.True(t, ok)
	assert.Equal(t, "Can you explain the changes?", input.LastUserText)
	assert.Equal(t, []string{"I changed the retry policy and circuit breaker thresholds."}, input.PriorUserTexts)
	assert.Equal(t, "Review carefully Be concise", input.SystemText)
}

func TestBuildComplexityInput_SupportsStreamingRequestTypes(t *testing.T) {
	prompt := "Write a short summary of this changelog"
	userRole := schemas.ResponsesInputMessageRoleUser
	instructions := "Answer carefully"

	tests := []struct {
		name         string
		req          *schemas.BifrostRequest
		wantLastUser string
		wantSystem   string
	}{
		{
			name: "chat_completion_stream",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionStreamRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Input: []schemas.ChatMessage{
						{Role: schemas.ChatMessageRoleSystem, Content: complexityChatString("Be concise")},
						{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Explain vector clocks")},
					},
				},
			},
			wantLastUser: "Explain vector clocks",
			wantSystem:   "Be concise",
		},
		{
			name: "text_completion_stream",
			req: &schemas.BifrostRequest{
				RequestType: schemas.TextCompletionStreamRequest,
				TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
					Input: &schemas.TextCompletionInput{PromptStr: &prompt},
				},
			},
			wantLastUser: prompt,
		},
		{
			name: "responses_stream",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesStreamRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Params: &schemas.ResponsesParameters{Instructions: &instructions},
					Input: []schemas.ResponsesMessage{
						{
							Role: &userRole,
							Content: complexityResponsesBlocks(
								complexityResponsesTextBlock("Compare Go channels and mutexes"),
							),
						},
					},
				},
			},
			wantLastUser: "Compare Go channels and mutexes",
			wantSystem:   "Answer carefully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, ok := buildComplexityInput(tt.req)
			require.True(t, ok)
			assert.Equal(t, tt.wantLastUser, input.LastUserText)
			assert.Equal(t, tt.wantSystem, input.SystemText)
		})
	}
}

func TestBuildComplexityInput_SkipsUnsupportedRequestTypesEvenWhenTextIsPresent(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	req := &schemas.BifrostRequest{
		RequestType: schemas.CountTokensRequest,
		CountTokensRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{
					Role: &userRole,
					Content: complexityResponsesBlocks(
						complexityResponsesTextBlock("How many tokens is this prompt?"),
					),
				},
			},
		},
	}

	input, ok := buildComplexityInput(req)
	require.False(t, ok)
	assert.Empty(t, input.LastUserText)
}

func TestBuildComplexityInput_SkipsMixedModalityUserContent(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser

	tests := []struct {
		name string
		req  *schemas.BifrostRequest
	}{
		{
			name: "chat_text_plus_image",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: complexityChatBlocks(
								complexityChatTextBlock("What changed in this screenshot?"),
								schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage},
							),
						},
					},
				},
			},
		},
		{
			name: "responses_text_plus_file",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Input: []schemas.ResponsesMessage{
						{
							Role: &userRole,
							Content: complexityResponsesBlocks(
								complexityResponsesTextBlock("Summarize this document"),
								schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeFile},
							),
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, ok := buildComplexityInput(tt.req)
			require.False(t, ok)
			assert.Empty(t, input.LastUserText)
		})
	}
}

func complexityChatString(text string) *schemas.ChatMessageContent {
	return &schemas.ChatMessageContent{ContentStr: &text}
}

func complexityChatBlocks(blocks ...schemas.ChatContentBlock) *schemas.ChatMessageContent {
	return &schemas.ChatMessageContent{ContentBlocks: blocks}
}

func complexityChatTextBlock(text string) schemas.ChatContentBlock {
	return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &text}
}

func complexityResponsesString(text string) *schemas.ResponsesMessageContent {
	return &schemas.ResponsesMessageContent{ContentStr: &text}
}

func complexityResponsesBlocks(blocks ...schemas.ResponsesMessageContentBlock) *schemas.ResponsesMessageContent {
	return &schemas.ResponsesMessageContent{ContentBlocks: blocks}
}

func complexityResponsesTextBlock(text string) schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &text}
}

func complexityResponsesOutputTextBlock(text string) schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &text}
}
