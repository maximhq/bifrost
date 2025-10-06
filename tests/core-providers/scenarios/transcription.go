package scenarios

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunTranscriptionTest executes the transcription test scenario
func RunTranscriptionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
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
				// Step 1: Generate TTS audio
				voice := GetProviderVoice(testConfig.Provider, tc.voiceType)
				ttsRequest := &schemas.BifrostSpeechRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.SpeechSynthesisModel,
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

				ttsResponse, err := client.SpeechRequest(ctx, ttsRequest)
				RequireNoError(t, err, "TTS generation failed for round-trip test")
				if ttsResponse.Speech == nil || ttsResponse.Speech.Audio == nil || len(ttsResponse.Speech.Audio) == 0 {
					t.Fatal("TTS returned invalid or empty audio for round-trip test")
				}

				// Save temp audio file
				tempDir := os.TempDir()
				audioFileName := filepath.Join(tempDir, "roundtrip_"+tc.name+"."+tc.format)
				writeErr := os.WriteFile(audioFileName, ttsResponse.Speech.Audio, 0644)
				require.NoError(t, writeErr, "Failed to save temp audio file")

				// Register cleanup
				t.Cleanup(func() {
					os.Remove(audioFileName)
				})

				t.Logf("🔄 Generated TTS audio for round-trip: %s (%d bytes)", audioFileName, len(ttsResponse.Speech.Audio))

				// Step 2: Transcribe the generated audio
				transcriptionRequest := &schemas.BifrostTranscriptionRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.TranscriptionModel,
					Input: &schemas.TranscriptionInput{
						File: ttsResponse.Speech.Audio,
					},
					Params: &schemas.TranscriptionParameters{
						Language:       bifrost.Ptr("en"),
						Format:         bifrost.Ptr("mp3"),
						ResponseFormat: tc.responseFormat,
					},
					Fallbacks: testConfig.Fallbacks,
				}

				retryConfig := GetTestRetryConfigForScenario("Transcription_RoundTrip", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: "Transcription_RoundTrip_" + tc.name,
					ExpectedBehavior: map[string]interface{}{
						"transcribe_audio": true,
						"round_trip_test":  true,
						"original_text":    tc.text,
					},
					TestMetadata: map[string]interface{}{
						"provider":     testConfig.Provider,
						"model":        testConfig.TranscriptionModel,
						"audio_format": tc.format,
					},
				}

				// Enhanced validation for transcription
				expectations := TranscriptionExpectations(10) // Expect at least some content
				expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

				transcriptionResponse, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "Transcription_RoundTrip_"+tc.name, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
					return client.TranscriptionRequest(ctx, transcriptionRequest)
				})
				if bifrostErr != nil {
					t.Fatalf("❌ Transcription_RoundTrip_"+tc.name+" request failed after retries: %v", GetErrorMessage(bifrostErr))
				}

				// Validate round-trip: check if transcribed text contains key words from original
				if transcriptionResponse.Transcribe == nil {
					t.Fatal("Transcription response missing transcribe data")
				}

				transcribedText := transcriptionResponse.Transcribe.Text
				if transcribedText == "" {
					t.Fatal("Transcribed text should not be empty")
				}

				// Normalize for comparison (lowercase, remove punctuation)
				originalWords := strings.Fields(strings.ToLower(tc.text))
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
				assert.GreaterOrEqual(t, foundWords, minExpectedWords,
					"Round-trip failed: original='%s', transcribed='%s', found %d/%d words",
					tc.text, transcribedText, foundWords, len(originalWords))

				// Validate response structure
				assert.Equal(t, "audio.transcription", transcriptionResponse.Object)
				assert.Equal(t, testConfig.TranscriptionModel, transcriptionResponse.Model)
				assert.Equal(t, testConfig.Provider, transcriptionResponse.ExtraFields.Provider)

				// For verbose_json format, check additional fields
				if tc.responseFormat != nil && *tc.responseFormat == "verbose_json" {
					assert.NotNil(t, transcriptionResponse.Transcribe.BifrostTranscribeNonStreamResponse)
					if transcriptionResponse.Transcribe.Task != nil {
						assert.Equal(t, "transcribe", *transcriptionResponse.Transcribe.Task)
					}
					if transcriptionResponse.Transcribe.Language != nil {
						assert.NotEmpty(t, *transcriptionResponse.Transcribe.Language)
					}
				}

				t.Logf("✅ Round-trip successful: '%s' → TTS → SST → '%s' (found %d/%d words)",
					tc.text, transcribedText, foundWords, len(originalWords))
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
					// Use the utility function to generate audio
					audioData, _ := GenerateTTSAudioForTest(ctx, t, client, testConfig.Provider, testConfig.SpeechSynthesisModel, tc.text, "primary", "mp3")

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
						Fallbacks: testConfig.Fallbacks,
					}

					response, err := client.TranscriptionRequest(ctx, request)
					require.Nilf(t, err, "Custom transcription failed: %v", err)
					require.NotNil(t, response.Transcribe)
					assert.NotEmpty(t, response.Transcribe.Text)

					t.Logf("✅ Custom transcription successful: '%s' → '%s'", tc.text, response.Transcribe.Text)
				})
			}
		})
	})
}

// RunTranscriptionAdvancedTest executes advanced transcription test scenarios
func RunTranscriptionAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Transcription {
		t.Logf("Transcription not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TranscriptionAdvanced", func(t *testing.T) {
		t.Run("AllResponseFormats", func(t *testing.T) {
			// Generate audio first for all format tests
			audioData, _ := GenerateTTSAudioForTest(ctx, t, client, testConfig.Provider, testConfig.SpeechSynthesisModel, TTSTestTextBasic, "primary", "mp3")

			// Test supported response formats (excluding text to avoid JSON parsing issues)
			formats := []string{"json", "verbose_json"}

			for _, format := range formats {
				t.Run("Format_"+format, func(t *testing.T) {
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
						Fallbacks: testConfig.Fallbacks,
					}

					response, err := client.TranscriptionRequest(ctx, request)
					require.Nilf(t, err, "Transcription failed for format %s: %v", format, err)
					require.NotNil(t, response)
					require.NotNil(t, response.Transcribe)

					// All formats should return some text
					assert.NotEmpty(t, response.Transcribe.Text)

					t.Logf("✅ Format %s successful: '%s'", format, response.Transcribe.Text)
				})
			}
		})

		t.Run("WithCustomParameters", func(t *testing.T) {
			// Generate audio for custom parameters test
			audioData, _ := GenerateTTSAudioForTest(ctx, t, client, testConfig.Provider, testConfig.SpeechSynthesisModel, TTSTestTextMedium, "secondary", "mp3")

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
				Fallbacks: testConfig.Fallbacks,
			}

			response, err := client.TranscriptionRequest(ctx, request)
			require.Nilf(t, err, "Advanced transcription failed: %v", err)
			require.NotNil(t, response)
			require.NotNil(t, response.Transcribe)
			assert.NotEmpty(t, response.Transcribe.Text)

			t.Logf("✅ Advanced transcription successful: '%s'", response.Transcribe.Text)
		})

		t.Run("MultipleLanguages", func(t *testing.T) {
			// Generate audio for language tests
			audioData, _ := GenerateTTSAudioForTest(ctx, t, client, testConfig.Provider, testConfig.SpeechSynthesisModel, TTSTestTextBasic, "primary", "mp3")

			// Test with different language hints (only English for now since our TTS is English)
			languages := []string{"en"}

			for _, lang := range languages {
				t.Run("Language_"+lang, func(t *testing.T) {
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
						Fallbacks: testConfig.Fallbacks,
					}

					response, err := client.TranscriptionRequest(ctx, request)
					require.Nilf(t, err, "Transcription failed for language %s: %v", lang, err)
					require.NotNil(t, response)
					require.NotNil(t, response.Transcribe)

					assert.NotEmpty(t, response.Transcribe.Text)
					t.Logf("✅ Language %s transcription successful: '%s'", lang, response.Transcribe.Text)
				})
			}
		})
	})
}
