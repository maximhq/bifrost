package utils

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestNewMultipartFileUpload asserts the streamed body has the exact length it declares (it is
// sent as Content-Length) and parses back to the same fields, in order, and the same file.
func TestNewMultipartFileUpload(t *testing.T) {
	file := bytes.Repeat([]byte("0123456789"), 100_000)
	fields := []MultipartField{{"purpose", "batch"}, {"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "3600"}}
	upload, err := NewMultipartFileUpload(fields, "file", "batch.jsonl", schemas.NewBytesBodySource(file))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(upload.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := upload.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(body) != upload.Size {
		t.Fatalf("body is %d bytes, Size says %d", len(body), upload.Size)
	}

	_, params, err := mime.ParseMediaType(upload.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for i, want := range fields {
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		got, _ := io.ReadAll(part)
		if part.FormName() != want.Name || string(got) != want.Value {
			t.Errorf("part %d = %s=%q, want %s=%q", i, part.FormName(), got, want.Name, want.Value)
		}
	}
	part, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if part.FormName() != "file" || part.FileName() != "batch.jsonl" {
		t.Errorf("file part is %s/%s, want file/batch.jsonl", part.FormName(), part.FileName())
	}
	if ct := part.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("file part Content-Type = %q, want application/octet-stream (as CreateFormFile writes it)", ct)
	}
	got, _ := io.ReadAll(part)
	if !bytes.Equal(got, file) {
		t.Errorf("file part has %d bytes that differ from the %d-byte file", len(got), len(file))
	}
	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("want no parts after the file, got %v", err)
	}
}

// TestNewMultipartFileUploadUnknownSize asserts a source of unknown size is refused: the body
// must be sent with an exact Content-Length.
func TestNewMultipartFileUploadUnknownSize(t *testing.T) {
	if _, err := NewMultipartFileUpload(nil, "file", "f", unknownSizeSource{}); err == nil {
		t.Fatal("want an error for a source of unknown size")
	}
}

type unknownSizeSource struct{}

func (unknownSizeSource) Size() int64 { return -1 }
func (unknownSizeSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

// TestFileUploadSource asserts FileSource wins over File, and File (Go SDK callers) is used
// without a copy when there is no FileSource.
func TestFileUploadSource(t *testing.T) {
	src := schemas.NewBytesBodySource([]byte("from source"))
	if got := FileUploadSource(&schemas.BifrostFileUploadRequest{File: []byte("bytes"), FileSource: src}); got != src {
		t.Error("want FileSource when it is set")
	}
	b, err := schemas.ReadBodySource(FileUploadSource(&schemas.BifrostFileUploadRequest{File: []byte("bytes")}))
	if err != nil || string(b) != "bytes" {
		t.Errorf("File fallback read %q, %v; want bytes", b, err)
	}
}

// TestSetPassthroughRequestBody asserts a passthrough upload passed as BodySource is sent as a
// stream with its exact length, overriding a Content-Length copied from the client, and that a
// plain Body is still sent as is.
func TestSetPassthroughRequestBody(t *testing.T) {
	var req fasthttp.Request
	req.Header.SetContentLength(999)
	if err := SetPassthroughRequestBody(&req, &schemas.BifrostPassthroughRequest{BodySource: schemas.NewBytesBodySource([]byte("streamed"))}); err != nil {
		t.Fatal(err)
	}
	if !req.IsBodyStream() || req.Header.ContentLength() != len("streamed") {
		t.Errorf("want a body stream of length %d, got stream=%v length=%d", len("streamed"), req.IsBodyStream(), req.Header.ContentLength())
	}
	if string(req.Body()) != "streamed" {
		t.Errorf("body = %q, want streamed", req.Body())
	}

	var plain fasthttp.Request
	if err := SetPassthroughRequestBody(&plain, &schemas.BifrostPassthroughRequest{Body: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	if plain.IsBodyStream() || string(plain.Body()) != `{"a":1}` {
		t.Errorf("plain body = %q (stream=%v), want the bytes as is", plain.Body(), plain.IsBodyStream())
	}
}

// TestPassthroughUsageRequestBody asserts usage extraction of a streamed upload reads its plain
// fields re-encoded as multipart, without the file, and reads Body when there is one.
func TestPassthroughUsageRequestBody(t *testing.T) {
	form := &multipart.Form{
		Value: map[string][]string{"n": {"2"}, "size": {"1024x1024"}},
		File:  map[string][]*multipart.FileHeader{"image": {{Filename: "big.png", Size: 50 << 20}}},
	}
	body := PassthroughUsageRequestBody(&schemas.BifrostPassthroughRequest{Form: form, BodySource: schemas.NewBytesBodySource(nil)})
	firstLine, _, _ := bytes.Cut(body, []byte("\r\n"))
	got, err := multipart.NewReader(bytes.NewReader(body), string(bytes.TrimPrefix(firstLine, []byte("--")))).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value["n"][0] != "2" || got.Value["size"][0] != "1024x1024" || len(got.File) != 0 {
		t.Errorf("usage body form = %v files=%d, want n=2 size=1024x1024 and no files", got.Value, len(got.File))
	}
	if b := PassthroughUsageRequestBody(&schemas.BifrostPassthroughRequest{Body: []byte("json")}); string(b) != "json" {
		t.Errorf("want Body as is, got %q", b)
	}
}
