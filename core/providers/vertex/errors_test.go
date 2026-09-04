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

// TestParseVertexError_SurfacesBadRequestFieldViolations covers issue #6589's
// observability ask: Vertex 400s often carry google.rpc.BadRequest fieldViolations
// in error.details naming the rejected field, but Bifrost only forwarded
// error.message ("Request contains an invalid argument."), leaving callers blind
// to which field the upstream rejected.
func TestParseVertexError_SurfacesBadRequestFieldViolations(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"generation_config.response_json_schema","description":"Invalid JSON schema."}]}]}}`)

	bifrostErr := parseVertexError(&resp)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "generation_config.response_json_schema",
		"the rejected field path from error.details must be surfaced")
	assert.Contains(t, bifrostErr.Error.Message, "Invalid JSON schema.",
		"the field violation description from error.details must be surfaced")
}
