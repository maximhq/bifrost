package sgl

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// buildSGLErrorResponse creates a fasthttp.Response with the given status code
// and body. Returned as a helper to keep tests focused on error parsing logic.
func buildSGLErrorResponse(status int, body string) *fasthttp.Response {
	resp := fasthttp.AcquireResponse()
	resp.SetStatusCode(status)
	resp.Header.SetContentType("application/json")
	resp.SetBodyString(body)
	return resp
}

// strDeref returns the dereferenced value or empty string for nil.
func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestParseSGLError_FlatEnvelope(t *testing.T) {
	t.Parallel()

	resp := buildSGLErrorResponse(400, `{"object":"error","message":"bad request","type":"BadRequestError","code":400}`)
	defer fasthttp.ReleaseResponse(resp)

	bifrostErr := ParseSGLError(resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Equal(t, "bad request", bifrostErr.Error.Message)
	assert.Equal(t, "BadRequestError", strDeref(bifrostErr.Error.Type))
	assert.Equal(t, "400", strDeref(bifrostErr.Error.Code))
}

func TestParseSGLError_WrappedEnvelope(t *testing.T) {
	t.Parallel()

	resp := buildSGLErrorResponse(400, `{"error":{"message":"wrapped boom","type":"invalid_request_error","code":"some_code"}}`)
	defer fasthttp.ReleaseResponse(resp)

	bifrostErr := ParseSGLError(resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Equal(t, "wrapped boom", bifrostErr.Error.Message)
	assert.Equal(t, "invalid_request_error", strDeref(bifrostErr.Error.Type))
	assert.Equal(t, "some_code", strDeref(bifrostErr.Error.Code))
}

func TestParseSGLError_SubstringMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		wantCode string
		wantType string
	}{
		{
			name:     "context length exceeded",
			message:  "This model's maximum context length is 4096 tokens. However, you requested 5000 tokens (..). Please reduce the length of the messages or completion. Input is longer than the model's context length.",
			wantCode: "context_length_exceeded",
			wantType: "invalid_request_error",
		},
		{
			name:     "out of memory",
			message:  "CUDA out of memory while attempting to allocate buffer",
			wantCode: "out_of_memory",
			wantType: "server_error",
		},
		{
			name:     "model not loaded",
			message:  "requested model is not loaded on this server",
			wantCode: "model_not_found",
			wantType: "invalid_request_error",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := `{"object":"error","message":` + jsonString(tc.message) + `,"type":"BadRequestError","code":400}`
			resp := buildSGLErrorResponse(400, body)
			defer fasthttp.ReleaseResponse(resp)

			bifrostErr := ParseSGLError(resp)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.Error)

			// Message is always preserved verbatim — never replaced by a generic.
			assert.Equal(t, tc.message, bifrostErr.Error.Message)

			assert.Equal(t, tc.wantCode, strDeref(bifrostErr.Error.Code), "code mapping")
			assert.Equal(t, tc.wantType, strDeref(bifrostErr.Error.Type), "type mapping")
		})
	}
}

func TestParseSGLError_FallthroughPreservesMessage(t *testing.T) {
	t.Parallel()

	// An unrecognized sglang error message should be preserved as-is, with
	// no substring-derived code/type overrides applied.
	const msg = "some sglang-specific error nobody has mapped yet"
	resp := buildSGLErrorResponse(503, `{"object":"error","message":"`+msg+`","type":"InternalServerError","code":503}`)
	defer fasthttp.ReleaseResponse(resp)

	bifrostErr := ParseSGLError(resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Equal(t, msg, bifrostErr.Error.Message)
	// Type/code come from the flat envelope, not from a substring mapping.
	assert.Equal(t, "InternalServerError", strDeref(bifrostErr.Error.Type))
	assert.Equal(t, "503", strDeref(bifrostErr.Error.Code))
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 503, *bifrostErr.StatusCode)
}

func TestParseSGLError_EmptyBodyDelegatesToFallback(t *testing.T) {
	t.Parallel()

	resp := buildSGLErrorResponse(429, "")
	defer fasthttp.ReleaseResponse(resp)

	bifrostErr := ParseSGLError(resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	// We do not assert exact phrasing of the HTTP-status fallback message;
	// only that we produced something non-empty so callers see a useful error.
	assert.NotEmpty(t, bifrostErr.Error.Message)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 429, *bifrostErr.StatusCode)
}

// TestParseSGLError_GzipFlatEnvelope verifies that a gzip-encoded sglang flat
// error envelope is still parsed correctly. ParseOpenAIError decodes the body
// upstream and stashes the decoded JSON on ExtraFields.RawResponse; our
// flat-envelope fallback must read from there, not from the still-compressed
// resp.Body().
func TestParseSGLError_GzipFlatEnvelope(t *testing.T) {
	t.Parallel()

	const msg = "out of memory while loading shard 0"
	plain := `{"object":"error","message":"` + msg + `","type":"InternalServerError","code":500}`

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write([]byte(plain))
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(500)
	resp.Header.SetContentType("application/json")
	resp.Header.Set("Content-Encoding", "gzip")
	resp.SetBody(buf.Bytes())

	bifrostErr := ParseSGLError(resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Equal(t, msg, bifrostErr.Error.Message)
	// Substring mapping should still fire on the decoded message.
	assert.Equal(t, "out_of_memory", strDeref(bifrostErr.Error.Code))
	assert.Equal(t, "server_error", strDeref(bifrostErr.Error.Type))
}

// jsonString minimally escapes a Go string for embedding in a JSON literal.
// Only handles characters used by the test fixtures.
func jsonString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			out = append(out, '\\', c)
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}
