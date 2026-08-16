package gemini_test

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A thinking model that spends its whole output budget before emitting a visible
// token returns a candidate with no Content at all: MAX_TOKENS, reasoning tokens
// billed, nothing to show. That is a successful 200 with an empty answer, not a
// filtered or malformed one -- MAX_TOKENS is deliberately absent from
// isErrorFinishReason, so it must not be routed through the error-response path.
//
// The chat-completions contract has no representation for "no choices": OpenAI
// answers a truncated generation with one choice carrying empty content and
// finish_reason "length". A nil Choices array marshals to `"choices":null`, which
// every OpenAI-shaped client dereferences blind -- the provider harness caught this
// as a cache-matrix row failing on `choices:null` with 253 reasoning tokens billed.
func TestContentlessCandidateStillYieldsAChoice(t *testing.T) {
	response := &gemini.GenerateContentResponse{
		ResponseID:   "contentless-test",
		ModelVersion: "gemini-2.5-pro",
		Candidates: []*gemini.Candidate{
			{FinishReason: gemini.FinishReasonMaxTokens},
		},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
			TotalTokenCount:      15748,
			ThoughtsTokenCount:   253,
			CandidatesTokenCount: 253,
		},
	}

	t.Run("non-stream", func(t *testing.T) {
		bifrostResp := response.ToBifrostChatResponse()
		require.NotNil(t, bifrostResp)
		require.Len(t, bifrostResp.Choices, 1, "a 200 chat completion must never carry a null choices array")

		choice := bifrostResp.Choices[0]
		require.NotNil(t, choice.FinishReason)
		assert.Equal(t, "length", *choice.FinishReason, "the real finish reason must survive, not a malformed-call stand-in")

		require.NotNil(t, choice.ChatNonStreamResponseChoice)
		message := choice.ChatNonStreamResponseChoice.Message
		require.NotNil(t, message)
		assert.Equal(t, schemas.ChatMessageRoleAssistant, message.Role)
		require.NotNil(t, message.Content, "content must be present and empty rather than absent")

		// A non-nil ChatMessageContent is not enough: its MarshalJSON emits `null` when
		// both ContentStr and ContentBlocks are nil, which is the same blind-dereference
		// hazard as the null Choices array this test exists to prevent.
		require.NotNil(t, message.Content.ContentStr, "content must be an empty string, not JSON null")
		assert.Equal(t, "", *message.Content.ContentStr)

		encoded, err := json.Marshal(message)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"content":""`, "an OpenAI-shaped client must not receive content:null")
	})

	t.Run("stream", func(t *testing.T) {
		state := gemini.NewGeminiStreamState()
		bifrostResp, bifrostErr, isLast := response.ToBifrostChatCompletionStream(state)
		require.Nil(t, bifrostErr)
		require.NotNil(t, bifrostResp)
		assert.True(t, isLast, "a finish reason with usage closes the stream")
		require.Len(t, bifrostResp.Choices, 1, "the terminal chunk must still carry a choice")

		choice := bifrostResp.Choices[0]
		require.NotNil(t, choice.FinishReason)
		assert.Equal(t, "length", *choice.FinishReason)
		require.NotNil(t, choice.ChatStreamResponseChoice)
		require.NotNil(t, choice.ChatStreamResponseChoice.Delta)
	})

	// An empty Parts slice is the same situation reached by a different wire shape,
	// so it must land on the same answer rather than on the nil-Choices path.
	t.Run("empty parts slice", func(t *testing.T) {
		withEmptyContent := *response
		withEmptyContent.Candidates = []*gemini.Candidate{
			{
				FinishReason: gemini.FinishReasonMaxTokens,
				Content:      &gemini.Content{Role: string(gemini.RoleModel)},
			},
		}

		bifrostResp := withEmptyContent.ToBifrostChatResponse()
		require.Len(t, bifrostResp.Choices, 1)
		require.NotNil(t, bifrostResp.Choices[0].FinishReason)
		assert.Equal(t, "length", *bifrostResp.Choices[0].FinishReason)
	})
}
