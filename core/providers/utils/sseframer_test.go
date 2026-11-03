package utils

import (
	"bufio"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestSSEFramerAcrossChunks(t *testing.T) {
	var f SSEFramer
	write := func(chunk string) [][]byte {
		t.Helper()
		events, err := f.Write([]byte(chunk))
		assert.NoError(t, err)
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

// An event past the native scanner's ceiling fails with bufio.ErrTooLong, as that scanner fails an
// LLM stream, and the events completed before it are still returned.
func TestSSEFramerFailsOversizedEvent(t *testing.T) {
	var f SSEFramer
	events, err := f.Write([]byte("data: {\"usage\":1}\n\ndata: " + strings.Repeat("x", sseMaxBufSize+1)))
	assert.ErrorIs(t, err, bufio.ErrTooLong)
	assert.Equal(t, []string{`{"usage":1}`}, toStrings(events))
	assert.Equal(t, []string{`{"usage":1}`}, toStrings(SplitSSEPayloads([]byte("data: {\"usage\":1}\n\ndata: "+strings.Repeat("x", sseMaxBufSize+1)))), "the preview keeps the events read before it")
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
