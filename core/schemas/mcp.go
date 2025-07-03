// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// MCPConfig represents the configuration for MCP integration in Bifrost.
// It enables tool auto-discovery and execution from local and external MCP servers.
type MCPConfig struct {
	ClientConfigs []MCPClientConfig `json:"client_configs,omitempty"` // Per-client execution configurations
}

// MCPClientConfig defines tool filtering for an MCP client.
type MCPClientConfig struct {
	Name             string            `json:"name"`                        // Client name
	ConnectionType   MCPConnectionType `json:"connection_type"`             // How to connect (HTTP, STDIO, or SSE)
	ConnectionString *string           `json:"connection_string,omitempty"` // HTTP or SSE URL (required for HTTP or SSE connections)
	StdioConfig      *MCPStdioConfig   `json:"stdio_config,omitempty"`      // STDIO configuration (required for STDIO connections)
	ToolsToSkip      []string          `json:"tools_to_skip,omitempty"`     // Tools to exclude from this client
	ToolsToExecute   []string          `json:"tools_to_execute,omitempty"`  // Tools to include from this client (if specified, only these are used)
}

// MCPConnectionType defines the communication protocol for MCP connections
type MCPConnectionType string

const (
	MCPConnectionTypeHTTP  MCPConnectionType = "http"  // HTTP-based MCP connection (streamable)
	MCPConnectionTypeSTDIO MCPConnectionType = "stdio" // STDIO-based MCP connection
	MCPConnectionTypeSSE   MCPConnectionType = "sse"   // Server-Sent Events MCP connection
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
	Tools  []string           `json:"tools"`  // Available tools mapped by name
	State  MCPConnectionState `json:"state"`  // Connection state
}
