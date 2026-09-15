package vllm_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestVLLM(t *testing.T) {
	if os.Getenv("VLLM_ENABLED") != "1" {
		t.Skip("Skipping vLLM tests: set VLLM_ENABLED=1 to enable (requires a live vLLM instance, see VLLM_BASE_URL)")
	}
	t.Parallel()
	baseURL := strings.TrimSpace(os.Getenv("VLLM_BASE_URL"))
	if baseURL == "" {
		t.Skip("Skipping vLLM tests because VLLM_BASE_URL is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	chatModel := getEnvWithDefault("VLLM_CHAT_MODEL", "Qwen/Qwen3-0.6B")
	textModel := getEnvWithDefault("VLLM_TEXT_MODEL", "Qwen/Qwen3-0.6B")
	// Reasoning/embedding/transcription/rerank each need a model actually
	// capable of them, and a single vLLM server only ever serves one model -
	// unset unless the caller points VLLM_*_BASE_URL at a dedicated instance
	// for that role (see llmtests.GetKeysForProvider's VLLM case), matching
	// the default chat/text pod (e.g. a reasoning-enabled deployment, or a
	// dedicated embedding/rerank/transcription model).
	reasoningModel := strings.TrimSpace(os.Getenv("VLLM_REASONING_MODEL"))
	embeddingModel := strings.TrimSpace(os.Getenv("VLLM_EMBEDDING_MODEL"))
	transcriptionModel := strings.TrimSpace(os.Getenv("VLLM_TRANSCRIPTION_MODEL"))
	rerankModel := strings.TrimSpace(os.Getenv("VLLM_RERANK_MODEL"))
	enableReasoningTests := reasoningModel != "" && strings.TrimSpace(os.Getenv("VLLM_REASONING_BASE_URL")) != ""
	enableEmbeddingTests := embeddingModel != "" && strings.TrimSpace(os.Getenv("VLLM_EMBEDDING_BASE_URL")) != ""
	enableRerankTests := rerankModel != "" && strings.TrimSpace(os.Getenv("VLLM_RERANK_BASE_URL")) != ""
	enableTranscriptionTests := transcriptionModel != "" && strings.TrimSpace(os.Getenv("VLLM_TRANSCRIPTION_BASE_URL")) != ""

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:           schemas.VLLM,
		ChatModel:          chatModel,
		TextModel:          textModel,
		ReasoningModel:     reasoningModel,
		EmbeddingModel:     embeddingModel,
		TranscriptionModel: transcriptionModel,
		RerankModel:        rerankModel,
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			TextCompletionStream:       true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			CompleteEnd2End:            true,
			Embedding:                  enableEmbeddingTests,
			Rerank:                     enableRerankTests,
			ListModels:                 true,
			Reasoning:                  enableReasoningTests,
			PassThroughExtraParams:     true,
			SpeechSynthesis:            false,
			SpeechSynthesisStream:      false,
			Transcription:              enableTranscriptionTests,
			TranscriptionStream:        false,
			ImageGeneration:            false,
			ImageGenerationStream:      false,
			ImageEdit:                  false,
			ImageEditStream:            false,
			ImageVariation:             false,
			ImageVariationStream:       false,
		},
	}

	t.Run("VLLMTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

func getEnvWithDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}