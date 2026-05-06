//go:build !tinygo && !wasm

// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/server"
)

// OAuth-related errors
var (
	ErrOAuth2ConfigNotFound           = errors.New("oauth2 config not found")
	ErrOAuth2ProviderNotAvailable     = errors.New("oauth2 provider not available")
	ErrOAuth2TokenExpired             = errors.New("oauth2 token expired")
	ErrOAuth2TokenInvalid             = errors.New("oauth2 token invalid")
	ErrOAuth2RefreshFailed            = errors.New("oauth2 token refresh failed")
	ErrOAuth2NotPerUserSession        = errors.New("state does not match a per-user oauth session")
	ErrOAuth2TokenNotFound            = errors.New("per-user oauth token not found for this identity and mcp server")
	ErrPerUserOAuthPendingFlowExpired = errors.New("per-user oauth pending flow has expired")
)

// MCPUserOAuthRequiredError is returned when a per-user OAuth MCP server requires
// the user to authenticate before tool execution can proceed.
type MCPUserOAuthRequiredError struct {
	MCPClientID   string `json:"mcp_client_id"`
	MCPClientName string `json:"mcp_client_name"`
	AuthorizeURL  string `json:"authorize_url"`
	SessionID     string `json:"session_id"`
	Message       string `json:"message"`
}

func (e *MCPUserOAuthRequiredError) Error() string {
	return e.Message
}

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

// UnmarshalJSON supports Go duration strings (e.g. "10m") for tool_sync_interval.
// Numeric values remain supported for backward compatibility (treated as raw nanoseconds).
func (c *MCPConfig) UnmarshalJSON(data []byte) error {
	type alias MCPConfig
	aux := &struct {
		ToolSyncInterval *json.Number `json:"tool_sync_interval,omitempty"`
		*alias
	}{alias: (*alias)(c)}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(aux); err == nil {
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("trailing JSON data")
		}
		if aux.ToolSyncInterval == nil {
			return nil
		}
		dur, parseErr := parseFlexibleDurationField(*aux.ToolSyncInterval, "tool_sync_interval")
		if parseErr != nil {
			return parseErr
		}
		c.ToolSyncInterval = dur
		return nil
	}

	// Allow Go duration strings while keeping numeric tokens as json.Number.
	auxStr := &struct {
		ToolSyncInterval *string `json:"tool_sync_interval,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, auxStr); err != nil {
		return err
	}
	if auxStr.ToolSyncInterval == nil {
		return nil
	}
	dur, err := parseFlexibleDurationField(*auxStr.ToolSyncInterval, "tool_sync_interval")
	if err != nil {
		return err
	}
	c.ToolSyncInterval = dur
	return nil
}

type MCPToolManagerConfig struct {
	// ToolExecutionTimeout accepts a Go duration string (e.g. "30s", "2m") or an
	// integer nanosecond value for backward compatibility.
	ToolExecutionTimeout  Duration             `json:"tool_execution_timeout"`
	MaxAgentDepth         int                  `json:"max_agent_depth"`
	CodeModeBindingLevel  CodeModeBindingLevel `json:"code_mode_binding_level,omitempty"`  // How tools are exposed in VFS: "server" or "tool"
	DisableAutoToolInject bool                 `json:"disable_auto_tool_inject,omitempty"` // When true, MCP tools are not injected into requests by default
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
	MCPAuthTypeNone         MCPAuthType = "none"           // No authentication
	MCPAuthTypeHeaders      MCPAuthType = "headers"        // Header-based authentication (API keys, etc.)
	MCPAuthTypeOauth        MCPAuthType = "oauth"          // OAuth 2.0 authentication (server-level, admin authenticates once)
	MCPAuthTypePerUserOauth MCPAuthType = "per_user_oauth" // Per-user OAuth 2.0 authentication (each user authenticates individually)
)

// MCPClientConfig defines tool filtering for an MCP client.
type MCPClientConfig struct {
	ID                  string            `json:"client_id"`                       // Client ID
	Name                string            `json:"name"`                            // Client name
	IsCodeModeClient    bool              `json:"is_code_mode_client"`             // Whether the client is a code mode client
	ConnectionType      MCPConnectionType `json:"connection_type"`                 // How to connect (HTTP, STDIO, SSE, or InProcess)
	ConnectionString    *EnvVar           `json:"connection_string,omitempty"`     // HTTP or SSE URL (required for HTTP or SSE connections)
	StdioConfig         *MCPStdioConfig   `json:"stdio_config,omitempty"`          // STDIO configuration (required for STDIO connections)
	AuthType            MCPAuthType       `json:"auth_type"`                       // Authentication type (none, headers, or oauth)
	OauthConfigID       *string           `json:"oauth_config_id,omitempty"`       // OAuth config ID (references oauth_configs table)
	OauthClientID       *EnvVar           `json:"oauth_client_id,omitempty"`       // Redacted OAuth client ID (populated on GET, not stored here)
	OauthClientSecret   *EnvVar           `json:"oauth_client_secret,omitempty"`   // Redacted OAuth client secret (populated on GET, not stored here)
	State               string            `json:"state,omitempty"`                 // Connection state (connected, disconnected, error)
	Headers             map[string]EnvVar `json:"headers,omitempty"`               // Headers to send with the request (for headers auth type)
	AllowedExtraHeaders WhiteList         `json:"allowed_extra_headers,omitempty"` // Allowlist of request-level headers that callers may forward to this MCP server at execution time
	InProcessServer     *server.MCPServer `json:"-"`                               // MCP server instance for in-process connections (Go package only)
	ToolsToExecute      WhiteList         `json:"tools_to_execute,omitempty"`      // Include-only list.
	// ToolsToExecute semantics:
	// - ["*"] => all tools are included
	// - []    => no tools are included (deny-by-default)
	// - nil/omitted => treated as [] (no tools)
	// - ["tool1", "tool2"] => include only the specified tools
	ToolsToAutoExecute WhiteList `json:"tools_to_auto_execute,omitempty"` // Auto-execute list.
	// ToolsToAutoExecute semantics:
	// - ["*"] => all tools are auto-executed
	// - []    => no tools are auto-executed (deny-by-default)
	// - nil/omitted => treated as [] (no tools)
	// - ["tool1", "tool2"] => auto-execute only the specified tools
	// Note: If a tool is in ToolsToAutoExecute but not in ToolsToExecute, it will be skipped.
	IsPingAvailable       *bool              `json:"is_ping_available,omitempty"`  // Whether the MCP server supports ping for health checks (nil/true = ping; false = listTools). Defaults to true.
	ToolSyncInterval      time.Duration      `json:"tool_sync_interval,omitempty"` // Per-client override for tool sync interval (0 = use global, negative = disabled)
	ToolPricing           map[string]float64 `json:"tool_pricing,omitempty"`       // Tool pricing for each tool (cost per execution)
	Disabled              bool               `json:"disabled"`                     // Whether the client is intentionally disabled (stops connection and workers)
	ConfigHash            string             `json:"-"`                            // Config hash for reconciliation (not serialized)
	AllowOnAllVirtualKeys bool               `json:"allow_on_all_virtual_keys"`    // Whether to allow the MCP client to run on all virtual keys

	// Discovered tools for per-user OAuth clients (persisted so they survive restart)
	DiscoveredTools           map[string]ChatTool `json:"-"` // Discovered tool schemas keyed by prefixed name
	DiscoveredToolNameMapping map[string]string   `json:"-"` // Mapping from sanitized tool names to original MCP names
}

// UnmarshalJSON supports Go duration strings (e.g. "10m") for tool_sync_interval.
// Numeric values remain supported for backward compatibility (treated as raw nanoseconds).
func (c *MCPClientConfig) UnmarshalJSON(data []byte) error {
	type alias MCPClientConfig
	aux := &struct {
		ToolSyncInterval *json.Number `json:"tool_sync_interval,omitempty"`
		*alias
	}{alias: (*alias)(c)}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(aux); err == nil {
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("trailing JSON data")
		}
		if aux.ToolSyncInterval == nil {
			return nil
		}
		dur, parseErr := parseFlexibleDurationField(*aux.ToolSyncInterval, "tool_sync_interval")
		if parseErr != nil {
			return parseErr
		}
		c.ToolSyncInterval = dur
		return nil
	}

	// Allow Go duration strings while keeping numeric tokens as json.Number.
	auxStr := &struct {
		ToolSyncInterval *string `json:"tool_sync_interval,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, auxStr); err != nil {
		return err
	}
	if auxStr.ToolSyncInterval == nil {
		return nil
	}
	dur, err := parseFlexibleDurationField(*auxStr.ToolSyncInterval, "tool_sync_interval")
	if err != nil {
		return err
	}
	c.ToolSyncInterval = dur
	return nil
}

func parseFlexibleDurationField(v any, fieldName string) (time.Duration, error) {
	switch t := v.(type) {
	case string:
		d, err := time.ParseDuration(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("invalid %s duration %q: %w", fieldName, t, err)
		}
		return d, nil
	case json.Number:
		raw := strings.TrimSpace(t.String())
		if raw == "" {
			return 0, fmt.Errorf("invalid %s: empty numeric value", fieldName)
		}
		if strings.Contains(raw, ".") {
			return 0, fmt.Errorf("invalid %s value %q: fractional numeric values are not allowed; use an integer nanosecond value or a duration string like \"10m\"", fieldName, raw)
		}

		// Keep parity with JavaScript-safe integer range for config interchange.
		const maxSafeJSONInt int64 = 9007199254740991
		const minSafeJSONInt int64 = -9007199254740991

		var ns int64
		if strings.ContainsAny(raw, "eE") {
			rat := new(big.Rat)
			if _, ok := rat.SetString(raw); !ok {
				return 0, fmt.Errorf("invalid %s value %q: expected an integer nanosecond value", fieldName, raw)
			}
			if rat.Denom().Cmp(big.NewInt(1)) != 0 {
				return 0, fmt.Errorf("invalid %s value %q: fractional numeric values are not allowed; use an integer nanosecond value or a duration string like \"10m\"", fieldName, raw)
			}
			if !rat.Num().IsInt64() {
				return 0, fmt.Errorf("invalid %s value %q: out of int64 range for nanoseconds", fieldName, raw)
			}
			ns = rat.Num().Int64()
		} else {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid %s value %q: expected an integer nanosecond value", fieldName, raw)
			}
			ns = parsed
		}

		if ns < minSafeJSONInt || ns > maxSafeJSONInt {
			return 0, fmt.Errorf("invalid %s value %q: exceeds safe integer range", fieldName, raw)
		}
		return time.Duration(ns), nil
	default:
		return 0, fmt.Errorf("invalid %s type %T: expected duration string (e.g. \"10m\") or number", fieldName, v)
	}
}

// NewMCPClientConfigFromMap creates a new MCP client config from a map[string]any.
func NewMCPClientConfigFromMap(configMap map[string]any) *MCPClientConfig {
	var config MCPClientConfig
	data, err := MarshalSorted(configMap)
	if err != nil {
		return nil
	}
	if err := Unmarshal(data, &config); err != nil {
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
	case MCPAuthTypePerUserOauth:
		// Per-user OAuth: headers are injected per-call in executeToolInternal, not at connection level
		return headers, nil
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
	MCPConnectionStateConnected    MCPConnectionState = "connected"     // Client is connected and ready to use
	MCPConnectionStateDisconnected MCPConnectionState = "disconnected"  // Client is not connected
	MCPConnectionStateError        MCPConnectionState = "error"         // Client is in an error state, and cannot be used
	MCPConnectionStatePendingTools MCPConnectionState = "pending_tools" // Connected but tools not yet populated
	MCPConnectionStateDisabled     MCPConnectionState = "disabled"      // Client is intentionally disabled by the user
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
