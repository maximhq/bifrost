package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ChatbotConfig holds configuration for the chatbot
type ChatbotConfig struct {
	Provider       schemas.ModelProvider
	Model          string
	MCPAgenticMode bool
	MCPServerPort  int
	Temperature    *float64
	MaxTokens      *int
}

// ChatSession manages the conversation state
type ChatSession struct {
	history      []schemas.ChatMessage
	client       *bifrost.Bifrost
	config       ChatbotConfig
	systemPrompt string
	account      *ComprehensiveTestAccount
}

// ComprehensiveTestAccount provides a test implementation of the Account interface for comprehensive testing.
type ComprehensiveTestAccount struct{}

// getEnvWithDefault returns the value of the environment variable if set, otherwise returns the default value
func getEnvWithDefault(envVar, defaultValue string) string {
	if value := os.Getenv(envVar); value != "" {
		return value
	}
	return defaultValue
}

// GetConfiguredProviders returns the list of initially supported providers.
func (account *ComprehensiveTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{
		schemas.OpenAI,
		schemas.Anthropic,
		schemas.Bedrock,
		schemas.Cohere,
		schemas.Azure,
		schemas.Vertex,
		schemas.Ollama,
		schemas.Mistral,
	}, nil
}

// GetKeysForProvider returns the API keys and associated models for a given provider.
func (account *ComprehensiveTestAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	switch providerKey {
	case schemas.OpenAI:
		return []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{"gpt-4o-mini", "gpt-4-turbo", "gpt-4o"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Anthropic:
		return []schemas.Key{
			{
				Value:  os.Getenv("ANTHROPIC_API_KEY"),
				Models: []string{"claude-3-7-sonnet-20250219", "claude-3-5-sonnet-20240620", "claude-2.1"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Bedrock:
		return []schemas.Key{
			{
				Value:  os.Getenv("BEDROCK_API_KEY"),
				Models: []string{"anthropic.claude-v2:1", "mistral.mixtral-8x7b-instruct-v0:1", "mistral.mistral-large-2402-v1:0", "anthropic.claude-3-sonnet-20240229-v1:0"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Cohere:
		return []schemas.Key{
			{
				Value:  os.Getenv("COHERE_API_KEY"),
				Models: []string{"command-a-03-2025", "c4ai-aya-vision-8b"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Azure:
		return []schemas.Key{
			{
				Value:  os.Getenv("AZURE_API_KEY"),
				Models: []string{"gpt-4o"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Vertex:
		return []schemas.Key{
			{
				Value:  os.Getenv("VERTEX_API_KEY"),
				Models: []string{"gemini-pro", "gemini-1.5-pro"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Mistral:
		return []schemas.Key{
			{
				Value:  os.Getenv("MISTRAL_API_KEY"),
				Models: []string{"mistral-large-2411", "pixtral-12b-latest"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Ollama:
		return []schemas.Key{
			{
				Value:  "", // Ollama is keyless
				Models: []string{"llama3.2", "llama3.1", "mistral", "codellama"},
				Weight: 1.0,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// GetConfigForProvider returns the configuration settings for a given provider.
func (account *ComprehensiveTestAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	switch providerKey {
	case schemas.OpenAI:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Anthropic:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Bedrock:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			// MetaConfig: &meta.BedrockMetaConfig{ // FIXME: meta package doesn't exist
			//	SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			//	Region:          bifrost.Ptr(getEnvWithDefault("AWS_REGION", "us-east-1")),
			// },
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Cohere:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Azure:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			// MetaConfig: &meta.AzureMetaConfig{ // FIXME: meta package doesn't exist
			//	Endpoint: os.Getenv("AZURE_ENDPOINT"),
			//	Deployments: map[string]string{
			//		"gpt-4o": "gpt-4o-aug",
			//	},
			//	APIVersion: bifrost.Ptr(getEnvWithDefault("AZURE_API_VERSION", "2024-08-01-preview")),
			// },
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Vertex:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			// MetaConfig: &meta.VertexMetaConfig{ // FIXME: meta package doesn't exist
			//	ProjectID:       os.Getenv("VERTEX_PROJECT_ID"),
			//	Region:          getEnvWithDefault("VERTEX_REGION", "us-central1"),
			//	AuthCredentials: os.Getenv("VERTEX_CREDENTIALS"),
			// },
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Ollama:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Mistral:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// NewChatSession creates a new chat session with the given configuration
func NewChatSession(config ChatbotConfig) (*ChatSession, error) {
	// Create MCP configuration for Bifrost
	mcpConfig := &schemas.MCPConfig{
		ClientConfigs: []schemas.MCPClientConfig{},
	}

	fmt.Println("🔌 Configuring Serper MCP server...")
	mcpConfig.ClientConfigs = append(mcpConfig.ClientConfigs, schemas.MCPClientConfig{
		Name:           "serper-web-search-mcp",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "npx",
			Args:    []string{"-y", "serper-search-scrape-mcp-server"},
			Envs:    []string{"SERPER_API_KEY"},
		},
		ToolsToSkip: []string{}, // No tools to skip for this client
	},
		schemas.MCPClientConfig{
			Name:             "gmail-mcp",
			ConnectionType:   schemas.MCPConnectionTypeSSE,
			ConnectionString: bifrost.Ptr("https://mcp.composio.dev/composio/server/654c1e3f-ea7d-47b6-9e31-398d00449654/sse"),
		},
	)

	fmt.Println("🔌 Configuring Context7 MCP server...")
	mcpConfig.ClientConfigs = append(mcpConfig.ClientConfigs, schemas.MCPClientConfig{
		Name:           "context7",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "npx",
			Args:    []string{"-y", "@upstash/context7-mcp"},
		},
		ToolsToSkip: []string{}, // No tools to skip for this client
	})

	// Initialize Bifrost with MCP configuration
	account := &ComprehensiveTestAccount{}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:   account,
		Plugins:   []schemas.Plugin{}, // No separate plugins needed - MCP is integrated
		Logger:    bifrost.NewDefaultLogger(schemas.LogLevelInfo),
		MCPConfig: mcpConfig, // MCP is now configured here
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Bifrost: %w", err)
	}

	session := &ChatSession{
		history: make([]schemas.ChatMessage, 0),
		client:  client,
		config:  config,
		account: account,
		systemPrompt: "You are a helpful AI assistant with access to various tools. " +
			"Use the available tools when they can help answer the user's questions more accurately or provide additional information.",
	}

	// Add system message to history
	if session.systemPrompt != "" {
		session.history = append(session.history, schemas.ChatMessage{
			Role: schemas.ModelChatMessageRoleSystem,
			Content: schemas.ChatMessageContent{
				ContentStr: &session.systemPrompt,
			},
		})
	}

	return session, nil
}

// getAvailableProviders returns a list of providers that have valid configurations
func (s *ChatSession) getAvailableProviders() []schemas.ModelProvider {
	configuredProviders, err := s.account.GetConfiguredProviders()
	if err != nil {
		return []schemas.ModelProvider{}
	}

	var availableProviders []schemas.ModelProvider
	for _, provider := range configuredProviders {
		// Check if provider has valid keys (except for keyless providers)
		if provider == schemas.Ollama || provider == schemas.Vertex {
			availableProviders = append(availableProviders, provider)
			continue
		}
		ctx := context.Background()
		keys, err := s.account.GetKeysForProvider(&ctx, provider)
		if err == nil && len(keys) > 0 && keys[0].Value != "" {
			availableProviders = append(availableProviders, provider)
		}
	}
	return availableProviders
}

// getAvailableModels returns available models for a given provider
func (s *ChatSession) getAvailableModels(provider schemas.ModelProvider) []string {
	ctx := context.Background()
	keys, err := s.account.GetKeysForProvider(&ctx, provider)
	if err != nil || len(keys) == 0 {
		return []string{}
	}
	return keys[0].Models
}

// switchProvider handles switching to a different provider
func (s *ChatSession) switchProvider() error {
	availableProviders := s.getAvailableProviders()
	if len(availableProviders) == 0 {
		fmt.Println("❌ No available providers found")
		return fmt.Errorf("no available providers")
	}

	fmt.Println("\n🔄 Available Providers:")
	fmt.Println("======================")
	for i, provider := range availableProviders {
		status := ""
		if provider == s.config.Provider {
			status = " (current)"
		}
		fmt.Printf("[%d] %s%s\n", i+1, provider, status)
	}

	fmt.Print("\nSelect provider (number): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return fmt.Errorf("input cancelled")
	}

	choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || choice < 1 || choice > len(availableProviders) {
		return fmt.Errorf("invalid choice")
	}

	newProvider := availableProviders[choice-1]

	// Get available models for the new provider
	models := s.getAvailableModels(newProvider)
	if len(models) == 0 {
		return fmt.Errorf("no models available for provider %s", newProvider)
	}

	// Auto-select first model or let user choose if multiple
	var newModel string
	if len(models) == 1 {
		newModel = models[0]
	} else {
		fmt.Printf("\n🧠 Available Models for %s:\n", newProvider)
		fmt.Println("================================")
		for i, model := range models {
			fmt.Printf("[%d] %s\n", i+1, model)
		}

		fmt.Print("\nSelect model (number): ")
		if !scanner.Scan() {
			return fmt.Errorf("input cancelled")
		}

		modelChoice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || modelChoice < 1 || modelChoice > len(models) {
			return fmt.Errorf("invalid model choice")
		}

		newModel = models[modelChoice-1]
	}

	// Update configuration
	s.config.Provider = newProvider
	s.config.Model = newModel

	fmt.Printf("✅ Switched to %s with model %s\n", newProvider, newModel)
	return nil
}

// switchModel handles switching to a different model for the current provider
func (s *ChatSession) switchModel() error {
	models := s.getAvailableModels(s.config.Provider)
	if len(models) == 0 {
		return fmt.Errorf("no models available for provider %s", s.config.Provider)
	}

	if len(models) == 1 {
		fmt.Printf("Only one model available for %s: %s\n", s.config.Provider, models[0])
		return nil
	}

	fmt.Printf("\n🧠 Available Models for %s:\n", s.config.Provider)
	fmt.Println("===============================")
	for i, model := range models {
		status := ""
		if model == s.config.Model {
			status = " (current)"
		}
		fmt.Printf("[%d] %s%s\n", i+1, model, status)
	}

	fmt.Print("\nSelect model (number): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return fmt.Errorf("input cancelled")
	}

	choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || choice < 1 || choice > len(models) {
		return fmt.Errorf("invalid choice")
	}

	newModel := models[choice-1]
	s.config.Model = newModel

	fmt.Printf("✅ Switched to model %s\n", newModel)
	return nil
}

// showCurrentConfig displays the current configuration
func (s *ChatSession) showCurrentConfig() {
	fmt.Println("\n⚙️  Current Configuration:")
	fmt.Println("=========================")
	fmt.Printf("🔧 Provider: %s\n", s.config.Provider)
	fmt.Printf("🧠 Model: %s\n", s.config.Model)
	fmt.Printf("🔄 Agentic Mode: %t\n", s.config.MCPAgenticMode)
	fmt.Printf("🌡️  Temperature: %.1f\n", *s.config.Temperature)
	fmt.Printf("📝 Max Tokens: %d\n", *s.config.MaxTokens)
	fmt.Printf("🔧 Tool Execution: Manual approval required\n")
}

// AddUserMessage adds a user message to the conversation history
func (s *ChatSession) AddUserMessage(message string) {
	userMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: schemas.ChatMessageContent{
			ContentStr: &message,
		},
	}
	s.history = append(s.history, userMessage)
}

// SendMessage sends a message and returns the assistant's response
func (s *ChatSession) SendMessage(message string) (string, error) {
	// Add user message to history
	s.AddUserMessage(message)

	// Prepare model parameters
	params := &schemas.ModelParameters{}
	if s.config.Temperature != nil {
		params.Temperature = s.config.Temperature
	}
	if s.config.MaxTokens != nil {
		params.MaxTokens = s.config.MaxTokens
	}
	params.ToolChoice = &schemas.ToolChoice{
		ToolChoiceStr: stringPtr("auto"),
	}

	// Create request
	request := &schemas.BifrostRequest{
		Provider: s.config.Provider,
		Model:    s.config.Model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &s.history,
		},
		Params: params,
	}

	// Start loading animation
	stopChan, wg := startLoader()

	// Send request
	response, err := s.client.ChatCompletionRequest(context.Background(), request)

	// Stop loading animation
	stopLoader(stopChan, wg)

	if err != nil {
		return "", fmt.Errorf("chat completion failed: %s", err.Error.Message)
	}

	if response == nil || len(response.Choices) == 0 {
		return "", fmt.Errorf("no response received")
	}

	// Get the assistant's response
	choice := response.Choices[0]
	assistantMessage := choice.Message

	// Add assistant message to history
	s.history = append(s.history, assistantMessage)

	// Check if assistant wants to use tools
	if assistantMessage.ToolCalls != nil && len(*assistantMessage.ToolCalls) > 0 {
		return s.handleToolCalls(assistantMessage)
	}

	// Extract text content for regular responses
	var responseText string
	if assistantMessage.Content.ContentStr != nil {
		responseText = *assistantMessage.Content.ContentStr
	} else if assistantMessage.Content.ContentBlocks != nil {
		var textParts []string
		for _, block := range *assistantMessage.Content.ContentBlocks {
			if block.Text != nil {
				textParts = append(textParts, *block.Text)
			}
		}
		responseText = strings.Join(textParts, "\n")
	}

	return responseText, nil
}

// handleToolCalls handles tool execution using the new Bifrost MCP integration
func (s *ChatSession) handleToolCalls(assistantMessage schemas.ChatMessage) (string, error) {
	toolCalls := *assistantMessage.ToolCalls

	// Display tools to user for approval
	fmt.Println("\n🔧 Assistant wants to use the following tools:")
	fmt.Println("============================================")

	for i, toolCall := range toolCalls {
		fmt.Printf("[%d] Tool: %s\n", i+1, *toolCall.Function.Name)
		fmt.Printf("    Arguments: %s\n", toolCall.Function.Arguments)
		fmt.Println()
	}

	fmt.Print("Do you want to execute these tools? (y/n): ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "❌ Tool execution cancelled by user.", nil
	}

	input := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if input != "y" && input != "yes" {
		return "❌ Tool execution cancelled by user.", nil
	}

	fmt.Println("✅ Executing tools...")

	// Execute each tool using Bifrost's ExecuteMCPTool method
	toolResults := make([]schemas.ChatMessage, 0)
	for _, toolCall := range toolCalls {
		// Start loading animation for this tool
		stopChan, wg := startLoader()

		// Execute the tool using Bifrost's integrated MCP functionality
		toolResult, err := s.client.ExecuteMCPTool(context.Background(), toolCall)

		// Stop loading animation
		stopLoader(stopChan, wg)

		if err != nil {
			fmt.Printf("❌ Error executing tool %s: %v\n", *toolCall.Function.Name, err)
			// Create error message for this tool
			errorResult := schemas.ChatMessage{
				Role: schemas.ModelChatMessageRoleTool,
				Content: schemas.ChatMessageContent{
					ContentStr: stringPtr(fmt.Sprintf("Error executing tool: %v", err)),
				},
				ToolMessage: &schemas.ToolMessage{
					ToolCallID: toolCall.ID,
				},
			}
			toolResults = append(toolResults, errorResult)
		} else {
			fmt.Printf("✅ Tool %s executed successfully\n", *toolCall.Function.Name)
			toolResults = append(toolResults, *toolResult)
		}
	}

	// Add tool results to conversation history
	s.history = append(s.history, toolResults...)

	// If agentic mode is enabled, send conversation back to LLM for synthesis
	if s.config.MCPAgenticMode {
		return s.synthesizeToolResults()
	}

	// Non-agentic mode: return the results directly
	var responseText strings.Builder
	responseText.WriteString("🔧 Tool execution completed:\n\n")

	for i, result := range toolResults {
		if result.Content.ContentStr != nil {
			responseText.WriteString(fmt.Sprintf("Tool %d result: %s\n", i+1, *result.Content.ContentStr))
		}
	}

	return responseText.String(), nil
}

// synthesizeToolResults sends the conversation with tool results back to LLM for synthesis
func (s *ChatSession) synthesizeToolResults() (string, error) {
	// Add synthesis prompt
	synthesisPrompt := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: schemas.ChatMessageContent{
			ContentStr: stringPtr("Please provide a comprehensive response based on the tool results above."),
		},
	}

	// Temporarily add synthesis prompt for the request
	conversationWithSynthesis := append(s.history, synthesisPrompt)

	// Create synthesis request
	synthesisRequest := &schemas.BifrostRequest{
		Provider: s.config.Provider,
		Model:    s.config.Model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &conversationWithSynthesis,
		},
		Params: &schemas.ModelParameters{
			Temperature: s.config.Temperature,
			MaxTokens:   s.config.MaxTokens,
		},
	}

	fmt.Println("🤖 Synthesizing response...")

	// Start loading animation
	stopChan, wg := startLoader()

	// Send synthesis request
	synthesisResponse, err := s.client.ChatCompletionRequest(context.Background(), synthesisRequest)

	// Stop loading animation
	stopLoader(stopChan, wg)

	if err != nil {
		fmt.Printf("⚠️ Synthesis failed: %v. Returning tool results directly.\n", err)
		// Fallback to direct tool results
		var responseText strings.Builder
		responseText.WriteString("🔧 Tool execution completed (synthesis failed):\n\n")

		// Get tool results from history (last few messages that are tool messages)
		for i := len(s.history) - 1; i >= 0; i-- {
			if s.history[i].Role == schemas.ModelChatMessageRoleTool {
				if s.history[i].Content.ContentStr != nil {
					responseText.WriteString(fmt.Sprintf("Tool result: %s\n", *s.history[i].Content.ContentStr))
				}
			} else {
				break // Stop when we hit non-tool messages
			}
		}

		return responseText.String(), nil
	}

	if synthesisResponse == nil || len(synthesisResponse.Choices) == 0 {
		return "❌ No synthesis response received", nil
	}

	// Get synthesized response
	synthesizedMessage := synthesisResponse.Choices[0].Message

	// Add synthesized response to history (replace the temporary synthesis prompt effect)
	s.history = append(s.history, synthesizedMessage)

	// Extract text content
	var responseText string
	if synthesizedMessage.Content.ContentStr != nil {
		responseText = *synthesizedMessage.Content.ContentStr
	} else if synthesizedMessage.Content.ContentBlocks != nil {
		var textParts []string
		for _, block := range *synthesizedMessage.Content.ContentBlocks {
			if block.Text != nil {
				textParts = append(textParts, *block.Text)
			}
		}
		responseText = strings.Join(textParts, "\n")
	}

	return responseText, nil
}

// PrintHistory prints the conversation history
func (s *ChatSession) PrintHistory() {
	fmt.Println("\n📜 Conversation History:")
	fmt.Println("========================")

	for i, msg := range s.history {
		if msg.Role == schemas.ModelChatMessageRoleSystem {
			continue // Skip system messages in history display
		}

		var content string
		if msg.Content.ContentStr != nil {
			content = *msg.Content.ContentStr
		} else if msg.Content.ContentBlocks != nil {
			var textParts []string
			for _, block := range *msg.Content.ContentBlocks {
				if block.Text != nil {
					textParts = append(textParts, *block.Text)
				}
			}
			content = strings.Join(textParts, "\n")
		}

		role := cases.Title(language.English).String(string(msg.Role))
		timestamp := fmt.Sprintf("[%d]", i)

		fmt.Printf("%s %s: %s\n\n", timestamp, role, content)
	}
}

// Cleanup closes the chat session and cleans up resources
func (s *ChatSession) Cleanup() {
	if s.client != nil {
		s.client.Shutdown()
	}
}

// printWelcome prints the welcome message and instructions
func printWelcome(config ChatbotConfig) {
	fmt.Println("🤖 Bifrost CLI Chatbot")
	fmt.Println("======================")
	fmt.Printf("🔧 Provider: %s\n", config.Provider)
	fmt.Printf("🧠 Model: %s\n", config.Model)
	fmt.Printf("🔄 Agentic Mode: %t\n", config.MCPAgenticMode)
	fmt.Printf("🔧 Tool Execution: Manual approval required\n")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  /help      - Show this help message")
	fmt.Println("  /history   - Show conversation history")
	fmt.Println("  /clear     - Clear conversation history")
	fmt.Println("  /config    - Show current configuration")
	fmt.Println("  /provider  - Switch provider")
	fmt.Println("  /model     - Switch model")
	fmt.Println("  /quit      - Exit the chatbot")
	fmt.Println()
	fmt.Println("Type your message and press Enter to chat!")
	fmt.Println("When the assistant wants to use tools, you'll be asked to approve them.")
	fmt.Println("==========================================")
}

// printHelp prints help information
func printHelp() {
	fmt.Println("\n📖 Help")
	fmt.Println("========")
	fmt.Println("Available commands:")
	fmt.Println("  /help      - Show this help message")
	fmt.Println("  /history   - Show conversation history")
	fmt.Println("  /clear     - Clear conversation history (keeps system prompt)")
	fmt.Println("  /config    - Show current provider, model, and settings")
	fmt.Println("  /provider  - Switch between different AI providers")
	fmt.Println("  /model     - Switch between models for current provider")
	fmt.Println("  /quit      - Exit the chatbot")
	fmt.Println()
	fmt.Println("Supported providers:")
	fmt.Println("• OpenAI (gpt-4o-mini, gpt-4-turbo, gpt-4o)")
	fmt.Println("• Anthropic (claude models)")
	fmt.Println("• Bedrock (AWS hosted models)")
	fmt.Println("• Cohere (command models)")
	fmt.Println("• Azure (Azure OpenAI models)")
	fmt.Println("• Vertex (Google Cloud models)")
	fmt.Println("• Mistral (mistral models)")
	fmt.Println("• Ollama (local models)")
	fmt.Println()
	fmt.Println("Tool execution:")
	fmt.Println("• When the assistant wants to use tools, you'll be asked to approve them")
	fmt.Println("• You can review the tool names and arguments before approving")
	fmt.Println("• Available tools include web search and Context7")
	fmt.Println("• In agentic mode, tool results are synthesized into natural responses")
	fmt.Println("• In non-agentic mode, raw tool results are displayed")
	fmt.Println()
}

// stringPtr is a helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}

// startLoader starts a loading spinner animation
func startLoader() (chan bool, *sync.WaitGroup) {
	stopChan := make(chan bool)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0

		for {
			select {
			case <-stopChan:
				// Clear the spinner
				fmt.Print("\r\033[K") // Clear current line
				return
			default:
				fmt.Printf("\r🤖 Assistant: %s Thinking...", spinner[i%len(spinner)])
				i++
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	return stopChan, &wg
}

// stopLoader stops the loading animation
func stopLoader(stopChan chan bool, wg *sync.WaitGroup) {
	close(stopChan)
	wg.Wait()
}

func main() {
	// Check for required environment variables
	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Println("❌ Error: OPENAI_API_KEY environment variable is required")
		fmt.Println("💡 Set additional provider API keys to access more models:")
		fmt.Println("   - ANTHROPIC_API_KEY for Claude models")
		fmt.Println("   - COHERE_API_KEY for Cohere models")
		fmt.Println("   - MISTRAL_API_KEY for Mistral models")
		fmt.Println("   - AWS credentials for Bedrock")
		fmt.Println("   - AZURE_API_KEY and AZURE_ENDPOINT for Azure OpenAI")
		fmt.Println("   - VERTEX_PROJECT_ID and credentials for Vertex AI")
		os.Exit(1)
	}

	// Default configuration
	config := ChatbotConfig{
		Provider:       schemas.OpenAI,
		Model:          "gpt-4o-mini",
		MCPAgenticMode: true,
		MCPServerPort:  8585,
		Temperature:    bifrost.Ptr(0.7),
		MaxTokens:      bifrost.Ptr(1000),
	}

	// Create chat session
	fmt.Println("🚀 Starting Bifrost CLI Chatbot...")
	session, err := NewChatSession(config)
	if err != nil {
		fmt.Printf("❌ Failed to create chat session: %v\n", err)
		os.Exit(1)
	}

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n\n👋 Goodbye! Cleaning up...")
		session.Cleanup()
		os.Exit(0)
	}()

	// Give MCP servers time to initialize
	fmt.Println("⏳ Waiting for MCP servers to initialize...")
	time.Sleep(3 * time.Second)

	// Print welcome message
	printWelcome(config)

	// Main chat loop
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n💬 You: ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// Handle commands
		switch input {
		case "/help":
			printHelp()
			continue
		case "/history":
			session.PrintHistory()
			continue
		case "/clear":
			// Keep system prompt but clear conversation history
			systemPrompt := session.history[0] // Assuming first message is system
			session.history = []schemas.ChatMessage{systemPrompt}
			fmt.Println("🧹 Conversation history cleared!")
			continue
		case "/config":
			session.showCurrentConfig()
			continue
		case "/provider":
			if err := session.switchProvider(); err != nil {
				fmt.Printf("❌ Error switching provider: %v\n", err)
			}
			continue
		case "/model":
			if err := session.switchModel(); err != nil {
				fmt.Printf("❌ Error switching model: %v\n", err)
			}
			continue
		case "/quit":
			fmt.Println("👋 Goodbye!")
			session.Cleanup()
			return
		}

		// Send message and get response
		response, err := session.SendMessage(input)
		if err != nil {
			fmt.Printf("\r🤖 Assistant: ❌ Error: %v\n", err)
			continue
		}

		fmt.Printf("🤖 Assistant: %s\n", response)
	}

	// Cleanup
	session.Cleanup()
}
