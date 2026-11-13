package utils

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// MultipartField is one plain form field of a multipart upload body.
type MultipartField struct {
	Name  string
	Value string
}

// MultipartFileUpload is a multipart/form-data body made of plain fields followed by one file,
// ready to send as a request body stream. Only the small head (fields and the file part's
// header) and tail (closing boundary) are held in memory; the file is read from its source
// while the request is written, so it is never copied into a buffer.
type MultipartFileUpload struct {
	Body        io.ReadCloser // head, then the file, then the tail; the caller must Close it
	Size        int           // exact body length, for Content-Length
	ContentType string        // multipart/form-data with the boundary
}

// NewMultipartFileUpload builds a streamed multipart body. The file part is written as
// CreateFormFile writes it, so the bytes match a body built in memory with multipart.Writer.
func NewMultipartFileUpload(fields []MultipartField, fileField, filename string, file schemas.BodySource) (*MultipartFileUpload, error) {
	size := file.Size()
	if size < 0 {
		return nil, fmt.Errorf("file size must be known to stream a multipart upload")
	}
	var head bytes.Buffer
	writer := multipart.NewWriter(&head)
	for _, f := range fields {
		if err := writer.WriteField(f.Name, f.Value); err != nil {
			return nil, fmt.Errorf("failed to write %s field: %w", f.Name, err)
		}
	}
	if _, err := writer.CreateFormFile(fileField, filename); err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	headLen := head.Len()
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}
	// Close appended the closing boundary to the same buffer; split it off as the tail.
	all := head.Bytes()
	headBytes, tail := all[:headLen], all[headLen:]

	fileReader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open file content: %w", err)
	}
	return &MultipartFileUpload{
		Body: multipartUploadReader{
			Reader: io.MultiReader(bytes.NewReader(headBytes), fileReader, bytes.NewReader(tail)),
			file:   fileReader,
		},
		Size:        len(headBytes) + int(size) + len(tail),
		ContentType: writer.FormDataContentType(),
	}, nil
}

// FileUploadSource returns the request's file as a BodySource: FileSource when the transport
// passed the upload as a handle, else the in-memory File bytes (Go SDK callers), without copying.
func FileUploadSource(request *schemas.BifrostFileUploadRequest) schemas.BodySource {
	if request.FileSource != nil {
		return request.FileSource
	}
	return schemas.NewBytesBodySource(request.File)
}

// multipartUploadReader closes the file reader when the body is closed.
type multipartUploadReader struct {
	io.Reader
	file io.Closer
}

func (r multipartUploadReader) Close() error { return r.file.Close() }
