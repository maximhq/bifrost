package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// createListToolFilesTool creates the listToolFiles tool definition
func (h *ToolsHandler) createListToolFilesTool() schemas.ChatTool {
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
					"This tool is generic and works with any set of servers connected at runtime.",
			),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &map[string]interface{}{},
				Required:   []string{},
			},
		},
	}
}

// handleListToolFiles handles the listToolFiles tool call
func (h *ToolsHandler) handleListToolFiles(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	availableToolsPerClient := h.clientManager.GetToolPerClient(ctx)

	if len(availableToolsPerClient) == 0 {
		responseText := "No servers are currently connected. There are no virtual .d.ts files available. " +
			"Please ensure servers are connected before using this tool."
		return createToolResponseMessage(toolCall, responseText), nil
	}

	// Build tree structure
	treeLines := []string{"servers/"}
	for clientName := range availableToolsPerClient {
		client := h.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn(fmt.Sprintf("%s Client %s not found, skipping", MCPLogPrefix, clientName))
			continue
		}
		if !client.ExecutionConfig.IsCodeModeClient {
			continue
		}
		treeLines = append(treeLines, fmt.Sprintf("  %s.d.ts", clientName))
	}

	responseText := strings.Join(treeLines, "\n")
	return createToolResponseMessage(toolCall, responseText), nil
}
