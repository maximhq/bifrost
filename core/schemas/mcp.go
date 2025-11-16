// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import "context"

// MCPServerInstance represents an MCP server instance for InProcess connections.
// This should be a *github.com/mark3labs/mcp-go/server.MCPServer instance.
// We use interface{} to avoid creating a dependency on the mcp-go package in schemas.
type MCPServerInstance interface{}

// MCPManager defines the interface for MCP (Model Context Protocol) management operations.
// It provides methods for client management, tool registration and execution, and agent mode functionality.
type MCPManager interface {
	// Client Management
	GetClients() ([]MCPClient, error)
	AddClient(config MCPClientConfig) error
	RemoveClient(id string) error
	EditClient(id string, updatedConfig MCPClientConfig) error
	ReconnectClient(id string) error

	// Tool Management
	RegisterTool(name, description string, handler func(args any) (string, error), toolSchema ChatTool) error
	ExecuteTool(ctx context.Context, toolCall ChatAssistantMessageToolCall) (*ChatMessage, error)
	AddMCPToolsToBifrostRequest(ctx context.Context, req *BifrostRequest) *BifrostRequest

	// Agent Mode
	CheckAndExecuteAgentMode(ctx *context.Context, originalReq *BifrostChatRequest, initialResponse *BifrostChatResponse, llmCaller BifrostLLMCaller) (*BifrostChatResponse, *BifrostError)

	// Lifecycle Management
	Cleanup() error
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
	InProcessServer  MCPServerInstance `json:"-"`                           // MCP server instance for in-process connections (Go package only)
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

// MCPClient represents a connected MCP client with its configuration and tools,
// and connection information, after it has been initialized.
// It is returned by GetMCPClients() method.
type MCPClient struct {
	Config MCPClientConfig    `json:"config"` // Tool filtering settings
	Tools  []ChatToolFunction `json:"tools"`  // Available tools
	State  MCPConnectionState `json:"state"`  // Connection state
}
