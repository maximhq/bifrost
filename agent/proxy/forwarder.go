package proxy

import (
	"io"
	"net/http"
	"time"
)

// Forwarder sends rewritten requests to the Bifrost gateway and relays
// responses back to the original client. It uses persistent connections
// for performance and handles SSE streaming by flushing chunks immediately.
type Forwarder struct {
	client *http.Client
}

// NewForwarder creates a gateway forwarder with connection pooling.
func NewForwarder() *Forwarder {
	return &Forwarder{
		client: &http.Client{
			// No timeout — streaming responses (SSE) can be long-lived.
			// Individual request timeouts should be set per-request if needed.
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
				// Allow HTTP/2 for gateway connection
				ForceAttemptHTTP2: true,
			},
		},
	}
}

// Forward sends the rewritten request to the gateway and writes the response
// back to the client response writer. Handles streaming by flushing as data
// arrives.
func (f *Forwarder) Forward(w http.ResponseWriter, req *http.Request) {
	// Send the request to the gateway
	resp, err := f.client.Do(req)
	if err != nil {
		http.Error(w, "gateway error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	// Set the status code
	w.WriteHeader(resp.StatusCode)

	// Stream the response body, flushing after each chunk for SSE support
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 32*1024) // 32KB chunks
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				// Log but don't propagate — we've already started writing the response
			}
			return
		}
	}
}

// ForwardRoundTrip sends the rewritten request and returns the raw response.
// The caller is responsible for reading and closing the response body.
// This is used by the MITM proxy which handles its own response writing.
func (f *Forwarder) ForwardRoundTrip(req *http.Request) (*http.Response, error) {
	return f.client.Do(req)
}
