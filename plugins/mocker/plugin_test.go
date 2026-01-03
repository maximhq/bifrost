package mocker

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// BaseAccount implements the schemas.Account interface for testing purposes.
// It provides mock implementations of the required methods to test the Mocker plugin
// with a basic OpenAI configuration.
type BaseAccount struct{}

// GetConfiguredProviders returns a list of supported providers for testing.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

// GetKeysForProvider returns a dummy API key configuration for testing.
// Since we're testing the mocker plugin, these keys should never be used
// as the plugin intercepts requests before they reach the actual providers.
func (baseAccount *BaseAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  *schemas.NewEnvVar("dummy-api-key-for-testing"), // Dummy key
			Models: []string{"gpt-4", "gpt-4-turbo", "claude-3"},
			Weight: 1.0,
		},
	}, nil
}

// GetConfigForProvider returns default provider configuration for testing.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// TestMockerPlugin_GetName tests the plugin name
func TestMockerPlugin_GetName(t *testing.T) {
	plugin, err := Init(MockerConfig{})
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}
	if plugin.GetName() != PluginName {
		t.Errorf("Expected '%s', got '%s'", PluginName, plugin.GetName())
	}
}

// TestMockerPlugin_Disabled tests that disabled plugin doesn't interfere
func TestMockerPlugin_Disabled(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := MockerConfig{
		Enabled: false,
	}
	plugin, err := Init(config)
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}

	account := BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	// This should pass through to the real provider (but will fail due to dummy key)
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, test message"),
				},
			},
		},
	})

	// Should get an authentication error from OpenAI, not a mock response
	// This proves the plugin is disabled and not intercepting requests
	if bifrostErr == nil {
		t.Error("Expected error from real provider with dummy API key")
	}
}

// TestMockerPlugin_DefaultMockRule tests the default catch-all rule
func TestMockerPlugin_DefaultMockRule(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := MockerConfig{
		Enabled: true, // No rules provided, should create default rule
	}
	plugin, err := Init(config)
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}

	account := BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	response, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, test message"),
				},
			},
		},
	})

	if bifrostErr != nil {
		t.Fatalf("Expected no error, got: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("Expected response")
	}
	if len(response.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	if response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr == nil {
		t.Fatal("Expected content string")
	}
	if *response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr != "This is a mock response from the Mocker plugin" {
		t.Errorf("Expected default mock message, got: %s", *response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
	}
}

// TestMockerPlugin_CustomSuccessRule tests custom success response
func TestMockerPlugin_CustomSuccessRule(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "openai-success",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions: Conditions{
					Providers: []string{"openai"},
				},
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							Message: "Custom OpenAI mock response",
							Usage: &Usage{
								PromptTokens:     15,
								CompletionTokens: 25,
								TotalTokens:      40,
							},
						},
					},
				},
			},
		},
	}
	plugin, err := Init(config)
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}

	account := BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	response, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, test message"),
				},
			},
		},
	})

	if bifrostErr != nil {
		t.Fatalf("Expected no error, got: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("Expected response")
	}
	if len(response.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	if response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr == nil {
		t.Fatal("Expected content string")
	}
	if *response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr != "Custom OpenAI mock response" {
		t.Errorf("Expected custom message, got: %s", *response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
	}
	if response.Usage.TotalTokens != 40 {
		t.Errorf("Expected 40 total tokens, got %d", response.Usage.TotalTokens)
	}
}

// TestMockerPlugin_ErrorResponse tests error response generation
func TestMockerPlugin_ErrorResponse(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	allowFallbacks := false
	config := MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "rate-limit-error",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions: Conditions{
					Providers: []string{"openai"},
				},
				Responses: []Response{
					{
						Type:           ResponseTypeError,
						AllowFallbacks: &allowFallbacks,
						Error: &ErrorResponse{
							Message:    "Rate limit exceeded",
							Type:       bifrost.Ptr("rate_limit"),
							Code:       bifrost.Ptr("429"),
							StatusCode: bifrost.Ptr(429),
						},
					},
				},
			},
		},
	}
	plugin, err := Init(config)
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}

	account := BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, test message"),
				},
			},
		},
	})

	if bifrostErr == nil {
		t.Fatal("Expected error response")
	}
	if bifrostErr.Error.Message != "Rate limit exceeded" {
		t.Errorf("Expected 'Rate limit exceeded', got: %s", bifrostErr.Error.Message)
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 429 {
		t.Errorf("Expected status code 429, got: %v", bifrostErr.StatusCode)
	}
}

// TestMockerPlugin_MessageTemplate tests template variable substitution
func TestMockerPlugin_MessageTemplate(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "template-test",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions:  Conditions{}, // Match all
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							MessageTemplate: bifrost.Ptr("Hello from {{provider}} using model {{model}}"),
						},
					},
				},
			},
		},
	}
	plugin, err := Init(config)
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}

	account := BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	response, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-3",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello, test message"),
				},
			},
		},
	})

	if bifrostErr != nil {
		t.Fatalf("Expected no error, got: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("Expected response")
	}
	if len(response.Choices) == 0 {
		t.Fatal("Expected at least one choice")
	}
	if response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr == nil {
		t.Fatal("Expected content string")
	}
	expectedMessage := "Hello from anthropic using model claude-3"
	if *response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr != expectedMessage {
		t.Errorf("Expected '%s', got: %s", expectedMessage, *response.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
	}
}

// TestMockerPlugin_Statistics tests plugin statistics tracking
func TestMockerPlugin_Statistics(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := MockerConfig{
		Enabled: true,
		Rules: []MockRule{
			{
				Name:        "stats-test",
				Enabled:     true,
				Priority:    100,
				Probability: 1.0,
				Conditions:  Conditions{}, // Match all
				Responses: []Response{
					{
						Type: ResponseTypeSuccess,
						Content: &SuccessResponse{
							Message: "Stats test response",
						},
					},
				},
			},
		},
	}
	plugin, err := Init(config)
	if err != nil {
		t.Fatalf("Expected no error creating plugin, got: %v", err)
	}

	account := BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:    &account,
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	// Make multiple requests
	for i := 0; i < 3; i++ {
		_, _ = client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Hello, test message"),
					},
				},
			},
		})
	}

	// Check statistics
	stats := plugin.GetStats()
	if stats.TotalRequests != 3 {
		t.Errorf("Expected 3 total requests, got %d", stats.TotalRequests)
	}
	if stats.MockedRequests != 3 {
		t.Errorf("Expected 3 mocked requests, got %d", stats.MockedRequests)
	}
	if stats.ResponsesGenerated != 3 {
		t.Errorf("Expected 3 responses generated, got %d", stats.ResponsesGenerated)
	}
	if stats.RuleHits["stats-test"] != 3 {
		t.Errorf("Expected 3 hits for 'stats-test' rule, got %d", stats.RuleHits["stats-test"])
	}
}

// TestMockerPlugin_ValidationErrors tests configuration validation
func TestMockerPlugin_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		config      MockerConfig
		expectError bool
	}{
		{
			name: "invalid default behavior",
			config: MockerConfig{
				Enabled:         true,
				DefaultBehavior: "invalid",
			},
			expectError: true,
		},
		{
			name: "missing rule name",
			config: MockerConfig{
				Enabled: true,
				Rules: []MockRule{
					{
						Name:    "", // Missing name
						Enabled: true,
						Responses: []Response{
							{
								Type: ResponseTypeSuccess,
								Content: &SuccessResponse{
									Message: "test",
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name: "invalid probability",
			config: MockerConfig{
				Enabled: true,
				Rules: []MockRule{
					{
						Name:        "test",
						Enabled:     true,
						Probability: 1.5, // Invalid probability > 1
						Responses: []Response{
							{
								Type: ResponseTypeSuccess,
								Content: &SuccessResponse{
									Message: "test",
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name: "valid configuration",
			config: MockerConfig{
				Enabled:         true,
				DefaultBehavior: DefaultBehaviorPassthrough,
				Rules: []MockRule{
					{
						Name:        "valid-rule",
						Enabled:     true,
						Probability: 0.5,
						Responses: []Response{
							{
								Type: ResponseTypeSuccess,
								Content: &SuccessResponse{
									Message: "Valid response",
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Init(tt.config)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}
		})
	}
}
