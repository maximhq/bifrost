package passthroughparse

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// multipartBody builds a multipart/form-data body with text fields and one file part per file.
func multipartBody(t testing.TB, fields [][2]string, files map[string][]byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, kv := range fields {
		require.NoError(t, w.WriteField(kv[0], kv[1]))
	}
	for field, data := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="upload-`+strings.TrimSuffix(field, "[]")+`.bin"`)
		header.Set("Content-Type", "application/octet-stream")
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

func TestSummarizeMultipart(t *testing.T) {
	audio := bytes.Repeat([]byte{0x00, 0xff, 0x10}, 1000)
	contentType, body := multipartBody(t, [][2]string{
		{"model", "whisper-1"}, {"language", "en"},
		{"timestamp_granularities[]", "word"}, {"timestamp_granularities[]", "segment"},
	}, map[string][]byte{"file": audio})

	summary, ok := SummarizeMultipart(contentType, body)
	require.True(t, ok)
	require.False(t, bytes.Contains(summary, audio[:30]), "file bytes must never be kept")

	var decoded MultipartSummary
	require.NoError(t, sonic.Unmarshal(summary, &decoded))
	require.Equal(t, "whisper-1", decoded.Fields["model"])
	require.Equal(t, []any{"word", "segment"}, decoded.Fields["timestamp_granularities[]"])
	require.Len(t, decoded.Files, 1)
	require.Equal(t, MultipartFile{Field: "file", Filename: "upload-file.bin", ContentType: "application/octet-stream", Bytes: int64(len(audio))}, decoded.Files[0])

	// Byte-stable: the same body always summarizes to the same JSON.
	again, _ := SummarizeMultipart(contentType, body)
	require.Equal(t, summary, again)

	_, ok = SummarizeMultipart("application/json", body)
	require.False(t, ok)
	_, ok = SummarizeMultipart("multipart/form-data", body)
	require.False(t, ok, "no boundary")
	_, ok = SummarizeMultipart(contentType, body[:len(body)/2])
	require.False(t, ok, "truncated body")
}

func TestParseMultipartRequests(t *testing.T) {
	summarize := func(fields [][2]string, files map[string][]byte) []byte {
		ct, body := multipartBody(t, fields, files)
		summary, ok := SummarizeMultipart(ct, body)
		require.True(t, ok)
		return summary
	}

	t.Run("openai transcription", func(t *testing.T) {
		body := summarize([][2]string{{"model", "whisper-1"}, {"language", "en"}, {"temperature", "0.2"}, {"response_format", "json"}, {"timestamp_granularities[]", "word"}}, map[string][]byte{"file": []byte("RIFF....")})
		r, err := Parse(Input{Provider: schemas.OpenAI, Model: "whisper-1", Method: "POST", Path: "/v1/audio/transcriptions", StatusCode: 200, RequestBody: body, ResponseBody: []byte(`{"text":"hello"}`)})
		require.NoError(t, err)
		req := r.Request.TranscriptionRequest
		require.NotNil(t, req)
		require.Equal(t, "upload-file.bin", req.Input.Filename)
		require.Empty(t, req.Input.File)
		require.Equal(t, "en", *req.Params.Language)
		require.InDelta(t, 0.2, *req.Params.Temperature, 1e-9)
		require.Equal(t, []string{"word"}, req.Params.TimestampGranularities)
		require.Equal(t, "hello", r.Response.TranscriptionResponse.Text)
	})
	t.Run("openai image edit", func(t *testing.T) {
		body := summarize([][2]string{{"model", "gpt-image-1"}, {"prompt", "add a hat"}, {"n", "2"}, {"size", "1024x1024"}}, map[string][]byte{"image[]": []byte("png")})
		r, err := Parse(Input{Provider: schemas.OpenAI, Model: "gpt-image-1", Method: "POST", Path: "/v1/images/edits", StatusCode: 200, RequestBody: body, ResponseBody: []byte(`{"created":1,"data":[{"b64_json":"aGk="}]}`)})
		require.NoError(t, err)
		req := r.Request.ImageEditRequest
		require.NotNil(t, req)
		require.Equal(t, "add a hat", req.Input.Prompt)
		require.Len(t, req.Input.Images, 1)
		require.Equal(t, 2, *req.Params.N)
		require.Len(t, r.Response.ImageGenerationResponse.Data, 1)
	})
	t.Run("openai image variation", func(t *testing.T) {
		body := summarize([][2]string{{"model", "dall-e-2"}, {"n", "1"}, {"size", "256x256"}}, map[string][]byte{"image": []byte("png")})
		r, err := Parse(Input{Provider: schemas.OpenAI, Model: "dall-e-2", Method: "POST", Path: "/v1/images/variations", StatusCode: 200, RequestBody: body})
		require.NoError(t, err)
		require.NotNil(t, r.Request.ImageVariationRequest)
		require.Equal(t, "256x256", *r.Request.ImageVariationRequest.Params.Size)
	})
	t.Run("a raw multipart body is not a summary", func(t *testing.T) {
		_, raw := multipartBody(t, [][2]string{{"model", "whisper-1"}}, map[string][]byte{"file": []byte("x")})
		r, err := Parse(Input{Provider: schemas.OpenAI, Model: "whisper-1", Method: "POST", Path: "/v1/audio/transcriptions", StatusCode: 200, RequestBody: raw})
		require.NoError(t, err)
		require.Nil(t, r)
	})
}

// TestSummarizeMultipartIsBestEffort pins that malformed bodies are rejected, never a panic.
func TestSummarizeMultipartIsBestEffort(t *testing.T) {
	ct := "multipart/form-data; boundary=XYZ"
	for _, body := range []string{
		"", "--XYZ", "--XYZ--", "--XYZ\r\n", "--XYZ\r\nContent-Disposition: form-data\r\n\r\nvalue",
		"--XYZ\r\nbroken header\r\n\r\nv\r\n--XYZ--", "--XYZ\r\nContent-Disposition: ;;;\r\n\r\nv\r\n--XYZ--",
		"preamble\r\n--XYZ\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--XYZ--",
	} {
		require.NotPanics(t, func() { _, _ = SummarizeMultipart(ct, []byte(body)) }, "body %q", body)
	}
	summary, ok := SummarizeMultipart(ct, []byte("preamble\r\n--XYZ\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--XYZ--"))
	require.True(t, ok)
	require.JSONEq(t, `{"fields":{"a":"1"}}`, string(summary))
}

// BenchmarkSummarizeMultipart records the request-path cost of logging a multipart passthrough
// upload. It runs in PreLLMHook only when content logging stores content, and scans the body once.
func BenchmarkSummarizeMultipart(b *testing.B) {
	contentType, body := multipartBody(b, [][2]string{{"model", "whisper-1"}, {"language", "en"}}, map[string][]byte{"file": bytes.Repeat([]byte{0xab}, 1<<20)})
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		_, _ = SummarizeMultipart(contentType, body)
	}
}

func rawMultipart(boundary string, parts ...string) []byte {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("--" + boundary + "\r\n" + p + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// TestSummarizeMultipartRejectsInvalidDisposition pins that a part whose Content-Disposition does
// not parse makes the whole body unreadable as a form, so no summary is logged rather than one that
// silently misses a part.
func TestSummarizeMultipartRejectsInvalidDisposition(t *testing.T) {
	body := rawMultipart("XYZ",
		"Content-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1",
		"Content-Disposition: ;;;\r\n\r\nmystery",
	)
	_, ok := SummarizeMultipart("multipart/form-data; boundary=XYZ", body)
	require.False(t, ok)
}

// TestSummarizeMultipartNonTextPartWithoutFilename pins that a part sent without a filename but with
// a binary Content-Type is recorded as a file (name, type, size), never copied into fields; parts
// without a Content-Type or with a textual one stay fields.
func TestSummarizeMultipartNonTextPartWithoutFilename(t *testing.T) {
	audio := "\x00\xffID3 not text"
	body := rawMultipart("XYZ",
		"Content-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1",
		"Content-Disposition: form-data; name=\"notes\"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello",
		"Content-Disposition: form-data; name=\"meta\"\r\nContent-Type: application/json\r\n\r\n{\"a\":1}",
		"Content-Disposition: form-data; name=\"file\"\r\nContent-Type: audio/mpeg\r\n\r\n"+audio,
	)
	summary, ok := SummarizeMultipart("multipart/form-data; boundary=XYZ", body)
	require.True(t, ok)
	require.NotContains(t, string(summary), "ID3", "binary part content must never be stored")
	var decoded MultipartSummary
	require.NoError(t, sonic.Unmarshal(summary, &decoded))
	require.Equal(t, map[string]any{"model": "whisper-1", "notes": "hello", "meta": `{"a":1}`}, decoded.Fields)
	require.Equal(t, []MultipartFile{{Field: "file", ContentType: "audio/mpeg", Bytes: int64(len(audio))}}, decoded.Files)
}

// TestSummarizeMultipartBoundsTheWholeSummary pins the summary-wide cap: past 256 KiB of field text
// or 1,000 parts, further text values are left out and the summary is marked truncated, keeping
// what was captured; file parts are still counted.
func TestSummarizeMultipartBoundsTheWholeSummary(t *testing.T) {
	big := strings.Repeat("x", 60<<10)
	var parts []string
	for i := 0; i < 6; i++ {
		parts = append(parts, fmt.Sprintf("Content-Disposition: form-data; name=\"f%d\"\r\n\r\n%s", i, big))
	}
	parts = append(parts, "Content-Disposition: form-data; name=\"file\"; filename=\"a.bin\"\r\n\r\nabc")
	summary, ok := SummarizeMultipart("multipart/form-data; boundary=XYZ", rawMultipart("XYZ", parts...))
	require.True(t, ok)
	var decoded MultipartSummary
	require.NoError(t, sonic.Unmarshal(summary, &decoded))
	require.True(t, decoded.Truncated)
	require.Len(t, decoded.Fields, 4, "four 60 KiB fields fit under 256 KiB, the fifth does not")
	require.Len(t, decoded.Files, 1, "file parts are still counted past the text cap")

	parts = parts[:0]
	for i := 0; i < 1500; i++ {
		parts = append(parts, fmt.Sprintf("Content-Disposition: form-data; name=\"k%d\"\r\n\r\nv", i))
	}
	summary, ok = SummarizeMultipart("multipart/form-data; boundary=XYZ", rawMultipart("XYZ", parts...))
	require.True(t, ok)
	decoded = MultipartSummary{}
	require.NoError(t, sonic.Unmarshal(summary, &decoded))
	require.True(t, decoded.Truncated)
	require.Len(t, decoded.Fields, 1000)

	small, ok := SummarizeMultipart("multipart/form-data; boundary=XYZ", rawMultipart("XYZ", "Content-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1"))
	require.True(t, ok)
	require.NotContains(t, string(small), "truncated")
}

// TestSummarizeMultipartFlagsACutField pins that a single field longer than the per-field cap is
// kept at the cap and the summary is marked truncated, so a reader knows the value was cut.
func TestSummarizeMultipartFlagsACutField(t *testing.T) {
	long := strings.Repeat("p", 100<<10)
	summary, ok := SummarizeMultipart("multipart/form-data; boundary=XYZ", rawMultipart("XYZ",
		"Content-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1",
		"Content-Disposition: form-data; name=\"prompt\"\r\n\r\n"+long,
	))
	require.True(t, ok)
	var decoded MultipartSummary
	require.NoError(t, sonic.Unmarshal(summary, &decoded))
	require.True(t, decoded.Truncated)
	require.Len(t, decoded.Fields["prompt"], maxMultipartFieldBytes)
	require.Equal(t, "whisper-1", decoded.Fields["model"])
}

// TestSummarizeMultipartFormMatchesBodySummary asserts a streamed upload, summarized from its
// parsed form, logs the same summary as the same upload summarized from its bytes.
func TestSummarizeMultipartFormMatchesBodySummary(t *testing.T) {
	audio := bytes.Repeat([]byte{0x00, 0xff, 0x10}, 1000)
	contentType, body := multipartBody(t, [][2]string{
		{"language", "en"}, {"model", "whisper-1"},
		{"timestamp_granularities[]", "word"}, {"timestamp_granularities[]", "segment"},
	}, map[string][]byte{"file": audio})
	fromBody, ok := SummarizeMultipart(contentType, body)
	require.True(t, ok)

	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(1024)
	require.NoError(t, err)
	defer form.RemoveAll()
	fromForm, ok := SummarizeMultipartForm(form, nil)
	require.True(t, ok)
	require.JSONEq(t, string(fromBody), string(fromForm))
	require.False(t, bytes.Contains(fromForm, audio[:30]), "file bytes must never be kept")

	_, ok = SummarizeMultipartForm(nil, nil)
	require.False(t, ok)
}

// TestSummarizeMultipartFormKeepsBinaryValuesOut asserts a part without a filename whose content is
// not text (Go's form reader files every such part under Value, whatever its Content-Type) is
// recorded like a file, by field and size, never as text in the log summary, matching what the
// byte-based summary does for a non-textual part.
func TestSummarizeMultipartFormKeepsBinaryValuesOut(t *testing.T) {
	binary := "\x00\x01\x02PNG\xff\xfe"
	form := &multipart.Form{Value: map[string][]string{"prompt": {"a red fox"}, "mask": {binary}}}
	summary, ok := SummarizeMultipartForm(form, nil)
	require.True(t, ok)
	var decoded MultipartSummary
	require.NoError(t, sonic.Unmarshal(summary, &decoded))
	require.Equal(t, "a red fox", decoded.Fields["prompt"])
	require.NotContains(t, decoded.Fields, "mask", "binary value logged as text: %s", summary)
	require.Equal(t, []MultipartFile{{Field: "mask", Bytes: int64(len(binary))}}, decoded.Files)
	require.False(t, bytes.Contains(summary, []byte("PNG")), "binary bytes reached the summary: %s", summary)
}

// TestReadMultipartFormKeepsValueTypes asserts a streamed upload is summarized by the same rule as
// its bytes: a part without a filename whose Content-Type is not textual is recorded like a file,
// even when its content is printable text, which the form alone cannot tell apart from a field.
// ReadForm drops that Content-Type, so ReadMultipartForm and MultipartValueTypes report it.
func TestReadMultipartFormKeepsValueTypes(t *testing.T) {
	secret := "SECRET-FILE-CONTENTS printable bytes of a file sent without a filename"
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "whisper-1"))
	part := func(header textproto.MIMEHeader, data string) {
		p, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = p.Write([]byte(data))
		require.NoError(t, err)
	}
	part(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="prompt"`}, "Content-Type": {"text/plain; charset=utf-8"}}, "a red fox")
	part(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="mask"`}, "Content-Type": {"application/octet-stream"}}, secret)
	part(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="a.wav"`}, "Content-Type": {"audio/wav"}}, "RIFF-audio")
	require.NoError(t, w.Close())
	body := buf.Bytes()

	form, types, err := ReadMultipartForm(bytes.NewReader(body), w.Boundary(), 1024)
	require.NoError(t, err)
	defer form.RemoveAll()
	summary, ok := SummarizeMultipartForm(form, types)
	require.True(t, ok)
	require.False(t, bytes.Contains(summary, []byte("SECRET-FILE-CONTENTS")), "file content sent without a filename reached the log summary: %s", summary)
	fromBody, ok := SummarizeMultipart(w.FormDataContentType(), body)
	require.True(t, ok)
	require.JSONEq(t, string(fromBody), string(summary), "a streamed upload must log what the same upload logs from its bytes")

	want := map[string][]string{"model": {""}, "prompt": {"text/plain; charset=utf-8"}, "mask": {"application/octet-stream"}}
	require.Equal(t, want, types)
	require.Equal(t, want, MultipartValueTypes(bytes.NewReader(body), w.Boundary()))
	require.Equal(t, []string{secret}, form.Value["mask"], "the value itself is still parsed for forwarding")
	f, err := form.File["file"][0].Open()
	require.NoError(t, err)
	defer f.Close()
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "RIFF-audio", string(got))

	// A malformed body returns ReadForm's error, and the header scan never holds it up.
	truncated := body[:len(body)/2]
	_, _, err = ReadMultipartForm(bytes.NewReader(truncated), w.Boundary(), 1024)
	require.Error(t, err)
	_, _, err = ReadMultipartForm(strings.NewReader("not multipart"), w.Boundary(), 1024)
	require.Error(t, err)
}

// BenchmarkReadMultipartForm compares ReadMultipartForm with the plain ReadForm the transport ran
// before for a passthrough upload (a few fields and a 1 MB file spilled to a temp file, with
// fasthttp's 8 KB in-memory limit): the difference is the cost of reading the value types.
func BenchmarkReadMultipartForm(b *testing.B) {
	contentType, body := multipartBody(b, [][2]string{{"model", "whisper-1"}, {"language", "en"}, {"response_format", "json"}},
		map[string][]byte{"file": bytes.Repeat([]byte{0x00, 0xff, 0x10, 0x42}, 1<<18)})
	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(b, err)
	boundary := params["boundary"]
	b.Run("ReadForm", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for b.Loop() {
			form, err := multipart.NewReader(bytes.NewReader(body), boundary).ReadForm(8 << 10)
			if err != nil {
				b.Fatal(err)
			}
			form.RemoveAll()
		}
	})
	b.Run("ReadMultipartForm", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for b.Loop() {
			form, _, err := ReadMultipartForm(bytes.NewReader(body), boundary, 8<<10)
			if err != nil {
				b.Fatal(err)
			}
			form.RemoveAll()
		}
	})
}
