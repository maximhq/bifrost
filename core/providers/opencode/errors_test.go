package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestParseOpencodeError(t *testing.T) {
	t.Run("overlays the Opencode envelope and keeps the status and retry hint", func(t *testing.T) {
		var resp fasthttp.Response
		resp.SetStatusCode(fasthttp.StatusTooManyRequests)
		resp.Header.Set("Retry-After", "7")
		resp.SetBodyString(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)

		bErr := parseOpencodeError(&resp)

		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, fasthttp.StatusTooManyRequests, *bErr.StatusCode)
		assert.Equal(t, "slow down", bErr.Error.Message)
		require.NotNil(t, bErr.Error.Type)
		assert.Equal(t, "rate_limit_error", *bErr.Error.Type)
		assert.Equal(t, int64(7000), bErr.ExtraFields.RetryAfter)
	})

	t.Run("an empty body names the status", func(t *testing.T) {
		var resp fasthttp.Response
		resp.SetStatusCode(fasthttp.StatusBadGateway)

		bErr := parseOpencodeError(&resp)

		assert.Equal(t, "provider API error (status 502)", bErr.Error.Message)
		assert.Equal(t, int64(0), bErr.ExtraFields.RetryAfter)
	})
}
