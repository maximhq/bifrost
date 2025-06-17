package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	mcp_golang "github.com/metoro-io/mcp-golang"
)

// Helper method to find MCP client for a tool (fixes repeated code and race condition)
func (p *MCPPlugin) findMCPClientForTool(toolName string) *PluginClient {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, client := range p.clientMap {
		if client.ToolMap != nil {
			if _, isMCPTool := client.ToolMap[toolName]; isMCPTool {
				return client
			}
		}
	}
	return nil
}

// Helper method to create tool response message (fixes repeated code)
func (p *MCPPlugin) createToolResponseMessage(toolCall schemas.ToolCall, responseText string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role:    schemas.ModelChatMessageRoleTool,
		Content: schemas.MessageContent{ContentStr: &responseText},
		ToolMessage: &schemas.ToolMessage{
			ToolCallID: toolCall.ID,
		},
	}
}

// Helper method to extract text from MCP response (fixes repeated code)
func (p *MCPPlugin) extractTextFromMCPResponse(toolResponse *mcp_golang.ToolResponse, toolName string) string {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
	}

	var responseTextBuilder strings.Builder
	if len(toolResponse.Content) > 0 {
		for _, contentBlock := range toolResponse.Content {
			if contentBlock.TextContent != nil && contentBlock.TextContent.Text != "" {
				responseTextBuilder.WriteString(contentBlock.TextContent.Text)
				responseTextBuilder.WriteString("\n")
			}
		}
	}

	if responseTextBuilder.Len() > 0 {
		return strings.TrimSpace(responseTextBuilder.String())
	}
	return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
}

// Helper method to execute a single tool (fixes repeated code and context issues)
func (p *MCPPlugin) executeSingleTool(ctx context.Context, toolCall schemas.ToolCall) (schemas.BifrostMessage, error) {
	if toolCall.Function.Name == nil {
		return schemas.BifrostMessage{}, fmt.Errorf("tool call missing function name")
	}
	toolName := *toolCall.Function.Name

	// Parse tool arguments
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return schemas.BifrostMessage{}, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	// Call the tool via MCP client -> MCP server
	toolResponse, callErr := p.callTool(ctx, toolName, arguments)
	if callErr != nil {
		return schemas.BifrostMessage{}, fmt.Errorf("MCP tool call failed: %v", callErr)
	}

	// Extract text from MCP response
	responseText := p.extractTextFromMCPResponse(toolResponse, toolName)

	// Create tool response message
	return p.createToolResponseMessage(toolCall, responseText), nil
}

// convertMCPToolToBifrostSchema converts an MCP tool to Bifrost schema format
func convertMCPToolToBifrostSchema(mcpTool *mcp_golang.ToolRetType) schemas.Tool {
	// Convert MCP tool schema to Bifrost tool schema
	properties := make(map[string]interface{})
	required := []string{}

	if mcpTool.InputSchema != nil {
		if schemaMap, ok := mcpTool.InputSchema.(map[string]interface{}); ok {
			if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
				properties = props
			}
			if req, ok := schemaMap["required"].([]interface{}); ok {
				for _, r := range req {
					if reqStr, ok := r.(string); ok {
						required = append(required, reqStr)
					}
				}
			}
		}
	}

	// If no properties are defined, create an empty properties object
	// This is required by OpenAI's function calling schema
	if properties == nil {
		properties = make(map[string]interface{})
	}

	description := ""
	if mcpTool.Description != nil {
		description = *mcpTool.Description
	}

	return schemas.Tool{
		Type: "function",
		Function: schemas.Function{
			Name:        mcpTool.Name,
			Description: description,
			Parameters: schemas.FunctionParameters{
				Type:       "object",
				Properties: properties,
				Required:   required,
			},
		},
	}
}

// shouldIncludeClient determines if a client should be included based on filtering rules
func (p *MCPPlugin) shouldIncludeClient(clientName string, includeClients, excludeClients []string) bool {
	// If includeClients is specified, only include those clients (whitelist mode)
	if len(includeClients) > 0 {
		for _, includeName := range includeClients {
			if clientName == includeName {
				return true
			}
		}
		return false // Not in include list
	}

	// If excludeClients is specified, exclude those clients (blacklist mode)
	if len(excludeClients) > 0 {
		for _, excludeName := range excludeClients {
			if clientName == excludeName {
				return false
			}
		}
	}

	// Default: include all clients
	return true
}

// checkAgenticModeAvailable checks if agentic mode is properly configured and logs errors
func (p *MCPPlugin) checkAgenticModeAvailable() bool {
	if p.agenticMode && p.bifrostClient == nil {
		p.logger.Warn(LogPrefix + " Agentic mode is enabled but Bifrost client is not set. Falling back to normal execution.")
		p.logger.Info(LogPrefix + " Hint: Call plugin.SetBifrostClient(bifrostInstance) to enable agentic mode.")
		return false
	}
	return p.agenticMode && p.bifrostClient != nil
}

// getToolExecutionPolicy returns the execution policy for a given tool
func (p *MCPPlugin) getToolExecutionPolicy(toolName, clientName string) ToolExecutionPolicy {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Then check client-specific configuration (external MCP tools)
	client, exists := p.clientMap[clientName]
	if !exists {
		// Default: require approval for unknown clients
		return ToolExecutionPolicyRequireApproval
	}

	// Check for tool-specific policy first
	if toolPolicy, exists := client.ExecutionConfig.ToolPolicies[toolName]; exists {
		return toolPolicy
	}

	// Fall back to client default policy
	return client.ExecutionConfig.DefaultPolicy
}

// shouldRequireApproval returns true if the tool requires user approval
func (p *MCPPlugin) shouldRequireApproval(toolName, clientName string) bool {
	return p.getToolExecutionPolicy(toolName, clientName) == ToolExecutionPolicyRequireApproval
}

// shouldSkipTool returns true if the tool should be skipped for this client
func (p *MCPPlugin) shouldSkipTool(toolName, clientName string) bool {
	// ConnectToExternalMCP function already has the mutex lock
	client, exists := p.clientMap[clientName]
	if !exists {
		return false // Don't skip if client config doesn't exist
	}

	return slices.Contains(client.ExecutionConfig.ToolsToSkip, toolName)
}
