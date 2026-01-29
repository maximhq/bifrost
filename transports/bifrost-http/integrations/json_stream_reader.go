package integrations

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// ═══════════════════════════════════════════════════════════════
// Byte-level JSON stream scanner
// ═══════════════════════════════════════════════════════════════
//
// jsonStreamReader is a minimal JSON scanner that finds and extracts specific
// top-level keys from a JSON stream without materializing other values. This
// is critical for large payloads where the "contents" array contains hundreds
// of megabytes of base64 data — the scanner skips this data byte-by-byte
// through the bufio.Reader's 64KB buffer, using constant memory regardless
// of the data size.

// jsonStreamReader wraps a buffered reader and provides methods for scanning
// JSON streams with minimal memory allocation.
type jsonStreamReader struct {
	br *bufio.Reader
}

// newJSONStreamReader creates a jsonStreamReader with a read buffer.
func newJSONStreamReader(reader io.Reader, size int) *jsonStreamReader {
	return &jsonStreamReader{
		br: bufio.NewReaderSize(reader, size),
	}
}

// scanTopLevelKeys scans a JSON stream and captures values for the specified
// top-level keys. Non-matching keys are skipped without allocating memory.
// Returns a map of key name → raw JSON value bytes.
// Stops early once all requested keys are found.
//
// Memory: O(64KB buffer + total size of matched values)
func (jr *jsonStreamReader) scanTopLevelKeys(keys []string) map[string][]byte {
	if err := jr.expect('{'); err != nil {
		return nil
	}

	results := make(map[string][]byte, len(keys))
	remaining := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		remaining[k] = struct{}{}
	}

	first := true
	for {
		b, err := jr.peekNonWhitespace()
		if err != nil {
			return results
		}
		if b == '}' {
			break
		}
		if !first {
			if b != ',' {
				return results
			}
			jr.br.ReadByte() // consume ','
		}
		first = false

		if err := jr.expect('"'); err != nil {
			return results
		}
		key, err := jr.readKeyString()
		if err != nil {
			return results
		}
		if err := jr.expect(':'); err != nil {
			return results
		}

		if _, want := remaining[key]; want {
			value, err := jr.captureValue()
			if err != nil {
				return results
			}
			results[key] = value
			delete(remaining, key)
			if len(remaining) == 0 {
				break // Found all requested keys
			}
		} else {
			if err := jr.skipValue(); err != nil {
				return results
			}
		}
	}

	return results
}

// skipWhitespace advances past any JSON whitespace (space, tab, CR, LF).
func (jr *jsonStreamReader) skipWhitespace() error {
	for {
		b, err := jr.br.ReadByte()
		if err != nil {
			return err
		}
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return jr.br.UnreadByte()
		}
	}
}

// peekNonWhitespace skips whitespace and peeks at the next byte without consuming it.
func (jr *jsonStreamReader) peekNonWhitespace() (byte, error) {
	if err := jr.skipWhitespace(); err != nil {
		return 0, err
	}
	buf, err := jr.br.Peek(1)
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

// expect skips whitespace, reads one byte, and returns an error if it doesn't match expected.
func (jr *jsonStreamReader) expect(expected byte) error {
	if err := jr.skipWhitespace(); err != nil {
		return err
	}
	b, err := jr.br.ReadByte()
	if err != nil {
		return err
	}
	if b != expected {
		return fmt.Errorf("expected '%c', got '%c'", expected, b)
	}
	return nil
}

// readKeyString reads a JSON string value (opening quote already consumed)
// and returns the content. Escape sequences are tracked to correctly detect
// the closing quote (a \" does not end the string), and both the backslash
// and the escaped character are preserved in the output. This means the
// returned key is the raw JSON content between quotes, not a decoded string.
// For our use case (matching keys like "generationConfig"), this is correct
// since those keys never contain escape sequences.
func (jr *jsonStreamReader) readKeyString() (string, error) {
	var key []byte
	escaped := false

	for {
		b, err := jr.br.ReadByte()
		if err != nil {
			return "", err
		}

		if escaped {
			key = append(key, b)
			escaped = false
			continue
		}

		if b == '\\' {
			escaped = true
			key = append(key, b) // preserve the backslash
			continue
		}

		if b == '"' {
			return string(key), nil
		}

		key = append(key, b)
	}
}

// skipValue reads and discards a complete JSON value without allocating memory
// for its content. This is the key to Phase B's memory efficiency: a 400MB
// base64 string inside "contents" flows through the 64KB bufio buffer without
// being materialized as a Go string.
func (jr *jsonStreamReader) skipValue() error {
	if err := jr.skipWhitespace(); err != nil {
		return err
	}

	b, err := jr.br.ReadByte()
	if err != nil {
		return err
	}

	switch {
	case b == '"':
		return jr.skipString()
	case b == '{' || b == '[':
		return jr.skipCompound()
	case b == 't':
		return jr.discard(3) // "rue"
	case b == 'f':
		return jr.discard(4) // "alse"
	case b == 'n':
		return jr.discard(3) // "ull"
	case b == '-' || (b >= '0' && b <= '9'):
		return jr.skipNumber()
	default:
		return fmt.Errorf("unexpected byte in JSON value: 0x%02x", b)
	}
}

// skipString skips the content of a JSON string (opening quote already consumed).
// Handles escape sequences correctly without allocating any memory.
func (jr *jsonStreamReader) skipString() error {
	escaped := false
	for {
		b, err := jr.br.ReadByte()
		if err != nil {
			return err
		}
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' {
			escaped = true
			continue
		}
		if b == '"' {
			return nil
		}
	}
}

// skipCompound skips a complete JSON object or array (opening bracket already consumed).
// Tracks nesting depth and handles strings correctly (brackets inside strings are ignored).
func (jr *jsonStreamReader) skipCompound() error {
	depth := 1
	inString := false
	escaped := false

	for depth > 0 {
		b, err := jr.br.ReadByte()
		if err != nil {
			return err
		}

		if escaped {
			escaped = false
			continue
		}

		if inString {
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}

	return nil
}

// skipNumber skips remaining characters of a JSON number (first char already consumed).
func (jr *jsonStreamReader) skipNumber() error {
	for {
		b, err := jr.br.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if (b >= '0' && b <= '9') || b == '.' || b == 'e' || b == 'E' || b == '+' || b == '-' {
			continue
		}
		return jr.br.UnreadByte()
	}
}

// discard discards exactly n bytes from the reader.
func (jr *jsonStreamReader) discard(n int) error {
	_, err := jr.br.Discard(n)
	return err
}

// captureValue reads a complete JSON value and returns it as a byte slice.
// Used for small values like the generationConfig object (~100-300 bytes).
func (jr *jsonStreamReader) captureValue() ([]byte, error) {
	if err := jr.skipWhitespace(); err != nil {
		return nil, err
	}

	b, err := jr.br.ReadByte()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteByte(b)

	switch {
	case b == '"':
		return jr.captureString(&buf)
	case b == '{' || b == '[':
		return jr.captureCompound(&buf)
	case b == 't': // true
		return jr.captureN(&buf, 3)
	case b == 'f': // false
		return jr.captureN(&buf, 4)
	case b == 'n': // null
		return jr.captureN(&buf, 3)
	case b == '-' || (b >= '0' && b <= '9'):
		return jr.captureNumber(&buf)
	default:
		return nil, fmt.Errorf("unexpected byte in JSON value: 0x%02x", b)
	}
}

// captureString captures a JSON string value (opening quote already in buf).
func (jr *jsonStreamReader) captureString(buf *bytes.Buffer) ([]byte, error) {
	escaped := false
	for {
		b, err := jr.br.ReadByte()
		if err != nil {
			return nil, err
		}
		buf.WriteByte(b)

		if escaped {
			escaped = false
			continue
		}
		if b == '\\' {
			escaped = true
			continue
		}
		if b == '"' {
			return buf.Bytes(), nil
		}
	}
}

// captureCompound captures a JSON object or array (opening bracket already in buf).
func (jr *jsonStreamReader) captureCompound(buf *bytes.Buffer) ([]byte, error) {
	depth := 1
	inString := false
	escaped := false

	for depth > 0 {
		b, err := jr.br.ReadByte()
		if err != nil {
			return nil, err
		}
		buf.WriteByte(b)

		if escaped {
			escaped = false
			continue
		}

		if inString {
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}

	return buf.Bytes(), nil
}

// captureNumber captures a JSON number (first digit/minus already in buf).
func (jr *jsonStreamReader) captureNumber(buf *bytes.Buffer) ([]byte, error) {
	for {
		b, err := jr.br.ReadByte()
		if err != nil {
			if err == io.EOF {
				return buf.Bytes(), nil
			}
			return nil, err
		}
		if (b >= '0' && b <= '9') || b == '.' || b == 'e' || b == 'E' || b == '+' || b == '-' {
			buf.WriteByte(b)
			continue
		}
		if err := jr.br.UnreadByte(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
}

// captureN reads exactly n bytes from the reader and appends to buf.
func (jr *jsonStreamReader) captureN(buf *bytes.Buffer, n int) ([]byte, error) {
	for i := 0; i < n; i++ {
		b, err := jr.br.ReadByte()
		if err != nil {
			return nil, err
		}
		buf.WriteByte(b)
	}
	return buf.Bytes(), nil
}
