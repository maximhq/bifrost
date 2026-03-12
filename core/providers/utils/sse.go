package utils

import (
	"bufio"
	"bytes"
	"io"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	sseInitialBufSize = 8 * 1024        // 8KB — sufficient for >99.9% of SSE lines
	sseMaxBufSize     = 10 * 1024 * 1024 // 10MB — allow large tokens (tool calls, audio)
)

// SSEDataReader reads SSE data-only events (Format A: OpenAI, Gemini, Cohere, etc.).
// ReadDataLine returns the next SSE data payload, stripping the "data:" prefix.
// Returns (nil, io.EOF) at end of stream or on "data: [DONE]".
type SSEDataReader interface {
	ReadDataLine() ([]byte, error)
}

// SSEEventReader reads SSE events with type and data (Format B: Anthropic, Replicate, etc.).
// ReadEvent returns the complete event once an empty-line delimiter is encountered.
// Multiple "data:" lines within one event are concatenated with newlines.
// Returns ("", nil, io.EOF) at end of stream.
type SSEEventReader interface {
	ReadEvent() (eventType string, data []byte, err error)
}

// SSEReaderFactory creates SSE readers for streaming response processing.
// Enterprise injects this via BifrostContextKeySSEReaderFactory to replace
// the default bufio.Scanner-based implementations with streaming readers.
type SSEReaderFactory struct {
	NewDataReader  func(reader io.Reader) SSEDataReader
	NewEventReader func(reader io.Reader) SSEEventReader
}

// GetSSEDataReader returns an SSEDataReader for the given reader.
// If enterprise has injected an SSEReaderFactory via context, uses that.
// Otherwise returns a default implementation wrapping bufio.NewScanner.
func GetSSEDataReader(ctx *schemas.BifrostContext, reader io.Reader) SSEDataReader {
	if ctx != nil {
		if factory, ok := ctx.Value(schemas.BifrostContextKeySSEReaderFactory).(*SSEReaderFactory); ok && factory != nil && factory.NewDataReader != nil {
			return factory.NewDataReader(reader)
		}
	}
	return newDefaultSSEDataReader(reader)
}

// GetSSEEventReader returns an SSEEventReader for the given reader.
// If enterprise has injected an SSEReaderFactory via context, uses that.
// Otherwise returns a default implementation wrapping bufio.NewScanner.
func GetSSEEventReader(ctx *schemas.BifrostContext, reader io.Reader) SSEEventReader {
	if ctx != nil {
		if factory, ok := ctx.Value(schemas.BifrostContextKeySSEReaderFactory).(*SSEReaderFactory); ok && factory != nil && factory.NewEventReader != nil {
			return factory.NewEventReader(reader)
		}
	}
	return newDefaultSSEEventReader(reader)
}

// Reusable byte prefixes for SSE field parsing.
var (
	sseDataPrefix  = []byte("data:")
	sseDoneMarker  = []byte("[DONE]")
	sseEventPrefix = []byte("event:")
	sseIDPrefix    = []byte("id:")
	sseRetryPrefix = []byte("retry:")
)

// defaultSSEDataReader implements SSEDataReader using bufio.NewScanner.
// Handles Format A SSE streams (data-only: OpenAI, Gemini, Cohere, etc.).
type defaultSSEDataReader struct {
	scanner *bufio.Scanner
}

func newDefaultSSEDataReader(reader io.Reader) *defaultSSEDataReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, sseInitialBufSize), sseMaxBufSize)
	return &defaultSSEDataReader{scanner: scanner}
}

func (r *defaultSSEDataReader) ReadDataLine() ([]byte, error) {
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		// Skip empty lines and comments
		if len(line) == 0 || line[0] == ':' {
			continue
		}

		// Parse "data:" lines
		if bytes.HasPrefix(line, sseDataPrefix) {
			data := line[5:] // len("data:") == 5
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			if len(data) == 0 {
				continue
			}
			if bytes.Equal(data, sseDoneMarker) {
				return nil, io.EOF
			}
			// Copy to decouple from scanner's internal buffer
			return append([]byte(nil), data...), nil
		}

		// Skip known SSE fields (event, id, retry)
		if bytes.HasPrefix(line, sseEventPrefix) ||
			bytes.HasPrefix(line, sseIDPrefix) ||
			bytes.HasPrefix(line, sseRetryPrefix) {
			continue
		}

		// Non-SSE line: return as-is (raw JSON error fallback, e.g. OpenAI)
		return append([]byte(nil), line...), nil
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// defaultSSEEventReader implements SSEEventReader using bufio.NewScanner.
// Handles Format B SSE streams (event+data: Anthropic, Replicate, Mistral, etc.).
// Events are delimited by empty lines; multiple "data:" lines are concatenated.
type defaultSSEEventReader struct {
	scanner   *bufio.Scanner
	eventType string
	eventData []byte
}

func newDefaultSSEEventReader(reader io.Reader) *defaultSSEEventReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, sseInitialBufSize), sseMaxBufSize)
	return &defaultSSEEventReader{scanner: scanner}
}

func (r *defaultSSEEventReader) ReadEvent() (string, []byte, error) {
	for r.scanner.Scan() {
		line := r.scanner.Bytes()

		// Skip comments
		if len(line) > 0 && line[0] == ':' {
			continue
		}

		// Empty line = event boundary
		if len(line) == 0 {
			if r.eventType == "" && len(r.eventData) == 0 {
				continue
			}
			eventType := r.eventType
			eventData := make([]byte, len(r.eventData))
			copy(eventData, r.eventData)
			r.eventType = ""
			r.eventData = r.eventData[:0]
			return eventType, eventData, nil
		}

		// Parse SSE fields
		if bytes.HasPrefix(line, sseEventPrefix) {
			field := line[6:] // len("event:") == 6
			if len(field) > 0 && field[0] == ' ' {
				field = field[1:]
			}
			r.eventType = string(field)
		} else if bytes.HasPrefix(line, sseDataPrefix) {
			data := line[5:] // len("data:") == 5
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			if len(r.eventData) > 0 {
				r.eventData = append(r.eventData, '\n')
			}
			r.eventData = append(r.eventData, data...)
		}
		// id:, retry:, and other fields are silently skipped
	}

	// Scanner done — return any accumulated event before EOF
	if r.eventType != "" || len(r.eventData) > 0 {
		eventType := r.eventType
		eventData := make([]byte, len(r.eventData))
		copy(eventData, r.eventData)
		r.eventType = ""
		r.eventData = r.eventData[:0]
		return eventType, eventData, nil
	}

	if err := r.scanner.Err(); err != nil {
		return "", nil, err
	}
	return "", nil, io.EOF
}
