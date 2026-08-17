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

// The generateContent twin of the guarantee above, for callers coming in on
// /genai/v1beta/models/{model}:generateContent instead of the chat route.
//
// GenerateContentResponse.Candidates is tagged `omitempty`, so dropping the
// candidate does not ship `"candidates":[]` -- it ships a body with no candidates
// key at all, leaving only usageMetadata. The provider harness caught exactly that:
// a 200 whose entire body was usageMetadata with 253 thought tokens billed and
// candidatesTokensDetails tokenCount 0. Every Gemini-shaped client reads
// candidates[0] blind, so the contentless case must still produce one candidate
// carrying the real finish reason.
func TestContentlessResponseStillYieldsACandidate(t *testing.T) {
	maxTokens := &schemas.ResponsesResponseIncompleteDetails{
		Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens,
	}

	// A thinking model that burns its whole budget before emitting a visible token
	// returns no output items whatsoever.
	t.Run("no output items", func(t *testing.T) {
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:             "gemini-2.5-pro",
			IncompleteDetails: maxTokens,
		})

		require.NotNil(t, geminiResp)
		require.Len(t, geminiResp.Candidates, 1, "a 200 generateContent body must never omit candidates")
		assert.Equal(t, gemini.FinishReasonMaxTokens, geminiResp.Candidates[0].FinishReason,
			"the real finish reason must survive rather than being dropped with the candidate")

		encoded, err := json.Marshal(geminiResp)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"candidates"`,
			"omitempty must not be allowed to erase the candidates key")
	})

	// The same situation reached by a different route: output items exist but none of
	// them convert to a renderable part, so the accumulated parts slice ends up empty.
	t.Run("output items with no renderable parts", func(t *testing.T) {
		emptyText := ""
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:             "gemini-2.5-pro",
			IncompleteDetails: maxTokens,
			Output: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{ContentStr: &emptyText},
				},
			},
		})

		require.NotNil(t, geminiResp)
		require.Len(t, geminiResp.Candidates, 1)
		assert.Equal(t, gemini.FinishReasonMaxTokens, geminiResp.Candidates[0].FinishReason)
	})

	// Server-side tool parts are preserved verbatim at parse time so a Gemini turn
	// can be replayed losslessly, signatures included. They are read before the
	// output loop, so they exist independently of whether any output item does --
	// a turn whose only act was a server-side tool call, cut short before it wrote
	// anything visible, has preserved parts and an empty Output at once. The
	// no-output branch has to carry them for the same reason the loop branch does;
	// dropping them there discards the replay payload the parse step went out of
	// its way to keep.
	t.Run("no output items still carries preserved server-side tool parts", func(t *testing.T) {
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:             "gemini-2.5-pro",
			IncompleteDetails: maxTokens,
			ProviderExtraFields: map[string]interface{}{
				"serverSideToolParts": []*gemini.Part{
					{
						ToolCall:         &gemini.ToolCall{ToolType: "GOOGLE_SEARCH_WEB", ID: "tc-1"},
						ThoughtSignature: []byte{0x0A, 0x0B},
					},
				},
			},
		})

		require.NotNil(t, geminiResp)
		require.Len(t, geminiResp.Candidates, 1)
		require.NotNil(t, geminiResp.Candidates[0].Content)
		require.Len(t, geminiResp.Candidates[0].Content.Parts, 1,
			"the preserved tool part must survive the no-output branch")

		part := geminiResp.Candidates[0].Content.Parts[0]
		require.NotNil(t, part.ToolCall)
		assert.Equal(t, "GOOGLE_SEARCH_WEB", part.ToolCall.ToolType)
		assert.Equal(t, "tc-1", part.ToolCall.ID)
		assert.Equal(t, []byte{0x0A, 0x0B}, part.ThoughtSignature,
			"the signature is what makes the replay verifiable, so it must survive too")
	})

	// Emitting the terminal candidate unconditionally fixed the zero-candidate case
	// but broke the one above it: when a role change has already flushed a candidate
	// and the messages after it contribute nothing, the unconditional append adds a
	// SECOND candidate that is empty and carries the finish reason, while the first
	// carries the content and none. A Gemini-shaped client reads candidates[0], so it
	// sees content with no finish reason and an inexplicable sibling.
	//
	// Skipping the append instead would lose the finish reason entirely, so it moves
	// onto the candidate that actually exists.
	t.Run("a flushed candidate absorbs the finish reason instead of gaining an empty sibling", func(t *testing.T) {
		text := "Paris"
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:             "gemini-2.5-pro",
			IncompleteDetails: maxTokens,
			Output: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{ContentStr: &text},
				},
				// Role flips, flushing the candidate above; this one yields no parts.
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}},
				},
			},
		})

		require.NotNil(t, geminiResp)
		require.Len(t, geminiResp.Candidates, 1,
			"a payload-free tail must not become a second, empty candidate")
		require.NotNil(t, geminiResp.Candidates[0].Content)
		require.Len(t, geminiResp.Candidates[0].Content.Parts, 1)
		assert.Equal(t, "Paris", geminiResp.Candidates[0].Content.Parts[0].Text)
		assert.Equal(t, gemini.FinishReasonMaxTokens, geminiResp.Candidates[0].FinishReason,
			"the finish reason must land on the candidate that carries the content")
	})

	// The role-change guard tested the raw slice length while the filter ran after it,
	// so a flush of nothing but payload-free parts still produced a candidate whose
	// content was empty. The guard has to judge what survives the filter.
	t.Run("no candidate is emitted with empty content beside a populated one", func(t *testing.T) {
		text := "Paris"
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model: "gemini-2.5-pro",
			Output: []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{},
					},
				},
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: &text},
				},
			},
		})

		require.NotNil(t, geminiResp)
		// Asserted positively first. A bare range-and-check passes when Candidates is
		// empty and passes again when an extra candidate has a nil Content -- both of
		// which are exactly the failures this case exists to catch, so the loop alone
		// pins nothing.
		require.Len(t, geminiResp.Candidates, 1, "the payload-free group must not become its own candidate")
		require.NotNil(t, geminiResp.Candidates[0].Content)
		require.Len(t, geminiResp.Candidates[0].Content.Parts, 1)
		assert.Equal(t, "Paris", geminiResp.Candidates[0].Content.Parts[0].Text)

		for i, c := range geminiResp.Candidates {
			require.NotNil(t, c.Content, "candidate %d has nil content", i)
			for j, p := range c.Content.Parts {
				encoded, err := json.Marshal(p)
				require.NoError(t, err)
				assert.NotEqual(t, "{}", string(encoded), "candidate %d part %d is payload-free", i, j)
			}
		}
	})

	// The finish-reason transfer must not cost the response its metadata. Candidates
	// flushed by the role-change branch carry only Index and Content, and grounding,
	// safety ratings and avgLogprobs are attached solely by buildGeminiTerminalCandidate
	// -- so a branch that skips that call drops all of them for the whole response. A
	// grounded web-search turn whose trailing role group produces nothing would lose
	// groundingMetadata entirely, which the unconditional append used to preserve.
	t.Run("finish-reason transfer keeps grounding and preserved metadata", func(t *testing.T) {
		text := "Paris is the capital of France."
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:             "gemini-2.5-pro",
			IncompleteDetails: maxTokens,
			ProviderExtraFields: map[string]interface{}{
				"avgLogprobs": -0.25,
			},
			Output: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{ContentStr: &text},
				},
				// Role flips and the trailing group yields nothing, so the transfer
				// branch runs instead of the append.
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}},
				},
			},
		})

		require.NotNil(t, geminiResp)
		require.Len(t, geminiResp.Candidates, 1)
		c := geminiResp.Candidates[0]
		assert.Equal(t, gemini.FinishReasonMaxTokens, c.FinishReason, "finish reason must transfer")
		assert.Equal(t, -0.25, c.AvgLogprobs,
			"metadata attached only by buildGeminiTerminalCandidate must survive the transfer branch")
	})

	// Preserved server-side tool parts are replayed AHEAD of generated content, which is
	// what reproduces Gemini's own ordering. Prepending them to the terminal role group
	// puts them behind anything an earlier role change already flushed.
	t.Run("preserved tool parts lead the first candidate, not the last", func(t *testing.T) {
		text := "Paris"
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model: "gemini-2.5-pro",
			ProviderExtraFields: map[string]interface{}{
				"serverSideToolParts": []*gemini.Part{
					{ToolCall: &gemini.ToolCall{ToolType: "GOOGLE_SEARCH_WEB", ID: "tc-1"}},
				},
			},
			Output: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{ContentStr: &text},
				},
				// Role change flushes the assistant group into candidates[0].
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: &text},
				},
			},
		})

		require.NotNil(t, geminiResp)
		require.NotEmpty(t, geminiResp.Candidates)
		first := geminiResp.Candidates[0]
		require.NotNil(t, first.Content)
		require.NotEmpty(t, first.Content.Parts)
		require.NotNil(t, first.Content.Parts[0].ToolCall,
			"the preserved tool call must lead the FIRST candidate, ahead of generated content")
		assert.Equal(t, "tc-1", first.Content.Parts[0].ToolCall.ID)
	})

	// A normal completion must be untouched by the change above.
	t.Run("ordinary response is unaffected", func(t *testing.T) {
		text := "Paris"
		geminiResp := gemini.ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model: "gemini-2.5-pro",
			Output: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{ContentStr: &text},
				},
			},
		})

		require.NotNil(t, geminiResp)
		require.Len(t, geminiResp.Candidates, 1)
		require.NotNil(t, geminiResp.Candidates[0].Content)
		require.Len(t, geminiResp.Candidates[0].Content.Parts, 1)
		assert.Equal(t, "Paris", geminiResp.Candidates[0].Content.Parts[0].Text)
		assert.Equal(t, gemini.FinishReasonStop, geminiResp.Candidates[0].FinishReason)
	})
}
