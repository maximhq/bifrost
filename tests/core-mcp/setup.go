package mcp

import (
	"context"
	"fmt"
	"os"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestTimeout defines the maximum duration for MCP tests
const TestTimeout = 10 * time.Minute

// TestAccount is a minimal account implementation for testing
type TestAccount struct{}

func (a *TestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *TestAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  os.Getenv("OPENAI_API_KEY"),
			Models: []string{},
			Weight: 1.0,
		},
	}, nil
}

func (a *TestAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// setupTestBifrost initializes and returns a Bifrost instance for testing
// This creates a basic Bifrost instance without any MCP clients configured
func setupTestBifrost(ctx context.Context) (*bifrost.Bifrost, error) {
	return setupTestBifrostWithMCPConfig(ctx, &schemas.MCPConfig{
		ClientConfigs: []schemas.MCPClientConfig{},
		ToolManagerConfig: &schemas.MCPToolManagerConfig{
			MaxAgentDepth:        10,
			ToolExecutionTimeout: 30 * time.Second,
		},
		FetchNewRequestIDFunc: func(ctx context.Context) string {
			return "test-request-id"
		},
	})
}

// setupTestBifrostWithCodeMode initializes and returns a Bifrost instance for testing with CodeMode
// This sets up bifrostInternal client as a code mode client
// Note: Tools must be registered first to create the bifrostInternal client
func setupTestBifrostWithCodeMode(ctx context.Context) (*bifrost.Bifrost, error) {
	b, err := setupTestBifrost(ctx)
	if err != nil {
		return nil, err
	}

	// Register tools first to create the bifrostInternal client
	err = registerTestTools(b)
	if err != nil {
		return nil, fmt.Errorf("failed to register test tools: %w", err)
	}

	// Get current client config to preserve existing settings
	clients, err := b.GetMCPClients()
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP clients: %w", err)
	}

	var currentConfig *schemas.MCPClientConfig
	for _, client := range clients {
		if client.Config.ID == "bifrostInternal" {
			currentConfig = &client.Config
			break
		}
	}

	if currentConfig == nil {
		return nil, fmt.Errorf("bifrostInternal client not found")
	}

	// Set bifrostInternal client to code mode and ensure tools are available
	// Preserve existing ToolsToExecute if set, otherwise use wildcard
	toolsToExecute := currentConfig.ToolsToExecute
	if len(toolsToExecute) == 0 {
		toolsToExecute = []string{"*"}
	}

	err = b.EditMCPClient("bifrostInternal", schemas.MCPClientConfig{
		ID:                 currentConfig.ID,
		Name:               currentConfig.Name,
		ConnectionType:     currentConfig.ConnectionType,
		IsCodeModeClient:   true,
		ToolsToExecute:     toolsToExecute,
		ToolsToAutoExecute: currentConfig.ToolsToAutoExecute,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set bifrostInternal client to code mode: %w", err)
	}

	return b, nil
}

// setupTestBifrostWithMCPConfig initializes Bifrost with custom MCP config
func setupTestBifrostWithMCPConfig(ctx context.Context, mcpConfig *schemas.MCPConfig) (*bifrost.Bifrost, error) {
	account := &TestAccount{}

	// Ensure FetchNewRequestIDFunc is set if not provided
	// This is required for the tools handler to be fully setup
	if mcpConfig.FetchNewRequestIDFunc == nil {
		mcpConfig.FetchNewRequestIDFunc = func(ctx context.Context) string {
			return "test-request-id"
		}
	}

	if mcpConfig.ToolManagerConfig == nil {
		mcpConfig.ToolManagerConfig = &schemas.MCPToolManagerConfig{
			MaxAgentDepth:        schemas.DefaultMaxAgentDepth,
			ToolExecutionTimeout: schemas.DefaultToolExecutionTimeout,
		}
	}

	b, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:   account,
		Plugins:   nil,
		Logger:    bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		MCPConfig: mcpConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Bifrost: %w", err)
	}

	return b, nil
}

// registerTestTools registers simple test tools for testing
func registerTestTools(b *bifrost.Bifrost) error {
	// Echo tool
	echoSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "echo",
			Description: schemas.Ptr("Echoes back the input message"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The message to echo",
					},
				},
				Required: []string{"message"},
			},
		},
	}
	if err := b.RegisterMCPTool("echo", "Echoes back the input message", func(args any) (string, error) {
		argsMap, ok := args.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("invalid args type")
		}
		message, ok := argsMap["message"].(string)
		if !ok {
			return "", fmt.Errorf("message field is required")
		}
		return message, nil
	}, echoSchema); err != nil {
		return fmt.Errorf("failed to register echo tool: %w", err)
	}

	// Add tool
	addSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "add",
			Description: schemas.Ptr("Adds two numbers"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &map[string]interface{}{
					"a": map[string]interface{}{
						"type":        "number",
						"description": "First number",
					},
					"b": map[string]interface{}{
						"type":        "number",
						"description": "Second number",
					},
				},
				Required: []string{"a", "b"},
			},
		},
	}
	if err := b.RegisterMCPTool("add", "Adds two numbers", func(args any) (string, error) {
		argsMap, ok := args.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("invalid args type")
		}
		a, ok := argsMap["a"].(float64)
		if !ok {
			return "", fmt.Errorf("a field is required")
		}
		bVal, ok := argsMap["b"].(float64)
		if !ok {
			return "", fmt.Errorf("b field is required")
		}
		return fmt.Sprintf("%.0f", a+bVal), nil
	}, addSchema); err != nil {
		return fmt.Errorf("failed to register add tool: %w", err)
	}

	// Multiply tool
	multiplySchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "multiply",
			Description: schemas.Ptr("Multiplies two numbers"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &map[string]interface{}{
					"a": map[string]interface{}{
						"type":        "number",
						"description": "First number",
					},
					"b": map[string]interface{}{
						"type":        "number",
						"description": "Second number",
					},
				},
				Required: []string{"a", "b"},
			},
		},
	}
	if err := b.RegisterMCPTool("multiply", "Multiplies two numbers", func(args any) (string, error) {
		argsMap, ok := args.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("invalid args type")
		}
		a, ok := argsMap["a"].(float64)
		if !ok {
			return "", fmt.Errorf("a field is required")
		}
		bVal, ok := argsMap["b"].(float64)
		if !ok {
			return "", fmt.Errorf("b field is required")
		}
		return fmt.Sprintf("%.0f", a*bVal), nil
	}, multiplySchema); err != nil {
		return fmt.Errorf("failed to register multiply tool: %w", err)
	}

	// GetData tool - returns structured data
	getDataSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "get_data",
			Description: schemas.Ptr("Returns structured data"),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &map[string]interface{}{},
				Required:   []string{},
			},
		},
	}
	if err := b.RegisterMCPTool("get_data", "Returns structured data", func(args any) (string, error) {
		return `{"items": [{"id": 1, "name": "test"}, {"id": 2, "name": "example"}]}`, nil
	}, getDataSchema); err != nil {
		return fmt.Errorf("failed to register get_data tool: %w", err)
	}

	// ErrorTool - always returns an error
	errorToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "error_tool",
			Description: schemas.Ptr("A tool that always returns an error"),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &map[string]interface{}{},
				Required:   []string{},
			},
		},
	}
	if err := b.RegisterMCPTool("error_tool", "A tool that always returns an error", func(args any) (string, error) {
		return "", fmt.Errorf("this tool always fails")
	}, errorToolSchema); err != nil {
		return fmt.Errorf("failed to register error_tool: %w", err)
	}

	// SlowTool - takes time to execute
	slowToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "slow_tool",
			Description: schemas.Ptr("A tool that takes time to execute"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &map[string]interface{}{
					"delay_ms": map[string]interface{}{
						"type":        "number",
						"description": "Delay in milliseconds",
					},
				},
				Required: []string{"delay_ms"},
			},
		},
	}
	if err := b.RegisterMCPTool("slow_tool", "A tool that takes time to execute", func(args any) (string, error) {
		argsMap, ok := args.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("invalid args type")
		}
		delayMs, ok := argsMap["delay_ms"].(float64)
		if !ok {
			return "", fmt.Errorf("delay_ms field is required")
		}
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
		return fmt.Sprintf("Completed after %v ms", delayMs), nil
	}, slowToolSchema); err != nil {
		return fmt.Errorf("failed to register slow_tool: %w", err)
	}

	// ComplexArgsTool - accepts complex nested arguments
	complexArgsSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "complex_args_tool",
			Description: schemas.Ptr("A tool that accepts complex nested arguments"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &map[string]interface{}{
					"data": map[string]interface{}{
						"type":        "object",
						"description": "Complex nested data",
					},
				},
				Required: []string{"data"},
			},
		},
	}
	if err := b.RegisterMCPTool("complex_args_tool", "A tool that accepts complex nested arguments", func(args any) (string, error) {
		argsMap, ok := args.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("invalid args type")
		}
		data, ok := argsMap["data"]
		if !ok {
			return "", fmt.Errorf("data field is required")
		}
		return fmt.Sprintf("Received data: %v", data), nil
	}, complexArgsSchema); err != nil {
		return fmt.Errorf("failed to register complex_args_tool: %w", err)
	}

	return nil
}

// connectExternalMCP connects to an external MCP server
// This is a helper function that can be used when external MCP credentials are provided
func connectExternalMCP(b *bifrost.Bifrost, name, id, connectionType, connectionString string) error {
	var clientConfig schemas.MCPClientConfig

	switch connectionType {
	case "http":
		clientConfig = schemas.MCPClientConfig{
			ID:               id,
			Name:             name,
			ConnectionType:   schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.Ptr(connectionString),
		}
	case "sse":
		clientConfig = schemas.MCPClientConfig{
			ID:               id,
			Name:             name,
			ConnectionType:   schemas.MCPConnectionTypeSSE,
			ConnectionString: schemas.Ptr(connectionString),
		}
	default:
		return fmt.Errorf("unsupported connection type: %s", connectionType)
	}

	clients, err := b.GetMCPClients()
	if err != nil {
		return fmt.Errorf("failed to get MCP clients: %w", err)
	}
	for _, client := range clients {
		if client.Config.ID == id {
			// Client already exists
			return nil
		}
	}

	if err := b.AddMCPClient(clientConfig); err != nil {
		return fmt.Errorf("failed to add external MCP client: %w", err)
	}

	return nil
}
