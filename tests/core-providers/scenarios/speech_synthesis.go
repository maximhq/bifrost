package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunSpeechSynthesisTest executes the speech synthesis test scenario
func RunSpeechSynthesisTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.SpeechSynthesis {
		t.Logf("Speech synthesis not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("SpeechSynthesis", func(t *testing.T) {
		// Test with different text lengths and voices
		testCases := []struct {
			name           string
			text           string
			voice          string
			format         string
			expectMinBytes int
		}{
			{
				name:           "ShortText_Alloy",
				text:           "Hello, this is a test of speech synthesis.",
				voice:          "alloy",
				format:         "mp3",
				expectMinBytes: 1000, // Expect at least 1KB of audio data
			},
			{
				name:           "MediumText_Nova",
				text:           "This is a longer text to test speech synthesis with more content. The AI should convert this entire sentence into natural-sounding speech audio.",
				voice:          "nova",
				format:         "mp3",
				expectMinBytes: 2000,
			},
			{
				name:           "ShortText_Echo_WAV",
				text:           "Testing WAV format output.",
				voice:          "echo",
				format:         "wav",
				expectMinBytes: 500,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				voice := tc.voice
				request := &schemas.BifrostRequest{
					Provider: testConfig.Provider,
					Model:    "tts-1",
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

				response, err := client.SpeechRequest(ctx, request)
				require.Nilf(t, err, "Speech synthesis failed: %v", err)
				require.NotNil(t, response)
				require.NotNil(t, response.Speech)
				require.NotNil(t, response.Speech.Audio)

				// Validate audio data
				assert.Greater(t, len(response.Speech.Audio), tc.expectMinBytes, "Audio data should have minimum expected size")
				assert.Equal(t, "audio.speech", response.Object)
				assert.Equal(t, "tts-1", response.Model)

				t.Logf("✅ Speech synthesis successful: %d bytes of %s audio generated for voice '%s'",
					len(response.Speech.Audio), tc.format, tc.voice)
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
			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    "tts-1-hd", // Test with HD model
				Input: schemas.RequestInput{
					SpeechInput: &schemas.SpeechInput{
						Input: longText,
						VoiceConfig: schemas.SpeechVoiceInput{
							Voice: &voice,
						},
						ResponseFormat: "mp3",
						Instructions:   "Speak slowly and clearly with natural intonation.",
					},
				},
				Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
				Fallbacks: testConfig.Fallbacks,
			}

			response, err := client.SpeechRequest(ctx, request)
			require.Nilf(t, err, "HD speech synthesis failed: %v", err)
			require.NotNil(t, response)
			require.NotNil(t, response.Speech)
			require.NotNil(t, response.Speech.Audio)

			// Validate longer audio
			assert.Greater(t, len(response.Speech.Audio), 5000, "HD audio should be substantial")
			assert.Equal(t, "tts-1-hd", response.Model)

			t.Logf("✅ HD speech synthesis successful: %d bytes generated", len(response.Speech.Audio))
		})

		t.Run("AllVoiceOptions", func(t *testing.T) {
			// Test all available voices
			voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
			testText := "Testing voice variation with this consistent text."

			for _, voice := range voices {
				t.Run("Voice_"+voice, func(t *testing.T) {
					voiceCopy := voice
					request := &schemas.BifrostRequest{
						Provider: testConfig.Provider,
						Model:    "tts-1",
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

					response, err := client.SpeechRequest(ctx, request)
					require.Nilf(t, err, "Speech synthesis failed for voice %s: %v", voice, err)
					require.NotNil(t, response)
					require.NotNil(t, response.Speech)
					require.NotNil(t, response.Speech.Audio)

					assert.Greater(t, len(response.Speech.Audio), 500, "Audio should be generated for voice %s", voice)
					t.Logf("✅ Voice %s: %d bytes generated", voice, len(response.Speech.Audio))
				})
			}
		})
	})
}
