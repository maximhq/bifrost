// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/server"
)

type MCPToolHandler interface {
	SetupCompleted() bool                                                                                                                                   // Returns true if the handler is fully setup and ready to use
	ParseAndAddToolsToRequest(ctx context.Context, req *BifrostRequest) *BifrostRequest                                                                     // Parse the available tools and add them to the Bifrost request
	ExecuteTool(ctx context.Context, toolCall ChatAssistantMessageToolCall) (*ChatMessage, error)                                                           // Execute a tool call and return the result as a tool message, It DOES NOT check if the tool is allowed to be executed by the client, so it is the responsibility of the caller to check if the tool is allowed to be executed by the client.
	ExecuteAgent(ctx context.Context, req *BifrostChatRequest, resp *BifrostChatResponse, llmCaller BifrostLLMCaller) (*BifrostChatResponse, *BifrostError) // Execute an agent mode tool call and return the result as a chat response
	SetToolsFetcherFunc(toolsFetcherFunc func(ctx context.Context) []ChatTool)                                                                              // Set the function to get the available tools
	SetClientForToolFetcherFunc(clientForToolFetcherFunc func(toolName string) *MCPClientState)                                                             // Set the function to get the client for a tool
	SetFetchNewRequestIDFunc(fetchNewRequestIDFunc func(ctx context.Context) string)                                                                        // Set the function to get a new request ID
}

// BifrostLLMCaller defines the interface for making LLM calls from the agent mode.
// This interface allows the MCP manager to make chat completion requests during agent execution.
type BifrostLLMCaller interface {
	ChatCompletionRequest(ctx context.Context, req *BifrostChatRequest) (*BifrostChatResponse, *BifrostError)
}

// MCPConfig represents the configuration for MCP integration in Bifrost.
// It enables tool auto-discovery and execution from local and external MCP servers.
type MCPConfig struct {
	ClientConfigs        []MCPClientConfig `json:"client_configs,omitempty"`         // Per-client execution configurations
	MaxAgentDepth        int               `json:"max_agent_depth,omitempty"`        // Maximum depth for agent mode tool execution (default: 10)
	ToolExecutionTimeout int               `json:"tool_execution_timeout,omitempty"` // Timeout for individual tool execution in seconds (default: 30)

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provider, same request ID is used for all tool call result messages without any overrides.
	FetchNewRequestIDFunc func(ctx context.Context) string `json:"-"`
}

// MCPClientConfig defines tool filtering for an MCP client.
type MCPClientConfig struct {
	ID               string            `json:"id"`                          // Client ID
	Name             string            `json:"name"`                        // Client name
	ConnectionType   MCPConnectionType `json:"connection_type"`             // How to connect (HTTP, STDIO, SSE, or InProcess)
	ConnectionString *string           `json:"connection_string,omitempty"` // HTTP or SSE URL (required for HTTP or SSE connections)
	StdioConfig      *MCPStdioConfig   `json:"stdio_config,omitempty"`      // STDIO configuration (required for STDIO connections)
	Headers          map[string]string `json:"headers,omitempty"`           // Headers to send with the request
	InProcessServer  *server.MCPServer `json:"-"`                           // MCP server instance for in-process connections (Go package only)
	ToolsToExecute   []string          `json:"tools_to_execute,omitempty"`  // Include-only list.
	// ToolsToExecute semantics:
	// - ["*"] => all tools are included
	// - []    => no tools are included (deny-by-default)
	// - nil/omitted => treated as [] (no tools)
	// - ["tool1", "tool2"] => include only the specified tools
	ToolsToAutoExecute []string `json:"tools_to_auto_execute,omitempty"` // Auto-execute list.
	// ToolsToAutoExecute semantics:
	// - ["*"] => all tools are auto-executed
	// - []    => no tools are auto-executed (deny-by-default)
	// - nil/omitted => treated as [] (no tools)
	// - ["tool1", "tool2"] => auto-execute only the specified tools
	// Note: If a tool is in ToolsToAutoExecute but not in ToolsToExecute, it will be skipped.
}

// MCPConnectionType defines the communication protocol for MCP connections
type MCPConnectionType string

const (
	MCPConnectionTypeHTTP      MCPConnectionType = "http"      // HTTP-based connection
	MCPConnectionTypeSTDIO     MCPConnectionType = "stdio"     // STDIO-based connection
	MCPConnectionTypeSSE       MCPConnectionType = "sse"       // Server-Sent Events connection
	MCPConnectionTypeInProcess MCPConnectionType = "inprocess" // In-process (in-memory) connection
)

// MCPStdioConfig defines how to launch a STDIO-based MCP server.
type MCPStdioConfig struct {
	Command string   `json:"command"` // Executable command to run
	Args    []string `json:"args"`    // Command line arguments
	Envs    []string `json:"envs"`    // Environment variables required
}

type MCPConnectionState string

const (
	MCPConnectionStateConnected    MCPConnectionState = "connected"    // Client is connected and ready to use
	MCPConnectionStateDisconnected MCPConnectionState = "disconnected" // Client is not connected
	MCPConnectionStateError        MCPConnectionState = "error"        // Client is in an error state, and cannot be used
)

// MCPClientState represents a connected MCP client with its configuration and tools.
// It is used internally by the MCP manager to track the state of a connected MCP client.
type MCPClientState struct {
	Name            string                  // Unique name for this client
	Conn            *client.Client          // Active MCP client connection
	ExecutionConfig MCPClientConfig         // Tool filtering settings
	ToolMap         map[string]ChatTool     // Available tools mapped by name
	ConnectionInfo  MCPClientConnectionInfo `json:"connection_info"` // Connection metadata for management
	CancelFunc      context.CancelFunc      `json:"-"`               // Cancel function for SSE connections (not serialized)
}

// MCPClientConnectionInfo stores metadata about how a client is connected.
type MCPClientConnectionInfo struct {
	Type               MCPConnectionType `json:"type"`                           // Connection type (HTTP, STDIO, SSE, or InProcess)
	ConnectionURL      *string           `json:"connection_url,omitempty"`       // HTTP/SSE endpoint URL (for HTTP/SSE connections)
	StdioCommandString *string           `json:"stdio_command_string,omitempty"` // Command string for display (for STDIO connections)
}

// MCPClient represents a connected MCP client with its configuration and tools,
// and connection information, after it has been initialized.
// It is returned by GetMCPClients() method in bifrost.
type MCPClient struct {
	Config MCPClientConfig    `json:"config"` // Tool filtering settings
	Tools  []ChatToolFunction `json:"tools"`  // Available tools
	State  MCPConnectionState `json:"state"`  // Connection state
}
