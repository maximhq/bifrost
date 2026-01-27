//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// createReadToolFileTool creates the readToolFile tool definition for code mode.
// This tool allows reading virtual .pyi stub files for specific MCP servers/tools,
// generating Python type stubs from the server's tool schemas.
func (s *StarlarkCodeMode) createReadToolFileTool() schemas.ChatTool {
	bindingLevel := s.GetBindingLevel()

	var fileNameDescription, toolDescription string

	if bindingLevel == schemas.CodeModeBindingLevelServer {
		fileNameDescription = "The virtual filename from listToolFiles in format: servers/<serverName>.pyi (e.g., 'calculator.pyi')"
		toolDescription = "Reads a virtual .pyi stub file for a specific MCP server, returning compact Python function signatures " +
			"for all tools available on that server. The fileName should be in format servers/<serverName>.pyi as listed by listToolFiles. " +
			"The function performs case-insensitive matching and removes the .pyi extension. " +
			"Each tool can be accessed in code via: serverName.tool_name(param=value). " +
			"If the compact signature is not enough to understand a tool, use getToolDocs for detailed documentation. " +
			"Workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode. " +
			"IMPORTANT: If the response header shows 'Total lines: X (this is the complete file)', " +
			"do NOT call this tool again with startLine/endLine - you already have the complete file."
	} else {
		fileNameDescription = "The virtual filename from listToolFiles in format: servers/<serverName>/<toolName>.pyi (e.g., 'calculator/add.pyi')"
		toolDescription = "Reads a virtual .pyi stub file for a specific tool, returning its compact Python function signature. " +
			"The fileName should be in format servers/<serverName>/<toolName>.pyi as listed by listToolFiles. " +
			"The function performs case-insensitive matching and removes the .pyi extension. " +
			"The tool can be accessed in code via: serverName.tool_name(param=value). " +
			"If the compact signature is not enough to understand the tool, use getToolDocs for detailed documentation. " +
			"Workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode. " +
			"IMPORTANT: If the response header shows 'Total lines: X (this is the complete file)', " +
			"do NOT call this tool again with startLine/endLine - you already have the complete file."
	}

	readToolFileProps := schemas.OrderedMap{
		"fileName": map[string]interface{}{
			"type":        "string",
			"description": fileNameDescription,
		},
		"startLine": map[string]interface{}{
			"type":        "number",
			"description": "Optional 1-based starting line number for partial file read. Usually not needed - omit to read the entire file. Files are typically small (under 50 lines).",
		},
		"endLine": map[string]interface{}{
			"type":        "number",
			"description": "Optional 1-based ending line number for partial file read. Usually not needed - omit to read the entire file. Will be clamped to actual file size if too large.",
		},
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        codemcp.ToolTypeReadToolFile,
			Description: schemas.Ptr(toolDescription),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &readToolFileProps,
				Required:   []string{"fileName"},
			},
		},
	}
}

// handleReadToolFile handles the readToolFile tool call.
func (s *StarlarkCodeMode) handleReadToolFile(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	// Parse tool arguments
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	fileName, ok := arguments["fileName"].(string)
	if !ok || fileName == "" {
		return nil, fmt.Errorf("fileName parameter is required and must be a string")
	}

	// Parse the file path to extract server name and optional tool name
	serverName, toolName, isToolLevel := parseVFSFilePath(fileName)

	// Get available tools per client
	availableToolsPerClient := s.clientManager.GetToolPerClient(ctx)

	// Find matching client
	var matchedClientName string
	var matchedTools []schemas.ChatTool
	matchCount := 0

	for clientName, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn("%s Client %s not found, skipping", codemcp.CodeModeLogPrefix, clientName)
			continue
		}
		if !client.ExecutionConfig.IsCodeModeClient || len(tools) == 0 {
			continue
		}

		clientNameLower := strings.ToLower(clientName)
		serverNameLower := strings.ToLower(serverName)

		if clientNameLower == serverNameLower {
			matchCount++
			if matchCount > 1 {
				// Multiple matches found
				errorMsg := fmt.Sprintf("Multiple servers match filename '%s':\n", fileName)
				for name := range availableToolsPerClient {
					if strings.ToLower(name) == serverNameLower {
						errorMsg += fmt.Sprintf("  - %s\n", name)
					}
				}
				errorMsg += "\nPlease use a more specific filename. Use the exact display name from listToolFiles to avoid ambiguity."
				return createToolResponseMessage(toolCall, errorMsg), nil
			}

			matchedClientName = clientName

			if isToolLevel {
				// Tool-level: filter to specific tool
				var foundTool *schemas.ChatTool
				toolNameLower := strings.ToLower(toolName)
				for i, tool := range tools {
					if tool.Function != nil {
						// Strip client prefix and replace - with _ for comparison
						unprefixedToolName := stripClientPrefix(tool.Function.Name, clientName)
						unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
						if strings.ToLower(unprefixedToolName) == toolNameLower {
							foundTool = &tools[i]
							break
						}
					}
				}

				if foundTool == nil {
					availableTools := make([]string, 0)
					for _, tool := range tools {
						if tool.Function != nil {
							// Strip client prefix and replace - with _ for display
							unprefixedToolName := stripClientPrefix(tool.Function.Name, clientName)
							unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
							availableTools = append(availableTools, unprefixedToolName)
						}
					}
					errorMsg := fmt.Sprintf("Tool '%s' not found in server '%s'. Available tools in this server are:\n", toolName, clientName)
					for _, t := range availableTools {
						errorMsg += fmt.Sprintf("  - %s/%s.pyi\n", clientName, t)
					}
					return createToolResponseMessage(toolCall, errorMsg), nil
				}

				matchedTools = []schemas.ChatTool{*foundTool}
			} else {
				// Server-level: use all tools
				matchedTools = tools
			}
		}
	}

	if matchedClientName == "" {
		// Build helpful error message with available files
		bindingLevel := s.GetBindingLevel()
		var availableFiles []string

		for name := range availableToolsPerClient {
			if bindingLevel == schemas.CodeModeBindingLevelServer {
				availableFiles = append(availableFiles, fmt.Sprintf("%s.pyi", name))
			} else {
				client := s.clientManager.GetClientByName(name)
				if client != nil && client.ExecutionConfig.IsCodeModeClient {
					if tools, ok := availableToolsPerClient[name]; ok {
						for _, tool := range tools {
							if tool.Function != nil {
								// Strip client prefix and replace - with _ for display
								unprefixedToolName := stripClientPrefix(tool.Function.Name, name)
								unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
								availableFiles = append(availableFiles, fmt.Sprintf("%s/%s.pyi", name, unprefixedToolName))
							}
						}
					}
				}
			}
		}

		errorMsg := fmt.Sprintf("No server found matching '%s'. Available virtual files are:\n", serverName)
		for _, f := range availableFiles {
			errorMsg += fmt.Sprintf("  - %s\n", f)
		}
		return createToolResponseMessage(toolCall, errorMsg), nil
	}

	// Generate compact Python signatures
	fileContent := generateCompactSignatures(matchedClientName, matchedTools, isToolLevel)
	lines := strings.Split(fileContent, "\n")
	totalLines := len(lines)

	// Prepend total lines info so LLM knows the file size upfront
	fileContent = fmt.Sprintf("# Total lines: %d (this is the complete file, no need to paginate)\n%s", totalLines+1, fileContent)
	// Recalculate lines after prepending
	lines = strings.Split(fileContent, "\n")
	totalLines = len(lines)

	// Handle line slicing if provided
	var startLine, endLine *int
	if sl, ok := arguments["startLine"].(float64); ok {
		slInt := int(sl)
		startLine = &slInt
	}
	if el, ok := arguments["endLine"].(float64); ok {
		elInt := int(el)
		endLine = &elInt
	}

	if startLine != nil || endLine != nil {
		start := 1
		if startLine != nil {
			start = *startLine
		}
		end := totalLines
		if endLine != nil {
			end = *endLine
		}

		// Clamp values to valid range instead of erroring
		// This handles cases where LLM requests more lines than exist
		if start < 1 {
			start = 1
		}
		if start > totalLines {
			start = totalLines
		}
		if end < 1 {
			end = 1
		}
		if end > totalLines {
			end = totalLines
		}
		if start > end {
			// If start > end after clamping, just return the start line
			end = start
		}

		// Slice lines (convert to 0-based indexing)
		selectedLines := lines[start-1 : end]
		fileContent = strings.Join(selectedLines, "\n")
	}

	return createToolResponseMessage(toolCall, fileContent), nil
}

// parseVFSFilePath parses a VFS file path and extracts the server name and optional tool name.
func parseVFSFilePath(fileName string) (serverName, toolName string, isToolLevel bool) {
	// Remove .pyi extension
	basePath := strings.TrimSuffix(fileName, ".pyi")

	// Remove "servers/" prefix if present
	basePath = strings.TrimPrefix(basePath, "servers/")

	// Defensive validation: reject paths with path traversal attempts
	if strings.Contains(basePath, "..") {
		// Return empty to indicate invalid path
		return "", "", false
	}

	// Check for path separator
	parts := strings.Split(basePath, "/")
	if len(parts) == 2 {
		// Tool-level: "serverName/toolName"
		// Validate that tool name doesn't contain additional path separators or traversal
		if parts[1] == "" || strings.Contains(parts[1], "/") || strings.Contains(parts[1], "..") {
			// Invalid tool name, treat as server-level
			return parts[0], "", false
		}
		return parts[0], parts[1], true
	}
	// Server-level: "serverName"
	// Validate server name doesn't contain path separators or traversal
	if strings.Contains(basePath, "/") || strings.Contains(basePath, "..") {
		// Invalid path
		return "", "", false
	}
	return basePath, "", false
}

// generateCompactSignatures generates compact Python function signatures for tools.
func generateCompactSignatures(clientName string, tools []schemas.ChatTool, isToolLevel bool) string {
	var sb strings.Builder

	// Minimal header
	if isToolLevel && len(tools) == 1 && tools[0].Function != nil {
		toolName := parseToolName(stripClientPrefix(tools[0].Function.Name, clientName))
		sb.WriteString(fmt.Sprintf("# %s.%s tool\n", clientName, toolName))
	} else {
		sb.WriteString(fmt.Sprintf("# %s server tools\n", clientName))
	}
	sb.WriteString(fmt.Sprintf("# Usage: %s.tool_name(param=value)\n", clientName))
	sb.WriteString(fmt.Sprintf("# For detailed docs: use getToolDocs(server=\"%s\", tool=\"tool_name\")\n", clientName))
	sb.WriteString("# Note: Descriptions may be truncated. Use getToolDocs for full details.\n\n")

	for _, tool := range tools {
		if tool.Function == nil || tool.Function.Name == "" {
			continue
		}

		// Strip client prefix and replace - with _ for code mode compatibility
		unprefixedToolName := stripClientPrefix(tool.Function.Name, clientName)
		unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
		toolName := parseToolName(unprefixedToolName)

		// Format inline parameters in Python style
		params := formatPythonParams(tool.Function.Parameters)

		// Get description (truncate if too long)
		desc := ""
		if tool.Function.Description != nil && *tool.Function.Description != "" {
			desc = *tool.Function.Description
			// Truncate long descriptions to first sentence or 80 chars
			if idx := strings.Index(desc, ". "); idx > 0 && idx < 80 {
				desc = desc[:idx+1]
			} else if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
		}

		// Write Python signature: def tool_name(param: type, param: type = None) -> dict:  # description
		if desc != "" {
			sb.WriteString(fmt.Sprintf("def %s(%s) -> dict:  # %s\n", toolName, params, desc))
		} else {
			sb.WriteString(fmt.Sprintf("def %s(%s) -> dict\n", toolName, params))
		}
	}

	return sb.String()
}

// formatPythonParams formats tool parameters as Python function parameters.
func formatPythonParams(params *schemas.ToolFunctionParameters) string {
	if params == nil || params.Properties == nil || len(*params.Properties) == 0 {
		return ""
	}

	props := *params.Properties
	required := make(map[string]bool)
	if params.Required != nil {
		for _, req := range params.Required {
			required[req] = true
		}
	}

	// Sort properties: required first, then optional, alphabetically within each group
	requiredNames := make([]string, 0)
	optionalNames := make([]string, 0)
	for name := range props {
		if required[name] {
			requiredNames = append(requiredNames, name)
		} else {
			optionalNames = append(optionalNames, name)
		}
	}
	// Simple alphabetical sort for each group
	for i := 0; i < len(requiredNames)-1; i++ {
		for j := i + 1; j < len(requiredNames); j++ {
			if requiredNames[i] > requiredNames[j] {
				requiredNames[i], requiredNames[j] = requiredNames[j], requiredNames[i]
			}
		}
	}
	for i := 0; i < len(optionalNames)-1; i++ {
		for j := i + 1; j < len(optionalNames); j++ {
			if optionalNames[i] > optionalNames[j] {
				optionalNames[i], optionalNames[j] = optionalNames[j], optionalNames[i]
			}
		}
	}

	parts := make([]string, 0, len(props))

	// Add required params first
	for _, propName := range requiredNames {
		prop := props[propName]
		propMap, ok := prop.(map[string]interface{})
		if !ok {
			continue
		}
		pyType := jsonSchemaToPython(propMap)
		parts = append(parts, fmt.Sprintf("%s: %s", propName, pyType))
	}

	// Add optional params with default None
	for _, propName := range optionalNames {
		prop := props[propName]
		propMap, ok := prop.(map[string]interface{})
		if !ok {
			continue
		}
		pyType := jsonSchemaToPython(propMap)
		parts = append(parts, fmt.Sprintf("%s: %s = None", propName, pyType))
	}

	return strings.Join(parts, ", ")
}

// jsonSchemaToPython converts a JSON Schema type definition to a Python type string.
func jsonSchemaToPython(prop map[string]interface{}) string {
	// Check for enum first - takes precedence over type to show allowed values
	if enum, ok := prop["enum"].([]interface{}); ok && len(enum) > 0 {
		enumStrs := make([]string, 0, len(enum))
		for _, e := range enum {
			enumStrs = append(enumStrs, fmt.Sprintf("%q", e))
		}
		return "Literal[" + strings.Join(enumStrs, ", ") + "]"
	}

	// Check for const (single fixed value)
	if constVal, ok := prop["const"]; ok {
		return fmt.Sprintf("Literal[%q]", constVal)
	}

	// Fall back to type-based conversion
	if typeVal, ok := prop["type"].(string); ok {
		switch typeVal {
		case "string":
			return "str"
		case "number":
			return "float"
		case "integer":
			return "int"
		case "boolean":
			return "bool"
		case "array":
			itemsType := "Any"
			if items, ok := prop["items"].(map[string]interface{}); ok {
				itemsType = jsonSchemaToPython(items)
			}
			return fmt.Sprintf("list[%s]", itemsType)
		case "object":
			return "dict"
		case "null":
			return "None"
		}
	}

	return "Any"
}
