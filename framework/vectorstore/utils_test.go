package vectorstore

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBoundedPageLimit covers the conversion the Qdrant and Pinecone clients
// need. The interesting cases are the ones a plain uint32(limit) got wrong:
// nothing bounds the caller's limit, so anything above MaxUint32 used to wrap
// to an unrelated page size, and an exact multiple of 2^32 wrapped to zero.
func TestBoundedPageLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int64
		want  uint32
	}{
		{name: "in range", limit: 250, want: 250},
		{name: "zero falls back", limit: 0, want: 100},
		{name: "negative falls back", limit: -1, want: 100},
		{name: "at the ceiling", limit: math.MaxUint32, want: math.MaxUint32},
		// Truncation used to turn this into a 4-row page.
		{name: "just past the ceiling", limit: int64(math.MaxUint32) + 5, want: math.MaxUint32},
		// The worst case: uint32(1<<32) is 0, which scrolls nothing while still
		// reading as a filled page to the caller's cursor logic.
		{name: "exact multiple of 2^32", limit: 1 << 32, want: math.MaxUint32},
		{name: "max int64", limit: math.MaxInt64, want: math.MaxUint32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, boundedPageLimit(tt.limit, 100))
		})
	}

	// The fallback is per call site: GetNearest defaults to a smaller page than
	// GetAll, and only a non-positive limit may reach it.
	assert.Equal(t, uint32(10), boundedPageLimit(0, 10))
	assert.Equal(t, uint32(7), boundedPageLimit(7, 10))
}
