package soniox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func newErrorResponse(status int, body string) *fasthttp.Response {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(status)
	resp.Header.SetContentType("application/json")
	resp.SetBodyString(body)
	return resp
}

func TestParseSonioxError(t *testing.T) {
	t.Run("REST validation failure lists each field", func(t *testing.T) {
		bifrostErr := parseSonioxError(newErrorResponse(400, `{"status_code":400,"error_type":"invalid_request","message":"Your request did not pass validation.","validation_errors":[{"error_type":"file_not_found","location":"body.payload.file_id","message":"File could not be found."}],"request_id":"r1"}`))
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 400, *bifrostErr.StatusCode)
		require.NotNil(t, bifrostErr.Error.Type)
		assert.Equal(t, "invalid_request", *bifrostErr.Error.Type)
		assert.Equal(t, "Your request did not pass validation. body.payload.file_id: File could not be found.", bifrostErr.Error.Message)
	})

	t.Run("TTS host shape uses error_message", func(t *testing.T) {
		bifrostErr := parseSonioxError(newErrorResponse(400, `{"error_code":400,"error_message":"Missing required field: language","error_type":"invalid_request","request_id":"r2"}`))
		assert.Equal(t, "Missing required field: language", bifrostErr.Error.Message)
		require.NotNil(t, bifrostErr.Error.Type)
		assert.Equal(t, "invalid_request", *bifrostErr.Error.Type)
	})

	t.Run("authentication failure keeps its status", func(t *testing.T) {
		bifrostErr := parseSonioxError(newErrorResponse(401, `{"status_code":401,"error_type":"unauthenticated","message":"Incorrect API key provided.","validation_errors":[]}`))
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 401, *bifrostErr.StatusCode)
		assert.Equal(t, "Incorrect API key provided.", bifrostErr.Error.Message)
	})

	t.Run("a non-JSON body still yields an error with the status", func(t *testing.T) {
		bifrostErr := parseSonioxError(newErrorResponse(403, `error code: 1010`))
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 403, *bifrostErr.StatusCode)
		require.NotNil(t, bifrostErr.Error)
		assert.NotEmpty(t, bifrostErr.Error.Message)
	})
}

func TestParseSonioxJobError(t *testing.T) {
	t.Run("an invalid audio file is a client error", func(t *testing.T) {
		bifrostErr := parseSonioxJobError(&SonioxTranscription{
			ID:           "job-1",
			ErrorType:    new("invalid_audio_file"),
			ErrorMessage: new("Invalid audio file: Error analyzing the file."),
		})
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 400, *bifrostErr.StatusCode)
		assert.Equal(t, "invalid_audio_file", *bifrostErr.Error.Type)
		assert.Equal(t, "Invalid audio file: Error analyzing the file. (transcription job-1)", bifrostErr.Error.Message)
	})

	t.Run("any other job failure is an upstream failure", func(t *testing.T) {
		bifrostErr := parseSonioxJobError(&SonioxTranscription{ID: "job-2", ErrorType: new("internal_error")})
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 502, *bifrostErr.StatusCode)
		assert.Equal(t, "soniox transcription failed (transcription job-2)", bifrostErr.Error.Message)
	})
}
