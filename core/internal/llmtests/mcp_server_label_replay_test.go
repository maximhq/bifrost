package llmtests

import (
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestMCPServerLabelSurvivesHistoryReplay reproduces, against a real OpenAI
// backend, the exact failure this test guards against: OpenAI's hosted
// "mcp" tool produces mcp_list_tools/mcp_call output items carrying a
// required server_label field. When that history is replayed on a
// subsequent turn, Bifrost must preserve server_label through its
// unmarshal-then-remarshal cycle in ResponsesMessage, or OpenAI rejects the
// follow-up request with "Missing required parameter: 'input[N].server_label'".
//
// Requires OPENAI_API_KEY (a Platform API key, not a ChatGPT-session token —
// hosted "mcp" tools are a Platform-API-only feature). Skipped otherwise.
// Uses a public, free MCP server (mcp.deepwiki.com) as the tool target and
// gpt-4o-mini to keep cost minimal.
func TestMCPServerLabelSurvivesHistoryReplay(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set; skipping live mcp_call/mcp_list_tools replay test")
	}

	client, ctx, cancel, err := SetupTest()
	require.NoError(t, err)
	defer cancel()
	defer client.Shutdown()

	const serverLabel = "deepwiki"
	tools := []schemas.ResponsesTool{
		{
			Type: schemas.ResponsesToolTypeMCP,
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel: serverLabel,
				ServerURL:   bifrost.Ptr("https://mcp.deepwiki.com/mcp"),
				RequireApproval: &schemas.ResponsesToolMCPAllowedToolsApprovalSetting{
					Setting: bifrost.Ptr("never"),
				},
			},
		},
	}

	turn1Prompt := "Use the deepwiki mcp server's ask_question tool to ask about the " +
		"facebook/react repository: what is this repo about? Actually call the tool, " +
		"don't just describe it."

	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	turn1Req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    []schemas.ResponsesMessage{CreateBasicResponsesMessage(turn1Prompt)},
		Params: &schemas.ResponsesParameters{
			Tools: tools,
			Store: bifrost.Ptr(false),
		},
	}

	turn1Resp, bfErr := client.ResponsesRequest(bfCtx, turn1Req)
	require.Nil(t, bfErr, "turn 1 (real mcp tool call) must succeed: %+v", bfErr)
	require.NotNil(t, turn1Resp)

	var sawListTools, sawCall bool
	for _, item := range turn1Resp.Output {
		if item.Type == nil {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeMCPListTools:
			sawListTools = true
			require.NotNil(t, item.ResponsesToolMessage)
			require.NotNil(t, item.ResponsesToolMessage.ResponsesMCPListTools)
			require.Equal(t, serverLabel, item.ResponsesToolMessage.ResponsesMCPListTools.ServerLabel,
				"mcp_list_tools item must carry server_label immediately on the first hop")
		case schemas.ResponsesMessageTypeMCPCall:
			sawCall = true
			require.NotNil(t, item.ResponsesToolMessage)
			require.NotNil(t, item.ResponsesToolMessage.ResponsesMCPToolCall)
			require.Equal(t, serverLabel, item.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel,
				"mcp_call item must carry server_label immediately on the first hop")
		}
	}
	require.True(t, sawListTools, "expected an mcp_list_tools item in turn 1 output")
	require.True(t, sawCall, "expected an mcp_call item in turn 1 output — model didn't call the tool")

	// Replay turn 1's full output back as history, exactly as a real client
	// would, plus a new user message. This is the scenario that 400s on an
	// unfixed build because server_label gets dropped during Bifrost's
	// internal unmarshal/remarshal of the mcp_list_tools/mcp_call items.
	history := append([]schemas.ResponsesMessage{CreateBasicResponsesMessage(turn1Prompt)}, turn1Resp.Output...)
	history = append(history, CreateBasicResponsesMessage("Thanks. Now, based on that, summarize in one sentence."))

	turn2Req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    history,
		Params: &schemas.ResponsesParameters{
			Tools: tools,
			Store: bifrost.Ptr(false),
		},
	}

	turn2Resp, bfErr := client.ResponsesRequest(bfCtx, turn2Req)
	if bfErr != nil {
		msg := ""
		if bfErr.Error != nil {
			msg = bfErr.Error.Message
		}
		if strings.Contains(msg, "server_label") {
			t.Fatalf("server_label was dropped on history replay, OpenAI rejected the request: %s", msg)
		}
		t.Fatalf("turn 2 (history replay) failed unexpectedly: %+v", bfErr)
	}
	require.NotNil(t, turn2Resp)
}
