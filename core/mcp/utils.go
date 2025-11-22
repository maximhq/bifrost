package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// GetClientForTool safely finds a client that has the specified tool.
// Returns a copy of the client state to avoid data races. Callers should be aware
// that fields like Conn and ToolMap are still shared references and may be modified
// by other goroutines, but the struct itself is safe from concurrent modification.
func (m *MCPManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, client := range m.clientMap {
		if _, exists := client.ToolMap[toolName]; exists {
			// Return a copy to prevent TOCTOU race conditions
			// The caller receives a snapshot of the client state at this point in time
			clientCopy := *client
			return &clientCopy
		}
	}
	return nil
}

// GetToolPerClient returns all tools from connected MCP clients.
// Applies client filtering if specified in the context.
// Returns a map of client name to its available tools.
// Parameters:
//   - ctx: Execution context
//
// Returns:
//   - map[string][]schemas.ChatTool: Map of client name to its available tools
func (m *MCPManager) GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var includeClients []string

	// Extract client filtering from request context
	if existingIncludeClients, ok := ctx.Value(MCPContextKeyIncludeClients).([]string); ok && existingIncludeClients != nil {
		includeClients = existingIncludeClients
	}

	tools := make(map[string][]schemas.ChatTool)
	for _, client := range m.clientMap {
		// Use client name as the key (not ID)
		clientName := client.ExecutionConfig.Name

		// Apply client filtering logic
		if !shouldIncludeClient(clientName, includeClients) {
			logger.Debug(fmt.Sprintf("%s Skipping MCP client %s: not in include clients list", MCPLogPrefix, clientName))
			continue
		}

		logger.Debug(fmt.Sprintf("Checking tools for MCP client %s with tools to execute: %v", clientName, client.ExecutionConfig.ToolsToExecute))

		// Add all tools from this client
		for toolName, tool := range client.ToolMap {
			// Check if tool should be skipped based on client configuration
			if shouldSkipToolForConfig(toolName, client.ExecutionConfig) {
				logger.Debug(fmt.Sprintf("%s Skipping MCP tool %s: not in tools to execute list", MCPLogPrefix, toolName))
				continue
			}

			// Check if tool should be skipped based on request context
			if shouldSkipToolForRequest(ctx, clientName, toolName) {
				logger.Debug(fmt.Sprintf("%s Skipping MCP tool %s: not in include tools list", MCPLogPrefix, toolName))
				continue
			}

			tools[clientName] = append(tools[clientName], tool)
		}
		if len(tools[clientName]) > 0 {
			logger.Debug(fmt.Sprintf("%s Added %d tools for MCP client %s", MCPLogPrefix, len(tools[clientName]), clientName))
		}
	}
	return tools
}

// GetClientByName returns a client by name.
//
// Parameters:
//   - clientName: Name of the client to get
//
// Returns:
//   - *schemas.MCPClientState: Client state if found, nil otherwise
func (m *MCPManager) GetClientByName(clientName string) *schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, client := range m.clientMap {
		if client.ExecutionConfig.Name == clientName {
			// Return a copy to prevent TOCTOU race conditions
			// The caller receives a snapshot of the client state at this point in time
			clientCopy := *client
			return &clientCopy
		}
	}
	return nil
}

// retrieveExternalTools retrieves and filters tools from an external MCP server without holding locks.
func retrieveExternalTools(ctx context.Context, client *client.Client, clientName string) (map[string]schemas.ChatTool, error) {
	// Get available tools from external server
	listRequest := mcp.ListToolsRequest{
		PaginatedRequest: mcp.PaginatedRequest{
			Request: mcp.Request{
				Method: string(mcp.MethodToolsList),
			},
		},
	}

	toolsResponse, err := client.ListTools(ctx, listRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %v", err)
	}

	if toolsResponse == nil {
		return make(map[string]schemas.ChatTool), nil // No tools available
	}

	tools := make(map[string]schemas.ChatTool)

	// toolsResponse is already a ListToolsResult
	for _, mcpTool := range toolsResponse.Tools {
		// Convert MCP tool schema to Bifrost format
		bifrostTool := convertMCPToolToBifrostSchema(&mcpTool)
		// Prefix tool name with client name to make it permanent
		prefixedToolName := fmt.Sprintf("%s_%s", clientName, mcpTool.Name)
		// Update the tool's function name to match the prefixed name
		if bifrostTool.Function != nil {
			bifrostTool.Function.Name = prefixedToolName
		}
		tools[prefixedToolName] = bifrostTool
	}

	return tools, nil
}

// shouldIncludeClient determines if a client should be included based on filtering rules.
func shouldIncludeClient(clientName string, includeClients []string) bool {
	// If includeClients is specified (not nil), apply whitelist filtering
	if includeClients != nil {
		// Handle empty array [] - means no clients are included
		if len(includeClients) == 0 {
			return false // No clients allowed
		}

		// Handle wildcard "*" - if present, all clients are included
		if slices.Contains(includeClients, "*") {
			return true // All clients allowed
		}

		// Check if specific client is in the list
		return slices.Contains(includeClients, clientName)
	}

	// Default: include all clients when no filtering specified (nil case)
	return true
}

// shouldSkipToolForConfig checks if a tool should be skipped based on client configuration (without accessing clientMap).
func shouldSkipToolForConfig(toolName string, config schemas.MCPClientConfig) bool {
	// If ToolsToExecute is specified (not nil), apply filtering
	if config.ToolsToExecute != nil {
		// Handle empty array [] - means no tools are allowed
		if len(config.ToolsToExecute) == 0 {
			return true // No tools allowed
		}

		// Handle wildcard "*" - if present, all tools are allowed
		if slices.Contains(config.ToolsToExecute, "*") {
			return false // All tools allowed
		}

		// Check if specific tool is in the allowed list
		return !slices.Contains(config.ToolsToExecute, toolName) // Tool not in allowed list
	}

	return true // Tool is skipped (nil is treated as [] - no tools)
}

// canAutoExecuteTool checks if a tool can be auto-executed based on client configuration.
// Returns true if the tool can be auto-executed, false otherwise.
func canAutoExecuteTool(toolName string, config schemas.MCPClientConfig) bool {
	// First check if tool is in ToolsToExecute (must be executable first)
	if shouldSkipToolForConfig(toolName, config) {
		return false // Tool is not in ToolsToExecute, so it cannot be auto-executed
	}

	// If ToolsToAutoExecute is specified (not nil), apply filtering
	if config.ToolsToAutoExecute != nil {
		// Handle empty array [] - means no tools are auto-executed
		if len(config.ToolsToAutoExecute) == 0 {
			return false // No tools auto-executed
		}

		// Handle wildcard "*" - if present, all tools are auto-executed
		if slices.Contains(config.ToolsToAutoExecute, "*") {
			return true // All tools auto-executed
		}

		// Check if specific tool is in the auto-execute list
		return slices.Contains(config.ToolsToAutoExecute, toolName)
	}

	return false // Tool is not auto-executed (nil is treated as [] - no tools)
}

// shouldSkipToolForRequest checks if a tool should be skipped based on the request context.
func shouldSkipToolForRequest(ctx context.Context, clientName, toolName string) bool {
	includeTools := ctx.Value(MCPContextKeyIncludeTools)

	if includeTools != nil {
		// Try []string first (preferred type)
		if includeToolsList, ok := includeTools.([]string); ok {
			// Handle empty array [] - means no tools are included
			if len(includeToolsList) == 0 {
				return true // No tools allowed
			}

			// Handle wildcard "clientName/*" - if present, all tools are included for this client
			if slices.Contains(includeToolsList, fmt.Sprintf("%s/*", clientName)) {
				return false // All tools allowed
			}

			// Check if specific tool is in the list (format: clientName/toolName)
			fullToolName := fmt.Sprintf("%s/%s", clientName, toolName)
			if slices.Contains(includeToolsList, fullToolName) {
				return false // Tool is explicitly allowed
			}

			// If includeTools is specified but this tool is not in it, skip it
			return true
		}
	}

	return false // Tool is allowed (default when no filtering specified)
}

// convertMCPToolToBifrostSchema converts an MCP tool definition to Bifrost format.
func convertMCPToolToBifrostSchema(mcpTool *mcp.Tool) schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        mcpTool.Name,
			Description: schemas.Ptr(mcpTool.Description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       mcpTool.InputSchema.Type,
				Properties: schemas.Ptr(mcpTool.InputSchema.Properties),
				Required:   mcpTool.InputSchema.Required,
			},
		},
	}
}

// extractTextFromMCPResponse extracts text content from an MCP tool response.
func extractTextFromMCPResponse(toolResponse *mcp.CallToolResult, toolName string) string {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
	}

	var result strings.Builder
	for _, contentBlock := range toolResponse.Content {
		// Handle typed content
		switch content := contentBlock.(type) {
		case mcp.TextContent:
			result.WriteString(content.Text)
		case mcp.ImageContent:
			result.WriteString(fmt.Sprintf("[Image Response: %s, MIME: %s]\n", content.Data, content.MIMEType))
		case mcp.AudioContent:
			result.WriteString(fmt.Sprintf("[Audio Response: %s, MIME: %s]\n", content.Data, content.MIMEType))
		case mcp.EmbeddedResource:
			result.WriteString(fmt.Sprintf("[Embedded Resource Response: %s]\n", content.Type))
		default:
			// Fallback: try to extract from map structure
			if jsonBytes, err := json.Marshal(contentBlock); err == nil {
				var contentMap map[string]interface{}
				if json.Unmarshal(jsonBytes, &contentMap) == nil {
					if text, ok := contentMap["text"].(string); ok {
						result.WriteString(fmt.Sprintf("[Text Response: %s]\n", text))
						continue
					}
				}
				// Final fallback: serialize as JSON
				result.WriteString(string(jsonBytes))
			}
		}
	}

	if result.Len() > 0 {
		return strings.TrimSpace(result.String())
	}
	return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
}

// createToolResponseMessage creates a tool response message with the execution result.
func createToolResponseMessage(toolCall schemas.ChatAssistantMessageToolCall, responseText string) *schemas.ChatMessage {
	return &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: &responseText,
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: toolCall.ID,
		},
	}
}

// validateMCPClientConfig validates an MCP client configuration.
func validateMCPClientConfig(config *schemas.MCPClientConfig) error {
	if strings.TrimSpace(config.ID) == "" {
		return fmt.Errorf("id is required for MCP client config")
	}
	if err := validateMCPClientName(config.Name); err != nil {
		return fmt.Errorf("invalid name for MCP client: %w", err)
	}
	if config.ConnectionType == "" {
		return fmt.Errorf("connection type is required for MCP client config")
	}
	switch config.ConnectionType {
	case schemas.MCPConnectionTypeHTTP:
		if config.ConnectionString == nil {
			return fmt.Errorf("ConnectionString is required for HTTP connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSSE:
		if config.ConnectionString == nil {
			return fmt.Errorf("ConnectionString is required for SSE connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSTDIO:
		if config.StdioConfig == nil {
			return fmt.Errorf("StdioConfig is required for STDIO connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeInProcess:
		// InProcess requires a server instance to be provided programmatically
		// This cannot be validated from JSON config - the server must be set when using the Go package
		if config.InProcessServer == nil {
			return fmt.Errorf("InProcessServer is required for InProcess connection type in client '%s' (Go package only)", config.Name)
		}
	default:
		return fmt.Errorf("unknown connection type '%s' in client '%s'", config.ConnectionType, config.Name)
	}
	return nil
}

func validateMCPClientName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required for MCP client")
	}
	for _, r := range name {
		if r > 127 { // non-ASCII
			return fmt.Errorf("name must contain only ASCII characters")
		}
	}
	if strings.Contains(name, "-") {
		return fmt.Errorf("name cannot contain hyphens")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("name cannot contain spaces")
	}
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("name cannot start with a number")
	}
	return nil
}

// parseToolName parses the tool name to be JavaScript-compatible.
// It converts spaces and hyphens to underscores, removes invalid characters, and ensures
// the name starts with a valid JavaScript identifier character.
func parseToolName(toolName string) string {
	if toolName == "" {
		return ""
	}

	var result strings.Builder
	runes := []rune(toolName)

	// Process first character - must be letter, underscore, or dollar sign
	if len(runes) > 0 {
		first := runes[0]
		if unicode.IsLetter(first) || first == '_' || first == '$' {
			result.WriteRune(unicode.ToLower(first))
		} else {
			// If first char is invalid, prefix with underscore
			result.WriteRune('_')
			if unicode.IsDigit(first) {
				result.WriteRune(first)
			}
		}
	}

	// Process remaining characters
	for i := 1; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' {
			result.WriteRune(unicode.ToLower(r))
		} else if unicode.IsSpace(r) || r == '-' {
			// Replace spaces and hyphens with single underscore
			// Avoid consecutive underscores
			if result.Len() > 0 && result.String()[result.Len()-1] != '_' {
				result.WriteRune('_')
			}
		}
		// Skip other invalid characters
	}

	parsed := result.String()

	// Remove trailing underscores
	parsed = strings.TrimRight(parsed, "_")

	// Ensure we have at least one character
	// Should never happen, but just in case
	if parsed == "" {
		return "tool"
	}

	return parsed
}

// extractToolCallsFromCode extracts tool calls from TypeScript code
// Tool calls are in the format: serverName.toolName(...) or await serverName.toolName(...)
func extractToolCallsFromCode(code string) ([]toolCallInfo, error) {
	toolCalls := []toolCallInfo{}

	// Regex pattern to match tool calls:
	// - Optional "await" keyword
	// - Server name (identifier)
	// - Dot
	// - Tool name (identifier)
	// - Opening parenthesis
	// This pattern matches: await serverName.toolName( or serverName.toolName(
	toolCallPattern := regexp.MustCompile(`(?:await\s+)?([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\.\s*([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\(`)

	// Find all matches
	matches := toolCallPattern.FindAllStringSubmatch(code, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			serverName := match[1]
			toolName := match[2]
			toolCalls = append(toolCalls, toolCallInfo{
				serverName: serverName,
				toolName:   toolName,
			})
		}
	}

	return toolCalls, nil
}

// isToolCallAllowedForCodeMode checks if a tool call is allowed based on allowedAutoExecutionTools map
func isToolCallAllowedForCodeMode(serverName, toolName string, allClientNames []string, allowedAutoExecutionTools map[string][]string) bool {
	// Check if the server name is in the list of all client names
	if !slices.Contains(allClientNames, serverName) {
		// It can be a built-in JavaScript/TypeScript object, if not then downstream execution will fail with a runtime error.
		return true
	}

	// Get allowed tools for this server
	allowedTools, exists := allowedAutoExecutionTools[serverName]
	if !exists {
		// Server not in allowed list, return false to prevent downstream execution.
		return false
	}

	// Check if wildcard "*" is present (all tools allowed)
	if slices.Contains(allowedTools, "*") {
		return true
	}

	// Check if specific tool is in the allowed list
	if slices.Contains(allowedTools, toolName) {
		return true
	}

	return false // Tool not in allowed list
}

// hasToolCalls checks if a chat response contains tool calls that need to be executed
func hasToolCallsForChatResponse(response *schemas.BifrostChatResponse) bool {
	if response == nil || len(response.Choices) == 0 {
		return false
	}

	choice := response.Choices[0]

	// If finish_reason is "stop", this indicates non-auto-executable tools that require user approval.
	// Don't return true even if tool calls are present, as the agent loop should not process them.
	if choice.FinishReason != nil && *choice.FinishReason == "stop" {
		return false
	}

	// Check finish reason
	if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" {
		return true
	}

	// Check if message has tool calls
	if choice.ChatNonStreamResponseChoice != nil &&
		choice.ChatNonStreamResponseChoice.Message != nil &&
		choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil &&
		len(choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls) > 0 {
		return true
	}

	return false
}

func hasToolCallsForResponsesResponse(response *schemas.BifrostResponsesResponse) bool {
	if response == nil || len(response.Output) == 0 {
		return false
	}

	// Check if any output message is a tool call
	for _, output := range response.Output {
		if output.Type == nil {
			continue
		}

		// Check for tool call types
		switch *output.Type {
		case schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesMessageTypeCustomToolCall:
			// Verify that ResponsesToolMessage is actually set
			if output.ResponsesToolMessage != nil {
				return true
			}
		}
	}

	return false
}

// stripClientPrefix removes the client name prefix from a tool name.
// Tool names are stored with format "{clientName}_{toolName}", but when calling
// the MCP server, we need the original tool name without the prefix.
//
// Parameters:
//   - prefixedToolName: Tool name with client prefix (e.g., "calculator_add")
//   - clientName: Client name to strip (e.g., "calculator")
//
// Returns:
//   - string: Original tool name without prefix (e.g., "add")
func stripClientPrefix(prefixedToolName, clientName string) string {
	prefix := clientName + "_"
	if strings.HasPrefix(prefixedToolName, prefix) {
		return strings.TrimPrefix(prefixedToolName, prefix)
	}
	// If prefix doesn't match, return as-is (shouldn't happen, but be safe)
	return prefixedToolName
}
