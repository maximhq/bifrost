package bedrock_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Wire-level reproduction of issue #6342.
//
// A client on the Anthropic-native ingress (`POST /anthropic/v1/messages`) with a
// `bedrock/` model prefix never takes the Anthropic passthrough -- isClaudeModel in
// transports/bifrost-http/integrations/anthropic.go excludes schemas.Bedrock -- so the
// request is converted Anthropic -> Bifrost Responses -> Bedrock Converse.
//
// Converse has no message-level tool role: a tool result rides a user turn. So every
// dropped tool result also deletes the user turn that separated two assistant turns,
// and ConvertBifrostMessagesToBedrockMessages' "merge consecutive messages with the
// same role" pass then fuses them. With every tool result dropped, a 15-message replay
// collapses to [user, assistant] and every replayed thinking block lands in one
// oversized assistant message at a content index it never occupied upstream. Bedrock
// rejects that non-retryably:
//
//	messages.1.content.5: `thinking` or `redacted_thinking` blocks in the latest
//	assistant message cannot be modified.
//
// which is unrecoverable -- the client replays the same history on every retry.
//
// `content` is optional on an Anthropic tool_result block (only `type` and
// `tool_use_id` are required; see platform.claude.com/docs/en/api/messages,
// ToolResultBlockParam), so a void tool or an errored tool with no body produces
// exactly this payload.
func TestAnthropicIngressBedrockReplayKeepsTurnBoundaries(t *testing.T) {
	// The reporter's conversation: one user turn, then 7 assistant tool-call steps,
	// each answered by a tool result. Steps 0, 3, 4, 5 and 6 carry a signed thinking
	// block, matching their convertToModelMessages dump.
	thinkingOnStep := map[int]bool{0: true, 3: true, 4: true, 5: true, 6: true}

	messages := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "run the tool"}}},
	}
	for step := 0; step < 7; step++ {
		var assistantBlocks []map[string]any
		if thinkingOnStep[step] {
			assistantBlocks = append(assistantBlocks, map[string]any{
				"type":      "thinking",
				"thinking":  fmt.Sprintf("deliberating on step %d", step),
				"signature": fmt.Sprintf("ErUBCkYIBxgCKkA_signature_%d", step),
			})
		}
		assistantBlocks = append(assistantBlocks,
			map[string]any{"type": "text", "text": fmt.Sprintf("calling the tool, step %d", step)},
			map[string]any{
				"type":  "tool_use",
				"id":    fmt.Sprintf("toolu_01Step%02d", step),
				"name":  "get_weather",
				"input": map[string]any{"city": "san francisco"},
			},
		)
		messages = append(messages, map[string]any{"role": "assistant", "content": assistantBlocks})
		// A tool_result with no `content` key -- legal, and what a void or errored
		// tool produces.
		messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{{
			"type":        "tool_result",
			"tool_use_id": fmt.Sprintf("toolu_01Step%02d", step),
		}}})
	}

	body, err := json.Marshal(map[string]any{
		"model":      "bedrock/us.anthropic.claude-opus-4-8",
		"max_tokens": 4096,
		"thinking":   map[string]any{"type": "adaptive"},
		"tools": []map[string]any{{
			"name":         "get_weather",
			"description":  "look up the weather",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		}},
		"messages": messages,
	})
	require.NoError(t, err)

	var ingressReq anthropic.AnthropicMessageRequest
	require.NoError(t, json.Unmarshal(body, &ingressReq))
	require.Len(t, ingressReq.Messages, 15, "fixture must reproduce the reporter's 15-message replay")

	ctx := &schemas.BifrostContext{}
	bifrostReq := ingressReq.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	converseReq, err := bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)

	// 1. Turn boundaries survive: 15 in, 15 out, strictly alternating.
	require.Equalf(t, len(ingressReq.Messages), len(converseReq.Messages),
		"every tool result must keep its user turn; collapsing turns merges the assistant messages behind them (converse roles: %s)",
		converseRoles(converseReq.Messages))

	for i, msg := range converseReq.Messages {
		wantRole := bedrock.BedrockMessageRoleUser
		if i%2 == 1 {
			wantRole = bedrock.BedrockMessageRoleAssistant
		}
		require.Equalf(t, wantRole, msg.Role, "message %d role", i)
	}

	// 2. Every replayed thinking block stays in its own assistant turn, at content
	//    index 0, with its signature byte-for-byte intact. Bedrock verifies each
	//    signature against the block it signed, so a block that moved turn or index
	//    is a modified block.
	seenSignatures := 0
	for step := 0; step < 7; step++ {
		msg := converseReq.Messages[step*2+1]
		if !thinkingOnStep[step] {
			for j, block := range msg.Content {
				require.Nilf(t, block.ReasoningContent,
					"step %d had no thinking block upstream, but content.%d is reasoning", step, j)
			}
			continue
		}

		require.NotNilf(t, msg.Content[0].ReasoningContent,
			"step %d: thinking block must stay first in its own assistant turn", step)
		require.NotNil(t, msg.Content[0].ReasoningContent.ReasoningText)
		require.NotNilf(t, msg.Content[0].ReasoningContent.ReasoningText.Signature,
			"step %d: replay signature dropped", step)
		require.Equalf(t, fmt.Sprintf("ErUBCkYIBxgCKkA_signature_%d", step),
			*msg.Content[0].ReasoningContent.ReasoningText.Signature,
			"step %d: replay signature altered", step)
		seenSignatures++

		// Exactly one reasoning block per turn -- no other turn's thinking migrated in.
		reasoningInTurn := 0
		for _, block := range msg.Content {
			if block.ReasoningContent != nil {
				reasoningInTurn++
			}
		}
		require.Equalf(t, 1, reasoningInTurn, "step %d: expected exactly one reasoning block in the turn", step)
	}
	require.Equal(t, 5, seenSignatures, "all five signed thinking blocks must be asserted")

	// 3. Each tool result is answered in the turn immediately after its call.
	for step := 0; step < 7; step++ {
		userMsg := converseReq.Messages[step*2+2]
		require.Lenf(t, userMsg.Content, 1, "step %d: tool-result turn must hold exactly its own result", step)
		require.NotNilf(t, userMsg.Content[0].ToolResult, "step %d: tool result dropped", step)
		require.Equalf(t, fmt.Sprintf("toolu_01Step%02d", step),
			userMsg.Content[0].ToolResult.ToolUseID, "step %d: tool result paired with the wrong call", step)
	}
}

// converseRoles renders just the role sequence, so a collapse failure reads as
// "user,assistant" instead of a page of content-block pointers.
func converseRoles(messages []bedrock.BedrockMessage) string {
	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, fmt.Sprintf("%s(%d)", m.Role, len(m.Content)))
	}
	return strings.Join(roles, ",")
}

// Interleaved thinking (issue #6342, second defect). On adaptive-thinking models
// Claude reasons *between* tool calls inside a single assistant turn, so one
// assistant message can be [thinking, text, tool_use, thinking, text, tool_use].
//
// Anthropic drops the "assistant turn must begin with a thinking block" rule for
// adaptive thinking (platform.claude.com/docs/en/build-with-claude/extended-thinking,
// "Turn structure in manual mode"), so hoisting every thinking block to the front of
// the turn is not a harmless normalization -- it relocates a signed block the client
// replayed verbatim, which is the same "blocks must remain as they were" violation
// that wedges the conversation.
//
// The turn must arrive at Bedrock in the order the client sent it, whichever order
// that is. Both orders are covered because they exercise different flush sites in
// ConvertBifrostMessagesToBedrockMessages: text-then-tool_use lets the reasoning ride
// into the assistant message being built, while tool_use-then-text has a tool call
// AND buffered reasoning pending at the same time, which is the case that used to
// emit the tool use in front of the reasoning block that preceded it.
func TestAnthropicIngressBedrockPreservesInterleavedThinkingOrder(t *testing.T) {
	think := func(step int) map[string]any {
		return map[string]any{
			"type":      "thinking",
			"thinking":  fmt.Sprintf("deliberating on step %d", step),
			"signature": fmt.Sprintf("ErUBCkYIBxgCKkA_signature_%d", step),
		}
	}
	redacted := func(step int) map[string]any {
		return map[string]any{"type": "redacted_thinking", "data": fmt.Sprintf("EmwKAhgBEgy_redacted_%d", step)}
	}
	text := func(step int) map[string]any {
		return map[string]any{"type": "text", "text": fmt.Sprintf("calling the tool, step %d", step)}
	}
	toolUse := func(step int) map[string]any {
		return map[string]any{
			"type":  "tool_use",
			"id":    fmt.Sprintf("toolu_01Step%02d", step),
			"name":  "get_weather",
			"input": map[string]any{"city": "san francisco"},
		}
	}

	tests := []struct {
		name  string
		order func(step int) []map[string]any
		want  []string
	}{
		{
			name:  "thinking, text, tool_use",
			order: func(step int) []map[string]any { return []map[string]any{think(step), text(step), toolUse(step)} },
			want: []string{
				"reasoning:ErUBCkYIBxgCKkA_signature_0",
				"text:calling the tool, step 0",
				"toolUse:toolu_01Step00",
				"reasoning:ErUBCkYIBxgCKkA_signature_1",
				"text:calling the tool, step 1",
				"toolUse:toolu_01Step01",
			},
		},
		{
			name:  "thinking, tool_use, text",
			order: func(step int) []map[string]any { return []map[string]any{think(step), toolUse(step), text(step)} },
			want: []string{
				"reasoning:ErUBCkYIBxgCKkA_signature_0",
				"toolUse:toolu_01Step00",
				"text:calling the tool, step 0",
				"reasoning:ErUBCkYIBxgCKkA_signature_1",
				"toolUse:toolu_01Step01",
				"text:calling the tool, step 1",
			},
		},
		{
			// No text between the calls: reasoning arrives while the previous tool call is
			// still pending, the case that used to be emitted ahead of it.
			name:  "thinking, tool_use",
			order: func(step int) []map[string]any { return []map[string]any{think(step), toolUse(step)} },
			want: []string{
				"reasoning:ErUBCkYIBxgCKkA_signature_0",
				"toolUse:toolu_01Step00",
				"reasoning:ErUBCkYIBxgCKkA_signature_1",
				"toolUse:toolu_01Step01",
			},
		},
		{
			name:  "redacted_thinking, text, tool_use",
			order: func(step int) []map[string]any { return []map[string]any{redacted(step), text(step), toolUse(step)} },
			want: []string{
				"reasoning:EmwKAhgBEgy_redacted_0", // Converse carries redacted_thinking as reasoningText with the data in signature
				"text:calling the tool, step 0",
				"toolUse:toolu_01Step00",
				"reasoning:EmwKAhgBEgy_redacted_1", // Converse carries redacted_thinking as reasoningText with the data in signature
				"text:calling the tool, step 1",
				"toolUse:toolu_01Step01",
			},
		},
		{
			name: "thinking, redacted_thinking, text, tool_use",
			order: func(step int) []map[string]any {
				return []map[string]any{think(step), redacted(step), text(step), toolUse(step)}
			},
			want: []string{
				"reasoning:ErUBCkYIBxgCKkA_signature_0",
				"reasoning:EmwKAhgBEgy_redacted_0", // Converse carries redacted_thinking as reasoningText with the data in signature
				"text:calling the tool, step 0",
				"toolUse:toolu_01Step00",
				"reasoning:ErUBCkYIBxgCKkA_signature_1",
				"reasoning:EmwKAhgBEgy_redacted_1", // Converse carries redacted_thinking as reasoningText with the data in signature
				"text:calling the tool, step 1",
				"toolUse:toolu_01Step01",
			},
		},
	}

	// A bare Claude id converts before governance picks a provider, so it must keep
	// the order too (issue #7768).
	for _, model := range []string{"bedrock/us.anthropic.claude-opus-4-8", "claude-opus-5-5"} {
		for _, tt := range tests {
			t.Run(model+"/"+tt.name, func(t *testing.T) {
				var assistantBlocks []map[string]any
				for step := 0; step < 2; step++ {
					assistantBlocks = append(assistantBlocks, tt.order(step)...)
				}

				body, err := json.Marshal(map[string]any{
					"model":      model,
					"max_tokens": 4096,
					"thinking":   map[string]any{"type": "adaptive"},
					"tools": []map[string]any{{
						"name":         "get_weather",
						"description":  "look up the weather",
						"input_schema": map[string]any{"type": "object"},
					}},
					"messages": []map[string]any{
						{"role": "user", "content": []map[string]any{{"type": "text", "text": "run the tool twice"}}},
						{"role": "assistant", "content": assistantBlocks},
						{"role": "user", "content": []map[string]any{
							{"type": "tool_result", "tool_use_id": "toolu_01Step00", "content": "sunny"},
							{"type": "tool_result", "tool_use_id": "toolu_01Step01", "content": "windy"},
						}},
					},
				})
				require.NoError(t, err)

				var ingressReq anthropic.AnthropicMessageRequest
				require.NoError(t, json.Unmarshal(body, &ingressReq))

				ctx := &schemas.BifrostContext{}
				converseReq, err := bedrock.ToBedrockResponsesRequest(ctx, ingressReq.ToBifrostResponsesRequest(ctx))
				require.NoError(t, err)

				require.Len(t, converseReq.Messages, 3, "roles: %s", converseRoles(converseReq.Messages))
				turn := converseReq.Messages[1]
				require.Equal(t, bedrock.BedrockMessageRoleAssistant, turn.Role)

				got := make([]string, 0, len(turn.Content))
				for _, block := range turn.Content {
					switch {
					case block.ReasoningContent != nil && block.ReasoningContent.RedactedContent != nil:
						got = append(got, "redacted:"+*block.ReasoningContent.RedactedContent)
					case block.ReasoningContent != nil && block.ReasoningContent.ReasoningText != nil && block.ReasoningContent.ReasoningText.RedactedContent != nil:
						got = append(got, "redacted:"+*block.ReasoningContent.ReasoningText.RedactedContent)
					case block.ReasoningContent != nil && block.ReasoningContent.ReasoningText != nil:
						sig := ""
						if block.ReasoningContent.ReasoningText.Signature != nil {
							sig = *block.ReasoningContent.ReasoningText.Signature
						}
						got = append(got, "reasoning:"+sig)
					case block.Text != nil:
						got = append(got, "text:"+*block.Text)
					case block.ToolUse != nil:
						got = append(got, "toolUse:"+block.ToolUse.ToolUseID)
					default:
						got = append(got, "other")
					}
				}
				require.Equal(t, tt.want, got, "interleaved turn must reach Bedrock in the order the client sent it")
			})
		}
	}
}

// Converse accepts only success|error on toolResult.status
// (docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolResultBlock.html),
// while Bifrost's Responses surface marks a failed tool result "incomplete". The
// egress switch used to let "incomplete" fall through to its default and report the
// failure to the model as a success -- so Bedrock could not read back what its own
// ingress had written (TestConverseToolErrorReachesChatSurface pins the other half).
//
// This matters directly for issue #6342: `is_error: true` with no body is one of the
// three tool_result shapes that used to be dropped entirely, so fixing the drop is
// what starts routing these results through this switch.
func TestAnthropicIngressBedrockToolErrorKeepsErrorStatus(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model":      "bedrock/us.anthropic.claude-opus-4-8",
		"max_tokens": 4096,
		"tools": []map[string]any{{
			"name":         "run",
			"description":  "run a command",
			"input_schema": map[string]any{"type": "object"},
		}},
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{{"type": "text", "text": "run it"}}},
			{"role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "running"},
				{"type": "tool_use", "id": "toolu_failed", "name": "run", "input": map[string]any{}},
			}},
			// is_error with no content: legal, and one of the shapes that used to be dropped.
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_failed", "is_error": true},
			}},
		},
	})
	require.NoError(t, err)

	var ingressReq anthropic.AnthropicMessageRequest
	require.NoError(t, json.Unmarshal(body, &ingressReq))

	ctx := &schemas.BifrostContext{}
	converseReq, err := bedrock.ToBedrockResponsesRequest(ctx, ingressReq.ToBifrostResponsesRequest(ctx))
	require.NoError(t, err)

	require.Len(t, converseReq.Messages, 3, "roles: %s", converseRoles(converseReq.Messages))

	var result *bedrock.BedrockToolResult
	for _, block := range converseReq.Messages[2].Content {
		if block.ToolResult != nil {
			result = block.ToolResult
			break
		}
	}
	require.NotNil(t, result, "content-less is_error tool_result must still reach Converse")
	require.Equal(t, "toolu_failed", result.ToolUseID)
	require.NotNil(t, result.Status, "Converse requires a readable status for a failed tool call")
	require.Equal(t, "error", *result.Status, "is_error must not be reported to the model as a success")
}

// TestAnthropicIngressBedrockReplayKeepsReasoningInItsOwnTurn pins the invariant PR
// #6854 restores: every assistant turn reaches Converse carrying exactly the reasoning
// blocks the client sent for THAT turn, and no others.
//
// Measured against Bedrock Converse with real signed blocks, the refusal
//
//	messages.N.content.M: `thinking` or `redacted_thinking` blocks in the latest
//	assistant message cannot be modified.
//
// fires on exactly one mutation: an EXTRA reasoning block in an assistant turn that is
// a tool continuation. Removing a block, reordering blocks and fusing turns are all
// accepted, so a block that migrates from turn N into turn N+1 is the shape that breaks
// a replay -- turn N+1 ends up with two where the model produced one.
//
// Two directions are covered, because fixing one broke the other once already:
//
//   - "thinking last" is the shape that migrated. pendingReasoningContentBlocks was
//     consumed by an assistant message or a tool-call flush and by nothing else, so
//     reasoning still buffered when a user turn arrived was carried into the next
//     assistant message.
//   - "thinking first" is the shape the first attempt at that fix deleted outright. It
//     settled the buffer before the tool-call flush could claim it, and the message
//     behind it is the user turn that opened the loop, so the block was dropped from
//     every ordinary agent turn. Bedrock accepts a missing block with a 200, which is
//     why this direction needs a converter-level test: it is invisible from the wire.
func TestAnthropicIngressBedrockReplayKeepsReasoningInItsOwnTurn(t *testing.T) {
	thinking := func(step int) map[string]any {
		return map[string]any{
			"type":      "thinking",
			"thinking":  fmt.Sprintf("deliberating on step %d", step),
			"signature": fmt.Sprintf("ErUBCkYIBxgCKkA_signature_%d", step),
		}
	}
	text := func(step int) map[string]any {
		return map[string]any{"type": "text", "text": fmt.Sprintf("working on step %d", step)}
	}
	toolUse := func(step int) map[string]any {
		return map[string]any{
			"type": "tool_use", "id": fmt.Sprintf("toolu_01Step%02d", step),
			"name": "get_weather", "input": map[string]any{"city": "tokyo"},
		}
	}

	shapes := []struct {
		name   string
		blocks func(step int) []map[string]any
	}{
		// The order Anthropic emits by default.
		{"thinking first", func(s int) []map[string]any {
			return []map[string]any{thinking(s), text(s), toolUse(s)}
		}},
		// Adaptive thinking, a max_tokens truncation, or a trailing block the ingress
		// dropped all leave the thinking block last.
		{"thinking last", func(s int) []map[string]any {
			return []map[string]any{text(s), toolUse(s), thinking(s)}
		}},
		// Interleaved: the model thought again after its first call.
		{"interleaved", func(s int) []map[string]any {
			return []map[string]any{thinking(s), toolUse(s), text(s)}
		}},
	}

	const rounds = 3
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			messages := []map[string]any{
				{"role": "user", "content": []map[string]any{{"type": "text", "text": "run the tool"}}},
			}
			for step := 0; step < rounds; step++ {
				messages = append(messages,
					map[string]any{"role": "assistant", "content": shape.blocks(step)},
					map[string]any{"role": "user", "content": []map[string]any{{
						"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_01Step%02d", step),
						"content": "18C, foggy",
					}}},
				)
			}

			body, err := json.Marshal(map[string]any{
				"model":      "bedrock/global.anthropic.claude-opus-5",
				"max_tokens": 4096,
				"thinking":   map[string]any{"type": "adaptive"},
				"tools": []map[string]any{{
					"name": "get_weather", "description": "look up the weather",
					"input_schema": map[string]any{"type": "object",
						"properties": map[string]any{"city": map[string]any{"type": "string"}}},
				}},
				"messages": messages,
			})
			require.NoError(t, err)

			var ingressReq anthropic.AnthropicMessageRequest
			require.NoError(t, json.Unmarshal(body, &ingressReq))

			ctx := &schemas.BifrostContext{}
			converseReq, err := bedrock.ToBedrockResponsesRequest(ctx, ingressReq.ToBifrostResponsesRequest(ctx))
			require.NoError(t, err)

			require.Equalf(t, len(messages), len(converseReq.Messages),
				"turn boundaries must survive (converse roles: %s)", converseRoles(converseReq.Messages))

			for step := 0; step < rounds; step++ {
				msg := converseReq.Messages[step*2+1]
				require.Equal(t, bedrock.BedrockMessageRoleAssistant, msg.Role, "message %d", step*2+1)

				var signatures []string
				for _, block := range msg.Content {
					if block.ReasoningContent == nil || block.ReasoningContent.ReasoningText == nil {
						continue
					}
					require.NotNilf(t, block.ReasoningContent.ReasoningText.Signature,
						"step %d: a reasoning block reached Converse unsigned", step)
					signatures = append(signatures, *block.ReasoningContent.ReasoningText.Signature)
				}

				// Exactly one, and it is this turn's own. Zero means the block was dropped;
				// more than one means another turn's block migrated in, which is what
				// Bedrock refuses with "cannot be modified".
				require.Lenf(t, signatures, 1,
					"step %d: expected exactly the one reasoning block this turn was sent, got %d (%v)",
					step, len(signatures), signatures)
				require.Equalf(t, fmt.Sprintf("ErUBCkYIBxgCKkA_signature_%d", step), signatures[0],
					"step %d: this turn is carrying another turn's signature", step)
			}
		})
	}
}

// TestAnthropicIngressBedrockTopLevelCacheControl pins issue #8541: Anthropic's automatic caching
// (a top-level cache_control on POST /anthropic/v1/messages with a bedrock/ model) must become a
// Converse cachePoint after the last cacheable block, the same place the Claude API puts the
// automatic breakpoint. It used to be parked in ExtraParams and never read, so the request billed
// as uncached input with no error.
func TestAnthropicIngressBedrockTopLevelCacheControl(t *testing.T) {
	longSystem := strings.Repeat("You are a meticulous assistant. ", 64)

	convert := func(t *testing.T, payload map[string]any) (*schemas.BifrostResponsesRequest, *bedrock.BedrockConverseRequest) {
		t.Helper()
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		var ingressReq anthropic.AnthropicMessageRequest
		require.NoError(t, json.Unmarshal(body, &ingressReq))
		ctx := &schemas.BifrostContext{}
		bifrostReq := ingressReq.ToBifrostResponsesRequest(ctx)
		require.NotNil(t, bifrostReq)
		converseReq, err := bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
		require.NoError(t, err)
		return bifrostReq, converseReq
	}
	countCachePoints := func(req *bedrock.BedrockConverseRequest) int {
		n := 0
		for _, s := range req.System {
			if s.CachePoint != nil {
				n++
			}
		}
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.CachePoint != nil {
					n++
				}
			}
		}
		return n
	}

	t.Run("string system and user turn get a trailing cachePoint", func(t *testing.T) {
		bifrostReq, converseReq := convert(t, map[string]any{
			"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
			"max_tokens":    64,
			"cache_control": map[string]any{"type": "ephemeral"},
			"system":        longSystem,
			"messages":      []map[string]any{{"role": "user", "content": "Say OK."}},
		})
		require.NotEmpty(t, converseReq.Messages)
		last := converseReq.Messages[len(converseReq.Messages)-1].Content
		require.GreaterOrEqual(t, len(last), 2, "expected [text, cachePoint] on the last message, got %+v", last)
		require.NotNil(t, last[len(last)-1].CachePoint,
			"top-level cache_control must become a cachePoint after the last cacheable block; last message content: %+v", last)
		require.Nil(t, last[len(last)-1].CachePoint.TTL, "no ttl on the marker means Bedrock's 5m default")
		require.NotNil(t, last[len(last)-2].Text)
		require.Equal(t, 1, countCachePoints(converseReq), "automatic caching spends exactly one breakpoint")
		require.NotContains(t, converseReq.ExtraParams, "cache_control", "the marker must not be forwarded as an unknown field")
		require.Contains(t, bifrostReq.Params.ExtraParams, "cache_control",
			"the shared Bifrost request must keep the marker so a fallback provider still sees it")
	})

	t.Run("ttl 1h is carried onto the cachePoint", func(t *testing.T) {
		_, converseReq := convert(t, map[string]any{
			"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
			"max_tokens":    64,
			"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
			"system":        longSystem,
			"messages":      []map[string]any{{"role": "user", "content": "Say OK."}},
		})
		last := converseReq.Messages[len(converseReq.Messages)-1].Content
		cp := last[len(last)-1].CachePoint
		require.NotNil(t, cp, "last message content: %+v", last)
		require.NotNil(t, cp.TTL)
		require.Equal(t, "1h", *cp.TTL)
	})

	t.Run("explicit marker on the last block makes automatic caching a no-op", func(t *testing.T) {
		_, converseReq := convert(t, map[string]any{
			"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
			"max_tokens":    64,
			"cache_control": map[string]any{"type": "ephemeral"},
			"system":        longSystem,
			"messages": []map[string]any{{"role": "user", "content": []map[string]any{{
				"type": "text", "text": "Say OK.", "cache_control": map[string]any{"type": "ephemeral"},
			}}}},
		})
		require.Equal(t, 1, countCachePoints(converseReq), "the explicit breakpoint already covers the last block")
	})

	t.Run("explicit system breakpoint plus automatic caching uses two slots", func(t *testing.T) {
		_, converseReq := convert(t, map[string]any{
			"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
			"max_tokens":    64,
			"cache_control": map[string]any{"type": "ephemeral"},
			"system": []map[string]any{{
				"type": "text", "text": longSystem, "cache_control": map[string]any{"type": "ephemeral"},
			}},
			"messages": []map[string]any{
				{"role": "user", "content": "first question"},
				{"role": "assistant", "content": "first answer"},
				{"role": "user", "content": "second question"},
			},
		})
		require.Equal(t, 2, countCachePoints(converseReq))
		last := converseReq.Messages[len(converseReq.Messages)-1].Content
		require.NotNil(t, last[len(last)-1].CachePoint, "the automatic breakpoint lands on the last message: %+v", last)
	})

	t.Run("trailing tool result is the last cacheable block", func(t *testing.T) {
		_, converseReq := convert(t, map[string]any{
			"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
			"max_tokens":    64,
			"cache_control": map[string]any{"type": "ephemeral"},
			"system":        longSystem,
			"tools": []map[string]any{{
				"name": "get_weather", "description": "look up the weather",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
			}},
			"messages": []map[string]any{
				{"role": "user", "content": "weather in Paris?"},
				{"role": "assistant", "content": []map[string]any{{
					"type": "tool_use", "id": "toolu_01Paris", "name": "get_weather", "input": map[string]any{"city": "Paris"},
				}}},
				{"role": "user", "content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "toolu_01Paris", "content": "sunny",
				}}},
			},
		})
		last := converseReq.Messages[len(converseReq.Messages)-1].Content
		require.GreaterOrEqual(t, len(last), 2, "last message content: %+v", last)
		require.NotNil(t, last[len(last)-2].ToolResult)
		require.NotNil(t, last[len(last)-1].CachePoint)
	})

	t.Run("no top-level marker adds no cachePoint", func(t *testing.T) {
		_, converseReq := convert(t, map[string]any{
			"model":      "bedrock/eu.anthropic.claude-sonnet-5-5",
			"max_tokens": 64,
			"system":     longSystem,
			"messages":   []map[string]any{{"role": "user", "content": "Say OK."}},
		})
		require.Equal(t, 0, countCachePoints(converseReq))
	})
}

// TestAnthropicIngressBedrockTopLevelCacheControlTTLConflict is the /anthropic/v1/messages side of
// the PR #8573 TTL-conflict review finding: an explicit 5m marker on the last block plus a 1h
// top-level cache_control is a 400 on the Claude API, so the Bedrock path rejects it too rather
// than silently caching for 5 minutes.
func TestAnthropicIngressBedrockTopLevelCacheControlTTLConflict(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
		"max_tokens":    64,
		"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{{
			"type": "text", "text": "Say OK.", "cache_control": map[string]any{"type": "ephemeral"},
		}}}},
	})
	require.NoError(t, err)
	var ingressReq anthropic.AnthropicMessageRequest
	require.NoError(t, json.Unmarshal(body, &ingressReq))
	ctx := &schemas.BifrostContext{}
	bifrostReq := ingressReq.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	_, err = bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.Error(t, err, "a 5m explicit marker under a 1h top-level cache_control must be rejected")
	require.True(t, providerUtils.IsInvalidRequestError(err), "the conflict is a caller fault and must surface as a 400, got %v", err)
	require.Contains(t, err.Error(), "cache_control")
}

// TestAnthropicIngressBedrockTopLevelCacheControlUnsupportedTTL is the /anthropic/v1/messages
// side of the unsupported-TTL review finding: a top-level ttl Bedrock cannot honour is a 400.
func TestAnthropicIngressBedrockTopLevelCacheControlUnsupportedTTL(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model":         "bedrock/eu.anthropic.claude-sonnet-5-5",
		"max_tokens":    64,
		"cache_control": map[string]any{"type": "ephemeral", "ttl": "2h"},
		"messages":      []map[string]any{{"role": "user", "content": "Say OK."}},
	})
	require.NoError(t, err)
	var ingressReq anthropic.AnthropicMessageRequest
	require.NoError(t, json.Unmarshal(body, &ingressReq))
	ctx := &schemas.BifrostContext{}
	bifrostReq := ingressReq.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	_, err = bedrock.ToBedrockResponsesRequest(ctx, bifrostReq)
	require.Error(t, err, "a top-level ttl of 2h must be rejected, not cached for 5m")
	require.True(t, providerUtils.IsInvalidRequestError(err), "unsupported ttl is a caller fault and must surface as a 400, got %v", err)
}
