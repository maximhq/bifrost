package lib

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/valyala/fasthttp"
)

// RequestErrorHandler replaces fasthttp's default handling of requests that fail before routing. A
// body over the configured limit is answered with 413 and a message naming the limit and the setting
// that controls it, and is logged, instead of fasthttp's generic "Error when parsing request" 400.
// limitMB is the client_config.max_request_body_size_mb the server was started with; warn may be nil.
func RequestErrorHandler(limitMB int, warn func(format string, args ...any)) func(*fasthttp.RequestCtx, error) {
	return func(ctx *fasthttp.RequestCtx, err error) {
		if errors.Is(err, fasthttp.ErrBodyTooLarge) {
			limit := limitMB
			message := fmt.Sprintf("Request body exceeds this Bifrost gateway's request size limit of %d MB. The limit is configurable with client_config.max_request_body_size_mb.", limit)
			if warn != nil {
				warn("request body exceeds max_request_body_size_mb (%d MB): method=%s path=%q content_length=%d remote=%s", limit, ctx.Method(), ctx.Path(), ctx.Request.Header.ContentLength(), ctx.RemoteIP())
			}
			body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "request_too_large", "message": message}})
			ctx.Response.Reset()
			ctx.SetStatusCode(http.StatusRequestEntityTooLarge)
			ctx.SetContentType("application/json")
			ctx.SetBody(body)
			return
		}
		// Everything else keeps fasthttp's default responses.
		var small *fasthttp.ErrSmallBuffer
		var netErr *net.OpError
		switch {
		case errors.As(err, &small):
			ctx.Error("Too big request header", fasthttp.StatusRequestHeaderFieldsTooLarge)
		case errors.As(err, &netErr) && netErr.Timeout():
			ctx.Error("Request timeout", fasthttp.StatusRequestTimeout)
		default:
			ctx.Error("Error when parsing request", fasthttp.StatusBadRequest)
		}
	}
}
