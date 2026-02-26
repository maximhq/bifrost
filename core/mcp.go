package bifrost

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/mcp/codemode/starlark"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// MCP PUBLIC API

// RegisterMCPTool registers a typed tool handler with the MCP integration.
// This allows developers to easily add custom tools that will be available
// to all LLM requests processed by this Bifrost instance.
//
// Parameters:
//   - name: Unique tool name
//   - description: Human-readable tool description
//   - handler: Function that handles tool execution
//   - toolSchema: Bifrost tool schema for function calling
//
// Returns:
//   - error: Any registration error
//
// Example:
//
//	type EchoArgs struct {
//	    Message string `json:"message"`
//	}
//
//	err := bifrost.RegisterMCPTool("echo", "Echo a message",
//	    func(args EchoArgs) (string, error) {
//	        return args.Message, nil
//	    }, toolSchema)
func (bifrost *Bifrost) RegisterMCPTool(name, description string, handler func(args any) (string, error), toolSchema schemas.ChatTool) error {
	if bifrost.MCPManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.MCPManager.RegisterTool(name, description, handler, toolSchema)
}

// IMPORTANT: Running the MCP client management operations (GetMCPClients, AddMCPClient, RemoveMCPClient, EditMCPClientTools)
// may temporarily increase latency for incoming requests while the operations are being processed.
// These operations involve network I/O and connection management that require mutex locks
// which can block briefly during execution.

// GetMCPClients returns all MCP clients managed by the Bifrost instance.
//
// Returns:
//   - []schemas.MCPClient: List of all MCP clients
//   - error: Any retrieval error
func (bifrost *Bifrost) GetMCPClients() ([]schemas.MCPClient, error) {
	if bifrost.MCPManager == nil {
		return nil, fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	clients := bifrost.MCPManager.GetClients()
	clientsInConfig := make([]schemas.MCPClient, 0, len(clients))

	for _, client := range clients {
		tools := make([]schemas.ChatToolFunction, 0, len(client.ToolMap))
		for _, tool := range client.ToolMap {
			if tool.Function != nil {
				// Create a deep copy (for name) of the tool function to avoid modifying the original
				toolFunction := schemas.ChatToolFunction{}
				toolFunction.Name = tool.Function.Name
				toolFunction.Description = tool.Function.Description
				toolFunction.Parameters = tool.Function.Parameters
				toolFunction.Strict = tool.Function.Strict
				// Remove the client prefix from the tool name
				toolFunction.Name = strings.TrimPrefix(toolFunction.Name, client.ExecutionConfig.Name+"-")
				tools = append(tools, toolFunction)
			}
		}

		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Name < tools[j].Name
		})

		clientsInConfig = append(clientsInConfig, schemas.MCPClient{
			Config: client.ExecutionConfig,
			Tools:  tools,
			State:  client.State,
		})
	}

	return clientsInConfig, nil
}

// GetAvailableTools returns the available tools for the given context.
//
// Returns:
//   - []schemas.ChatTool: List of available tools
func (bifrost *Bifrost) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	if bifrost.MCPManager == nil {
		return nil
	}
	return bifrost.MCPManager.GetAvailableTools(ctx)
}

// AddMCPClient adds a new MCP client to the Bifrost instance.
// This allows for dynamic MCP client management at runtime.
//
// Parameters:
//   - config: MCP client configuration
//
// Returns:
//   - error: Any registration error
//
// Example:
//
//	err := bifrost.AddMCPClient(schemas.MCPClientConfig{
//	    Name: "my-mcp-client",
//	    ConnectionType: schemas.MCPConnectionTypeHTTP,
//	    ConnectionString: &url,
//	})
func (bifrost *Bifrost) AddMCPClient(config *schemas.MCPClientConfig) error {
	if bifrost.MCPManager == nil {
		// Use sync.Once to ensure thread-safe initialization
		bifrost.mcpInitOnce.Do(func() {
			// Initialize with empty config - client will be added via AddClient below
			mcpConfig := schemas.MCPConfig{
				ClientConfigs: []*schemas.MCPClientConfig{},
			}
			// Set up plugin pipeline provider functions for executeCode tool hooks
			mcpConfig.PluginPipelineProvider = func() interface{} {
				return bifrost.getPluginPipeline()
			}
			mcpConfig.ReleasePluginPipeline = func(pipeline interface{}) {
				if pp, ok := pipeline.(*PluginPipeline); ok {
					bifrost.releasePluginPipeline(pp)
				}
			}
			// Create Starlark CodeMode for code execution (with default config)
			codeMode := starlark.NewStarlarkCodeMode(nil, bifrost.logger)
			bifrost.MCPManager = mcp.NewMCPManager(bifrost.ctx, mcpConfig, bifrost.oauth2Provider, bifrost.logger, codeMode)
		})
	}

	// Handle case where initialization succeeded elsewhere but manager is still nil
	if bifrost.MCPManager == nil {
		return fmt.Errorf("MCP manager is not initialized")
	}

	return bifrost.MCPManager.AddClient(config)
}

// RemoveMCPClient removes an MCP client from the Bifrost instance.
// This allows for dynamic MCP client management at runtime.
//
// Parameters:
//   - id: ID of the client to remove
//
// Returns:
//   - error: Any removal error
//
// Example:
//
//	err := bifrost.RemoveMCPClient("my-mcp-client-id")
//	if err != nil {
//	    log.Fatalf("Failed to remove MCP client: %v", err)
//	}
func (bifrost *Bifrost) RemoveMCPClient(id string) error {
	if bifrost.MCPManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.MCPManager.RemoveClient(id)
}

// SetMCPManager sets the MCP manager for this Bifrost instance.
// This allows injecting a custom MCP manager implementation (e.g., for enterprise features).
//
// Parameters:
//   - manager: The MCP manager to set (must implement MCPManagerInterface)
func (bifrost *Bifrost) SetMCPManager(manager mcp.MCPManagerInterface) {
	bifrost.MCPManager = manager
}

// UpdateMCPClient updates the MCP client.
// This allows for dynamic MCP client tool management at runtime.
//
// Parameters:
//   - id: ID of the client to edit
//   - updatedConfig: Updated MCP client configuration
//
// Returns:
//   - error: Any edit error
//
// Example:
//
//	err := bifrost.UpdateMCPClient("my-mcp-client-id", schemas.MCPClientConfig{
//	    Name:           "my-mcp-client-name",
//	    ToolsToExecute: []string{"tool1", "tool2"},
//	})
func (bifrost *Bifrost) UpdateMCPClient(id string, updatedConfig *schemas.MCPClientConfig) error {
	if bifrost.MCPManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.MCPManager.UpdateClient(id, updatedConfig)
}

// ReconnectMCPClient attempts to reconnect an MCP client if it is disconnected.
//
// Parameters:
//   - id: ID of the client to reconnect
//
// Returns:
//   - error: Any reconnection error
func (bifrost *Bifrost) ReconnectMCPClient(id string) error {
	if bifrost.MCPManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	return bifrost.MCPManager.ReconnectClient(id)
}

// UpdateToolManagerConfig updates the tool manager config for the MCP manager.
// This allows for hot-reloading of the tool manager config at runtime.
func (bifrost *Bifrost) UpdateToolManagerConfig(maxAgentDepth int, toolExecutionTimeoutInSeconds int, codeModeBindingLevel string) error {
	if bifrost.MCPManager == nil {
		return fmt.Errorf("MCP is not configured in this Bifrost instance")
	}

	bifrost.MCPManager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        maxAgentDepth,
		ToolExecutionTimeout: time.Duration(toolExecutionTimeoutInSeconds) * time.Second,
		CodeModeBindingLevel: schemas.CodeModeBindingLevel(codeModeBindingLevel),
	})
	return nil
}

// ExecuteChatMCPTool executes an MCP tool call and returns the result as a chat message.
// This is the main public API for manual MCP tool execution in Chat format.
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - *schemas.ChatMessage: Tool message with execution result
//   - *schemas.BifrostError: Any execution error
func (bifrost *Bifrost) ExecuteChatMCPTool(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	// Handle nil context early to prevent issues downstream
	if ctx == nil {
		ctx = bifrost.ctx
	}

	// Validate toolCall is not nil
	if toolCall == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "toolCall cannot be nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ChatCompletionRequest
		return nil, err
	}

	// Get MCP request from pool and populate
	mcpRequest := bifrost.getMCPRequest()
	mcpRequest.RequestType = schemas.MCPRequestTypeChatToolCall
	mcpRequest.ChatAssistantMessageToolCall = toolCall
	defer bifrost.releaseMCPRequest(mcpRequest)

	// Execute with common handler
	result, err := bifrost.handleMCPToolExecution(ctx, mcpRequest, schemas.ChatCompletionRequest)
	if err != nil {
		return nil, err
	}

	// Validate and extract chat message from result
	if result == nil || result.ChatMessage == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "MCP tool execution returned nil chat message"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ChatCompletionRequest
		return nil, err
	}

	return result.ChatMessage, nil
}

// ExecuteResponsesMCPTool executes an MCP tool call and returns the result as a responses message.
// This is the main public API for manual MCP tool execution in Responses format.
//
// Parameters:
//   - ctx: Execution context
//   - toolCall: The tool call to execute (from assistant message)
//
// Returns:
//   - *schemas.ResponsesMessage: Tool message with execution result
//   - *schemas.BifrostError: Any execution error
func (bifrost *Bifrost) ExecuteResponsesMCPTool(ctx *schemas.BifrostContext, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	// Handle nil context early to prevent issues downstream
	if ctx == nil {
		ctx = bifrost.ctx
	}

	// Validate toolCall is not nil
	if toolCall == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "toolCall cannot be nil"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ResponsesRequest
		return nil, err
	}

	// Get MCP request from pool and populate
	mcpRequest := bifrost.getMCPRequest()
	mcpRequest.RequestType = schemas.MCPRequestTypeResponsesToolCall
	mcpRequest.ResponsesToolMessage = toolCall
	defer bifrost.releaseMCPRequest(mcpRequest)

	// Execute with common handler
	result, err := bifrost.handleMCPToolExecution(ctx, mcpRequest, schemas.ResponsesRequest)
	if err != nil {
		return nil, err
	}

	// Validate and extract responses message from result
	if result == nil || result.ResponsesMessage == nil {
		err := schemas.AcquireBifrostError()
		err.IsBifrostError = false
		err.Error.Message = "MCP tool execution returned nil responses message"
		if err.ExtraFields == nil {
			err.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
		}
		err.ExtraFields.RequestType = schemas.ResponsesRequest
		return nil, err
	}

	return result.ResponsesMessage, nil
}
