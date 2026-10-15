package lib

import "github.com/valyala/fasthttp"

// DefaultBodyBufferPoolLimitBytes is the largest request-body buffer a pooled
// fasthttp RequestCtx keeps between requests when server.body_buffer_pool_limit_bytes
// is unset. 4 MiB keeps typical bodies on the zero-allocation reuse path while
// stopping keep-alive connections from pinning their largest-ever body.
const DefaultBodyBufferPoolLimitBytes = 4 * 1024 * 1024

// BodyBufferPoolLimitDisabled turns the limit off, restoring fasthttp's default of
// keeping every body buffer at its high-water mark.
const BodyBufferPoolLimitDisabled = -1

// ResolveBodyBufferPoolLimit returns the per-buffer retention limit in bytes for the
// given server config, or BodyBufferPoolLimitDisabled when the limit is turned off.
// Unset (0) resolves to DefaultBodyBufferPoolLimitBytes; positive values are clamped
// to maxRequestBodySizeBytes when that is positive, since no body buffer can grow
// past it and a larger limit would be indistinguishable from it.
func ResolveBodyBufferPoolLimit(serverConfig *ServerConfig, maxRequestBodySizeBytes int) int {
	limit := 0
	if serverConfig != nil {
		limit = serverConfig.BodyBufferPoolLimitBytes
	}
	if limit < 0 {
		return BodyBufferPoolLimitDisabled
	}
	if limit == 0 {
		limit = DefaultBodyBufferPoolLimitBytes
	}
	if maxRequestBodySizeBytes > 0 && limit > maxRequestBodySizeBytes {
		limit = maxRequestBodySizeBytes
	}
	return limit
}

// ApplyBodyBufferPoolLimit configures fasthttp to drop request-body buffers larger
// than the resolved limit instead of keeping them for reuse.
//
// fasthttp's server keeps one RequestCtx per keep-alive connection and, by default,
// only truncates its body buffer between requests (and when the ctx returns to the
// pool for the next connection). Each pooled ctx therefore holds the largest body it
// has ever read, so with large LLM payloads per-pod memory ratchets up and never comes
// back. SetBodySizePoolLimit makes Request.Reset release any buffer above the limit,
// which runs after every request and on ctx release, bounding retention to roughly
// connections × limit. The setting is process-global, so it also applies to fasthttp
// client requests (only pooling changes, never request bytes). The response side is
// left at fasthttp's default.
func ApplyBodyBufferPoolLimit(serverConfig *ServerConfig, maxRequestBodySizeBytes int) {
	limit := ResolveBodyBufferPoolLimit(serverConfig, maxRequestBodySizeBytes)
	if limit == BodyBufferPoolLimitDisabled {
		if logger != nil {
			logger.Info("request body buffer pool limit disabled: pooled connections keep their largest body buffer")
		}
		return
	}
	fasthttp.SetBodySizePoolLimit(limit, BodyBufferPoolLimitDisabled)
	if logger != nil {
		logger.Info("request body buffer pool limit: %d bytes (buffers above this are released after each request)", limit)
	}
}
