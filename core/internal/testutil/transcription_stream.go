package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunTranscriptionStreamTest executes the streaming transcription test scenario
func RunTranscriptionStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TranscriptionStream {
		t.Logf("Transcription streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TranscriptionStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Generate TTS audio for streaming round-trip validation
		streamRoundTripCases := []struct {
			name           string
			text           string
			voiceType      string
			format         string
			responseFormat *string
		}{
			{
				name:           "StreamRoundTrip_Basic_MP3",
				text:           TTSTestTextBasic,
				voiceType:      "primary",
				format:         "mp3",
				responseFormat: nil, // Default JSON streaming
			},
			{
				name:           "StreamRoundTrip_Medium_MP3",
				text:           TTSTestTextMedium,
				voiceType:      "secondary",
				format:         "mp3",
				responseFormat: bifrost.Ptr("json"),
			},
			{
				name:           "StreamRoundTrip_Technical_MP3",
				text:           TTSTestTextTechnical,
				voiceType:      "tertiary",
				format:         "mp3",
				responseFormat: bifrost.Ptr("json"),
			},
		}

		for _, tc := range streamRoundTripCases {
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
					Fallbacks: testConfig.TranscriptionFallbacks,
				}

				// Use retry framework for TTS generation
				ttsRetryConfig := GetTestRetryConfigForScenario("SpeechSynthesis", testConfig)
				ttsRetryContext := TestRetryContext{
					ScenarioName: "TranscriptionStream_TTS",
					ExpectedBehavior: map[string]interface{}{
						"should_generate_audio": true,
					},
					TestMetadata: map[string]interface{}{
						"provider": speechSynthesisProvider,
						"model":    speechSynthesisModel,
					},
				}
				ttsExpectations := SpeechExpectations(100)
				ttsExpectations = ModifyExpectationsForProvider(ttsExpectations, testConfig.Provider)
				ttsSpeechRetryConfig := SpeechRetryConfig{
					MaxAttempts: ttsRetryConfig.MaxAttempts,
					BaseDelay:   ttsRetryConfig.BaseDelay,
					MaxDelay:    ttsRetryConfig.MaxDelay,
					Conditions:  []SpeechRetryCondition{},
					OnRetry:     ttsRetryConfig.OnRetry,
					OnFinalFail: ttsRetryConfig.OnFinalFail,
				}

				ttsResponse, err := WithSpeechTestRetry(t, ttsSpeechRetryConfig, ttsRetryContext, ttsExpectations, "TranscriptionStream_TTS", func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.SpeechRequest(bfCtx, ttsRequest)
				})
				if err != nil {
					t.Fatalf("❌ TTS generation failed for stream round-trip test after retries: %v", GetErrorMessage(err))
				}
				if ttsResponse == nil || len(ttsResponse.Audio) == 0 {
					t.Fatal("❌ TTS returned invalid or empty audio for stream round-trip test after retries")
				}

				// Save temp audio file
				tempDir := os.TempDir()
				audioFileName := filepath.Join(tempDir, "stream_roundtrip_"+tc.name+"."+tc.format)
				writeErr := os.WriteFile(audioFileName, ttsResponse.Audio, 0644)
				if writeErr != nil {
					t.Fatalf("Failed to save temp audio file: %v", writeErr)
				}

				// Register cleanup
				t.Cleanup(func() {
					os.Remove(audioFileName)
				})

				t.Logf("Generated TTS audio for stream round-trip: %s (%d bytes)", audioFileName, len(ttsResponse.Audio))

				// Step 2: Test streaming transcription
				streamRequest := &schemas.BifrostTranscriptionRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.TranscriptionModel,
					Input: &schemas.TranscriptionInput{
						File: ttsResponse.Audio,
					},
					Params: &schemas.TranscriptionParameters{
						Language:       bifrost.Ptr("en"),
						Format:         bifrost.Ptr(tc.format),
						ResponseFormat: tc.responseFormat,
					},
					Fallbacks: testConfig.TranscriptionFallbacks,
				}

				// Use retry framework for streaming transcription
				retryConfig := GetTestRetryConfigForScenario("TranscriptionStream", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: "TranscriptionStream_" + tc.name,
					ExpectedBehavior: map[string]interface{}{
						"transcribe_streaming_audio": true,
						"round_trip_test":            true,
						"original_text":              tc.text,
					},
					TestMetadata: map[string]interface{}{
						"provider":     testConfig.Provider,
						"model":        testConfig.TranscriptionModel,
						"audio_format": tc.format,
						"voice_type":   tc.voiceType,
					},
				}

				responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.TranscriptionStreamRequest(bfCtx, streamRequest)
				})

				RequireNoError(t, err, "Transcription stream initiation failed")
				if responseChannel == nil {
					t.Fatal("Response channel should not be nil")
				}

				streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()

				fullTranscriptionText := ""
				lastResponse := &schemas.BifrostStreamChunk{}
				streamErrors := []string{}
				lastTokenLatency := int64(0)

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

						if response.BifrostTranscriptionStreamResponse == nil {
							streamErrors = append(streamErrors, "Stream response missing transcription stream payload")
							continue
						}

						if response.BifrostTranscriptionStreamResponse != nil {
							lastTokenLatency = response.BifrostTranscriptionStreamResponse.ExtraFields.Latency
						}

						if response.BifrostTranscriptionStreamResponse.Text == "" && response.BifrostTranscriptionStreamResponse.Delta == nil {
							streamErrors = append(streamErrors, "Stream response missing transcription data")
							continue
						}

						chunkIndex := response.BifrostTranscriptionStreamResponse.ExtraFields.ChunkIndex

						// Log latency for each chunk (can be 0 for inter-chunks)
						t.Logf("📊 Transcription chunk %d latency: %d ms", chunkIndex, response.BifrostTranscriptionStreamResponse.ExtraFields.Latency)

						// Collect transcription chunks
						transcribeData := response.BifrostTranscriptionStreamResponse
						if transcribeData.Text != "" {
							t.Logf("✅ Received transcription text chunk %d with latency %d ms: '%s'", chunkIndex, response.BifrostTranscriptionStreamResponse.ExtraFields.Latency, transcribeData.Text)
						}

						// Handle delta vs complete text chunks
						if transcribeData.Delta != nil {
							// This is a delta chunk
							deltaText := *transcribeData.Delta
							fullTranscriptionText += deltaText
							t.Logf("✅ Received transcription delta chunk %d with latency %d ms: '%s'", chunkIndex, response.BifrostTranscriptionStreamResponse.ExtraFields.Latency, deltaText)
						}

						// Validate chunk structure
						if response.BifrostTranscriptionStreamResponse.Type != schemas.TranscriptionStreamResponseTypeDelta {
							t.Logf("⚠️ Unexpected object type in stream: %s", response.BifrostTranscriptionStreamResponse.Type)
						}
						if response.BifrostTranscriptionStreamResponse.ExtraFields.ModelRequested != "" && response.BifrostTranscriptionStreamResponse.ExtraFields.ModelRequested != testConfig.TranscriptionModel {
							t.Logf("⚠️ Unexpected model in stream: %s", response.BifrostTranscriptionStreamResponse.ExtraFields.ModelRequested)
						}

						lastResponse = DeepCopyBifrostStreamChunk(response)

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

				if lastResponse == nil {
					t.Fatal("Should have received at least one response")
				}

				if fullTranscriptionText == "" {
					t.Fatal("Transcribed text should not be empty")
				}

				if lastTokenLatency == 0 {
					t.Fatalf("❌ Last token latency is 0")
				}

				// Normalize for comparison (lowercase, remove punctuation)
				originalWords := strings.Fields(strings.ToLower(tc.text))
				transcribedWords := strings.Fields(strings.ToLower(fullTranscriptionText))

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

				// Enhanced round-trip validation with better error reporting
				minExpectedWords := len(originalWords) / 2
				if foundWords < minExpectedWords {
					t.Logf("❌ Stream round-trip validation failed:")
					t.Logf("   Original: '%s'", tc.text)
					t.Logf("   Transcribed: '%s'", fullTranscriptionText)
					t.Logf("   Found %d/%d words (expected at least %d)", foundWords, len(originalWords), minExpectedWords)

					// Log word-by-word comparison for debugging
					t.Logf("   Word comparison:")
					for i, word := range originalWords {
						if i < 5 { // Show first 5 words
							cleanWord := strings.Trim(word, ".,!?;:")
							if len(cleanWord) >= 3 {
								found := false
								for _, transcribed := range transcribedWords {
									if strings.Contains(strings.ToLower(transcribed), cleanWord) {
										found = true
										break
									}
								}
								status := "❌"
								if found {
									status = "✅"
								}
								t.Logf("     %s '%s'", status, cleanWord)
							}
						}
					}
					t.Fatalf("Round-trip accuracy too low: got %d/%d words, need at least %d", foundWords, len(originalWords), minExpectedWords)
				}
			})
		}
	})
}

// RunTranscriptionStreamAdvancedTest executes advanced streaming transcription test scenarios
func RunTranscriptionStreamAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TranscriptionStream {
		t.Logf("Transcription streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TranscriptionStreamAdvanced", func(t *testing.T) {
		t.Run("JSONStreaming", func(t *testing.T) {
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

			// Generate audio for streaming test
			audioData, _ := GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, TTSTestTextBasic, "primary", "mp3")

			// Test streaming with JSON format
			request := &schemas.BifrostTranscriptionRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.TranscriptionModel,
				Input: &schemas.TranscriptionInput{
					File: audioData,
				},
				Params: &schemas.TranscriptionParameters{
					Language:       bifrost.Ptr("en"),
					Format:         bifrost.Ptr("mp3"),
					ResponseFormat: bifrost.Ptr("json"),
				},
				Fallbacks: testConfig.TranscriptionFallbacks,
			}

			retryConfig := GetTestRetryConfigForScenario("TranscriptionStreamJSON", testConfig)
			retryContext := TestRetryContext{
				ScenarioName: "TranscriptionStream_JSON",
				ExpectedBehavior: map[string]interface{}{
					"transcribe_streaming_audio": true,
					"json_format":                true,
				},
				TestMetadata: map[string]interface{}{
					"provider": testConfig.Provider,
					"model":    testConfig.TranscriptionModel,
					"format":   "json",
				},
			}

			responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.TranscriptionStreamRequest(bfCtx, request)
			})

			RequireNoError(t, err, "JSON streaming failed")

			var receivedResponse bool
			var streamErrors []string

			for response := range responseChannel {
				if response == nil {
					streamErrors = append(streamErrors, "Received nil JSON stream response")
					continue
				}

				if response.BifrostError != nil {
					streamErrors = append(streamErrors, FormatErrorConcise(ParseBifrostError(response.BifrostError)))
					continue
				}

				if response.BifrostTranscriptionStreamResponse != nil {
					receivedResponse = true

					// Check for JSON streaming specific fields
					transcribeData := response.BifrostTranscriptionStreamResponse
					if transcribeData.Type != "" {
						t.Logf("✅ Stream type: %v", transcribeData.Type)
						if transcribeData.Delta != nil {
							t.Logf("✅ Delta: %s", *transcribeData.Delta)
						}
					}

					if transcribeData.Text != "" {
						t.Logf("✅ Received transcription text: %s", transcribeData.Text)
					}
				}
			}

			if len(streamErrors) > 0 {
				t.Logf("⚠️ JSON stream errors: %v", streamErrors)
			}

			if !receivedResponse {
				t.Fatal("Should receive at least one response")
			}
			t.Logf("✅ Verbose JSON streaming successful")
		})

		t.Run("MultipleLanguages_Streaming", func(t *testing.T) {
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

			// Generate audio for language streaming tests
			audioData, _ := GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, TTSTestTextBasic, "primary", "mp3")
			// Test streaming with different language hints (only English for now)
			languages := []string{"en"}

			for _, lang := range languages {
				t.Run("StreamLang_"+lang, func(t *testing.T) {
					if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
						t.Parallel()
					}

					langCopy := lang
					request := &schemas.BifrostTranscriptionRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.TranscriptionModel,
						Input: &schemas.TranscriptionInput{
							File: audioData,
						},
						Params: &schemas.TranscriptionParameters{
							Language: &langCopy,
						},
						Fallbacks: testConfig.TranscriptionFallbacks,
					}

					retryConfig := GetTestRetryConfigForScenario("TranscriptionStreamLang", testConfig)
					retryContext := TestRetryContext{
						ScenarioName: "TranscriptionStream_Lang_" + lang,
						ExpectedBehavior: map[string]interface{}{
							"transcribe_streaming_audio": true,
							"language":                   lang,
						},
						TestMetadata: map[string]interface{}{
							"provider": testConfig.Provider,
							"language": lang,
						},
					}

					responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
						bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
						return client.TranscriptionStreamRequest(bfCtx, request)
					})

					RequireNoError(t, err, fmt.Sprintf("Streaming failed for language %s", lang))

					var receivedData bool
					var streamErrors []string
					var lastTokenLatency int64

					for response := range responseChannel {
						if response == nil {
							streamErrors = append(streamErrors, fmt.Sprintf("Received nil stream response for language %s", lang))
							continue
						}

						if response.BifrostError != nil {
							streamErrors = append(streamErrors, fmt.Sprintf("Error in stream for language %s: %s", lang, FormatErrorConcise(ParseBifrostError(response.BifrostError))))
							continue
						}

						if response.BifrostTranscriptionStreamResponse != nil {
							receivedData = true
							t.Logf("✅ Received transcription data for language %s", lang)
							if response.BifrostTranscriptionStreamResponse != nil {
								lastTokenLatency = response.BifrostTranscriptionStreamResponse.ExtraFields.Latency
							}
						}
					}

					if len(streamErrors) > 0 {
						t.Logf("⚠️ Stream errors for language %s: %v", lang, streamErrors)
					}

					if !receivedData {
						t.Fatalf("Should receive transcription data for language %s", lang)
					}

					if lastTokenLatency == 0 {
						t.Fatalf("❌ Last token latency is 0")
					}

					t.Logf("✅ Streaming successful for language: %s", lang)
				})
			}
		})

		t.Run("WithCustomPrompt_Streaming", func(t *testing.T) {
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

			// Generate audio for custom prompt streaming test
			audioData, _ := GenerateTTSAudioForTest(ctx, t, client, speechSynthesisProvider, speechSynthesisModel, TTSTestTextTechnical, "tertiary", "mp3")

			// Test streaming with custom prompt for context
			request := &schemas.BifrostTranscriptionRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.TranscriptionModel,
				Input: &schemas.TranscriptionInput{
					File: audioData,
				},
				Params: &schemas.TranscriptionParameters{
					Language: bifrost.Ptr("en"),
					Prompt:   bifrost.Ptr("This audio contains technical terms, proper nouns, and streaming-related vocabulary."),
				},
				Fallbacks: testConfig.TranscriptionFallbacks,
			}

			retryConfig := GetTestRetryConfigForScenario("TranscriptionStreamPrompt", testConfig)
			retryContext := TestRetryContext{
				ScenarioName: "TranscriptionStream_CustomPrompt",
				ExpectedBehavior: map[string]interface{}{
					"transcribe_streaming_audio": true,
					"custom_prompt":              true,
					"technical_content":          true,
				},
				TestMetadata: map[string]interface{}{
					"provider":   testConfig.Provider,
					"model":      testConfig.TranscriptionModel,
					"has_prompt": true,
				},
			}

			responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.TranscriptionStreamRequest(bfCtx, request)
			})

			RequireNoError(t, err, "Custom prompt streaming failed")

			var chunkCount int
			var streamErrors []string
			var receivedText string
			var lastTokenLatency int64

			for response := range responseChannel {
				if response == nil {
					streamErrors = append(streamErrors, "Received nil stream response with custom prompt")
					continue
				}

				if response.BifrostError != nil {
					streamErrors = append(streamErrors, FormatErrorConcise(ParseBifrostError(response.BifrostError)))
					continue
				}

				if response.BifrostTranscriptionStreamResponse != nil {
					lastTokenLatency = response.BifrostTranscriptionStreamResponse.ExtraFields.Latency
				}

				if response.BifrostTranscriptionStreamResponse != nil && response.BifrostTranscriptionStreamResponse.Text != "" {
					chunkCount++
					chunkText := response.BifrostTranscriptionStreamResponse.Text
					receivedText += chunkText
					t.Logf("✅ Custom prompt chunk %d: '%s'", chunkCount, chunkText)
				}
			}

			if len(streamErrors) > 0 {
				t.Logf("⚠️ Custom prompt stream errors: %v", streamErrors)
			}

			if chunkCount == 0 {
				t.Fatal("Should receive at least one transcription chunk")
			}

			// Additional validation for custom prompt effectiveness
			if receivedText != "" {
				t.Logf("✅ Custom prompt produced transcription: '%s'", receivedText)
			} else {
				t.Logf("⚠️ Custom prompt produced empty transcription")
			}

			if lastTokenLatency == 0 {
				t.Fatalf("❌ Last token latency is 0")
			}

			t.Logf("✅ Custom prompt streaming successful: %d chunks received", chunkCount)
		})
	})
}
