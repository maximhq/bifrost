// Package integrations provides HTTP transport integrations for Bifrost.
// This file implements large payload streaming optimization for GenAI routes.
package integrations

import (
	"bytes"
	"io"
	"slices"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

// LargePayloadConfig configures large payload streaming optimization
type LargePayloadConfig struct {
	Enabled      bool  `json:"enabled"`       // Enable streaming optimization (default: true)
	Threshold    int64 `json:"threshold"`     // Bytes threshold to trigger streaming (default: 10MB)
	MaxSize      int64 `json:"max_size"`      // Max allowed payload size (default: 500MB)
	PrefetchSize int   `json:"prefetch_size"` // Bytes to prefetch for metadata extraction (default: 64KB)
}

// DefaultLargePayloadConfig returns default configuration
func DefaultLargePayloadConfig() *LargePayloadConfig {
	return &LargePayloadConfig{
		Enabled:      true,
		Threshold:    10 * 1024 * 1024,  // 10MB
		MaxSize:      500 * 1024 * 1024, // 500MB
		PrefetchSize: 64 * 1024,         // 64KB
	}
}

// IsLargePayload checks Content-Length against configured threshold.
// Set header "X-Bifrost-Force-Buffering: true" to bypass streaming and force
// the old buffering path (for load testing / comparison). Remove this check
// once benchmarking is complete.
func IsLargePayload(ctx *fasthttp.RequestCtx, config *LargePayloadConfig) bool {
	if config == nil || !config.Enabled {
		return false
	}
	if string(ctx.Request.Header.Peek("X-Bifrost-Force-Buffering")) == "true" {
		return false
	}
	contentLength := ctx.Request.Header.ContentLength()
	return contentLength > int(config.Threshold)
}

// StreamingRequestBody holds streaming request components for large payloads
type StreamingRequestBody struct {
	// UpstreamReader is the io.Reader for upstream request body
	// Either: io.MultiReader(prefetch, remaining) or TeeReader pipe
	UpstreamReader io.Reader

	// ContentLength is the total body size (from Content-Length header)
	ContentLength int

	// Metadata holds extracted routing/observability metadata
	// Available immediately after CreateStreamingRequestBody returns (for Phase A)
	// For Phase B (byte-level scanner), may need to call WaitForMetadata()
	Metadata *SelectiveRequestMetadata

	// UsedJstreamFallback indicates if the fallback scanner was needed (for metrics/logging)
	UsedJstreamFallback bool

	// done is closed when scanner goroutine completes (only if fallback used)
	done <-chan struct{}
}

// SelectiveRequestMetadata holds routing-relevant fields extracted from request
// These are ONLY for routing/observability, NOT for request modification
type SelectiveRequestMetadata struct {
	ResponseModalities []string  // e.g., ["AUDIO"] for speech routing
	SpeechConfig       *struct{} // Just need presence, not contents
}

// HasData returns true if any metadata was extracted
func (m *SelectiveRequestMetadata) HasData() bool {
	if m == nil {
		return false
	}
	return len(m.ResponseModalities) > 0 || m.SpeechConfig != nil
}

// CreateStreamingRequestBody creates a streaming body with two-phase extraction:
// Phase A (Fast): Prefetch 64KB + sonic.Get() - works if metadata is at start
// Phase B (Fallback): Byte-level scanner + TeeReader - scans entire stream if needed
//
// Memory: O(prefetch buffer) for Phase A, O(64KB scanner buffer) for Phase B
// Request body is passed through UNCHANGED to upstream
func CreateStreamingRequestBody(
	ctx *fasthttp.RequestCtx,
	config *LargePayloadConfig,
) (*StreamingRequestBody, error) {
	bodyStream := ctx.RequestBodyStream()
	contentLength := ctx.Request.Header.ContentLength()
	prefetchSize := config.PrefetchSize
	if prefetchSize <= 0 {
		prefetchSize = 64 * 1024 // Default 64KB
	}

	if bodyStream == nil {
		// Fallback: no streaming support from fasthttp
		body := ctx.Request.Body()
		metadata := ExtractRequestMetadataFromBytes(body)
		return &StreamingRequestBody{
			UpstreamReader:      bytes.NewReader(body),
			ContentLength:       len(body),
			Metadata:            metadata,
			UsedJstreamFallback: false,
		}, nil
	}

	// ═══════════════════════════════════════════════════════════════
	// PHASE A: Prefetch + sonic.Get() (Fast Path)
	// ═══════════════════════════════════════════════════════════════
	prefetchBuf := make([]byte, prefetchSize)
	n, err := io.ReadFull(bodyStream, prefetchBuf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	prefetchBuf = prefetchBuf[:n] // Trim to actual bytes read

	// Try to extract metadata from prefetch buffer
	metadata := ExtractRequestMetadataFromBytes(prefetchBuf)

	if metadata != nil && metadata.HasData() {
		// SUCCESS: Metadata found in prefetch!
		// Combine prefetch buffer + remaining stream for upstream
		upstreamReader := io.MultiReader(
			bytes.NewReader(prefetchBuf),
			bodyStream,
		)
		return &StreamingRequestBody{
			UpstreamReader:      upstreamReader,
			ContentLength:       contentLength,
			Metadata:            metadata,
			UsedJstreamFallback: false,
		}, nil
	}

	// ═══════════════════════════════════════════════════════════════
	// PHASE B: Byte-level scanner + TeeReader (Fallback - metadata not in prefetch)
	// ═══════════════════════════════════════════════════════════════
	// This path is taken when:
	// - generationConfig appears AFTER contents (video) in JSON
	// - Prefetch didn't contain complete metadata

	// Create pipe: data flows pipeWriter -> pipeReader
	pipeReader, pipeWriter := io.Pipe()

	// Combine prefetch + remaining into single reader
	combinedReader := io.MultiReader(
		bytes.NewReader(prefetchBuf),
		bodyStream,
	)

	// TeeReader: reads from combined, copies to pipeWriter
	teeReader := io.TeeReader(combinedReader, pipeWriter)

	doneChan := make(chan struct{})
	metadataResult := &SelectiveRequestMetadata{}

	// Goroutine: scan with byte-level scanner while data flows through
	go func() {
		defer close(doneChan)
		defer pipeWriter.Close()

		// Scan for metadata using byte-level scanner (field-order independent)
		scanned := scanForMetadataFromStream(teeReader, config)
		if scanned != nil {
			*metadataResult = *scanned
		}

		// CRITICAL: Continue reading to EOF even after finding metadata
		// This ensures ALL data flows through TeeReader to pipeWriter
		_, _ = io.Copy(io.Discard, teeReader)
	}()

	return &StreamingRequestBody{
		UpstreamReader:      pipeReader,
		ContentLength:       contentLength,
		Metadata:            metadataResult, // Will be populated by goroutine
		UsedJstreamFallback: true,
		done:                doneChan,
	}, nil
}

// WaitForMetadata blocks until scanning is complete (only needed for fallback path)
// For prefetch path, metadata is immediately available
func (s *StreamingRequestBody) WaitForMetadata() *SelectiveRequestMetadata {
	if s.done != nil {
		<-s.done
	}
	return s.Metadata
}

// scanForMetadataFromStream scans a JSON stream for the "generationConfig" key
// at the top level and extracts routing metadata from its value.
//
// This is a thin wrapper around jsonStreamReader.scanTopLevelKeys that extracts
// only the "generationConfig" key and parses routing metadata from it.
//
// Memory: O(64KB buffer + generationConfig object size ≈ 64KB + ~300B)
func scanForMetadataFromStream(reader io.Reader, config *LargePayloadConfig) *SelectiveRequestMetadata {
	jr := newJSONStreamReader(reader, config.PrefetchSize)
	results := jr.scanTopLevelKeys([]string{"generationConfig"})
	if data, ok := results["generationConfig"]; ok {
		return extractMetadataFromGenerationConfigBytes(data)
	}
	return nil
}

// extractMetadataFromGenerationConfigBytes extracts routing metadata from a
// generationConfig JSON object value (not the full request body).
// Used by scanForMetadataFromStream after it has located and captured the
// generationConfig value from the stream.
func extractMetadataFromGenerationConfigBytes(data []byte) *SelectiveRequestMetadata {
	var metadata SelectiveRequestMetadata

	node, err := sonic.Get(data, "responseModalities")
	if err == nil {
		raw, _ := node.Raw()
		if raw != "" {
			var modalities []string
			if err := sonic.UnmarshalString(raw, &modalities); err == nil {
				metadata.ResponseModalities = modalities
			}
		}
	}

	node, err = sonic.Get(data, "speechConfig")
	if err == nil {
		raw, _ := node.Raw()
		if raw != "" {
			metadata.SpeechConfig = &struct{}{}
		}
	}

	return &metadata
}

// ExtractRequestMetadataFromBytes extracts metadata using sonic.Get()
// Memory: O(1) - only extracts specific fields, doesn't parse entire JSON
func ExtractRequestMetadataFromBytes(data []byte) *SelectiveRequestMetadata {
	var metadata SelectiveRequestMetadata

	// Extract responseModalities from generationConfig
	node, err := sonic.Get(data, "generationConfig", "responseModalities")
	if err == nil {
		raw, _ := node.Raw()
		if raw != "" {
			var modalities []string
			if err := sonic.UnmarshalString(raw, &modalities); err == nil {
				metadata.ResponseModalities = modalities
			}
		}
	}

	// Check for speechConfig presence
	node, err = sonic.Get(data, "generationConfig", "speechConfig")
	if err == nil {
		raw, _ := node.Raw()
		if raw != "" {
			metadata.SpeechConfig = &struct{}{}
		}
	}

	return &metadata
}

// IsSpeechRequestFromMetadata checks if request is speech based on extracted metadata
func IsSpeechRequestFromMetadata(metadata *SelectiveRequestMetadata) bool {
	if metadata == nil {
		return false
	}
	if slices.Contains(metadata.ResponseModalities, "AUDIO") {
		return true
	}
	return metadata.SpeechConfig != nil
}

// IsTranscriptionRequestFromMetadata checks for transcription requests.
//
// For large payloads, this ALWAYS returns false. Here's why:
//
// Transcription = audio INPUT (user sends audio to be converted to text)
// Detection requires scanning: contents[].parts[].inlineData.mimeType or
//
//	contents[].parts[].fileData.mimeType for audio MIME types
//
// The problem: The `contents` array is exactly where the large payload lives
// (e.g., 400MB base64-encoded video/audio). Parsing it would require loading
// the entire payload into memory, defeating the purpose of streaming optimization.
//
// In contrast, speech detection uses generationConfig.responseModalities and
// generationConfig.speechConfig, which are small metadata fields we CAN extract
// from the prefetch buffer without parsing the full payload.
//
// Trade-off: Large payload transcription requests will be classified as
// IsTranscription=false. This may affect transcription-specific routing/governance,
// but the request itself still works correctly - it just won't be tagged.
func IsTranscriptionRequestFromMetadata(_ *SelectiveRequestMetadata) bool {
	return false
}

// IsImageGenerationRequestFromMetadata checks if request is image generation based on extracted metadata.
// This checks if responseModalities contains "IMAGE".
// Note: Model-based detection (IsImagenModel) is handled separately via URL extraction.
func IsImageGenerationRequestFromMetadata(metadata *SelectiveRequestMetadata) bool {
	if metadata == nil {
		return false
	}
	return slices.Contains(metadata.ResponseModalities, "IMAGE")
}

// IsImageEditRequestFromMetadata checks for image edit requests from metadata.
//
// For large payloads, this ALWAYS returns false. Here's why:
//
// Image edit detection requires scanning:
// - contents[].parts[].inlineData.mimeType for image MIME types + IMAGE in responseModalities
// - instances[].referenceImages for Imagen model image edits
//
// Both `contents` and `instances` are exactly where the large payload lives
// (e.g., base64-encoded images). Parsing them would require loading the entire
// payload into memory, defeating the purpose of streaming optimization.
//
// Trade-off: Large payload image edit requests will be classified as
// IsImageEdit=false (and may route as image generation instead).
// The request itself still works correctly — it just won't be tagged as an edit.
func IsImageEditRequestFromMetadata(_ *SelectiveRequestMetadata) bool {
	return false
}

// LargePayloadUsageMetadata minimal struct for response token extraction
type LargePayloadUsageMetadata struct {
	PromptTokenCount        int32 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int32 `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int32 `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int32 `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int32 `json:"thoughtsTokenCount,omitempty"`
}

// ExtractUsageMetadataSelective uses sonic.Get() for response token extraction
// Memory: O(1) - only extracts usageMetadata field
func ExtractUsageMetadataSelective(data []byte) *LargePayloadUsageMetadata {
	node, err := sonic.Get(data, "usageMetadata")
	if err != nil {
		return nil
	}
	raw, _ := node.Raw()
	if raw == "" {
		return nil
	}
	var usage LargePayloadUsageMetadata
	if err := sonic.UnmarshalString(raw, &usage); err != nil {
		return nil
	}
	return &usage
}
