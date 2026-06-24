//go:build !tinygo && !wasm

package mcp

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

// MCPManagerInterface defines the interface for MCP management functionality.
// This interface allows different implementations to be used interchangeably
// in the Bifrost core.
type MCPManagerInterface interface {
	// Tool Operations
	// AddToolsToRequest parses available MCP tools and adds them to the request
	AddToolsToRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostRequest

	// GetAvailableTools returns all available MCP tools for the given context
	GetAvailableTools(ctx *schemas.BifrostContext) []schemas.ChatTool

	// UpdateToolManagerConfig updates the configuration for the tool manager.
	// DisableAutoToolInject in the config controls auto injection — pass the
	// current value whenever only other fields change so it is never silently reset.
	UpdateToolManagerConfig(config *schemas.MCPToolManagerConfig)

	// Agent Mode Operations
	// CheckAndExecuteAgentForChatRequest handles agent mode for Chat Completions API.
	// Tool executions inside the agent loop go through the plugin gate internally —
	// callers no longer inject an executeTool function.
	CheckAndExecuteAgentForChatRequest(
		ctx *schemas.BifrostContext,
		req *schemas.BifrostChatRequest,
		response *schemas.BifrostChatResponse,
		makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
	) (*schemas.BifrostChatResponse, *schemas.BifrostError)

	// CheckAndExecuteAgentForResponsesRequest handles agent mode for Responses API.
	// Tool executions inside the agent loop go through the plugin gate internally.
	CheckAndExecuteAgentForResponsesRequest(
		ctx *schemas.BifrostContext,
		req *schemas.BifrostResponsesRequest,
		response *schemas.BifrostResponsesResponse,
		makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
	) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

	// ExecuteChatTool / ExecuteResponsesTool run a single MCP tool call through the
	// plugin gate and return the result in the appropriate API format. Bifrost's
	// ExecuteChatMCPTool / ExecuteResponsesMCPTool delegate here.
	ExecuteChatTool(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError)
	ExecuteResponsesTool(ctx *schemas.BifrostContext, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError)

	// Client Management
	// GetClients returns all MCP clients
	GetClients() []schemas.MCPClientState

	// AddClient adds a new MCP client with the given configuration
	AddClient(ctx context.Context, config *schemas.MCPClientConfig) error

	// ConnectConfiguredClients dials all clients supplied at construction time.
	// Construction no longer connects; call this once all plugins are registered so
	// PreMCPConnectionHook sees the full plugin set.
	ConnectConfiguredClients(ctx context.Context)

	// RemoveClient removes an MCP client by ID
	RemoveClient(id string) error

	// UpdateClient updates an existing MCP client configuration
	UpdateClient(id string, updatedConfig *schemas.MCPClientConfig) error

	// UpdateClientConnection reconnects an existing MCP client using updated
	// auth-related connection fields (for example, headers and OAuth config).
	UpdateClientConnection(id string, newConfig *schemas.MCPClientConfig) error

	// ReconnectClient reconnects an MCP client by ID
	ReconnectClient(id string) error

	// DisableClient shuts down a client's connection and workers without removing it
	DisableClient(id string) error

	// EnableClient reconnects a disabled client and restarts its workers
	EnableClient(id string) error

	// VerifyHeadersConnection creates a temporary MCP connection using a set of
	// caller-supplied header values to verify connectivity and discover tools.
	// The connection is closed after verification.
	VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (map[string]schemas.ChatTool, map[string]string, error)

	// VerifyPerUserOAuthConnection creates a temporary MCP connection using a
	// test access token to verify connectivity and discover tools. The connection
	// is closed after verification.
	VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (map[string]schemas.ChatTool, map[string]string, error)

	// SetClientTools updates the tool map and name mapping for an existing client.
	SetClientTools(clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string)

	// Tool Registration
	// RegisterTool registers a local tool with the MCP server
	RegisterTool(name, description string, toolFunction MCPToolFunction[any], toolSchema schemas.ChatTool) error

	// Lifecycle
	// Cleanup performs cleanup of all MCP resources
	Cleanup() error
}

// Ensure MCPManager implements MCPManagerInterface
var _ MCPManagerInterface = (*MCPManager)(nil)
