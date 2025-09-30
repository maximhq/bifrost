package tests

import (
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenRouter(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.OpenRouter,
		ChatModel: "openai/gpt-4o",
		TextModel: "google/gemini-2.5-flash",
		EmbeddingModel: "",
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}

// TestOpenRouterAnthropic tests Anthropic models via OpenRouter
func TestOpenRouterAnthropic(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.OpenRouter,
		ChatModel: "anthropic/claude-3.5-sonnet", // Using Claude 3.5 Sonnet via OpenRouter
		TextModel: "", // Anthropic models don't support text completion
		EmbeddingModel: "",
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported by Anthropic
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      false, // Skip provider-specific tests
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}

// TestOpenRouterMetaLlama tests Meta's Llama models via OpenRouter
func TestOpenRouterMetaLlama(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:  schemas.OpenRouter,
		ChatModel: "meta-llama/llama-3.1-70b-instruct", // Using Llama 3.1 70B via OpenRouter
		TextModel: "meta-llama/llama-3.1-8b-instruct",  // Using smaller model for text completion
		EmbeddingModel: "",
		Scenarios: config.TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Llama models typically don't support image inputs
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			ProviderSpecific:      false,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}