package lib

import (
	"bytes"
	"mime/multipart"

	"github.com/maximhq/bifrost/framework/passthroughparse"
	"github.com/valyala/fasthttp"
)

// fastHTTPUserValueMultipartForm holds a request's *parsedMultipartForm once the passthrough route
// or a transport plugin has parsed the request's multipart form, so the other reuses it.
const fastHTTPUserValueMultipartForm = "__bifrost_multipart_form"

// fastHTTPUserValuePassthroughRoute marks a request served by a passthrough route (see
// MarkPassthroughRoute).
const fastHTTPUserValuePassthroughRoute = "__bifrost_passthrough_route"

// fastHTTPUserValueMultipartBodyReplaced marks a request whose body a plugin replaced (see
// ResetMultipartForm): its body is in memory as bytes, whatever its Content-Length says.
const fastHTTPUserValueMultipartBodyReplaced = "__bifrost_multipart_body_replaced"

// multipartFormMaxMemory is the in-memory limit for a form parsed from the body stream, as fasthttp
// uses for its own parse: larger files go straight to temp files.
const multipartFormMaxMemory = 8 << 10

// parsedMultipartForm is a request's parsed multipart form and the Content-Type of each of its
// value parts (see passthroughparse.ReadMultipartForm).
type parsedMultipartForm struct {
	form  *multipart.Form
	types map[string][]string
	owned bool // parsed here from the body stream, so its temp files are ours to remove
}

// Close removes the temp files of a form parsed here. fasthttp closes every user value that is an
// io.Closer when the request is reset, after the response is written, so they last the request.
// A form fasthttp parsed is left to fasthttp.
func (p *parsedMultipartForm) Close() error {
	if p.owned {
		return p.form.RemoveAll()
	}
	return nil
}

// PassthroughMultipartForm returns a multipart request's parsed form and the Content-Type each
// value part arrived with, which a parsed form loses (valueTypes[name][i] is the type of
// form.Value[name][i]). It is for the passthrough route, which forwards the form as it arrived and
// logs it.
//
// An uncompressed body still in its stream is parsed here, once, with ReadMultipartForm, and the
// result is kept on the request: route handlers do not read it from ctx.MultipartForm(). A body
// already in memory is parsed by fasthttp and its types are read from the bytes when they are kept
// (multipartBodyBytesReadable); otherwise, as for a compressed body, valueTypes is nil.
func PassthroughMultipartForm(ctx *fasthttp.RequestCtx) (form *multipart.Form, valueTypes map[string][]string, err error) {
	boundary := string(ctx.Request.Header.MultipartFormBoundary())
	uncompressed := len(ctx.Request.Header.ContentEncoding()) == 0
	if p, ok := ctx.UserValue(fastHTTPUserValueMultipartForm).(*parsedMultipartForm); ok {
		if p.types == nil && multipartBodyBytesReadable(ctx) {
			p.types = passthroughparse.MultipartValueTypes(bytes.NewReader(ctx.Request.Body()), boundary)
		}
		return p.form, p.types, nil
	}
	if boundary == "" {
		return nil, nil, fasthttp.ErrNoMultipartForm
	}
	if ctx.Request.IsBodyStream() && uncompressed {
		form, valueTypes, err = passthroughparse.ReadMultipartForm(ctx.RequestBodyStream(), boundary, multipartFormMaxMemory)
		if err != nil {
			return nil, nil, err
		}
		ctx.SetUserValue(fastHTTPUserValueMultipartForm, &parsedMultipartForm{form: form, types: valueTypes, owned: true})
		return form, valueTypes, nil
	}
	form, err = ctx.MultipartForm()
	if err != nil {
		return nil, nil, err
	}
	if multipartBodyBytesReadable(ctx) {
		valueTypes = passthroughparse.MultipartValueTypes(bytes.NewReader(ctx.Request.Body()), boundary)
	}
	ctx.SetUserValue(fastHTTPUserValueMultipartForm, &parsedMultipartForm{form: form, types: valueTypes})
	return form, valueTypes, nil
}

// ResetMultipartForm forgets a request's parsed multipart form once its body has been replaced
// (a transport plugin set a new body): the form came from the old body, and Request.SetBody has
// already dropped fasthttp's copy and its temp files. A form parsed here has its temp files
// removed now. The next PassthroughMultipartForm or SharedMultipartForm parses the new body.
func ResetMultipartForm(ctx *fasthttp.RequestCtx) {
	if p, ok := ctx.UserValue(fastHTTPUserValueMultipartForm).(*parsedMultipartForm); ok {
		p.Close() //nolint:errcheck
		ctx.RemoveUserValue(fastHTTPUserValueMultipartForm)
	}
	ctx.SetUserValue(fastHTTPUserValueMultipartBodyReplaced, true)
}

// multipartBodyBytesReadable reports whether a multipart request's raw bytes are in memory, so the
// value types can be read from them: an uncompressed body that is not a stream and either was
// replaced by a plugin (ResetMultipartForm) or has no positive Content-Length (a chunked upload
// RequestBodyMiddleware read into memory). fasthttp pre-parses a multipart body of known length
// while reading it and keeps no bytes, and Body() would then rebuild the whole form in memory
// (without the types), so a known-length body as read from the client is never read back.
func multipartBodyBytesReadable(ctx *fasthttp.RequestCtx) bool {
	if ctx.Request.IsBodyStream() || len(ctx.Request.Header.ContentEncoding()) != 0 {
		return false
	}
	if replaced, _ := ctx.UserValue(fastHTTPUserValueMultipartBodyReplaced).(bool); replaced {
		return true
	}
	return ctx.Request.Header.ContentLength() <= 0
}

// MarkPassthroughRoute wraps a passthrough route's handler chain, middlewares included, so code that
// runs before the route handler knows the request is forwarded as it arrived. A transport plugin
// that reads the upload (SharedMultipartForm) then gets the passthrough parse, value types and all,
// since no route handler on these routes reads ctx.MultipartForm().
func MarkPassthroughRoute(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.SetUserValue(fastHTTPUserValuePassthroughRoute, true)
		next(ctx)
	}
}

// SharedMultipartForm returns a request's parsed multipart form for code that runs before the
// route handler (a transport plugin reading the body). On a passthrough route (MarkPassthroughRoute)
// it is PassthroughMultipartForm. Elsewhere it is the form already parsed, or fasthttp's
// ctx.MultipartForm(), which the route handler reads again; its value types are then nil.
func SharedMultipartForm(ctx *fasthttp.RequestCtx) (form *multipart.Form, valueTypes map[string][]string, err error) {
	if p, ok := ctx.UserValue(fastHTTPUserValueMultipartForm).(*parsedMultipartForm); ok {
		return p.form, p.types, nil
	}
	if passthrough, _ := ctx.UserValue(fastHTTPUserValuePassthroughRoute).(bool); passthrough {
		return PassthroughMultipartForm(ctx)
	}
	form, err = ctx.MultipartForm()
	if err != nil {
		return nil, nil, err
	}
	ctx.SetUserValue(fastHTTPUserValueMultipartForm, &parsedMultipartForm{form: form})
	return form, nil, nil
}
