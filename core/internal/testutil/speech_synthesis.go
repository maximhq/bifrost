package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSpeechSynthesisTest executes the speech synthesis test scenario
// This function now supports testing multiple speech synthesis models - the test passes only if ALL models pass
func RunSpeechSynthesisTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesis {
		t.Logf("Speech synthesis not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesis", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "SpeechSynthesis", ModelTypeSpeechSynthesis, runSpeechSynthesisTestForModel)
	})
}

// runSpeechSynthesisTestForModel runs the speech synthesis test for a specific model
// The config passed here will have only ONE model in SpeechSynthesisModels array
func runSpeechSynthesisTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetSpeechSynthesisModelOrFirst(testConfig)

	// Test with shared text constants for round-trip validation with transcription
	testCases := []struct {
		name           string
		text           string
		voiceType      string
		format         string
		expectMinBytes int
		saveForSST     bool // Whether to save this audio for SST round-trip testing
	}{
		{
			name:           "BasicText_Primary_MP3",
			text:           TTSTestTextBasic,
			voiceType:      "primary",
			format:         GetProviderDefaultFormat(testConfig.Provider),
			expectMinBytes: 1000,
			saveForSST:     true,
		},
		{
			name:           "MediumText_Secondary_MP3",
			text:           TTSTestTextMedium,
			voiceType:      "secondary",
			format:         GetProviderDefaultFormat(testConfig.Provider),
			expectMinBytes: 2000,
			saveForSST:     true,
		},
		{
			name:           "TechnicalText_Tertiary_MP3",
			text:           TTSTestTextTechnical,
			voiceType:      "tertiary",
			format:         GetProviderDefaultFormat(testConfig.Provider),
			expectMinBytes: 500,
			saveForSST:     true,
		},
	}

	for _, tc := range testCases {
		voice := GetProviderVoice(testConfig.Provider, tc.voiceType)
		request := &schemas.BifrostSpeechRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: &schemas.SpeechInput{
				Input: tc.text,
			},
			Params: &schemas.SpeechParameters{
				VoiceConfig: &schemas.SpeechVoiceInput{
					Voice: &voice,
				},
				ResponseFormat: tc.format,
			},
			Fallbacks: testConfig.SpeechSynthesisFallbacks,
		}

		// Use retry framework with enhanced validation
		retryConfig := GetTestRetryConfigForScenario("SpeechSynthesis", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "SpeechSynthesis_" + tc.name,
			ExpectedBehavior: map[string]interface{}{
				"should_generate_audio": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    model,
				"format":   tc.format,
				"voice":    voice,
			},
		}

		// Enhanced validation for speech synthesis
		expectations := SpeechExpectations(tc.expectMinBytes)
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		// Create Speech retry config
		speechRetryConfig := SpeechRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []SpeechRetryCondition{}, // Add specific speech retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		speechResponse, bifrostErr := WithSpeechTestRetry(t, speechRetryConfig, retryContext, expectations, "SpeechSynthesis_"+tc.name, func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
			requestCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			return client.SpeechRequest(requestCtx, request)
		})

		if bifrostErr != nil {
			return fmt.Errorf("SpeechSynthesis_%s request failed after retries: %v", tc.name, GetErrorMessage(bifrostErr))
		}

		// Additional speech-specific validations (complementary to main validation)
		if err := validateSpeechSynthesisSpecificWithError(speechResponse, tc.expectMinBytes, model); err != nil {
			return fmt.Errorf("SpeechSynthesis_%s validation failed: %v", tc.name, err)
		}

		// Save audio file for SST round-trip testing if requested
		if tc.saveForSST {
			tempDir := os.TempDir()
			audioFileName := filepath.Join(tempDir, "tts_"+tc.name+"."+tc.format)

			err := os.WriteFile(audioFileName, speechResponse.Audio, 0644)
			if err != nil {
				return fmt.Errorf("failed to save audio file for SST testing: %v", err)
			}

			// Register cleanup to remove temp file
			t.Cleanup(func() {
				os.Remove(audioFileName)
			})

			t.Logf("💾 Audio saved for SST testing: %s (text: '%s')", audioFileName, tc.text)
		}

		t.Logf("✅ Speech synthesis successful: %d bytes of %s audio generated for voice '%s'",
			len(speechResponse.Audio), tc.format, voice)
	}

	t.Logf("🎉 SpeechSynthesis test passed for model: %s!", model)
	return nil
}

// RunSpeechSynthesisAdvancedTest executes advanced speech synthesis test scenarios
// This function now supports testing multiple speech synthesis models - the test passes only if ALL models pass
func RunSpeechSynthesisAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesis {
		t.Logf("Speech synthesis not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesisAdvanced", func(t *testing.T) {
		WrapTestScenario(t, client, ctx, testConfig, "SpeechSynthesisAdvanced", ModelTypeSpeechSynthesis, runSpeechSynthesisAdvancedTestForModel)
	})
}

// runSpeechSynthesisAdvancedTestForModel runs the advanced speech synthesis test for a specific model
// The config passed here will have only ONE model in SpeechSynthesisModels array
func runSpeechSynthesisAdvancedTestForModel(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) error {
	// Get the single model from the config
	model := GetSpeechSynthesisModelOrFirst(testConfig)

	// Test with longer text and HD model
	longText := `
	This is a comprehensive test of the text-to-speech functionality using a longer piece of text.
	The system should be able to handle multiple sentences, proper punctuation, and maintain
	consistent voice quality throughout the entire speech generation process. This test ensures
	that the speech synthesis can handle realistic use cases with substantial content.
	`

	voice := GetProviderVoice(testConfig.Provider, "tertiary")
	request := &schemas.BifrostSpeechRequest{
		Provider: testConfig.Provider,
		Model:    model,
		Input: &schemas.SpeechInput{
			Input: longText,
		},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: &voice,
			},
			ResponseFormat: GetProviderDefaultFormat(testConfig.Provider),
			Instructions:   "Speak slowly and clearly with natural intonation.",
		},
		Fallbacks: testConfig.SpeechSynthesisFallbacks,
	}

	retryConfig := GetTestRetryConfigForScenario("SpeechSynthesisHD", testConfig)
	retryContext := TestRetryContext{
		ScenarioName: "SpeechSynthesis_HD_LongText",
		ExpectedBehavior: map[string]interface{}{
			"generate_hd_audio": true,
			"handle_long_text":  true,
			"min_audio_bytes":   5000,
		},
		TestMetadata: map[string]interface{}{
			"provider":    testConfig.Provider,
			"model":       model,
			"text_length": len(longText),
		},
	}

	expectations := SpeechExpectations(5000) // HD should produce substantial audio
	expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

	// Create Speech retry config
	speechRetryConfig := SpeechRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []SpeechRetryCondition{}, // Add specific speech retry conditions as needed
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	speechResponse, bifrostErr := WithSpeechTestRetry(t, speechRetryConfig, retryContext, expectations, "SpeechSynthesis_HD", func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
		requestCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		return client.SpeechRequest(requestCtx, request)
	})
	if bifrostErr != nil {
		return fmt.Errorf("SpeechSynthesis_HD request failed after retries: %v", GetErrorMessage(bifrostErr))
	}

	if speechResponse == nil || speechResponse.Audio == nil {
		return fmt.Errorf("HD speech synthesis response missing audio data")
	}

	audioSize := len(speechResponse.Audio)
	if audioSize < 5000 {
		return fmt.Errorf("HD audio data too small: got %d bytes, expected at least 5000", audioSize)
	}

	if speechResponse.ExtraFields.ModelRequested != model {
		t.Logf("⚠️ Expected HD model, got: %s", speechResponse.ExtraFields.ModelRequested)
	}

	t.Logf("✅ HD speech synthesis successful: %d bytes generated", len(speechResponse.Audio))

	// Test provider-specific voice options
	voiceTypes := []string{"primary", "secondary", "tertiary"}
	testText := TTSTestTextBasic // Use shared constant

	for _, voiceType := range voiceTypes {
		voiceLocal := GetProviderVoice(testConfig.Provider, voiceType)
		requestVoice := &schemas.BifrostSpeechRequest{
			Provider: testConfig.Provider,
			Model:    model,
			Input: &schemas.SpeechInput{
				Input: testText,
			},
			Params: &schemas.SpeechParameters{
				VoiceConfig: &schemas.SpeechVoiceInput{
					Voice: &voiceLocal,
				},
				ResponseFormat: GetProviderDefaultFormat(testConfig.Provider),
			},
			Fallbacks: testConfig.SpeechSynthesisFallbacks,
		}

		expectationsVoice := SpeechExpectations(500)
		expectationsVoice = ModifyExpectationsForProvider(expectationsVoice, testConfig.Provider)

		// Use retry framework for voice test
		voiceRetryConfig := GetTestRetryConfigForScenario("SpeechSynthesis", testConfig)
		voiceRetryContext := TestRetryContext{
			ScenarioName: "SpeechSynthesis_VoiceType_" + voiceType,
			ExpectedBehavior: map[string]interface{}{
				"should_generate_audio": true,
			},
			TestMetadata: map[string]interface{}{
				"provider":   testConfig.Provider,
				"model":      model,
				"voice_type": voiceType,
				"voice":      voiceLocal,
			},
		}
		voiceSpeechRetryConfig := SpeechRetryConfig{
			MaxAttempts: voiceRetryConfig.MaxAttempts,
			BaseDelay:   voiceRetryConfig.BaseDelay,
			MaxDelay:    voiceRetryConfig.MaxDelay,
			Conditions:  []SpeechRetryCondition{},
			OnRetry:     voiceRetryConfig.OnRetry,
			OnFinalFail: voiceRetryConfig.OnFinalFail,
		}

		speechResponseVoice, bifrostErrVoice := WithSpeechTestRetry(t, voiceSpeechRetryConfig, voiceRetryContext, expectationsVoice, "SpeechSynthesis_VoiceType_"+voiceType, func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
			requestCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			return client.SpeechRequest(requestCtx, requestVoice)
		})

		if bifrostErrVoice != nil {
			return fmt.Errorf("SpeechSynthesis_Voice_%s request failed after retries: %v", voiceType, GetErrorMessage(bifrostErrVoice))
		}

		if speechResponseVoice == nil || speechResponseVoice.Audio == nil {
			return fmt.Errorf("Voice %s (%s) missing audio data after retries", voiceLocal, voiceType)
		}

		audioSizeVoice := len(speechResponseVoice.Audio)
		if audioSizeVoice < 500 {
			return fmt.Errorf("Audio too small for voice %s: got %d bytes, expected at least 500", voiceLocal, audioSizeVoice)
		}
		t.Logf("✅ Voice %s (%s): %d bytes generated", voiceLocal, voiceType, len(speechResponseVoice.Audio))
	}

	t.Logf("🎉 SpeechSynthesisAdvanced test passed for model: %s!", model)
	return nil
}

// validateSpeechSynthesisSpecific performs speech-specific validation
// This is complementary to the main validation framework and focuses on speech synthesis concerns
func validateSpeechSynthesisSpecific(t *testing.T, response *schemas.BifrostSpeechResponse, expectMinBytes int, expectedModel string) {
	if response == nil {
		t.Fatal("Invalid speech synthesis response structure")
	}

	if response.Audio == nil {
		t.Fatal("Speech synthesis response missing audio data")
	}

	audioSize := len(response.Audio)
	if audioSize < expectMinBytes {
		t.Fatalf("Audio data too small: got %d bytes, expected at least %d", audioSize, expectMinBytes)
	}

	if expectedModel != "" && response.ExtraFields.ModelRequested != expectedModel {
		t.Logf("⚠️ Expected model, got: %s", response.ExtraFields.ModelRequested)
	}

	t.Logf("✅ Audio validation passed: %d bytes generated", audioSize)
}

// validateSpeechSynthesisSpecificWithError performs speech-specific validation and returns errors
// This version is used in the multi-model testing pattern
func validateSpeechSynthesisSpecificWithError(response *schemas.BifrostSpeechResponse, expectMinBytes int, expectedModel string) error {
	if response == nil {
		return fmt.Errorf("invalid speech synthesis response structure")
	}

	if response.Audio == nil {
		return fmt.Errorf("speech synthesis response missing audio data")
	}

	audioSize := len(response.Audio)
	if audioSize < expectMinBytes {
		return fmt.Errorf("audio data too small: got %d bytes, expected at least %d", audioSize, expectMinBytes)
	}

	if expectedModel != "" && response.ExtraFields.ModelRequested != expectedModel {
		// Log warning but don't fail - model mismatch is not always an error
		return nil
	}

	return nil
}
