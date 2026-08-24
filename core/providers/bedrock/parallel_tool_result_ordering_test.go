package bedrock

// Regression test for the non-deterministic tool_result ordering bug.
//
// When an assistant turn contains N parallel tool_use blocks, the corresponding
// user turn must list the tool_result blocks in the same order.  Bedrock rejects
// requests where the counts or ordering don't match ("toolResult blocks exceed
// toolUse blocks of previous turn").
//
// Root cause: ConvertBifrostMessagesToBedrockMessages stores pending results in
// a map[string]*ToolResult and then iterates that map directly, producing
// non-deterministic output.  Running the conversion many times should reveal
// the ordering instability.
//
// This test mirrors the production payload that triggered the bug:
//   messages[11] = assistant: [tool_use A, tool_use B]
//   messages[12] = user:      [tool_result A, tool_result B]

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildParallelToolPayload builds a BifrostResponsesRequest that mirrors the
// 13-message DEP-6385 production conversation.  We only need the last 3 turns
// to reproduce the ordering bug, but include several prior turns to stay close
// to the real payload.
func buildParallelToolPayload() *schemas.BifrostResponsesRequest {
	ptr := func(s string) *string { return &s }
	msgType := func(t schemas.ResponsesMessageType) *schemas.ResponsesMessageType { return &t }
	role := func(r schemas.ResponsesMessageRoleType) *schemas.ResponsesMessageRoleType {
		return &r
	}
	output := func(s string) *schemas.ResponsesToolMessageOutputStruct {
		return &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: ptr(s)}
	}

	fc := func(callID, name, args string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: msgType(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    ptr(callID),
				Name:      ptr(name),
				Arguments: ptr(args),
			},
		}
	}
	fco := func(callID, result string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: msgType(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: ptr(callID),
				Output: output(result),
			},
		}
	}

	return &schemas.BifrostResponsesRequest{
		Model: "eu.anthropic.claude-sonnet-4-6",
		Input: []schemas.ResponsesMessage{
			// [0] user
			{
				Type: msgType(schemas.ResponsesMessageTypeMessage),
				Role: role(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: ptr("Investigate the security alert for CVE-2026-34182 in ecr-mirror"),
				},
			},
			// [1,2] assistant calls 2 tools in parallel; [3,4] results
			fc("tooluse_A1", "web_fetch", `{"url":"https://security-tracker.debian.org/tracker/CVE-2026-34182"}`),
			fc("tooluse_A2", "get_repos", `{"repository_name":"ecr-mirror"}`),
			fco("tooluse_A1", `{"cve":"CVE-2026-34182","severity":"HIGH","description":"heap overflow in libexpat"}`),
			fco("tooluse_A2", `{"repos":["ecr-mirror"]}`),
			// [5,6] single tool call + result
			fc("tooluse_B1", "search_k8s_deployments", `{"image":"ecr-mirror"}`),
			fco("tooluse_B1", `{"deployments":["deploy/ecr-mirror"]}`),
			// [7,8] single tool call + result
			fc("tooluse_C1", "get_deployment_details", `{"name":"ecr-mirror"}`),
			fco("tooluse_C1", `{"image":"sha256:abc","tag":"v1.2.3"}`),
			// [9,10] single tool call + result
			fc("tooluse_D1", "get_image_digest", `{"image":"ecr-mirror","tag":"v1.2.3"}`),
			fco("tooluse_D1", `{"digest":"sha256:deadbeef"}`),
			// [11,12] assistant calls 2 tools in parallel (the failing pair)
			fc("tooluse_RwHN0v2n5kuNuZ2qoMV3SN", "web_fetch", `{"url":"https://security-tracker.debian.org/tracker/CVE-2026-34182"}`),
			fc("tooluse_WXSFqSts5GjTjKpIeyBs24", "get_repos", `{"repository_name":"ecr-mirror"}`),
			fco("tooluse_RwHN0v2n5kuNuZ2qoMV3SN", `{"detail":"The vulnerability affects libexpat < 2.7.1. The base image uses libexpat 2.6.4."}`),
			fco("tooluse_WXSFqSts5GjTjKpIeyBs24", `{"repos":["ecr-mirror","ecr-mirror-staging"]}`),
		},
	}
}

// TestParallelToolResultOrdering verifies that tool_result blocks in the output
// Bedrock message are in the same order as the tool_use blocks in the preceding
// assistant message.  We run many iterations to expose the non-determinism.
func TestParallelToolResultOrdering(t *testing.T) {
	req := buildParallelToolPayload()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	const iterations = 200
	for i := 0; i < iterations; i++ {
		bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
		require.NoError(t, err, "iteration %d: conversion failed", i)
		require.NotNil(t, bedrockReq)

		msgs := bedrockReq.Messages
		require.NotEmpty(t, msgs, "iteration %d: no messages", i)

		// Find the last assistant message with 2 tool_use blocks.
		lastAssistantIdx := -1
		for j := len(msgs) - 1; j >= 0; j-- {
			if msgs[j].Role == BedrockMessageRoleAssistant {
				toolUseCount := 0
				for _, b := range msgs[j].Content {
					if b.ToolUse != nil {
						toolUseCount++
					}
				}
				if toolUseCount == 2 {
					lastAssistantIdx = j
					break
				}
			}
		}
		require.NotEqual(t, -1, lastAssistantIdx,
			"iteration %d: could not find assistant message with 2 tool_use blocks", i)

		// Collect tool_use IDs in order.
		var toolUseIDs []string
		for _, b := range msgs[lastAssistantIdx].Content {
			if b.ToolUse != nil {
				toolUseIDs = append(toolUseIDs, b.ToolUse.ToolUseID)
			}
		}
		require.Len(t, toolUseIDs, 2,
			"iteration %d: expected 2 tool_use blocks, got %d", i, len(toolUseIDs))

		// The next message must be a user message with matching tool_result blocks.
		require.Less(t, lastAssistantIdx+1, len(msgs),
			"iteration %d: no user message following the assistant tool_use message", i)
		userMsg := msgs[lastAssistantIdx+1]
		assert.Equal(t, BedrockMessageRoleUser, userMsg.Role,
			"iteration %d: message after parallel tool_use is not a user message", i)

		var toolResultIDs []string
		for _, b := range userMsg.Content {
			if b.ToolResult != nil {
				toolResultIDs = append(toolResultIDs, b.ToolResult.ToolUseID)
			}
		}
		assert.Len(t, toolResultIDs, 2,
			"iteration %d: expected 2 tool_result blocks, got %d", i, len(toolResultIDs))

		// The key assertion: tool_result order must match tool_use order.
		assert.Equal(t, toolUseIDs, toolResultIDs,
			"iteration %d: tool_result order %v does not match tool_use order %v — Bedrock will reject this",
			i, toolResultIDs, toolUseIDs)
	}
}

// reportedGeminiToolUseID is the id from the OpenCode/Bedrock report: a Gemini-minted call id
// with an embedded thought signature, replayed onto a glm-5 / kimi-2.5 turn.
const reportedGeminiToolUseID = "d556q31u_ts_AY89a18eH3FucoBHPCdX6w7jgIhjSnIj7hU_mGofiw3SE2HL8kuMRcnfV3pGV7iUXRi_YQVYUgCH4bmcbYrS03yxWRtsPFb7KHPbp_iRinWZKPHtiyGVRlXfTsqeDBZOt3YNzKL-ycnZh0WeoLthSqnjkeZKT6idSNfd"

var bedrockToolUseIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)

// TestBedrockAliasToolUseID verifies the alias only rewrites ids Bedrock would reject, so
// every id that works today stays byte-identical on the wire.
func TestBedrockAliasToolUseID(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		wantSameID bool
	}{
		{"anthropic id", "tooluse_RwHN0v2n5kuNuZ2qoMV3SN", true},
		{"openai id", "call_9v2n5kuNuZ2qoMV3SNRwHN0", true},
		{"kimi id keeps dots and colons", "functions.read:0", true},
		{"exactly 64 chars", strings.Repeat("a", 64), true},
		{"65 chars", strings.Repeat("a", 65), false},
		{"gemini thought signature", reportedGeminiToolUseID, false},
		{"unsafe characters", "call/abc 123", false},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bedrockAliasToolUseID(tc.id)
			assert.Regexp(t, bedrockToolUseIDPattern, got, "alias violates Bedrock's toolUseId constraint")
			if tc.wantSameID {
				assert.Equal(t, tc.id, got, "id Bedrock already accepts must pass through unchanged")
			} else {
				assert.Equal(t, got, bedrockAliasToolUseID(tc.id), "alias must be deterministic")
			}
		})
	}

	// The hash covers the full id, so ids sharing a truncated head still alias apart.
	head := strings.Repeat("z", 70)
	assert.NotEqual(t, bedrockAliasToolUseID(head+"_one"), bedrockAliasToolUseID(head+"_two"),
		"ids sharing a truncated head must not collide")
}

// TestBedrockToolUseIDPairingResponsesPath verifies an over-long id is aliased identically on
// the tool_use and its tool_result, so Bedrock can still pair them.
func TestBedrockToolUseIDPairingResponsesPath(t *testing.T) {
	ptr := func(s string) *string { return &s }
	msgType := func(t schemas.ResponsesMessageType) *schemas.ResponsesMessageType { return &t }

	req := &schemas.BifrostResponsesRequest{
		Model: "zai.glm-5",
		Input: []schemas.ResponsesMessage{
			{
				Type: msgType(schemas.ResponsesMessageTypeMessage),
				Role: func(r schemas.ResponsesMessageRoleType) *schemas.ResponsesMessageRoleType { return &r }(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: ptr("What did we do so far?"),
				},
			},
			{
				Type: msgType(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    ptr(reportedGeminiToolUseID),
					Name:      ptr("bash"),
					Arguments: ptr(`{"command":"ls"}`),
				},
			},
			{
				Type: msgType(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: ptr(reportedGeminiToolUseID),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: ptr("build/ src/")},
				},
			},
		},
	}

	bedrockReq, err := ToBedrockResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req)
	require.NoError(t, err)

	var toolUseIDs, toolResultIDs []string
	for _, msg := range bedrockReq.Messages {
		for _, block := range msg.Content {
			if block.ToolUse != nil {
				toolUseIDs = append(toolUseIDs, block.ToolUse.ToolUseID)
			}
			if block.ToolResult != nil {
				toolResultIDs = append(toolResultIDs, block.ToolResult.ToolUseID)
			}
		}
	}

	require.Len(t, toolUseIDs, 1)
	require.Len(t, toolResultIDs, 1)
	assert.Regexp(t, bedrockToolUseIDPattern, toolUseIDs[0], "Bedrock rejects toolUse.toolUseId over 64 chars")
	assert.Regexp(t, bedrockToolUseIDPattern, toolResultIDs[0], "Bedrock rejects toolResult.toolUseId over 64 chars")
	assert.Equal(t, toolUseIDs[0], toolResultIDs[0], "tool_use and tool_result must alias to the same id")
}

// TestBedrockToolUseIDPairingChatPath is TestBedrockToolUseIDPairingResponsesPath for the
// chat completions path, which builds toolUse/toolResult blocks through a separate converter.
func TestBedrockToolUseIDPairingChatPath(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "zai.glm-5",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What did we do so far?")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							ID:   schemas.Ptr(reportedGeminiToolUseID),
							Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"ls"}`,
							},
						},
					},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(reportedGeminiToolUseID)},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("build/ src/")},
			},
		},
	}

	bedrockReq, err := ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	var toolUseID, toolResultID string
	for _, msg := range bedrockReq.Messages {
		for _, block := range msg.Content {
			if block.ToolUse != nil {
				toolUseID = block.ToolUse.ToolUseID
			}
			if block.ToolResult != nil {
				toolResultID = block.ToolResult.ToolUseID
			}
		}
	}

	assert.Regexp(t, bedrockToolUseIDPattern, toolUseID, "Bedrock rejects toolUse.toolUseId over 64 chars")
	assert.Regexp(t, bedrockToolUseIDPattern, toolResultID, "Bedrock rejects toolResult.toolUseId over 64 chars")
	assert.Equal(t, toolUseID, toolResultID, "tool_use and tool_result must alias to the same id")
}

// TestBedrockAliasToolUseIDFullHashAvoidsCollision uses a real pair of 68-char ids found by
// brute-force search: their uint32(xxhash.Sum64String(...)) values collide (the bug in the
// previous 32-bit-truncated hash), but the full 64-bit hashes don't. If bedrockAliasToolUseID
// ever regresses to hashing only a uint32-truncated slice, this reproduces a real duplicate
// toolUseId — which Bedrock rejects outright, or worse, silently misattributes a tool_result.
func TestBedrockAliasToolUseIDFullHashAvoidsCollision(t *testing.T) {
	idA := "d556q31u_ts_AY89a18eH3FucoBHPCdX6w7jgIhjSnIj7hU_mGofiw3SE2HL8_n38390"
	idB := "d556q31u_ts_AY89a18eH3FucoBHPCdX6w7jgIhjSnIj7hU_mGofiw3SE2HL8_n63004"
	require.Greater(t, len(idA), 64)
	require.Greater(t, len(idB), 64)

	// Document the collision this pair exercises: same 32-bit-truncated hash, different ids.
	require.Equal(t, uint32(xxhash.Sum64String(idA)), uint32(xxhash.Sum64String(idB)),
		"fixture no longer demonstrates a 32-bit hash collision")
	require.NotEqual(t, idA, idB)

	aliasA := bedrockAliasToolUseID(idA)
	aliasB := bedrockAliasToolUseID(idB)
	assert.NotEqual(t, aliasA, aliasB, "ids with a colliding 32-bit hash must still alias apart")
	assert.Regexp(t, bedrockToolUseIDPattern, aliasA)
	assert.Regexp(t, bedrockToolUseIDPattern, aliasB)
}
