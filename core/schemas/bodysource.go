package schemas

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"slices"
	"strings"
)

// BodySource is a read-only handle to a request body or uploaded file that may be too large to
// copy. It lets a body pass through the gateway as a pointer or a stream instead of a []byte
// copied at every layer.
//
// Open returns a new reader each time it is called, so a retry can send the body again. The
// caller must Close every reader it opens. Size is the length in bytes, or -1 when it is not
// known before reading.
//
// A BodySource built from an HTTP request is valid only while that request is being served: it
// may read the request's temp files, which are deleted when the request ends. Do not keep it, or
// a reader opened from it, after the request returns.
type BodySource interface {
	Size() int64
	Open() (io.ReadCloser, error)
}

// NewBytesBodySource wraps bytes that are already in memory. It does not copy them, so the
// caller must not change b while the source is in use.
func NewBytesBodySource(b []byte) BodySource {
	return &bytesBodySource{b: b}
}

type bytesBodySource struct {
	b []byte
}

func (s *bytesBodySource) Size() int64 { return int64(len(s.b)) }

func (s *bytesBodySource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.b)), nil
}

// NewMultipartFileBodySource exposes one file of a parsed multipart form. A file larger than the
// parser's memory limit stays in its temp file and is read from disk on each Open.
func NewMultipartFileBodySource(fh *multipart.FileHeader) BodySource {
	return multipartFileBodySource{fh: fh}
}

type multipartFileBodySource struct {
	fh *multipart.FileHeader
}

func (s multipartFileBodySource) Size() int64 { return s.fh.Size }

func (s multipartFileBodySource) Open() (io.ReadCloser, error) {
	return s.fh.Open()
}

// ReadBodySource reads the whole source into memory, for callers that need the bytes. When the
// size is known it allocates exactly once.
func ReadBodySource(src BodySource) ([]byte, error) {
	r, err := src.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	size := src.Size()
	if size < 0 {
		return io.ReadAll(r)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, fmt.Errorf("read body source: %w", err)
	}
	return b, nil
}

// NewMultipartFormBodySource streams a parsed multipart form as a multipart/form-data body with
// the given boundary. Only the fields and the part headers are held in memory; each file is read
// from its source (memory or temp file) on Open. Size is exact, so the body can be sent with a
// Content-Length. Fields and files are written in name order, so the bytes are the same on every
// Open. The form must stay valid (its temp files not removed) while the source is in use.
//
// valueTypes, when set, is the Content-Type each form.Value part arrived with, by name and index
// (the parsed form has lost it). A part with a type is written with it; a name whose list does not
// line up with its values, or a part without a type, is written as a plain field.
func NewMultipartFormBodySource(form *multipart.Form, boundary string, valueTypes map[string][]string) (BodySource, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		return nil, fmt.Errorf("invalid multipart boundary: %w", err)
	}
	for _, name := range sortedFormKeys(form.Value) {
		values := form.Value[name]
		types := valueTypes[name]
		if len(types) != len(values) {
			types = nil
		}
		for i, v := range values {
			var err error
			if types != nil && types[i] != "" {
				var part io.Writer
				part, err = w.CreatePart(textproto.MIMEHeader{
					"Content-Disposition": {`form-data; name="` + formFieldNameEscaper.Replace(name) + `"`},
					"Content-Type":        {types[i]},
				})
				if err == nil {
					_, err = io.WriteString(part, v)
				}
			} else {
				err = w.WriteField(name, v)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to write %s field: %w", name, err)
			}
		}
	}
	// Each file's content goes right after its part header: cut the buffer there, and stream the
	// file between the cuts.
	var cuts []int
	var files []*multipart.FileHeader
	size := int64(0)
	for _, name := range sortedFormKeys(form.File) {
		for _, fh := range form.File[name] {
			if _, err := w.CreatePart(fh.Header); err != nil {
				return nil, fmt.Errorf("failed to write %s file part header: %w", name, err)
			}
			cuts = append(cuts, buf.Len())
			files = append(files, fh)
			size += fh.Size
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}
	all := buf.Bytes()
	segments := make([][]byte, 0, len(cuts)+1)
	start := 0
	for _, cut := range cuts {
		segments = append(segments, all[start:cut])
		start = cut
	}
	segments = append(segments, all[start:])
	return &multipartFormBodySource{segments: segments, files: files, size: size + int64(len(all))}, nil
}

// multipartFormBodySource is segments[0], files[0], segments[1], files[1], ..., segments[n].
type multipartFormBodySource struct {
	segments [][]byte
	files    []*multipart.FileHeader
	size     int64
}

func (s *multipartFormBodySource) Size() int64 { return s.size }

// Open returns a reader over the form. Each file part is opened only when the reader reaches it
// and closed when its segment ends, so the stream holds at most one file descriptor at a time
// however many disk-backed parts the form has. An error opening a file is returned from Read.
func (s *multipartFormBodySource) Open() (io.ReadCloser, error) {
	return &multipartFormReader{src: s}, nil
}

// multipartFormReader reads the pieces of a multipartFormBodySource in order: segments[0],
// files[0], segments[1], files[1], ..., segments[n]. Even piece indexes are segments, odd ones
// files.
type multipartFormReader struct {
	src   *multipartFormBodySource
	piece int
	cur   io.Reader
	file  io.Closer // the open file behind cur, if cur is a file part
}

func (r *multipartFormReader) Read(p []byte) (int, error) {
	pieces := len(r.src.segments) + len(r.src.files)
	for {
		if r.cur == nil {
			if r.piece >= pieces {
				return 0, io.EOF
			}
			if r.piece%2 == 0 {
				r.cur = bytes.NewReader(r.src.segments[r.piece/2])
			} else {
				fh := r.src.files[r.piece/2]
				f, err := fh.Open()
				if err != nil {
					return 0, fmt.Errorf("failed to open %s: %w", fh.Filename, err)
				}
				r.cur, r.file = f, f
			}
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			if closeErr := r.closeFile(); closeErr != nil {
				return n, closeErr
			}
			r.cur = nil
			r.piece++
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

// Close closes the file part being read, if any.
func (r *multipartFormReader) Close() error {
	r.cur = nil
	r.piece = len(r.src.segments) + len(r.src.files)
	return r.closeFile()
}

func (r *multipartFormReader) closeFile() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// formFieldNameEscaper escapes a field name inside a quoted Content-Disposition parameter, as
// multipart.Writer.WriteField does.
var formFieldNameEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// sortedFormKeys returns a form map's keys in order.
func sortedFormKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
