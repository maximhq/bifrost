package llmtests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCrossProviderScenarios(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping cross provider scenarios test")
	return

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Define available providers for cross-provider testing
	providers := []ProviderConfig{
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
	testConfig := CrossProviderTestConfig{
		Providers: providers,
		ConversationSettings: ConversationSettings{
			MaxMessages:                25,
			ConversationGeneratorModel: "gpt-4o",
			RequiredMessageTypes: []MessageModality{
				ModalityText,
				ModalityTool,
				ModalityVision,
			},
		},
		TestSettings: TestSettings{
			EnableRetries:        true,
			MaxRetriesPerMessage: 2,
			ValidationStrength:   ValidationModerate,
		},
	}

	// Get predefined scenarios
	scenariosList := GetPredefinedScenarios()

	for _, scenario := range scenariosList {
		// Test each scenario with both Chat Completions and Responses API
		t.Run(scenario.Name+"_ChatCompletions", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			RunCrossProviderScenarioTest(t, client, bfCtx, testConfig, scenario, false) // false = Chat Completions API
		})

		t.Run(scenario.Name+"_ResponsesAPI", func(t *testing.T) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			RunCrossProviderScenarioTest(t, client, bfCtx, testConfig, scenario, true) // true = Responses API
		})
	}
}

func TestCrossProviderConsistency(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping cross provider consistency test")
	return

	client, ctx, cancel, err := SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	providers := []ProviderConfig{
		{Provider: schemas.OpenAI, ChatModel: "gpt-4o-mini", Available: true},
		{Provider: schemas.Anthropic, ChatModel: "claude-3-5-sonnet-20241022", Available: true},
		{Provider: schemas.Groq, ChatModel: "llama-3.1-70b-versatile", Available: true},
		{Provider: schemas.Gemini, ChatModel: "gemini-1.5-pro", Available: true},
	}

	testConfig := CrossProviderTestConfig{
		Providers: providers,
		TestSettings: TestSettings{
			ValidationStrength: ValidationLenient, // More lenient for consistency testing
		},
	}

	// Test same prompt across different providers
	t.Run("SamePrompt_DifferentProviders_ChatCompletions", func(t *testing.T) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		RunCrossProviderConsistencyTest(t, client, bfCtx, testConfig, false) // Chat Completions
	})

	t.Run("SamePrompt_DifferentProviders_ResponsesAPI", func(t *testing.T) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		RunCrossProviderConsistencyTest(t, client, bfCtx, testConfig, true) // Responses API
	})
}
