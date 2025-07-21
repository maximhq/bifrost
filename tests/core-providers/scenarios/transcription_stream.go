package scenarios

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunTranscriptionStreamTest executes the streaming transcription test scenario
func RunTranscriptionStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TranscriptionStream {
		t.Logf("Transcription streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TranscriptionStream", func(t *testing.T) {
		// Test streaming with different audio formats and configurations
		testCases := []struct {
			name           string
			audioData      []byte
			language       *string
			prompt         *string
			responseFormat *string
			expectChunks   int
		}{
			{
				name:           "BasicMP3_Streaming",
				audioData:      TestAudioDataMP3,
				language:       bifrost.Ptr("en"),
				prompt:         nil,
				responseFormat: nil, // Default JSON streaming
				expectChunks:   1,
			},
			{
				name:           "WAV_WithPrompt_Streaming",
				audioData:      TestAudioDataWAV,
				language:       bifrost.Ptr("en"),
				prompt:         bifrost.Ptr("This is a test audio file for streaming transcription."),
				responseFormat: bifrost.Ptr("verbose_json"),
				expectChunks:   1,
			},
			{
				name:           "MP3_Text_Streaming",
				audioData:      TestAudioDataMP3,
				language:       nil, // Auto-detect
				prompt:         nil,
				responseFormat: bifrost.Ptr("text"),
				expectChunks:   1,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				request := &schemas.BifrostRequest{
					Provider: testConfig.Provider,
					Model:    "gpt-4o-transcribe",
					Input: schemas.RequestInput{
						TranscriptionInput: &schemas.TranscriptionInput{
							File:           tc.audioData,
							Language:       tc.language,
							Prompt:         tc.prompt,
							ResponseFormat: tc.responseFormat,
						},
					},
					Params: MergeModelParameters(&schemas.ModelParameters{
						Temperature: bifrost.Ptr(0.0), // More deterministic output
					}, testConfig.CustomParams),
					Fallbacks: testConfig.Fallbacks,
				}

				// Test streaming response
				responseChannel, err := client.TranscriptionStreamRequest(ctx, request)
				require.Nilf(t, err, "Transcription stream failed: %v", err)
				require.NotNil(t, responseChannel, "Response channel should not be nil")

				var fullTranscriptionText string
				var chunkCount int
				var lastResponse *schemas.BifrostStream

				// Create a timeout context for the stream reading
				streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()

				// Read streaming chunks
				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							// Channel closed, streaming complete
							goto streamComplete
						}

						require.NotNil(t, response, "Stream response should not be nil")

						// Check for errors in stream
						if response.BifrostError != nil {
							t.Fatalf("Error in stream: %v", response.BifrostError)
						}

						require.NotNil(t, response.Transcribe, "Transcribe data should be present in stream")

						// Collect transcription chunks
						if response.Transcribe.Text != "" {
							if response.Transcribe.BifrostTranscribeStreamResponse != nil &&
								response.Transcribe.BifrostTranscribeStreamResponse.Delta != nil {
								// This is a delta chunk
								fullTranscriptionText += *response.Transcribe.BifrostTranscribeStreamResponse.Delta
							} else {
								// This is a complete text chunk
								fullTranscriptionText += response.Transcribe.Text
							}
							chunkCount++
							t.Logf("Received transcription chunk %d: '%s'", chunkCount, response.Transcribe.Text)
						}

						// Validate stream response structure
						assert.Equal(t, "audio.transcription.chunk", response.Object)
						assert.Equal(t, "gpt-4o-transcribe", response.Model)
						assert.Equal(t, testConfig.Provider, response.ExtraFields.Provider)

						lastResponse = response

					case <-streamCtx.Done():
						t.Fatal("Stream reading timed out")
					}
				}

			streamComplete:
				// Validate streaming results
				assert.GreaterOrEqual(t, chunkCount, tc.expectChunks, "Should receive minimum expected chunks")
				assert.NotNil(t, lastResponse, "Should have received at least one response")

				t.Logf("✅ Streaming transcription successful: %d chunks, final text: '%s'",
					chunkCount, fullTranscriptionText)
			})
		}
	})
}

// RunTranscriptionStreamAdvancedTest executes advanced streaming transcription test scenarios
func RunTranscriptionStreamAdvancedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.TranscriptionStream {
		t.Logf("Transcription streaming not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("TranscriptionStreamAdvanced", func(t *testing.T) {
		t.Run("VerboseJSON_Streaming", func(t *testing.T) {
			// Test streaming with verbose JSON format for detailed information
			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    "gpt-4o-transcribe",
				Input: schemas.RequestInput{
					TranscriptionInput: &schemas.TranscriptionInput{
						File:           TestAudioDataMP3,
						Language:       bifrost.Ptr("en"),
						ResponseFormat: bifrost.Ptr("verbose_json"),
					},
				},
				Params: MergeModelParameters(&schemas.ModelParameters{
					ExtraParams: map[string]interface{}{
						"timestamp_granularities": []string{"word", "segment"},
					},
				}, testConfig.CustomParams),
				Fallbacks: testConfig.Fallbacks,
			}

			responseChannel, err := client.TranscriptionStreamRequest(ctx, request)
			require.Nilf(t, err, "Verbose JSON streaming failed: %v", err)

			var receivedResponse bool
			streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			for {
				select {
				case response, ok := <-responseChannel:
					if !ok {
						goto verboseStreamComplete
					}

					if response.BifrostError != nil {
						t.Fatalf("Error in verbose stream: %v", response.BifrostError)
					}

					if response.Transcribe != nil {
						receivedResponse = true

						// Check for verbose_json specific fields
						if response.Transcribe.BifrostTranscribeStreamResponse != nil {
							t.Logf("Stream type: %v", response.Transcribe.BifrostTranscribeStreamResponse.Type)
							if response.Transcribe.BifrostTranscribeStreamResponse.Delta != nil {
								t.Logf("Delta: %s", *response.Transcribe.BifrostTranscribeStreamResponse.Delta)
							}
						}
					}

				case <-streamCtx.Done():
					t.Fatal("Verbose stream reading timed out")
				}
			}

		verboseStreamComplete:
			assert.True(t, receivedResponse, "Should receive at least one response")
			t.Logf("✅ Verbose JSON streaming successful")
		})

		t.Run("MultipleLanguages_Streaming", func(t *testing.T) {
			// Test streaming with different language hints
			languages := []string{"en", "es", "fr", "de"}

			for _, lang := range languages {
				t.Run("StreamLang_"+lang, func(t *testing.T) {
					langCopy := lang
					request := &schemas.BifrostRequest{
						Provider: testConfig.Provider,
						Model:    "gpt-4o-transcribe",
						Input: schemas.RequestInput{
							TranscriptionInput: &schemas.TranscriptionInput{
								File:     TestAudioDataMP3,
								Language: &langCopy,
							},
						},
						Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
						Fallbacks: testConfig.Fallbacks,
					}

					responseChannel, err := client.TranscriptionStreamRequest(ctx, request)
					require.Nilf(t, err, "Streaming failed for language %s: %v", lang, err)

					var receivedData bool
					streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()

					for {
						select {
						case response, ok := <-responseChannel:
							if !ok {
								goto langStreamComplete
							}

							if response.BifrostError != nil {
								t.Fatalf("Error in stream for language %s: %v", lang, response.BifrostError)
							}

							if response.Transcribe != nil {
								receivedData = true
							}

						case <-streamCtx.Done():
							t.Fatalf("Stream timed out for language %s", lang)
						}
					}

				langStreamComplete:
					assert.True(t, receivedData, "Should receive transcription data for language %s", lang)
					t.Logf("✅ Streaming successful for language: %s", lang)
				})
			}
		})

		t.Run("WithCustomPrompt_Streaming", func(t *testing.T) {
			// Test streaming with custom prompt for context
			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    "gpt-4o-transcribe",
				Input: schemas.RequestInput{
					TranscriptionInput: &schemas.TranscriptionInput{
						File:     TestAudioDataMP3,
						Language: bifrost.Ptr("en"),
						Prompt:   bifrost.Ptr("This audio contains technical terms, proper nouns, and streaming-related vocabulary."),
					},
				},
				Params: MergeModelParameters(&schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.1),
				}, testConfig.CustomParams),
				Fallbacks: testConfig.Fallbacks,
			}

			responseChannel, err := client.TranscriptionStreamRequest(ctx, request)
			require.Nilf(t, err, "Custom prompt streaming failed: %v", err)

			var chunkCount int
			streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			for {
				select {
				case response, ok := <-responseChannel:
					if !ok {
						goto promptStreamComplete
					}

					if response.BifrostError != nil {
						t.Fatalf("Error in prompt stream: %v", response.BifrostError)
					}

					if response.Transcribe != nil && response.Transcribe.Text != "" {
						chunkCount++
					}

				case <-streamCtx.Done():
					t.Fatal("Prompt stream reading timed out")
				}
			}

		promptStreamComplete:
			assert.Greater(t, chunkCount, 0, "Should receive at least one transcription chunk")
			t.Logf("✅ Custom prompt streaming successful: %d chunks received", chunkCount)
		})
	})
}
