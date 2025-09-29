package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"
	"github.com/maximhq/bifrost/tests/core-providers/scenarios"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCrossProviderScenarios(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Define available providers for cross-provider testing
	providers := []scenarios.ProviderConfig{
		{
			Provider:        schemas.OpenAI,
			ChatModel:       "gpt-4o-mini",
			VisionModel:     "gpt-4o",
			ToolsSupported:  true,
			VisionSupported: true,
			StreamSupported: true,
			Available:       true,
		},
		{
			Provider:        schemas.Anthropic,
			ChatModel:       "claude-3-5-sonnet-20241022",
			VisionModel:     "claude-3-5-sonnet-20241022",
			ToolsSupported:  true,
			VisionSupported: true,
			StreamSupported: true,
			Available:       true,
		},
		{
			Provider:        schemas.Groq,
			ChatModel:       "llama-3.1-70b-versatile",
			VisionModel:     "", // No vision support
			ToolsSupported:  true,
			VisionSupported: false,
			StreamSupported: true,
			Available:       true,
		},
		{
			Provider:        schemas.Gemini,
			ChatModel:       "gemini-1.5-pro",
			VisionModel:     "gemini-1.5-pro",
			ToolsSupported:  true,
			VisionSupported: true,
			StreamSupported: true,
			Available:       true,
		},
		{
			Provider:        schemas.Bedrock,
			ChatModel:       "claude-sonnet-4",
			VisionModel:     "claude-sonnet-4",
			ToolsSupported:  true,
			VisionSupported: true,
			StreamSupported: false,
			Available:       true,
		},
		{
			Provider:        schemas.Vertex,
			ChatModel:       "gemini-1.5-pro",
			VisionModel:     "gemini-1.5-pro",
			ToolsSupported:  true,
			VisionSupported: true,
			StreamSupported: false,
			Available:       true,
		},
	}

	// Test configuration
	testConfig := scenarios.CrossProviderTestConfig{
		Providers: providers,
		ConversationSettings: scenarios.ConversationSettings{
			MaxMessages:                25,
			ConversationGeneratorModel: "gpt-4o",
			RequiredMessageTypes: []scenarios.MessageModality{
				scenarios.ModalityText,
				scenarios.ModalityTool,
				scenarios.ModalityVision,
			},
		},
		TestSettings: scenarios.TestSettings{
			EnableRetries:        true,
			MaxRetriesPerMessage: 2,
			ValidationStrength:   scenarios.ValidationModerate,
		},
	}

	// Get predefined scenarios
	scenariosList := scenarios.GetPredefinedScenarios()

	for _, scenario := range scenariosList {
		// Test each scenario with both Chat Completions and Responses API
		t.Run(scenario.Name+"_ChatCompletions", func(t *testing.T) {
			scenarios.RunCrossProviderScenarioTest(t, client, ctx, testConfig, scenario, false) // false = Chat Completions API
		})

		t.Run(scenario.Name+"_ResponsesAPI", func(t *testing.T) {
			scenarios.RunCrossProviderScenarioTest(t, client, ctx, testConfig, scenario, true) // true = Responses API
		})
	}
}

func TestCrossProviderConsistency(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	providers := []scenarios.ProviderConfig{
		{Provider: schemas.OpenAI, ChatModel: "gpt-4o-mini", Available: true},
		{Provider: schemas.Anthropic, ChatModel: "claude-3-5-sonnet-20241022", Available: true},
		{Provider: schemas.Groq, ChatModel: "llama-3.1-70b-versatile", Available: true},
		{Provider: schemas.Gemini, ChatModel: "gemini-1.5-pro", Available: true},
	}

	testConfig := scenarios.CrossProviderTestConfig{
		Providers: providers,
		TestSettings: scenarios.TestSettings{
			ValidationStrength: scenarios.ValidationLenient, // More lenient for consistency testing
		},
	}

	// Test same prompt across different providers
	t.Run("SamePrompt_DifferentProviders_ChatCompletions", func(t *testing.T) {
		scenarios.RunCrossProviderConsistencyTest(t, client, ctx, testConfig, false) // Chat Completions
	})

	t.Run("SamePrompt_DifferentProviders_ResponsesAPI", func(t *testing.T) {
		scenarios.RunCrossProviderConsistencyTest(t, client, ctx, testConfig, true) // Responses API
	})
}
