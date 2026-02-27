//go:build !tinygo && !wasm

// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/server"
)

// OAuth-related errors
var (
	ErrOAuth2ConfigNotFound       = errors.New("oauth2 config not found")
	ErrOAuth2ProviderNotAvailable = errors.New("oauth2 provider not available")
	ErrOAuth2TokenExpired         = errors.New("oauth2 token expired")
	ErrOAuth2TokenInvalid         = errors.New("oauth2 token invalid")
	ErrOAuth2RefreshFailed        = errors.New("oauth2 token refresh failed")
)

// MCPConfig represents the configuration for MCP integration in Bifrost.
// It enables tool auto-discovery and execution from local and external MCP servers.
type MCPConfig struct {
	ClientConfigs     []*MCPClientConfig    `json:"client_configs,omitempty"`      // Per-client execution configurations
	ToolManagerConfig *MCPToolManagerConfig `json:"tool_manager_config,omitempty"` // MCP tool manager configuration
	ToolSyncInterval  time.Duration         `json:"tool_sync_interval,omitempty"`  // Global default interval for syncing tools from MCP servers (0 = use default 10 min)

	// Function to fetch a new request ID for each tool call result message in agent mode,
	// this is used to ensure that the tool call result messages are unique and can be tracked in plugins or by the user.
	// This id is attached to ctx.Value(schemas.BifrostContextKeyRequestID) in the agent mode.
	// If not provider, same request ID is used for all tool call result messages without any overrides.
	FetchNewRequestIDFunc func(ctx *BifrostContext) string `json:"-"`

	// PluginPipelineProvider returns a plugin pipeline for running MCP plugin hooks.
	// Used when executeCode tool calls nested MCP tools to ensure plugins run for them.
	// The plugin pipeline should be released back to the pool using ReleasePluginPipeline.
	PluginPipelineProvider func() interface{} `json:"-"`

	// ReleasePluginPipeline releases a plugin pipeline back to the pool.
	// This should be called after the plugin pipeline is no longer needed.
	ReleasePluginPipeline func(pipeline interface{}) `json:"-"`
}

type MCPToolManagerConfig struct {
	ToolExecutionTimeout time.Duration        `json:"tool_execution_timeout"`
	MaxAgentDepth        int                  `json:"max_agent_depth"`
	CodeModeBindingLevel CodeModeBindingLevel `json:"code_mode_binding_level,omitempty"` // How tools are exposed in VFS: "server" or "tool"
}

const (
	DefaultMaxAgentDepth        = 10
	DefaultToolExecutionTimeout = 30 * time.Second
)

// CodeModeBindingLevel defines how tools are exposed in the VFS for code execution
type CodeModeBindingLevel string

const (
	CodeModeBindingLevelServer CodeModeBindingLevel = "server"
	CodeModeBindingLevelTool   CodeModeBindingLevel = "tool"
)

// MCPAuthType defines the authentication type for MCP connections
type MCPAuthType string

const (
	MCPAuthTypeNone    MCPAuthType = "none"    // No authentication
	MCPAuthTypeHeaders MCPAuthType = "headers" // Header-based authentication (API keys, etc.)
	MCPAuthTypeOauth   MCPAuthType = "oauth"   // OAuth 2.0 authentication
)

// MCPClientConfig defines tool filtering for an MCP client.
type MCPClientConfig struct {
	ID               string            `json:"client_id"`                          // Client ID
	Name             string            `json:"name"`                        // Client name
	IsCodeModeClient bool              `json:"is_code_mode_client"`         // Whether the client is a code mode client
	ConnectionType   MCPConnectionType `json:"connection_type"`             // How to connect (HTTP, STDIO, SSE, or InProcess)
	ConnectionString *EnvVar           `json:"connection_string,omitempty"` // HTTP or SSE URL (required for HTTP or SSE connections)
	StdioConfig      *MCPStdioConfig   `json:"stdio_config,omitempty"`      // STDIO configuration (required for STDIO connections)
	AuthType         MCPAuthType       `json:"auth_type"`                   // Authentication type (none, headers, or oauth)
	OauthConfigID    *string           `json:"oauth_config_id,omitempty"`   // OAuth config ID (references oauth_configs table)
	State            string            `json:"state,omitempty"`             // Connection state (connected, disconnected, error)
	Headers          map[string]EnvVar `json:"headers,omitempty"`           // Headers to send with the request (for headers auth type)
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
	IsPingAvailable  bool               `json:"is_ping_available"`            // Whether the MCP server supports ping for health checks (default: true). If false, uses listTools for health checks.
	ToolSyncInterval time.Duration      `json:"tool_sync_interval,omitempty"` // Per-client override for tool sync interval (0 = use global, negative = disabled)
	ToolPricing      map[string]float64 `json:"tool_pricing,omitempty"`       // Tool pricing for each tool (cost per execution)
	ConfigHash       string             `json:"-"`                            // Config hash for reconciliation (not serialized)
}

// NewMCPClientConfigFromMap creates a new MCP client config from a map[string]any.
func NewMCPClientConfigFromMap(configMap map[string]any) *MCPClientConfig {
	var config MCPClientConfig
	data, err := sonic.Marshal(configMap)
	if err != nil {
		return nil
	}
	if err := sonic.Unmarshal(data, &config); err != nil {
		return nil
	}
	return &config
}

// HttpHeaders returns the HTTP headers for the MCP client config.
func (c *MCPClientConfig) HttpHeaders(ctx context.Context, oauth2Provider OAuth2Provider) (map[string]string, error) {
	headers := make(map[string]string)

	switch c.AuthType {
	case MCPAuthTypeOauth:
		if c.OauthConfigID == nil {
			return nil, ErrOAuth2ConfigNotFound
		}
		if oauth2Provider == nil {
			return nil, ErrOAuth2ProviderNotAvailable
		}
		accessToken, err := oauth2Provider.GetAccessToken(ctx, *c.OauthConfigID)
		if err != nil {
			return nil, err
		}
		// Validate token format - trim whitespace and check for invalid characters
		accessToken = strings.TrimSpace(accessToken)
		if accessToken == "" {
			return nil, errors.New("access token is empty")
		}
		if strings.ContainsAny(accessToken, "\n\r\t") {
			return nil, errors.New("access token contains invalid characters")
		}
		headers["Authorization"] = "Bearer " + accessToken
	case MCPAuthTypeHeaders:
		for key, value := range c.Headers {
			headers[key] = value.GetValue()
		}
	case MCPAuthTypeNone:
		// No headers to add
	default:
		// Default to headers behavior for backward compatibility
		for key, value := range c.Headers {
			headers[key] = value.GetValue()
		}
	}

	return headers, nil
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
	Name            string                   // Unique name for this client
	Conn            *client.Client           // Active MCP client connection
	ExecutionConfig *MCPClientConfig         // Tool filtering settings
	ToolMap         map[string]ChatTool      // Available tools mapped by name
	ToolNameMapping map[string]string        // Maps sanitized_name -> original_mcp_name (e.g., "notion_search" -> "notion-search")
	ConnectionInfo  *MCPClientConnectionInfo `json:"connection_info"` // Connection metadata for management
	CancelFunc      context.CancelFunc       `json:"-"`               // Cancel function for SSE connections (not serialized)
	State           MCPConnectionState       // Connection state (connected, disconnected, error)
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
	Config *MCPClientConfig   `json:"config"` // Tool filtering settings
	Tools  []ChatToolFunction `json:"tools"`  // Available tools
	State  MCPConnectionState `json:"state"`  // Connection state
}
