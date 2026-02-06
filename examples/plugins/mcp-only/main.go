package main

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin configuration
type PluginConfig struct {
	BlockedTools       []string `json:"blocked_tools"`        // List of tool names to block
	EnableAudit        bool     `json:"enable_audit"`         // Enable audit trail logging
	EnableLogging      bool     `json:"enable_logging"`       // Enable detailed logging
	TransformErrors    bool     `json:"transform_errors"`     // Transform 404 errors to friendly messages
	CustomErrorMessage string   `json:"custom_error_message"` // Custom error message for blocked tools
}

var (
	// Default configuration
	pluginConfig = &PluginConfig{
		BlockedTools:       []string{"dangerous_tool"},
		EnableAudit:        true,
		EnableLogging:      true,
		TransformErrors:    true,
		CustomErrorMessage: "Tool is not allowed by security policy",
	}
)

// Init is called when the plugin is loaded (optional)
func Init(config any) error {
	fmt.Println("[MCP-Only Plugin] Init called")

	// Parse configuration
	if configMap, ok := config.(map[string]interface{}); ok {
		if blockedTools, ok := configMap["blocked_tools"].([]interface{}); ok {
			pluginConfig.BlockedTools = []string{}
			for _, tool := range blockedTools {
				if toolName, ok := tool.(string); ok {
					pluginConfig.BlockedTools = append(pluginConfig.BlockedTools, toolName)
				}
			}
			fmt.Printf("[MCP-Only Plugin] Blocked tools: %v\n", pluginConfig.BlockedTools)
		}

		if enableAudit, ok := configMap["enable_audit"].(bool); ok {
			pluginConfig.EnableAudit = enableAudit
			fmt.Printf("[MCP-Only Plugin] Audit trail: %v\n", pluginConfig.EnableAudit)
		}

		if enableLogging, ok := configMap["enable_logging"].(bool); ok {
			pluginConfig.EnableLogging = enableLogging
			fmt.Printf("[MCP-Only Plugin] Logging enabled: %v\n", pluginConfig.EnableLogging)
		}

		if transformErrors, ok := configMap["transform_errors"].(bool); ok {
			pluginConfig.TransformErrors = transformErrors
			fmt.Printf("[MCP-Only Plugin] Error transformation: %v\n", pluginConfig.TransformErrors)
		}

		if customMsg, ok := configMap["custom_error_message"].(string); ok {
			pluginConfig.CustomErrorMessage = customMsg
		}
	}

	fmt.Printf("[MCP-Only Plugin] Configuration loaded: %+v\n", pluginConfig)
	return nil
}

// GetName returns the name of the plugin (required)
// This is the system identifier - not editable by users
// Users can set a custom display_name in the config for the UI
func GetName() string {
	return "mcp-only"
}

// PreMCPHook is called before MCP tool/resource calls are executed
// This example demonstrates request validation and governance
func PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if pluginConfig.EnableLogging {
		fmt.Println("[MCP-Only Plugin] PreMCPHook called")
		fmt.Printf("[MCP-Only Plugin] Request type: %v\n", req.RequestType)
	}

	// Example: Governance - check tool calls (configurable)
	if req.ChatAssistantMessageToolCall != nil {
		toolName := ""
		if req.ChatAssistantMessageToolCall.Function.Name != nil {
			toolName = *req.ChatAssistantMessageToolCall.Function.Name
		}

		if pluginConfig.EnableLogging {
			fmt.Printf("[MCP-Only Plugin] Tool call: %s\n", toolName)
		}

		// Check if tool is in blocked list
		for _, blockedTool := range pluginConfig.BlockedTools {
			if toolName == blockedTool {
				fmt.Printf("[MCP-Only Plugin] Blocked tool call: %s\n", toolName)
				// Return a short-circuit response to prevent the call
				errorMsg := fmt.Sprintf("%s: %s", pluginConfig.CustomErrorMessage, toolName)
				// Get the tool call ID to link the response back to the original call
				toolCallID := req.ChatAssistantMessageToolCall.ID
				return req, &schemas.MCPPluginShortCircuit{
					Response: &schemas.BifrostMCPResponse{
						// Chat API format - tool result message
						ChatMessage: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleTool,
							ChatToolMessage: &schemas.ChatToolMessage{
								ToolCallID: toolCallID,
							},
							Content: &schemas.ChatMessageContent{
								ContentStr: &errorMsg,
							},
						},
						// Responses API format - function_call_output
						ResponsesMessage: &schemas.ResponsesMessage{
							Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
							ResponsesToolMessage: &schemas.ResponsesToolMessage{
								CallID: toolCallID,
								Output: &schemas.ResponsesToolMessageOutputStruct{
									ResponsesToolCallOutputStr: &errorMsg,
								},
							},
						},
					},
				}, nil
			}
		}
	}

	// Example: Add audit trail to context (configurable)
	if pluginConfig.EnableAudit {
		auditMsg := fmt.Sprintf("MCP request processed at %v", ctx.Value(schemas.BifrostContextKey("request_id")))
		ctx.SetValue(schemas.BifrostContextKey("mcp-audit-trail"), auditMsg)
		if pluginConfig.EnableLogging {
			fmt.Printf("[MCP-Only Plugin] Audit: %s\n", auditMsg)
		}
	}

	// Return modified request, no short-circuit, no error
	return req, nil, nil
}

// PostMCPHook is called after MCP tool/resource calls complete
// This example demonstrates response logging and error handling
func PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if pluginConfig.EnableLogging {
		fmt.Println("[MCP-Only Plugin] PostMCPHook called")
	}

	// Retrieve audit trail from context (if enabled)
	if pluginConfig.EnableAudit {
		auditTrail := ctx.Value(schemas.BifrostContextKey("mcp-audit-trail"))
		if pluginConfig.EnableLogging {
			fmt.Printf("[MCP-Only Plugin] Audit trail: %v\n", auditTrail)
		}
	}

	// Example: Log the response (configurable)
	if pluginConfig.EnableLogging && resp != nil {
		if resp.ChatMessage != nil {
			fmt.Printf("[MCP-Only Plugin] Chat message response received\n")
		}
		if resp.ResponsesMessage != nil {
			fmt.Printf("[MCP-Only Plugin] Responses message received\n")
		}
	}

	// Example: Log errors if present
	if bifrostErr != nil && bifrostErr.Error != nil {
		fmt.Printf("[MCP-Only Plugin] Error occurred: %v\n", bifrostErr.Error.Message)
	}

	// Example: Transform error responses (configurable)
	if pluginConfig.TransformErrors && bifrostErr != nil && bifrostErr.StatusCode != nil && *bifrostErr.StatusCode == 404 {
		// Convert 404 to a more user-friendly error
		if bifrostErr.Error != nil {
			bifrostErr.Error.Message = "The requested MCP resource was not found. Please check your request."
			if pluginConfig.EnableLogging {
				fmt.Println("[MCP-Only Plugin] Error message transformed")
			}
		}
	}

	// Return modified response and error
	return resp, bifrostErr, nil
}

// Cleanup is called when the plugin is unloaded (required)
func Cleanup() error {
	if pluginConfig.EnableLogging {
		fmt.Println("[MCP-Only Plugin] Cleanup called")
	}
	return nil
}
