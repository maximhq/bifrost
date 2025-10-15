package scenarios

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunSpeechSynthesisStreamTest executes the streaming speech synthesis test scenario
func RunSpeechSynthesisStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesisStream {
		t.Logf("Speech synthesis streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesisStream", func(t *testing.T) {
		// Test streaming with different text lengths
		testCases := []struct {
			name            string
			text            string
			voice           string
			format          string
			expectMinChunks int
			expectMinBytes  int
		}{
			{
				name:            "ShortText_Streaming",
				text:            "This is a short text for streaming speech synthesis test.",
				voice:           GetProviderVoice(testConfig.Provider, "primary"),
				format:          "mp3",
				expectMinChunks: 1,
				expectMinBytes:  1000,
			},
			{
				name: "LongText_Streaming",
				text: `This is a longer text to test streaming speech synthesis functionality. 
				       The streaming should provide audio chunks as they are generated, allowing for 
				       real-time playback while the rest of the audio is still being processed. 
				       This enables better user experience with reduced latency.`,
				voice:           GetProviderVoice(testConfig.Provider, "secondary"),
				format:          "mp3",
				expectMinChunks: 2,
				expectMinBytes:  3000,
			},
			{
				name:            "MediumText_Echo_WAV",
				text:            "Testing streaming with WAV format. This should produce multiple audio chunks in WAV format for streaming playback.",
				voice:           GetProviderVoice(testConfig.Provider, "tertiary"),
				format:          "wav",
				expectMinChunks: 1,
				expectMinBytes:  2000,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
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
					Fallbacks: testConfig.Fallbacks,
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

				responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
					return client.SpeechStreamRequest(ctx, request)
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
				var lastResponse *schemas.BifrostStream
				var streamErrors []string

				streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()

				// Read streaming chunks with enhanced validation
				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							// Channel closed, streaming complete
							goto streamComplete
						}

						if response == nil {
							streamErrors = append(streamErrors, "Received nil stream response")
							continue
						}

						// Check for errors in stream
						if response.BifrostError != nil {
							streamErrors = append(streamErrors, FormatErrorConcise(ParseBifrostError(response.BifrostError)))
							continue
						}

						if response.BifrostSpeechStreamResponse == nil {
							streamErrors = append(streamErrors, "Stream response missing speech stream payload")
							continue
						}

						if response.BifrostSpeechStreamResponse.Audio == nil {
							streamErrors = append(streamErrors, "Stream response missing audio data")
							continue
						}

						// Collect audio chunks
						if response.BifrostSpeechStreamResponse.Audio != nil {
							chunkSize := len(response.BifrostSpeechStreamResponse.Audio)
							if chunkSize == 0 {
								t.Logf("⚠️ Skipping zero-length audio chunk")
								continue
							}
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

						lastResponse = response

					case <-streamCtx.Done():
						streamErrors = append(streamErrors, "Stream reading timed out")
						goto streamComplete
					}
				}

			streamComplete:
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
					t.Logf("⚠️ Average chunk size seems small: %d bytes", averageChunkSize)
				}

				t.Logf("✅ Streaming speech synthesis successful: %d chunks, %d total bytes for voice '%s' in %s format",
					chunkCount, totalBytes, tc.voice, tc.format)
			})
		}
	})
}

// RunSpeechSynthesisStreamAdvancedTest executes advanced streaming speech synthesis test scenarios
func RunSpeechSynthesisStreamAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesisStream {
		t.Logf("Speech synthesis streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesisStreamAdvanced", func(t *testing.T) {
		t.Run("LongText_HDModel_Streaming", func(t *testing.T) {
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
					ResponseFormat: "mp3",
					Instructions:   "Speak at a natural pace with clear pronunciation.",
				},
				Fallbacks: testConfig.Fallbacks,
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

			responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
				return client.SpeechStreamRequest(ctx, request)
			})

			RequireNoError(t, err, "HD streaming speech synthesis failed")

			var totalBytes int
			var chunkCount int
			var streamErrors []string

			streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			for {
				select {
				case response, ok := <-responseChannel:
					if !ok {
						goto hdStreamComplete
					}

					if response == nil {
						streamErrors = append(streamErrors, "Received nil HD stream response")
						continue
					}

					if response.BifrostError != nil {
						streamErrors = append(streamErrors, FormatErrorConcise(ParseBifrostError(response.BifrostError)))
						continue
					}

					if response.BifrostSpeechStreamResponse != nil && response.BifrostSpeechStreamResponse.Audio != nil {
						chunkSize := len(response.BifrostSpeechStreamResponse.Audio)
						if chunkSize == 0 {
							t.Logf("⚠️ Skipping zero-length HD audio chunk")
							continue
						}
						totalBytes += chunkSize
						chunkCount++
						t.Logf("✅ HD chunk %d: %d bytes", chunkCount, chunkSize)
					}

					if response.BifrostSpeechStreamResponse != nil && response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested != "" && response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested != testConfig.SpeechSynthesisModel {
						t.Logf("⚠️ Unexpected HD model: %s", response.BifrostSpeechStreamResponse.ExtraFields.ModelRequested)
					}

				case <-streamCtx.Done():
					streamErrors = append(streamErrors, "HD stream reading timed out")
					goto hdStreamComplete
				}
			}

		hdStreamComplete:
			if len(streamErrors) > 0 {
				t.Logf("⚠️ HD stream errors: %v", streamErrors)
			}

			if chunkCount <= 3 {
				t.Fatalf("HD model should produce more chunks for long text: got %d, expected > 3", chunkCount)
			}

			if totalBytes <= 10000 {
				t.Fatalf("HD model should produce substantial audio data: got %d bytes, expected > 10000", totalBytes)
			}

			t.Logf("✅ HD streaming successful: %d chunks, %d total bytes", chunkCount, totalBytes)
		})

		t.Run("MultipleVoices_Streaming", func(t *testing.T) {
			// Test streaming with all available voices
			voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
			testText := "Testing streaming speech synthesis with different voice options."

			for _, voice := range voices {
				t.Run("StreamingVoice_"+voice, func(t *testing.T) {
					voiceCopy := voice
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
							ResponseFormat: "mp3",
						},
						Fallbacks: testConfig.Fallbacks,
					}

					retryConfig := GetTestRetryConfigForScenario("SpeechSynthesisStreamVoice", testConfig)
					retryContext := TestRetryContext{
						ScenarioName: "SpeechSynthesisStream_Voice_" + voice,
						ExpectedBehavior: map[string]interface{}{
							"generate_streaming_audio": true,
							"voice_type":               voice,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"voice":    voice,
						},
					}

					responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
						return client.SpeechStreamRequest(ctx, request)
					})

					RequireNoError(t, err, fmt.Sprintf("Streaming failed for voice %s", voice))

					var receivedData bool
					var streamErrors []string

					streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()

					for {
						select {
						case response, ok := <-responseChannel:
							if !ok {
								goto voiceStreamComplete
							}

							if response == nil {
								streamErrors = append(streamErrors, fmt.Sprintf("Received nil stream response for voice %s", voice))
								continue
							}

							if response.BifrostError != nil {
								streamErrors = append(streamErrors, fmt.Sprintf("Error in stream for voice %s: %s", voice, FormatErrorConcise(ParseBifrostError(response.BifrostError))))
								continue
							}

							if response.BifrostSpeechStreamResponse != nil && response.BifrostSpeechStreamResponse.Audio != nil && len(response.BifrostSpeechStreamResponse.Audio) > 0 {
								receivedData = true
								t.Logf("✅ Received data for voice %s: %d bytes", voice, len(response.BifrostSpeechStreamResponse.Audio))
							}

						case <-streamCtx.Done():
							streamErrors = append(streamErrors, fmt.Sprintf("Stream timed out for voice %s", voice))
							goto voiceStreamComplete
						}
					}

				voiceStreamComplete:
					if len(streamErrors) > 0 {
						t.Logf("⚠️ Stream errors for voice %s: %v", voice, streamErrors)
					}

					if !receivedData {
						t.Fatalf("Should receive audio data for voice %s", voice)
					}
					t.Logf("✅ Streaming successful for voice: %s", voice)
				})
			}
		})
	})
}
