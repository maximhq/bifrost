package utils

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// A rune split across chunks is counted once, invalid bytes count one each, and the total always
// matches a buffered utf8.RuneCount of the same bytes, whatever the chunk boundaries.
func TestCountRunesAcrossChunks(t *testing.T) {
	body := []byte("h\xc3\xa9llo \xe2\x82\xac \xf0\x9f\x98\x80 \xff end")
	want := int64(utf8.RuneCount(body))
	for size := 1; size <= len(body); size++ {
		var carry [utf8.UTFMax]byte
		carryLen := 0
		var got int64
		for i := 0; i < len(body); i += size {
			got += countRunes(body[i:min(i+size, len(body))], &carry, &carryLen)
		}
		got += int64(utf8.RuneCount(carry[:carryLen]))
		assert.Equal(t, want, got, "chunk size %d", size)
	}
}

// The per-chunk cost of billing a stream by size: one add for bytes, a rune scan only when a route
// bills characters. Run with -benchmem; neither path allocates.
func BenchmarkCountRunes(b *testing.B) {
	b.Run("ascii_4k", func(b *testing.B) {
		ascii := make([]byte, 4096)
		for i := range ascii {
			ascii[i] = 'a'
		}
		var carry [utf8.UTFMax]byte
		carryLen := 0
		b.SetBytes(int64(len(ascii)))
		b.ReportAllocs()
		for b.Loop() {
			countRunes(ascii, &carry, &carryLen)
		}
	})
	b.Run("multibyte_4k", func(b *testing.B) {
		chunk := []byte(strings.Repeat("héllo wörld € ", 4096/len("héllo wörld € ")))
		var carry [utf8.UTFMax]byte
		carryLen := 0
		b.SetBytes(int64(len(chunk)))
		b.ReportAllocs()
		for b.Loop() {
			countRunes(chunk, &carry, &carryLen)
		}
	})
}
