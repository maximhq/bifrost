package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// findMCPClientForTool safely finds a client that has the specified tool.
func (m *MCPManager) findMCPClientForTool(toolName string) *MCPClient {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, client := range m.clientMap {
		if _, exists := client.ToolMap[toolName]; exists {
			return client
		}
	}
	return nil
}

// getAvailableTools returns all tools from connected MCP clients.
// Applies client filtering if specified in the context.
func (m *MCPManager) getAvailableTools(ctx context.Context) []schemas.ChatTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var includeClients []string

	// Extract client filtering from request context
	if existingIncludeClients, ok := ctx.Value(MCPContextKeyIncludeClients).([]string); ok && existingIncludeClients != nil {
		includeClients = existingIncludeClients
	}

	tools := make([]schemas.ChatTool, 0)
	for id, client := range m.clientMap {
		// Apply client filtering logic
		if !shouldIncludeClient(id, includeClients) {
			logger.Debug(fmt.Sprintf("%s Skipping MCP client %s: not in include clients list", MCPLogPrefix, id))
			continue
		}

		logger.Debug(fmt.Sprintf("Checking tools for MCP client %s with tools to execute: %v", id, client.ExecutionConfig.ToolsToExecute))

		// Add all tools from this client
		for toolName, tool := range client.ToolMap {
			// Check if tool should be skipped based on client configuration
			if shouldSkipToolForConfig(toolName, client.ExecutionConfig) {
				logger.Debug(fmt.Sprintf("%s Skipping MCP tool %s: not in tools to execute list", MCPLogPrefix, toolName))
				continue
			}

			// Check if tool should be skipped based on request context
			if shouldSkipToolForRequest(id, toolName, ctx) {
				logger.Debug(fmt.Sprintf("%s Skipping MCP tool %s: not in include tools list", MCPLogPrefix, toolName))
				continue
			}

			tools = append(tools, tool)
		}
	}
	return tools
}

// retrieveExternalTools retrieves and filters tools from an external MCP server without holding locks.
func retrieveExternalTools(ctx context.Context, client *client.Client) (map[string]schemas.ChatTool, error) {
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
		tools[mcpTool.Name] = bifrostTool
	}

	return tools, nil
}

// shouldIncludeClient determines if a client should be included based on filtering rules.
func shouldIncludeClient(clientID string, includeClients []string) bool {
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
		return slices.Contains(includeClients, clientID)
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

// shouldSkipToolForRequest checks if a tool should be skipped based on the request context.
func shouldSkipToolForRequest(clientID, toolName string, ctx context.Context) bool {
	includeTools := ctx.Value(MCPContextKeyIncludeTools)

	if includeTools != nil {
		// Try []string first (preferred type)
		if includeToolsList, ok := includeTools.([]string); ok {
			// Handle empty array [] - means no tools are included
			if len(includeToolsList) == 0 {
				return true // No tools allowed
			}

			// Handle wildcard "clientName/*" - if present, all tools are included for this client
			if slices.Contains(includeToolsList, fmt.Sprintf("%s/*", clientID)) {
				return false // All tools allowed
			}

			// Check if specific tool is in the list (format: clientName/toolName)
			fullToolName := fmt.Sprintf("%s/%s", clientID, toolName)
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

	if strings.TrimSpace(config.Name) == "" {
		return fmt.Errorf("name is required for MCP client config")
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
