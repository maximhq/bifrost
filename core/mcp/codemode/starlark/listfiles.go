//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"fmt"
	"strings"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// createListToolFilesTool creates the listToolFiles tool definition for code mode.
// This tool allows listing all available virtual .pyi stub files for connected MCP servers.
// The description is dynamically generated based on the configured CodeModeBindingLevel.
func (s *StarlarkCodeMode) createListToolFilesTool() schemas.ChatTool {
	bindingLevel := s.GetBindingLevel()
	var description string

	if bindingLevel == schemas.CodeModeBindingLevelServer {
		description = "Returns a tree structure listing all virtual .pyi stub files available for connected MCP servers. " +
			"Each server has a corresponding file (e.g., servers/<serverName>.pyi) that contains compact Python signatures for all tools in that server. " +
			"Use readToolFile to read a specific server file and see all available tools with their signatures. " +
			"Use getToolDocs if you need detailed documentation for a specific tool. " +
			"In code, access tools via: server_name.tool_name(param=value). " +
			"The server names used in code correspond to the human-readable names shown in this listing. " +
			"This tool is generic and works with any set of servers connected at runtime. " +
			"Always check this tool whenever you are unsure about what tools you have available or if you want to verify available servers and their tools."
	} else {
		description = "Returns a tree structure listing all virtual .pyi stub files available for connected MCP servers, organized by individual tool. " +
			"Each tool has a corresponding file (e.g., servers/<serverName>/<toolName>.pyi) that contains compact Python signatures for that specific tool. " +
			"Use readToolFile to read a specific tool file and see its signature. " +
			"Use getToolDocs if you need detailed documentation for a specific tool. " +
			"In code, access tools via: server_name.tool_name(param=value). " +
			"The server names used in code correspond to the human-readable names shown in this listing. " +
			"This tool is generic and works with any set of servers connected at runtime. " +
			"Always check this tool whenever you are unsure about what tools you have available or if you want to verify available servers and their tools."
	}

	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        codemcp.ToolTypeListToolFiles,
			Description: schemas.Ptr(description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: schemas.NewOrderedMap(),
				Required:   []string{},
			},
		},
	}
}

// handleListToolFiles handles the listToolFiles tool call.
// It builds a tree structure listing all virtual .pyi files available for code mode clients.
func (s *StarlarkCodeMode) handleListToolFiles(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	availableToolsPerClient := s.clientManager.GetToolPerClient(ctx)

	if len(availableToolsPerClient) == 0 {
		responseText := "No servers are currently connected. There are no virtual .pyi files available. " +
			"Please ensure servers are connected before using this tool."
		return createToolResponseMessage(toolCall, responseText), nil
	}

	// Get the code mode binding level
	bindingLevel := s.GetBindingLevel()

	// Build file list based on binding level
	var files []string
	codeModeServerCount := 0

	for clientName, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil {
			s.logger.Warn("%s Client %s not found, skipping", codemcp.CodeModeLogPrefix, clientName)
			continue
		}
		if !client.ExecutionConfig.IsCodeModeClient {
			continue
		}
		codeModeServerCount++

		if bindingLevel == schemas.CodeModeBindingLevelServer {
			// Server-level: one file per server
			files = append(files, fmt.Sprintf("servers/%s.pyi", clientName))
		} else {
			// Tool-level: one file per tool
			for _, tool := range tools {
				if tool.Function != nil && tool.Function.Name != "" {
					// Strip the client prefix from tool name (format: "client-toolname" -> "toolname")
					// But replace - with _ for valid Python identifiers
					toolName := stripClientPrefix(tool.Function.Name, clientName)
					// Replace any remaining hyphens with underscores for Python compatibility
					toolName = strings.ReplaceAll(toolName, "-", "_")
					// Validate normalized tool name to prevent path traversal
					if err := validateNormalizedToolName(toolName); err != nil {
						s.logger.Warn("%s Skipping tool '%s' from client '%s': %v", codemcp.CodeModeLogPrefix, tool.Function.Name, clientName, err)
						continue
					}
					toolFileName := fmt.Sprintf("servers/%s/%s.pyi", clientName, toolName)
					files = append(files, toolFileName)
				}
			}
		}
	}

	if codeModeServerCount == 0 {
		responseText := "Servers are connected but none are configured for code mode. " +
			"There are no virtual .pyi files available."
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
