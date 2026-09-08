package bedrock

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The streaming path now closes reasoning items with a populated Summary and the signature
// that signs it, where it previously emitted an empty summary and no token at all. That means
// convertBifrostReasoningToBedrockReasoning now produces blocks on replay where it produced
// none, so #6854's turn-placement logic is newly exercised on this shape.
//
// TestStreamingReasoningReplaySerialisesForBedrock covers the OLD shape (empty summary +
// signature). This covers the new one: reasoning must stay in the assistant turn that produced
// it rather than shifting forward, which is the failure #6854 fixed
// ("`thinking` blocks in the latest assistant message cannot be modified").
func TestStreamedSummaryReplayKeepsReasoningInItsOwnTurn(t *testing.T) {
	sig := "EqQBCgIYAhIM...fixture"

	var input []schemas.ResponsesMessage
	input = append(input, schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Start.")},
	})
	for i := 0; i < 3; i++ {
		input = append(input,
			schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "thinking"}},
					EncryptedContent: &sig,
				},
			},
			schemas.ResponsesMessage{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Answer.")},
			},
			schemas.ResponsesMessage{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Continue.")},
			},
		)
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0", input, false)
	require.NoError(t, err)
	raw, err := sonic.Marshal(messages)
	require.NoError(t, err)
	t.Logf("MESSAGES: %s", raw)

	perTurn := map[int]int{}
	gjson.GetBytes(raw, "#.content").ForEach(func(turn, content gjson.Result) bool {
		content.ForEach(func(_, block gjson.Result) bool {
			if r := block.Get("reasoningContent.reasoningText"); r.Exists() {
				perTurn[int(turn.Int())]++
				if !r.Get("signature").Exists() {
					t.Errorf("turn %d: UNSIGNED reasoning block reached Converse", turn.Int())
				}
				if !r.Get("text").Exists() {
					t.Errorf("turn %d: reasoning block has no text key", turn.Int())
				}
			}
			return true
		})
		return true
	})
	t.Logf("reasoning blocks per turn: %v", perTurn)
	for turn, n := range perTurn {
		if n != 1 {
			t.Errorf("turn %d carries %d reasoning blocks, want exactly 1", turn, n)
		}
	}
	if len(perTurn) != 3 {
		t.Errorf("expected reasoning in 3 assistant turns, got %d", len(perTurn))
	}
}

// The ordinary agent turn #6854 calls out by name: [thinking, tool_use] followed by a tool
// result. Settling the reasoning too early here "silently deleted the thinking block from
// every ordinary agent turn", so the new shape has to land in front of the tool_use, in the
// same assistant turn.
func TestStreamedSummaryReplayKeepsReasoningWithItsToolCall(t *testing.T) {
	sig := "EqQBCgIYAhIM...fixture"
	args := `{"city":"Paris"}`

	input := []schemas.ResponsesMessage{
		{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")}},
		{Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "need the tool"}},
				EncryptedContent: &sig,
			}},
		{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr("tooluse_1"), Name: schemas.Ptr("get_weather"), Arguments: &args,
			}},
		{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr("tooluse_1"), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr(`{"temp_c":18}`)},
			}},
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0", input, false)
	require.NoError(t, err)
	raw, err := sonic.Marshal(messages)
	require.NoError(t, err)
	t.Logf("TOOL LOOP: %s", raw)

	// The assistant turn holding the tool_use must also hold the reasoning, first.
	found := false
	gjson.GetBytes(raw, "#").ForEach(func(_, _ gjson.Result) bool { return true })
	gjson.ParseBytes(raw).ForEach(func(idx, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		blocks := msg.Get("content").Array()
		hasTool, reasoningAt := false, -1
		for i, b := range blocks {
			if b.Get("toolUse").Exists() {
				hasTool = true
			}
			if b.Get("reasoningContent").Exists() {
				reasoningAt = i
			}
		}
		if hasTool {
			found = true
			if reasoningAt != 0 {
				t.Errorf("assistant turn %d: reasoning at index %d, want 0 (before the tool_use)", idx.Int(), reasoningAt)
			}
		}
		return true
	})
	require.True(t, found, "no assistant turn with a tool_use was produced")
}
