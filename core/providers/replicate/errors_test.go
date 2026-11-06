package replicate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestParseReplicateError(t *testing.T) {
	t.Run("detail message with a retry hint", func(t *testing.T) {
		var resp fasthttp.Response
		resp.SetStatusCode(fasthttp.StatusTooManyRequests)
		resp.Header.Set("Retry-After", "7")
		resp.SetBodyString(`{"title":"Request was throttled","detail":"Request was throttled. Expected available in 7 seconds.","status":429}`)

		bErr := parseReplicateError(&resp)

		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, fasthttp.StatusTooManyRequests, *bErr.StatusCode)
		assert.Equal(t, "Request was throttled. Expected available in 7 seconds.", bErr.Error.Message)
		assert.Equal(t, int64(7000), bErr.ExtraFields.RetryAfter)
	})

	t.Run("a body without detail is the message", func(t *testing.T) {
		var resp fasthttp.Response
		resp.SetStatusCode(fasthttp.StatusBadGateway)
		resp.SetBodyString("upstream unavailable")

		bErr := parseReplicateError(&resp)

		assert.Equal(t, "upstream unavailable", bErr.Error.Message)
		assert.Equal(t, int64(0), bErr.ExtraFields.RetryAfter)
	})
}
