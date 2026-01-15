package testutil

import (
	"context"
	"fmt"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunChatAudioTest executes the chat audio test scenario using multi-model testing framework
// This function now supports testing multiple chat audio models - the test passes only if ALL models pass
func RunChatAudioTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ChatAudio || GetChatAudioModelOrFirst(testConfig) == "" {
		t.Logf("Chat audio not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ChatAudio", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "ChatAudio", ModelTypeChatAudio, runChatAudioTestForModel)
	})
}

// runChatAudioTestForModel runs the chat audio test for a specific model
// The config passed here will have only ONE model in ChatAudioModels array
func runChatAudioTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetChatAudioModelOrFirst(testConfig)

	// Load sample audio file and encode as base64
	encodedAudio, err := GetSampleAudioBase64()
	if err != nil {
		return fmt.Errorf("failed to load sample audio file: %v", err)
	}

	// Create chat message with audio input
	chatMessages := []schemas.ChatMessage{
		CreateAudioChatMessage("Describe in detail the spoken audio input.", encodedAudio, "mp3"),
	}

	// Use retry framework for audio requests
	retryConfig := GetTestRetryConfigForScenario("ChatAudio", testConfig)
	retryContext := TestRetryContext{
		ScenarioName: "ChatAudio",
		ExpectedBehavior: map[string]interface{}{
			"should_process_audio":     true,
			"should_return_audio":      true,
			"should_return_transcript": true,
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    model,
		},
	}

	// Create Chat Completions retry config
	chatRetryConfig := ChatRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []ChatRetryCondition{},
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	// Test Chat Completions API with audio
	chatOperation := func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				Modalities: []string{"text", "audio"},
				Audio: &schemas.ChatAudioParameters{
					Voice:  "alloy",
					Format: "wav", // output format
				},
				MaxCompletionTokens: bifrost.Ptr(200),
			},
			Fallbacks: testConfig.Fallbacks,
		}
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, err := client.ChatCompletionRequest(bfCtx, chatReq)
		if err != nil {
			return nil, err
		}
		if response != nil {
			return response, nil
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "No chat response returned",
			},
		}
	}

	expectations := GetExpectationsForScenario("ChatAudio", testConfig, map[string]interface{}{})
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

	chatResponse, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "ChatAudio", chatOperation)

	// Check that the request succeeded
	if chatError != nil {
		return fmt.Errorf("Chat Completions API failed: %s", GetErrorMessage(chatError))
	}

	if chatResponse == nil {
		return fmt.Errorf("chat response should not be nil")
	}

	if len(chatResponse.Choices) == 0 {
		return fmt.Errorf("chat response should have at least one choice")
	}

	choice := chatResponse.Choices[0]
	if choice.ChatNonStreamResponseChoice == nil {
		return fmt.Errorf("expected non-streaming response choice")
	}

	message := choice.ChatNonStreamResponseChoice.Message
	if message == nil {
		return fmt.Errorf("message should not be nil")
	}

	// Check for audio in the response
	if message.ChatAssistantMessage == nil {
		return fmt.Errorf("expected ChatAssistantMessage")
	}

	if message.ChatAssistantMessage.Audio == nil {
		return fmt.Errorf("expected audio in response (choices[0].message.audio should be present)")
	}

	audio := message.ChatAssistantMessage.Audio
	if audio.Data == "" {
		t.Error("❌ Expected audio.data to be present in response")
	} else {
		t.Logf("✅ Audio data present in response (length: %d)", len(audio.Data))
	}

	if audio.Transcript == "" {
		t.Error("❌ Expected audio.transcript to be present in response")
	} else {
		t.Logf("✅ Audio transcript present in response: %s", audio.Transcript)
	}

	// Log the content if available
	if message.Content != nil && message.Content.ContentStr != nil {
		t.Logf("✅ Chat response content: %s", *message.Content.ContentStr)
	}

	t.Logf("🎉 ChatAudio test passed for model: %s!", model)
	return nil
}

// RunChatAudioStreamTest executes the chat audio streaming test scenario using multi-model testing framework
// This function now supports testing multiple chat audio models - the test passes only if ALL models pass
func RunChatAudioStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ChatAudio || GetChatAudioModelOrFirst(testConfig) == "" {
		t.Logf("Chat audio streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("ChatAudioStream", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "ChatAudioStream", ModelTypeChatAudio, runChatAudioStreamTestForModel)
	})
}

// runChatAudioStreamTestForModel runs the chat audio stream test for a specific model
// The config passed here will have only ONE model in ChatAudioModels array
func runChatAudioStreamTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetChatAudioModelOrFirst(testConfig)

	// Load sample audio file and encode as base64
	encodedAudio, err := GetSampleAudioBase64()
	if err != nil {
		return fmt.Errorf("failed to load sample audio file: %v", err)
	}

	// Create chat message with audio input
	chatMessages := []schemas.ChatMessage{
		CreateAudioChatMessage("Describe in detail the spoken audio input.", encodedAudio, "mp3"),
	}

	// Use retry framework for audio streaming requests
	retryConfig := StreamingRetryConfig()
	retryContext := TestRetryContext{
		ScenarioName: "ChatAudioStream",
		ExpectedBehavior: map[string]interface{}{
			"should_process_audio":     true,
			"should_return_audio":      true,
			"should_return_transcript": true,
		},
		TestMetadata: map[string]interface{}{
			"provider": testConfig.Provider,
			"model":    model,
		},
	}

	// Test Chat Completions Stream API with audio
	chatReq := &schemas.BifrostChatRequest{
		Provider: testConfig.Provider,
		Model:    model,
		Input:    chatMessages,
		Params: &schemas.ChatParameters{
			Modalities: []string{"text", "audio"},
			Audio: &schemas.ChatAudioParameters{
				Voice:  "alloy",
				Format: "pcm16", // output format
			},
		},
		Fallbacks: testConfig.Fallbacks,
	}

	responseChannel, bifrostErr := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		return client.ChatCompletionStreamRequest(bfCtx, chatReq)
	})

	// Enhanced error handling
	if bifrostErr != nil {
		return fmt.Errorf("chat audio stream request failed: %v", bifrostErr)
	}
	if responseChannel == nil {
		return fmt.Errorf("response channel should not be nil")
	}

	// Accumulate stream chunks
	var chunks []*schemas.BifrostStream
	var audioData strings.Builder
	var audioTranscript strings.Builder
	var audioID string
	var audioExpiresAt int
	var lastUsage *schemas.BifrostLLMUsage

	for chunk := range responseChannel {
		chunks = append(chunks, chunk)

		if chunk.BifrostError != nil && chunk.BifrostError.Error != nil {
			return fmt.Errorf("stream error: %v", chunk.BifrostError.Error)
		}

		if chunk.BifrostChatResponse != nil {
			if len(chunk.BifrostChatResponse.Choices) > 0 {
				choice := chunk.BifrostChatResponse.Choices[0]

				// Accumulate text content
				if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
					delta := choice.ChatStreamResponseChoice.Delta

					// Accumulate audio data from delta
					if delta.Audio != nil {
						if delta.Audio.Data != "" {
							audioData.WriteString(delta.Audio.Data)
						}
						if delta.Audio.Transcript != "" {
							audioTranscript.WriteString(delta.Audio.Transcript)
						}
						if delta.Audio.ID != "" {
							audioID = delta.Audio.ID
						}
						if delta.Audio.ExpiresAt != 0 {
							audioExpiresAt = delta.Audio.ExpiresAt
						}
					}
				}
			}

			// Capture final usage
			if chunk.BifrostChatResponse.Usage != nil {
				lastUsage = chunk.BifrostChatResponse.Usage
			}
		}
	}

	// Validate that we received chunks
	if len(chunks) == 0 {
		return fmt.Errorf("expected to receive stream chunks")
	}

	t.Logf("✅ Received %d stream chunks", len(chunks))

	// Validate accumulated audio data (check overall, not per-chunk)
	accumulatedAudioData := audioData.String()
	accumulatedTranscript := audioTranscript.String()

	// Check overall: at least one of audio data or transcript should be present
	if accumulatedAudioData == "" && accumulatedTranscript == "" {
		return fmt.Errorf("expected overall audio data or transcript to be present in stream chunks")
	}

	if accumulatedAudioData != "" {
		t.Logf("✅ Accumulated audio data (length: %d)", len(accumulatedAudioData))
	} else {
		t.Logf("⚠️ No accumulated audio data found")
	}

	if accumulatedTranscript != "" {
		t.Logf("✅ Accumulated audio transcript: %s", accumulatedTranscript)
	} else {
		t.Logf("⚠️ No accumulated audio transcript found")
	}

	// Validate audio metadata
	if audioID != "" {
		t.Logf("✅ Audio ID: %s", audioID)
	}
	if audioExpiresAt != 0 {
		t.Logf("✅ Audio expires at: %d", audioExpiresAt)
	}

	// Validate usage if available
	if lastUsage != nil {
		t.Logf("✅ Token usage - Prompt: %d, Completion: %d, Total: %d",
			lastUsage.PromptTokens,
			lastUsage.CompletionTokens,
			lastUsage.TotalTokens)

		// Check for audio tokens
		if lastUsage.PromptTokensDetails != nil && lastUsage.PromptTokensDetails.AudioTokens > 0 {
			t.Logf("✅ Input audio tokens: %d", lastUsage.PromptTokensDetails.AudioTokens)
		}
		if lastUsage.CompletionTokensDetails != nil && lastUsage.CompletionTokensDetails.AudioTokens > 0 {
			t.Logf("✅ Output audio tokens: %d", lastUsage.CompletionTokensDetails.AudioTokens)
		}
	}

	t.Logf("🎉 ChatAudioStream test passed for model: %s!", model)
	return nil
}
