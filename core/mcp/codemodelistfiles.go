package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// createListToolFilesTool creates the listToolFiles tool definition for code mode.
// This tool allows listing all available virtual .d.ts declaration files for connected MCP servers.
// The description is dynamically generated based on the configured CodeModeBindingLevel.
//
// Returns:
//   - schemas.ChatTool: The tool definition for listing tool files
func (m *ToolsManager) createListToolFilesTool() schemas.ChatTool {
	bindingLevel := m.GetCodeModeBindingLevel()
	var description string

	if bindingLevel == schemas.CodeModeBindingLevelServer {
		description = "Returns a tree structure listing all virtual .d.ts declaration files available for connected MCP servers. " +
			"Each server has a corresponding file (e.g., servers/<serverName>.d.ts) that contains definitions for all tools in that server. " +
			"Use readToolFile to read a specific server file and see all available tools. " +
			"In code, access tools via: await serverName.toolName({ args }). " +
			"The server names used in code correspond to the human-readable names shown in this listing. " +
			"This tool is generic and works with any set of servers connected at runtime. " +
			"Always check this tool whenever you are unsure about what tools you have available or if you want to verify available servers and their tools. " +
			"If you have even the SLIGHTEST DOUBT that the current tools might not be useful for the task, check listToolFiles to discover all available tools."
	} else {
		description = "Returns a tree structure listing all virtual .d.ts declaration files available for connected MCP servers, organized by individual tool. " +
			"Each tool has a corresponding file (e.g., servers/<serverName>/<toolName>.d.ts) that contains definitions for that specific tool. " +
			"Use readToolFile to read a specific tool file and see its parameters and usage. " +
			"In code, access tools via: await serverName.toolName({ args }). " +
			"The server names used in code correspond to the human-readable names shown in this listing. " +
			"This tool is generic and works with any set of servers connected at runtime. " +
			"Always check this tool whenever you are unsure about what tools you have available or if you want to verify available servers and their tools. " +
			"If you have even the SLIGHTEST DOUBT that the current tools might not be useful for the task, check listToolFiles to discover all available tools."
	}

	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeListToolFiles,
			Description: schemas.Ptr(description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &schemas.OrderedMap{},
				Required:   []string{},
			},
		},
	}
}

// handleListToolFiles handles the listToolFiles tool call.
// It builds a tree structure listing all virtual .d.ts files available for code mode clients.
// The structure depends on the CodeModeBindingLevel:
// - "server": servers/<name>.d.ts (one file per server)
// - "tool": servers/<name>/<toolName>.d.ts (one file per tool)
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

	// Get the code mode binding level
	bindingLevel := m.GetCodeModeBindingLevel()

	// Build file list based on binding level
	var files []string
	codeModeServerCount := 0

	for clientName, tools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn("%s Client %s not found, skipping", MCPLogPrefix, clientName)
			continue
		}
		if !client.ExecutionConfig.IsCodeModeClient {
			continue
		}
		codeModeServerCount++

		if bindingLevel == schemas.CodeModeBindingLevelServer {
			// Server-level: one file per server
			files = append(files, fmt.Sprintf("servers/%s.d.ts", clientName))
		} else {
			// Tool-level: one file per tool
			for _, tool := range tools {
				if tool.Function != nil && tool.Function.Name != "" {
					toolFileName := fmt.Sprintf("servers/%s/%s.d.ts", clientName, tool.Function.Name)
					files = append(files, toolFileName)
				}
			}
		}
	}

	if codeModeServerCount == 0 {
		responseText := "Servers are connected but none are configured for code mode. " +
			"There are no virtual .d.ts files available."
		return createToolResponseMessage(toolCall, responseText), nil
	}

	// Build tree structure from file list
	responseText := buildVFSTree(files)
	return createToolResponseMessage(toolCall, responseText), nil
}

// VFS tree node structure for building hierarchical file structure
type treeNode struct {
	isDirectory bool
	children    map[string]*treeNode
}

// buildVFSTree creates a hierarchical tree structure from a flat list of file paths.
// It groups files by directory and formats them with proper indentation.
//
// Example input:
//   - ["servers/calculator.d.ts", "servers/youtube.d.ts"]
//   - ["servers/calculator/add.d.ts", "servers/youtube/GET_CHANNELS.d.ts"]
//
// Example output for server-level:
//   servers/
//     calculator.d.ts
//     youtube.d.ts
//
// Example output for tool-level:
//   servers/
//     calculator/
//       add.d.ts
//     youtube/
//       GET_CHANNELS.d.ts
func buildVFSTree(files []string) string {
	if len(files) == 0 {
		return ""
	}

	root := &treeNode{
		isDirectory: true,
		children:    make(map[string]*treeNode),
	}

	// Parse all files and build tree structure
	for _, file := range files {
		parts := strings.Split(file, "/")
		current := root

		// Create all intermediate directories and final file
		for i, part := range parts {
			if _, exists := current.children[part]; !exists {
				current.children[part] = &treeNode{
					isDirectory: i < len(parts)-1, // Last part is file, not directory
					children:    make(map[string]*treeNode),
				}
			}
			current = current.children[part]
		}
	}

	// Render tree structure with proper indentation
	var lines []string
	renderTreeNode(root, "", &lines, true)

	return strings.Join(lines, "\n")
}

// renderTreeNode recursively renders a tree node and its children with proper indentation.
func renderTreeNode(node *treeNode, indent string, lines *[]string, isRoot bool) {
	// Get sorted keys for consistent output
	var keys []string
	for key := range node.children {
		keys = append(keys, key)
	}

	// Simple bubble sort for small lists (good enough for this use case)
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	for _, key := range keys {
		child := node.children[key]

		// Format the line
		var line string
		if isRoot {
			// Root level - no indentation
			if child.isDirectory {
				line = key + "/"
			} else {
				line = key
			}
		} else {
			// Non-root levels - add indentation
			if child.isDirectory {
				line = indent + key + "/"
			} else {
				line = indent + key
			}
		}

		*lines = append(*lines, line)

		// Recurse into children
		if child.isDirectory && len(child.children) > 0 {
			var nextIndent string
			if isRoot {
				nextIndent = "  "
			} else {
				nextIndent = indent + "  "
			}
			renderTreeNode(child, nextIndent, lines, false)
		}
	}
}
