package schemas

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
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
