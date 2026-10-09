package anthropic

import (
	"strings"

	"github.com/bytedance/sonic"
)

// decodeAnthropicStreamEvent decodes one SSE data payload of a Messages stream
// into event, which must be zero.
//
// Nearly every event of a stream is a content_block_delta carrying one string
// (text, partial_json, thinking or signature), plus a handful of
// content_block_stop / ping / message_stop events. For those shapes a strict
// hand-written scan fills event directly: it reads only the keys type, index
// and delta, each at most once, with a delta holding type plus at most one
// value key, and it gives up — without touching event — on anything else
// (another key, a null, a number that is not a small non-negative integer, a
// \u escape, a control character, trailing data). Everything it gives up on,
// including every malformed payload, goes to sonic exactly as before, so the
// decoded value and the error for any payload are the ones sonic produces
// (pinned by TestDecodeAnthropicStreamEvent_MatchesSonic and the fuzz target
// next to it).
//
// The scan avoids sonic's per-call decoder setup and, on arm64, the 2 MiB
// parser buffers sonic's decoder pool refills after every GC; it also copies
// only the decoded string instead of the whole payload.
func decodeAnthropicStreamEvent(data []byte, event *AnthropicStreamEvent) error {
	if decodeAnthropicStreamEventFast(data, event) {
		return nil
	}
	return sonic.Unmarshal(data, event)
}

// streamDeltaBlock backs the pointers of one decoded content_block_delta in a
// single allocation: the event's Delta, its Index and the delta's one string
// field all point into it.
type streamDeltaBlock struct {
	delta AnthropicStreamDelta
	index int
	value string
}

// delta value keys the fast path decodes; any other delta key falls back.
const (
	deltaFieldNone = iota
	deltaFieldText
	deltaFieldPartialJSON
	deltaFieldThinking
	deltaFieldSignature
)

// fastStreamDelta is a delta object as the fast path scanned it.
type fastStreamDelta struct {
	typ      []byte
	hasType  bool
	field    int    // deltaField*
	value    []byte // raw contents of the value string
	valueEsc bool   // value holds escapes
}

// decodeAnthropicStreamEventFast reports whether it decoded data into event.
// On false it has not modified event.
func decodeAnthropicStreamEventFast(data []byte, event *AnthropicStreamEvent) bool {
	s := streamEventScanner{b: data}
	if !s.consume('{') {
		return false
	}
	var (
		typ               []byte
		index             int
		delta             fastStreamDelta
		hasType, hasIndex bool
		hasDelta          bool
	)
	if !s.consume('}') {
		for {
			key, esc, ok := s.str()
			if !ok || esc || !s.consume(':') {
				return false
			}
			switch string(key) {
			case "type":
				if hasType {
					return false
				}
				if typ, esc, ok = s.str(); !ok || esc {
					return false
				}
				hasType = true
			case "index":
				if hasIndex {
					return false
				}
				if index, ok = s.smallUint(); !ok {
					return false
				}
				hasIndex = true
			case "delta":
				if hasDelta || !s.delta(&delta) {
					return false
				}
				hasDelta = true
			default:
				return false
			}
			if s.consume(',') {
				continue
			}
			if s.consume('}') {
				break
			}
			return false
		}
	}
	if !s.atEnd() {
		return false
	}

	// Decoded; fill event. Every string is a constant or a copy, so event never
	// aliases data.
	if hasType {
		event.Type = streamEventTypeOf(typ)
	}
	if hasDelta {
		block := &streamDeltaBlock{}
		if delta.hasType {
			block.delta.Type = streamDeltaTypeOf(delta.typ)
		}
		if delta.field != deltaFieldNone {
			if delta.valueEsc {
				block.value = unescapeSimpleJSONString(delta.value)
			} else {
				block.value = string(delta.value)
			}
			switch delta.field {
			case deltaFieldText:
				block.delta.Text = &block.value
			case deltaFieldPartialJSON:
				block.delta.PartialJSON = &block.value
			case deltaFieldThinking:
				block.delta.Thinking = &block.value
			case deltaFieldSignature:
				block.delta.Signature = &block.value
			}
		}
		event.Delta = &block.delta
		if hasIndex {
			block.index = index
			event.Index = &block.index
		}
	} else if hasIndex {
		event.Index = new(int)
		*event.Index = index
	}
	return true
}

// streamEventTypeOf returns the event type named by b, as the package constant
// when it is a known one so the common case does not allocate.
func streamEventTypeOf(b []byte) AnthropicStreamEventType {
	switch string(b) {
	case string(AnthropicStreamEventTypeContentBlockDelta):
		return AnthropicStreamEventTypeContentBlockDelta
	case string(AnthropicStreamEventTypeContentBlockStop):
		return AnthropicStreamEventTypeContentBlockStop
	case string(AnthropicStreamEventTypePing):
		return AnthropicStreamEventTypePing
	case string(AnthropicStreamEventTypeMessageStop):
		return AnthropicStreamEventTypeMessageStop
	}
	return AnthropicStreamEventType(b)
}

// streamDeltaTypeOf is streamEventTypeOf for delta types.
func streamDeltaTypeOf(b []byte) AnthropicStreamDeltaType {
	switch string(b) {
	case string(AnthropicStreamDeltaTypeText):
		return AnthropicStreamDeltaTypeText
	case string(AnthropicStreamDeltaTypeInputJSON):
		return AnthropicStreamDeltaTypeInputJSON
	case string(AnthropicStreamDeltaTypeThinking):
		return AnthropicStreamDeltaTypeThinking
	case string(AnthropicStreamDeltaTypeSignature):
		return AnthropicStreamDeltaTypeSignature
	}
	return AnthropicStreamDeltaType(b)
}

// streamEventScanner is a cursor over one JSON payload for the fast path. Each
// method skips leading JSON whitespace and reports false on anything outside
// the subset the fast path accepts.
type streamEventScanner struct {
	b []byte
	i int
}

func (s *streamEventScanner) skipWS() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// consume skips whitespace and, if the next byte is c, consumes it.
func (s *streamEventScanner) consume(c byte) bool {
	s.skipWS()
	if s.i < len(s.b) && s.b[s.i] == c {
		s.i++
		return true
	}
	return false
}

func (s *streamEventScanner) atEnd() bool {
	s.skipWS()
	return s.i == len(s.b)
}

// delta scans a delta object: a type and at most one of text, partial_json,
// thinking or signature, each at most once.
func (s *streamEventScanner) delta(d *fastStreamDelta) bool {
	if !s.consume('{') {
		return false
	}
	if s.consume('}') {
		return true
	}
	for {
		key, esc, ok := s.str()
		if !ok || esc || !s.consume(':') {
			return false
		}
		field := deltaFieldNone
		switch string(key) {
		case "type":
			if d.hasType {
				return false
			}
			if d.typ, esc, ok = s.str(); !ok || esc {
				return false
			}
			d.hasType = true
		case "text":
			field = deltaFieldText
		case "partial_json":
			field = deltaFieldPartialJSON
		case "thinking":
			field = deltaFieldThinking
		case "signature":
			field = deltaFieldSignature
		default:
			return false
		}
		if field != deltaFieldNone {
			if d.field != deltaFieldNone {
				return false
			}
			d.field = field
			if d.value, d.valueEsc, ok = s.str(); !ok {
				return false
			}
		}
		if s.consume(',') {
			continue
		}
		return s.consume('}')
	}
}

// str scans a JSON string and returns its raw contents (between the quotes)
// and whether it holds an escape. Only the single-character escapes are
// accepted (\u and invalid escapes report false), as are no control
// characters; bytes >= 0x80 pass through as they are.
func (s *streamEventScanner) str() (raw []byte, escaped bool, ok bool) {
	if !s.consume('"') {
		return nil, false, false
	}
	start := s.i
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == '"':
			raw = s.b[start:s.i]
			s.i++
			return raw, escaped, true
		case c == '\\':
			if s.i+1 >= len(s.b) {
				return nil, false, false
			}
			switch s.b[s.i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			default:
				return nil, false, false
			}
			escaped = true
			s.i += 2
		case c < 0x20:
			return nil, false, false
		default:
			s.i++
		}
	}
	return nil, false, false
}

// smallUint scans a JSON number that is a non-negative integer of at most nine
// digits without a leading zero (so it fits any int), followed by a delimiter.
func (s *streamEventScanner) smallUint() (int, bool) {
	s.skipWS()
	start := s.i
	n := 0
	for s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9' {
		n = n*10 + int(s.b[s.i]-'0')
		s.i++
	}
	digits := s.i - start
	if digits == 0 || digits > 9 || (digits > 1 && s.b[start] == '0') {
		return 0, false
	}
	if s.i < len(s.b) {
		switch s.b[s.i] {
		case ',', '}', ' ', '\t', '\n', '\r':
		default:
			return 0, false
		}
	}
	return n, true
}

// unescapeSimpleJSONString decodes the single-character escapes str accepted.
func unescapeSimpleJSONString(raw []byte) string {
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' {
			out.WriteByte(c)
			continue
		}
		i++
		switch raw[i] {
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		default: // '"', '\\', '/'
			out.WriteByte(raw[i])
		}
	}
	return out.String()
}
