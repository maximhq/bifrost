package modelark

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func makeErrorResponse(statusCode int, body string) *fasthttp.Response {
	resp := fasthttp.AcquireResponse()
	resp.SetStatusCode(statusCode)
	resp.Header.SetContentType("application/json")
	resp.SetBodyString(body)
	return resp
}

func TestParseModelArkError(t *testing.T) {
	t.Run("surfaces_code_and_message", func(t *testing.T) {
		resp := makeErrorResponse(fasthttp.StatusBadRequest, `{"error":{"code":"ModelNotOpen","message":"The model is not activated."}}`)
		defer fasthttp.ReleaseResponse(resp)

		bifrostErr := parseModelArkError(resp)
		require.NotNil(t, bifrostErr.Error)
		require.NotNil(t, bifrostErr.Error.Code)
		assert.Equal(t, "ModelNotOpen", *bifrostErr.Error.Code)
		assert.Equal(t, "The model is not activated.", bifrostErr.Error.Message)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, fasthttp.StatusBadRequest, *bifrostErr.StatusCode)
	})

	t.Run("falls_back_when_envelope_is_absent", func(t *testing.T) {
		resp := makeErrorResponse(fasthttp.StatusServiceUnavailable, `{}`)
		defer fasthttp.ReleaseResponse(resp)

		bifrostErr := parseModelArkError(resp)
		require.NotNil(t, bifrostErr.Error)
		assert.Nil(t, bifrostErr.Error.Code)
		assert.Equal(t, "ModelArk API request failed", bifrostErr.Error.Message)
	})

	t.Run("trims_trailing_newlines", func(t *testing.T) {
		resp := makeErrorResponse(fasthttp.StatusBadRequest, `{"error":{"code":"InvalidParameter","message":"duration out of range\n\n"}}`)
		defer fasthttp.ReleaseResponse(resp)

		bifrostErr := parseModelArkError(resp)
		require.NotNil(t, bifrostErr.Error)
		assert.Equal(t, "duration out of range", bifrostErr.Error.Message)
	})
}
