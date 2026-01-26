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

// createGetToolDocsTool creates the getToolDocs tool definition for code mode.
// This tool provides detailed documentation for a specific tool when the compact
// signatures from readToolFile are not sufficient to understand how to use it.
func (s *StarlarkCodeMode) createGetToolDocsTool() schemas.ChatTool {
	getToolDocsProps := schemas.OrderedMap{
		"server": map[string]interface{}{
			"type":        "string",
			"description": "The server name (e.g., 'calculator'). Use listToolFiles to see available servers.",
		},
		"tool": map[string]interface{}{
			"type":        "string",
			"description": "The tool name (e.g., 'add'). Use readToolFile to see available tools for a server.",
		},
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: codemcp.ToolTypeGetToolDocs,
			Description: schemas.Ptr(
				"Get detailed documentation for a specific tool including full parameter descriptions, " +
					"types, and usage examples. Use this when the compact signature from readToolFile " +
					"is not sufficient to understand how to use a tool. " +
					"Requires both server name and tool name as parameters.",
			),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &getToolDocsProps,
				Required:   []string{"server", "tool"},
			},
		},
	}
}

// handleGetToolDocs handles the getToolDocs tool call.
func (s *StarlarkCodeMode) handleGetToolDocs(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	// Parse tool arguments
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	serverName, ok := arguments["server"].(string)
	if !ok || serverName == "" {
		return nil, fmt.Errorf("server parameter is required and must be a string")
	}

	toolName, ok := arguments["tool"].(string)
	if !ok || toolName == "" {
		return nil, fmt.Errorf("tool parameter is required and must be a string")
	}

	// Get available tools per client
	availableToolsPerClient := s.clientManager.GetToolPerClient(ctx)

	// Find matching client
	var matchedClientName string
	var matchedTool *schemas.ChatTool

	serverNameLower := strings.ToLower(serverName)
	toolNameLower := strings.ToLower(toolName)

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
		if clientNameLower == serverNameLower {
			matchedClientName = clientName

			// Find the specific tool
			for i, tool := range tools {
				if tool.Function != nil {
					// Strip client prefix and replace - with _ for comparison
					unprefixedToolName := stripClientPrefix(tool.Function.Name, clientName)
					unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
					if strings.ToLower(unprefixedToolName) == toolNameLower {
						matchedTool = &tools[i]
						break
					}
				}
			}
			break
		}
	}

	// Handle server not found
	if matchedClientName == "" {
		var availableServers []string
		for name := range availableToolsPerClient {
			client := s.clientManager.GetClientByName(name)
			if client != nil && client.ExecutionConfig.IsCodeModeClient {
				availableServers = append(availableServers, name)
			}
		}
		errorMsg := fmt.Sprintf("Server '%s' not found. Available servers are:\n", serverName)
		for _, sn := range availableServers {
			errorMsg += fmt.Sprintf("  - %s\n", sn)
		}
		return createToolResponseMessage(toolCall, errorMsg), nil
	}

	// Handle tool not found
	if matchedTool == nil {
		tools := availableToolsPerClient[matchedClientName]
		var availableTools []string
		for _, tool := range tools {
			if tool.Function != nil {
				unprefixedToolName := stripClientPrefix(tool.Function.Name, matchedClientName)
				unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
				availableTools = append(availableTools, unprefixedToolName)
			}
		}
		errorMsg := fmt.Sprintf("Tool '%s' not found in server '%s'. Available tools are:\n", toolName, matchedClientName)
		for _, t := range availableTools {
			errorMsg += fmt.Sprintf("  - %s\n", t)
		}
		return createToolResponseMessage(toolCall, errorMsg), nil
	}

	// Generate detailed documentation using generateTypeDefinitions
	docContent := generateTypeDefinitions(matchedClientName, []schemas.ChatTool{*matchedTool}, true)

	return createToolResponseMessage(toolCall, docContent), nil
}

// generateTypeDefinitions generates Python documentation with docstrings from ChatTool schemas.
func generateTypeDefinitions(clientName string, tools []schemas.ChatTool, isToolLevel bool) string {
	var sb strings.Builder

	// Write comprehensive header
	sb.WriteString("# ============================================================================\n")
	if isToolLevel && len(tools) == 1 && tools[0].Function != nil {
		sb.WriteString(fmt.Sprintf("# Documentation for %s.%s tool\n", clientName, tools[0].Function.Name))
	} else {
		sb.WriteString(fmt.Sprintf("# Documentation for %s MCP server\n", clientName))
	}
	sb.WriteString("# ============================================================================\n")
	sb.WriteString("#\n")
	if isToolLevel && len(tools) == 1 {
		sb.WriteString("# This file contains Python documentation for a specific tool on this MCP server.\n")
	} else {
		sb.WriteString("# This file contains Python documentation for all tools available on this MCP server.\n")
	}
	sb.WriteString("#\n")
	sb.WriteString("# USAGE INSTRUCTIONS:\n")
	sb.WriteString(fmt.Sprintf("# Call tools using: result = %s.tool_name(param=value)\n", clientName))
	sb.WriteString("# No async/await needed - calls are synchronous.\n")
	sb.WriteString("#\n")
	sb.WriteString("# CRITICAL - HANDLING RESPONSES:\n")
	sb.WriteString("# Tool responses are dicts. To avoid runtime errors:\n")
	sb.WriteString("# 1. Use print(result) to inspect the response structure first\n")
	sb.WriteString("# 2. Access dict values with brackets: result[\"key\"] NOT result.key\n")
	sb.WriteString("# 3. Use .get() for safe access: result.get(\"key\", default)\n")
	sb.WriteString("#\n")
	sb.WriteString("# Common error: \"key not found\" or \"has no attribute\"\n")
	sb.WriteString("# Fix: Use print() to see actual structure, then use result[\"key\"] or .get()\n")
	sb.WriteString("# ============================================================================\n\n")

	// Generate function definitions for each tool
	for _, tool := range tools {
		if tool.Function == nil || tool.Function.Name == "" {
			continue
		}

		originalToolName := tool.Function.Name
		unprefixedToolName := stripClientPrefix(originalToolName, clientName)
		unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
		toolName := parseToolName(unprefixedToolName)
		description := ""
		if tool.Function.Description != nil {
			description = *tool.Function.Description
		}

		// Generate function signature
		params := formatPythonParams(tool.Function.Parameters)
		sb.WriteString(fmt.Sprintf("def %s(%s) -> dict:\n", toolName, params))

		// Generate docstring
		sb.WriteString("    \"\"\"\n")
		if description != "" {
			sb.WriteString(fmt.Sprintf("    %s\n", description))
			sb.WriteString("\n")
		}

		// Args section
		if tool.Function.Parameters != nil && tool.Function.Parameters.Properties != nil {
			props := *tool.Function.Parameters.Properties
			required := make(map[string]bool)
			if tool.Function.Parameters.Required != nil {
				for _, req := range tool.Function.Parameters.Required {
					required[req] = true
				}
			}

			if len(props) > 0 {
				sb.WriteString("    Args:\n")

				// Sort properties for consistent output
				propNames := make([]string, 0, len(props))
				for name := range props {
					propNames = append(propNames, name)
				}
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

					pyType := jsonSchemaToPython(propMap)
					propDesc := ""
					if desc, ok := propMap["description"].(string); ok && desc != "" {
						propDesc = desc
					} else {
						propDesc = fmt.Sprintf("%s parameter", propName)
					}

					requiredNote := ""
					if required[propName] {
						requiredNote = " (required)"
					} else {
						requiredNote = " (optional)"
					}

					sb.WriteString(fmt.Sprintf("        %s (%s): %s%s\n", propName, pyType, propDesc, requiredNote))
				}
				sb.WriteString("\n")
			}
		}

		// Returns section
		sb.WriteString("    Returns:\n")
		sb.WriteString("        dict: Response from the tool. Structure varies by tool.\n")
		sb.WriteString("              Use print(result) to inspect the actual structure.\n")
		sb.WriteString("\n")

		// Example section
		sb.WriteString("    Example:\n")
		sb.WriteString(fmt.Sprintf("        result = %s.%s(%s)\n", clientName, toolName, getExampleParams(tool.Function.Parameters)))
		sb.WriteString("        print(result)  # Always inspect response first!\n")
		sb.WriteString("        value = result.get(\"key\", default)  # Safe access\n")
		sb.WriteString("    \"\"\"\n")
		sb.WriteString("    ...\n\n")
	}

	return sb.String()
}

// getExampleParams generates example parameter usage for a function.
func getExampleParams(params *schemas.ToolFunctionParameters) string {
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

	// Get first required param as example
	for name := range props {
		if required[name] {
			return fmt.Sprintf("%s=\"...\"", name)
		}
	}

	// If no required, get first param
	for name := range props {
		return fmt.Sprintf("%s=\"...\"", name)
	}

	return ""
}
