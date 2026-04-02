package utils

import (
	"bytes"
	"io"
	"math"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// LargeResponseReader wraps an io.Reader and releases the fasthttp response on Close.
// Used by providers to keep the response alive while the transport streams it to the client.
type LargeResponseReader struct {
	io.Reader
	Resp     *fasthttp.Response
	cleanup  func()
	consumed bool // true after Read returns io.EOF — body fully consumed through Reader chain
}

// Read delegates to the wrapped Reader and tracks EOF so Close() can skip
// a redundant (and potentially blocking) drain of the body stream.
func (r *LargeResponseReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.consumed = true
	}
	return n, err
}

// Close drains any unconsumed body stream and releases the underlying fasthttp
// response back to the pool. Draining prevents "whitespace in header" errors on
// connection reuse when the client disconnects before the full response is consumed
// (see: fasthttp#1743).
//
// When the body was already fully consumed through the Reader chain (consumed == true),
// the drain is skipped. For identity-encoded responses (no Content-Length), the body
// stream is a fasthttp closeReader that blocks until the TCP connection closes — which
// can take minutes if the upstream server keeps the connection alive.
func (r *LargeResponseReader) Close() error {
	if r == nil || r.Resp == nil {
		return nil
	}
	if !r.consumed {
		if bodyStream := r.Resp.BodyStream(); bodyStream != nil {
			_, _ = io.Copy(io.Discard, bodyStream)
			if closer, ok := bodyStream.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}
	if r.cleanup != nil {
		r.cleanup()
		r.cleanup = nil
	}
	fasthttp.ReleaseResponse(r.Resp)
	r.Resp = nil
	return nil
}

// BuildLargeResponseClient creates a streaming-enabled fasthttp client for large response detection.
// The client caps buffering at the threshold and enables response body streaming.
func BuildLargeResponseClient(base *fasthttp.Client, responseThreshold int64) *fasthttp.Client {
	client := CloneFastHTTPClientConfig(base)
	if responseThreshold > 0 && responseThreshold <= int64(math.MaxInt) {
		client.MaxResponseBodySize = int(responseThreshold)
	}
	client.StreamResponseBody = true
	return client
}

// PrepareResponseStreaming configures response body streaming when a large response
// threshold is set in context. Returns the client to use for MakeRequestWithContext.
// When threshold > 0: sets resp.StreamBody = true and returns a streaming-enabled client.
// When threshold <= 0: returns the original client unchanged (no-op for feature-off path).
func PrepareResponseStreaming(ctx *schemas.BifrostContext, client *fasthttp.Client, resp *fasthttp.Response) *fasthttp.Client {
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)
	if responseThreshold <= 0 {
		return client
	}
	resp.StreamBody = true
	return BuildLargeResponseClient(client, responseThreshold)
}

// MaterializeStreamErrorBody reads a streamed error body into resp so that resp.Body()
// returns the error payload for parsing. No-op when response streaming is not active.
func MaterializeStreamErrorBody(ctx *schemas.BifrostContext, resp *fasthttp.Response) {
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)
	if responseThreshold <= 0 {
		return
	}
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		gz, reader, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
		if wasGzip {
			defer ReleaseGzipReader(gz)
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(reader, 512*1024)) // 512KB cap for error bodies
		if readErr != nil {
			return
		}
		resp.SetBody(bodyBytes)
	}
}

// FinalizeResponseWithLargeDetection processes the response body with optional large response
// detection. Takes ownership semantics: when isLargeResponse is true, the caller must NOT
// release resp (it's wrapped in a reader stored in context). When false, resp is unchanged
// and the caller should release as normal.
//
// Returns:
//   - (body, false, nil) — normal path; body ready for parsing; resp NOT released.
//   - (nil, true, nil) — large response detected; context keys set for streaming;
//     caller must set respOwned = false.
//   - (nil, false, err) — error; resp NOT released.
func FinalizeResponseWithLargeDetection(
	ctx *schemas.BifrostContext,
	resp *fasthttp.Response,
	logger schemas.Logger,
) ([]byte, bool, *schemas.BifrostError) {
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)

	// No threshold — normal buffered read (feature-off path)
	if responseThreshold <= 0 {
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		// Copy body before caller releases resp
		return append([]byte(nil), body...), false, nil
	}

	contentLength := resp.Header.ContentLength()

	// Known small response — read from stream, return body for normal parsing
	if contentLength > 0 && int64(contentLength) <= responseThreshold {
		if bodyStream := resp.BodyStream(); bodyStream != nil {
			gz, reader, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
			if wasGzip {
				defer ReleaseGzipReader(gz)
			}
			bodyBytes, readErr := io.ReadAll(reader)
			if readErr != nil {
				return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, readErr)
			}
			return bodyBytes, false, nil
		}
		// No stream — buffered fallback
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return append([]byte(nil), body...), false, nil
	}

	// Unknown Content-Length (chunked transfer encoding) — buffer up to responseThreshold
	// to determine if response is truly large. Responses within threshold are returned
	// buffered for normal parsing/logging; only responses exceeding threshold are streamed.
	if contentLength <= 0 {
		if bodyStream := resp.BodyStream(); bodyStream != nil {
			gz, reader, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
			releaseGzip := func() {}
			if wasGzip {
				releaseGzip = func() {
					ReleaseGzipReader(gz)
				}
			}
			bodyBytes, readErr := io.ReadAll(io.LimitReader(reader, responseThreshold+1))
			if readErr != nil {
				releaseGzip()
				return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, readErr)
			}
			if int64(len(bodyBytes)) <= responseThreshold {
				releaseGzip()
				return bodyBytes, false, nil
			}
			// Exceeds threshold without Content-Length — set up large response streaming.
			combinedReader := io.MultiReader(bytes.NewReader(bodyBytes), reader)
			closableReader := &LargeResponseReader{
				Reader:  combinedReader,
				Resp:    resp,
				cleanup: releaseGzip,
			}
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, closableReader)
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentLength, contentLength)
			if ct := string(resp.Header.ContentType()); ct != "" {
				ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentType, ct)
			}
			previewLen := min(len(bodyBytes), 1048576)
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadResponsePreview, string(bodyBytes[:previewLen]))
			return nil, true, nil
		}
		// No stream — buffered fallback
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return append([]byte(nil), body...), false, nil
	}

	// Known large response (Content-Length > threshold) — prefetch first 64KB for
	// metadata extraction, then stream the rest without full materialization.
	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		// No stream available — fall back to buffered read
		if logger != nil {
			logger.Warn("large-response fallback to buffered path: content_length=%d threshold=%d body_stream_nil=true", contentLength, responseThreshold)
		}
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return append([]byte(nil), body...), false, nil
	}

	// Decompress on-the-fly if provider returned gzip-encoded response.
	// Clears Content-Encoding so the transport doesn't re-add it to the client response.
	gz, decompressedStream, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
	if wasGzip {
		contentLength = -1 // decompressed size unknown; transport will use chunked encoding
	}

	prefetchSize := 64 * 1024 // default
	if ps, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadPrefetchSize).(int); ok && ps > 0 {
		prefetchSize = ps
	}
	prefetchBuf := make([]byte, prefetchSize)
	n, readErr := io.ReadFull(decompressedStream, prefetchBuf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		if wasGzip {
			ReleaseGzipReader(gz)
		}
		return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, readErr)
	}
	prefetchBuf = prefetchBuf[:n]

	combinedReader := io.MultiReader(bytes.NewReader(prefetchBuf), decompressedStream)
	closableReader := &LargeResponseReader{
		Reader: combinedReader,
		Resp:   resp,
		cleanup: func() {
			if wasGzip {
				ReleaseGzipReader(gz)
			}
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, closableReader)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentLength, contentLength)
	if ct := string(resp.Header.ContentType()); ct != "" {
		ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentType, ct)
	}
	previewLen := min(n, 1048576)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadResponsePreview, string(prefetchBuf[:previewLen]))

	return nil, true, nil
}

// ParseOpenAIUsageFromBytes parses OpenAI-format usage from raw JSON bytes into BifrostLLMUsage.
// Handles both Chat Completions (prompt_tokens/completion_tokens) and Responses API
// (input_tokens/output_tokens) field names. Expects the "usage" object bytes directly,
// not the full response body.
func ParseOpenAIUsageFromBytes(data []byte) *schemas.BifrostLLMUsage {
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		// Responses API uses different field names
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := sonic.Unmarshal(data, &usage); err != nil {
		return nil
	}

	result := &schemas.BifrostLLMUsage{}
	if usage.PromptTokens > 0 {
		result.PromptTokens = usage.PromptTokens
	} else if usage.InputTokens > 0 {
		result.PromptTokens = usage.InputTokens
	}
	if usage.CompletionTokens > 0 {
		result.CompletionTokens = usage.CompletionTokens
	} else if usage.OutputTokens > 0 {
		result.CompletionTokens = usage.OutputTokens
	}
	if usage.TotalTokens > 0 {
		result.TotalTokens = usage.TotalTokens
	} else {
		result.TotalTokens = result.PromptTokens + result.CompletionTokens
	}

	if result.TotalTokens == 0 {
		return nil
	}
	return result
}

// SetupStreamingPassthrough configures large response passthrough for streaming
// responses when large payload mode is active. Wraps the response body stream
// in a LargeResponseReader and sets context keys for the transport layer.
// Returns true if passthrough was set up. When true, the caller should return
// a closed channel and must NOT release resp — it's owned by the reader in context.
func SetupStreamingPassthrough(ctx *schemas.BifrostContext, resp *fasthttp.Response) bool {
	isLargePayload, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if !isLargePayload {
		return false
	}

	reader, releaseGzip := DecompressStreamBody(resp)

	// Wrap reader with idle timeout to detect stalled streams.
	reader, stopIdleTimeout := NewIdleTimeoutReader(reader, resp.BodyStream(), GetStreamIdleTimeout(ctx))

	closableReader := &LargeResponseReader{
		Reader: reader,
		Resp:   resp,
		cleanup: func() {
			stopIdleTimeout()
			releaseGzip()
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, closableReader)
	if ct := string(resp.Header.ContentType()); ct != "" {
		ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentType, ct)
	}
	return true
}
