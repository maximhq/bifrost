package schemas

import (
	"bytes"
	"sync"
)

// bufferPool provides a pool for bytes.Buffer objects.
var bufferPool = sync.Pool{
	New: func() interface{} {
		return &bytes.Buffer{}
	},
}

// AcquireBuffer gets a buffer from the pool and resets it.
func AcquireBuffer() *bytes.Buffer {
	return bufferPool.Get().(*bytes.Buffer)
}

// ReleaseBuffer returns a buffer to the pool after resetting it.
func ReleaseBuffer(b *bytes.Buffer) {
	if b == nil {
		return
	}
	b.Reset()
	bufferPool.Put(b)
}
