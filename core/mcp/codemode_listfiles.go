package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// createListToolFilesTool creates the listToolFiles tool definition for code mode.
// This tool allows listing all available virtual .d.ts declaration files for connected MCP servers.
//
// Returns:
//   - schemas.ChatTool: The tool definition for listing tool files
func (m *ToolsManager) createListToolFilesTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeListToolFiles,
			Description: schemas.Ptr(
				"Returns a tree structure listing all virtual .d.ts declaration files available for connected MCP servers. " +
					"Each connected server has a corresponding virtual file that can be read using readToolFile. " +
					"The filenames follow the pattern <serverDisplayName>.d.ts where serverDisplayName is the human-readable " +
					"name reported by each connected server. Note that the code-level bindings (used in executeToolCode) use " +
					"configuration keys from SERVER_CONFIGS, which may differ from these display names. " +
					"This tool is generic and works with any set of servers connected at runtime. " +
					"Always check this tool whenever you are unsure about what tools you have available or if you want to verify available servers and their tools. " +
					"If you have even the SLIGHTEST DOUBT that the current tools might not be useful for the task, check listToolFiles to discover all available tools.",
			),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &map[string]interface{}{},
				Required:   []string{},
			},
		},
	}
}

// handleListToolFiles handles the listToolFiles tool call.
// It builds a tree structure listing all virtual .d.ts files available for code mode clients.
//
// Parameters:
//   - ctx: Context for accessing client tools
//   - toolCall: The tool call request containing no arguments
//
// Returns:
//   - *schemas.ChatMessage: A tool response message containing the file tree structure
//   - error: Any error that occurred during processing
func (m *ToolsManager) handleListToolFiles(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)

	if len(availableToolsPerClient) == 0 {
		responseText := "No servers are currently connected. There are no virtual .d.ts files available. " +
			"Please ensure servers are connected before using this tool."
		return createToolResponseMessage(toolCall, responseText), nil
	}

	// Build tree structure
	treeLines := []string{"servers/"}
	codeModeServerCount := 0
	for clientName := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn(fmt.Sprintf("%s Client %s not found, skipping", MCPLogPrefix, clientName))
			continue
		}
		if !client.ExecutionConfig.IsCodeModeClient {
			continue
		}
		codeModeServerCount++
		treeLines = append(treeLines, fmt.Sprintf("  %s.d.ts", clientName))
	}

	if codeModeServerCount == 0 {
		responseText := "Servers are connected but none are configured for code mode. " +
			"There are no virtual .d.ts files available."
		return createToolResponseMessage(toolCall, responseText), nil
	}

	responseText := strings.Join(treeLines, "\n")
	return createToolResponseMessage(toolCall, responseText), nil
}
