package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// WeatherArgs defines the arguments for the weather tool
type WeatherArgs struct {
	City string `json:"city" jsonschema:"required,description=The city name to get weather for"`
}

// Mock weather data for testing
var mockWeatherData = map[string]string{
	"new york":      "Sunny, 22°C",
	"london":        "Cloudy, 15°C",
	"tokyo":         "Rainy, 18°C",
	"paris":         "Partly cloudy, 19°C",
	"san francisco": "Foggy, 16°C",
}

// BaseAccount implements the schemas.Account interface for testing purposes.
type BaseAccount struct{}

func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (baseAccount *BaseAccount) GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  os.Getenv("OPENAI_API_KEY"),
			Models: []string{"gpt-4o-mini", "gpt-4-turbo"},
			Weight: 1.0,
		},
	}, nil
}

func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

func TestMCPPlugin_WeatherTool(t *testing.T) {
	// Check if OpenAI API key is available
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	// Create the MCP host plugin
	plugin, err := NewMCPPlugin(MCPPluginConfig{ServerPort: ":8282"}, bifrost.NewDefaultLogger(schemas.LogLevelDebug)) // Use a different port
	if err != nil {
		t.Fatalf("Failed to create MCP plugin: %v", err)
	}

	// Define the weather tool schema for Bifrost
	weatherToolSchema := schemas.Tool{
		Type: "function",
		Function: schemas.Function{
			Name:        "get_weather",
			Description: "Get current weather information for a specified city",
			Parameters: schemas.FunctionParameters{
				Type: "object",
				Properties: map[string]interface{}{
					"city": map[string]interface{}{
						"type":        "string",
						"description": "The city name to get weather for",
					},
				},
				Required: []string{"city"},
			},
		},
	}

	// Register weather tool using plugin's API
	weatherHandler := func(args WeatherArgs) (string, error) {
		// Case-insensitive lookup
		cityLower := strings.ToLower(args.City)
		if weather, exists := mockWeatherData[cityLower]; exists {
			return fmt.Sprintf("Weather in %s: %s", args.City, weather), nil
		}
		return fmt.Sprintf("Weather data not available for %s", args.City), nil
	}

	err = RegisterTool(plugin, "get_weather", "Get current weather information for a specified city",
		weatherHandler, weatherToolSchema, ToolExecutionPolicyAutoExecute)
	if err != nil {
		t.Fatalf("Failed to register weather tool: %v", err)
	}

	// Initialize Bifrost with the MCP plugin
	account := BaseAccount{}
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("Failed to initialize Bifrost: %v", err)
	}

	t.Run("WeatherQuery_ThroughBifrost", func(t *testing.T) {
		fmt.Println("=== BIFROST WEATHER QUERY TEST ===")

		// Make a chat completion request that should trigger the weather tool
		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("What's the weather like in New York? Use the get_weather tool.")},
					},
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Chat completion request failed: %v", bifrostErr)
		}

		if response == nil {
			t.Fatalf("Expected response, got nil")
		}

		fmt.Printf("Response received with %d choices\n", len(response.Choices))

		// Check if we got a response
		if len(response.Choices) == 0 {
			t.Fatalf("Expected at least one choice in response")
		}

		// Log the response for debugging
		choice := response.Choices[0]
		fmt.Printf("Response role: %s\n", choice.Message.Role)

		if choice.Message.Content.ContentStr != nil {
			fmt.Printf("Response content: %s\n", *choice.Message.Content.ContentStr)

			// Check if the response contains weather information
			responseText := strings.ToLower(*choice.Message.Content.ContentStr)
			if !strings.Contains(responseText, "weather") && !strings.Contains(responseText, "sunny") &&
				!strings.Contains(responseText, "cloudy") && !strings.Contains(responseText, "°c") {
				t.Logf("Warning: Response may not contain expected weather information")
			}
		}

		// Check if tool calls were made (could be in the response)
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			fmt.Printf("Response contains %d tool calls\n", len(*choice.Message.AssistantMessage.ToolCalls))
			for i, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				fmt.Printf("  Tool call %d: %s\n", i+1, *toolCall.Function.Name)
			}
		}
	})

	// Cleanup
	client.Cleanup()
	fmt.Println("\n=== TEST COMPLETED SUCCESSFULLY ===")
}

// Helper function to print choices as JSON
func printChoicesAsJSON(t *testing.T, choices []schemas.BifrostResponseChoice) {
	t.Helper()
	jsonData, err := json.MarshalIndent(choices, "", "  ")
	if err != nil {
		t.Errorf("Failed to marshal choices to JSON: %v", err)
		return
	}
	fmt.Printf("--- Response Choices (JSON) ---\n%s\n-----------------------------\n", string(jsonData))
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

func TestMCPPlugin_Context7Integration(t *testing.T) {
	// Skip this test if no OPENAI_API_KEY is set
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping Context7 integration test: OPENAI_API_KEY not set")
	}

	// Create MCP plugin
	plugin, err := NewMCPPlugin(MCPPluginConfig{}, bifrost.NewDefaultLogger(schemas.LogLevelDebug))
	if err != nil {
		t.Fatalf("Failed to create plugin: %v", err)
	}

	// Connect to Context7 MCP server using npx
	err = plugin.ConnectToExternalMCP(ExternalMCPConfig{
		Name:           "context7",
		ConnectionType: ConnectionTypeSTDIO,
		StdioConfig: &StdioConfig{
			Command: "npx",
			Args:    []string{"-y", "@upstash/context7-mcp"},
		},
	})
	if err != nil {
		t.Fatalf("Failed to connect to Context7 MCP: %v", err)
	}

	// Give some time for the external server to start and register tools
	time.Sleep(2 * time.Second)

	// Initialize Bifrost with the MCP plugin
	account := BaseAccount{}
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("Failed to initialize Bifrost: %v", err)
	}

	// Test 1: Check if Context7 tools are available through the plugin
	availableTools := plugin.getFilteredAvailableTools(nil)

	expectedTools := []string{"resolve-library-id", "get-library-docs"}
	for _, expectedTool := range expectedTools {
		found := false
		for _, tool := range availableTools {
			if tool.Function.Name == expectedTool {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected Context7 tool '%s' not found in available tools", expectedTool)
		}
	}

	// Test 2: Use Context7 to resolve a library ID through Bifrost
	t.Run("ResolveLibraryID_ThroughBifrost", func(t *testing.T) {
		fmt.Println("=== CONTEXT7 RESOLVE LIBRARY ID TEST ===")

		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("I need to resolve the library ID for 'react' using the resolve-library-id tool.")},
					},
				},
			},
			Params: &schemas.ModelParameters{
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStr: stringPtr("auto"),
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Chat completion request failed: %v", bifrostErr)
		}

		if response == nil || len(response.Choices) == 0 {
			t.Fatal("Expected response with choices")
		}

		choice := response.Choices[0]
		fmt.Printf("Response role: %s\n", choice.Message.Role)

		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			fmt.Printf("Response contains %d tool calls\n", len(*choice.Message.AssistantMessage.ToolCalls))
			for i, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				fmt.Printf("  Tool call %d: %s\n", i+1, *toolCall.Function.Name)
				if *toolCall.Function.Name == "resolve-library-id" {
					t.Logf("Successfully called resolve-library-id with arguments: %s", toolCall.Function.Arguments)
				}
			}
		} else {
			t.Log("No tool calls made - this might be expected if the model chooses not to call tools")
		}
	})

	// Test 3: Use Context7 to get documentation
	t.Run("GetLibraryDocs_ThroughBifrost", func(t *testing.T) {
		fmt.Println("=== CONTEXT7 GET LIBRARY DOCS TEST ===")

		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("Get documentation for React hooks using the get-library-docs tool with library ID '/facebook/react'.")},
					},
				},
			},
			Params: &schemas.ModelParameters{
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStr: stringPtr("auto"),
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Chat completion request failed: %v", bifrostErr)
		}

		if response == nil || len(response.Choices) == 0 {
			t.Fatal("Expected response with choices")
		}

		choice := response.Choices[0]
		fmt.Printf("Response role: %s\n", choice.Message.Role)

		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			fmt.Printf("Response contains %d tool calls\n", len(*choice.Message.AssistantMessage.ToolCalls))
			for i, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
				fmt.Printf("  Tool call %d: %s\n", i+1, *toolCall.Function.Name)
				if *toolCall.Function.Name == "get-library-docs" {
					t.Logf("Successfully called get-library-docs with arguments: %s", toolCall.Function.Arguments)
				}
			}
		} else {
			t.Log("No tool calls made - this might be expected if the model chooses not to call tools")
		}
	})

	// Test 4: Use Context7 to get documentation and provide a final answer
	t.Run("GetLibraryDocsAndAnswer_ThroughBifrost", func(t *testing.T) {
		fmt.Println("=== CONTEXT7 GET DOCS AND ANSWER TEST ===")

		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini", // A capable model is needed for this
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("Can you explain what React's useReducer hook is and provide a simple code example? Use the available tools.")},
					},
				},
			},
			Params: &schemas.ModelParameters{
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStr: stringPtr("auto"),
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Chat completion request failed: %v", bifrostErr)
		}

		if response == nil || len(response.Choices) == 0 {
			t.Fatal("Expected response with choices")
		}

		choice := response.Choices[0]
		fmt.Printf("Final response role: %s\n", choice.Message.Role)

		if choice.Message.Content.ContentStr == nil {
			t.Fatal("Expected a final text response from the model, but got nil content")
		}

		finalAnswer := *choice.Message.Content.ContentStr
		fmt.Printf("Final answer from model:\n%s\n", finalAnswer)

		// Check for keywords that indicate the model successfully used the tool and synthesized an answer
		expectedKeywords := []string{"usereducer", "hook", "state", "dispatch", "reducer"}
		answerLower := strings.ToLower(finalAnswer)

		for _, keyword := range expectedKeywords {
			if !strings.Contains(answerLower, keyword) {
				t.Errorf("Final answer is missing expected keyword: '%s'", keyword)
			}
		}

		// Also check that no tool calls are in the *final* response, as they should have been handled
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			t.Errorf("Expected final response to be a text message, but it contained tool calls")
		}
	})

	// Cleanup
	client.Cleanup()
	fmt.Println("\n=== CONTEXT7 INTEGRATION TEST COMPLETED ===")
}

func TestMCPPlugin_Maxim(t *testing.T) {
	// Skip this test if no OPENAI_API_KEY is set
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping Maxim MCP integration test: OPENAI_API_KEY not set")
	}

	// Create MCP plugin with agentic mode enabled
	plugin, err := NewMCPPlugin(MCPPluginConfig{
		AgenticMode: true, // Enable agentic flow
	}, bifrost.NewDefaultLogger(schemas.LogLevelDebug))
	if err != nil {
		t.Fatalf("Failed to create plugin: %v", err)
	}

	// Connect to Maxim MCP server using uvx
	err = plugin.ConnectToExternalMCP(ExternalMCPConfig{
		Name:           "maxim-mcp",
		ConnectionType: ConnectionTypeSTDIO,
		StdioConfig: &StdioConfig{
			Command: "npx",
			Args:    []string{"-y", "@maximai/mcp-server@latest"},
		},
	})
	if err != nil {
		t.Fatalf("Failed to connect to maxim-mcp MCP: %v", err)
	}

	// Give some time for the external server to start and register tools
	time.Sleep(3 * time.Second)

	// Initialize Bifrost with the MCP plugin
	account := BaseAccount{}
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: &account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	})
	if err != nil {
		t.Fatalf("Failed to initialize Bifrost: %v", err)
	}

	// Set the Bifrost client for agentic mode
	plugin.SetBifrostClient(client)

	t.Run("SearchAndAnswer_ThroughBifrost", func(t *testing.T) {
		fmt.Println("=== MAXIM MCP AND ANSWER TEST ===")

		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini", // A capable model is needed for this
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("fetch log-repository-entities from repository-id cma3pm2qn0668oeu5mdylurdv and workspace-id cm82w2kn1004r9wkqhsixdpzg")},
					},
				},
			},
			Params: &schemas.ModelParameters{
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStr: stringPtr("auto"),
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Chat completion request failed: %v", bifrostErr)
		}

		if response == nil || len(response.Choices) == 0 {
			t.Fatal("Expected response with choices")
		}

		// printChoicesAsJSON(t, response.Choices)

		choice := response.Choices[0]
		fmt.Printf("Final response role: %s\n", choice.Message.Role)

		if choice.Message.Content.ContentStr == nil {
			t.Fatal("Expected a final text response from the model, but got nil content")
		}

		finalAnswer := *choice.Message.Content.ContentStr
		fmt.Printf("Final answer from model:\n%s\n", finalAnswer)

		// Check for keywords that indicate the model successfully used the tool and synthesized an answer
		// Using a broad check because the exact phrasing can vary.
		answerLower := strings.ToLower(finalAnswer)
		if !strings.Contains(answerLower, "bifrost") {
			t.Errorf("Final answer is missing expected keyword: 'bifrost'")
		}

		// Also check that no tool calls are in the *final* response, as they should have been handled
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil && len(*choice.Message.AssistantMessage.ToolCalls) > 0 {
			t.Errorf("Expected final response to be a text message, but it contained tool calls")
		}
	})

	// Cleanup
	client.Cleanup()
	fmt.Println("\n=== MAXIM MCP TEST COMPLETED ===")
}

func TestMCPPlugin_AgenticMode(t *testing.T) {
	// Skip this test if no OPENAI_API_KEY is set
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping agentic mode test: OPENAI_API_KEY not set")
	}

	t.Run("NonAgenticMode", func(t *testing.T) {
		// Test non-agentic mode (original behavior)
		plugin, err := NewMCPPlugin(MCPPluginConfig{
			ServerPort:  ":8383",
			AgenticMode: false, // Disable agentic flow
		}, bifrost.NewDefaultLogger(schemas.LogLevelDebug))
		if err != nil {
			t.Fatalf("Failed to create MCP plugin: %v", err)
		}

		// Register a simple test tool
		testToolSchema := schemas.Tool{
			Type: "function",
			Function: schemas.Function{
				Name:        "test_tool",
				Description: "A simple test tool",
				Parameters: schemas.FunctionParameters{
					Type:       "object",
					Properties: map[string]interface{}{},
					Required:   []string{},
				},
			},
		}

		err = RegisterTool(plugin, "test_tool", "A simple test tool",
			func(args struct{}) (string, error) {
				return "Test tool executed successfully", nil
			}, testToolSchema, ToolExecutionPolicyAutoExecute)
		if err != nil {
			t.Fatalf("Failed to register test tool: %v", err)
		}

		// Initialize Bifrost
		account := BaseAccount{}
		client, err := bifrost.Init(schemas.BifrostConfig{
			Account: &account,
			Plugins: []schemas.Plugin{plugin},
			Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Test request that should trigger tool call
		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("Use the test_tool")},
					},
				},
			},
			Params: &schemas.ModelParameters{
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStr: stringPtr("auto"),
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Request failed: %v", bifrostErr)
		}

		if response == nil || len(response.Choices) == 0 {
			t.Fatal("Expected response with choices")
		}

		// In non-agentic mode, the response should be a tool message
		choice := response.Choices[0]
		if choice.Message.Role != schemas.ModelChatMessageRoleTool {
			t.Errorf("Expected tool message role, got: %s", choice.Message.Role)
		}

		client.Cleanup()
	})

	t.Run("AgenticMode", func(t *testing.T) {
		// Test agentic mode (new behavior)
		plugin, err := NewMCPPlugin(MCPPluginConfig{
			ServerPort:  ":8384",
			AgenticMode: true, // Enable agentic flow
		}, bifrost.NewDefaultLogger(schemas.LogLevelDebug))
		if err != nil {
			t.Fatalf("Failed to create MCP plugin: %v", err)
		}

		// Register a simple test tool
		testToolSchema := schemas.Tool{
			Type: "function",
			Function: schemas.Function{
				Name:        "test_tool_agentic",
				Description: "A simple test tool for agentic mode",
				Parameters: schemas.FunctionParameters{
					Type:       "object",
					Properties: map[string]interface{}{},
					Required:   []string{},
				},
			},
		}

		err = RegisterTool(plugin, "test_tool_agentic", "A simple test tool for agentic mode",
			func(args struct{}) (string, error) {
				return "Tool found the answer: 42", nil
			}, testToolSchema, ToolExecutionPolicyAutoExecute)
		if err != nil {
			t.Fatalf("Failed to register test tool: %v", err)
		}

		// Initialize Bifrost
		account := BaseAccount{}
		client, err := bifrost.Init(schemas.BifrostConfig{
			Account: &account,
			Plugins: []schemas.Plugin{plugin},
			Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Set the Bifrost client for agentic mode
		plugin.SetBifrostClient(client)

		// Test request that should trigger tool call and then synthesis
		response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: schemas.RequestInput{
				ChatCompletionInput: &[]schemas.BifrostMessage{
					{
						Role:    schemas.ModelChatMessageRoleUser,
						Content: schemas.MessageContent{ContentStr: stringPtr("Use the test_tool_agentic to find the meaning of life")},
					},
				},
			},
			Params: &schemas.ModelParameters{
				ToolChoice: &schemas.ToolChoice{
					ToolChoiceStr: stringPtr("auto"),
				},
			},
		})

		if bifrostErr != nil {
			t.Fatalf("Request failed: %v", bifrostErr)
		}

		if response == nil || len(response.Choices) == 0 {
			t.Fatal("Expected response with choices")
		}

		// In agentic mode, the response should be an assistant message with synthesized content
		choice := response.Choices[0]
		if choice.Message.Role != schemas.ModelChatMessageRoleAssistant {
			t.Errorf("Expected assistant message role, got: %s", choice.Message.Role)
		}

		// Check that the response contains some indication of synthesis
		if choice.Message.Content.ContentStr == nil {
			t.Fatal("Expected synthesized text response")
		}

		finalAnswer := *choice.Message.Content.ContentStr
		fmt.Printf("Agentic synthesized response: %s\n", finalAnswer)

		// The response should ideally contain information from the tool result
		answerLower := strings.ToLower(finalAnswer)
		if !strings.Contains(answerLower, "42") {
			t.Log("Warning: Synthesized response may not contain tool result data")
		}

		client.Cleanup()

	})

	fmt.Println("\n=== AGENTIC MODE TESTS COMPLETED ===")
}
