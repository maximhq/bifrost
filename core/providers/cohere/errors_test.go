package cohere

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestParseCohereError_PopulatesNestedErrorType verifies the upstream exception
// type is surfaced on the nested error object (error.type), not only at the top
// level, so OpenAI-shaped consumers see it.
func TestParseCohereError_PopulatesNestedErrorType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"type":"invalid_request_error","message":"model not found"}`)

	bifrostErr := parseCohereError(&resp)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type, "nested error.type must be populated")
	assert.Equal(t, "invalid_request_error", *bifrostErr.Error.Type)
	require.NotNil(t, bifrostErr.Type, "top-level type must remain populated")
	assert.Equal(t, "invalid_request_error", *bifrostErr.Type)
	assert.Equal(t, "model not found", bifrostErr.Error.Message)
}
