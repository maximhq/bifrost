// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// MCPServerInstance represents an MCP server instance for InProcess connections.
// This should be a *github.com/mark3labs/mcp-go/server.MCPServer instance.
// We use interface{} to avoid creating a dependency on the mcp-go package in schemas.
type MCPServerInstance interface{}

// MCPConfig represents the configuration for MCP integration in Bifrost.
// It enables tool auto-discovery and execution from local and external MCP servers.
type MCPConfig struct {
	ClientConfigs []MCPClientConfig `json:"client_configs,omitempty"` // Per-client execution configurations
}

// MCPClientConfig defines tool filtering for an MCP client.
type MCPClientConfig struct {
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
	Name   string             `json:"name"`   // Unique name for this client
	Config MCPClientConfig    `json:"config"` // Tool filtering settings
	Tools  []ChatToolFunction `json:"tools"`  // Available tools
	State  MCPConnectionState `json:"state"`  // Connection state
}
