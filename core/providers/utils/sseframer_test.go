package utils

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSEFramerAcrossChunks(t *testing.T) {
	var f SSEFramer
	write := func(chunk string) [][]byte {
		t.Helper()
		events := f.Write([]byte(chunk))
		return events
	}
	assert.Empty(t, write("data: {\"a\":"))
	events := write("1}\n\ndata: {\"b\":2}\r\n\r\n: comment\n\ndata: {\"c\"")
	assert.Equal(t, []string{`{"a":1}`, `{"b":2}`}, toStrings(events), "comment-only frames carry no payload")
	assert.Empty(t, write(":3}"))
	assert.Equal(t, `{"c":3}`, string(f.Flush()), "a trailing event without its delimiter is flushed")
	assert.Nil(t, f.Flush())

	assert.Equal(t, []string{"x", "y"}, toStrings(SplitSSEPayloads([]byte("data: x\n\ndata: y"))))
}

// A relay never fails a stream for its size: an event past the native scanner's ceiling is held
// until its delimiter arrives and delivered whole, so its usage is read; the preview reads it too.
func TestSSEFramerSkipsEventPastScannerCeiling(t *testing.T) {
	big := "{\"usage\":2,\"pad\":\"" + strings.Repeat("x", sseMaxBufSize+(1<<20)) + "\"}"
	stream := "data: {\"usage\":1}\n\ndata: " + big + "\n\ndata: {\"usage\":3}\n\n"
	var f SSEFramer
	var events [][]byte
	held := 0
	for i := 0; i < len(stream); i += 64 << 10 {
		got := f.Write([]byte(stream[i:min(i+64<<10, len(stream))]))
		events = append(events, got...)
		held = max(held, f.pending.Len())
	}
	assert.Equal(t, []string{`{"usage":1}`, `{"usage":3}`}, toStrings(events), "the oversized event is skipped and framing carries on after it")
	assert.Equal(t, 1, f.Skipped())
	assert.LessOrEqual(t, held, sseMaxBufSize+(64<<10), "the framer holds no more than the ceiling and one write")
	assert.Nil(t, f.Flush())
	assert.Equal(t, []string{`{"usage":1}`, `{"usage":3}`}, toStrings(SplitSSEPayloads([]byte(stream))))

	t.Run("an oversized event the stream ends without delimiting is not flushed", func(t *testing.T) {
		var g SSEFramer
		assert.Empty(t, g.Write([]byte("data: "+big)))
		assert.Nil(t, g.Flush())
		assert.Equal(t, 1, g.Skipped())
	})
}

// The framer resumes its delimiter search where the last write left off; whatever the split of the
// stream into writes, it must frame exactly as framing the whole body at once does, a delimiter
// split across two writes (either kind) included.
func TestSSEFramerIncrementalScanMatchesWholeBody(t *testing.T) {
	body := "data: a\n\ndata: b\r\n\r\n: ping\n\ndata: c\r\n\ndata: d\n\r\n\r\nid: 1\ndata: e\r\n\r\n\n\ndata: f\r\r\n\ndata: g"
	var whole SSEFramer
	want := whole.Write([]byte(body))
	tail := whole.Flush()
	require.NotEmpty(t, want)
	for size := 1; size <= len(body); size++ {
		var f SSEFramer
		var got [][]byte
		for i := 0; i < len(body); i += size {
			events := f.Write([]byte(body[i:min(i+size, len(body))]))
			got = append(got, events...)
		}
		assert.Equal(t, toStrings(want), toStrings(got), "writes of %d bytes", size)
		assert.Equal(t, string(tail), string(f.Flush()), "writes of %d bytes", size)
	}
}

// BenchmarkSSEFramerLargeEvent feeds one 9 MiB event in 4 KiB writes, the shape that made the
// framer rescan its whole pending buffer on every write.
func BenchmarkSSEFramerLargeEvent(b *testing.B) {
	stream := []byte("data: {\"usage\":1,\"pad\":\"" + strings.Repeat("x", 9<<20) + "\"}\n\n")
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		var f SSEFramer
		n := 0
		for i := 0; i < len(stream); i += 4 << 10 {
			events := f.Write(stream[i:min(i+4<<10, len(stream))])
			n += len(events)
		}
		if n != 1 {
			b.Fatalf("framed %d events, want 1", n)
		}
	}
}

func TestRuneCounterMatchesBufferedCount(t *testing.T) {
	body := []byte("héllo wörld 日本語 🎉 \xff tail")
	for split := 1; split <= 5; split++ {
		var c RuneCounter
		for i := 0; i < len(body); i += split {
			end := min(i+split, len(body))
			c.Write(body[i:end])
		}
		assert.Equal(t, int64(utf8.RuneCount(body)), c.Total(), "chunks of %d bytes", split)
	}
	var truncated RuneCounter
	truncated.Write([]byte("ab\xe6\x97"))
	assert.Equal(t, int64(utf8.RuneCount([]byte("ab\xe6\x97"))), truncated.Total(), "an unfinished sequence counts byte-wise")
	assert.Equal(t, utf8.RuneCountInString(strings.Repeat("é", 10)), RuneCount([]byte(strings.Repeat("é", 10))))
}

func toStrings(in [][]byte) []string {
	out := make([]string, 0, len(in))
	for _, b := range in {
		out = append(out, string(b))
	}
	return out
}
