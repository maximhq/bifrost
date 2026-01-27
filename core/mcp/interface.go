//go:build !tinygo && !wasm

package mcp

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

// MCPManagerInterface defines the interface for MCP management functionality.
// This interface allows different implementations (OSS and Enterprise) to be used
// interchangeably in the Bifrost core.
type MCPManagerInterface interface {
	// Tool Operations
	// AddToolsToRequest parses available MCP tools and adds them to the request
	AddToolsToRequest(ctx context.Context, req *schemas.BifrostRequest) *schemas.BifrostRequest

	// GetAvailableTools returns all available MCP tools for the given context
	GetAvailableTools(ctx context.Context) []schemas.ChatTool

	// ExecuteToolCall executes a single tool call and returns the result
	ExecuteToolCall(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error)

	// UpdateToolManagerConfig updates the configuration for the tool manager
	UpdateToolManagerConfig(config *schemas.MCPToolManagerConfig)

	// Agent Mode Operations
	// CheckAndExecuteAgentForChatRequest handles agent mode for Chat Completions API
	CheckAndExecuteAgentForChatRequest(
		ctx *schemas.BifrostContext,
		req *schemas.BifrostChatRequest,
		response *schemas.BifrostChatResponse,
		makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
		executeTool func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error),
	) (*schemas.BifrostChatResponse, *schemas.BifrostError)

	// CheckAndExecuteAgentForResponsesRequest handles agent mode for Responses API
	CheckAndExecuteAgentForResponsesRequest(
		ctx *schemas.BifrostContext,
		req *schemas.BifrostResponsesRequest,
		response *schemas.BifrostResponsesResponse,
		makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
		executeTool func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error),
	) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

	// Client Management
	// GetClients returns all MCP clients
	GetClients() []schemas.MCPClientState

	// AddClient adds a new MCP client with the given configuration
	AddClient(config *schemas.MCPClientConfig) error

	// RemoveClient removes an MCP client by ID
	RemoveClient(id string) error

	// EditClient updates an existing MCP client configuration
	EditClient(id string, updatedConfig *schemas.MCPClientConfig) error

	// ReconnectClient reconnects an MCP client by ID
	ReconnectClient(id string) error

	// Tool Registration
	// RegisterTool registers a local tool with the MCP server
	RegisterTool(name, description string, toolFunction MCPToolFunction[any], toolSchema schemas.ChatTool) error

	// Lifecycle
	// Cleanup performs cleanup of all MCP resources
	Cleanup() error
}

// Ensure MCPManager implements MCPManagerInterface
var _ MCPManagerInterface = (*MCPManager)(nil)
