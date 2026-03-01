package lib

import (
	"io"
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var logger schemas.Logger

// SetLogger sets the logger for the application.
func SetLogger(l schemas.Logger) {
	logger = l
}

// StreamLargeResponseBody extracts the large response reader from context and streams
// it directly to the client. Sets status 200, content-type, and content-length headers.
// Returns false if the reader is not available (caller should send an error response).
func StreamLargeResponseBody(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) bool {
	reader, ok := bifrostCtx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser)
	if !ok || reader == nil {
		return false
	}

	contentLength, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargeResponseContentLength).(int)
	contentType, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargeResponseContentType).(string)

	// Mirror large-response-mode to fasthttp UserValue so post-hook middleware
	// (which only sees ctx.UserValue, not bifrostCtx) can skip body materialization.
	ctx.SetUserValue(FastHTTPUserValueLargeResponseMode, true)

	ctx.SetStatusCode(fasthttp.StatusOK)
	if contentType != "" {
		ctx.SetContentType(contentType)
	} else {
		ctx.SetContentType("application/json")
	}
	// bodySize for SetBodyStream: positive = known size, -1 = unknown (read until EOF).
	// fasthttp treats 0 as "known empty", so default to -1 when CL is unavailable.
	bodySize := contentLength
	if bodySize > 0 {
		ctx.Response.Header.Set("Content-Length", strconv.Itoa(contentLength))
	} else {
		bodySize = -1
	}

	ctx.Response.SetBodyStream(reader, bodySize)
	return true
}
