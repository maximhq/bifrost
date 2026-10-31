package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestParseVertexError_PopulatesStatusType verifies the Vertex status
// (e.g. RESOURCE_EXHAUSTED) is surfaced on error.type rather than being dropped,
// so passthrough/OpenAI-shaped consumers see the exception type.
func TestParseVertexError_PopulatesStatusType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.SetBodyString(`{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)

	bifrostErr := parseVertexError(&resp)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type, "nested error.type must be populated from status")
	assert.Equal(t, "RESOURCE_EXHAUSTED", *bifrostErr.Error.Type)
	assert.Equal(t, "Quota exceeded", bifrostErr.Error.Message)
}

// TestParseVertexError_NoStatusNoType verifies that when the body carries no
// Vertex status we don't fabricate an error.type.
func TestParseVertexError_NoStatusNoType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"error":{"code":400,"message":"bad request"}}`)

	bifrostErr := parseVertexError(&resp)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Nil(t, bifrostErr.Error.Type, "no status present, so none should be fabricated")
}

// TestParseVertexError_RetryInfo verifies a google.rpc.RetryInfo detail in the body
// becomes the error's retry hint, and that a body without one leaves it unset.
func TestParseVertexError_RetryInfo(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.SetBodyString(`{"error":{"code":429,"message":"Resource exhausted. Please try again later.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"12s"}]}}`)

	bifrostErr := parseVertexError(&resp)

	require.NotNil(t, bifrostErr)
	assert.Equal(t, int64(12000), bifrostErr.ExtraFields.RetryAfter)

	var plain fasthttp.Response
	plain.SetStatusCode(fasthttp.StatusTooManyRequests)
	plain.SetBodyString(`{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)
	assert.Equal(t, int64(0), parseVertexError(&plain).ExtraFields.RetryAfter)
}

// The cached-content and batch job parsers carry the retry hint too.
func TestVertexOperationErrors_RetryInfo(t *testing.T) {
	body := `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"12s"}]}}`

	var cached fasthttp.Response
	cached.SetStatusCode(fasthttp.StatusTooManyRequests)
	cached.SetBodyString(body)
	cachedErr := parseVertexCachedContentError(&cached)
	assert.Equal(t, "Quota exceeded", cachedErr.Error.Message)
	assert.Equal(t, int64(12000), cachedErr.ExtraFields.RetryAfter)

	var job fasthttp.Response
	job.SetStatusCode(fasthttp.StatusTooManyRequests)
	job.Header.Set("Retry-After", "5")
	job.SetBodyString(`{"error":{"code":429,"message":"Quota exceeded"}}`)
	jobErr := parseVertexJobAPIError(&job, "batch create")
	assert.Equal(t, "Quota exceeded", jobErr.Error.Message)
	assert.Equal(t, int64(5000), jobErr.ExtraFields.RetryAfter)
}
