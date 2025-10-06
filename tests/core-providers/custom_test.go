package tests

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestCustomProvider(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       config.ProviderOpenAICustom,
		ChatModel:      "llama-3.3-70b-versatile",
		TextModel:      "", // OpenAI doesn't support text completion in newer models
		EmbeddingModel: "", // groq custom base: embeddings not supported
		Scenarios: config.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false,
		},
	}

	runAllComprehensiveTests(t, client, ctx, testConfig)
}

func TestCustomProvider_DisallowedOperation(t *testing.T) {
	// Skip test if required API key is not available
	if os.Getenv("GROQ_API_KEY") == "" {
		t.Skipf("skipping test: GROQ_API_KEY not set")
	}

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Create a speech request to the custom provider
	prompt := "The future of artificial intelligence is"
	request := &schemas.BifrostSpeechRequest{
		Provider: config.ProviderOpenAICustom, // Use the custom provider
		Model:    "llama-3.3-70b-versatile",   // Use a model that exists for this provider
		Input: &schemas.SpeechInput{
			Input: prompt,
		},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: bifrost.Ptr("alloy"),
			},
			ResponseFormat: "mp3",
		},
	}

	// Attempt to make a speech stream request
	response, bifrostErr := client.SpeechStreamRequest(ctx, request)

	// Assert that the request failed with an error
	assert.NotNil(t, bifrostErr, "Expected error for disallowed speech stream operation")
	assert.Nil(t, response, "Expected no response for disallowed operation")

	// Assert that the error message contains "not supported" or "not supported by openai-custom"
	msg := strings.ToLower(bifrostErr.Error.Message)
	assert.Contains(t, msg, "not supported", "error should indicate operation is not supported")
	assert.Contains(t, msg, string(config.ProviderOpenAICustom), "error should mention refusing provider")
	assert.Equal(t, config.ProviderOpenAICustom, bifrostErr.ExtraFields.Provider, "error should be attributed to the custom provider")
}

func TestCustomProvider_MismatchedIdentity(t *testing.T) {
	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	// Use a provider that doesn't exist
	wrongProvider := schemas.ModelProvider("wrong-provider")

	request := &schemas.BifrostChatRequest{
		Provider: wrongProvider,
		Model:    "llama-3.3-70b-versatile",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("Hello! What's the capital of France?"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(100),
		},
	}

	// Attempt to make a chat completion request
	response, bifrostErr := client.ChatCompletionRequest(ctx, request)

	// Assert that the request failed with an error
	assert.NotNil(t, bifrostErr, "Expected error for mismatched identity")
	assert.Nil(t, response, "Expected no response for mismatched identity")

	msg := strings.ToLower(bifrostErr.Error.Message)
	assert.Contains(t, msg, "unsupported provider", "error should mention unsupported provider")
	assert.Contains(t, msg, strings.ToLower(string(wrongProvider)), "error should mention the wrong provider")
	assert.Equal(t, wrongProvider, bifrostErr.ExtraFields.Provider, "error should include the unsupported provider identity")
}
