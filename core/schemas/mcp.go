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
