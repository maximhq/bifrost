package huggingface

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestParseHuggingFaceImageError_PopulatesNestedErrorType verifies the upstream
// exception type is surfaced on the nested error object (error.type), matching
// the other providers, so OpenAI-shaped consumers see it.
func TestParseHuggingFaceImageError_PopulatesNestedErrorType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"type":"validation_error","message":"invalid input"}`)

	bifrostErr := parseHuggingFaceImageError(&resp)

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type, "nested error.type must be populated")
	assert.Equal(t, "validation_error", *bifrostErr.Error.Type)
	require.NotNil(t, bifrostErr.Type, "top-level type must remain populated")
	assert.Equal(t, "validation_error", *bifrostErr.Type)
}
