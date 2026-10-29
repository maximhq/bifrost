package utils

import (
	"bytes"
	"unicode/utf8"
	"unsafe"
)

// SSEFramer splits a byte stream into SSE events incrementally, for relays that forward a body
// unchanged and only observe its events. Each Write returns the data payloads of the events it
// completed. The buffer is bounded by the same ceiling as the native SSE scanner: an event that
// grows past it is dropped rather than buffered without limit.
type SSEFramer struct {
	pending bytes.Buffer
}

// Write appends chunk and returns the data payload of every event it completed. The returned
// slices are copies and stay valid after the next call.
func (f *SSEFramer) Write(chunk []byte) [][]byte {
	f.pending.Write(chunk)
	var out [][]byte
	for {
		data := f.pending.Bytes()
		idx, delimLen := findFirstSSEFrameDelimiter(data)
		if idx < 0 {
			break
		}
		if p := extractSSEDataPayload(data[:idx]); isSSEEventPayload(p) {
			out = append(out, append([]byte(nil), p...))
		}
		f.pending.Next(idx + delimLen)
	}
	if f.pending.Len() > sseMaxBufSize {
		f.pending.Reset()
	}
	return out
}

// Flush returns the payload of a trailing event the stream ended without delimiting, if any.
func (f *SSEFramer) Flush() []byte {
	defer f.pending.Reset()
	if p := extractSSEDataPayload(f.pending.Bytes()); isSSEEventPayload(p) {
		return append([]byte(nil), p...)
	}
	return nil
}

// isSSEEventPayload reports a payload worth observing: not empty, and not a comment-only frame
// (": keep-alive"), which extractSSEDataPayload hands back as-is when no data: line is present.
func isSSEEventPayload(p []byte) bool {
	p = bytes.TrimSpace(p)
	return len(p) > 0 && p[0] != ':'
}

// SplitSSEPayloads frames a buffered SSE body the way SSEFramer frames a live one and returns each
// event's data payload, a trailing event without its delimiter included. It serves config-time
// checks (the usage preview); relays frame incrementally.
func SplitSSEPayloads(body []byte) [][]byte {
	var f SSEFramer
	out := f.Write(body)
	if p := f.Flush(); p != nil {
		out = append(out, p)
	}
	return out
}

// RuneCounter counts the UTF-8 runes of a stream fed in chunks, carrying a sequence split across
// two chunks so it is counted once. Invalid bytes count one each, as utf8.RuneCount does, so the
// total matches a buffered RuneCount of the same body.
type RuneCounter struct {
	carry    [utf8.UTFMax]byte
	carryLen int
	runes    int64
}

// Write counts the complete runes in chunk.
func (c *RuneCounter) Write(chunk []byte) {
	// Finish the sequence carried from the last chunk the way a buffered decode would: bytes are
	// pulled in a few at a time and decoded rune by rune, so an invalid lead byte counts as one
	// rune on its own and the bytes after it are not swallowed with it.
	for c.carryLen > 0 && len(chunk) > 0 {
		take := min(utf8.UTFMax-c.carryLen, len(chunk))
		copy(c.carry[c.carryLen:], chunk[:take])
		c.carryLen += take
		chunk = chunk[take:]
		for c.carryLen > 0 && utf8.FullRune(c.carry[:c.carryLen]) {
			_, size := utf8.DecodeRune(c.carry[:c.carryLen])
			c.runes++
			copy(c.carry[:], c.carry[size:c.carryLen])
			c.carryLen -= size
		}
	}
	if c.carryLen > 0 {
		return // the chunk ended inside a rune
	}
	end := len(chunk)
	for k := len(chunk) - 1; k >= 0 && k >= len(chunk)-utf8.UTFMax; k-- {
		if utf8.RuneStart(chunk[k]) {
			if !utf8.FullRune(chunk[k:]) {
				end = k
			}
			break
		}
	}
	c.runes += int64(RuneCount(chunk[:end]))
	c.carryLen = copy(c.carry[:], chunk[end:])
}

// Total returns the runes counted so far, an unfinished trailing sequence counted byte-wise.
func (c *RuneCounter) Total() int64 {
	return c.runes + int64(RuneCount(c.carry[:c.carryLen]))
}

// RuneCount counts the UTF-8 runes in b exactly as utf8.RuneCount does, without the string copy
// utf8.RuneCount takes on its non-ASCII path (one allocation the size of the input, per call).
func RuneCount(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	return utf8.RuneCountInString(unsafe.String(unsafe.SliceData(b), len(b)))
}
