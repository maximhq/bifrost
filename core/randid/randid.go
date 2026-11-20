// Package randid hands out cryptographically random bytes and version 4 UUIDs for IDs minted on
// the request path. The bytes come from crypto/rand, read a batch at a time, so an ID costs a copy
// out of a buffer instead of one read of the system random source, which concurrent callers
// contend on.
package randid

import (
	cryptorand "crypto/rand"
	"sync"

	"github.com/google/uuid"
)

// batchSize is how many bytes one read of crypto/rand fetches into a buffer: 256 UUIDs.
const batchSize = 4096

// buffer is one batch of random bytes. left counts the unread bytes at the end of buf; zero means
// empty, so a new buffer reads crypto/rand on first use.
type buffer struct {
	left int
	buf  [batchSize]byte
}

// buffers holds the batches. sync.Pool serves each P from its own slot first, so callers on
// different cores rarely touch the same buffer, and a buffer has one holder between Get and Put.
// A buffer the garbage collector drops takes its unread bytes with it. It holds at most a buffer
// or two per P, about 4 KB each, freed when the pool is idle across garbage collections.
var buffers = sync.Pool{New: func() any { return new(buffer) }}

// Read fills p with cryptographically random bytes that no other caller receives. Like
// crypto/rand.Read, it ends the program if the system random source fails.
func Read(p []byte) {
	if len(p) > batchSize {
		_, _ = cryptorand.Read(p)
		return
	}
	b := buffers.Get().(*buffer)
	if b.left < len(p) {
		_, _ = cryptorand.Read(b.buf[:])
		b.left = batchSize
	}
	copy(p, b.buf[batchSize-b.left:])
	b.left -= len(p)
	buffers.Put(b)
}

// NewUUID returns a random (version 4) UUID, the same as uuid.New, built from bytes Read hands out.
func NewUUID() uuid.UUID {
	var u uuid.UUID
	Read(u[:])
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return u
}
