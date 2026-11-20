package lib

import (
	"bufio"
	"bytes"
	"io"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// multipartFormTestBody is an upload with a plain field, a part without a filename that carries a
// non-text Content-Type, and a file larger than the in-memory limit, so the parse spills it to a
// temp file.
func multipartFormTestBody(t *testing.T) (contentType string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("purpose", "batch"))
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="mask"`}, "Content-Type": {"application/octet-stream"}})
	require.NoError(t, err)
	_, err = part.Write([]byte("printable mask bytes"))
	require.NoError(t, err)
	part, err = w.CreateFormFile("file", "big.jsonl")
	require.NoError(t, err)
	_, err = part.Write(bytes.Repeat([]byte("x"), 4*multipartFormMaxMemory))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

func multipartFormTestCtx(contentType string, body []byte, streamed bool) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.SetContentType(contentType)
	if streamed {
		ctx.Request.SetBodyStream(bytes.NewReader(body), len(body))
	} else {
		ctx.Request.SetBody(body)
	}
	return ctx
}

var multipartFormTestTypes = map[string][]string{"purpose": {""}, "mask": {"application/octet-stream"}}

// TestPassthroughMultipartFormStreamedFirst asserts the passthrough route parses a streamed body
// once with its value types, a transport plugin then reuses that form (the stream is already read),
// and the form's temp files are removed when the request ends.
func TestPassthroughMultipartFormStreamedFirst(t *testing.T) {
	contentType, body := multipartFormTestBody(t)
	ctx := multipartFormTestCtx(contentType, body, true)

	form, types, err := PassthroughMultipartForm(ctx)
	require.NoError(t, err)
	require.Equal(t, multipartFormTestTypes, types)
	again, againTypes, err := PassthroughMultipartForm(ctx)
	require.NoError(t, err)
	require.Same(t, form, again, "the form is parsed once per request")
	require.Equal(t, types, againTypes)

	shared, sharedTypes, err := SharedMultipartForm(ctx)
	require.NoError(t, err)
	require.Same(t, form, shared, "a transport plugin must reuse the passthrough parse, not read the spent stream")
	require.Equal(t, types, sharedTypes)

	f, err := form.File["file"][0].Open()
	require.NoError(t, err)
	content, err := io.ReadAll(f)
	f.Close()
	require.NoError(t, err)
	require.Len(t, content, 4*multipartFormMaxMemory)

	ctx.Request.Reset()
	_, err = form.File["file"][0].Open()
	require.Error(t, err, "the form's temp file must be removed when the request ends")
}

// TestPassthroughMultipartFormPluginFirst asserts that when a transport plugin opened the body
// first, the passthrough route gets that same form instead of failing on the spent stream. Off a
// passthrough route fasthttp parsed it, so a streamed body's value types are unknown and a buffered
// body's are read from its bytes. On a passthrough route (MarkPassthroughRoute) the plugin already
// gets the passthrough parse, value types included.
func TestPassthroughMultipartFormPluginFirst(t *testing.T) {
	contentType, body := multipartFormTestBody(t)
	for _, tc := range []struct {
		name        string
		streamed    bool
		passthrough bool
		wantTypes   map[string][]string
	}{
		{name: "streamed", streamed: true, wantTypes: nil},
		{name: "buffered", streamed: false, wantTypes: multipartFormTestTypes},
		{name: "passthrough route streamed", streamed: true, passthrough: true, wantTypes: multipartFormTestTypes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := multipartFormTestCtx(contentType, body, tc.streamed)
			defer ctx.Request.Reset()
			var shared *multipart.Form
			var sharedTypes map[string][]string
			var err error
			read := func(ctx *fasthttp.RequestCtx) { shared, sharedTypes, err = SharedMultipartForm(ctx) }
			if tc.passthrough {
				MarkPassthroughRoute(read)(ctx)
			} else {
				read(ctx)
			}
			require.NoError(t, err)
			if !tc.passthrough {
				require.Nil(t, sharedTypes)
			}

			form, types, err := PassthroughMultipartForm(ctx)
			require.NoError(t, err)
			require.Same(t, shared, form)
			require.Equal(t, tc.wantTypes, types)
			require.Equal(t, []string{"printable mask bytes"}, form.Value["mask"])
		})
	}
}

// TestPassthroughMultipartFormPreParsedKnownLength asserts a known-length upload that fasthttp
// pre-parsed while reading it (no bytes kept) is used as parsed, with value types unknown: reading
// Body() there would rebuild the whole form in memory, and without the types anyway.
func TestPassthroughMultipartFormPreParsedKnownLength(t *testing.T) {
	contentType, body := multipartFormTestBody(t)
	raw := append([]byte("POST /openai_passthrough/v1/files HTTP/1.1\r\nHost: bifrost\r\nContent-Type: "+contentType+
		"\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"), body...)
	ctx := &fasthttp.RequestCtx{}
	require.NoError(t, ctx.Request.Read(bufio.NewReader(bytes.NewReader(raw))))
	defer ctx.Request.Reset()

	form, types, err := PassthroughMultipartForm(ctx)
	require.NoError(t, err)
	require.Nil(t, types)
	require.Equal(t, []string{"printable mask bytes"}, form.Value["mask"])
	require.False(t, multipartBodyBytesReadable(ctx), "a pre-parsed body of known length must not be read back with Body()")
}
