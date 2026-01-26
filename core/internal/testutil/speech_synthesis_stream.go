package testutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSpeechSynthesisStreamTest executes the streaming speech synthesis test scenario
func RunSpeechSynthesisStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesisStream {
		t.Logf("Speech synthesis streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesisStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Test streaming with different text lengths
		testCases := []struct {
			name            string
			text            string
			voice           string
			format          string
			expectMinChunks int
			expectMinBytes  int
			skip            bool
		}{
			{
				name:            "ShortText_Streaming",
				text:            "This is a short text for streaming speech synthesis test.",
				voice:           GetProviderVoice(testConfig.Provider, "primary"),
				format:          GetProviderDefaultFormat(testConfig.Provider),
				expectMinChunks: 1,
				expectMinBytes:  1000,
				skip:            false,
			},
			{
				name: "LongText_Streaming",
				text: `This is a longer text to test streaming speech synthesis functionality. 
				       The streaming should provide audio chunks as they are generated, allowing for 
				       real-time playback while the rest of the audio is still being processed. 
				       This enables better user experience with reduced latency.`,
				voice:           GetProviderVoice(testConfig.Provider, "secondary"),
				format:          GetProviderDefaultFormat(testConfig.Provider),
				expectMinChunks: 2,
				expectMinBytes:  3000,
				skip:            testConfig.Provider == schemas.Gemini,
			},
			// This flow is allowed to only pro accounts
			// {
			// 	name:            "MediumText_Echo_WAV",
			// 	text:            "Testing streaming with WAV format. This should produce multiple audio chunks in WAV format for streaming playback.",
			// 	voice:           GetProviderVoice(testConfig.Provider, "tertiary"),
			// 	format:          "wav",
			// 	expectMinChunks: 1,
			// 	expectMinBytes:  2000,
			// 	skip:            false,
			// },
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
					t.Parallel()
				}

				if tc.skip {
					t.Skipf("Skipping %s test", tc.name)
					return
				}

				voice := tc.voice
				request := &schemas.BifrostSpeechRequest{
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
					Fallbacks: testConfig.SpeechSynthesisFallbacks,
				}

				// Use retry framework for streaming speech synthesis
				retryConfig := GetTestRetryConfigForScenario("SpeechSynthesisStream", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: "SpeechSynthesisStream_" + tc.name,
					ExpectedBehavior: map[string]interface{}{
						"generate_streaming_audio": true,
						"voice_type":               tc.voice,
						"format":                   tc.format,
						"min_chunks":               tc.expectMinChunks,
						"min_total_bytes":          tc.expectMinBytes,
					},
					TestMetadata: map[string]interface{}{
						"provider":    testConfig.Provider,
						"model":       testConfig.SpeechSynthesisModel,
						"text_length": len(tc.text),
						"voice":       tc.voice,
						"format":      tc.format,
					},
				}

				

				responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
					requestCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					return client.SpeechStreamRequest(requestCtx, request)
				})

				// Enhanced validation for streaming speech synthesis
				if err != nil {
					RequireNoError(t, err, "Speech synthesis stream initiation failed")
				}
				if responseChannel == nil {
					t.Fatal("Response channel should not be nil")
				}

				var totalBytes int
				var chunkCount int
				var lastResponse *schemas.BifrostStreamChunk
				var streamErrors []string
				var lastTokenLatency int64
				var audioBuffer bytes.Buffer // Accumulate audio chunks for validation

				// Read streaming chunks with enhanced validation
				for response := range responseChannel {
					if response == nil {
						streamErrors = append(streamErrors, "Received nil stream response")
						continue
					}

					// Check for errors in stream
					if response.BifrostError != nil {
						streamErrors = append(streamErrors, FormatErrorConcise(ParseBifrostError(response.BifrostError)))
						continue
					}

					if response.BifrostSpeechStreamResponse != nil {
						lastTokenLatency = response.BifrostSpeechStreamResponse.ExtraFields.Latency
					}

					if response.BifrostSpeechStreamResponse == nil {
						streamErrors = append(streamErrors, "Stream response missing speech stream payload")
						continue
					}

					if response.BifrostSpeechStreamResponse.Audio == nil {
						streamErrors = append(streamErrors, "Stream response missing audio data")
						continue
					}

					// Log latency for each chunk (can be 0 for inter-chunks)
					t.Logf("📊 Speech chunk %d latency: %d ms", chunkCount+1, response.BifrostSpeechStreamResponse.ExtraFields.Latency)

					// Collect audio chunks
					if response.BifrostSpeechStreamResponse.Audio != nil {
						chunkSize := len(response.BifrostSpeechStreamResponse.Audio)
						if chunkSize == 0 {
							t.Logf("⚠️ Skipping zero-length audio chunk")
							continue
						}
						// Accumulate audio data for codec validation
						audioBuffer.Write(response.BifrostSpeechStreamResponse.Audio)
						totalBytes += chunkSize
						chunkCount++
						t.Logf("✅ Received audio chunk %d: %d bytes", chunkCount, chunkSize)

						// Validate chunk structure
						if response.BifrostSpeechStreamResponse.Type != "" && (response.BifrostSpeechStreamResponse.Type != schemas.SpeechStreamResponseTypeDelta && response.BifrostSpeechStreamResponse.Type != schemas.SpeechStreamResponseTypeDone) {
							t.Logf("⚠️ Unexpected object type in stream: %s", response.BifrostSpeechStreamResponse.Type)
						}
						if response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested != "" && response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested != testConfig.SpeechSynthesisModel {
							t.Logf("⚠️ Unexpected model in stream: %s", response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested)
						}
					}

					lastResponse = DeepCopyBifrostStreamChunk(response)
				}

				// Enhanced validation of streaming results
				if len(streamErrors) > 0 {
					t.Logf("⚠️ Stream errors encountered: %v", streamErrors)
				}

				if chunkCount < tc.expectMinChunks {
					t.Fatalf("Insufficient chunks received: got %d, expected at least %d", chunkCount, tc.expectMinChunks)
				}

				if totalBytes < tc.expectMinBytes {
					t.Fatalf("Insufficient audio data: got %d bytes, expected at least %d", totalBytes, tc.expectMinBytes)
				}

				if lastResponse == nil {
					t.Fatal("Should have received at least one response")
				}

				// Additional streaming-specific validations
				if chunkCount == 0 {
					t.Fatal("No audio chunks received from stream")
				}

				averageChunkSize := totalBytes / chunkCount
				if averageChunkSize < 100 {
					t.Logf("Average chunk size seems small: %d bytes", averageChunkSize)
				}

				if lastTokenLatency == 0 {
					t.Fatalf("❌ Last token latency is 0")
				}

				// Save audio to temp file, validate codec, and cleanup after test
				if audioBuffer.Len() > 0 {
					var err error
					audioData := audioBuffer.Bytes()
					if testConfig.Provider == schemas.Gemini {
						audioData, err = utils.ConvertPCMToWAV(audioData, utils.DefaultGeminiPCMConfig())
						if err != nil {
							t.Fatalf("Failed to convert PCM to WAV: %v", err)
						}
					}
					filePath, validationErr := SaveAndValidateAudio(t, audioData)
					if validationErr != nil {
						t.Fatalf("Audio codec validation failed: %v", validationErr)
					}
					t.Logf("Audio file validated successfully: %s", filePath)
				} else {
					t.Fatal("No audio data accumulated for codec validation")
				}

				t.Logf("✅ Streaming speech synthesis successful: %d chunks, %d total bytes for voice '%s' in %s format",
					chunkCount, totalBytes, tc.voice, tc.format)
			})
		}
	})
}

// RunSpeechSynthesisStreamAdvancedTest executes advanced streaming speech synthesis test scenarios
func RunSpeechSynthesisStreamAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesisStream {
		t.Logf("Speech synthesis streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesisStreamAdvanced", func(t *testing.T) {
		t.Run("LongText_HDModel_Streaming", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			if testConfig.Provider == schemas.Gemini {
				t.Skipf("Skipping %s test", "LongText_HDModel_Streaming")
				return
			}

			// Test streaming with HD model and very long text
			finalText := ""
			for i := 1; i <= 20; i++ {
				finalText += strings.Replace("This is sentence number %d in a very long text for testing streaming speech synthesis with the HD model. ", "%d", string(rune('0'+i%10)), -1)
			}

			voice := GetProviderVoice(testConfig.Provider, "tertiary")
			request := &schemas.BifrostSpeechRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.SpeechSynthesisModel,
				Input: &schemas.SpeechInput{
					Input: finalText,
				},
				Params: &schemas.SpeechParameters{
					VoiceConfig: &schemas.SpeechVoiceInput{
						Voice: &voice,
					},
					ResponseFormat: GetProviderDefaultFormat(testConfig.Provider),
					Instructions:   "Speak at a natural pace with clear pronunciation.",
				},
				Fallbacks: testConfig.SpeechSynthesisFallbacks,
			}

			retryConfig := GetTestRetryConfigForScenario("SpeechSynthesisStreamHD", testConfig)
			retryContext := TestRetryContext{
				ScenarioName: "SpeechSynthesisStreamHD_LongText",
				ExpectedBehavior: map[string]interface{}{
					"generate_hd_streaming_audio": true,
					"handle_long_text":            true,
					"min_chunks":                  3,
					"min_total_bytes":             10000,
				},
				TestMetadata: map[string]interface{}{
					"provider":    testConfig.Provider,
					"model":       testConfig.SpeechSynthesisModel,
					"text_length": len(finalText),
					"voice":       voice,
				},
			}

			
			responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				requestCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				return client.SpeechStreamRequest(requestCtx, request)
			})

			RequireNoError(t, err, "HD streaming speech synthesis failed")

			var totalBytes int
			var chunkCount int
			var streamErrors []string
			var lastTokenLatency int64
			var audioBuffer bytes.Buffer // Accumulate audio chunks for validation

			for response := range responseChannel {
				if response == nil {
					streamErrors = append(streamErrors, "Received nil HD stream response")
					continue
				}

				if response.BifrostError != nil {
					streamErrors = append(streamErrors, FormatErrorConcise(ParseBifrostError(response.BifrostError)))
					continue
				}

				if response.BifrostSpeechStreamResponse != nil {
					lastTokenLatency = response.BifrostSpeechStreamResponse.ExtraFields.Latency
				}

				if response.BifrostSpeechStreamResponse != nil && response.BifrostSpeechStreamResponse.Audio != nil {
					chunkSize := len(response.BifrostSpeechStreamResponse.Audio)
					if chunkSize == 0 {
						t.Logf("⚠️ Skipping zero-length HD audio chunk")
						continue
					}
					// Accumulate audio data for codec validation
					audioBuffer.Write(response.BifrostSpeechStreamResponse.Audio)
					totalBytes += chunkSize
					chunkCount++
					t.Logf("✅ HD chunk %d: %d bytes", chunkCount, chunkSize)
				}

				if response.BifrostSpeechStreamResponse != nil && response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested != "" && response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested != testConfig.SpeechSynthesisModel {
					t.Logf("⚠️ Unexpected HD model: %s", response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested)
				}
			}

			if len(streamErrors) > 0 {
				t.Logf("⚠️ HD stream errors: %v", streamErrors)
			}

			if chunkCount <= 3 {
				t.Fatalf("HD model should produce more chunks for long text: got %d, expected > 3", chunkCount)
			}

			if totalBytes <= 10000 {
				t.Fatalf("HD model should produce substantial audio data: got %d bytes, expected > 10000", totalBytes)
			}

			if lastTokenLatency == 0 {
				t.Fatalf("❌ Last token latency is 0")
			}

			// Save audio to temp file, validate codec, and cleanup after test
			if audioBuffer.Len() > 0 {
				// If provider is Gemini, we will have to convert the PCM bytes to WAV bytes
				var err error
				audioData := audioBuffer.Bytes()
				if testConfig.Provider == schemas.Gemini {
					audioData, err = utils.ConvertPCMToWAV(audioData, utils.DefaultGeminiPCMConfig())
					if err != nil {
						t.Fatalf("Failed to convert PCM to WAV: %v", err)
					}
				}
				filePath, validationErr := SaveAndValidateAudio(t, audioData)
				if validationErr != nil {
					t.Fatalf("Audio codec validation failed: %v", validationErr)
				}
				t.Logf("Audio file validated successfully (detected format: %s): %s", GetProviderDefaultFormat(testConfig.Provider), filePath)
			} else {
				t.Fatal("No audio data accumulated for codec validation")
			}

			t.Logf("✅ HD streaming successful: %d chunks, %d total bytes", chunkCount, totalBytes)
		})

		t.Run("MultipleVoices_Streaming", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			voices := []string{}

			// Test streaming with all available voices
			openaiVoices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
			geminiVoices := []string{"achernar", "achird", "erinome"}

			// it's not possible to test all voices with Elevenlabs, we are using a few
			elevenlabsVoices := []string{"21m00Tcm4TlvDq8ikWAM", "29vD33N1CtxCmqQRPOHJ", "2EiwWnXFnvU5JabPnv8n"}

			testText := "Testing streaming speech synthesis with different voice options."

			switch testConfig.Provider {
			case schemas.OpenAI:
				voices = openaiVoices
			case schemas.Gemini:
				voices = geminiVoices
			case schemas.Elevenlabs:
				voices = elevenlabsVoices
			}

			for _, voice := range voices {
				voiceCopy := voice
				t.Run("StreamingVoice_"+voiceCopy, func(t *testing.T) {
					if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
						t.Parallel()
					}

					request := &schemas.BifrostSpeechRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.SpeechSynthesisModel,
						Input: &schemas.SpeechInput{
							Input: testText,
						},
						Params: &schemas.SpeechParameters{
							VoiceConfig: &schemas.SpeechVoiceInput{
								Voice: &voiceCopy,
							},
							ResponseFormat: GetProviderDefaultFormat(testConfig.Provider),
						},
						Fallbacks: testConfig.SpeechSynthesisFallbacks,
					}

					retryConfig := GetTestRetryConfigForScenario("SpeechSynthesisStreamVoice", testConfig)
					retryContext := TestRetryContext{
						ScenarioName: "SpeechSynthesisStream_Voice_" + voiceCopy,
						ExpectedBehavior: map[string]interface{}{
							"generate_streaming_audio": true,
							"voice_type":               voiceCopy,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"voice":    voiceCopy,
						},
					}

					
					// Use retry framework with stream validation
					var accumulatedAudio bytes.Buffer // Accumulate audio for codec validation
					validationResult := WithSpeechStreamValidationRetry(
						t,
						retryConfig,
						retryContext,
						func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {							
							accumulatedAudio.Reset() // Reset buffer on retry
							requestCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
							return client.SpeechStreamRequest(requestCtx, request)
						},
						func(responseChannel chan *schemas.BifrostStreamChunk) SpeechStreamValidationResult {
							// Validate stream content
							var receivedData bool
							var streamErrors []string
							var lastTokenLatency int64
							var validationErrors []string

							for response := range responseChannel {
								if response == nil {
									streamErrors = append(streamErrors, fmt.Sprintf("Received nil stream response for voice %s", voiceCopy))
									continue
								}

								if response.BifrostError != nil {
									streamErrors = append(streamErrors, fmt.Sprintf("Error in stream for voice %s: %s", voiceCopy, FormatErrorConcise(ParseBifrostError(response.BifrostError))))
									continue
								}

								if response.BifrostSpeechStreamResponse != nil {
									lastTokenLatency = response.BifrostSpeechStreamResponse.ExtraFields.Latency
								}

								if response.BifrostSpeechStreamResponse != nil && response.BifrostSpeechStreamResponse.Audio != nil && len(response.BifrostSpeechStreamResponse.Audio) > 0 {
									receivedData = true
									// Accumulate audio data for codec validation
									accumulatedAudio.Write(response.BifrostSpeechStreamResponse.Audio)
									t.Logf("✅ Received data for voice %s: %d bytes", voiceCopy, len(response.BifrostSpeechStreamResponse.Audio))
								}
							}

							// Build validation errors
							if len(streamErrors) > 0 {
								validationErrors = append(validationErrors, fmt.Sprintf("Stream errors: %v", streamErrors))
							}

							if !receivedData {
								validationErrors = append(validationErrors, fmt.Sprintf("Should receive audio data for voice %s", voiceCopy))
							}

							if lastTokenLatency == 0 {
								validationErrors = append(validationErrors, "Last token latency is 0")
							}

							return SpeechStreamValidationResult{
								Passed:       len(validationErrors) == 0,
								Errors:       validationErrors,
								ReceivedData: receivedData,
								StreamErrors: streamErrors,
								LastLatency:  lastTokenLatency,
							}
						},
					)

					// Check validation result
					if !validationResult.Passed {
						allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
						t.Fatalf("❌ Speech streaming validation failed for voice %s: %s", voiceCopy, strings.Join(allErrors, "; "))
					}

					// Save audio to temp file, validate codec, and cleanup after test
					if accumulatedAudio.Len() > 0 {
						var err error
						audioData := accumulatedAudio.Bytes()
						if testConfig.Provider == schemas.Gemini {
							audioData, err = utils.ConvertPCMToWAV(audioData, utils.DefaultGeminiPCMConfig())
							if err != nil {
								t.Fatalf("Failed to convert PCM to WAV: %v", err)
							}
						}
						filePath, validationErr := SaveAndValidateAudio(t, audioData)
						if validationErr != nil {
							t.Fatalf("❌ Audio codec validation failed for voice %s: %v", voiceCopy, validationErr)
						}
						t.Logf("🎵 Audio file validated successfully for voice %s: %s", voiceCopy, filePath)
					} else {
						t.Fatalf("❌ No audio data accumulated for codec validation (voice: %s)", voiceCopy)
					}

					t.Logf("✅ Streaming successful for voice: %s", voiceCopy)
				})
			}
		})
	})
}
