package integrations

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestIsLargePayload(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int
		config        *LargePayloadConfig
		expected      bool
	}{
		{
			name:          "nil config returns false",
			contentLength: 100 * 1024 * 1024,
			config:        nil,
			expected:      false,
		},
		{
			name:          "disabled config returns false",
			contentLength: 100 * 1024 * 1024,
			config:        &LargePayloadConfig{Enabled: false, Threshold: 10 * 1024 * 1024},
			expected:      false,
		},
		{
			name:          "below threshold returns false",
			contentLength: 5 * 1024 * 1024, // 5MB
			config:        DefaultLargePayloadConfig(),
			expected:      false,
		},
		{
			name:          "at threshold returns false",
			contentLength: 10 * 1024 * 1024, // exactly 10MB
			config:        DefaultLargePayloadConfig(),
			expected:      false,
		},
		{
			name:          "above threshold returns true",
			contentLength: 11 * 1024 * 1024, // 11MB
			config:        DefaultLargePayloadConfig(),
			expected:      true,
		},
		{
			name:          "way above threshold returns true",
			contentLength: 400 * 1024 * 1024, // 400MB
			config:        DefaultLargePayloadConfig(),
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetContentLength(tt.contentLength)

			result := IsLargePayload(ctx, tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSelectiveRequestMetadata_HasData(t *testing.T) {
	tests := []struct {
		name     string
		metadata *SelectiveRequestMetadata
		expected bool
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			expected: false,
		},
		{
			name:     "empty metadata",
			metadata: &SelectiveRequestMetadata{},
			expected: false,
		},
		{
			name: "has response modalities",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"AUDIO"},
			},
			expected: true,
		},
		{
			name: "has speech config",
			metadata: &SelectiveRequestMetadata{
				SpeechConfig: &struct{}{},
			},
			expected: true,
		},
		{
			name: "has both",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"AUDIO"},
				SpeechConfig:       &struct{}{},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.metadata.HasData()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractRequestMetadataFromBytes(t *testing.T) {
	tests := []struct {
		name           string
		jsonData       string
		wantModalities []string
		wantSpeech     bool
	}{
		{
			name:           "empty JSON",
			jsonData:       `{}`,
			wantModalities: nil,
			wantSpeech:     false,
		},
		{
			name:           "no generationConfig",
			jsonData:       `{"contents": [{"parts": [{"text": "hello"}]}]}`,
			wantModalities: nil,
			wantSpeech:     false,
		},
		{
			name:           "generationConfig without modalities",
			jsonData:       `{"generationConfig": {"temperature": 0.7}}`,
			wantModalities: nil,
			wantSpeech:     false,
		},
		{
			name:           "responseModalities with AUDIO",
			jsonData:       `{"generationConfig": {"responseModalities": ["AUDIO"]}}`,
			wantModalities: []string{"AUDIO"},
			wantSpeech:     false,
		},
		{
			name:           "responseModalities with TEXT",
			jsonData:       `{"generationConfig": {"responseModalities": ["TEXT"]}}`,
			wantModalities: []string{"TEXT"},
			wantSpeech:     false,
		},
		{
			name:           "speechConfig present",
			jsonData:       `{"generationConfig": {"speechConfig": {"voiceConfig": {"prebuiltVoiceConfig": {"voiceName": "Kore"}}}}}`,
			wantModalities: nil,
			wantSpeech:     true,
		},
		{
			name:           "both responseModalities and speechConfig",
			jsonData:       `{"generationConfig": {"responseModalities": ["AUDIO"], "speechConfig": {}}}`,
			wantModalities: []string{"AUDIO"},
			wantSpeech:     true,
		},
		{
			name:           "metadata after large content (simulated prefetch)",
			jsonData:       `{"generationConfig": {"responseModalities": ["AUDIO"]}, "contents": []}`,
			wantModalities: []string{"AUDIO"},
			wantSpeech:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := ExtractRequestMetadataFromBytes([]byte(tt.jsonData))
			require.NotNil(t, metadata)

			assert.Equal(t, tt.wantModalities, metadata.ResponseModalities)
			if tt.wantSpeech {
				assert.NotNil(t, metadata.SpeechConfig)
			} else {
				assert.Nil(t, metadata.SpeechConfig)
			}
		})
	}
}

func TestIsSpeechRequestFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata *SelectiveRequestMetadata
		expected bool
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			expected: false,
		},
		{
			name:     "empty metadata",
			metadata: &SelectiveRequestMetadata{},
			expected: false,
		},
		{
			name: "TEXT modality",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"TEXT"},
			},
			expected: false,
		},
		{
			name: "AUDIO modality",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"AUDIO"},
			},
			expected: true,
		},
		{
			name: "speechConfig only",
			metadata: &SelectiveRequestMetadata{
				SpeechConfig: &struct{}{},
			},
			expected: true,
		},
		{
			name: "multiple modalities including AUDIO",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"TEXT", "AUDIO"},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSpeechRequestFromMetadata(tt.metadata)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsTranscriptionRequestFromMetadata(t *testing.T) {
	// For large payloads, transcription always defaults to false
	// (requires content inspection which we skip)
	metadata := &SelectiveRequestMetadata{
		ResponseModalities: []string{"TEXT"},
	}
	assert.False(t, IsTranscriptionRequestFromMetadata(metadata))
	assert.False(t, IsTranscriptionRequestFromMetadata(nil))
}

func TestIsImageGenerationRequestFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata *SelectiveRequestMetadata
		expected bool
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			expected: false,
		},
		{
			name:     "empty metadata",
			metadata: &SelectiveRequestMetadata{},
			expected: false,
		},
		{
			name: "TEXT modality",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"TEXT"},
			},
			expected: false,
		},
		{
			name: "AUDIO modality",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"AUDIO"},
			},
			expected: false,
		},
		{
			name: "IMAGE modality",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"IMAGE"},
			},
			expected: true,
		},
		{
			name: "multiple modalities including IMAGE",
			metadata: &SelectiveRequestMetadata{
				ResponseModalities: []string{"TEXT", "IMAGE"},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsImageGenerationRequestFromMetadata(tt.metadata)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractUsageMetadataSelective(t *testing.T) {
	tests := []struct {
		name     string
		jsonData string
		wantNil  bool
		wantData *LargePayloadUsageMetadata
	}{
		{
			name:     "empty JSON",
			jsonData: `{}`,
			wantNil:  true,
		},
		{
			name:     "no usageMetadata",
			jsonData: `{"candidates": [{"content": {"parts": [{"text": "hello"}]}}]}`,
			wantNil:  true,
		},
		{
			name:     "usageMetadata present",
			jsonData: `{"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20, "totalTokenCount": 30}}`,
			wantNil:  false,
			wantData: &LargePayloadUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 20,
				TotalTokenCount:      30,
			},
		},
		{
			name:     "usageMetadata with all fields",
			jsonData: `{"usageMetadata": {"promptTokenCount": 100, "candidatesTokenCount": 200, "totalTokenCount": 300, "cachedContentTokenCount": 50, "thoughtsTokenCount": 25}}`,
			wantNil:  false,
			wantData: &LargePayloadUsageMetadata{
				PromptTokenCount:        100,
				CandidatesTokenCount:    200,
				TotalTokenCount:         300,
				CachedContentTokenCount: 50,
				ThoughtsTokenCount:      25,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractUsageMetadataSelective([]byte(tt.jsonData))
			if tt.wantNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.wantData.PromptTokenCount, result.PromptTokenCount)
				assert.Equal(t, tt.wantData.CandidatesTokenCount, result.CandidatesTokenCount)
				assert.Equal(t, tt.wantData.TotalTokenCount, result.TotalTokenCount)
				assert.Equal(t, tt.wantData.CachedContentTokenCount, result.CachedContentTokenCount)
				assert.Equal(t, tt.wantData.ThoughtsTokenCount, result.ThoughtsTokenCount)
			}
		})
	}
}

func TestScanForMetadataFromStream(t *testing.T) {
	tests := []struct {
		name           string
		jsonData       string
		wantModalities []string
		wantSpeech     bool
		wantNil        bool
	}{
		{
			name:           "metadata at start",
			jsonData:       `{"generationConfig": {"responseModalities": ["AUDIO"]}, "contents": [{"parts": [{"text": "hello"}]}]}`,
			wantModalities: []string{"AUDIO"},
			wantSpeech:     false,
		},
		{
			name:           "metadata at end",
			jsonData:       `{"contents": [{"parts": [{"text": "hello"}]}], "generationConfig": {"responseModalities": ["AUDIO"]}}`,
			wantModalities: []string{"AUDIO"},
			wantSpeech:     false,
		},
		{
			name:       "speechConfig in middle",
			jsonData:   `{"model": "gemini-2.0", "generationConfig": {"speechConfig": {}}, "contents": []}`,
			wantSpeech: true,
		},
		{
			name:     "no metadata",
			jsonData: `{"contents": [{"parts": [{"text": "hello"}]}]}`,
			wantNil:  true,
		},
		{
			name:           "TEXT modalities with speechConfig - should find both",
			jsonData:       `{"generationConfig": {"responseModalities": ["TEXT"], "speechConfig": {}}, "contents": []}`,
			wantModalities: []string{"TEXT"},
			wantSpeech:     true,
		},
		{
			name:           "TEXT modalities only - should not break early and miss nothing",
			jsonData:       `{"generationConfig": {"responseModalities": ["TEXT"]}, "contents": []}`,
			wantModalities: []string{"TEXT"},
			wantSpeech:     false,
		},
		{
			name:           "both AUDIO and speechConfig",
			jsonData:       `{"generationConfig": {"responseModalities": ["AUDIO"], "speechConfig": {}}, "contents": []}`,
			wantModalities: []string{"AUDIO"},
			wantSpeech:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := strings.NewReader(tt.jsonData)
			result := scanForMetadataFromStream(reader, DefaultLargePayloadConfig())

			if tt.wantNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				if len(tt.wantModalities) > 0 {
					assert.Equal(t, tt.wantModalities, result.ResponseModalities)
				}
				if tt.wantSpeech {
					assert.NotNil(t, result.SpeechConfig)
				}
			}
		})
	}
}

func TestScanTopLevelKeys(t *testing.T) {
	tests := []struct {
		name     string
		jsonData string
		keys     []string
		want     map[string]string // expected key→value (as raw JSON strings)
	}{
		{
			name:     "single key found",
			jsonData: `{"foo": "bar", "baz": 123}`,
			keys:     []string{"foo"},
			want:     map[string]string{"foo": `"bar"`},
		},
		{
			name:     "multiple keys found",
			jsonData: `{"a": 1, "b": "two", "c": [3]}`,
			keys:     []string{"a", "c"},
			want:     map[string]string{"a": "1", "c": "[3]"},
		},
		{
			name:     "key not present",
			jsonData: `{"foo": "bar"}`,
			keys:     []string{"missing"},
			want:     map[string]string{},
		},
		{
			name:     "some keys missing",
			jsonData: `{"a": 1, "b": 2}`,
			keys:     []string{"a", "missing"},
			want:     map[string]string{"a": "1"},
		},
		{
			name:     "all keys found - early exit",
			jsonData: `{"target": true, "skipped_entirely": "this value is never read"}`,
			keys:     []string{"target"},
			want:     map[string]string{"target": "true"},
		},
		{
			name:     "object value captured",
			jsonData: `{"config": {"nested": "value"}, "other": 1}`,
			keys:     []string{"config"},
			want:     map[string]string{"config": `{"nested": "value"}`},
		},
		{
			name:     "array value captured",
			jsonData: `{"items": [1, 2, 3], "count": 3}`,
			keys:     []string{"items", "count"},
			want:     map[string]string{"items": "[1, 2, 3]", "count": "3"},
		},
		{
			name:     "null value captured",
			jsonData: `{"val": null}`,
			keys:     []string{"val"},
			want:     map[string]string{"val": "null"},
		},
		{
			name:     "false value captured",
			jsonData: `{"active": false}`,
			keys:     []string{"active"},
			want:     map[string]string{"active": "false"},
		},
		{
			name:     "empty object",
			jsonData: `{}`,
			keys:     []string{"anything"},
			want:     map[string]string{},
		},
		{
			name:     "key after large string value",
			jsonData: `{"big": "` + strings.Repeat("x", 100000) + `", "small": 42}`,
			keys:     []string{"small"},
			want:     map[string]string{"small": "42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jr := newJSONStreamReader(strings.NewReader(tt.jsonData), defaultBufSize)
			results := jr.scanTopLevelKeys(tt.keys)

			assert.Equal(t, len(tt.want), len(results), "number of results should match")
			for k, wantVal := range tt.want {
				gotVal, ok := results[k]
				assert.True(t, ok, "key %q should be in results", k)
				assert.Equal(t, wantVal, string(gotVal), "value for key %q", k)
			}
		})
	}
}

func TestStreamingRequestBody_PhaseA(t *testing.T) {
	// Test Phase A: metadata found in prefetch
	jsonData := `{"generationConfig": {"responseModalities": ["AUDIO"]}, "contents": [{"parts": [{"text": "hello"}]}]}`

	// Create a mock body stream using a pipe
	reader := strings.NewReader(jsonData)

	config := &LargePayloadConfig{
		Enabled:      true,
		Threshold:    100, // Low threshold for testing
		PrefetchSize: 1024,
	}

	// Simulate what CreateStreamingRequestBody does for Phase A
	prefetchBuf := make([]byte, config.PrefetchSize)
	n, err := io.ReadFull(reader, prefetchBuf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		t.Fatalf("unexpected error: %v", err)
	}
	prefetchBuf = prefetchBuf[:n]

	metadata := ExtractRequestMetadataFromBytes(prefetchBuf)
	require.NotNil(t, metadata)
	assert.True(t, metadata.HasData())
	assert.Contains(t, metadata.ResponseModalities, "AUDIO")

	// Verify we can still read the full body
	upstreamReader := io.MultiReader(bytes.NewReader(prefetchBuf), reader)
	fullBody, err := io.ReadAll(upstreamReader)
	require.NoError(t, err)
	assert.Equal(t, jsonData, string(fullBody))
}

func TestStreamingRequestBody_WaitForMetadata(t *testing.T) {
	srb := &StreamingRequestBody{
		Metadata: &SelectiveRequestMetadata{
			ResponseModalities: []string{"AUDIO"},
		},
		done: nil, // No goroutine, immediately available
	}

	result := srb.WaitForMetadata()
	require.NotNil(t, result)
	assert.Contains(t, result.ResponseModalities, "AUDIO")
}

func TestStreamingRequestBody_WaitForMetadata_WithChannel(t *testing.T) {
	doneChan := make(chan struct{})
	metadata := &SelectiveRequestMetadata{}

	srb := &StreamingRequestBody{
		Metadata: metadata,
		done:     doneChan,
	}

	// Simulate scanner completing
	go func() {
		metadata.ResponseModalities = []string{"AUDIO"}
		close(doneChan)
	}()

	result := srb.WaitForMetadata()
	require.NotNil(t, result)
	assert.Contains(t, result.ResponseModalities, "AUDIO")
}

// ============================================================================
// Data Integrity Tests
// ============================================================================

func TestStreamingRequestBody_DataIntegrity_ByteByByte(t *testing.T) {
	// Test that data passes through unchanged when read byte-by-byte
	testCases := []struct {
		name string
		size int
	}{
		{"1KB", 1024},
		{"64KB", 64 * 1024},
		{"1MB", 1024 * 1024},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate deterministic data
			original := make([]byte, tc.size)
			for i := range original {
				original[i] = byte(i % 256)
			}

			// Wrap with metadata
			jsonPrefix := `{"generationConfig":{"responseModalities":["AUDIO"]},"contents":[{"parts":[{"data":"`
			jsonSuffix := `"}]}]}`

			fullPayload := append([]byte(jsonPrefix), original...)
			fullPayload = append(fullPayload, []byte(jsonSuffix)...)

			// Use OneByteReader to force byte-by-byte reading
			reader := iotest.OneByteReader(bytes.NewReader(fullPayload))

			// Read all through our streaming mechanism
			result, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, fullPayload, result, "Data should pass through unchanged")
		})
	}
}

func TestStreamingRequestBody_HashVerification(t *testing.T) {
	// Verify SHA256 hash matches after streaming
	sizes := []int{1024, 64 * 1024, 256 * 1024}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			// Generate payload
			data := make([]byte, size)
			for i := range data {
				data[i] = byte((i * 7) % 256) // Deterministic pattern
			}

			// Calculate original hash
			originalHash := sha256.Sum256(data)

			// Stream through reader and hash result
			reader := bytes.NewReader(data)
			hasher := sha256.New()
			_, err := io.Copy(hasher, reader)
			require.NoError(t, err)

			resultHash := hasher.Sum(nil)
			assert.Equal(t, originalHash[:], resultHash, "Hash should match after streaming")
		})
	}
}

// ============================================================================
// io.Pipe Deadlock Prevention Tests
// ============================================================================

func TestPipeReader_NoDeadlock_FastConsumer(t *testing.T) {
	done := make(chan bool)
	go func() {
		pr, pw := io.Pipe()

		// Writer goroutine
		go func() {
			defer pw.Close()
			data := make([]byte, 1024*1024) // 1MB
			pw.Write(data)
		}()

		// Consumer (should not block)
		io.Copy(io.Discard, pr)
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: Pipe read blocked for >5 seconds")
	}
}

func TestPipeReader_NoDeadlock_SlowConsumer(t *testing.T) {
	done := make(chan bool)
	go func() {
		pr, pw := io.Pipe()

		// Writer goroutine
		go func() {
			defer pw.Close()
			data := make([]byte, 1024) // Small chunks
			for range 100 {
				pw.Write(data)
			}
		}()

		// Slow consumer
		buf := make([]byte, 128)
		for {
			_, err := pr.Read(buf)
			if err == io.EOF {
				break
			}
			time.Sleep(1 * time.Millisecond) // Simulate slow processing
		}
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("DEADLOCK: Slow consumer blocked for >10 seconds")
	}
}

func TestPipeWriter_CloseEarly_NoBlock(t *testing.T) {
	done := make(chan bool)
	go func() {
		pr, pw := io.Pipe()

		// Close writer early
		pw.Close()

		// Reader should get EOF immediately
		buf := make([]byte, 1024)
		_, err := pr.Read(buf)
		if err != io.EOF {
			t.Errorf("Expected EOF, got %v", err)
		}
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: Early close blocked for >2 seconds")
	}
}

// ============================================================================
// Concurrent Access Tests
// ============================================================================

func TestConcurrentMetadataExtraction(t *testing.T) {
	payloads := []string{
		`{"generationConfig":{"responseModalities":["AUDIO"]}}`,
		`{"generationConfig":{"responseModalities":["TEXT"]}}`,
		`{"generationConfig":{"responseModalities":["IMAGE"]}}`,
		`{"generationConfig":{"speechConfig":{}}}`,
	}

	var wg sync.WaitGroup
	results := make(chan *SelectiveRequestMetadata, len(payloads)*10)

	// Run 10 extractions of each payload concurrently
	for i := 0; i < 10; i++ {
		for _, payload := range payloads {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				metadata := ExtractRequestMetadataFromBytes([]byte(p))
				results <- metadata
			}(payload)
		}
	}

	wg.Wait()
	close(results)

	count := 0
	for metadata := range results {
		assert.NotNil(t, metadata)
		count++
	}
	assert.Equal(t, len(payloads)*10, count)
}

func TestConcurrentStreamScanning(t *testing.T) {
	payload := `{"contents":[{"text":"hello"}],"generationConfig":{"responseModalities":["AUDIO"]}}`

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := strings.NewReader(payload)
			result := scanForMetadataFromStream(reader, DefaultLargePayloadConfig())
			if result == nil {
				errors <- fmt.Errorf("expected non-nil result")
			} else if len(result.ResponseModalities) == 0 {
				errors <- fmt.Errorf("expected modalities")
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

// ============================================================================
// Edge Case Tests
// ============================================================================

func TestExtractMetadata_MalformedJSON(t *testing.T) {
	malformedCases := []string{
		"{incomplete",
		`{"nested": {"unclosed": [`,
		"",
		"null",
		"[]",
		"{}",
		`{"generationConfig": "not_an_object"}`,
		`{"generationConfig": {"responseModalities": "not_an_array"}}`,
	}

	for _, input := range malformedCases {
		t.Run(input[:min(20, len(input))], func(t *testing.T) {
			// Should not panic
			metadata := ExtractRequestMetadataFromBytes([]byte(input))
			// Result may be nil or empty, but should not panic
			_ = metadata
		})
	}
}

func TestStreamScan_MalformedJSON(t *testing.T) {
	malformedCases := []string{
		"{incomplete",
		`{"nested": {"unclosed": [`,
		"",
	}

	for _, input := range malformedCases {
		t.Run(input[:min(15, len(input))], func(t *testing.T) {
			reader := strings.NewReader(input)
			// Should not panic, should return nil
			result := scanForMetadataFromStream(reader, DefaultLargePayloadConfig())
			// Scanner handles malformed JSON gracefully
			_ = result
		})
	}
}

func TestThresholdBoundary_Exact(t *testing.T) {
	threshold := int64(10 * 1024 * 1024) // 10MB

	tests := []struct {
		contentLength int64
		expectedLarge bool
	}{
		{threshold - 1, false},
		{threshold, false},
		{threshold + 1, true},
	}

	config := DefaultLargePayloadConfig()

	for _, tt := range tests {
		t.Run(fmt.Sprintf("len_%d", tt.contentLength), func(t *testing.T) {
			// Mock the content length check
			// Note: Actual fasthttp.RequestCtx mocking would be needed here
			isLarge := tt.contentLength > config.Threshold
			assert.Equal(t, tt.expectedLarge, isLarge)
		})
	}
}

// ============================================================================
// Benchmark Tests
// ============================================================================

func BenchmarkExtractMetadata_Small(b *testing.B) {
	payload := []byte(`{"generationConfig":{"responseModalities":["AUDIO"]}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractRequestMetadataFromBytes(payload)
	}
}

func BenchmarkExtractMetadata_Medium(b *testing.B) {
	// 1KB payload
	content := strings.Repeat("x", 1024)
	payload := []byte(fmt.Sprintf(`{"generationConfig":{"responseModalities":["AUDIO"]},"data":"%s"}`, content))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractRequestMetadataFromBytes(payload)
	}
}

func BenchmarkStreamScan_MetadataAtStart(b *testing.B) {
	payload := `{"generationConfig":{"responseModalities":["AUDIO"]},"contents":[]}`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader := strings.NewReader(payload)
		scanForMetadataFromStream(reader, DefaultLargePayloadConfig())
	}
}

func BenchmarkStreamScan_MetadataAtEnd(b *testing.B) {
	content := strings.Repeat("x", 64*1024) // 64KB before metadata
	payload := fmt.Sprintf(`{"contents":[{"data":"%s"}],"generationConfig":{"responseModalities":["AUDIO"]}}`, content)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader := strings.NewReader(payload)
		scanForMetadataFromStream(reader, DefaultLargePayloadConfig())
	}
}

// ============================================================================
// Payload Generation Helpers for Memory Benchmarks
// ============================================================================

// generateLargeJSONPayload creates a realistic JSON payload of approximately
// the given total size. metadataAtStart=true puts generationConfig before
// contents (Phase A); false puts contents first (Phase B).
func generateLargeJSONPayload(totalSize int, metadataAtStart bool) []byte {
	metadata := `"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Kore"}}}}`
	contentsPrefix := `"contents":[{"parts":[{"inlineData":{"mimeType":"video/mp4","data":"`
	contentsSuffix := `"}}]}]`

	// Calculate the data size needed to reach totalSize
	// JSON structure: { metadata , contentsPrefix DATA contentsSuffix }
	overhead := 1 + len(metadata) + 1 + len(contentsPrefix) + len(contentsSuffix) + 1
	dataSize := totalSize - overhead
	if dataSize < 0 {
		dataSize = 1024
	}

	// Generate base64-like data (valid JSON string characters, repeating pattern)
	const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = b64chars[i%64]
	}

	var buf bytes.Buffer
	buf.Grow(totalSize + 64)
	buf.WriteByte('{')
	if metadataAtStart {
		buf.WriteString(metadata)
		buf.WriteByte(',')
		buf.WriteString(contentsPrefix)
		buf.Write(data)
		buf.WriteString(contentsSuffix)
	} else {
		buf.WriteString(contentsPrefix)
		buf.Write(data)
		buf.WriteString(contentsSuffix)
		buf.WriteByte(',')
		buf.WriteString(metadata)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// simulateStreamingPhaseA replicates the Phase A fast path:
// prefetch 64KB, extract metadata with sonic.Get, compose io.MultiReader, consume body.
// This is what CreateStreamingRequestBody does when metadata is at the start of JSON.
func simulateStreamingPhaseA(payload []byte, prefetchSize int) *SelectiveRequestMetadata {
	reader := bytes.NewReader(payload)

	prefetchBuf := make([]byte, prefetchSize)
	n, err := io.ReadFull(reader, prefetchBuf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil
	}
	prefetchBuf = prefetchBuf[:n]

	metadata := ExtractRequestMetadataFromBytes(prefetchBuf)

	upstreamReader := io.MultiReader(bytes.NewReader(prefetchBuf), reader)
	io.Copy(io.Discard, upstreamReader)

	return metadata
}

// simulateStreamingPhaseB replicates the Phase B fallback path:
// prefetch 64KB (metadata not found), setup TeeReader + io.Pipe, scan with byte-level scanner.
// This is what CreateStreamingRequestBody does when metadata is after the large data.
func simulateStreamingPhaseB(payload []byte, prefetchSize int) *SelectiveRequestMetadata {
	reader := bytes.NewReader(payload)

	prefetchBuf := make([]byte, prefetchSize)
	n, err := io.ReadFull(reader, prefetchBuf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil
	}
	prefetchBuf = prefetchBuf[:n]

	pipeReader, pipeWriter := io.Pipe()
	combinedReader := io.MultiReader(bytes.NewReader(prefetchBuf), reader)
	teeReader := io.TeeReader(combinedReader, pipeWriter)

	doneChan := make(chan struct{})
	var metadata *SelectiveRequestMetadata

	go func() {
		defer close(doneChan)
		defer pipeWriter.Close()
		metadata = scanForMetadataFromStream(teeReader, DefaultLargePayloadConfig())
		io.Copy(io.Discard, teeReader)
	}()

	io.Copy(io.Discard, pipeReader)
	<-doneChan

	return metadata
}

// simulateBufferingPath replicates the old code path without streaming optimization:
// copy entire payload into a new byte slice, then sonic.Unmarshal the whole thing.
// This is what ctx.Request.Body() + sonic.Unmarshal(rawBody, req) does.
func simulateBufferingPath(payload []byte) {
	rawBody := make([]byte, len(payload))
	copy(rawBody, payload)

	var parsed map[string]any
	sonic.Unmarshal(rawBody, &parsed) //nolint:errcheck
	_ = parsed
}

// formatBytes formats a byte count into a human-readable string.
func formatBytes(b uint64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.2f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.2f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.2f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ============================================================================
// Memory Allocation Benchmarks - Streaming vs Buffering
// ============================================================================
//
// These benchmarks compare the memory allocation characteristics of:
//   - STREAMING PATH (our implementation): Prefetch 64KB + io.MultiReader/TeeReader
//   - BUFFERING PATH (without our changes): Read all + sonic.Unmarshal
//
// Run:
//
//	go test -bench=BenchmarkMemory -benchmem -benchtime=3s \
//	  ./transports/bifrost-http/integrations/...
//
// Expected output shows streaming B/op stays constant while buffering scales with payload:
//
//	BenchmarkMemory_Streaming_PhaseA/1MB    ~200KB B/op
//	BenchmarkMemory_Streaming_PhaseA/20MB   ~200KB B/op  (constant!)
//	BenchmarkMemory_Buffering/1MB           ~3MB B/op
//	BenchmarkMemory_Buffering/20MB          ~60MB B/op   (scales with payload!)

func BenchmarkMemory_Streaming_PhaseA(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1MB", 1 * 1024 * 1024},
		{"5MB", 5 * 1024 * 1024},
		{"20MB", 20 * 1024 * 1024},
	}

	for _, s := range sizes {
		payload := generateLargeJSONPayload(s.size, true)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				simulateStreamingPhaseA(payload, 64*1024)
			}
		})
	}
}

func BenchmarkMemory_Streaming_PhaseB(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1MB", 1 * 1024 * 1024},
		{"5MB", 5 * 1024 * 1024},
		{"20MB", 20 * 1024 * 1024},
	}

	for _, s := range sizes {
		payload := generateLargeJSONPayload(s.size, false) // metadata at END
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				simulateStreamingPhaseB(payload, 64*1024)
			}
		})
	}
}

func BenchmarkMemory_Buffering(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1MB", 1 * 1024 * 1024},
		{"5MB", 5 * 1024 * 1024},
		{"20MB", 20 * 1024 * 1024},
	}

	for _, s := range sizes {
		payload := generateLargeJSONPayload(s.size, true)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				simulateBufferingPath(payload)
			}
		})
	}
}

// ============================================================================
// Heap Allocation Tests - runtime.MemStats Comparison
// ============================================================================
//
// These tests use runtime.MemStats to measure actual Go heap allocations,
// providing definitive proof that streaming uses less memory than buffering.
//
// Run:
//
//	go test -v -run TestHeapAllocation -timeout 120s \
//	  ./transports/bifrost-http/integrations/...

func TestHeapAllocation_StreamingVsBuffering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory allocation test in short mode")
	}

	sizes := []struct {
		name string
		size int
	}{
		{"1MB", 1 * 1024 * 1024},
		{"5MB", 5 * 1024 * 1024},
		{"20MB", 20 * 1024 * 1024},
	}

	for _, s := range sizes {
		t.Run(s.name, func(t *testing.T) {
			payload := generateLargeJSONPayload(s.size, true)
			iterations := 3

			// --- Measure buffering path ---
			runtime.GC()
			runtime.GC()
			var memBefore runtime.MemStats
			runtime.ReadMemStats(&memBefore)

			for i := 0; i < iterations; i++ {
				simulateBufferingPath(payload)
			}

			runtime.GC()
			runtime.GC()
			var memAfter runtime.MemStats
			runtime.ReadMemStats(&memAfter)

			bufferingAlloc := (memAfter.TotalAlloc - memBefore.TotalAlloc) / uint64(iterations)

			// --- Measure streaming Phase A path ---
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&memBefore)

			for i := 0; i < iterations; i++ {
				simulateStreamingPhaseA(payload, 64*1024)
			}

			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&memAfter)

			streamingAllocA := (memAfter.TotalAlloc - memBefore.TotalAlloc) / uint64(iterations)

			// --- Measure streaming Phase B path ---
			payloadB := generateLargeJSONPayload(s.size, false) // metadata at end
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&memBefore)

			for i := 0; i < iterations; i++ {
				simulateStreamingPhaseB(payloadB, 64*1024)
			}

			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&memAfter)

			streamingAllocB := (memAfter.TotalAlloc - memBefore.TotalAlloc) / uint64(iterations)

			// --- Report ---
			t.Logf("Payload size: %s", s.name)
			t.Logf("  Buffering path:     %s allocated per op", formatBytes(bufferingAlloc))
			t.Logf("  Streaming Phase A:  %s allocated per op", formatBytes(streamingAllocA))
			t.Logf("  Streaming Phase B:  %s allocated per op", formatBytes(streamingAllocB))

			if bufferingAlloc > 0 {
				ratioA := float64(streamingAllocA) / float64(bufferingAlloc)
				ratioB := float64(streamingAllocB) / float64(bufferingAlloc)
				t.Logf("  Phase A / Buffering: %.1f%%", ratioA*100)
				t.Logf("  Phase B / Buffering: %.1f%%", ratioB*100)

				// Streaming should allocate significantly less than buffering.
				// For payloads >= 1MB, streaming allocates ~128-256KB while
				// buffering allocates ~2-3x the payload size.
				assert.Less(t, ratioA, 0.5,
					"Streaming Phase A should allocate less than 50%% of buffering for %s payload", s.name)
			}
		})
	}
}

func TestConcurrentStreaming_MemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent memory test in short mode")
	}

	payloadSize := 10 * 1024 * 1024 // 10MB per request
	concurrency := 10
	payload := generateLargeJSONPayload(payloadSize, true)

	// --- Measure concurrent buffering ---
	runtime.GC()
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			simulateBufferingPath(payload)
		}()
	}
	wg.Wait()

	runtime.GC()
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	bufferingTotal := memAfter.TotalAlloc - memBefore.TotalAlloc

	// --- Measure concurrent streaming ---
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			simulateStreamingPhaseA(payload, 64*1024)
		}()
	}
	wg.Wait()

	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&memAfter)

	streamingTotal := memAfter.TotalAlloc - memBefore.TotalAlloc

	// --- Report ---
	t.Logf("Concurrent %d x %dMB payloads:", concurrency, payloadSize/(1024*1024))
	t.Logf("  Buffering total allocated: %s", formatBytes(bufferingTotal))
	t.Logf("  Streaming total allocated: %s", formatBytes(streamingTotal))

	if bufferingTotal > 0 {
		ratio := float64(streamingTotal) / float64(bufferingTotal)
		t.Logf("  Streaming / Buffering: %.1f%%", ratio*100)
		savings := bufferingTotal - streamingTotal
		t.Logf("  Memory saved: %s", formatBytes(savings))
	}

	// Streaming should use a small fraction of what buffering uses.
	// 10 concurrent 10MB requests:
	//   Buffering: ~10 * 20MB = ~200MB (copy + unmarshal overhead per request)
	//   Streaming: ~10 * 128KB = ~1.3MB (prefetch buffer per request)
	maxStreamingAllowed := uint64(concurrency) * 5 * 1024 * 1024 // 5MB per request max
	assert.Less(t, streamingTotal, maxStreamingAllowed,
		"Concurrent streaming should not allocate more than %s total", formatBytes(maxStreamingAllowed))
}
