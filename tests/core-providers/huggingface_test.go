package tests

import (
	"os"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	defaultHFChatModel      = "google/gemma-2-2b-it"
	defaultHFTextModel      = defaultHFChatModel
	defaultHFEmbeddingModel = "sentence-transformers/all-MiniLM-L6-v2"
)

func hfEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestHuggingFace(t *testing.T) {
	t.Parallel()
	if os.Getenv("HUGGINGFACE_API_KEY") == "" {
		t.Skip("Skipping Hugging Face tests because HUGGINGFACE_API_KEY is not set")
	}

	chatModel := hfEnvOrDefault("HUGGINGFACE_CHAT_MODEL", defaultHFChatModel)
	textModel := hfEnvOrDefault("HUGGINGFACE_TEXT_MODEL", defaultHFTextModel)
	embeddingModel := hfEnvOrDefault("HUGGINGFACE_EMBEDDING_MODEL", defaultHFEmbeddingModel)

	client, ctx, cancel, err := config.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := config.ComprehensiveTestConfig{
		Provider:       schemas.HuggingFace,
		ChatModel:      chatModel,
		TextModel:      textModel,
		EmbeddingModel: embeddingModel,
		Scenarios: config.TestScenarios{
			TextCompletion:        textModel != "",
			TextCompletionStream:  textModel != "",
			SimpleChat:            chatModel != "",
			CompletionStream:      chatModel != "",
			MultiTurnConversation: chatModel != "",
			ToolCalls:             false,
			ToolCallsStreaming:    false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       false,
			SpeechSynthesis:       false,
			SpeechSynthesisStream: false,
			Transcription:         false,
			TranscriptionStream:   false,
			Embedding:             embeddingModel != "",
			Reasoning:             false,
			ListModels:            true,
		},
	}

	t.Run("HuggingFaceTests", func(t *testing.T) {
		runAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
