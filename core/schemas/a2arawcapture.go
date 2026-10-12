package schemas

import (
	"context"
	"encoding/base64"
	"sync"
)

type a2aRawCaptureKey uint8

// A2ARawCaptureContextKey carries a lookup handle across transports and logging.
const A2ARawCaptureContextKey a2aRawCaptureKey = 0

// A2ARawCaptureManager owns transport buffers until the operation's post-hooks finish.
type A2ARawCaptureManager struct {
	captures sync.Map // Capture ID -> *A2ARawCapture
}

// A2ARawCaptureHandle identifies manager-owned buffers without retaining them.
type A2ARawCaptureHandle struct {
	manager   *A2ARawCaptureManager
	captureID string
}

// Start allocates capture state for one operation.
func (m *A2ARawCaptureManager) Start(captureID string) A2ARawCaptureHandle {
	m.captures.Store(captureID, &A2ARawCapture{})
	return A2ARawCaptureHandle{manager: m, captureID: captureID}
}

// Delete releases the manager's ownership after all capture consumers finish.
func (m *A2ARawCaptureManager) Delete(captureID string) {
	m.captures.Delete(captureID)
}

// GetA2ARawCapture resolves the operation's handle for transport or logging use.
func GetA2ARawCapture(ctx context.Context) *A2ARawCapture {
	if ctx == nil {
		return nil
	}
	handle, ok := ctx.Value(A2ARawCaptureContextKey).(A2ARawCaptureHandle)
	if !ok || handle.manager == nil {
		return nil
	}
	capture, _ := handle.manager.captures.Load(handle.captureID)
	result, _ := capture.(*A2ARawCapture)
	return result
}

// A2ARawCapture bounds each direction to 1 MiB. Oversized directions are dropped
// entirely instead of presenting a truncated body as an exact capture.
type A2ARawCapture struct {
	mu                                    sync.Mutex
	request, response                     []byte
	requestDropped, responseDropped       bool
	requestIncomplete, responseIncomplete bool
}

// Reset starts a new upstream attempt so retries do not concatenate unrelated bodies.
func (c *A2ARawCapture) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.request, c.response = nil, nil
	c.requestDropped, c.responseDropped = false, false
	c.requestIncomplete, c.responseIncomplete = false, false
}

// Append copies observed transport bytes without retaining pooled transport buffers.
func (c *A2ARawCapture) Append(data []byte, request bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf, dropped := &c.response, &c.responseDropped
	if request {
		buf, dropped = &c.request, &c.requestDropped
	}
	if *dropped {
		return
	}
	if len(*buf)+len(data) > 1<<20 {
		*buf = nil
		*dropped = true
		return
	}
	*buf = append(*buf, data...)
}

// SetComplete marks whether the observed direction reached its transport body boundary.
func (c *A2ARawCapture) SetComplete(request, complete bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if request {
		c.requestIncomplete = !complete
	} else {
		c.responseIncomplete = !complete
	}
}

// Snapshot returns complete, size-bounded bodies as lossless base64 strings.
func (c *A2ARawCapture) Snapshot() (request, response *string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request != nil && !c.requestIncomplete {
		value := base64.StdEncoding.EncodeToString(c.request)
		request = &value
	}
	if c.response != nil && !c.responseIncomplete {
		value := base64.StdEncoding.EncodeToString(c.response)
		response = &value
	}
	return
}
