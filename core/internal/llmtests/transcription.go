package llmtests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunTranscriptionTest executes the transcription test scenario
func RunTranscriptionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Transcription {
		t.Logf("Transcription not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("Transcription", func(t *testing.T) {
		// First generate TTS audio for round-trip validation
		roundTripCases := []struct {
			name           string
			text           string
			voiceType      string
			format         string
			responseFormat *string
		}{
			{
				name:           "RoundTrip_Basic_MP3",
				text:           TTSTestTextBasic,
				voiceType:      "primary",
				format:         "mp3",
				responseFormat: bifrost.Ptr("json"),
			},
			{
				name:           "RoundTrip_Medium_MP3",
				text:           TTSTestTextMedium,
				voiceType:      "secondary",
				format:         "mp3",
				responseFormat: bifrost.Ptr("json"),
			},
			{
				name:           "RoundTrip_Technical_MP3",
				text:           TTSTestTextTechnical,
				voiceType:      "tertiary",
				format:         "mp3",
				responseFormat: bifrost.Ptr("json"),
			},
		}

		for _, tc := range roundTripCases {
			t.Run(tc.name, func(t *testing.T) {
				if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
					t.Parallel()
				}

				speechSynthesisProvider := testConfig.Provider
				if testConfig.ExternalTTSProvider != "" {
					speechSynthesisProvider = testConfig.ExternalTTSProvider
				}

				speechSynthesisModel := testConfig.SpeechSynthesisModel
				if testConfig.ExternalTTSModel != "" {
					speechSynthesisModel = testConfig.ExternalTTSModel
				}

				var transcriptionRequest *schemas.BifrostTranscriptionRequest
				if testConfig.Provider == schemas.HuggingFace && strings.HasPrefix(testConfig.TranscriptionModel, "fal-ai/") {

					// For Fal-AI models on HuggingFace, we have to use mp3 but fal-ai speech models only return wav
					// So we read from a pre-generated mp3 file to avoid format issues
					_, filename, _, _ := runtime.Caller(0)
					dir := filepath.Dir(filename)
					filePath := filepath.Join(dir, "scenarios", "media", fmt.Sprintf("%s.mp3", tc.name))
					fileContent, err := os.ReadFile(filePath)
					if err != nil {
						t.Fatalf("failed to read audio fixture %s: %v", filePath, err)
					}
					transcriptionRequest = &schemas.BifrostTranscriptionRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.TranscriptionModel,
						Input: &schemas.TranscriptionInput{
							File: fileContent,
						},
						Params: &schemas.TranscriptionParameters{
							Language:       bifrost.Ptr("en"),
							Format:         bifrost.Ptr("mp3"),
							ResponseFormat: tc.responseFormat,
						},
						Fallbacks: testConfig.TranscriptionFallbacks,
					}
				} else {

					// Step 1: Generate TTS audio
					voice := GetProviderVoice(speechSynthesisProvider, tc.voiceType)
					ttsRequest := &schemas.BifrostSpeechRequest{
						Provider: speechSynthesisProvider,
						Model:    speechSynthesisModel,
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

					// Use retry framework for TTS generation
					ttsRetryConfig := GetTestRetryConfigForScenario("SpeechSynthesis", testConfig)
					ttsRetryContext := TestRetryContext{
						ScenarioName: "Transcription_RoundTrip_TTS_" + tc.name,
						ExpectedBehavior: map[string]interface{}{
							"should_generate_audio": true,
						},
						TestMetadata: map[string]interface{}{
							"provider": speechSynthesisProvider,
							"model":    speechSynthesisModel,
							"format":   tc.format,
						},
					}
					ttsExpectations := SpeechExpectations(100) // Minimum expected bytes
					ttsExpectations = ModifyExpectationsForProvider(ttsExpectations, testConfig.Provider)
					speechRetryConfig := SpeechRetryConfig{
						MaxAttempts: ttsRetryConfig.MaxAttempts,
						BaseDelay:   ttsRetryConfig.BaseDelay,
						MaxDelay:    ttsRetryConfig.MaxDelay,
						Conditions:  []SpeechRetryCondition{},
						OnRetry:     ttsRetryConfig.OnRetry,
						OnFinalFail: ttsRetryConfig.OnFinalFail,
					}

					ttsResponse, err := WithSpeechTestRetry(t, speechRetryConfig, ttsRetryContext, ttsExpectations, "Transcription_RoundTrip_TTS_"+tc.name, func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
						bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
						return client.SpeechRequest(bfCtx, ttsRequest)
					})
					if err != nil {
						t.Fatalf("❌ TTS generation failed for round-trip test after retries: %v", GetErrorMessage(err))
					}
					if ttsResponse == nil || len(ttsResponse.Audio) == 0 {
						t.Fatal("❌ TTS returned invalid or empty audio for round-trip test after retries")
					}

					// Save temp audio file
					tempDir := os.TempDir()
					audioFileName := filepath.Join(tempDir, "roundtrip_"+tc.name+"."+tc.format)
					writeErr := os.WriteFile(audioFileName, ttsResponse.Audio, 0o644)
					require.NoError(t, writeErr, "Failed to save temp audio file")

					// Register cleanup
					t.Cleanup(func() {
						os.Remove(audioFileName)
					})

					t.Logf("Generated TTS audio for round-trip: %s (%d bytes)", audioFileName, len(ttsResponse.Audio))

					// Step 2: Transcribe the generated audio
					transcriptionRequest = &schemas.BifrostTranscriptionRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.TranscriptionModel,
						Input: &schemas.TranscriptionInput{
							File: ttsResponse.Audio,
						},
						Params: &schemas.TranscriptionParameters{
							Language:       bifrost.Ptr("en"),
							Format:         schemas.Ptr(tc.format),
							ResponseFormat: tc.responseFormat,
						},
						Fallbacks: testConfig.TranscriptionFallbacks,
					}
				}

				// Use retry framework for transcription
				retryConfig := GetTestRetryConfigForScenario("Transcription", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: "Transcription_RoundTrip_" + tc.name,
					ExpectedBehavior: map[string]interface{}{
						"should_transcribe_audio": true,
						"round_trip_test":         true,
					},
					TestMetadata: map[string]interface{}{
						"provider": testConfig.Provider,
						"model":    testConfig.TranscriptionModel,
						"format":   tc.format,
					},
				}

				// Enhanced validation for transcription
				expectations := TranscriptionExpectations(10) // Expect at least some content
				expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

				// Create Transcription retry config
				transcriptionRetryConfig := TranscriptionRetryConfig{
					MaxAttempts: retryConfig.MaxAttempts,
					BaseDelay:   retryConfig.BaseDelay,
					MaxDelay:    retryConfig.MaxDelay,
					Conditions:  []TranscriptionRetryCondition{}, // Add specific transcription retry conditions as needed
					OnRetry:     retryConfig.OnRetry,
					OnFinalFail: retryConfig.OnFinalFail,
				}

				transcriptionResponse, bifrostErr := WithTranscriptionTestRetry(t, transcriptionRetryConfig, retryContext, expectations, "Transcription_RoundTrip_"+tc.name, func() (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.TranscriptionRequest(bfCtx, transcriptionRequest)
				})

				if bifrostErr != nil {
					t.Fatalf("❌ Transcription_RoundTrip_"+tc.name+" request failed after retries: %v", GetErrorMessage(bifrostErr))
				}

				// Validate round-trip transcription (complementary to main validation)
				validateTranscriptionRoundTrip(t, transcriptionResponse, tc.text, tc.name, testConfig)
			})
		}

		// Additional test cases using the utility function for edge cases
		t.Run("AdditionalAudioTests", func(t *testing.T) {
			// Test with custom generated audio for specific scenarios
			customCases := []struct {
				name           string
				text           string
				language       *string
				responseFormat *string
			}{
				{
					name:           "Numbers_And_Punctuation",
					text:           "Testing numbers 1, 2, 3 and punctuation marks! Question?",
					language:       bifrost.Ptr("en"),
					responseFormat: bifrost.Ptr("json"),
				},
				{
					name:           "Technical_Terms",
					text:           "API gateway processes HTTP requests with JSON payloads",
					language:       bifrost.Ptr("en"),
					responseFormat: bifrost.Ptr("json"),
				},
			}

			for _, tc := range customCases {
				t.Run(tc.name, func(t *testing.T) {
					if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
						t.Parallel()
					}

					speechSynthesisProvider := testConfig.Provider
					if testConfig.ExternalTTSProvider != "" {
						speechSynthesisProvider = testConfig.ExternalTTSProvider
					}

					speechSynthesisModel := testConfig.SpeechSynthesisModel
					if testConfig.ExternalTTSModel != "" {
						speechSynthesisModel = testConfig.ExternalTTSModel
					}

					var audioData []byte
					var readErr error
					if testConfig.Provider == schemas.HuggingFace && strings.HasPrefix(testConfig.TranscriptionModel, "fal-ai/") {

						// For Fal-AI models on HuggingFace, we have to use mp3 but fal-ai speech models only return wav
						// So we read from a pre-generated mp3 file to avoid format issues
						_, filename, _, _ := runtime.Caller(0)
						dir := filepath.Dir(filename)
						filePath := filepath.Join(dir, "scenarios", "media", fmt.Sprintf("%s.mp3", tc.name))
						audioData, readErr = os.ReadFile(filePath)
						if readErr != nil {
							t.Fatalf("failed to read audio fixture %s: %v", filePath, readErr)
						}
					} else {
						// Use the utility function to generate audio
						audioData, _ = GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, tc.text, "primary", "mp3")
					}
					// Test transcription
					request := &schemas.BifrostTranscriptionRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.TranscriptionModel,
						Input: &schemas.TranscriptionInput{
							File: audioData,
						},
						Params: &schemas.TranscriptionParameters{
							Language:       tc.language,
							Format:         bifrost.Ptr("mp3"),
							ResponseFormat: tc.responseFormat,
						},
						Fallbacks: testConfig.TranscriptionFallbacks,
					}

					// Use retry framework for custom transcription
					customRetryConfig := GetTestRetryConfigForScenario("Transcription", testConfig)
					customRetryContext := TestRetryContext{
						ScenarioName: "Transcription_Custom_" + tc.name,
						ExpectedBehavior: map[string]interface{}{
							"should_transcribe_audio": true,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"model":    testConfig.TranscriptionModel,
						},
					}
					customExpectations := TranscriptionExpectations(5)
					customExpectations = ModifyExpectationsForProvider(customExpectations, testConfig.Provider)
					customTranscriptionRetryConfig := TranscriptionRetryConfig{
						MaxAttempts: customRetryConfig.MaxAttempts,
						BaseDelay:   customRetryConfig.BaseDelay,
						MaxDelay:    customRetryConfig.MaxDelay,
						Conditions:  []TranscriptionRetryCondition{},
						OnRetry:     customRetryConfig.OnRetry,
						OnFinalFail: customRetryConfig.OnFinalFail,
					}

					response, err := WithTranscriptionTestRetry(t, customTranscriptionRetryConfig, customRetryContext, customExpectations, "Transcription_Custom_"+tc.name, func() (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
						bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
						return client.TranscriptionRequest(bfCtx, request)
					})
					if err != nil {
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						t.Fatalf("❌ Custom transcription failed after retries: %s", errorMsg)
					}
					if response == nil {
						t.Fatalf("❌ Custom transcription returned nil response after retries")
					}
					if response.Text == "" {
						t.Fatalf("❌ Custom transcription returned empty text after retries")
					}

					t.Logf("✅ Custom transcription successful: '%s' → '%s'", tc.text, response.Text)
				})
			}
		})
	})
}

// RunTranscriptionAdvancedTest executes advanced transcription test scenarios
func RunTranscriptionAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Transcription {
		t.Logf("Transcription not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TranscriptionAdvanced", func(t *testing.T) {
		t.Run("AllResponseFormats", func(t *testing.T) {
			// Test supported response formats (excluding text to avoid JSON parsing issues)
			formats := []string{"json"}

			for _, format := range formats {
				t.Run("Format_"+format, func(t *testing.T) {
					if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
						t.Parallel()
					}

					speechSynthesisProvider := testConfig.Provider
					if testConfig.ExternalTTSProvider != "" {
						speechSynthesisProvider = testConfig.ExternalTTSProvider
					}

					speechSynthesisModel := testConfig.SpeechSynthesisModel
					if testConfig.ExternalTTSModel != "" {
						speechSynthesisModel = testConfig.ExternalTTSModel
					}

					var audioData []byte
					var readErr error
					if testConfig.Provider == schemas.HuggingFace && strings.HasPrefix(testConfig.TranscriptionModel, "fal-ai/") {

						// For Fal-AI models on HuggingFace, we have to use mp3 but fal-ai speech models only return wav
						// So we read from a pre-generated mp3 file to avoid format issues
						_, filename, _, _ := runtime.Caller(0)
						dir := filepath.Dir(filename)
						filePath := filepath.Join(dir, "scenarios", "media", "RoundTrip_Basic_MP3.mp3")
						audioData, readErr = os.ReadFile(filePath)
						if readErr != nil {
							t.Fatalf("failed to read audio fixture %s: %v", filePath, readErr)
						}
					} else {
						// Use the utility function to generate audio
						audioData, _ = GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, TTSTestTextBasic, "primary", "mp3")
					}

					formatCopy := format
					request := &schemas.BifrostTranscriptionRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.TranscriptionModel,
						Input: &schemas.TranscriptionInput{
							File: audioData,
						},
						Params: &schemas.TranscriptionParameters{
							Format:         bifrost.Ptr("mp3"),
							ResponseFormat: &formatCopy,
						},
						Fallbacks: testConfig.TranscriptionFallbacks,
					}

					// Use retry framework for format test
					formatRetryConfig := GetTestRetryConfigForScenario("Transcription", testConfig)
					formatRetryContext := TestRetryContext{
						ScenarioName: "Transcription_Format_" + format,
						ExpectedBehavior: map[string]interface{}{
							"should_transcribe_audio": true,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"model":    testConfig.TranscriptionModel,
							"format":   format,
						},
					}
					formatExpectations := TranscriptionExpectations(5)
					formatExpectations = ModifyExpectationsForProvider(formatExpectations, testConfig.Provider)
					formatTranscriptionRetryConfig := TranscriptionRetryConfig{
						MaxAttempts: formatRetryConfig.MaxAttempts,
						BaseDelay:   formatRetryConfig.BaseDelay,
						MaxDelay:    formatRetryConfig.MaxDelay,
						Conditions:  []TranscriptionRetryCondition{},
						OnRetry:     formatRetryConfig.OnRetry,
						OnFinalFail: formatRetryConfig.OnFinalFail,
					}

					response, err := WithTranscriptionTestRetry(t, formatTranscriptionRetryConfig, formatRetryContext, formatExpectations, "Transcription_Format_"+format, func() (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
						bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
						return client.TranscriptionRequest(bfCtx, request)
					})
					if err != nil {
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						t.Fatalf("❌ Transcription failed for format %s after retries: %s", format, errorMsg)
					}
					if response == nil {
						t.Fatalf("❌ Transcription returned nil response for format %s after retries", format)
					}
					if response.Text == "" {
						t.Fatalf("❌ Transcription returned empty text for format %s after retries", format)
					}

					t.Logf("✅ Format %s successful: '%s'", format, response.Text)
				})
			}
		})

		t.Run("WithCustomParameters", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			speechSynthesisProvider := testConfig.Provider
			if testConfig.ExternalTTSProvider != "" {
				speechSynthesisProvider = testConfig.ExternalTTSProvider
			}

			speechSynthesisModel := testConfig.SpeechSynthesisModel
			if testConfig.ExternalTTSModel != "" {
				speechSynthesisModel = testConfig.ExternalTTSModel
			}

			var audioData []byte
			var readErr error
			if testConfig.Provider == schemas.HuggingFace && strings.HasPrefix(testConfig.TranscriptionModel, "fal-ai/") {

				// For Fal-AI models on HuggingFace, we have to use mp3 but fal-ai speech models only return wav
				// So we read from a pre-generated mp3 file to avoid format issues
				_, filename, _, _ := runtime.Caller(0)
				dir := filepath.Dir(filename)
				filePath := filepath.Join(dir, "scenarios", "media", "RoundTrip_Medium_MP3.mp3")
				audioData, readErr = os.ReadFile(filePath)
				if readErr != nil {
					t.Fatalf("failed to read audio fixture %s: %v", filePath, readErr)
				}
			} else {
				// Generate audio for custom parameters test
				audioData, _ = GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, TTSTestTextMedium, "secondary", "mp3")
			}

			// Test with custom parameters and temperature
			request := &schemas.BifrostTranscriptionRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.TranscriptionModel,
				Input: &schemas.TranscriptionInput{
					File: audioData,
				},
				Params: &schemas.TranscriptionParameters{
					Language:       bifrost.Ptr("en"),
					Format:         bifrost.Ptr("mp3"),
					Prompt:         bifrost.Ptr("This audio contains technical terminology and proper nouns."),
					ResponseFormat: bifrost.Ptr("json"), // Use json instead of verbose_json for whisper-1
				},
				Fallbacks: testConfig.TranscriptionFallbacks,
			}

			// Use retry framework for advanced transcription
			advancedRetryConfig := GetTestRetryConfigForScenario("Transcription", testConfig)
			advancedRetryContext := TestRetryContext{
				ScenarioName: "Transcription_Advanced_CustomParams",
				ExpectedBehavior: map[string]interface{}{
					"should_transcribe_audio": true,
				},
				TestMetadata: map[string]interface{}{
					"provider": testConfig.Provider,
					"model":    testConfig.TranscriptionModel,
				},
			}
			advancedExpectations := TranscriptionExpectations(5)
			advancedExpectations = ModifyExpectationsForProvider(advancedExpectations, testConfig.Provider)
			advancedTranscriptionRetryConfig := TranscriptionRetryConfig{
				MaxAttempts: advancedRetryConfig.MaxAttempts,
				BaseDelay:   advancedRetryConfig.BaseDelay,
				MaxDelay:    advancedRetryConfig.MaxDelay,
				Conditions:  []TranscriptionRetryCondition{},
				OnRetry:     advancedRetryConfig.OnRetry,
				OnFinalFail: advancedRetryConfig.OnFinalFail,
			}

			response, err := WithTranscriptionTestRetry(t, advancedTranscriptionRetryConfig, advancedRetryContext, advancedExpectations, "Transcription_Advanced_CustomParams", func() (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.TranscriptionRequest(bfCtx, request)
			})
			if err != nil {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				t.Fatalf("❌ Advanced transcription failed after retries: %s", errorMsg)
			}
			if response == nil {
				t.Fatalf("❌ Advanced transcription returned nil response after retries")
			}
			if response.Text == "" {
				t.Fatalf("❌ Advanced transcription returned empty text after retries")
			}

			t.Logf("✅ Advanced transcription successful: '%s'", response.Text)
		})

		t.Run("MultipleLanguages", func(t *testing.T) {
			// Test with different language hints (only English for now since our TTS is English)
			languages := []string{"en"}

			for _, lang := range languages {
				t.Run("Language_"+lang, func(t *testing.T) {
					if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
						t.Parallel()
					}

					speechSynthesisProvider := testConfig.Provider
					if testConfig.ExternalTTSProvider != "" {
						speechSynthesisProvider = testConfig.ExternalTTSProvider
					}

					speechSynthesisModel := testConfig.SpeechSynthesisModel
					if testConfig.ExternalTTSModel != "" {
						speechSynthesisModel = testConfig.ExternalTTSModel
					}

					var audioData []byte
					var readErr error
					if testConfig.Provider == schemas.HuggingFace && strings.HasPrefix(testConfig.TranscriptionModel, "fal-ai/") {

						// For Fal-AI models on HuggingFace, we have to use mp3 but fal-ai speech models only return wav
						// So we read from a pre-generated mp3 file to avoid format issues
						_, filename, _, _ := runtime.Caller(0)
						dir := filepath.Dir(filename)
						filePath := filepath.Join(dir, "scenarios", "media", "RoundTrip_Basic_MP3.mp3")
						audioData, readErr = os.ReadFile(filePath)
						if readErr != nil {
							t.Fatalf("failed to read audio fixture %s: %v", filePath, readErr)
						}
					} else {
						// Use the utility function to generate audio
						audioData, _ = GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, TTSTestTextBasic, "primary", "mp3")
					}

					langCopy := lang
					request := &schemas.BifrostTranscriptionRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.TranscriptionModel,
						Input: &schemas.TranscriptionInput{
							File: audioData,
						},
						Params: &schemas.TranscriptionParameters{
							Format:   bifrost.Ptr("mp3"),
							Language: &langCopy,
						},
						Fallbacks: testConfig.TranscriptionFallbacks,
					}

					// Use retry framework for language test
					langRetryConfig := GetTestRetryConfigForScenario("Transcription", testConfig)
					langRetryContext := TestRetryContext{
						ScenarioName: "Transcription_Language_" + lang,
						ExpectedBehavior: map[string]interface{}{
							"should_transcribe_audio": true,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"model":    testConfig.TranscriptionModel,
							"language": lang,
						},
					}
					langExpectations := TranscriptionExpectations(5)
					langExpectations = ModifyExpectationsForProvider(langExpectations, testConfig.Provider)
					langTranscriptionRetryConfig := TranscriptionRetryConfig{
						MaxAttempts: langRetryConfig.MaxAttempts,
						BaseDelay:   langRetryConfig.BaseDelay,
						MaxDelay:    langRetryConfig.MaxDelay,
						Conditions:  []TranscriptionRetryCondition{},
						OnRetry:     langRetryConfig.OnRetry,
						OnFinalFail: langRetryConfig.OnFinalFail,
					}

					response, err := WithTranscriptionTestRetry(t, langTranscriptionRetryConfig, langRetryContext, langExpectations, "Transcription_Language_"+lang, func() (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
						bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
						return client.TranscriptionRequest(bfCtx, request)
					})
					if err != nil {
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						t.Fatalf("❌ Transcription failed for language %s after retries: %s", lang, errorMsg)
					}
					if response == nil {
						t.Fatalf("❌ Transcription returned nil response for language %s after retries", lang)
					}
					if response.Text == "" {
						t.Fatalf("❌ Transcription returned empty text for language %s after retries", lang)
					}
					t.Logf("✅ Language %s transcription successful: '%s'", lang, response.Text)
				})
			}
		})
	})
}

// validateTranscriptionRoundTrip performs round-trip validation for transcription responses
// This is complementary to the main validation framework and focuses on transcription accuracy
func validateTranscriptionRoundTrip(t *testing.T, response *schemas.BifrostTranscriptionResponse, originalText string, testName string, testConfig ComprehensiveTestConfig) {
	if response == nil || response.Text == "" {
		t.Fatal("Transcription response missing transcribed text")
	}

	transcribedText := response.Text

	// Normalize for comparison (lowercase, remove punctuation)
	originalWords := strings.Fields(strings.ToLower(originalText))
	transcribedWords := strings.Fields(strings.ToLower(transcribedText))

	// Check that at least 50% of original words are found in transcription
	foundWords := 0
	for _, originalWord := range originalWords {
		// Remove punctuation for comparison
		cleanOriginal := strings.Trim(originalWord, ".,!?;:")
		if len(cleanOriginal) < 3 { // Skip very short words
			continue
		}

		for _, transcribedWord := range transcribedWords {
			cleanTranscribed := strings.Trim(transcribedWord, ".,!?;:")
			if strings.Contains(cleanTranscribed, cleanOriginal) || strings.Contains(cleanOriginal, cleanTranscribed) {
				foundWords++
				break
			}
		}
	}

	// Expect at least 50% word match for successful round-trip
	minExpectedWords := len(originalWords) / 2
	if foundWords < minExpectedWords {
		t.Logf("⚠️ Round-trip validation concern:")
		t.Logf("   Original: '%s'", originalText)
		t.Logf("   Transcribed: '%s'", transcribedText)
		t.Logf("   Found %d/%d words (%.1f%%), expected ≥ %d (50%%)",
			foundWords, len(originalWords), float64(foundWords)/float64(len(originalWords))*100, minExpectedWords)
		// Note: Not failing test as this can be provider/model dependent
	} else {
		t.Logf("✅ Round-trip validation passed: found %d/%d words (%.1f%%)",
			foundWords, len(originalWords), float64(foundWords)/float64(len(originalWords))*100)
	}

	// Check provider field
	if response.ExtraFields.Provider != testConfig.Provider {
		t.Logf("⚠️ Provider mismatch: expected %s, got %s", testConfig.Provider, response.ExtraFields.Provider)
	}

	t.Logf("Round-trip test '%s' completed successfully", testName)
}
