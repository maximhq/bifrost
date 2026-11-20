//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"fmt"
	"slices"
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

	// Purpose and trigger first, then what comes back and the next step. The catalogue of the
	// caller's code mode servers is appended per request (see codeModeCatalogue in core/mcp).
	const trigger = "Those tools are not in your tool list, so call this first whenever a request may need an external system or service you have no direct tool for, " +
		"and before telling the user something is unavailable. "
	if bindingLevel == schemas.CodeModeBindingLevelServer {
		description = "Code mode, step 1 of 4. Lists the connected MCP servers whose tools are reachable only through code mode. " + trigger +
			"Returns one file per server: servers/<server>.pyi. Next: readToolFile on the server you need."
	} else {
		description = "Code mode, step 1 of 4. Lists the tools of the connected MCP servers that are reachable only through code mode. " + trigger +
			"Returns one file per tool: servers/<server>/<tool>.pyi, where <tool> is the exact name to call. Next: readToolFile on the tool you need."
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
	availableToolsPerClient := s.clientManager.GetToolPerClient(codemcp.WithModelToolsOnly(ctx))

	if len(availableToolsPerClient) == 0 {
		responseText := "No servers are currently connected. There are no virtual .pyi files available. " +
			"Please ensure servers are connected before using this tool."
		return createToolResponseMessage(toolCall, responseText, false), nil
	}

	// Get the code mode binding level
	bindingLevel := s.GetBindingLevel()

	// Build file list based on binding level
	var files []string
	var serversWithInstructions []string
	codeModeServerCount := 0

	for clientName, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil {
			s.logger.Warn("%s Client %s not found, skipping", codemcp.CodeModeLogPrefix, clientName)
			continue
		}
		if !client.ExecutionConfig.CodeModeFor(ctx) {
			continue
		}
		codeModeServerCount++
		if s.clientManager.GetInstructionsForClient(ctx, client.ExecutionConfig.ID) != "" {
			serversWithInstructions = append(serversWithInstructions, clientName)
		}

		if bindingLevel == schemas.CodeModeBindingLevelServer {
			// Server-level: one file per server
			files = append(files, fmt.Sprintf("servers/%s.pyi", clientName))
		} else {
			// Tool-level: one file per tool
			for _, tool := range tools {
				if tool.Function != nil && tool.Function.Name != "" {
					toolName := getCanonicalToolName(clientName, tool.Function.Name)
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
		return createToolResponseMessage(toolCall, responseText, false), nil
	}

	// Build tree structure from file list
	slices.Sort(serversWithInstructions)
	responseText := buildListToolFilesResponse(files, bindingLevel, serversWithInstructions)
	return createToolResponseMessage(toolCall, responseText, false), nil
}

func buildListToolFilesResponse(files []string, bindingLevel schemas.CodeModeBindingLevel, serversWithInstructions []string) string {
	tree := buildVFSTree(files)
	if tree == "" {
		return ""
	}

	header := []string{
		"# Workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode",
	}

	if bindingLevel == schemas.CodeModeBindingLevelServer {
		header = append(header, "# Read the server .pyi file before executeToolCode to confirm exact tool names and parameters.")
	} else {
		header = append(header,
			"# Filenames below use the exact canonical tool identifiers available in executeToolCode.",
			"# Still call readToolFile before executeToolCode to confirm parameters and return shape.",
		)
	}

	// A pointer, not the prose: this listing is an index, so the text stays in the .pyi stub.
	if len(serversWithInstructions) > 0 {
		header = append(header, fmt.Sprintf("# Servers with their own usage instructions: %s. Read their .pyi before calling their tools.",
			strings.Join(serversWithInstructions, ", ")))
	}

	return strings.Join(append(header, "", tree), "\n")
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
