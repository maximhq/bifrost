package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// createReadToolFileTool creates the readToolFile tool definition for code mode.
// This tool allows reading virtual .d.ts declaration files for specific MCP servers/tools,
// generating TypeScript type definitions from the server's tool schemas.
// The description is dynamically generated based on the configured CodeModeBindingLevel.
//
// Returns:
//   - schemas.ChatTool: The tool definition for reading tool files
func (m *ToolsManager) createReadToolFileTool() schemas.ChatTool {
	bindingLevel := m.GetCodeModeBindingLevel()

	var fileNameDescription, toolDescription string

	if bindingLevel == schemas.CodeModeBindingLevelServer {
		fileNameDescription = "The virtual filename from listToolFiles in format: servers/<serverName>.d.ts (e.g., 'calculator.d.ts')"
		toolDescription = "Reads a virtual .d.ts declaration file for a specific MCP server, generating TypeScript type definitions " +
			"for all tools available on that server. The fileName should be in format servers/<serverName>.d.ts as listed by listToolFiles. " +
			"The function performs case-insensitive matching and removes the .d.ts extension. " +
			"Optionally, you can specify startLine and endLine (1-based, inclusive) to read only a portion of the file. " +
			"IMPORTANT: Line numbers are 1-based, not 0-based. The first line is line 1, not line 0. " +
			"This generates TypeScript type definitions describing all tools in the server and their argument types, " +
			"enabling code-mode execution. Each tool can be accessed in code via: await serverName.toolName({ args }). " +
			"Always follow this workflow: first use listToolFiles to see available servers, then use readToolFile to understand " +
			"all available tool definitions for a server, and finally use executeToolCode to execute your code."
	} else {
		fileNameDescription = "The virtual filename from listToolFiles in format: servers/<serverName>/<toolName>.d.ts (e.g., 'calculator/add.d.ts')"
		toolDescription = "Reads a virtual .d.ts declaration file for a specific tool, generating TypeScript type definitions " +
			"for that individual tool. The fileName should be in format servers/<serverName>/<toolName>.d.ts as listed by listToolFiles. " +
			"The function performs case-insensitive matching and removes the .d.ts extension. " +
			"Optionally, you can specify startLine and endLine (1-based, inclusive) to read only a portion of the file. " +
			"IMPORTANT: Line numbers are 1-based, not 0-based. The first line is line 1, not line 0. " +
			"This generates TypeScript type definitions for a single tool, describing its parameters and usage, " +
			"enabling focused code-mode execution. The tool can be accessed in code via: await serverName.toolName({ args }). " +
			"Always follow this workflow: first use listToolFiles to see available tools, then use readToolFile to understand " +
			"a specific tool's definition, and finally use executeToolCode to execute your code."
	}

	readToolFileProps := schemas.OrderedMap{
		"fileName": map[string]interface{}{
			"type":        "string",
			"description": fileNameDescription,
		},
		"startLine": map[string]interface{}{
			"type":        "number",
			"description": "Optional 1-based starting line number for partial file read (inclusive). Note: Line numbers start at 1, not 0. The first line is line 1.",
		},
		"endLine": map[string]interface{}{
			"type":        "number",
			"description": "Optional 1-based ending line number for partial file read (inclusive)",
		},
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeReadToolFile,
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
// It reads a virtual .d.ts file for a specific MCP server/tool, generates TypeScript type definitions,
// and optionally returns a portion of the file based on line range parameters.
// Supports both server-level files (e.g., "calculator.d.ts") and tool-level files (e.g., "calculator/add.d.ts").
//
// Parameters:
//   - ctx: Context for accessing client tools
//   - toolCall: The tool call request containing fileName and optional startLine/endLine
//
// Returns:
//   - *schemas.ChatMessage: A tool response message containing the TypeScript definitions
//   - error: Any error that occurred during processing
func (m *ToolsManager) handleReadToolFile(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
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
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)

	// Find matching client
	var matchedClientName string
	var matchedTools []schemas.ChatTool
	matchCount := 0

	for clientName, tools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn("%s Client %s not found, skipping", MCPLogPrefix, clientName)
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
					if tool.Function != nil && strings.ToLower(tool.Function.Name) == toolNameLower {
						foundTool = &tools[i]
						break
					}
				}

				if foundTool == nil {
					availableTools := make([]string, 0)
					for _, tool := range tools {
						if tool.Function != nil {
							availableTools = append(availableTools, tool.Function.Name)
						}
					}
					errorMsg := fmt.Sprintf("Tool '%s' not found in server '%s'. Available tools in this server are:\n", toolName, clientName)
					for _, t := range availableTools {
						errorMsg += fmt.Sprintf("  - %s/%s.d.ts\n", clientName, t)
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
		bindingLevel := m.GetCodeModeBindingLevel()
		var availableFiles []string

		for name := range availableToolsPerClient {
			if bindingLevel == schemas.CodeModeBindingLevelServer {
				availableFiles = append(availableFiles, fmt.Sprintf("%s.d.ts", name))
			} else {
				client := m.clientManager.GetClientByName(name)
				if client != nil && client.ExecutionConfig.IsCodeModeClient {
					if tools, ok := availableToolsPerClient[name]; ok {
						for _, tool := range tools {
							if tool.Function != nil {
								availableFiles = append(availableFiles, fmt.Sprintf("%s/%s.d.ts", name, tool.Function.Name))
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

	// Generate TypeScript definitions
	fileContent := generateTypeDefinitions(matchedClientName, matchedTools, isToolLevel)
	lines := strings.Split(fileContent, "\n")
	totalLines := len(lines)

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

		// Validate line numbers
		if start < 1 || start > totalLines {
			errorMsg := fmt.Sprintf("Invalid startLine: %d. Must be between 1 and %d (total lines in file). Provided: startLine=%d, endLine=%v, totalLines=%d",
				start, totalLines, start, endLine, totalLines)
			return createToolResponseMessage(toolCall, errorMsg), nil
		}
		if end < 1 || end > totalLines {
			errorMsg := fmt.Sprintf("Invalid endLine: %d. Must be between 1 and %d (total lines in file). Provided: startLine=%d, endLine=%d, totalLines=%d",
				end, totalLines, start, end, totalLines)
			return createToolResponseMessage(toolCall, errorMsg), nil
		}
		if start > end {
			errorMsg := fmt.Sprintf("Invalid line range: startLine (%d) must be less than or equal to endLine (%d). Total lines in file: %d",
				start, end, totalLines)
			return createToolResponseMessage(toolCall, errorMsg), nil
		}

		// Slice lines (convert to 0-based indexing)
		selectedLines := lines[start-1 : end]
		fileContent = strings.Join(selectedLines, "\n")
	}

	return createToolResponseMessage(toolCall, fileContent), nil
}

// HELPER FUNCTIONS

// parseVFSFilePath parses a VFS file path and extracts the server name and optional tool name.
// For server-level paths (e.g., "calculator.d.ts"), returns (serverName="calculator", toolName="", isToolLevel=false)
// For tool-level paths (e.g., "calculator/add.d.ts"), returns (serverName="calculator", toolName="add", isToolLevel=true)
//
// Parameters:
//   - fileName: The virtual file path from listToolFiles
//
// Returns:
//   - serverName: The name of the MCP server
//   - toolName: The name of the tool (empty for server-level)
//   - isToolLevel: Whether this is a tool-level path
func parseVFSFilePath(fileName string) (serverName, toolName string, isToolLevel bool) {
	// Remove .d.ts extension
	basePath := strings.TrimSuffix(fileName, ".d.ts")

	// Remove "servers/" prefix if present
	basePath = strings.TrimPrefix(basePath, "servers/")

	// Check for path separator
	parts := strings.Split(basePath, "/")
	if len(parts) == 2 {
		// Tool-level: "serverName/toolName"
		return parts[0], parts[1], true
	}
	// Server-level: "serverName"
	return basePath, "", false
}

// generateTypeDefinitions generates TypeScript type definitions from ChatTool schemas
// with comprehensive comments to help LLMs understand how to use the tools.
// It creates interfaces for tool inputs and responses, along with function declarations.
//
// Parameters:
//   - clientName: Name of the MCP client/server
//   - tools: List of chat tools to generate definitions for
//   - isToolLevel: Whether this is a tool-level definition (single tool) or server-level (all tools)
//
// Returns:
//   - string: Complete TypeScript declaration file content
func generateTypeDefinitions(clientName string, tools []schemas.ChatTool, isToolLevel bool) string {
	var sb strings.Builder

	// Write comprehensive header comment
	sb.WriteString("// ============================================================================\n")
	if isToolLevel && len(tools) == 1 && tools[0].Function != nil {
		// Tool-level: show individual tool name
		sb.WriteString(fmt.Sprintf("// Type definitions for %s.%s tool\n", clientName, tools[0].Function.Name))
	} else {
		// Server-level: show all tools in server
		sb.WriteString(fmt.Sprintf("// Type definitions for %s MCP server\n", clientName))
	}
	sb.WriteString("// ============================================================================\n")
	sb.WriteString("//\n")
	if isToolLevel && len(tools) == 1 {
		sb.WriteString("// This file contains TypeScript type definitions for a specific tool on this MCP server.\n")
	} else {
		sb.WriteString("// This file contains TypeScript type definitions for all tools available on this MCP server.\n")
	}
	sb.WriteString("// These definitions enable code-mode execution as described in the MCP code execution pattern.\n")
	sb.WriteString("//\n")
	sb.WriteString("// USAGE INSTRUCTIONS:\n")
	sb.WriteString("// 1. Each tool has an input interface (e.g., ToolNameInput) that defines the required parameters\n")
	sb.WriteString("// 2. Each tool has a function declaration showing how to call it\n")
	sb.WriteString("// 3. To use these tools in executeToolCode, you would call them like:\n")
	sb.WriteString("//    const result = await <serverName>.<toolName>({ ...args });\n")
	sb.WriteString("//\n")
	sb.WriteString("// NOTE: The server name used in executeToolCode is the same as the display name shown here.\n")
	sb.WriteString("// ============================================================================\n\n")

	// Generate interfaces and function declarations for each tool
	for _, tool := range tools {
		if tool.Function == nil || tool.Function.Name == "" {
			continue
		}

		originalToolName := tool.Function.Name
		// Parse tool name for property name compatibility (used in virtual TypeScript files)
		toolName := parseToolName(originalToolName)
		description := ""
		if tool.Function.Description != nil {
			description = *tool.Function.Description
		}

		// Generate input interface with detailed comments
		inputInterfaceName := toPascalCase(toolName) + "Input"
		sb.WriteString("// ----------------------------------------------------------------------------\n")
		sb.WriteString(fmt.Sprintf("// Tool: %s\n", toolName))
		sb.WriteString("// ----------------------------------------------------------------------------\n")
		if description != "" {
			sb.WriteString(fmt.Sprintf("// Description: %s\n", description))
		}
		sb.WriteString(fmt.Sprintf("// Input interface for %s\n", toolName))
		sb.WriteString(fmt.Sprintf("// This interface defines all parameters that can be passed to the %s tool.\n", toolName))
		sb.WriteString(fmt.Sprintf("interface %s {\n", inputInterfaceName))

		if tool.Function.Parameters != nil && tool.Function.Parameters.Properties != nil {
			props := *tool.Function.Parameters.Properties
			required := make(map[string]bool)
			if tool.Function.Parameters.Required != nil {
				for _, req := range tool.Function.Parameters.Required {
					required[req] = true
				}
			}

			// Sort properties for consistent output
			propNames := make([]string, 0, len(props))
			for name := range props {
				propNames = append(propNames, name)
			}
			// Simple alphabetical sort
			for i := 0; i < len(propNames)-1; i++ {
				for j := i + 1; j < len(propNames); j++ {
					if propNames[i] > propNames[j] {
						propNames[i], propNames[j] = propNames[j], propNames[i]
					}
				}
			}

			for _, propName := range propNames {
				prop := props[propName]
				propMap, ok := prop.(map[string]interface{})
				if !ok {
					continue
				}

				tsType := jsonSchemaToTypeScript(propMap)
				optional := ""
				if !required[propName] {
					optional = "?"
				}

				propDesc := ""
				if desc, ok := propMap["description"].(string); ok && desc != "" {
					propDesc = fmt.Sprintf(" // %s", desc)
				} else {
					propDesc = fmt.Sprintf(" // %s parameter", propName)
				}

				requiredNote := ""
				if required[propName] {
					requiredNote = " (required)"
				} else {
					requiredNote = " (optional)"
				}

				sb.WriteString(fmt.Sprintf("  %s%s: %s;%s%s\n", propName, optional, tsType, propDesc, requiredNote))
			}
		}

		sb.WriteString("}\n\n")

		// Generate response interface with helpful comments
		responseInterfaceName := toPascalCase(toolName) + "Response"
		sb.WriteString(fmt.Sprintf("// Response interface for %s\n", toolName))
		sb.WriteString("// The actual response structure depends on the tool implementation.\n")
		sb.WriteString("// This is a placeholder interface - the actual response may contain different fields.\n")
		sb.WriteString(fmt.Sprintf("interface %s {\n", responseInterfaceName))
		sb.WriteString("  // Response structure depends on the tool implementation\n")
		sb.WriteString("  // Common fields may include: result, error, data, etc.\n")
		sb.WriteString("  [key: string]: any;\n")
		sb.WriteString("}\n\n")

		// Generate function declaration with usage example
		sb.WriteString(fmt.Sprintf("// Function declaration for %s\n", toolName))
		if description != "" {
			sb.WriteString(fmt.Sprintf("// %s\n", description))
		}
		sb.WriteString("//\n")
		sb.WriteString("// Usage example in executeToolCode:\n")
		sb.WriteString(fmt.Sprintf("//   const result = await <serverName>.%s({ ... });\n", toolName))
		sb.WriteString("//   // Replace <serverName> with the actual server name/ID\n")
		sb.WriteString(fmt.Sprintf("//   // Replace { ... } with the appropriate %sInput object\n", inputInterfaceName))
		sb.WriteString(fmt.Sprintf("export async function %s(input: %s): Promise<%s>;\n\n", toolName, inputInterfaceName, responseInterfaceName))
	}

	return sb.String()
}

// jsonSchemaToTypeScript converts a JSON Schema type definition to a TypeScript type string.
// It handles basic types, arrays, enums, and defaults to "any" for unknown types.
//
// Parameters:
//   - prop: JSON Schema property definition map
//
// Returns:
//   - string: TypeScript type string representation
func jsonSchemaToTypeScript(prop map[string]interface{}) string {
	// Check for explicit type
	if typeVal, ok := prop["type"].(string); ok {
		switch typeVal {
		case "string":
			return "string"
		case "number", "integer":
			return "number"
		case "boolean":
			return "boolean"
		case "array":
			itemsType := "any"
			if items, ok := prop["items"].(map[string]interface{}); ok {
				itemsType = jsonSchemaToTypeScript(items)
			}
			return fmt.Sprintf("%s[]", itemsType)
		case "object":
			return "object"
		case "null":
			return "null"
		}
	}

	// Check for enum
	if enum, ok := prop["enum"].([]interface{}); ok && len(enum) > 0 {
		enumStrs := make([]string, 0, len(enum))
		for _, e := range enum {
			enumStrs = append(enumStrs, fmt.Sprintf("%q", e))
		}
		return strings.Join(enumStrs, " | ")
	}

	// Default to any
	return "any"
}

// toPascalCase converts a string to PascalCase format.
// It splits on underscores, hyphens, and spaces, then capitalizes the first letter
// of each word and lowercases the rest.
//
// Parameters:
//   - s: Input string to convert
//
// Returns:
//   - string: PascalCase formatted string
func toPascalCase(s string) string {
	if s == "" {
		return s
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
	result := ""
	for _, part := range parts {
		if len(part) > 0 {
			result += strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		}
	}
	if result == "" {
		return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
	}
	return result
}
