package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	mcpcore "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// GLOBAL MCP SERVER PATHS
// =============================================================================

var (
	// Global paths to MCP server binaries (initialized once)
	mcpServerPaths struct {
		TemperatureServer  string
		GoTestServer       string
		EdgeCaseServer     string
		ParallelTestServer string
		ErrorTestServer    string
		BifrostRoot        string
		ExamplesRoot       string
	}
)

// InitMCPServerPaths initializes the global MCP server paths
// Call this in tests that need STDIO MCP servers
func InitMCPServerPaths(t *testing.T) {
	if mcpServerPaths.BifrostRoot != "" {
		return // Already initialized
	}

	bifrostRoot := GetBifrostRoot(t)
	examplesRoot := filepath.Join(bifrostRoot, "..", "examples")

	mcpServerPaths.BifrostRoot = bifrostRoot
	mcpServerPaths.ExamplesRoot = examplesRoot
	mcpServerPaths.TemperatureServer = filepath.Join(examplesRoot, "mcps", "temperature", "dist", "index.js")
	mcpServerPaths.GoTestServer = filepath.Join(examplesRoot, "mcps", "go-test-server", "bin", "go-test-server")
	mcpServerPaths.EdgeCaseServer = filepath.Join(examplesRoot, "mcps", "edge-case-server", "bin", "edge-case-server")
	mcpServerPaths.ParallelTestServer = filepath.Join(examplesRoot, "mcps", "parallel-test-server", "bin", "parallel-test-server")
	mcpServerPaths.ErrorTestServer = filepath.Join(examplesRoot, "mcps", "error-test-server", "bin", "error-test-server")

	t.Logf("Initialized MCP server paths:")
	t.Logf("  - Bifrost Root: %s", mcpServerPaths.BifrostRoot)
	t.Logf("  - Examples Root: %s", mcpServerPaths.ExamplesRoot)
	t.Logf("  - Temperature: %s", mcpServerPaths.TemperatureServer)
	t.Logf("  - GoTest: %s", mcpServerPaths.GoTestServer)
	t.Logf("  - EdgeCase: %s", mcpServerPaths.EdgeCaseServer)
	t.Logf("  - ParallelTest: %s", mcpServerPaths.ParallelTestServer)
	t.Logf("  - ErrorTest: %s", mcpServerPaths.ErrorTestServer)
}

// =============================================================================
// SAMPLE TOOL DEFINITIONS
// =============================================================================

// GetSampleCalculatorTool returns a sample calculator tool definition
func GetSampleCalculatorTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "calculator",
			Description: schemas.Ptr("Performs basic arithmetic operations (add, subtract, multiply, divide)"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"operation": map[string]interface{}{
						"type":        "string",
						"description": "The operation to perform",
						"enum":        []string{"add", "subtract", "multiply", "divide"},
					},
					"x": map[string]interface{}{
						"type":        "number",
						"description": "First number",
					},
					"y": map[string]interface{}{
						"type":        "number",
						"description": "Second number",
					},
				},
				Required: []string{"operation", "x", "y"},
			},
		},
	}
}

// GetSampleEchoTool returns a sample echo tool definition
func GetSampleEchoTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "echo",
			Description: schemas.Ptr("Echoes back the input message"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The message to echo",
					},
				},
				Required: []string{"message"},
			},
		},
	}
}

// GetSampleWeatherTool returns a sample weather tool definition
func GetSampleWeatherTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "get_weather",
			Description: schemas.Ptr("Gets the current weather for a location"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"location": map[string]interface{}{
						"type":        "string",
						"description": "The location to get weather for",
					},
					"units": map[string]interface{}{
						"type":        "string",
						"description": "Temperature units (celsius or fahrenheit)",
						"enum":        []string{"celsius", "fahrenheit"},
					},
				},
				Required: []string{"location"},
			},
		},
	}
}

// GetSampleDelayTool returns a sample delay tool for timeout testing
func GetSampleDelayTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "delay",
			Description: schemas.Ptr("Delays execution for a specified number of seconds"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"seconds": map[string]interface{}{
						"type":        "number",
						"description": "Number of seconds to delay",
					},
				},
				Required: []string{"seconds"},
			},
		},
	}
}

// GetSampleErrorTool returns a sample error tool for error testing
func GetSampleErrorTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "throw_error",
			Description: schemas.Ptr("Throws an error for testing error handling"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"error_message": map[string]interface{}{
						"type":        "string",
						"description": "The error message to throw",
					},
				},
				Required: []string{"error_message"},
			},
		},
	}
}

// =============================================================================
// SAMPLE CHAT MESSAGES
// =============================================================================

// GetSampleUserMessage returns a sample user message
func GetSampleUserMessage(content string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}
}

// GetSampleAssistantMessage returns a sample assistant message
func GetSampleAssistantMessage(content string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}
}

// GetSampleToolCallMessage returns a sample message with tool calls
func GetSampleToolCallMessage(toolCalls []schemas.ChatAssistantMessageToolCall) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		ChatAssistantMessage: &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		},
	}
}

// GetSampleToolResultMessage returns a sample tool result message
func GetSampleToolResultMessage(toolCallID, content string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: &toolCallID,
		},
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}
}

// GetSampleCalculatorToolCall returns a sample calculator tool call
func GetSampleCalculatorToolCall(id string, operation string, x, y float64) schemas.ChatAssistantMessageToolCall {
	argsMap := map[string]interface{}{
		"operation": operation,
		"x":         x,
		"y":         y,
	}
	argsJSON, _ := json.Marshal(argsMap)

	return schemas.ChatAssistantMessageToolCall{
		ID:   &id,
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-calculator"),
			Arguments: string(argsJSON),
		},
	}
}

// GetSampleEchoToolCall returns a sample echo tool call
func GetSampleEchoToolCall(id string, message string) schemas.ChatAssistantMessageToolCall {
	argsMap := map[string]interface{}{
		"message": message,
	}
	argsJSON, _ := json.Marshal(argsMap)

	return schemas.ChatAssistantMessageToolCall{
		ID:   &id,
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-echo"),
			Arguments: string(argsJSON),
		},
	}
}

// GetSampleWeatherToolCall returns a sample weather tool call
func GetSampleWeatherToolCall(id string, location string, units string) schemas.ChatAssistantMessageToolCall {
	argsMap := map[string]interface{}{
		"location": location,
	}
	if units != "" {
		argsMap["units"] = units
	}
	argsJSON, _ := json.Marshal(argsMap)

	return schemas.ChatAssistantMessageToolCall{
		ID:   &id,
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-get_weather"),
			Arguments: string(argsJSON),
		},
	}
}

// GetSampleDelayToolCall returns a sample delay tool call
func GetSampleDelayToolCall(id string, seconds float64) schemas.ChatAssistantMessageToolCall {
	argsMap := map[string]interface{}{
		"seconds": seconds,
	}
	argsJSON, _ := json.Marshal(argsMap)

	return schemas.ChatAssistantMessageToolCall{
		ID:   &id,
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-delay"),
			Arguments: string(argsJSON),
		},
	}
}

// =============================================================================
// INPROCESS TOOL REGISTRATION HELPERS
// =============================================================================

// RegisterEchoTool registers a simple echo tool for testing
func RegisterEchoTool(manager *mcp.MCPManager) error {
	echoToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "echo",
			Description: schemas.Ptr("Echoes back the input message"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The message to echo back",
					},
				},
				Required: []string{"message"},
			},
		},
	}

	return manager.RegisterTool(
		"echo",
		"Echoes back the input message",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}
			message, ok := argsMap["message"].(string)
			if !ok {
				return "", fmt.Errorf("message must be a string")
			}
			result := map[string]interface{}{
				"echoed": message,
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		echoToolSchema,
	)
}

// RegisterCalculatorTool registers a calculator tool for testing
func RegisterCalculatorTool(manager *mcp.MCPManager) error {
	calculatorToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "calculator",
			Description: schemas.Ptr("Performs basic arithmetic operations"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"operation": map[string]interface{}{
						"type":        "string",
						"description": "The operation to perform (add, subtract, multiply, divide)",
						"enum":        []string{"add", "subtract", "multiply", "divide"},
					},
					"x": map[string]interface{}{
						"type":        "number",
						"description": "First number",
					},
					"y": map[string]interface{}{
						"type":        "number",
						"description": "Second number",
					},
				},
				Required: []string{"operation", "x", "y"},
			},
		},
	}

	return manager.RegisterTool(
		"calculator",
		"Performs basic arithmetic operations",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			operation, ok := argsMap["operation"].(string)
			if !ok {
				return "", fmt.Errorf("operation must be a string")
			}

			x, ok := argsMap["x"].(float64)
			if !ok {
				return "", fmt.Errorf("x must be a number")
			}

			y, ok := argsMap["y"].(float64)
			if !ok {
				return "", fmt.Errorf("y must be a number")
			}

			var result float64
			switch operation {
			case "add":
				result = x + y
			case "subtract":
				result = x - y
			case "multiply":
				result = x * y
			case "divide":
				if y == 0 {
					return "", fmt.Errorf("division by zero")
				}
				result = x / y
			default:
				return "", fmt.Errorf("unknown operation: %s", operation)
			}

			resultMap := map[string]interface{}{
				"result": result,
			}
			resultJSON, _ := json.Marshal(resultMap)
			return string(resultJSON), nil
		},
		calculatorToolSchema,
	)
}

// RegisterWeatherTool registers a mock weather tool for testing
func RegisterWeatherTool(manager *mcp.MCPManager) error {
	weatherToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "get_weather",
			Description: schemas.Ptr("Gets the current weather for a location"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"location": map[string]interface{}{
						"type":        "string",
						"description": "The city and state, e.g. San Francisco, CA",
					},
					"units": map[string]interface{}{
						"type":        "string",
						"description": "The temperature unit (celsius or fahrenheit)",
						"enum":        []string{"celsius", "fahrenheit"},
					},
				},
				Required: []string{"location"},
			},
		},
	}

	return manager.RegisterTool(
		"get_weather",
		"Gets the current weather for a location",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			location, ok := argsMap["location"].(string)
			if !ok {
				return "", fmt.Errorf("location must be a string")
			}

			units := "fahrenheit"
			if u, ok := argsMap["units"].(string); ok {
				units = u
			}

			// Return mock weather data
			result := map[string]interface{}{
				"location":    location,
				"temperature": 72,
				"units":       units,
				"conditions":  "sunny",
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		weatherToolSchema,
	)
}

// RegisterSearchTool registers a mock search tool for testing
func RegisterSearchTool(manager *mcp.MCPManager) error {
	searchToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "search",
			Description: schemas.Ptr("Searches for information on a topic"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "The search query",
					},
					"max_results": map[string]interface{}{
						"type":        "number",
						"description": "Maximum number of results to return",
					},
				},
				Required: []string{"query"},
			},
		},
	}

	return manager.RegisterTool(
		"search",
		"Searches for information on a topic",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			query, ok := argsMap["query"].(string)
			if !ok {
				return "", fmt.Errorf("query must be a string")
			}

			maxResults := 5.0
			if m, ok := argsMap["max_results"].(float64); ok {
				maxResults = m
			}

			// Return mock search results
			result := map[string]interface{}{
				"query":   query,
				"results": []string{"Result 1 for " + query, "Result 2 for " + query},
				"count":   int(maxResults),
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		searchToolSchema,
	)
}

// RegisterGetTemperatureTool registers a mock temperature tool (same name as STDIO server for conflict testing)
func RegisterGetTemperatureTool(manager *mcp.MCPManager) error {
	getTemperatureToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "get_temperature",
			Description: schemas.Ptr("Get the current temperature for a popular city"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"location": map[string]interface{}{
						"type":        "string",
						"description": "The name of the city (e.g., 'New York', 'London', 'Tokyo')",
					},
				},
				Required: []string{"location"},
			},
		},
	}

	return manager.RegisterTool(
		"get_temperature",
		"Get the current temperature for a popular city",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			location, ok := argsMap["location"].(string)
			if !ok {
				return "", fmt.Errorf("location must be a string")
			}

			// Return mock temperature data (InProcess version - different from STDIO)
			result := map[string]interface{}{
				"location":    location,
				"temperature": 68,
				"unit":        "F",
				"condition":   "InProcess Mock Data",
				"source":      "bifrostInternal",
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		getTemperatureToolSchema,
	)
}

// RegisterGetTimeTool registers a tool that returns current time info
func RegisterGetTimeTool(manager *mcp.MCPManager) error {
	getTimeToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "get_time",
			Description: schemas.Ptr("Gets the current date and time"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"timezone": map[string]interface{}{
						"type":        "string",
						"description": "The timezone (e.g., UTC, America/New_York)",
					},
				},
			},
		},
	}

	return manager.RegisterTool(
		"get_time",
		"Gets the current date and time",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			timezone := "UTC"
			if ok {
				if tz, ok := argsMap["timezone"].(string); ok {
					timezone = tz
				}
			}

			// Return mock time data
			result := map[string]interface{}{
				"timezone": timezone,
				"datetime": "2024-01-15T10:30:00Z",
				"unix":     1705317000,
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		getTimeToolSchema,
	)
}

// RegisterReadFileTool registers a mock file reading tool for testing
func RegisterReadFileTool(manager *mcp.MCPManager) error {
	readFileToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "read_file",
			Description: schemas.Ptr("Reads the contents of a file"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "The file path to read",
					},
				},
				Required: []string{"path"},
			},
		},
	}

	return manager.RegisterTool(
		"read_file",
		"Reads the contents of a file",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			path, ok := argsMap["path"].(string)
			if !ok {
				return "", fmt.Errorf("path must be a string")
			}

			// Return mock file contents
			result := map[string]interface{}{
				"path":     path,
				"content":  "Mock file contents for " + path,
				"encoding": "utf-8",
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		readFileToolSchema,
	)
}

// RegisterDelayTool registers a delay tool that sleeps for specified seconds
func RegisterDelayTool(manager *mcp.MCPManager) error {
	delayToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "delay",
			Description: schemas.Ptr("Delays execution for specified seconds"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"seconds": map[string]interface{}{
						"type":        "number",
						"description": "Number of seconds to delay",
					},
				},
				Required: []string{"seconds"},
			},
		},
	}

	return manager.RegisterTool(
		"delay",
		"Delays execution for specified seconds",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			seconds, ok := argsMap["seconds"].(float64)
			if !ok {
				return "", fmt.Errorf("seconds must be a number")
			}

			// Sleep for the specified duration
			time.Sleep(time.Duration(seconds*1000) * time.Millisecond)

			result := map[string]interface{}{
				"delayed_seconds": seconds,
				"message":         fmt.Sprintf("Delayed for %.2f seconds", seconds),
			}
			resultJSON, _ := json.Marshal(result)
			return string(resultJSON), nil
		},
		delayToolSchema,
	)
}

// RegisterThrowErrorTool registers a tool that always throws an error
func RegisterThrowErrorTool(manager *mcp.MCPManager) error {
	throwErrorToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "throw_error",
			Description: schemas.Ptr("Throws an error with specified message"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: &schemas.OrderedMap{
					"error_message": map[string]interface{}{
						"type":        "string",
						"description": "The error message to throw",
					},
				},
				Required: []string{"error_message"},
			},
		},
	}

	return manager.RegisterTool(
		"throw_error",
		"Throws an error with specified message",
		func(args any) (string, error) {
			argsMap, ok := args.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("invalid arguments type")
			}

			errorMessage, ok := argsMap["error_message"].(string)
			if !ok {
				return "", fmt.Errorf("error_message must be a string")
			}

			// Return the error as requested
			return "", fmt.Errorf("%s", errorMessage)
		},
		throwErrorToolSchema,
	)
}

// SetInternalClientAutoExecute configures which tools should be auto-executed for the internal Bifrost client
func SetInternalClientAutoExecute(manager *mcp.MCPManager, toolNames []string) error {
	// Get the current internal client config
	clients := manager.GetClients()

	// Find the internal client
	var internalClient *schemas.MCPClientState
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "bifrostInternal" {
			internalClient = &clients[i]
			break
		}
	}

	if internalClient == nil {
		return fmt.Errorf("internal bifrost client not found")
	}

	// Update the ToolsToAutoExecute field
	internalClient.ExecutionConfig.ToolsToAutoExecute = toolNames

	// Apply the updated config
	return manager.EditClient(internalClient.ExecutionConfig.ID, internalClient.ExecutionConfig)
}

// SetInternalClientAsCodeMode configures the internal Bifrost client as a CodeMode client
func SetInternalClientAsCodeMode(manager *mcp.MCPManager, toolsToExecute []string) error {
	// Get the current internal client config
	clients := manager.GetClients()

	// Find the internal client
	var internalClient *schemas.MCPClientState
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "bifrostInternal" {
			internalClient = &clients[i]
			break
		}
	}

	if internalClient == nil {
		return fmt.Errorf("internal bifrost client not found")
	}

	// Update the config
	internalClient.ExecutionConfig.IsCodeModeClient = true
	internalClient.ExecutionConfig.ToolsToExecute = toolsToExecute

	// Apply the updated config
	return manager.EditClient(internalClient.ExecutionConfig.ID, internalClient.ExecutionConfig)
}

// =============================================================================
// SAMPLE RESPONSES API MESSAGES
// =============================================================================

// GetSampleResponsesUserMessage returns a sample Responses API user message
func GetSampleResponsesUserMessage(content string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: &content,
		},
	}
}

// GetSampleResponsesAssistantMessage returns a sample Responses API assistant message
func GetSampleResponsesAssistantMessage(content string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: &content,
		},
	}
}

// GetSampleResponsesToolCallMessage returns a sample Responses API tool call
func GetSampleResponsesToolCallMessage(callID, toolName string, args map[string]interface{}) schemas.ResponsesMessage {
	argsJSON, _ := json.Marshal(args)
	argsStr := string(argsJSON)

	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    &callID,
			Name:      &toolName,
			Arguments: &argsStr,
		},
	}
}

// GetSampleResponsesToolResultMessage returns a sample Responses API tool result
func GetSampleResponsesToolResultMessage(callID, output string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &callID,
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: &output,
			},
		},
	}
}

// =============================================================================
// SAMPLE MCP CLIENT CONFIGURATIONS
// =============================================================================

// GetSampleHTTPClientConfig returns a sample HTTP client configuration
func GetSampleHTTPClientConfig(serverURL string) schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:                 "test-http-client",
		Name:               "TestHTTPServer",
		ConnectionType:     schemas.MCPConnectionTypeHTTP,
		ConnectionString:   schemas.NewEnvVar(serverURL),
		ToolsToExecute:     []string{"*"}, // Allow all tools
		ToolsToAutoExecute: []string{},    // No auto-execute by default
	}
}

// GetSampleSSEClientConfig returns a sample SSE client configuration
func GetSampleSSEClientConfig(serverURL string) schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:                 "test-sse-client",
		Name:               "TestSSEServer",
		ConnectionType:     schemas.MCPConnectionTypeSSE,
		ConnectionString:   schemas.NewEnvVar(serverURL),
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
	}
}

// GetSampleSTDIOClientConfig returns a sample STDIO client configuration
func GetSampleSTDIOClientConfig(command string, args []string) schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:             "test-stdio-client",
		Name:           "TestSTDIOServer",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: command,
			Args:    args,
		},
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
	}
}

// GetSampleInProcessClientConfig returns a sample InProcess client configuration
func GetSampleInProcessClientConfig() schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:                 "test-inprocess-client",
		Name:               "TestInProcessServer",
		ConnectionType:     schemas.MCPConnectionTypeInProcess,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
	}
}

// GetTemperatureMCPClientConfig returns a STDIO client configuration for the temperature MCP server
// located in examples/mcps/temperature. This requires the temperature server to be built first.
// The path is relative to the bifrost root directory.
func GetTemperatureMCPClientConfig(bifrostRoot string) schemas.MCPClientConfig {
	// Use global path if available, otherwise fall back to parameter
	serverPath := mcpServerPaths.TemperatureServer
	if serverPath == "" {
		serverPath = bifrostRoot + "/examples/mcps/temperature-server/dist/index.js"
	}

	return schemas.MCPClientConfig{
		ID:             "temperature-mcp-client",
		Name:           "TemperatureMCPServer",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "node",
			Args:    []string{serverPath},
		},
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
	}
}

// GetGoTestServerConfig returns a STDIO client configuration for the go-test-server
// located in examples/mcps/go-test-server. Provides tools for string manipulation,
// JSON validation, UUID generation, hashing, and encoding/decoding.
// The server must be built first using: go build -o bin/go-test-server
func GetGoTestServerConfig(bifrostRoot string) schemas.MCPClientConfig {
	// Use global path if available, otherwise fall back to parameter
	serverPath := mcpServerPaths.GoTestServer
	if serverPath == "" {
		serverPath = bifrostRoot + "/../examples/mcps/go-test-server/bin/go-test-server"
	}

	return schemas.MCPClientConfig{
		ID:             "go-test-server",
		Name:           "GoTestServer",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: serverPath,
			Args:    []string{},
		},
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
		IsCodeModeClient:   true, // CodeMode enabled for testing
	}
}

// GetEdgeCaseServerConfig returns a STDIO client configuration for the edge-case-server
// located in examples/mcps/edge-case-server. Provides tools for testing edge cases
// like unicode, binary data, large payloads, nested structures, null values, and special characters.
// The server must be built first using: go build -o bin/edge-case-server
func GetEdgeCaseServerConfig(bifrostRoot string) schemas.MCPClientConfig {
	// Use global path if available, otherwise fall back to parameter
	serverPath := mcpServerPaths.EdgeCaseServer
	if serverPath == "" {
		serverPath = bifrostRoot + "/../examples/mcps/edge-case-server/bin/edge-case-server"
	}

	return schemas.MCPClientConfig{
		ID:             "edge-case-server",
		Name:           "EdgeCaseServer",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: serverPath,
			Args:    []string{},
		},
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
		IsCodeModeClient:   true, // CodeMode enabled for testing
	}
}

// GetErrorTestServerConfig returns a STDIO client configuration for the error-test-server
// located in examples/mcps/error-test-server. Provides tools for testing error scenarios
// including timeouts, malformed JSON, various error types, intermittent failures, and memory intensive operations.
// The server must be built first using: go build -o bin/error-test-server
func GetErrorTestServerConfig(bifrostRoot string) schemas.MCPClientConfig {
	// Use global path if available, otherwise fall back to parameter
	serverPath := mcpServerPaths.ErrorTestServer
	if serverPath == "" {
		serverPath = bifrostRoot + "/../examples/mcps/error-test-server/bin/error-test-server"
	}

	return schemas.MCPClientConfig{
		ID:             "error-test-server",
		Name:           "ErrorTestServer",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: serverPath,
			Args:    []string{},
		},
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
		IsCodeModeClient:   true, // CodeMode enabled for testing
	}
}

// GetParallelTestServerConfig returns a STDIO client configuration for the parallel-test-server
// located in examples/mcps/parallel-test-server. Provides tools with different execution times
// for testing parallel execution and timing behavior (fast, medium, slow, very slow operations).
// The server must be built first using: go build -o bin/parallel-test-server
func GetParallelTestServerConfig(bifrostRoot string) schemas.MCPClientConfig {
	// Use global path if available, otherwise fall back to parameter
	serverPath := mcpServerPaths.ParallelTestServer
	if serverPath == "" {
		serverPath = bifrostRoot + "/../examples/mcps/parallel-test-server/bin/parallel-test-server"
	}

	return schemas.MCPClientConfig{
		ID:             "parallel-test-server",
		Name:           "ParallelTestServer",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: serverPath,
			Args:    []string{},
		},
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
		IsCodeModeClient:   true, // CodeMode enabled for testing
	}
}

// GetBifrostRoot returns the bifrost root directory by walking up from the current directory
func GetBifrostRoot(t *testing.T) string {
	// Start from current working directory
	cwd, err := os.Getwd()
	require.NoError(t, err, "should get current working directory")

	// Walk up the directory tree to find the bifrost root (contains go.mod with module github.com/maximhq/bifrost)
	dir := cwd
	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			// Found go.mod, this is likely the bifrost root
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding go.mod
			t.Fatal("could not find bifrost root (go.mod not found)")
		}
		dir = parent
	}
}

// GetSampleCodeModeClientConfig returns a sample code mode client configuration
// with headers applied from test config
func GetSampleCodeModeClientConfig(t *testing.T, serverURL string) schemas.MCPClientConfig {
	t.Helper()
	config := schemas.MCPClientConfig{
		ID:                 "test-codemode-client",
		Name:               "TestCodeModeServer",
		ConnectionType:     schemas.MCPConnectionTypeHTTP,
		ConnectionString:   schemas.NewEnvVar(serverURL),
		IsCodeModeClient:   true,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{},
	}
	applyTestConfigHeaders(t, &config)
	return config
}

// =============================================================================
// FILTERING TEST SCENARIOS
// =============================================================================

// FilteringScenario represents a test scenario for tool filtering
type FilteringScenario struct {
	Name             string
	ConfigTools      []string // ToolsToExecute in config
	ContextTools     []string // Tools in context override
	RequestedTool    string   // Tool being requested
	ShouldExecute    bool     // Expected result
	ExpectedBehavior string   // Description of expected behavior
}

// GetActualToolNameFromServer gets the actual tool name from the HTTP server
// Returns the first tool available that matches the filter pattern
func GetActualToolNameFromServer(t *testing.T, clientName string) string {
	t.Helper()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	if len(clients) == 0 {
		t.Fatal("No MCP clients available")
	}

	client := clients[0]
	if len(client.ToolMap) == 0 {
		t.Fatal("No tools available from server")
	}

	// Return the first tool name
	for toolName := range client.ToolMap {
		return toolName
	}

	t.Fatal("No tools found")
	return ""
}

// GetActualToolNamesFromServer gets multiple actual tool names from the HTTP server
func GetActualToolNamesFromServer(t *testing.T, count int) []string {
	t.Helper()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	clientConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	manager := setupMCPManager(t, clientConfig)

	clients := manager.GetClients()
	if len(clients) == 0 {
		t.Fatal("No MCP clients available")
	}

	client := clients[0]
	if len(client.ToolMap) < count {
		t.Fatalf("Expected at least %d tools, got %d", count, len(client.ToolMap))
	}

	tools := make([]string, 0, count)
	for toolName := range client.ToolMap {
		tools = append(tools, toolName)
		if len(tools) >= count {
			break
		}
	}

	return tools
}

// GetFilteringScenarios returns comprehensive filtering test scenarios
func GetFilteringScenarios() []FilteringScenario {
	return []FilteringScenario{
		// Nil config scenarios
		{
			Name:             "config_nil_context_nil",
			ConfigTools:      nil,
			ContextTools:     nil,
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    false,
			ExpectedBehavior: "nil config defaults to deny-all",
		},
		{
			Name:             "config_nil_context_tool1",
			ConfigTools:      nil,
			ContextTools:     []string{"bifrostInternal-echo"},
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    true,
			ExpectedBehavior: "context overrides nil config",
		},
		{
			Name:             "config_nil_context_wildcard",
			ConfigTools:      nil,
			ContextTools:     []string{"*"},
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    true,
			ExpectedBehavior: "context wildcard overrides nil config",
		},

		// Empty array scenarios
		{
			Name:             "config_empty_context_nil",
			ConfigTools:      []string{},
			ContextTools:     nil,
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    false,
			ExpectedBehavior: "empty config denies all",
		},
		{
			Name:             "config_empty_context_tool1",
			ConfigTools:      []string{},
			ContextTools:     []string{"bifrostInternal-echo"},
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    true,
			ExpectedBehavior: "context overrides empty config",
		},

		// Wildcard scenarios
		{
			Name:             "config_wildcard_context_nil",
			ConfigTools:      []string{"*"},
			ContextTools:     nil,
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    true,
			ExpectedBehavior: "wildcard allows all",
		},
		{
			Name:             "config_wildcard_context_tool1",
			ConfigTools:      []string{"*"},
			ContextTools:     []string{"bifrostInternal-echo"},
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    true,
			ExpectedBehavior: "context restricts wildcard config",
		},
		{
			Name:             "config_wildcard_context_tool2",
			ConfigTools:      []string{"*"},
			ContextTools:     []string{"bifrostInternal-echo"},
			RequestedTool:    "bifrostInternal-calculator",
			ShouldExecute:    false,
			ExpectedBehavior: "context filters out calculator despite wildcard config",
		},

		// Explicit list scenarios
		{
			Name:             "config_tool1_context_nil",
			ConfigTools:      []string{"echo"},
			ContextTools:     nil,
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    true,
			ExpectedBehavior: "config allows echo",
		},
		{
			Name:             "config_tool1_context_nil_request_tool2",
			ConfigTools:      []string{"echo"},
			ContextTools:     nil,
			RequestedTool:    "bifrostInternal-calculator",
			ShouldExecute:    false,
			ExpectedBehavior: "config denies calculator",
		},
		{
			Name:             "config_tool1_tool2_context_tool2",
			ConfigTools:      []string{"echo", "calculator"},
			ContextTools:     []string{"bifrostInternal-calculator"},
			RequestedTool:    "bifrostInternal-calculator",
			ShouldExecute:    true,
			ExpectedBehavior: "context and config both allow calculator",
		},
		{
			Name:             "config_tool1_tool2_context_tool2_request_tool1",
			ConfigTools:      []string{"echo", "calculator"},
			ContextTools:     []string{"bifrostInternal-calculator"},
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    false,
			ExpectedBehavior: "context filters out echo despite config allowing it",
		},

		// Complex scenarios
		{
			Name:             "config_tool1_context_wildcard",
			ConfigTools:      []string{"echo"},
			ContextTools:     []string{"*"},
			RequestedTool:    "bifrostInternal-calculator",
			ShouldExecute:    false,
			ExpectedBehavior: "config is more restrictive than context wildcard",
		},
		{
			Name:             "config_wildcard_context_empty",
			ConfigTools:      []string{"*"},
			ContextTools:     []string{},
			RequestedTool:    "bifrostInternal-echo",
			ShouldExecute:    false,
			ExpectedBehavior: "empty context overrides wildcard config",
		},
	}
}

// =============================================================================
// AUTO-EXECUTE FILTERING SCENARIOS
// =============================================================================

// AutoExecuteScenario represents a test scenario for auto-execute filtering
type AutoExecuteScenario struct {
	Name               string
	ToolsToExecute     []string
	ToolsToAutoExecute []string
	RequestedTool      string
	ShouldAllowExecute bool // Can execute at all
	ShouldAutoExecute  bool // Should auto-execute in agent mode
	ExpectedBehavior   string
}

// GetAutoExecuteScenarios returns comprehensive auto-execute test scenarios
func GetAutoExecuteScenarios() []AutoExecuteScenario {
	return []AutoExecuteScenario{
		{
			Name:               "in_both_lists",
			ToolsToExecute:     []string{"YOUTUBE_SEARCH_YOU_TUBE", "YOUTUBE_VIDEO_DETAILS"},
			ToolsToAutoExecute: []string{"YOUTUBE_SEARCH_YOU_TUBE"},
			RequestedTool:      "YOUTUBE_SEARCH_YOU_TUBE",
			ShouldAllowExecute: true,
			ShouldAutoExecute:  true,
			ExpectedBehavior:   "tool in both lists should auto-execute",
		},
		{
			Name:               "in_execute_not_auto",
			ToolsToExecute:     []string{"YOUTUBE_SEARCH_YOU_TUBE", "YOUTUBE_VIDEO_DETAILS"},
			ToolsToAutoExecute: []string{},
			RequestedTool:      "YOUTUBE_SEARCH_YOU_TUBE",
			ShouldAllowExecute: true,
			ShouldAutoExecute:  false,
			ExpectedBehavior:   "tool allowed but not auto-execute",
		},
		{
			Name:               "in_auto_not_execute",
			ToolsToExecute:     []string{"YOUTUBE_SEARCH_YOU_TUBE"},
			ToolsToAutoExecute: []string{"YOUTUBE_VIDEO_DETAILS"},
			RequestedTool:      "YOUTUBE_VIDEO_DETAILS",
			ShouldAllowExecute: false,
			ShouldAutoExecute:  false,
			ExpectedBehavior:   "tool must be in execute list to work",
		},
		{
			Name:               "wildcard_execute_specific_auto",
			ToolsToExecute:     []string{"*"},
			ToolsToAutoExecute: []string{"YOUTUBE_SEARCH_YOU_TUBE"},
			RequestedTool:      "YOUTUBE_SEARCH_YOU_TUBE",
			ShouldAllowExecute: true,
			ShouldAutoExecute:  true,
			ExpectedBehavior:   "wildcard execute + specific auto works",
		},
		{
			Name:               "wildcard_execute_no_auto",
			ToolsToExecute:     []string{"*"},
			ToolsToAutoExecute: []string{},
			RequestedTool:      "YOUTUBE_SEARCH_YOU_TUBE",
			ShouldAllowExecute: true,
			ShouldAutoExecute:  false,
			ExpectedBehavior:   "wildcard execute without auto-execute",
		},
		{
			Name:               "wildcard_both",
			ToolsToExecute:     []string{"*"},
			ToolsToAutoExecute: []string{"*"},
			RequestedTool:      "YOUTUBE_SEARCH_YOU_TUBE",
			ShouldAllowExecute: true,
			ShouldAutoExecute:  true,
			ExpectedBehavior:   "wildcard in both lists allows all to auto-execute",
		},
	}
}

// =============================================================================
// ENVIRONMENT VARIABLES
// =============================================================================

const (
	// MCP Server URLs from environment
	EnvMCPHTTPServerURL = "MCP_HTTP_URL"
	EnvMCPSSEServerURL  = "MCP_SSE_URL"
	EnvMCPHTTPHeaders   = "MCP_HTTP_HEADERS" // JSON string of headers, e.g. {"Authorization":"Bearer token"}
	EnvMCPSSEHeaders    = "MCP_SSE_HEADERS"  // JSON string of headers, e.g. {"Authorization":"Bearer token"}

	// Bifrost API configuration
	EnvBifrostAPIKey       = "OPENAI_API_KEY"
	EnvBifrostTestProvider = "BIFROST_TEST_PROVIDER"
	EnvBifrostTestModel    = "BIFROST_TEST_MODEL"

	// Default values
	DefaultTestProvider = "openai"
	DefaultTestModel    = "gpt-4o"
)

// =============================================================================
// BIFROST SETUP
// =============================================================================

// testAccount is a minimal account implementation for MCP tests
type testAccount struct{}

func (a *testAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *testAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	// Get API key directly from environment (can't use GetTestConfig here as it's called from goroutines)
	apiKey := os.Getenv(EnvBifrostAPIKey)
	if apiKey == "" {
		return []schemas.Key{}, nil
	}
	return []schemas.Key{
		{
			Value:  *schemas.NewEnvVar(apiKey),
			Models: []string{}, // Empty means all models
			Weight: 1.0,
		},
	}, nil
}

func (a *testAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if providerKey == schemas.OpenAI {
		// Return default config for OpenAI
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	}
	return nil, fmt.Errorf("provider %s not supported", providerKey)
}

// setupBifrost creates a Bifrost instance for testing
func setupBifrost(t *testing.T) *bifrost.Bifrost {
	t.Helper()

	account := &testAccount{}

	// Create bifrost instance
	bifrostInstance, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err, "failed to create bifrost instance")

	// Cleanup
	t.Cleanup(func() {
		bifrostInstance.Shutdown()
	})

	return bifrostInstance
}

// setupMCPManager creates an MCP manager for testing
func setupMCPManager(t *testing.T, clientConfigs ...schemas.MCPClientConfig) *mcpcore.MCPManager {
	t.Helper()

	// Create MCP config
	mcpConfig := &schemas.MCPConfig{
		ClientConfigs: clientConfigs,
	}

	// Create MCP manager
	logger := &testLogger{t: t}
	manager := mcpcore.NewMCPManager(context.Background(), *mcpConfig, nil, logger)

	// Cleanup
	t.Cleanup(func() {
		// Remove all clients
		clients := manager.GetClients()
		for _, client := range clients {
			_ = manager.RemoveClient(client.ExecutionConfig.ID)
		}
	})

	return manager
}

// =============================================================================
// TEST CONFIGURATION
// =============================================================================

// TestConfig holds configuration for test execution
type TestConfig struct {
	HTTPServerURL string
	HTTPHeaders   map[string]schemas.EnvVar
	SSEServerURL  string
	SSEHeaders    map[string]schemas.EnvVar
	APIKey        string
	Provider      schemas.ModelProvider
	Model         string
	UseRealLLM    bool
	MaxRetries    int
	RetryDelay    time.Duration
}

// Global test configuration (initialized once)
var config *TestConfig
var configOnce sync.Once

// GetTestConfig loads configuration from environment variables
func GetTestConfig(t *testing.T) *TestConfig {
	t.Helper()

	// Initialize config once
	configOnce.Do(func() {
		config = loadTestConfig()
	})

	return config
}

// loadTestConfig loads the actual configuration
func loadTestConfig() *TestConfig {
	// Parse HTTP headers from environment variable
	// The EnvVar type has a custom UnmarshalJSON that handles both simple strings
	// and the full EnvVar schema: {"value": "...", "env_var": "...", "from_env": false}
	httpHeaders := make(map[string]schemas.EnvVar)
	if headersJSON := os.Getenv(EnvMCPHTTPHeaders); headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &httpHeaders); err != nil {
			// Log error but continue - headers are optional
			fmt.Fprintf(os.Stderr, "Warning: Failed to parse MCP_HTTP_HEADERS: %v\n", err)
		}
	}

	// Parse SSE headers from environment variable
	sseHeaders := make(map[string]schemas.EnvVar)
	if headersJSON := os.Getenv(EnvMCPSSEHeaders); headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &sseHeaders); err != nil {
			// Log error but continue - headers are optional
			fmt.Fprintf(os.Stderr, "Warning: Failed to parse MCP_SSE_HEADERS: %v\n", err)
		}
	}

	testConfig := &TestConfig{
		HTTPServerURL: os.Getenv(EnvMCPHTTPServerURL),
		HTTPHeaders:   httpHeaders,
		SSEServerURL:  os.Getenv(EnvMCPSSEServerURL),
		SSEHeaders:    sseHeaders,
		APIKey:        os.Getenv(EnvBifrostAPIKey),
		Provider:      schemas.ModelProvider(getEnvOrDefault(EnvBifrostTestProvider, DefaultTestProvider)),
		Model:         getEnvOrDefault(EnvBifrostTestModel, DefaultTestModel),
		UseRealLLM:    os.Getenv(EnvBifrostAPIKey) != "",
		MaxRetries:    3,
		RetryDelay:    time.Second,
	}

	return testConfig
}

// getEnvOrDefault returns environment variable value or default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// applyTestConfigHeaders applies headers from TestConfig to client config if available
func applyTestConfigHeaders(t *testing.T, clientConfig *schemas.MCPClientConfig) {
	t.Helper()
	config := GetTestConfig(t)

	// Apply HTTP headers if this is an HTTP connection and headers are configured
	if clientConfig.ConnectionType == schemas.MCPConnectionTypeHTTP && len(config.HTTPHeaders) > 0 {
		if clientConfig.Headers == nil {
			clientConfig.Headers = make(map[string]schemas.EnvVar)
		}
		for key, value := range config.HTTPHeaders {
			clientConfig.Headers[key] = value
		}
	}

	// Apply SSE headers if this is an SSE connection and headers are configured
	if clientConfig.ConnectionType == schemas.MCPConnectionTypeSSE && len(config.SSEHeaders) > 0 {
		if clientConfig.Headers == nil {
			clientConfig.Headers = make(map[string]schemas.EnvVar)
		}
		for key, value := range config.SSEHeaders {
			clientConfig.Headers[key] = value
		}
	}
}

// =============================================================================
// ASSERTION HELPERS
// =============================================================================

// AssertToolResponse asserts that a tool response is valid
func AssertToolResponse(t *testing.T, resp *schemas.BifrostMCPResponse, expectedContent string) {
	t.Helper()
	require.NotNil(t, resp, "response should not be nil")

	// Check Chat format
	if resp.ChatMessage != nil {
		assert.Equal(t, schemas.ChatMessageRoleTool, resp.ChatMessage.Role)
		if resp.ChatMessage.Content != nil && resp.ChatMessage.Content.ContentStr != nil {
			assert.Contains(t, *resp.ChatMessage.Content.ContentStr, expectedContent)
		}
	}

	// Check Responses format
	if resp.ResponsesMessage != nil {
		assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *resp.ResponsesMessage.Type)
		if resp.ResponsesMessage.ResponsesToolMessage != nil && resp.ResponsesMessage.ResponsesToolMessage.Output != nil {
			if resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				assert.Contains(t, *resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, expectedContent)
			}
		}
	}
}

// AssertToolExecuted asserts that a tool was successfully executed
func AssertToolExecuted(t *testing.T, resp *schemas.BifrostMCPResponse, err error) {
	t.Helper()
	require.NoError(t, err, "tool execution should not error")
	require.NotNil(t, resp, "tool response should not be nil")
}

// AssertToolNotExecuted asserts that a tool execution failed
func AssertToolNotExecuted(t *testing.T, err error, expectedErrorSubstring string) {
	t.Helper()
	require.Error(t, err, "tool execution should error")
	assert.Contains(t, err.Error(), expectedErrorSubstring)
}

// AssertClientState asserts that a client is in the expected state
func AssertClientState(t *testing.T, clients []schemas.MCPClientState, clientID string, expectedState schemas.MCPConnectionState) {
	t.Helper()

	found := false
	for _, client := range clients {
		if client.ExecutionConfig.ID == clientID {
			found = true
			assert.Equal(t, expectedState, client.State, "client %s should be in state %s", clientID, expectedState)
			break
		}
	}

	require.True(t, found, "client %s not found", clientID)
}

// AssertPluginCalled asserts that a plugin hook was called
func AssertPluginCalled(t *testing.T, plugin *TestLoggingPlugin, expectedCalls int) {
	t.Helper()
	assert.Equal(t, expectedCalls, plugin.GetPreHookCallCount(), "plugin should be called expected number of times")
}

// =============================================================================
// CODE MODE AGENT HELPERS
// =============================================================================

// GetSampleCodeModeAgentClientConfig returns code mode client configured for agent mode
// with headers applied from test config
func GetSampleCodeModeAgentClientConfig(t *testing.T, serverURL string) schemas.MCPClientConfig {
	t.Helper()
	config := schemas.MCPClientConfig{
		ID:                 "test-codemode-client",
		Name:               "TestCodeModeServer",
		ConnectionType:     schemas.MCPConnectionTypeHTTP,
		ConnectionString:   schemas.NewEnvVar(serverURL),
		IsCodeModeClient:   true,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: []string{"executeToolCode", "listToolFiles", "readToolFile"},
	}
	applyTestConfigHeaders(t, &config)
	return config
}

// GetSampleHTTPClientConfigNoSpaces returns HTTP client config without spaces in name (for agent tests)
func GetSampleHTTPClientConfigNoSpaces(serverURL string) schemas.MCPClientConfig {
	return schemas.MCPClientConfig{
		ID:                 "test-http-client",
		Name:               "TestHTTPServer",
		ConnectionType:     schemas.MCPConnectionTypeHTTP,
		ConnectionString:   schemas.NewEnvVar(serverURL),
		ToolsToExecute:     []string{"*"}, // Allow all tools
		ToolsToAutoExecute: []string{},    // No auto-execute by default
	}
}

// CreateExecuteToolCodeCall creates executeToolCode tool call for testing
func CreateExecuteToolCodeCall(callID string, code string) schemas.ChatAssistantMessageToolCall {
	// JSON escape the code string
	codeJSON, _ := json.Marshal(code)
	return schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr(callID),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, string(codeJSON)),
		},
	}
}

// CreateExecuteToolCodeCallResponses creates executeToolCode tool call for Responses API
func CreateExecuteToolCodeCallResponses(callID string, code string) schemas.ResponsesToolMessage {
	codeJSON, _ := json.Marshal(code)
	return schemas.ResponsesToolMessage{
		CallID:    schemas.Ptr(callID),
		Name:      schemas.Ptr("executeToolCode"),
		Arguments: schemas.Ptr(fmt.Sprintf(`{"code": %s}`, string(codeJSON))),
	}
}

// =============================================================================
// ENHANCED ASSERTION HELPERS
// =============================================================================

// AssertCodeExecutionSuccess asserts that code execution completed successfully
func AssertCodeExecutionSuccess(t *testing.T, result *schemas.ChatMessage, expectedOutputContains string) {
	t.Helper()
	require.NotNil(t, result, "result should not be nil")
	require.NotNil(t, result.Content, "result content should not be nil")
	require.NotNil(t, result.Content.ContentStr, "result content string should not be nil")

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// returnValue IS the result, not wrapped in {"result": ...}
	assert.NotNil(t, returnValue, "execution should return a value")

	if returnValue != nil && expectedOutputContains != "" {
		resultStr := fmt.Sprintf("%v", returnValue)
		assert.Contains(t, resultStr, expectedOutputContains, "result should contain expected output")
	}
}

// AssertCodeExecutionError asserts that code execution failed with an error
// Note: This checks if the return value contains an error field, not if ParseCodeModeResponse returned an error
func AssertCodeExecutionError(t *testing.T, result *schemas.ChatMessage, expectedErrorContains string) {
	t.Helper()
	require.NotNil(t, result, "result should not be nil")
	require.NotNil(t, result.Content, "result content should not be nil")
	require.NotNil(t, result.Content.ContentStr, "result content string should not be nil")

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	// If ParseCodeModeResponse itself returned an error, that's also an execution error
	if hasError {
		if expectedErrorContains != "" {
			assert.Contains(t, errorMsg, expectedErrorContains, "error message should contain expected text")
		}
		return
	}

	// Check if return value contains an error field (e.g., from try/catch in code)
	if returnValue != nil {
		if returnObj, ok := returnValue.(map[string]interface{}); ok {
			if errorField, hasErrorField := returnObj["error"]; hasErrorField {
				if expectedErrorContains != "" {
					errorStr := fmt.Sprintf("%v", errorField)
					assert.Contains(t, errorStr, expectedErrorContains, "error should contain expected message")
				}
				return
			}
		}
	}

	// If we get here, there's no error - this assertion should fail
	t.Errorf("Expected code execution error but none was found")
}

// AssertToolResponseContains asserts that tool response contains expected text
func AssertToolResponseContains(t *testing.T, resp *schemas.BifrostMCPResponse, expectedText string) {
	t.Helper()
	require.NotNil(t, resp, "response should not be nil")

	found := false

	// Check Chat format
	if resp.ChatMessage != nil && resp.ChatMessage.Content != nil && resp.ChatMessage.Content.ContentStr != nil {
		if assert.Contains(t, *resp.ChatMessage.Content.ContentStr, expectedText) {
			found = true
		}
	}

	// Check Responses format
	if resp.ResponsesMessage != nil && resp.ResponsesMessage.ResponsesToolMessage != nil &&
		resp.ResponsesMessage.ResponsesToolMessage.Output != nil &&
		resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
		if assert.Contains(t, *resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, expectedText) {
			found = true
		}
	}

	assert.True(t, found, "response should contain expected text in at least one format")
}

// AssertBifrostErrorContains asserts that bifrost error contains expected message
func AssertBifrostErrorContains(t *testing.T, bifrostErr *schemas.BifrostError, expectedMessage string) {
	t.Helper()
	require.NotNil(t, bifrostErr, "bifrost error should not be nil")
	require.NotNil(t, bifrostErr.Error, "bifrost error.Error should not be nil")
	assert.Contains(t, bifrostErr.Error.Message, expectedMessage, "error message should contain expected text")
}

// AssertToolCallExtracted asserts that tool calls are correctly extracted from code
func AssertToolCallExtracted(t *testing.T, code string, expectedServerName string, expectedToolName string) {
	t.Helper()

	// This is a basic check - the actual extraction is done by the MCP system
	// We just verify the code contains the expected pattern
	expectedPattern := fmt.Sprintf("%s.%s", expectedServerName, expectedToolName)
	assert.Contains(t, code, expectedPattern, "code should contain tool call pattern")
}

// AssertResponseHasToolCalls asserts that response has tool calls
func AssertResponseHasToolCalls(t *testing.T, resp *schemas.BifrostChatResponse, expectedCount int) {
	t.Helper()
	require.NotNil(t, resp, "response should not be nil")
	require.NotEmpty(t, resp.Choices, "response should have choices")

	choice := resp.Choices[0]
	if choice.ChatNonStreamResponseChoice != nil &&
		choice.ChatNonStreamResponseChoice.Message != nil &&
		choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil {
		toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
		assert.Len(t, toolCalls, expectedCount, "should have expected number of tool calls")
	}
}

// AssertAgentCompletedSuccessfully asserts that agent completed without errors
func AssertAgentCompletedSuccessfully(t *testing.T, resp *schemas.BifrostChatResponse, bifrostErr *schemas.BifrostError) {
	t.Helper()
	if bifrostErr != nil && bifrostErr.Error != nil {
		fmt.Println("bifrostErr", bifrostErr.Error.Message)
	}
	assert.Nil(t, bifrostErr, "agent should complete without error")
	require.NotNil(t, resp, "agent should return response")
	require.NotEmpty(t, resp.Choices, "agent response should have choices")
}

// =============================================================================
// TEST DATA GENERATORS
// =============================================================================

// GenerateRandomToolName generates a random tool name for testing
func GenerateRandomToolName(prefix string) string {
	return fmt.Sprintf("%s_tool_%d", prefix, time.Now().UnixNano())
}

// GenerateInvalidJSON returns various malformed JSON strings for testing
func GenerateInvalidJSON() []string {
	return []string{
		`{`,                                // Missing closing brace
		`{"key": "value"`,                  // Missing closing brace
		`{"key": }`,                        // Missing value
		`{key: "value"}`,                   // Unquoted key
		`{"key": "value",}`,                // Trailing comma
		`{"key": undefined}`,               // Invalid value
		`{'key': 'value'}`,                 // Single quotes
		`{"key": "value"}}`,                // Extra closing brace
		``,                                 // Empty string
		`null`,                             // Null value
		`[1, 2, 3]`,                        // Array instead of object
		`{"key": "value\nwith\nnewlines"}`, // Unescaped newlines
	}
}

// GenerateValidCode generates valid TypeScript/JavaScript code for testing
func GenerateValidCode(codeType string) string {
	switch codeType {
	case "simple_return":
		return "return 42"
	case "string_return":
		return `return "Hello, World!"`
	case "calculation":
		return "const x = 10; const y = 20; return x + y"
	case "object_return":
		return `return { status: "success", value: 42 }`
	case "array_return":
		return `return [1, 2, 3, 4, 5]`
	case "with_console_log":
		return `console.log("test"); return "done"`
	case "async_operation":
		return `const result = await Promise.resolve(42); return result`
	default:
		return "return 'default'"
	}
}

// GenerateInvalidCode generates invalid TypeScript/JavaScript code for testing
func GenerateInvalidCode(errorType string) string {
	switch errorType {
	case "syntax_error":
		return "const x = "
	case "missing_semicolon":
		return "const x = 10 const y = 20"
	case "unclosed_brace":
		return "function foo() { return 42"
	case "unclosed_bracket":
		return "const arr = [1, 2, 3"
	case "invalid_keyword":
		return "const 123invalid = 'value'"
	case "runtime_error":
		return "throw new Error('test error')"
	case "undefined_variable":
		return "return undefinedVariable"
	case "null_reference":
		return "const x = null; return x.property"
	default:
		return "return invalid syntax {"
	}
}

// GeneratePathTraversalAttempts generates various path traversal attack strings
func GeneratePathTraversalAttempts() []string {
	return []string{
		"../../../etc/passwd.d.ts",
		"servers/../../secrets.d.ts",
		"servers/../../../etc/passwd.d.ts",
		"..\\..\\..\\windows\\system32\\config\\sam.d.ts",
		"servers/test/../../../etc.d.ts",
		"servers/test/../../other.d.ts",
		"/etc/passwd.d.ts",
		"C:\\Windows\\System32\\config\\sam.d.ts",
		"servers/test\x00hidden/file.d.ts", // Null byte injection
		"servers/test%00hidden/file.d.ts",  // URL encoded null byte
	}
}

// GenerateUnicodeStrings generates various unicode strings for testing
func GenerateUnicodeStrings() []string {
	return []string{
		"Hello 世界",
		"Привет мир",
		"مرحبا بالعالم",
		"🌍🌎🌏",
		"Test™️®️©️",
		"Ñoño Çáféº",
		"עברית",
		"日本語テスト",
		"\u0000\u0001\u0002", // Control characters
	}
}

// =============================================================================
// TIMING HELPERS
// =============================================================================

// MeasureExecutionTime measures execution time of a function
func MeasureExecutionTime(t *testing.T, name string, fn func()) time.Duration {
	t.Helper()
	start := time.Now()
	fn()
	duration := time.Since(start)
	t.Logf("%s took %v", name, duration)
	return duration
}

// AssertExecutionTimeUnder asserts that execution completes within expected time
func AssertExecutionTimeUnder(t *testing.T, fn func(), maxDuration time.Duration, operationName string) {
	t.Helper()
	start := time.Now()
	fn()
	duration := time.Since(start)
	assert.LessOrEqual(t, duration, maxDuration, "%s should complete within %v, took %v", operationName, maxDuration, duration)
}

// =============================================================================
// CONTEXT HELPERS
// =============================================================================

// CreateTestContextWithMCPFilter creates a test context with MCP filtering
func CreateTestContextWithMCPFilter(includeClients []string, includeTools []string) *schemas.BifrostContext {
	baseCtx := context.Background()
	if includeClients != nil {
		baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, includeClients)
	}
	if includeTools != nil {
		baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, includeTools)
	}
	return schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)
}

// CreateTestContextWithTimeout creates a test context with custom timeout
func CreateTestContextWithCustomTimeout(timeout time.Duration) (*schemas.BifrostContext, context.CancelFunc) {
	baseCtx, cancel := context.WithTimeout(context.Background(), timeout)
	return schemas.NewBifrostContext(baseCtx, schemas.NoDeadline), cancel
}

// =============================================================================
// JSON HELPERS
// =============================================================================

// MustMarshalJSON marshals value to JSON or fails test
func MustMarshalJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err, "should marshal to JSON")
	return string(b)
}

// MustUnmarshalJSON unmarshals JSON to value or fails test
func MustUnmarshalJSON(t *testing.T, data string, v interface{}) {
	t.Helper()
	err := json.Unmarshal([]byte(data), v)
	require.NoError(t, err, "should unmarshal from JSON")
}

// ParseCodeModeResponse parses the text response from executeToolCode and extracts the return value.
// The response format is:
//
//	[Console output: ...]
//	Execution completed successfully.
//	Return value: <JSON>
//	Environment: ...
//
// OR for errors:
//
//	Execution runtime error:
//	<error message>
//	...
func ParseCodeModeResponse(t *testing.T, responseText string) (returnValue interface{}, hasError bool, errorMsg string) {
	t.Helper()

	t.Logf("Response text: %s", responseText)

	// Check for execution failure indicators
	if strings.Contains(responseText, "Execution failed:") || strings.Contains(responseText, "Execution runtime error:") || strings.Contains(responseText, "Execution validation error:") {
		return nil, true, responseText
	}

	// Find "Return value:" and extract everything after it until "Environment:"
	returnValueIdx := strings.Index(responseText, "Return value:")
	if returnValueIdx == -1 {
		// No return value found - check if execution completed without return
		if strings.Contains(responseText, "Execution completed successfully") {
			return nil, false, ""
		}
		return nil, true, "No return value found in response"
	}

	// Extract JSON starting after "Return value: "
	startIdx := returnValueIdx + len("Return value:")
	jsonStr := responseText[startIdx:]

	// Find the end - look for "\n\nEnvironment:" or end of string
	endIdx := strings.Index(jsonStr, "\n\nEnvironment:")
	if endIdx != -1 {
		jsonStr = jsonStr[:endIdx]
	}

	// Trim whitespace
	jsonStr = strings.TrimSpace(jsonStr)

	fmt.Println("returning json value from ParseCodeModeResponse:", jsonStr)

	// Parse the JSON return value
	var result interface{}
	err := json.Unmarshal([]byte(jsonStr), &result)
	if err != nil {
		return nil, true, fmt.Sprintf("Failed to parse return value JSON: %v (json: %s)", err, jsonStr)
	}

	return result, false, ""
}

// =============================================================================
// TOOL SETUP HELPERS
// =============================================================================

// SetupManagerWithTools creates a manager with specified tools registered
func SetupManagerWithTools(t *testing.T, tools []string) *mcp.MCPManager {
	t.Helper()
	manager := setupMCPManager(t)

	for _, toolName := range tools {
		switch toolName {
		case "echo":
			require.NoError(t, RegisterEchoTool(manager))
		case "calculator":
			require.NoError(t, RegisterCalculatorTool(manager))
		case "weather":
			require.NoError(t, RegisterWeatherTool(manager))
		case "search":
			require.NoError(t, RegisterSearchTool(manager))
		case "delay":
			require.NoError(t, RegisterDelayTool(manager))
		case "throw_error":
			require.NoError(t, RegisterThrowErrorTool(manager))
		case "get_time":
			require.NoError(t, RegisterGetTimeTool(manager))
		case "read_file":
			require.NoError(t, RegisterReadFileTool(manager))
		default:
			t.Fatalf("Unknown tool: %s", toolName)
		}
	}

	return manager
}

// SetupManagerWithAutoExecuteTools creates a manager with specified tools set to auto-execute
func SetupManagerWithAutoExecuteTools(t *testing.T, tools []string, autoExecuteTools []string) *mcp.MCPManager {
	t.Helper()
	manager := SetupManagerWithTools(t, tools)

	// Set auto-execute tools
	clients := manager.GetClients()
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "bifrostInternal" {
			clients[i].ExecutionConfig.ToolsToAutoExecute = autoExecuteTools
			err := manager.EditClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig)
			require.NoError(t, err)
			break
		}
	}

	return manager
}

// =============================================================================
// FILE PATH HELPERS
// =============================================================================

// GetTestDataPath returns path to test data file
func GetTestDataPath(t *testing.T, filename string) string {
	t.Helper()
	bifrostRoot := GetBifrostRoot(t)
	return filepath.Join(bifrostRoot, "core", "internal", "mcptests", "testdata", filename)
}

// CreateTempTestFile creates a temporary test file
func CreateTempTestFile(t *testing.T, content string) string {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "bifrost-test-*")
	require.NoError(t, err)

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)

	err = tmpFile.Close()
	require.NoError(t, err)

	// Cleanup
	t.Cleanup(func() {
		os.Remove(tmpFile.Name())
	})

	return tmpFile.Name()
}

// =============================================================================
// TEST LOGGER
// =============================================================================

// testLogger implements schemas.Logger for testing
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Debug(msg string, args ...any) {
	l.t.Logf("[DEBUG] "+msg, args...)
}

func (l *testLogger) Info(msg string, args ...any) {
	l.t.Logf("[INFO] "+msg, args...)
}

func (l *testLogger) Warn(msg string, args ...any) {
	l.t.Logf("[WARN] "+msg, args...)
}

func (l *testLogger) Error(msg string, args ...any) {
	l.t.Logf("[ERROR] "+msg, args...)
}

func (l *testLogger) Fatal(msg string, args ...any) {
	l.t.Fatalf("[FATAL] "+msg, args...)
}

func (l *testLogger) SetLevel(level schemas.LogLevel) {
	// No-op for tests
}

func (l *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {
	// No-op for tests
}

// =============================================================================
// DYNAMIC LLM MOCKER
// =============================================================================

// ChatResponseFunc is a function that generates a Chat response based on message history
type ChatResponseFunc func(history []schemas.ChatMessage) (*schemas.BifrostChatResponse, *schemas.BifrostError)

// ResponsesResponseFunc is a function that generates a Responses response based on message history
type ResponsesResponseFunc func(history []schemas.ResponsesMessage) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

// DynamicLLMMocker provides dynamic LLM responses that can inspect message history
type DynamicLLMMocker struct {
	chatResponseFuncs       []ChatResponseFunc
	responsesResponseFuncs  []ResponsesResponseFunc
	defaultChatResponse     ChatResponseFunc
	defaultResponsesResponse ResponsesResponseFunc
	chatCallCount           int
	responsesCallCount      int
	chatHistory             [][]schemas.ChatMessage
	responsesHistory        [][]schemas.ResponsesMessage
}

// NewDynamicLLMMocker creates a new dynamic LLM mocker
func NewDynamicLLMMocker() *DynamicLLMMocker {
	return &DynamicLLMMocker{
		chatResponseFuncs:      []ChatResponseFunc{},
		responsesResponseFuncs: []ResponsesResponseFunc{},
		chatHistory:            [][]schemas.ChatMessage{},
		responsesHistory:       [][]schemas.ResponsesMessage{},
	}
}

// AddChatResponse adds a Chat response function
func (m *DynamicLLMMocker) AddChatResponse(fn ChatResponseFunc) {
	m.chatResponseFuncs = append(m.chatResponseFuncs, fn)
}

// AddResponsesResponse adds a Responses response function
func (m *DynamicLLMMocker) AddResponsesResponse(fn ResponsesResponseFunc) {
	m.responsesResponseFuncs = append(m.responsesResponseFuncs, fn)
}

// AddStaticChatResponse adds a static Chat response (backwards compatible)
func (m *DynamicLLMMocker) AddStaticChatResponse(response *schemas.BifrostChatResponse) {
	m.AddChatResponse(func(history []schemas.ChatMessage) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return response, nil
	})
}

// AddStaticResponsesResponse adds a static Responses response (backwards compatible)
func (m *DynamicLLMMocker) AddStaticResponsesResponse(response *schemas.BifrostResponsesResponse) {
	m.AddResponsesResponse(func(history []schemas.ResponsesMessage) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return response, nil
	})
}

// SetDefaultChatResponse sets a default Chat response to use when no more specific responses are available
func (m *DynamicLLMMocker) SetDefaultChatResponse(fn ChatResponseFunc) {
	m.defaultChatResponse = fn
}

// SetDefaultResponsesResponse sets a default Responses response to use when no more specific responses are available
func (m *DynamicLLMMocker) SetDefaultResponsesResponse(fn ResponsesResponseFunc) {
	m.defaultResponsesResponse = fn
}

// SetDefaultStaticChatResponse sets a static default Chat response
func (m *DynamicLLMMocker) SetDefaultStaticChatResponse(response *schemas.BifrostChatResponse) {
	m.SetDefaultChatResponse(func(history []schemas.ChatMessage) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return response, nil
	})
}

// SetDefaultStaticResponsesResponse sets a static default Responses response
func (m *DynamicLLMMocker) SetDefaultStaticResponsesResponse(response *schemas.BifrostResponsesResponse) {
	m.SetDefaultResponsesResponse(func(history []schemas.ResponsesMessage) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return response, nil
	})
}

// MakeChatRequest implements the LLM caller interface for Chat API
func (m *DynamicLLMMocker) MakeChatRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Store the message history
	m.chatHistory = append(m.chatHistory, req.Input)

	var responseFn ChatResponseFunc

	if m.chatCallCount < len(m.chatResponseFuncs) {
		// Use a specific configured response
		responseFn = m.chatResponseFuncs[m.chatCallCount]
		m.chatCallCount++
	} else if m.defaultChatResponse != nil {
		// Use default response if available
		responseFn = m.defaultChatResponse
		m.chatCallCount++
	} else {
		// No response available - don't increment call count for failed attempts
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "no more mock chat responses available",
			},
		}
	}

	return responseFn(req.Input)
}

// MakeResponsesRequest implements the LLM caller interface for Responses API
func (m *DynamicLLMMocker) MakeResponsesRequest(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Store the message history
	m.responsesHistory = append(m.responsesHistory, req.Input)

	var responseFn ResponsesResponseFunc

	if m.responsesCallCount < len(m.responsesResponseFuncs) {
		// Use a specific configured response
		responseFn = m.responsesResponseFuncs[m.responsesCallCount]
		m.responsesCallCount++
	} else if m.defaultResponsesResponse != nil {
		// Use default response if available
		responseFn = m.defaultResponsesResponse
		m.responsesCallCount++
	} else {
		// No response available
		m.responsesCallCount++
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "no more mock responses api responses available",
			},
		}
	}

	return responseFn(req.Input)
}

// GetChatCallCount returns the number of Chat API calls made
func (m *DynamicLLMMocker) GetChatCallCount() int {
	return m.chatCallCount
}

// GetResponsesCallCount returns the number of Responses API calls made
func (m *DynamicLLMMocker) GetResponsesCallCount() int {
	return m.responsesCallCount
}

// GetChatHistory returns all Chat message histories
func (m *DynamicLLMMocker) GetChatHistory() [][]schemas.ChatMessage {
	return m.chatHistory
}

// GetResponsesHistory returns all Responses message histories
func (m *DynamicLLMMocker) GetResponsesHistory() [][]schemas.ResponsesMessage {
	return m.responsesHistory
}

// =============================================================================
// DYNAMIC LLM MOCKER - HELPER FUNCTIONS
// =============================================================================

// GetToolResultFromChatHistory extracts a tool result from Chat message history by call ID
func GetToolResultFromChatHistory(history []schemas.ChatMessage, callID string) (string, bool) {
	for _, msg := range history {
		if msg.Role == schemas.ChatMessageRoleTool {
			if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
				if *msg.ChatToolMessage.ToolCallID == callID {
					if msg.Content != nil && msg.Content.ContentStr != nil {
						return *msg.Content.ContentStr, true
					}
				}
			}
		}
	}
	return "", false
}

// GetToolResultFromResponsesHistory extracts a tool result from Responses message history by call ID
func GetToolResultFromResponsesHistory(history []schemas.ResponsesMessage, callID string) (string, bool) {
	for _, msg := range history {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				if *msg.ResponsesToolMessage.CallID == callID {
					if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
						return *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, true
					}
				}
			}
		}
	}
	return "", false
}

// GetAllToolResultsFromChatHistory extracts all tool results from Chat message history
func GetAllToolResultsFromChatHistory(history []schemas.ChatMessage) map[string]string {
	results := make(map[string]string)
	for _, msg := range history {
		if msg.Role == schemas.ChatMessageRoleTool {
			if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
				if msg.Content != nil && msg.Content.ContentStr != nil {
					results[*msg.ChatToolMessage.ToolCallID] = *msg.Content.ContentStr
				}
			}
		}
	}
	return results
}

// GetAllToolResultsFromResponsesHistory extracts all tool results from Responses message history
func GetAllToolResultsFromResponsesHistory(history []schemas.ResponsesMessage) map[string]string {
	results := make(map[string]string)
	for _, msg := range history {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
					results[*msg.ResponsesToolMessage.CallID] = *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
				}
			}
		}
	}
	return results
}

// GetLastUserMessageFromChatHistory extracts the last user message from Chat history
func GetLastUserMessageFromChatHistory(history []schemas.ChatMessage) (string, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == schemas.ChatMessageRoleUser {
			if history[i].Content != nil && history[i].Content.ContentStr != nil {
				return *history[i].Content.ContentStr, true
			}
		}
	}
	return "", false
}

// GetLastUserMessageFromResponsesHistory extracts the last user message from Responses history
func GetLastUserMessageFromResponsesHistory(history []schemas.ResponsesMessage) (string, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type != nil && *history[i].Type == schemas.ResponsesMessageTypeMessage {
			if history[i].Role != nil && *history[i].Role == schemas.ResponsesInputMessageRoleUser {
				if history[i].Content != nil && history[i].Content.ContentStr != nil {
					return *history[i].Content.ContentStr, true
				}
			}
		}
	}
	return "", false
}

// CountToolCallsInChatHistory counts the number of tool calls in Chat history
func CountToolCallsInChatHistory(history []schemas.ChatMessage) int {
	count := 0
	for _, msg := range history {
		if msg.Role == schemas.ChatMessageRoleAssistant {
			if msg.ChatAssistantMessage != nil {
				count += len(msg.ChatAssistantMessage.ToolCalls)
			}
		}
	}
	return count
}

// CountToolCallsInResponsesHistory counts the number of tool calls in Responses history
func CountToolCallsInResponsesHistory(history []schemas.ResponsesMessage) int {
	count := 0
	for _, msg := range history {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall {
			count++
		}
	}
	return count
}

// HasToolCallInChatHistory checks if a specific tool was called in Chat history
func HasToolCallInChatHistory(history []schemas.ChatMessage, toolName string) bool {
	for _, msg := range history {
		if msg.Role == schemas.ChatMessageRoleAssistant {
			if msg.ChatAssistantMessage != nil {
				for _, tc := range msg.ChatAssistantMessage.ToolCalls {
					if tc.Function.Name != nil {
						fullName := *tc.Function.Name
						// Check for exact match or with client prefix
						if fullName == toolName ||
							fullName == "bifrostInternal-"+toolName ||
							// Also check if toolName already has a prefix and matches exactly
							(strings.Contains(toolName, "-") && fullName == toolName) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// HasToolCallInResponsesHistory checks if a specific tool was called in Responses history
// Supports both prefixed (bifrostInternal-toolName) and unprefixed tool names
func HasToolCallInResponsesHistory(history []schemas.ResponsesMessage, toolName string) bool {
	for _, msg := range history {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall {
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
				fullName := *msg.ResponsesToolMessage.Name
				// Check exact match
				if fullName == toolName {
					return true
				}
				// Check with bifrostInternal- prefix
				if fullName == "bifrostInternal-"+toolName {
					return true
				}
				// Check if toolName already has a prefix (format: "prefix-toolName")
				// and matches the full name
				if len(toolName) > 0 && fullName == toolName {
					return true
				}
			}
		}
	}
	return false
}

// CreateChatResponseWithToolCalls creates a Chat response with tool calls
func CreateChatResponseWithToolCalls(toolCalls []schemas.ChatAssistantMessageToolCall) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("tool_calls"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr(""),
						},
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: toolCalls,
						},
					},
				},
			},
		},
	}
}

// CreateChatResponseWithText creates a Chat response with text
func CreateChatResponseWithText(text string) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("stop"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr(text),
						},
					},
				},
			},
		},
	}
}

// CreateResponsesResponseWithToolCalls creates a Responses response with tool calls
func CreateResponsesResponseWithToolCalls(toolCalls []schemas.ResponsesToolMessage) *schemas.BifrostResponsesResponse {
	output := []schemas.ResponsesMessage{}
	for _, tc := range toolCalls {
		msgType := schemas.ResponsesMessageTypeFunctionCall
		role := schemas.ResponsesInputMessageRoleAssistant
		output = append(output, schemas.ResponsesMessage{
			Type:                 &msgType,
			Role:                 &role,
			ResponsesToolMessage: &tc,
		})
	}
	return &schemas.BifrostResponsesResponse{
		Output: output,
	}
}

// CreateResponsesResponseWithText creates a Responses response with text
func CreateResponsesResponseWithText(text string) *schemas.BifrostResponsesResponse {
	msgType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleAssistant
	return &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: &msgType,
				Role: &role,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr(text),
				},
			},
		},
	}
}

// CreateDynamicChatResponse is a convenience function for creating a dynamic Chat response
func CreateDynamicChatResponse(fn func(history []schemas.ChatMessage) *schemas.BifrostChatResponse) ChatResponseFunc {
	return func(history []schemas.ChatMessage) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return fn(history), nil
	}
}

// CreateDynamicResponsesResponse is a convenience function for creating a dynamic Responses response
func CreateDynamicResponsesResponse(fn func(history []schemas.ResponsesMessage) *schemas.BifrostResponsesResponse) ResponsesResponseFunc {
	return func(history []schemas.ResponsesMessage) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return fn(history), nil
	}
}

// =============================================================================
// PREBUILT RESPONSE PATTERNS
// =============================================================================

// CreateValidatingChatResponse creates a Chat response that validates tool results before responding
// Example: CreateValidatingChatResponse("call-1", []string{"15", "C"}, "The temperature is 15°C", "Unexpected result")
func CreateValidatingChatResponse(callID string, mustContain []string, successText string, failureText string) ChatResponseFunc {
	return CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		result, found := GetToolResultFromChatHistory(history, callID)
		if !found {
			return CreateChatResponseWithText(failureText + " (tool result not found)")
		}

		// Simple validation - check if all required strings are in the result
		allFound := true
		for _, required := range mustContain {
			itemFound := false

			// Try to parse as JSON and check recursively
			var jsonData interface{}
			if err := json.Unmarshal([]byte(result), &jsonData); err == nil {
				// Check JSON structure
				if containsInJSON(jsonData, required) {
					itemFound = true
				}
			} else {
				// Fall back to simple string contains
				if containsString(result, required) {
					itemFound = true
				}
			}

			if !itemFound {
				allFound = false
				break
			}
		}

		if allFound {
			return CreateChatResponseWithText(successText)
		}
		return CreateChatResponseWithText(failureText)
	})
}

// containsString checks if a string contains a substring (case-sensitive)
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// containsInJSON recursively searches for a string in JSON structure
func containsInJSON(data interface{}, search string) bool {
	switch v := data.(type) {
	case string:
		return containsString(v, search)
	case map[string]interface{}:
		for _, val := range v {
			if containsInJSON(val, search) {
				return true
			}
		}
	case []interface{}:
		for _, val := range v {
			if containsInJSON(val, search) {
				return true
			}
		}
	}
	return false
}

// CreateConditionalChatResponse creates a Chat response based on a condition function
func CreateConditionalChatResponse(condition func(history []schemas.ChatMessage) bool, trueResponse, falseResponse *schemas.BifrostChatResponse) ChatResponseFunc {
	return CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		if condition(history) {
			return trueResponse
		}
		return falseResponse
	})
}

// CreateSequentialChatResponses creates multiple response functions that return responses in sequence
func CreateSequentialChatResponses(responses []*schemas.BifrostChatResponse) []ChatResponseFunc {
	funcs := make([]ChatResponseFunc, len(responses))
	for i, resp := range responses {
		r := resp // Capture for closure
		funcs[i] = CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
			return r
		})
	}
	return funcs
}

// CreateToolCallSequence creates a sequence of tool call -> result -> response
// This is useful for multi-turn agent scenarios
func CreateToolCallSequence(sequences []struct {
	ToolCall     schemas.ChatAssistantMessageToolCall
	ExpectedText string // Text to look for in the result before moving to next
	FinalText    string // Final response text
}) []ChatResponseFunc {
	funcs := make([]ChatResponseFunc, 0)

	for i, seq := range sequences {
		isLast := i == len(sequences)-1
		expectedText := seq.ExpectedText
		finalText := seq.FinalText
		toolCall := seq.ToolCall

		if isLast {
			// Last one - check for expected text and return final text
			funcs = append(funcs, CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
				if toolCall.ID != nil {
					result, found := GetToolResultFromChatHistory(history, *toolCall.ID)
					if found && (expectedText == "" || containsString(result, expectedText)) {
						return CreateChatResponseWithText(finalText)
					}
				}
				return CreateChatResponseWithText("Unexpected result in sequence")
			}))
		} else {
			// Not last - return next tool call
			funcs = append(funcs, CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
				return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{toolCall})
			}))
		}
	}

	return funcs
}

// =============================================================================
// EXAMPLE USAGE PATTERNS
// =============================================================================

/*
Example 1: Simple validation of tool result

mocker := NewDynamicLLMMocker()
mocker.AddChatResponse(
	CreateValidatingChatResponse(
		"call-1",
		[]string{"15", "C"},
		"The temperature is 15°C",
		"Unexpected temperature format",
	),
)

Example 2: Conditional response based on history

mocker := NewDynamicLLMMocker()
mocker.AddChatResponse(
	CreateConditionalChatResponse(
		func(history []schemas.ChatMessage) bool {
			return HasToolCallInChatHistory(history, "get_weather")
		},
		CreateChatResponseWithText("Weather data received"),
		CreateChatResponseWithText("No weather data found"),
	),
)

Example 3: Multi-turn agent scenario

mocker := NewDynamicLLMMocker()

// Turn 1: Request weather
mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
	return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
		GetSampleWeatherToolCall("call-1", "London", "celsius"),
	})
}))

// Turn 2: Validate result contains temperature and respond
mocker.AddChatResponse(
	CreateValidatingChatResponse(
		"call-1",
		[]string{"temperature", "London"},
		"The weather in London looks good!",
		"Could not get weather data",
	),
)

Example 4: Complex multi-turn with multiple tool calls

mocker := NewDynamicLLMMocker()

// Turn 1: Call multiple tools in parallel
mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
	return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
		GetSampleWeatherToolCall("call-1", "Tokyo", "celsius"),
		GetSampleWeatherToolCall("call-2", "London", "celsius"),
	})
}))

// Turn 2: Validate both results and respond
mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
	results := GetAllToolResultsFromChatHistory(history)

	tokyo, hasTokyo := results["call-1"]
	london, hasLondon := results["call-2"]

	if hasTokyo && hasLondon && containsString(tokyo, "Tokyo") && containsString(london, "London") {
		return CreateChatResponseWithText("Got weather for both cities!")
	}

	return CreateChatResponseWithText("Missing weather data")
}))
*/

// =============================================================================
// TOOL CALL HELPERS FOR TEST EXECUTION
// =============================================================================

// CreateToolCallForExecution creates a tool call with the proper client prefix
// for direct execution via ExecuteChatMCPTool.
// The tool name is automatically prefixed with "bifrostInternal-" to match
// how tools are stored in the MCP manager.
func CreateToolCallForExecution(callID string, toolName string, args map[string]interface{}) schemas.ChatAssistantMessageToolCall {
	argsJSON, _ := json.Marshal(args)
	prefixedToolName := "bifrostInternal-" + toolName

	return schemas.ChatAssistantMessageToolCall{
		ID:   &callID,
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &prefixedToolName,
			Arguments: string(argsJSON),
		},
	}
}

// CreateResponsesToolCallForExecution creates a Responses API tool call with the proper client prefix
// for direct execution via ExecuteResponsesMCPTool.
// The tool name is automatically prefixed with "bifrostInternal-" to match
// how tools are stored in the MCP manager.
func CreateResponsesToolCallForExecution(callID string, toolName string, args map[string]interface{}) schemas.ResponsesToolMessage {
	argsJSON, _ := json.Marshal(args)
	argsStr := string(argsJSON)
	prefixedToolName := "bifrostInternal-" + toolName

	return schemas.ResponsesToolMessage{
		CallID:    &callID,
		Name:      &prefixedToolName,
		Arguments: &argsStr,
	}
}
