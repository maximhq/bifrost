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

// RunTranscriptionTest executes the transcription test scenario
func RunTranscriptionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Transcription {
		t.Logf("Transcription not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("Transcription", func(t *testing.T) {
		// Test with different audio formats and configurations
		testCases := []struct {
			name           string
			audioData      []byte
			language       *string
			prompt         *string
			responseFormat *string
			expectText     string
		}{
			{
				name:           "BasicMP3_English",
				audioData:      TestAudioDataMP3,
				language:       bifrost.Ptr("en"),
				prompt:         nil,
				responseFormat: bifrost.Ptr("json"),
				expectText:     "", // Note: with minimal test data, might not produce meaningful text
			},
			{
				name:           "WAV_WithPrompt",
				audioData:      TestAudioDataWAV,
				language:       bifrost.Ptr("en"),
				prompt:         bifrost.Ptr("This is a test audio file containing speech."),
				responseFormat: bifrost.Ptr("verbose_json"),
				expectText:     "",
			},
			{
				name:           "MP3_PlainText",
				audioData:      TestAudioDataMP3,
				language:       nil, // Auto-detect
				prompt:         nil,
				responseFormat: bifrost.Ptr("text"),
				expectText:     "",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				request := &schemas.BifrostRequest{
					Provider: testConfig.Provider,
					Model:    "whisper-1",
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

				response, err := client.TranscriptionRequest(ctx, request)
				require.Nilf(t, err, "Transcription failed: %v", err)
				require.NotNil(t, response)
				require.NotNil(t, response.Transcribe)

				// Validate transcription response structure
				assert.Equal(t, "audio.transcription", response.Object)
				assert.Equal(t, "whisper-1", response.Model)
				assert.NotNil(t, response.Transcribe.Text)

				// For verbose_json format, check additional fields
				if tc.responseFormat != nil && *tc.responseFormat == "verbose_json" {
					assert.NotNil(t, response.Transcribe.BifrostTranscribeNonStreamResponse)
					if response.Transcribe.Task != nil {
						assert.Equal(t, "transcribe", *response.Transcribe.Task)
					}
					if response.Transcribe.Language != nil {
						assert.NotEmpty(t, *response.Transcribe.Language)
					}
				}

				t.Logf("✅ Transcription successful for %s: '%s'", tc.name, response.Transcribe.Text)
			})
		}
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
			// Test all supported response formats
			formats := []string{"json", "text", "srt", "verbose_json", "vtt"}

			for _, format := range formats {
				t.Run("Format_"+format, func(t *testing.T) {
					formatCopy := format
					request := &schemas.BifrostRequest{
						Provider: testConfig.Provider,
						Model:    "whisper-1",
						Input: schemas.RequestInput{
							TranscriptionInput: &schemas.TranscriptionInput{
								File:           TestAudioDataMP3,
								ResponseFormat: &formatCopy,
							},
						},
						Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
						Fallbacks: testConfig.Fallbacks,
					}

					response, err := client.TranscriptionRequest(ctx, request)
					require.Nilf(t, err, "Transcription failed for format %s: %v", format, err)
					require.NotNil(t, response)
					require.NotNil(t, response.Transcribe)

					// All formats should return some text
					assert.NotNil(t, response.Transcribe.Text)

					t.Logf("✅ Format %s successful: response received", format)
				})
			}
		})

		t.Run("WithCustomParameters", func(t *testing.T) {
			// Test with custom parameters and temperature
			request := &schemas.BifrostRequest{
				Provider: testConfig.Provider,
				Model:    "whisper-1",
				Input: schemas.RequestInput{
					TranscriptionInput: &schemas.TranscriptionInput{
						File:           TestAudioDataMP3,
						Language:       bifrost.Ptr("en"),
						Prompt:         bifrost.Ptr("This audio contains technical terminology and proper nouns."),
						ResponseFormat: bifrost.Ptr("verbose_json"),
					},
				},
				Params: MergeModelParameters(&schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.2),
					ExtraParams: map[string]interface{}{
						"timestamp_granularities": []string{"word", "segment"},
					},
				}, testConfig.CustomParams),
				Fallbacks: testConfig.Fallbacks,
			}

			response, err := client.TranscriptionRequest(ctx, request)
			require.Nilf(t, err, "Advanced transcription failed: %v", err)
			require.NotNil(t, response)
			require.NotNil(t, response.Transcribe)

			// Check for advanced fields in verbose_json
			if response.Transcribe.BifrostTranscribeNonStreamResponse != nil {
				// These fields might be present depending on the audio content
				t.Logf("Task: %v", response.Transcribe.Task)
				t.Logf("Language: %v", response.Transcribe.Language)
				t.Logf("Duration: %v", response.Transcribe.Duration)
				t.Logf("Words count: %d", len(response.Transcribe.Words))
				t.Logf("Segments count: %d", len(response.Transcribe.Segments))
			}

			t.Logf("✅ Advanced transcription successful with custom parameters")
		})

		t.Run("MultipleLanguages", func(t *testing.T) {
			// Test with different language hints
			languages := []string{"en", "es", "fr", "de", "it"}

			for _, lang := range languages {
				t.Run("Language_"+lang, func(t *testing.T) {
					langCopy := lang
					request := &schemas.BifrostRequest{
						Provider: testConfig.Provider,
						Model:    "whisper-1",
						Input: schemas.RequestInput{
							TranscriptionInput: &schemas.TranscriptionInput{
								File:     TestAudioDataMP3,
								Language: &langCopy,
							},
						},
						Params:    MergeModelParameters(&schemas.ModelParameters{}, testConfig.CustomParams),
						Fallbacks: testConfig.Fallbacks,
					}

					response, err := client.TranscriptionRequest(ctx, request)
					require.Nilf(t, err, "Transcription failed for language %s: %v", lang, err)
					require.NotNil(t, response)
					require.NotNil(t, response.Transcribe)

					assert.NotNil(t, response.Transcribe.Text)
					t.Logf("✅ Language %s transcription successful", lang)
				})
			}
		})
	})
}
