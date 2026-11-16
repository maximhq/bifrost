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

func TestParseCohereErrorCarriesUpstreamID(t *testing.T) {
	// Cohere returns {"id", "message"} on every v2 error; ToCohereError echoes the id back.
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{"id":"1ad6894a-9ed1-48db-9316-0efa1b87a7e2","message":"invalid request: inputs[0] is too long"}`))

	bifrostErr := parseCohereError(resp)
	require.NotNil(t, bifrostErr.EventID)
	assert.Equal(t, "1ad6894a-9ed1-48db-9316-0efa1b87a7e2", *bifrostErr.EventID)
	assert.Equal(t, "1ad6894a-9ed1-48db-9316-0efa1b87a7e2", ToCohereError(bifrostErr).ID)
}
