package utils

import (
	"bytes"
	"unicode/utf8"
	"unsafe"
)

// SSEFramer splits a byte stream into SSE events incrementally, for relays that forward a body
// unchanged and only observe its events. Each Write returns the data payloads of the events it
// completed. An event is held until its delimiter arrives, up to sseMaxBufSize, the ceiling the
// native SSE scanner and passthrough streams use: one that grows past it is skipped, as a
// passthrough stream drops it, and framing resumes after its delimiter. The bytes are still
// forwarded by the caller; only the usage the skipped event carried goes unread, and Skipped counts
// it. What it retains is the one event in progress, at most the ceiling plus one write.
type SSEFramer struct {
	pending  bytes.Buffer
	scanned  int  // bytes of pending the delimiter search already covered without a match
	skipping bool // the event in progress passed the ceiling; its bytes are dropped up to its delimiter
	skipped  int  // events skipped for passing the ceiling
}

// sseResumeOverlap is how far before the end of the bytes already searched a resumed search starts:
// one byte short of the longest delimiter (CRLF CRLF), so a delimiter split across writes is found.
const sseResumeOverlap = len("\r\n\r\n") - 1

// Write appends chunk and returns the data payload of every event it completed. The returned
// slices are copies and stay valid after the next call. The delimiter search resumes where the
// last one stopped, so an event fed in many writes is scanned once, not once per write.
func (f *SSEFramer) Write(chunk []byte) [][]byte {
	f.pending.Write(chunk)
	var out [][]byte
	for {
		data := f.pending.Bytes()
		// No delimiter starts before from: it would have ended inside the bytes already searched,
		// and been found there. So each delimiter's first match in data[from:] is its first in data,
		// and the result is the one a search of the whole of data returns.
		from := max(f.scanned-sseResumeOverlap, 0)
		idx, delimLen := findFirstSSEFrameDelimiter(data[from:])
		if idx < 0 {
			if f.skipping || len(data) > sseMaxBufSize {
				f.skipEventInProgress(data)
			} else {
				f.scanned = len(data)
			}
			break
		}
		idx += from
		switch {
		case f.skipping:
			f.skipping = false // the delimiter ends the skipped event; the next one is framed
		case idx > sseMaxBufSize:
			// Delimited within one write but past the ceiling all the same: skipped, so the outcome
			// does not depend on how the upstream's bytes were split into writes.
			f.skipped++
		default:
			if p := extractSSEDataPayload(data[:idx]); isSSEEventPayload(p) {
				out = append(out, append([]byte(nil), p...))
			}
		}
		f.pending.Next(idx + delimLen)
		f.scanned = 0
	}
	return out
}

// skipEventInProgress drops the held bytes of an event past the ceiling, keeping only the tail a
// delimiter split across writes could start in, so a skipped event holds a few bytes, not its size.
func (f *SSEFramer) skipEventInProgress(data []byte) {
	if !f.skipping {
		f.skipping = true
		f.skipped++
	}
	var tail [sseResumeOverlap]byte
	n := copy(tail[:], data[len(data)-min(len(data), sseResumeOverlap):])
	f.pending.Reset()
	f.pending.Write(tail[:n])
	f.scanned = n
}

// Skipped reports how many events passed the ceiling and were skipped.
func (f *SSEFramer) Skipped() int { return f.skipped }

// Flush returns the payload of a trailing event the stream ended without delimiting, if any.
func (f *SSEFramer) Flush() []byte {
	defer func() {
		f.pending.Reset()
		f.scanned = 0
		f.skipping = false
	}()
	if f.skipping {
		return nil // the tail of a skipped event
	}
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
