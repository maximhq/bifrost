package main

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// Plugin configuration
type PluginConfig struct {
	BlockedTools       []string `json:"blocked_tools"`        // List of tool names to block (envelope hooks)
	BlockedClients     []string `json:"blocked_clients"`      // MCP client names to refuse connections to (Connect hooks)
	AuditHeader        string   `json:"audit_header"`         // If non-empty, inject this header into HTTP/SSE Connect requests
	EnableAudit        bool     `json:"enable_audit"`         // Enable audit trail logging
	EnableLogging      bool     `json:"enable_logging"`       // Enable detailed logging
	TransformErrors    bool     `json:"transform_errors"`     // Transform 404 errors to friendly messages
	CustomErrorMessage string   `json:"custom_error_message"` // Custom error message for blocked tools / clients
}

var (
	// Default configuration
	pluginConfig = &PluginConfig{
		BlockedTools:       []string{"dangerous_tool"},
		BlockedClients:     []string{},
		AuditHeader:        "",
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

		if blockedClients, ok := configMap["blocked_clients"].([]interface{}); ok {
			pluginConfig.BlockedClients = []string{}
			for _, c := range blockedClients {
				if name, ok := c.(string); ok {
					pluginConfig.BlockedClients = append(pluginConfig.BlockedClients, name)
				}
			}
			fmt.Printf("[MCP-Only Plugin] Blocked clients: %v\n", pluginConfig.BlockedClients)
		}

		if auditHeader, ok := configMap["audit_header"].(string); ok {
			pluginConfig.AuditHeader = auditHeader
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

// PreMCPConnectionHook is called once per MCP client when its transport is
// being established (Connect lifecycle), separate from the per-call PreMCPHook
// path above. The request carries transport-level inputs — some are mutable
// and survive into the actual transport creation, others are observe-only.
//
// Mutable fields (changes are honored by Bifrost):
//   - req.ConnectionString  → URL for http/sse transports
//   - req.Headers           → transport-level headers (http/sse only; stdio/inprocess ignore)
//   - req.StdioCommand      → command for stdio transports
//   - req.StdioArgs         → argv for stdio transports
//
// Observe-only fields (mutations are ignored; changing them mid-flight would
// break the rest of the connect codepath):
//   - req.ClientName
//   - req.ConnectionType
//   - req.AuthType
//
// Returning a non-nil *MCPConnectionShortCircuit blocks the connection.
// Use Error to surface a refusal; use Response to synthesize a successful
// handshake (rare — typically for testing or mocking).
func PreMCPConnectionHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, error) {
	if pluginConfig.EnableLogging {
		fmt.Printf("[MCP-Only Plugin] PreMCPConnectionHook called: client=%s type=%s auth=%s\n",
			req.ClientName, req.ConnectionType, req.AuthType)

		allHeaders := ctx.Value(schemas.BifrostContextKeyRequestHeaders)
		fmt.Printf("[MCP-Only Plugin] Request headers: %+v\n", allHeaders)

	}

	// Example: refuse connections to blocklisted clients.
	for _, blocked := range pluginConfig.BlockedClients {
		if req.ClientName == blocked {
			fmt.Printf("[MCP-Only Plugin] Refusing Connect for blocked client: %s\n", req.ClientName)
			errMsg := fmt.Sprintf("%s: client %q is not allowed", pluginConfig.CustomErrorMessage, req.ClientName)
			return req, &schemas.MCPConnectionShortCircuit{
				Error: &schemas.BifrostError{
					StatusCode: schemas.Ptr(403),
					Error: &schemas.ErrorField{
						Message: errMsg,
					},
				},
			}, nil
		}
	}

	// Example: inject an audit header on HTTP/SSE transports. STDIO and
	// InProcess transports ignore Headers, so this mutation is silently a
	// no-op for them — the observe-only ConnectionType tells you which
	// transport you're about to bring up.
	if pluginConfig.AuditHeader != "" &&
		(req.ConnectionType == schemas.MCPConnectionTypeHTTP || req.ConnectionType == schemas.MCPConnectionTypeSSE) {
		if req.Headers == nil {
			req.Headers = make(map[string]string)
		}
		req.Headers[pluginConfig.AuditHeader] = fmt.Sprintf("bifrost-mcp-connect:%s", req.ClientName)
	}

	return req, nil, nil
}

// PostMCPConnectionHook runs after the upstream MCP handshake completes.
// The response carries ServerInfo + capability flags + protocol version
// negotiated during initialize. Use this for observation, capability gating,
// or to attach connection metadata to downstream telemetry.
//
// On a failed handshake, resp is nil and bifrostErr is populated. Plugins
// can return a transformed error (mirroring PostMCPHook's pattern below).
func PostMCPConnectionHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError, error) {
	if pluginConfig.EnableLogging {
		fmt.Println("[MCP-Only Plugin] PostMCPConnectionHook called")
	}

	if bifrostErr != nil {
		if pluginConfig.EnableLogging && bifrostErr.Error != nil {
			fmt.Printf("[MCP-Only Plugin] Connect failed: %s\n", bifrostErr.Error.Message)
		}
		return resp, bifrostErr, nil
	}

	if resp != nil && pluginConfig.EnableLogging {
		if resp.ServerInfo != nil {
			fmt.Printf("[MCP-Only Plugin] Connected: server=%s version=%s protocol=%s\n",
				resp.ServerInfo.Name, resp.ServerInfo.Version, resp.ProtocolVersion)
		}
		if resp.ServerCapabilities != nil && !resp.ServerCapabilities.Tools {
			fmt.Printf("[MCP-Only Plugin] Warning: server %q does not advertise Tools capability\n", resp.ExtraFields.ClientName)
		}
	}

	return resp, bifrostErr, nil
}

// Cleanup is called when the plugin is unloaded (required)
func Cleanup() error {
	if pluginConfig.EnableLogging {
		fmt.Println("[MCP-Only Plugin] Cleanup called")
	}
	return nil
}
