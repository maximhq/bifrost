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
	getToolDocsProps := schemas.NewOrderedMapFromPairs(
		schemas.KV("server", map[string]interface{}{
			"type":        "string",
			"description": "The server name (e.g., 'calculator'). Use listToolFiles to see available servers.",
		}),
		schemas.KV("tool", map[string]interface{}{
			"type":        "string",
			"description": "The tool name (e.g., 'add'). Use readToolFile to see available tools for a server.",
		}),
	)
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
				Properties: getToolDocsProps,
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
	for clientName, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil {
			s.logger.Warn("%s Client %s not found, skipping", codemcp.CodeModeLogPrefix, clientName)
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
					if matchesToolReference(toolName, clientName, tool.Function.Name) {
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
		return createToolResponseMessage(toolCall, errorMsg, true), nil
	}

	// Handle tool not found
	if matchedTool == nil {
		tools := availableToolsPerClient[matchedClientName]
		var availableTools []string
		for _, tool := range tools {
			if tool.Function != nil {
				availableTools = append(availableTools, getCanonicalToolName(matchedClientName, tool.Function.Name))
			}
		}
		errorMsg := fmt.Sprintf("Tool '%s' not found in server '%s'. Available tools are:\n", toolName, matchedClientName)
		for _, t := range availableTools {
			errorMsg += fmt.Sprintf("  - %s\n", t)
		}
		return createToolResponseMessage(toolCall, errorMsg, true), nil
	}

	// Generate detailed documentation using generateTypeDefinitions
	docContent := generateTypeDefinitions(matchedClientName, []schemas.ChatTool{*matchedTool}, true, s.serverInstructionsComment(matchedClientName), !s.jsonModuleShadowed(availableToolsPerClient))

	return createToolResponseMessage(toolCall, docContent, false), nil
}

// generateTypeDefinitions generates Python documentation with docstrings from ChatTool schemas.
// jsonModuleAvailable says whether the sandbox's json module is reachable, so the docs only point at
// json.decode when a script can call it.
func generateTypeDefinitions(clientName string, tools []schemas.ChatTool, isToolLevel bool, instructions string, jsonModuleAvailable bool) string {
	var sb strings.Builder

	// Write comprehensive header
	sb.WriteString("# ============================================================================\n")
	if isToolLevel && len(tools) == 1 && tools[0].Function != nil {
		sb.WriteString(fmt.Sprintf("# Documentation for %s.%s tool\n", clientName, getCanonicalToolName(clientName, tools[0].Function.Name)))
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
	sb.WriteString("# A tool returns the decoded JSON when the server sends JSON: a dict or list,\n")
	sb.WriteString("# or a number, bool or None. Any other text arrives as a str.\n")
	sb.WriteString("# 1. Check the shape, not the whole value: print(type(r)), and print(sorted(r.keys())) for a dict.\n")
	sb.WriteString("#    Printing a whole response puts all of it into your context.\n")
	sb.WriteString("# 2. dict: r[\"key\"] or r.get(\"key\", default), never r.key. list: r[0].\n")
	if jsonModuleAvailable {
		sb.WriteString("# 3. str holding JSON, often a field: data = json.decode(r[\"body\"]); plain text raises.\n")
	}
	sb.WriteString("#\n")
	sb.WriteString("# FAILED CALLS:\n")
	sb.WriteString("# A call that fails returns an error value, not a result: hasattr(r, \"error\") is True and\n")
	sb.WriteString("# r.error holds the message. Call fail(r.error) to stop the run, or skip it and carry on.\n")
	sb.WriteString("#\n")
	sb.WriteString("# Common errors: key \"x\" not in dict, dict has no .x field or method, string index\n")
	sb.WriteString("# Fix: check type(r) first, then index the shape you actually got\n")
	if instructions != "" {
		sb.WriteString("#\n")
		sb.WriteString(instructions)
	}
	sb.WriteString("# ============================================================================\n\n")

	// Generate function definitions for each tool
	for _, tool := range tools {
		if tool.Function == nil || tool.Function.Name == "" {
			continue
		}

		originalToolName := tool.Function.Name
		toolName := getCanonicalToolName(clientName, originalToolName)
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
			props := tool.Function.Parameters.Properties
			required := make(map[string]bool)
			if tool.Function.Parameters.Required != nil {
				for _, req := range tool.Function.Parameters.Required {
					required[req] = true
				}
			}

			if props.Len() > 0 {
				sb.WriteString("    Args:\n")

				// Sort properties for consistent output
				propNames := make([]string, 0, props.Len())
				props.Range(func(name string, _ interface{}) bool {
					propNames = append(propNames, name)
					return true
				})
				for i := 0; i < len(propNames)-1; i++ {
					for j := i + 1; j < len(propNames); j++ {
						if propNames[i] > propNames[j] {
							propNames[i], propNames[j] = propNames[j], propNames[i]
						}
					}
				}

				for _, propName := range propNames {
					prop, _ := props.Get(propName)
					propSchema, ok := asJSONSchema(prop)
					if !ok {
						continue
					}

					pyType := jsonSchemaToPython(propSchema)
					propDesc := ""
					descVal, _ := propSchema.Get("description")
					if desc, ok := descVal.(string); ok && desc != "" {
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
		sb.WriteString("        The decoded JSON (usually a dict or list; a number, bool or None for a\n")
		sb.WriteString("        bare value), or a str when the server's text is not JSON.\n")
		sb.WriteString("        Check type(result) before indexing it.\n")
		sb.WriteString("\n")

		// Example section
		sb.WriteString("    Example:\n")
		sb.WriteString(fmt.Sprintf("        result = %s.%s(%s)\n", clientName, toolName, getExampleParams(tool.Function.Parameters)))
		sb.WriteString("        print(type(result))  # check the shape before indexing it\n")
		sb.WriteString("        value = result.get(\"key\") if type(result) == \"dict\" else result\n")
		sb.WriteString("    \"\"\"\n")
		sb.WriteString("    ...\n\n")
	}

	return sb.String()
}

// getExampleParams generates example parameter usage for a function.
func getExampleParams(params *schemas.ToolFunctionParameters) string {
	if params == nil || params.Properties == nil || params.Properties.Len() == 0 {
		return ""
	}

	required := make(map[string]bool)
	if params.Required != nil {
		for _, req := range params.Required {
			required[req] = true
		}
	}

	keys := params.Properties.Keys()

	// Get first required param as example
	for _, name := range keys {
		if required[name] {
			return fmt.Sprintf("%s=\"...\"", name)
		}
	}

	// If no required, get first param
	if len(keys) > 0 {
		return fmt.Sprintf("%s=\"...\"", keys[0])
	}

	return ""
}
