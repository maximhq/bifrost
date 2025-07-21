package scenarios

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				voice:           "alloy",
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
				voice:           "nova",
				format:          "mp3",
				expectMinChunks: 2,
				expectMinBytes:  3000,
			},
			{
				name:            "MediumText_Echo_WAV",
				text:            "Testing streaming with WAV format. This should produce multiple audio chunks in WAV format for streaming playback.",
				voice:           "echo",
				format:          "wav",
				expectMinChunks: 1,
				expectMinBytes:  2000,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				voice := tc.voice
				request := &schemas.BifrostRequest{
					Provider: testConfig.Provider,
					Model:    "gpt-4o-mini-tts",
					Input: schemas.RequestInput{
						SpeechInput: &schemas.SpeechInput{
							Input: tc.text,
							VoiceConfig: schemas.SpeechVoiceInput{
								Voice: &voice,
							},
							ResponseFormat: tc.format,
						},
					},
					Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
					Fallbacks: testConfig.Fallbacks,
				}

				// Test streaming response
				responseChannel, err := client.SpeechStreamRequest(ctx, request)
				require.Nilf(t, err, "Speech synthesis stream failed: %v", err)
				require.NotNil(t, responseChannel, "Response channel should not be nil")

				var totalAudioBytes []byte
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

						require.NotNil(t, response.Speech, "Speech data should be present in stream")

						// Collect audio chunks
						if response.Speech.Audio != nil {
							totalAudioBytes = append(totalAudioBytes, response.Speech.Audio...)
							chunkCount++
							t.Logf("Received audio chunk %d: %d bytes", chunkCount, len(response.Speech.Audio))
						}

						// Validate stream response structure
						assert.Equal(t, "audio.speech.chunk", response.Object)
						assert.Equal(t, "gpt-4o-mini-tts", response.Model)
						assert.Equal(t, testConfig.Provider, response.ExtraFields.Provider)

						lastResponse = response

					case <-streamCtx.Done():
						t.Fatal("Stream reading timed out")
					}
				}

			streamComplete:
				// Validate streaming results
				assert.GreaterOrEqual(t, chunkCount, tc.expectMinChunks, "Should receive minimum expected chunks")
				assert.Greater(t, len(totalAudioBytes), tc.expectMinBytes, "Total audio should meet minimum size")
				assert.NotNil(t, lastResponse, "Should have received at least one response")

				t.Logf("✅ Streaming speech synthesis successful: %d chunks, %d total bytes for voice '%s' in %s format",
					chunkCount, len(totalAudioBytes), tc.voice, tc.format)
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

			voice := "shimmer"
			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    "gpt-4o-mini-tts",
				Input: schemas.RequestInput{
					SpeechInput: &schemas.SpeechInput{
						Input: finalText,
						VoiceConfig: schemas.SpeechVoiceInput{
							Voice: &voice,
						},
						ResponseFormat: "mp3",
						Instructions:   "Speak at a natural pace with clear pronunciation.",
					},
				},
				Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
				Fallbacks: testConfig.Fallbacks,
			}

			responseChannel, err := client.SpeechStreamRequest(ctx, request)
			require.Nilf(t, err, "HD streaming speech synthesis failed: %v", err)

			var totalBytes int
			var chunkCount int
			streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second) // Longer timeout for HD model
			defer cancel()

			for {
				select {
				case response, ok := <-responseChannel:
					if !ok {
						goto hdStreamComplete
					}

					if response.BifrostError != nil {
						t.Fatalf("Error in HD stream: %v", response.BifrostError)
					}

					if response.Speech != nil && response.Speech.Audio != nil {
						totalBytes += len(response.Speech.Audio)
						chunkCount++
					}

					assert.Equal(t, "gpt-4o-mini-tts", response.Model)

				case <-streamCtx.Done():
					t.Fatal("HD stream reading timed out")
				}
			}

		hdStreamComplete:
			assert.Greater(t, chunkCount, 3, "HD model should produce multiple chunks for long text")
			assert.Greater(t, totalBytes, 10000, "HD model should produce substantial audio data")

			t.Logf("✅ HD streaming successful: %d chunks, %d total bytes", chunkCount, totalBytes)
		})

		t.Run("MultipleVoices_Streaming", func(t *testing.T) {
			// Test streaming with all available voices
			voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
			testText := "Testing streaming speech synthesis with different voice options."

			for _, voice := range voices {
				t.Run("StreamingVoice_"+voice, func(t *testing.T) {
					voiceCopy := voice
					request := &schemas.BifrostRequest{
						Provider: testConfig.Provider,
						Model:    "gpt-4o-mini-tts",
						Input: schemas.RequestInput{
							SpeechInput: &schemas.SpeechInput{
								Input: testText,
								VoiceConfig: schemas.SpeechVoiceInput{
									Voice: &voiceCopy,
								},
								ResponseFormat: "mp3",
							},
						},
						Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
						Fallbacks: testConfig.Fallbacks,
					}

					responseChannel, err := client.SpeechStreamRequest(ctx, request)
					require.Nilf(t, err, "Streaming failed for voice %s: %v", voice, err)

					var receivedData bool
					streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()

					for {
						select {
						case response, ok := <-responseChannel:
							if !ok {
								goto voiceStreamComplete
							}

							if response.BifrostError != nil {
								t.Fatalf("Error in stream for voice %s: %v", voice, response.BifrostError)
							}

							if response.Speech != nil && response.Speech.Audio != nil && len(response.Speech.Audio) > 0 {
								receivedData = true
							}

						case <-streamCtx.Done():
							t.Fatalf("Stream timed out for voice %s", voice)
						}
					}

				voiceStreamComplete:
					assert.True(t, receivedData, "Should receive audio data for voice %s", voice)
					t.Logf("✅ Streaming successful for voice: %s", voice)
				})
			}
		})
	})
}
