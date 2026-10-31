package agent

import (
	"io"

	"github.com/maximhq/bifrost/core/schemas"
)

type captureReadCloser struct {
	io.ReadCloser
	capture  *schemas.A2ARawCapture
	request  bool
	expected int64
	read     int64
	failed   bool
}

// Read observes HTTP body bytes before SDK parsing or error-stream reframing.
func (r *captureReadCloser) Read(p []byte) (int, error) {
	r.capture.SetComplete(r.request, false)
	n, err := r.ReadCloser.Read(p)
	r.read += int64(n)
	if err != nil && err != io.EOF {
		r.failed = true
	}
	if n > 0 {
		r.capture.Append(p[:n], r.request)
	}
	r.capture.SetComplete(r.request, !r.failed && (err == io.EOF || r.expected > 0 && r.read == r.expected))
	return n, err
}
