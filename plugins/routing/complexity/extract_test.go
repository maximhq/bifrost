package complexity

import (
	"context"
	"strings"
	"testing"
	"time"

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

	input, ok := BuildInput(nil, req)
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

	input, ok := BuildInput(nil, req)
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

	input, ok := BuildInput(nil, req)
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

	input, ok := BuildInput(nil, req)
	require.True(t, ok)
	assert.Equal(t, "Can you explain the changes?", input.LastUserText)
	assert.Equal(t, []string{"I changed the retry policy and circuit breaker thresholds."}, input.PriorUserTexts)
	assert.Equal(t, "Review carefully Be concise", input.SystemText)
}

func TestBuildComplexityInput_BlankUserTurns(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	tests := []struct {
		name         string
		req          *schemas.BifrostRequest
		wantOK       bool
		wantLastUser string
	}{
		{
			name: "chat trailing blank preserves prior human turn",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Explain vector clocks")},
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("  ")},
				}},
			},
			wantOK:       true,
			wantLastUser: "Explain vector clocks",
		},
		{
			name: "responses trailing blank preserves prior human turn",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
					{Role: &userRole, Content: complexityResponsesString("Explain vector clocks")},
					{Role: &userRole, Content: complexityResponsesString("  ")},
				}},
			},
			wantOK:       true,
			wantLastUser: "Explain vector clocks",
		},
		{
			name: "all blank turns remain unavailable",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("")},
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("  ")},
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, ok := BuildInput(nil, tt.req)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantLastUser, input.LastUserText)
		})
	}
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
			input, ok := BuildInput(nil, tt.req)
			require.True(t, ok)
			assert.Equal(t, tt.wantLastUser, input.LastUserText)
			assert.Equal(t, tt.wantSystem, input.SystemText)
		})
	}
}

// The Anthropic→Responses conversion tags user text blocks as output_text for
// bedrock/-prefixed models (keepToolsGrouped path), emitting one message per
// text block. Complexity extraction must still treat these as text, otherwise
// Claude Code traffic using a bedrock/ alias never classifies and always falls
// through to the default routing rule.
func TestBuildComplexityInput_ResponsesOutputTextTypedUserBlocks(t *testing.T) {
	systemRole := schemas.ResponsesInputMessageRoleSystem
	userRole := schemas.ResponsesInputMessageRoleUser

	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{
					Role:    &systemRole,
					Content: complexityResponsesBlocks(complexityResponsesTextBlock("You are a coding agent")),
				},
				{
					Role:    &userRole,
					Content: complexityResponsesBlocks(complexityResponsesOutputTextBlock("Explain encryption")),
				},
			},
		},
	}

	input, ok := BuildInput(nil, req)
	require.True(t, ok)
	assert.Equal(t, "Explain encryption", input.LastUserText)
	assert.Equal(t, "You are a coding agent", input.SystemText)
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

	input, ok := BuildInput(nil, req)
	require.False(t, ok)
	assert.Empty(t, input.LastUserText)
}

func TestBuildComplexityInput_ExtractsTextFromMixedModalityUserContent(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser

	tests := []struct {
		name     string
		req      *schemas.BifrostRequest
		wantText string
	}{
		{
			name:     "chat_text_plus_image",
			wantText: "What changed in this screenshot?",
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
			name:     "responses_text_plus_file",
			wantText: "Summarize this document",
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
			input, ok := BuildInput(nil, tt.req)
			require.True(t, ok)
			assert.Equal(t, tt.wantText, input.LastUserText)
		})
	}
}

func TestBuildComplexityInput_SkipsUserContentWithoutText(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser

	tests := []struct {
		name string
		req  *schemas.BifrostRequest
	}{
		{
			name: "chat_image_only",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: complexityChatBlocks(
								schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage},
							),
						},
					},
				},
			},
		},
		{
			name: "responses_file_only",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Input: []schemas.ResponsesMessage{
						{
							Role: &userRole,
							Content: complexityResponsesBlocks(
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
			input, ok := BuildInput(nil, tt.req)
			require.False(t, ok)
			assert.Empty(t, input.LastUserText)
		})
	}
}

func TestSanitizeUserText_ClaudeCodeWrappers(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantText string
		wantKind complexityTextKind
	}{
		{
			name:     "system_reminder",
			text:     "<system-reminder>Internal context</system-reminder>",
			wantKind: complexityTextContextOnly,
		},
		{
			name:     "local_command_caveat",
			text:     "<local-command-caveat>Ignore local command messages</local-command-caveat>",
			wantKind: complexityTextHousekeeping,
		},
		{
			name:     "local_command_stdout",
			text:     "<local-command-stdout>Compacted</local-command-stdout>",
			wantKind: complexityTextHousekeeping,
		},
		{
			name:     "local_command_stderr",
			text:     "<local-command-stderr>command failed</local-command-stderr>",
			wantKind: complexityTextHousekeeping,
		},
		{
			name:     "command_name",
			text:     "<command-name>/compact</command-name>",
			wantKind: complexityTextHousekeeping,
		},
		{
			name:     "command_message",
			text:     "<command-message>compact</command-message>",
			wantKind: complexityTextHousekeeping,
		},
		{
			name:     "command_args",
			text:     "<command-args>focus on routing</command-args>",
			wantKind: complexityTextHousekeeping,
		},
		{
			name: "session_title_request",
			text: "<session>\nhello can u help me understand what sidekiq is\n</session>\n\n" +
				"Write the title in the predominant language of the session.",
			wantKind: complexityTextHousekeeping,
		},
		{
			name: "resume_recap_request",
			text: "The user stepped away and is coming back. Recap in under 40 words, 1-2 plain sentences, no markdown. " +
				"Lead with the overall goal and current task, then the one next action.",
			wantKind: complexityTextHousekeeping,
		},
		{
			name:     "session_tag_mentioned_inside_human_request",
			text:     "How should I parse a <session> XML element?",
			wantText: "How should I parse a <session> XML element?",
			wantKind: complexityTextHuman,
		},
		{
			name:     "wrapper_with_human_text",
			text:     "<local-command-stdout>build failed</local-command-stdout>\nWhy did the build fail?",
			wantText: "Why did the build fail?",
			wantKind: complexityTextHuman,
		},
		{
			name:     "malformed_wrapper_is_preserved",
			text:     "<local-command-stdout>build failed",
			wantText: "<local-command-stdout>build failed",
			wantKind: complexityTextHuman,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotText, gotKind := sanitizeUserText(tt.text, complexityHarnessClaudeCode)
			assert.Equal(t, tt.wantText, gotText)
			assert.Equal(t, tt.wantKind, gotKind)
		})
	}
}

func TestBuildComplexityInput_ClaudeCodeInjectedMessagesAreContinuations(t *testing.T) {
	claudeCtx := complexityHarnessContext(schemas.ClaudeCLI.String(), nil)
	tests := []struct {
		name string
		text string
	}{
		{
			name: "session_title_request",
			text: "<session>\nDebug the distributed queue worker\n</session>\n\n" +
				"Write the title in the predominant language of the session.",
		},
		{
			name: "resume_recap_request",
			text: "The user stepped away and is coming back. Recap in under 40 words, 1-2 plain sentences, no markdown.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, disposition := BuildInputWithDisposition(claudeCtx, &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString(tt.text)},
				}},
			})

			assert.Equal(t, InputContinuation, disposition)
			assert.Empty(t, input.LastUserText)
		})
	}
}

func TestBuildComplexityInput_ClaudeCodeContextAndHousekeeping(t *testing.T) {
	claudeCtx := complexityHarnessContext(schemas.ClaudeCLI.String(), nil)

	tests := []struct {
		name       string
		messages   []schemas.ChatMessage
		wantOK     bool
		wantLast   string
		wantPriors []string
	}{
		{
			name: "trailing_context_reveals_previous_human_turn",
			messages: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Explain vector clocks")},
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("<system-reminder>Use the repository instructions</system-reminder>")},
			},
			wantOK:   true,
			wantLast: "Explain vector clocks",
		},
		{
			name: "historical_command_is_not_conversation_context",
			messages: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Explain vector clocks")},
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("<command-name>/plugin</command-name>")},
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Compare them to Lamport clocks")},
			},
			wantOK:     true,
			wantLast:   "Compare them to Lamport clocks",
			wantPriors: []string{"Explain vector clocks"},
		},
		{
			name: "newest_local_command_skips_request",
			messages: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Explain vector clocks")},
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("<local-command-stdout>Compacted</local-command-stdout>")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, ok := BuildInput(claudeCtx, &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Input: tt.messages,
				},
			})
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantLast, input.LastUserText)
			assert.Equal(t, tt.wantPriors, input.PriorUserTexts)
		})
	}
}

func TestBuildComplexityInput_CodexContextAndSystemText(t *testing.T) {
	codexCtx := complexityHarnessContext(schemas.CodexDesktop.String(), nil)
	userRole := schemas.ResponsesInputMessageRoleUser
	systemRole := schemas.ResponsesInputMessageRoleSystem
	systemText := "Keep answers concise. <recommended_plugins>Plugin inventory</recommended_plugins>"

	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{Role: &systemRole, Content: complexityResponsesString(systemText)},
				{Role: &userRole, Content: complexityResponsesString("<environment_context><cwd>/tmp/repo</cwd></environment_context>")},
				{Role: &userRole, Content: complexityResponsesString("Fix the latest-message extractor")},
			},
		},
	}

	input, ok := BuildInput(codexCtx, req)
	require.True(t, ok)
	assert.Equal(t, "Fix the latest-message extractor", input.LastUserText)
	assert.Empty(t, input.PriorUserTexts)
	assert.Equal(t, "Keep answers concise.", input.SystemText)
	assert.Equal(t, systemText, *req.ResponsesRequest.Input[0].Content.ContentStr, "classifier sanitization must not mutate the request")
}

func TestBuildComplexityInput_CodexNewestHousekeepingSkipsRequest(t *testing.T) {
	codexCtx := complexityHarnessContext(schemas.CodexCLI.String(), nil)
	userRole := schemas.ResponsesInputMessageRoleUser

	tests := []struct {
		name       string
		latestText string
	}{
		{
			name:       "local_shell_command",
			latestText: "<user_shell_command><command>pwd</command><result>/tmp/repo</result></user_shell_command>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, ok := BuildInput(codexCtx, &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Input: []schemas.ResponsesMessage{
						{Role: &userRole, Content: complexityResponsesString("Explain vector clocks")},
						{Role: &userRole, Content: complexityResponsesString(tt.latestText)},
					},
				},
			})
			require.False(t, ok)
			assert.Empty(t, input.LastUserText)
		})
	}
}

func TestBuildComplexityInput_CodexRequestKinds(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{Role: &userRole, Content: complexityResponsesString("Explain vector clocks")},
			},
		},
	}

	tests := []struct {
		name        string
		userAgent   string
		requestKind string
		rawMetadata string
		wantOK      bool
	}{
		{name: "turn", userAgent: schemas.CodexCLI.String(), requestKind: "turn", wantOK: true},
		{name: "prewarm", userAgent: schemas.CodexCLI.String(), requestKind: "prewarm"},
		{name: "compaction", userAgent: schemas.CodexCLI.String(), requestKind: "compaction"},
		{name: "memory", userAgent: schemas.CodexDesktop.String(), requestKind: "memory"},
		{name: "unknown_kind_preserves_behavior", userAgent: schemas.CodexCLI.String(), requestKind: "future_kind", wantOK: true},
		{name: "malformed_metadata_preserves_behavior", userAgent: schemas.CodexCLI.String(), rawMetadata: "{", wantOK: true},
		{name: "non_codex_cannot_skip", userAgent: "generic-client/1.0", requestKind: "compaction", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawMetadata := tt.rawMetadata
			if rawMetadata == "" {
				rawMetadata = `{"request_kind":"` + tt.requestKind + `"}`
			}
			ctx := complexityHarnessContext("", map[string]string{
				"user-agent":            tt.userAgent,
				codexTurnMetadataHeader: rawMetadata,
			})

			input, ok := BuildInput(ctx, req)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, "Explain vector clocks", input.LastUserText)
			}
		})
	}
}

func TestBuildComplexityInput_HarnessMarkersRequireMatchingUserAgent(t *testing.T) {
	markerText := "<local-command-stdout>Compacted</local-command-stdout>"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString(markerText)},
			},
		},
	}

	input, ok := BuildInput(complexityHarnessContext("generic-client/1.0", nil), req)
	require.True(t, ok)
	assert.Equal(t, markerText, input.LastUserText)
}

func TestBuildInputWithDisposition(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	tests := []struct {
		name string
		ctx  *schemas.BifrostContext
		req  *schemas.BifrostRequest
		want InputDisposition
	}{
		{
			name: "human turn is classifiable",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Explain vector clocks")},
				}},
			},
			want: InputClassifiable,
		},
		{
			name: "supported conversation without human text is a continuation",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleAssistant, Content: complexityChatString("Tool result received")},
				}},
			},
			want: InputContinuation,
		},
		{
			name: "chat replay followed by assistant output is a continuation",
			req: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("Run the tests")},
					{Role: schemas.ChatMessageRoleAssistant, Content: complexityChatString("Calling the test tool")},
					{Role: schemas.ChatMessageRoleTool, Content: complexityChatString("Tests passed")},
				}},
			},
			want: InputContinuation,
		},
		{
			name: "responses replay followed by tool output is a continuation",
			req: func() *schemas.BifrostRequest {
				itemType := schemas.ResponsesMessageTypeFunctionCallOutput
				return &schemas.BifrostRequest{
					RequestType: schemas.ResponsesRequest,
					ResponsesRequest: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
						{Role: &userRole, Content: complexityResponsesString("Run the tests")},
						{Type: &itemType},
					}},
				}
			}(),
			want: InputContinuation,
		},
		{
			name: "unsupported operation bypasses session state",
			req:  &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest},
			want: InputBypass,
		},
		{
			name: "codex background request bypasses session state",
			ctx: complexityHarnessContext(schemas.CodexCLI.String(), map[string]string{
				codexTurnMetadataHeader: `{"request_kind":"compaction","session_id":"session-1"}`,
			}),
			req: &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{Input: []schemas.ResponsesMessage{
					{Role: &userRole, Content: complexityResponsesString("Compact this conversation")},
				}},
			},
			want: InputBypass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := BuildInputWithDisposition(tt.ctx, tt.req)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveComplexitySessionID(t *testing.T) {
	tests := []struct {
		name      string
		ctx       *schemas.BifrostContext
		want      string
		wantFound bool
	}{
		{
			name: "explicit bifrost session wins over native metadata",
			ctx: func() *schemas.BifrostContext {
				ctx := complexityHarnessContext(schemas.CodexCLI.String(), map[string]string{
					codexTurnMetadataHeader: `{"request_kind":"turn","session_id":"native-session"}`,
				})
				ctx.SetValue(schemas.BifrostContextKeySessionID, " explicit-session ")
				return ctx
			}(),
			want:      "explicit-session",
			wantFound: true,
		},
		{
			name: "claude native header is accepted for claude code",
			ctx: complexityHarnessContext(schemas.ClaudeCLI.String(), map[string]string{
				claudeCodeSessionIDHeader: "claude-session",
			}),
			want:      "claude-session",
			wantFound: true,
		},
		{
			name: "claude native header is rejected for a generic client",
			ctx: complexityHarnessContext("generic-client/1.0", map[string]string{
				claudeCodeSessionIDHeader: "spoofed-session",
			}),
		},
		{
			name: "codex native metadata is accepted for codex",
			ctx: complexityHarnessContext(schemas.CodexDesktop.String(), map[string]string{
				codexTurnMetadataHeader: `{"request_kind":"turn","session_id":"codex-session"}`,
			}),
			want:      "codex-session",
			wantFound: true,
		},
		{
			name: "invalid request kind does not invalidate codex session identity",
			ctx: complexityHarnessContext(schemas.CodexCLI.String(), map[string]string{
				codexTurnMetadataHeader: `{"request_kind":{},"session_id":"codex-session"}`,
			}),
			want:      "codex-session",
			wantFound: true,
		},
		{
			name: "oversized explicit identity is rejected without native fallback",
			ctx: func() *schemas.BifrostContext {
				ctx := complexityHarnessContext(schemas.CodexCLI.String(), map[string]string{
					codexTurnMetadataHeader: `{"session_id":"native-session"}`,
				})
				ctx.SetValue(schemas.BifrostContextKeySessionID, strings.Repeat("x", maxComplexitySessionIDLength+1))
				return ctx
			}(),
		},
		{
			name: "identity containing nul is rejected",
			ctx: func() *schemas.BifrostContext {
				ctx := complexityHarnessContext("", nil)
				ctx.SetValue(schemas.BifrostContextKeySessionID, "session\x00suffix")
				return ctx
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ResolveComplexitySessionID(tt.ctx)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.want, got)
		})
	}
}

func complexityHarnessContext(userAgent string, headers map[string]string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	if userAgent != "" {
		ctx.SetValue(schemas.BifrostContextKeyUserAgent, userAgent)
	}
	if headers != nil {
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	}
	return ctx
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
