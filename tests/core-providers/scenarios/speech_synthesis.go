package scenarios

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSpeechSynthesisTest executes the speech synthesis test scenario
func RunSpeechSynthesisTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesis {
		t.Logf("Speech synthesis not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesis", func(t *testing.T) {
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
				format:         "mp3",
				expectMinBytes: 1000,
				saveForSST:     true,
			},
			{
				name:           "MediumText_Secondary_MP3",
				text:           TTSTestTextMedium,
				voiceType:      "secondary",
				format:         "mp3",
				expectMinBytes: 2000,
				saveForSST:     true,
			},
			{
				name:           "TechnicalText_Tertiary_MP3",
				text:           TTSTestTextTechnical,
				voiceType:      "tertiary",
				format:         "mp3",
				expectMinBytes: 500,
				saveForSST:     true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				voice := GetProviderVoice(testConfig.Provider, tc.voiceType)
				request := &schemas.BifrostSpeechRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.SpeechSynthesisModel, // Use configured model
					Input: &schemas.SpeechInput{
						Input: tc.text,
					},
					Params: &schemas.SpeechParameters{
						VoiceConfig: &schemas.SpeechVoiceInput{
							Voice: &voice,
						},
						ResponseFormat: tc.format,
					},
					Fallbacks: testConfig.Fallbacks,
				}

				// Use retry framework for speech synthesis (can be resource intensive)
				retryConfig := GetTestRetryConfigForScenario("SpeechSynthesis", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: "SpeechSynthesis_" + tc.name,
					ExpectedBehavior: map[string]interface{}{
						"generate_audio":  true,
						"voice_type":      tc.voiceType,
						"format":          tc.format,
						"min_audio_bytes": tc.expectMinBytes,
					},
					TestMetadata: map[string]interface{}{
						"provider":    testConfig.Provider,
						"model":       testConfig.SpeechSynthesisModel,
						"voice":       voice,
						"text_length": len(tc.text),
					},
				}

				// Enhanced validation for speech synthesis
				expectations := SpeechExpectations(tc.expectMinBytes)
				expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

				response, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "SpeechSynthesis_"+tc.name, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
					return client.SpeechRequest(ctx, request)
				})
				if bifrostErr != nil {
					t.Fatalf("❌ SpeechSynthesis_"+tc.name+" request failed after retries: %v", GetErrorMessage(bifrostErr))
				}

				// Additional speech-specific validations
				if response.Speech == nil || response.Speech.Audio == nil {
					t.Fatal("Speech synthesis response missing audio data")
				}

				audioSize := len(response.Speech.Audio)
				if audioSize < tc.expectMinBytes {
					t.Fatalf("Audio data too small: got %d bytes, expected at least %d", audioSize, tc.expectMinBytes)
				}

				if response.Object != "audio.speech" {
					t.Logf("⚠️ Unexpected response object type: %s", response.Object)
				}

				if response.Model != testConfig.SpeechSynthesisModel {
					t.Logf("⚠️ Model mismatch: expected %s, got %s", testConfig.SpeechSynthesisModel, response.Model)
				}

				// Save audio file for SST round-trip testing if requested
				if tc.saveForSST {
					tempDir := os.TempDir()
					audioFileName := filepath.Join(tempDir, "tts_"+tc.name+"."+tc.format)

					err := os.WriteFile(audioFileName, response.Speech.Audio, 0644)
					require.NoError(t, err, "Failed to save audio file for SST testing")

					// Register cleanup to remove temp file
					t.Cleanup(func() {
						os.Remove(audioFileName)
					})

					t.Logf("💾 Audio saved for SST testing: %s (text: '%s')", audioFileName, tc.text)
				}

				t.Logf("✅ Speech synthesis successful: %d bytes of %s audio generated for voice '%s'",
					len(response.Speech.Audio), tc.format, voice)
			})
		}
	})
}

// RunSpeechSynthesisAdvancedTest executes advanced speech synthesis test scenarios
func RunSpeechSynthesisAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesis {
		t.Logf("Speech synthesis not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesisAdvanced", func(t *testing.T) {
		t.Run("LongText_HDModel", func(t *testing.T) {
			// Test with longer text and HD model
			longText := `
			This is a comprehensive test of the text-to-speech functionality using a longer piece of text.
			The system should be able to handle multiple sentences, proper punctuation, and maintain 
			consistent voice quality throughout the entire speech generation process. This test ensures
			that the speech synthesis can handle realistic use cases with substantial content.
			`

			voice := "shimmer"
			request := &schemas.BifrostSpeechRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.SpeechSynthesisModel,
				Input: &schemas.SpeechInput{
					Input: longText,
				},
				Params: &schemas.SpeechParameters{
					VoiceConfig: &schemas.SpeechVoiceInput{
						Voice: &voice,
					},
					ResponseFormat: "mp3",
					Instructions:   "Speak slowly and clearly with natural intonation.",
				},
				Fallbacks: testConfig.Fallbacks,
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
					"model":       testConfig.SpeechSynthesisModel,
					"text_length": len(longText),
				},
			}

			expectations := SpeechExpectations(5000) // HD should produce substantial audio
			expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

			response, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "SpeechSynthesis_HD", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
				return client.SpeechRequest(ctx, request)
			})
			if bifrostErr != nil {
				t.Fatalf("❌ SpeechSynthesis_HD request failed after retries: %v", GetErrorMessage(bifrostErr))
			}

			if response.Speech == nil || response.Speech.Audio == nil {
				t.Fatal("HD speech synthesis response missing audio data")
			}

			audioSize := len(response.Speech.Audio)
			if audioSize < 5000 {
				t.Fatalf("HD audio data too small: got %d bytes, expected at least 5000", audioSize)
			}

			if response.Model != testConfig.SpeechSynthesisModel {
				t.Logf("⚠️ Expected HD model, got: %s", response.Model)
			}

			t.Logf("✅ HD speech synthesis successful: %d bytes generated", len(response.Speech.Audio))
		})

		t.Run("AllVoiceOptions", func(t *testing.T) {
			// Test provider-specific voice options
			voiceTypes := []string{"primary", "secondary", "tertiary"}
			testText := TTSTestTextBasic // Use shared constant

			for _, voiceType := range voiceTypes {
				t.Run("VoiceType_"+voiceType, func(t *testing.T) {
					voice := GetProviderVoice(testConfig.Provider, voiceType)
					request := &schemas.BifrostSpeechRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.SpeechSynthesisModel,
						Input: &schemas.SpeechInput{
							Input: testText,
						},
						Params: &schemas.SpeechParameters{
							VoiceConfig: &schemas.SpeechVoiceInput{
								Voice: &voice,
							},
							ResponseFormat: "mp3",
						},
						Fallbacks: testConfig.Fallbacks,
					}

					retryConfig := GetTestRetryConfigForScenario("SpeechSynthesis_Voice", testConfig)
					retryContext := TestRetryContext{
						ScenarioName: "SpeechSynthesis_Voice_" + voiceType,
						ExpectedBehavior: map[string]interface{}{
							"generate_audio": true,
							"voice_type":     voiceType,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"voice":    voice,
						},
					}

					expectations := SpeechExpectations(500)
					expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

					response, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "SpeechSynthesis_Voice_"+voiceType, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
						return client.SpeechRequest(ctx, request)
					})

					if bifrostErr != nil {
						t.Fatalf("❌ SpeechSynthesis_Voice_"+voiceType+" request failed after retries: %v", GetErrorMessage(bifrostErr))
					}

					if response.Speech == nil || response.Speech.Audio == nil {
						t.Fatalf("Voice %s (%s) missing audio data", voice, voiceType)
					}

					audioSize := len(response.Speech.Audio)
					if audioSize < 500 {
						t.Fatalf("Audio too small for voice %s: got %d bytes, expected at least 500", voice, audioSize)
					}
					t.Logf("✅ Voice %s (%s): %d bytes generated", voice, voiceType, len(response.Speech.Audio))
				})
			}
		})
	})
}
