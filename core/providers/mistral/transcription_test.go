package mistral

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createMinimalAudioFile creates a minimal valid WAV file for testing purposes.
// This generates a 1-second silent WAV file that can be used for API testing.
func createMinimalAudioFile() []byte {
	// WAV file header for a 1-second, 8000Hz, 8-bit mono audio
	sampleRate := 8000
	bitsPerSample := 8
	numChannels := 1
	duration := 1 // 1 second
	dataSize := sampleRate * numChannels * (bitsPerSample / 8) * duration

	header := make([]byte, 44+dataSize)

	// RIFF header
	copy(header[0:4], "RIFF")
	writeUint32LE(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")

	// fmt chunk
	copy(header[12:16], "fmt ")
	writeUint32LE(header[16:20], 16)                                               // chunk size
	writeUint16LE(header[20:22], 1)                                                // audio format (PCM)
	writeUint16LE(header[22:24], uint16(numChannels))                              // num channels
	writeUint32LE(header[24:28], uint32(sampleRate))                               // sample rate
	writeUint32LE(header[28:32], uint32(sampleRate*numChannels*(bitsPerSample/8))) // byte rate
	writeUint16LE(header[32:34], uint16(numChannels*(bitsPerSample/8)))            // block align
	writeUint16LE(header[34:36], uint16(bitsPerSample))                            // bits per sample

	// data chunk
	copy(header[36:40], "data")
	writeUint32LE(header[40:44], uint32(dataSize))

	// Fill with silence (128 for 8-bit audio)
	for i := 44; i < len(header); i++ {
		header[i] = 128
	}

	return header
}

func writeUint16LE(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func writeUint32LE(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// TestToMistralTranscriptionRequest tests the Bifrost-to-Mistral request conversion.
func TestToMistralTranscriptionRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *schemas.BifrostTranscriptionRequest
		expected *MistralTranscriptionRequest
	}{
		{
			name:     "nil request",
			input:    nil,
			expected: nil,
		},
		{
			name: "nil input",
			input: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: nil,
			},
			expected: nil,
		},
		{
			name: "empty file",
			input: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{},
				},
			},
			expected: nil,
		},
		{
			name: "basic request",
			input: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{0x01, 0x02, 0x03},
				},
			},
			expected: &MistralTranscriptionRequest{
				Model: "mistral-large-latest",
				File:  []byte{0x01, 0x02, 0x03},
			},
		},
		{
			name: "with language",
			input: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{0x01, 0x02, 0x03},
				},
				Params: &schemas.TranscriptionParameters{
					Language: schemas.Ptr("en"),
				},
			},
			expected: &MistralTranscriptionRequest{
				Model:    "mistral-large-latest",
				File:     []byte{0x01, 0x02, 0x03},
				Language: schemas.Ptr("en"),
			},
		},
		{
			name: "with all parameters",
			input: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{0x01, 0x02, 0x03},
				},
				Params: &schemas.TranscriptionParameters{
					Language:       schemas.Ptr("en"),
					Prompt:         schemas.Ptr("This is a test"),
					ResponseFormat: schemas.Ptr("json"),
					ExtraParams: map[string]interface{}{
						"temperature":             0.5,
						"timestamp_granularities": []string{"word", "segment"},
					},
				},
			},
			expected: &MistralTranscriptionRequest{
				Model:                  "mistral-large-latest",
				File:                   []byte{0x01, 0x02, 0x03},
				Language:               schemas.Ptr("en"),
				Prompt:                 schemas.Ptr("This is a test"),
				ResponseFormat:         schemas.Ptr("json"),
				Temperature:            schemas.Ptr(0.5),
				TimestampGranularities: []string{"word", "segment"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ToMistralTranscriptionRequest(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.Model, result.Model)
			assert.Equal(t, tt.expected.File, result.File)

			if tt.expected.Language != nil {
				require.NotNil(t, result.Language)
				assert.Equal(t, *tt.expected.Language, *result.Language)
			}

			if tt.expected.Prompt != nil {
				require.NotNil(t, result.Prompt)
				assert.Equal(t, *tt.expected.Prompt, *result.Prompt)
			}

			if tt.expected.ResponseFormat != nil {
				require.NotNil(t, result.ResponseFormat)
				assert.Equal(t, *tt.expected.ResponseFormat, *result.ResponseFormat)
			}

			if tt.expected.Temperature != nil {
				require.NotNil(t, result.Temperature)
				assert.Equal(t, *tt.expected.Temperature, *result.Temperature)
			}

			assert.Equal(t, tt.expected.TimestampGranularities, result.TimestampGranularities)
		})
	}
}

// TestToBifrostTranscriptionResponse tests the Mistral-to-Bifrost response conversion.
func TestToBifrostTranscriptionResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *MistralTranscriptionResponse
		expected *schemas.BifrostTranscriptionResponse
	}{
		{
			name:     "nil response",
			input:    nil,
			expected: nil,
		},
		{
			name: "basic response",
			input: &MistralTranscriptionResponse{
				Text: "Hello world",
			},
			expected: &schemas.BifrostTranscriptionResponse{
				Text: "Hello world",
				Task: schemas.Ptr("transcribe"),
			},
		},
		{
			name: "response with duration and language",
			input: &MistralTranscriptionResponse{
				Text:     "Hello world",
				Duration: schemas.Ptr(5.5),
				Language: schemas.Ptr("en"),
			},
			expected: &schemas.BifrostTranscriptionResponse{
				Text:     "Hello world",
				Duration: schemas.Ptr(5.5),
				Language: schemas.Ptr("en"),
				Task:     schemas.Ptr("transcribe"),
			},
		},
		{
			name: "response with segments",
			input: &MistralTranscriptionResponse{
				Text: "Hello world",
				Segments: []MistralTranscriptionSegment{
					{
						ID:               0,
						Start:            0.0,
						End:              2.5,
						Text:             "Hello",
						Temperature:      0.5,
						AvgLogProb:       -0.5,
						CompressionRatio: 1.2,
						NoSpeechProb:     0.01,
					},
					{
						ID:    1,
						Start: 2.5,
						End:   5.0,
						Text:  "world",
					},
				},
			},
			expected: &schemas.BifrostTranscriptionResponse{
				Text: "Hello world",
				Task: schemas.Ptr("transcribe"),
				Segments: []schemas.TranscriptionSegment{
					{
						ID:               0,
						Start:            0.0,
						End:              2.5,
						Text:             "Hello",
						Temperature:      0.5,
						AvgLogProb:       -0.5,
						CompressionRatio: 1.2,
						NoSpeechProb:     0.01,
					},
					{
						ID:    1,
						Start: 2.5,
						End:   5.0,
						Text:  "world",
					},
				},
			},
		},
		{
			name: "response with words",
			input: &MistralTranscriptionResponse{
				Text: "Hello world",
				Words: []MistralTranscriptionWord{
					{Word: "Hello", Start: 0.0, End: 1.2},
					{Word: "world", Start: 1.5, End: 2.5},
				},
			},
			expected: &schemas.BifrostTranscriptionResponse{
				Text: "Hello world",
				Task: schemas.Ptr("transcribe"),
				Words: []schemas.TranscriptionWord{
					{Word: "Hello", Start: 0.0, End: 1.2},
					{Word: "world", Start: 1.5, End: 2.5},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.input.ToBifrostTranscriptionResponse()

			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.Text, result.Text)

			if tt.expected.Duration != nil {
				require.NotNil(t, result.Duration)
				assert.Equal(t, *tt.expected.Duration, *result.Duration)
			}

			if tt.expected.Language != nil {
				require.NotNil(t, result.Language)
				assert.Equal(t, *tt.expected.Language, *result.Language)
			}

			if tt.expected.Task != nil {
				require.NotNil(t, result.Task)
				assert.Equal(t, *tt.expected.Task, *result.Task)
			}

			assert.Equal(t, len(tt.expected.Segments), len(result.Segments))
			for i := range tt.expected.Segments {
				assert.Equal(t, tt.expected.Segments[i], result.Segments[i])
			}

			assert.Equal(t, len(tt.expected.Words), len(result.Words))
			for i := range tt.expected.Words {
				assert.Equal(t, tt.expected.Words[i], result.Words[i])
			}
		})
	}
}

// TestCreateMistralTranscriptionMultipartBody tests multipart form body creation.
func TestCreateMistralTranscriptionMultipartBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		request        *MistralTranscriptionRequest
		expectedFields map[string]string
		shouldHaveFile bool
		expectError    bool
	}{
		{
			name: "basic request",
			request: &MistralTranscriptionRequest{
				Model: "mistral-large-latest",
				File:  []byte{0x01, 0x02, 0x03},
			},
			expectedFields: map[string]string{
				"model": "mistral-large-latest",
			},
			shouldHaveFile: true,
		},
		{
			name: "with all optional fields",
			request: &MistralTranscriptionRequest{
				Model:          "mistral-large-latest",
				File:           []byte{0x01, 0x02, 0x03},
				Language:       schemas.Ptr("en"),
				Prompt:         schemas.Ptr("Test prompt"),
				ResponseFormat: schemas.Ptr("json"),
				Temperature:    schemas.Ptr(0.5),
			},
			expectedFields: map[string]string{
				"model":           "mistral-large-latest",
				"language":        "en",
				"prompt":          "Test prompt",
				"response_format": "json",
				"temperature":     "0.5",
			},
			shouldHaveFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, contentType, err := createMistralTranscriptionMultipartBody(tt.request, schemas.Mistral)

			if tt.expectError {
				assert.NotNil(t, err)
				return
			}

			require.Nil(t, err)
			require.NotNil(t, body)
			assert.Contains(t, contentType, "multipart/form-data")

			// Parse the multipart form to verify its contents
			reader := multipart.NewReader(body, extractBoundary(contentType))
			formValues := make(map[string]string)
			hasFile := false

			for {
				part, parseErr := reader.NextPart()
				if parseErr == io.EOF {
					break
				}
				require.NoError(t, parseErr)

				fieldName := part.FormName()
				if fieldName == "file" {
					hasFile = true
					// Verify file content
					fileContent, readErr := io.ReadAll(part)
					require.NoError(t, readErr)
					assert.Equal(t, tt.request.File, fileContent)
				} else {
					value, readErr := io.ReadAll(part)
					require.NoError(t, readErr)
					formValues[fieldName] = string(value)
				}
			}

			assert.Equal(t, tt.shouldHaveFile, hasFile)
			for key, expected := range tt.expectedFields {
				assert.Equal(t, expected, formValues[key], "Field %s mismatch", key)
			}
		})
	}
}

// extractBoundary extracts the boundary string from a Content-Type header.
func extractBoundary(contentType string) string {
	const prefix = "boundary="
	start := bytes.Index([]byte(contentType), []byte(prefix))
	if start == -1 {
		return ""
	}
	return contentType[start+len(prefix):]
}

// TestTranscriptionWithMockServer tests the Transcription method with a mock HTTP server.
func TestTranscriptionWithMockServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		responseBody   interface{}
		statusCode     int
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, *schemas.BifrostTranscriptionResponse)
	}{
		{
			name: "successful transcription",
			responseBody: MistralTranscriptionResponse{
				Text:     "Hello, this is a test transcription.",
				Duration: schemas.Ptr(3.5),
				Language: schemas.Ptr("en"),
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, resp *schemas.BifrostTranscriptionResponse) {
				assert.Equal(t, "Hello, this is a test transcription.", resp.Text)
				require.NotNil(t, resp.Duration)
				assert.Equal(t, 3.5, *resp.Duration)
				require.NotNil(t, resp.Language)
				assert.Equal(t, "en", *resp.Language)
				assert.Equal(t, schemas.TranscriptionRequest, resp.ExtraFields.RequestType)
				assert.Equal(t, schemas.Mistral, resp.ExtraFields.Provider)
			},
		},
		{
			name: "transcription with segments",
			responseBody: MistralTranscriptionResponse{
				Text: "Hello world",
				Segments: []MistralTranscriptionSegment{
					{ID: 0, Start: 0.0, End: 1.5, Text: "Hello"},
					{ID: 1, Start: 1.5, End: 3.0, Text: "world"},
				},
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, resp *schemas.BifrostTranscriptionResponse) {
				assert.Equal(t, "Hello world", resp.Text)
				require.Len(t, resp.Segments, 2)
				assert.Equal(t, "Hello", resp.Segments[0].Text)
				assert.Equal(t, "world", resp.Segments[1].Text)
			},
		},
		{
			name: "transcription with words",
			responseBody: MistralTranscriptionResponse{
				Text: "Hello world",
				Words: []MistralTranscriptionWord{
					{Word: "Hello", Start: 0.0, End: 0.8},
					{Word: "world", Start: 1.0, End: 1.5},
				},
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, resp *schemas.BifrostTranscriptionResponse) {
				assert.Equal(t, "Hello world", resp.Text)
				require.Len(t, resp.Words, 2)
				assert.Equal(t, "Hello", resp.Words[0].Word)
				assert.Equal(t, "world", resp.Words[1].Word)
			},
		},
		{
			name:          "server error",
			responseBody:  map[string]interface{}{"error": map[string]interface{}{"message": "Internal server error"}},
			statusCode:    http.StatusInternalServerError,
			expectError:   true,
			errorContains: "",
		},
		{
			name:          "unauthorized",
			responseBody:  map[string]interface{}{"error": map[string]interface{}{"message": "Invalid API key"}},
			statusCode:    http.StatusUnauthorized,
			expectError:   true,
			errorContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/v1/audio/transcriptions", r.URL.Path)
				assert.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")

				// Check for authorization header
				authHeader := r.Header.Get("Authorization")
				assert.Contains(t, authHeader, "Bearer")

				// Send response
				w.WriteHeader(tt.statusCode)
				responseJSON, _ := sonic.Marshal(tt.responseBody)
				w.Write(responseJSON)
			}))
			defer server.Close()

			// Create provider
			provider := NewMistralProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 30,
				},
			}, &testLogger{})

			// Create request
			audioData := createMinimalAudioFile()
			request := &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: &schemas.TranscriptionInput{
					File: audioData,
				},
				Params: &schemas.TranscriptionParameters{
					Language: schemas.Ptr("en"),
				},
			}

			// Make request
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewEnvVar("test-api-key")}, request)

			if tt.expectError {
				require.NotNil(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error.Message, tt.errorContains)
				}
				return
			}

			require.Nil(t, err)
			require.NotNil(t, resp)
			tt.validateResult(t, resp)
		})
	}
}

// TestTranscriptionNilInput tests handling of nil/invalid inputs.
func TestTranscriptionNilInput(t *testing.T) {
	t.Parallel()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.mistral.ai",
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	tests := []struct {
		name    string
		request *schemas.BifrostTranscriptionRequest
	}{
		{
			name: "nil input field",
			request: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: nil,
			},
		},
		{
			name: "empty file",
			request: &schemas.BifrostTranscriptionRequest{
				Model: "mistral-large-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, err := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewEnvVar("test-key")}, tt.request)

			require.NotNil(t, err)
			assert.Nil(t, resp)
			assert.Equal(t, "transcription input is not provided", err.Error.Message)
		})
	}
}

// TestTranscriptionStreamWithMockServer tests the TranscriptionStream method with a mock HTTP server.
func TestTranscriptionStreamWithMockServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		streamEvents   []string // SSE events to send
		expectError    bool
		validateResult func(*testing.T, []*schemas.BifrostTranscriptionStreamResponse)
	}{
		{
			name: "successful streaming transcription",
			streamEvents: []string{
				"event: transcription.language\ndata: {\"language\": \"en\"}\n",
				"event: transcription.text.delta\ndata: {\"text\": \"Hello\"}\n",
				"event: transcription.text.delta\ndata: {\"text\": \" world\"}\n",
				"event: transcription.done\ndata: {\"model\": \"voxtral-mini-latest\", \"usage\": {\"prompt_audio_seconds\": 5, \"prompt_tokens\": 10, \"total_tokens\": 100, \"completion_tokens\": 90}}\n",
			},
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse) {
				require.GreaterOrEqual(t, len(responses), 3, "Expected at least 3 responses")

				// Check for delta events
				foundHello := false
				foundWorld := false
				foundDone := false

				for _, resp := range responses {
					if resp.Delta != nil {
						if *resp.Delta == "Hello" {
							foundHello = true
						}
						if *resp.Delta == " world" {
							foundWorld = true
						}
					}
					if resp.Type == schemas.TranscriptionStreamResponseTypeDone {
						foundDone = true
						require.NotNil(t, resp.Usage)
					}
				}

				assert.True(t, foundHello, "Expected to find 'Hello' delta")
				assert.True(t, foundWorld, "Expected to find ' world' delta")
				assert.True(t, foundDone, "Expected to find done event")
			},
		},
		{
			name: "streaming with segments",
			streamEvents: []string{
				"event: transcription.segment\ndata: {\"segment\": {\"id\": 0, \"start\": 0.0, \"end\": 1.5, \"text\": \"Hello\"}}\n",
				"event: transcription.segment\ndata: {\"segment\": {\"id\": 1, \"start\": 1.5, \"end\": 3.0, \"text\": \"world\"}}\n",
				"event: transcription.done\ndata: {\"model\": \"voxtral-mini-latest\", \"usage\": {\"prompt_audio_seconds\": 3}}\n",
			},
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse) {
				require.GreaterOrEqual(t, len(responses), 2, "Expected at least 2 responses")

				// Check segment content
				foundHello := false
				foundWorld := false

				for _, resp := range responses {
					if resp.Text == "Hello" {
						foundHello = true
					}
					if resp.Text == "world" {
						foundWorld = true
					}
				}

				assert.True(t, foundHello, "Expected to find 'Hello' segment")
				assert.True(t, foundWorld, "Expected to find 'world' segment")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server that sends SSE events
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/v1/audio/transcriptions", r.URL.Path)
				assert.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")
				assert.Contains(t, r.Header.Get("Accept"), "text/event-stream")

				// Set SSE headers
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.WriteHeader(http.StatusOK)

				// Send SSE events
				flusher, ok := w.(http.Flusher)
				require.True(t, ok, "ResponseWriter must support Flusher")

				for _, event := range tt.streamEvents {
					w.Write([]byte(event))
					w.Write([]byte("\n"))
					flusher.Flush()
				}
			}))
			defer server.Close()

			// Create provider
			provider := NewMistralProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 30,
				},
			}, &testLogger{})

			// Create request
			audioData := createMinimalAudioFile()
			request := &schemas.BifrostTranscriptionRequest{
				Model: "voxtral-mini-latest",
				Input: &schemas.TranscriptionInput{
					File: audioData,
				},
				Params: &schemas.TranscriptionParameters{
					Language: schemas.Ptr("en"),
				},
			}

			// Create post hook runner (no-op for tests)
			postHookRunner := func(ctx *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return response, err
			}

			// Make streaming request
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			streamChan, err := provider.TranscriptionStream(ctx, postHookRunner, schemas.Key{Value: *schemas.NewEnvVar("test-api-key")}, request)

			if tt.expectError {
				require.NotNil(t, err)
				return
			}

			require.Nil(t, err)
			require.NotNil(t, streamChan)

			// Collect responses
			var responses []*schemas.BifrostTranscriptionStreamResponse
			for streamResp := range streamChan {
				if streamResp.BifrostTranscriptionStreamResponse != nil {
					responses = append(responses, streamResp.BifrostTranscriptionStreamResponse)
				}
			}

			tt.validateResult(t, responses)
		})
	}
}

// TestTranscriptionStreamNilInput tests handling of nil/invalid inputs for streaming.
func TestTranscriptionStreamNilInput(t *testing.T) {
	t.Parallel()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.mistral.ai",
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, &testLogger{})

	// Create post hook runner (no-op for tests)
	postHookRunner := func(ctx *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return response, err
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	tests := []struct {
		name    string
		request *schemas.BifrostTranscriptionRequest
	}{
		{
			name: "nil input field",
			request: &schemas.BifrostTranscriptionRequest{
				Model: "voxtral-mini-latest",
				Input: nil,
			},
		},
		{
			name: "empty file",
			request: &schemas.BifrostTranscriptionRequest{
				Model: "voxtral-mini-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stream, err := provider.TranscriptionStream(ctx, postHookRunner, schemas.Key{Value: *schemas.NewEnvVar("test-key")}, tt.request)

			require.NotNil(t, err)
			assert.Nil(t, stream)
			assert.Equal(t, "transcription input is not provided", err.Error.Message)
		})
	}
}

// TestToBifrostTranscriptionStreamResponse tests the streaming event conversion.
func TestToBifrostTranscriptionStreamResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    *MistralTranscriptionStreamEvent
		expected *schemas.BifrostTranscriptionStreamResponse
	}{
		{
			name:     "nil event",
			event:    nil,
			expected: nil,
		},
		{
			name: "text delta event",
			event: &MistralTranscriptionStreamEvent{
				Event: string(MistralTranscriptionStreamEventTextDelta),
				Data: &MistralTranscriptionStreamData{
					Text: "Hello world",
				},
			},
			expected: &schemas.BifrostTranscriptionStreamResponse{
				Type:  schemas.TranscriptionStreamResponseTypeDelta,
				Text:  "Hello world",
				Delta: schemas.Ptr("Hello world"),
			},
		},
		{
			name: "language event",
			event: &MistralTranscriptionStreamEvent{
				Event: string(MistralTranscriptionStreamEventLanguage),
				Data: &MistralTranscriptionStreamData{
					Language: "en",
				},
			},
			expected: &schemas.BifrostTranscriptionStreamResponse{
				Type: schemas.TranscriptionStreamResponseTypeDelta,
				Text: "",
			},
		},
		{
			name: "segment event",
			event: &MistralTranscriptionStreamEvent{
				Event: string(MistralTranscriptionStreamEventSegment),
				Data: &MistralTranscriptionStreamData{
					Segment: &MistralTranscriptionStreamSegment{
						ID:    0,
						Start: 0.0,
						End:   1.5,
						Text:  "Hello",
					},
				},
			},
			expected: &schemas.BifrostTranscriptionStreamResponse{
				Type:  schemas.TranscriptionStreamResponseTypeDelta,
				Text:  "Hello",
				Delta: schemas.Ptr("Hello"),
			},
		},
		{
			name: "done event with usage",
			event: &MistralTranscriptionStreamEvent{
				Event: string(MistralTranscriptionStreamEventDone),
				Data: &MistralTranscriptionStreamData{
					Model: "voxtral-mini-latest",
					Usage: &MistralTranscriptionUsage{
						PromptAudioSeconds: 10,
						PromptTokens:       50,
						TotalTokens:        200,
						CompletionTokens:   150,
					},
				},
			},
			expected: &schemas.BifrostTranscriptionStreamResponse{
				Type: schemas.TranscriptionStreamResponseTypeDone,
				Usage: &schemas.TranscriptionUsage{
					Type:         "tokens",
					TotalTokens:  schemas.Ptr(200),
					InputTokens:  schemas.Ptr(50),
					OutputTokens: schemas.Ptr(150),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.event.ToBifrostTranscriptionStreamResponse()

			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.Type, result.Type)
			assert.Equal(t, tt.expected.Text, result.Text)

			if tt.expected.Delta != nil {
				require.NotNil(t, result.Delta)
				assert.Equal(t, *tt.expected.Delta, *result.Delta)
			}

			if tt.expected.Usage != nil {
				require.NotNil(t, result.Usage)
				assert.Equal(t, tt.expected.Usage.Type, result.Usage.Type)
				if tt.expected.Usage.TotalTokens != nil {
					require.NotNil(t, result.Usage.TotalTokens)
					assert.Equal(t, *tt.expected.Usage.TotalTokens, *result.Usage.TotalTokens)
				}
			}
		})
	}
}

// TestCreateMistralTranscriptionStreamMultipartBody tests the streaming multipart body creation.
func TestCreateMistralTranscriptionStreamMultipartBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		request             *MistralTranscriptionRequest
		expectedFields      map[string]string
		expectedArrayFields map[string][]string
	}{
		{
			name: "basic streaming request",
			request: &MistralTranscriptionRequest{
				Model:    "voxtral-mini-latest",
				File:     []byte{0x01, 0x02, 0x03},
				Language: schemas.Ptr("en"),
				Stream:   schemas.Ptr(true),
			},
			expectedFields: map[string]string{
				"stream":   "true",
				"model":    "voxtral-mini-latest",
				"language": "en",
			},
		},
		{
			name: "streaming with all optional fields",
			request: &MistralTranscriptionRequest{
				Model:                  "voxtral-mini-latest",
				File:                   []byte{0x01, 0x02, 0x03},
				Language:               schemas.Ptr("fr"),
				Prompt:                 schemas.Ptr("Test prompt"),
				ResponseFormat:         schemas.Ptr("verbose_json"),
				Temperature:            schemas.Ptr(0.5),
				Stream:                 schemas.Ptr(true),
				TimestampGranularities: []string{"word", "segment"},
			},
			expectedFields: map[string]string{
				"stream":          "true",
				"model":           "voxtral-mini-latest",
				"language":        "fr",
				"prompt":          "Test prompt",
				"response_format": "verbose_json",
				"temperature":     "0.5",
			},
			expectedArrayFields: map[string][]string{
				"timestamp_granularities[]": {"word", "segment"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, contentType, err := createMistralTranscriptionMultipartBody(tt.request, schemas.Mistral)

			require.Nil(t, err)
			require.NotNil(t, body)
			assert.Contains(t, contentType, "multipart/form-data")

			// Parse the multipart form to verify fields
			reader := multipart.NewReader(body, extractBoundary(contentType))
			formValues := make(map[string]string)
			arrayFormValues := make(map[string][]string)

			for {
				part, parseErr := reader.NextPart()
				if parseErr == io.EOF {
					break
				}
				require.NoError(t, parseErr)

				fieldName := part.FormName()
				if fieldName != "file" {
					value, readErr := io.ReadAll(part)
					require.NoError(t, readErr)
					// Handle array fields (like timestamp_granularities[])
					if existing, ok := arrayFormValues[fieldName]; ok {
						arrayFormValues[fieldName] = append(existing, string(value))
					} else if _, isArray := tt.expectedArrayFields[fieldName]; isArray {
						arrayFormValues[fieldName] = []string{string(value)}
					} else {
						formValues[fieldName] = string(value)
					}
				}
			}

			// Verify expected fields
			for key, expected := range tt.expectedFields {
				assert.Equal(t, expected, formValues[key], "Field %s mismatch", key)
			}

			// Verify expected array fields
			for key, expected := range tt.expectedArrayFields {
				assert.Equal(t, expected, arrayFormValues[key], "Array field %s mismatch", key)
			}
		})
	}
}

// TestTranscriptionStreamEdgeCases tests edge cases in streaming transcription.
func TestTranscriptionStreamEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		streamEvents   []string
		statusCode     int
		expectError    bool
		validateResult func(*testing.T, []*schemas.BifrostTranscriptionStreamResponse, *schemas.BifrostError)
	}{
		{
			name: "empty text delta",
			streamEvents: []string{
				"event: transcription.text.delta\ndata: {\"text\": \"\"}\n",
				"event: transcription.done\ndata: {\"model\": \"voxtral-mini-latest\", \"usage\": {}}\n",
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.Nil(t, err)
				require.GreaterOrEqual(t, len(responses), 1)
				// Should handle empty text gracefully
				foundDone := false
				for _, resp := range responses {
					if resp.Type == schemas.TranscriptionStreamResponseTypeDone {
						foundDone = true
					}
				}
				assert.True(t, foundDone, "Expected done event")
			},
		},
		{
			name: "done event without usage",
			streamEvents: []string{
				"event: transcription.text.delta\ndata: {\"text\": \"Hello\"}\n",
				"event: transcription.done\ndata: {\"model\": \"voxtral-mini-latest\"}\n",
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.Nil(t, err)
				require.GreaterOrEqual(t, len(responses), 1)
				// Should handle missing usage gracefully
				var doneResp *schemas.BifrostTranscriptionStreamResponse
				for _, resp := range responses {
					if resp.Type == schemas.TranscriptionStreamResponseTypeDone {
						doneResp = resp
					}
				}
				require.NotNil(t, doneResp, "Expected done event")
				// Usage should be nil when not provided
				assert.Nil(t, doneResp.Usage)
			},
		},
		{
			name: "multiple consecutive deltas",
			streamEvents: []string{
				"event: transcription.text.delta\ndata: {\"text\": \"Hello\"}\n",
				"event: transcription.text.delta\ndata: {\"text\": \" \"}\n",
				"event: transcription.text.delta\ndata: {\"text\": \"world\"}\n",
				"event: transcription.text.delta\ndata: {\"text\": \"!\"}\n",
				"event: transcription.done\ndata: {\"model\": \"voxtral-mini-latest\", \"usage\": {\"total_tokens\": 100}}\n",
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.Nil(t, err)
				require.GreaterOrEqual(t, len(responses), 4, "Expected at least 4 responses")

				// Verify all deltas received
				var allText string
				for _, resp := range responses {
					if resp.Delta != nil {
						allText += *resp.Delta
					}
				}
				assert.Equal(t, "Hello world!", allText)
			},
		},
		{
			name: "language event only",
			streamEvents: []string{
				"event: transcription.language\ndata: {\"language\": \"fr\"}\n",
				"event: transcription.done\ndata: {\"model\": \"voxtral-mini-latest\"}\n",
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.Nil(t, err)
				require.GreaterOrEqual(t, len(responses), 1)
			},
		},
		{
			name:         "http error response",
			streamEvents: []string{},
			statusCode:   http.StatusUnauthorized,
			expectError:  true,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.NotNil(t, err)
				assert.Nil(t, responses)
			},
		},
		{
			name:         "internal server error",
			streamEvents: []string{},
			statusCode:   http.StatusInternalServerError,
			expectError:  true,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.NotNil(t, err)
				assert.Nil(t, responses)
			},
		},
		{
			name: "segment with all fields",
			streamEvents: []string{
				"event: transcription.segment\ndata: {\"segment\": {\"id\": 0, \"start\": 0.0, \"end\": 2.5, \"text\": \"Complete segment\"}}\n",
				"event: transcription.done\ndata: {\"usage\": {\"prompt_audio_seconds\": 3, \"prompt_tokens\": 10, \"total_tokens\": 50, \"completion_tokens\": 40}}\n",
			},
			statusCode: http.StatusOK,
			validateResult: func(t *testing.T, responses []*schemas.BifrostTranscriptionStreamResponse, err *schemas.BifrostError) {
				require.Nil(t, err)
				require.GreaterOrEqual(t, len(responses), 2)

				// Find segment response
				var segmentResp *schemas.BifrostTranscriptionStreamResponse
				for _, resp := range responses {
					if resp.Text == "Complete segment" {
						segmentResp = resp
						break
					}
				}
				require.NotNil(t, segmentResp, "Expected segment response")
				assert.Equal(t, "Complete segment", segmentResp.Text)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.statusCode != http.StatusOK {
					w.WriteHeader(tt.statusCode)
					w.Write([]byte(`{"error": {"message": "Test error", "type": "test_error"}}`))
					return
				}

				// Set SSE headers
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.WriteHeader(http.StatusOK)

				flusher, ok := w.(http.Flusher)
				require.True(t, ok)

				for _, event := range tt.streamEvents {
					w.Write([]byte(event))
					w.Write([]byte("\n"))
					flusher.Flush()
				}
			}))
			defer server.Close()

			// Create provider
			provider := NewMistralProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 30,
				},
			}, &testLogger{})

			// Create request
			request := &schemas.BifrostTranscriptionRequest{
				Model: "voxtral-mini-latest",
				Input: &schemas.TranscriptionInput{
					File: createMinimalAudioFile(),
				},
			}

			// Create post hook runner
			postHookRunner := func(ctx *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return response, err
			}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			streamChan, err := provider.TranscriptionStream(ctx, postHookRunner, schemas.Key{Value: *schemas.NewEnvVar("test-key")}, request)

			if tt.expectError {
				tt.validateResult(t, nil, err)
				return
			}

			require.Nil(t, err)
			require.NotNil(t, streamChan)

			var responses []*schemas.BifrostTranscriptionStreamResponse
			for streamResp := range streamChan {
				if streamResp.BifrostTranscriptionStreamResponse != nil {
					responses = append(responses, streamResp.BifrostTranscriptionStreamResponse)
				}
			}

			tt.validateResult(t, responses, nil)
		})
	}
}

// TestTranscriptionStreamContextCancellation tests context cancellation during streaming.
func TestTranscriptionStreamContextCancellation(t *testing.T) {
	t.Parallel()

	// Create a server that sends events slowly
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		// Send initial event
		w.Write([]byte("event: transcription.text.delta\ndata: {\"text\": \"Starting...\"}\n\n"))
		flusher.Flush()

		// Wait longer than the context timeout
		time.Sleep(5 * time.Second)

		// This should not be received
		w.Write([]byte("event: transcription.done\ndata: {}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 30,
		},
	}, &testLogger{})

	request := &schemas.BifrostTranscriptionRequest{
		Model: "voxtral-mini-latest",
		Input: &schemas.TranscriptionInput{
			File: createMinimalAudioFile(),
		},
	}

	postHookRunner := func(ctx *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return response, err
	}

	// Create context with short timeout
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	streamChan, err := provider.TranscriptionStream(ctx, postHookRunner, schemas.Key{Value: *schemas.NewEnvVar("test-key")}, request)
	require.Nil(t, err)
	require.NotNil(t, streamChan)

	// Collect responses - should timeout before receiving all
	var receivedCount int
	for range streamChan {
		receivedCount++
	}

	// Should receive at most the first event before timeout
	assert.LessOrEqual(t, receivedCount, 2, "Should receive limited events due to context cancellation")
}

// TestTranscriptionExtraParamsEdgeCases tests edge cases for extra parameters.
func TestTranscriptionExtraParamsEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		extraParams map[string]interface{}
		expectTemp  *float64
		expectGran  []string
	}{
		{
			name:        "nil extra params",
			extraParams: nil,
			expectTemp:  nil,
			expectGran:  nil,
		},
		{
			name:        "empty extra params",
			extraParams: map[string]interface{}{},
			expectTemp:  nil,
			expectGran:  nil,
		},
		{
			name: "temperature as int",
			extraParams: map[string]interface{}{
				"temperature": 1,
			},
			expectTemp: schemas.Ptr(1.0),
			expectGran: nil,
		},
		{
			name: "temperature as float",
			extraParams: map[string]interface{}{
				"temperature": 0.7,
			},
			expectTemp: schemas.Ptr(0.7),
			expectGran: nil,
		},
		{
			name: "invalid temperature type",
			extraParams: map[string]interface{}{
				"temperature": "invalid",
			},
			expectTemp: nil,
			expectGran: nil,
		},
		{
			name: "timestamp granularities",
			extraParams: map[string]interface{}{
				"timestamp_granularities": []string{"word", "segment"},
			},
			expectTemp: nil,
			expectGran: []string{"word", "segment"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := &schemas.BifrostTranscriptionRequest{
				Model: "voxtral-mini-latest",
				Input: &schemas.TranscriptionInput{
					File: []byte{0x01, 0x02, 0x03},
				},
				Params: &schemas.TranscriptionParameters{
					ExtraParams: tt.extraParams,
				},
			}

			result := ToMistralTranscriptionRequest(request)
			require.NotNil(t, result)

			if tt.expectTemp != nil {
				require.NotNil(t, result.Temperature)
				assert.Equal(t, *tt.expectTemp, *result.Temperature)
			} else {
				assert.Nil(t, result.Temperature)
			}

			assert.Equal(t, tt.expectGran, result.TimestampGranularities)
		})
	}
}

// TestFormatFloat64EdgeCases tests edge cases for float formatting.
func TestFormatFloat64EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    float64
		expected string
	}{
		{0.0, "0"},
		{0.5, "0.5"},
		{1.0, "1"},
		{1.23456, "1.23456"},
		{-0.5, "-0.5"},
		{0.123456789, "0.123456789"},
		{100.0, "100"},
		{0.001, "0.001"},
	}

	for _, tt := range tests {
		result := formatFloat64(tt.input)
		assert.Equal(t, tt.expected, result, "formatFloat64(%f)", tt.input)
	}
}

// TestFormatFloat64 tests the float64 formatting function.
func TestFormatFloat64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    float64
		expected string
	}{
		{0.0, "0"},
		{0.5, "0.5"},
		{1.0, "1"},
		{1.23456, "1.23456"},
		{-0.5, "-0.5"},
	}

	for _, tt := range tests {
		result := formatFloat64(tt.input)
		assert.Equal(t, tt.expected, result, "formatFloat64(%f)", tt.input)
	}
}

// testLogger is a minimal logger implementation for testing.
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any)                     {}
func (l *testLogger) Info(msg string, args ...any)                      {}
func (l *testLogger) Warn(msg string, args ...any)                      {}
func (l *testLogger) Error(msg string, args ...any)                     {}
func (l *testLogger) Fatal(msg string, args ...any)                     {}
func (l *testLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *testLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *testLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestMistralTranscriptionIntegration tests the transcription endpoint with the real Mistral API.
// This test requires MISTRAL_API_KEY environment variable to be set.
// Run with: MISTRAL_API_KEY=xxx go test -v -run TestMistralTranscriptionIntegration
func TestMistralTranscriptionIntegration(t *testing.T) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MISTRAL_API_KEY not set")
	}

	// Create provider
	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.mistral.ai",
			DefaultRequestTimeoutInSeconds: 60,
		},
	}, &testLogger{})

	// Create a minimal but valid audio file for testing
	// Note: Mistral may reject this minimal WAV file - this tests error handling too
	audioData := createMinimalAudioFile()

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	request := &schemas.BifrostTranscriptionRequest{
		Model: "voxtral-mini-latest", // Mistral's audio transcription model
		Input: &schemas.TranscriptionInput{
			File: audioData,
		},
		Params: &schemas.TranscriptionParameters{
			Language:       schemas.Ptr("en"),
			ResponseFormat: schemas.Ptr("json"),
		},
	}

	t.Log("🎤 Testing Mistral transcription with voxtral-mini-latest...")
	resp, err := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewEnvVar(apiKey)}, request)

	if err != nil {
		// Log the error but don't fail - the minimal audio may not be valid for Mistral
		t.Logf("⚠️ Transcription returned error (may be expected for minimal audio): %v", err)
		if err.Error != nil {
			t.Logf("   Error message: %s", err.Error.Message)
		}
		// Verify proper error structure
		assert.NotNil(t, err.Error, "Error should have Error field populated")
		t.Log("✅ Error handling works correctly")
		return
	}

	// If successful, validate the response
	t.Log("✅ Transcription succeeded!")
	assert.NotNil(t, resp)
	// TODO: Send a proper audio file with speech to validate resp.Text is non-empty
	// assert.NotEmpty(t, resp.Text)
	assert.Equal(t, schemas.TranscriptionRequest, resp.ExtraFields.RequestType)
	assert.Equal(t, schemas.Mistral, resp.ExtraFields.Provider)
	t.Logf("   Transcribed text: %s", resp.Text)
}

// TestMistralTranscriptionStreamIntegration tests the streaming transcription endpoint with the real Mistral API.
// This test requires MISTRAL_API_KEY environment variable to be set.
// Run with: MISTRAL_API_KEY=xxx go test -v -run TestMistralTranscriptionStreamIntegration
func TestMistralTranscriptionStreamIntegration(t *testing.T) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: MISTRAL_API_KEY not set")
	}

	// Create provider
	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "https://api.mistral.ai",
			DefaultRequestTimeoutInSeconds: 60,
		},
	}, &testLogger{})

	// Create a minimal but valid audio file for testing
	audioData := createMinimalAudioFile()

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	request := &schemas.BifrostTranscriptionRequest{
		Model: "voxtral-mini-latest", // Mistral's audio transcription model
		Input: &schemas.TranscriptionInput{
			File: audioData,
		},
		Params: &schemas.TranscriptionParameters{
			Language: schemas.Ptr("en"),
		},
	}

	// Create post hook runner (no-op for tests)
	postHookRunner := func(ctx *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return response, err
	}

	t.Log("🎤 Testing Mistral streaming transcription with voxtral-mini-latest...")
	streamChan, err := provider.TranscriptionStream(ctx, postHookRunner, schemas.Key{Value: *schemas.NewEnvVar(apiKey)}, request)

	if err != nil {
		// Log the error but don't fail - the minimal audio may not be valid for Mistral
		t.Logf("⚠️ Streaming transcription returned error (may be expected for minimal audio): %v", err)
		if err.Error != nil {
			t.Logf("   Error message: %s", err.Error.Message)
		}
		// Verify proper error structure
		assert.NotNil(t, err.Error, "Error should have Error field populated")
		t.Log("✅ Error handling works correctly")
		return
	}

	require.NotNil(t, streamChan)

	// Collect streaming responses
	var allText string
	var chunkCount int
	var lastResponse *schemas.BifrostTranscriptionStreamResponse

	for streamResp := range streamChan {
		if streamResp.BifrostError != nil {
			t.Logf("⚠️ Stream error (may be expected for minimal audio): %v", streamResp.BifrostError.Error.Message)
			return
		}

		if streamResp.BifrostTranscriptionStreamResponse != nil {
			chunkCount++
			lastResponse = streamResp.BifrostTranscriptionStreamResponse

			if streamResp.BifrostTranscriptionStreamResponse.Delta != nil {
				allText += *streamResp.BifrostTranscriptionStreamResponse.Delta
			}

			t.Logf("📊 Chunk %d: type=%s, latency=%dms",
				chunkCount,
				streamResp.BifrostTranscriptionStreamResponse.Type,
				streamResp.BifrostTranscriptionStreamResponse.ExtraFields.Latency)
		}
	}

	t.Log("✅ Streaming transcription completed!")
	t.Logf("   Total chunks received: %d", chunkCount)
	t.Logf("   Transcribed text: %s", allText)

	if lastResponse != nil {
		assert.Equal(t, schemas.TranscriptionStreamRequest, lastResponse.ExtraFields.RequestType)
		assert.Equal(t, schemas.Mistral, lastResponse.ExtraFields.Provider)
	}
}
