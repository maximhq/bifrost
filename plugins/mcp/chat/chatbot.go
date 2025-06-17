package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	mcp "github.com/maximhq/bifrost/plugins/mcp"
)

// ChatbotConfig holds configuration for the chatbot
type ChatbotConfig struct {
	Provider       schemas.ModelProvider
	Model          string
	MCPAgenticMode bool
	MCPServerPort  string
	EnableMaximMCP bool
	Temperature    *float64
	MaxTokens      *int
}

// ChatSession manages the conversation state
type ChatSession struct {
	history      []schemas.BifrostMessage
	client       *bifrost.Bifrost
	mcpPlugin    *mcp.MCPPlugin
	config       ChatbotConfig
	systemPrompt string
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

// NewChatSession creates a new chat session with the given configuration
func NewChatSession(config ChatbotConfig) (*ChatSession, error) {
	// Create MCP plugin with client-level execution policies
	mcpConfig := mcp.MCPPluginConfig{
		ServerPort:  config.MCPServerPort,
		AgenticMode: config.MCPAgenticMode,
		ClientConfigs: []mcp.ClientExecutionConfig{
			// All clients require approval by default for safety
			{
				Name:          "maxim-mcp",
				DefaultPolicy: mcp.ToolExecutionPolicyRequireApproval,
				ToolPolicies:  map[string]mcp.ToolExecutionPolicy{},                       // Can override specific tools here
				ToolsToSkip:   []string{"get-current-utc-time", "get-maxim-workspace-id"}, // Skip these tools for this client
			},
			{
				Name:          "serper-web-search-mcp",
				DefaultPolicy: mcp.ToolExecutionPolicyAutoExecute,
				ToolPolicies:  map[string]mcp.ToolExecutionPolicy{}, // Can override specific tools here
				ToolsToSkip:   []string{},                           // No tools to skip for this client
			},
			{
				Name:          "context7",
				DefaultPolicy: mcp.ToolExecutionPolicyAutoExecute,
				ToolPolicies:  map[string]mcp.ToolExecutionPolicy{}, // Can override specific tools here
				ToolsToSkip:   []string{},                           // No tools to skip for this client
			},
		},
	}

	mcpPlugin, err := mcp.NewMCPPlugin(mcpConfig, nil) // nil logger will use default
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP plugin: %w", err)
	}

	// Connect to external MCP servers based on config
	if config.EnableMaximMCP {
		fmt.Println("🔌 Connecting to Maxim MCP server...")
		err = mcpPlugin.ConnectToExternalMCP(mcp.ExternalMCPConfig{
			Name:           "maxim-mcp",
			ConnectionType: mcp.ConnectionTypeSTDIO,
			StdioConfig: &mcp.StdioConfig{
				Command: "npx",
				Args:    []string{"-y", "@maximai/mcp-server@latest"},
			},
		})
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to connect to Maxim MCP: %v\n", err)
		} else {
			fmt.Println("✅ Connected to Maxim MCP server")
		}
	}

	fmt.Println("🔌 Connecting to Serper MCP server...")
	err = mcpPlugin.ConnectToExternalMCP(mcp.ExternalMCPConfig{
		Name:           "serper-web-search-mcp",
		ConnectionType: mcp.ConnectionTypeSTDIO,
		StdioConfig: &mcp.StdioConfig{
			Command: "npx",
			Args:    []string{"-y", "serper-search-scrape-mcp-server"},
		},
	})
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to connect to Serper MCP: %v\n", err)
	} else {
		fmt.Println("✅ Connected to Serper MCP server")
	}

	fmt.Println("🔌 Connecting to Context7 MCP server...")
	err = mcpPlugin.ConnectToExternalMCP(mcp.ExternalMCPConfig{
		Name:           "context7",
		ConnectionType: mcp.ConnectionTypeSTDIO,
		StdioConfig: &mcp.StdioConfig{
			Command: "npx",
			Args:    []string{"-y", "@upstash/context7-mcp"},
		},
	})
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to connect to Context7 MCP: %v\n", err)
	} else {
		fmt.Println("✅ Connected to Context7 MCP server")
	}

	// Initialize Bifrost
	account := &BaseAccount{}
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: account,
		Plugins: []schemas.Plugin{mcpPlugin},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelInfo),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Bifrost: %w", err)
	}

	// Set Bifrost client for agentic mode
	if config.MCPAgenticMode {
		mcpPlugin.SetBifrostClient(client)
	}

	session := &ChatSession{
		history:   make([]schemas.BifrostMessage, 0),
		client:    client,
		mcpPlugin: mcpPlugin,
		config:    config,
		systemPrompt: "You are a helpful AI assistant with access to various tools. " +
			"Use the available tools when they can help answer the user's questions more accurately or provide additional information.",
	}

	// Add system message to history
	if session.systemPrompt != "" {
		session.history = append(session.history, schemas.BifrostMessage{
			Role: schemas.ModelChatMessageRoleSystem,
			Content: schemas.MessageContent{
				ContentStr: &session.systemPrompt,
			},
		})
	}

	return session, nil
}

// AddUserMessage adds a user message to the conversation history
func (s *ChatSession) AddUserMessage(message string) {
	userMessage := schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
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

	// Check if response contains pending tools (requiring user approval)
	if response.ExtraFields.PendingMCPTools != nil && len(*response.ExtraFields.PendingMCPTools) > 0 {
		return s.handlePendingTools(response)
	}

	// Get the assistant's response
	choice := response.Choices[0]
	assistantMessage := choice.Message

	// Add assistant message to history
	s.history = append(s.history, assistantMessage)

	// Extract text content
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

// handlePendingTools handles the safe mode flow by showing pending tools and asking for approval
func (s *ChatSession) handlePendingTools(response *schemas.BifrostResponse) (string, error) {
	pendingTools := *response.ExtraFields.PendingMCPTools

	// Store the assistant message with tool calls but DON'T add to history yet
	// We'll add the final synthesized response instead
	choice := response.Choices[0]
	assistantMessage := choice.Message

	// Display pending tools to user
	fmt.Println("\n🔒 Safe Mode: The following tools require your approval:")
	fmt.Println("=====================================================")

	for i, tool := range pendingTools {
		fmt.Printf("[%d] Tool: %s\n", i+1, tool.Tool.Function.Name)
		fmt.Printf("    Client: %s\n", tool.ClientName)
		fmt.Printf("    Arguments: %+v\n", tool.ToolCall.Function.Arguments)
		fmt.Println()
	}

	fmt.Print("Do you want to approve these tools? (y/n): ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "❌ No input received. Tool execution cancelled.", nil
	}

	input := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if input != "y" && input != "yes" {
		return "❌ Tool execution cancelled by user.", nil
	}

	// First, create tool response messages for the approved tools
	// This ensures proper conversation flow
	toolResponseMessages := make([]schemas.BifrostMessage, 0)

	for _, pendingTool := range pendingTools {
		// Create tool response message placeholder
		// The actual execution will happen in the MCP plugin, but we need these in history
		toolMsg := schemas.BifrostMessage{
			Role: schemas.ModelChatMessageRoleTool,
			Content: schemas.MessageContent{
				ContentStr: stringPtr("Tool execution approved - executing " + pendingTool.Tool.Function.Name),
			},
			ToolMessage: &schemas.ToolMessage{
				ToolCallID: pendingTool.ToolCall.ID,
			},
		}
		toolResponseMessages = append(toolResponseMessages, toolMsg)
	}

	// Note: We don't add tool response messages to persistent history here
	// The final synthesized response will be added instead

	// Create approved tools list using just the tool names
	// The MCP plugin will match these by name to execute the approved tools
	approvedTools := make([]schemas.Tool, 0)
	for _, pendingTool := range pendingTools {
		approvedTools = append(approvedTools, pendingTool.Tool)
	}

	// Create conversation history for approved request
	// Include the assistant message with tool calls, but don't add it to our persistent history yet
	conversationForApproval := append(s.history, assistantMessage)
	conversationForApproval = append(conversationForApproval, toolResponseMessages...)

	// Create new request with approved tools
	approvedRequest := &schemas.BifrostRequest{
		Provider: s.config.Provider,
		Model:    s.config.Model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &conversationForApproval,
		},
		MCPTools: &approvedTools,
	}

	fmt.Println("✅ Tools approved")

	// Start loading animation for execution
	stopChan, wg := startLoader()

	// Send approved request
	approvedResponse, err := s.client.ChatCompletionRequest(context.Background(), approvedRequest)

	// Stop loading animation
	stopLoader(stopChan, wg)

	if err != nil {
		return "", fmt.Errorf("approved tool execution failed: %s", err.Error.Message)
	}

	if approvedResponse == nil || len(approvedResponse.Choices) == 0 {
		return "", fmt.Errorf("no response received from approved execution")
	}

	// Get the final response
	finalChoice := approvedResponse.Choices[0]
	finalMessage := finalChoice.Message

	// Replace the placeholder tool messages with the actual response
	// In agentic mode, this will be a synthesized response
	// In non-agentic mode, this will be the tool result
	if finalMessage.Role == schemas.ModelChatMessageRoleAssistant {
		// This is a synthesized response from agentic mode
		s.history = append(s.history, finalMessage)
	} else {
		// This might be a tool message in non-agentic mode, replace the last placeholder
		if len(s.history) > 0 && s.history[len(s.history)-1].Role == schemas.ModelChatMessageRoleTool {
			s.history[len(s.history)-1] = finalMessage
		} else {
			s.history = append(s.history, finalMessage)
		}
	}

	// Extract text content
	var responseText string
	if finalMessage.Content.ContentStr != nil {
		responseText = *finalMessage.Content.ContentStr
	} else if finalMessage.Content.ContentBlocks != nil {
		var textParts []string
		for _, block := range *finalMessage.Content.ContentBlocks {
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

		role := strings.Title(string(msg.Role))
		timestamp := fmt.Sprintf("[%d]", i)

		fmt.Printf("%s %s: %s\n\n", timestamp, role, content)
	}
}

// Cleanup closes the chat session and cleans up resources
func (s *ChatSession) Cleanup() {
	if s.client != nil {
		s.client.Cleanup()
	}
	if s.mcpPlugin != nil {
		s.mcpPlugin.Cleanup()
	}
}

// printWelcome prints the welcome message and instructions
func printWelcome(config ChatbotConfig) {
	fmt.Println("🤖 Bifrost CLI Chatbot")
	fmt.Println("======================")
	fmt.Printf("🔧 Provider: %s\n", config.Provider)
	fmt.Printf("🧠 Model: %s\n", config.Model)
	fmt.Printf("🔄 Agentic Mode: %t\n", config.MCPAgenticMode)
	fmt.Printf("🔒 Tool Execution: Client-level policies (secure by default)\n")
	if config.EnableMaximMCP {
		fmt.Println("🛠️  Maxim MCP tools enabled")
	}
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  /help    - Show this help message")
	fmt.Println("  /history - Show conversation history")
	fmt.Println("  /clear   - Clear conversation history")
	fmt.Println("  /quit    - Exit the chatbot")
	fmt.Println()
	fmt.Println("Type your message and press Enter to chat!")
	fmt.Println("==========================================")
}

// printHelp prints help information
func printHelp() {
	fmt.Println("\n📖 Help")
	fmt.Println("========")
	fmt.Println("Available commands:")
	fmt.Println("  /help    - Show this help message")
	fmt.Println("  /history - Show conversation history")
	fmt.Println("  /clear   - Clear conversation history (keeps system prompt)")
	fmt.Println("  /quit    - Exit the chatbot")
	fmt.Println()
	fmt.Println("The chatbot has access to various tools depending on configuration:")
	fmt.Println("• Maxim MCP tools for data operations")
	fmt.Println("• DuckDuckGo search for web information")
	fmt.Println("• In agentic mode, tool results are automatically synthesized")
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
		os.Exit(1)
	}

	// Default configuration
	config := ChatbotConfig{
		Provider:       schemas.OpenAI,
		Model:          "gpt-4o-mini",
		MCPAgenticMode: true,
		MCPServerPort:  ":8585",
		EnableMaximMCP: true,
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

	// Give MCP servers time to start
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
			session.history = []schemas.BifrostMessage{systemPrompt}
			fmt.Println("🧹 Conversation history cleared!")
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
