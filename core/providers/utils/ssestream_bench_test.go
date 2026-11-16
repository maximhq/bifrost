package utils

// Benchmarks for the shared SSE framing readers.
//
// Every streaming response in the gateway is framed by one of these two
// readers, once per event. A stream of a few hundred tokens therefore pays this
// cost a few hundred times before the caller sees the last chunk, which makes
// the framing loop (scanner buffer splitting, prefix parsing, the copy out of
// the scanner buffer) worth tracking on its own - separate from the provider
// specific JSON conversion that follows it.
//
// Run:
//
//	go test ./core/providers/utils/ -bench SSE -benchmem

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const benchSSEEventCount = 256

// benchSSEDataStream builds a Format A (data-only) stream the way OpenAI,
// Gemini and Cohere send it: one JSON chunk per data line, terminated by
// [DONE], with keep-alive comments interleaved.
func benchSSEDataStream() []byte {
	var sb strings.Builder
	for i := range benchSSEEventCount {
		if i%32 == 0 {
			sb.WriteString(": keep-alive\n\n")
		}
		sb.WriteString(`data: {"id":"chatcmpl-Bq9xK2LmNpQrStUvWxYz","object":"chat.completion.chunk","created":1742914380,"model":"gpt-4o-2024-11-20","choices":[{"index":0,"delta":{"content":" incrementally streamed token"},"finish_reason":null}]}`)
		sb.WriteString("\n\n")
	}
	sb.WriteString("data: [DONE]\n\n")
	return []byte(sb.String())
}

// benchSSEEventStream builds a Format B (typed event) stream the way Anthropic
// and Replicate send it: an event line plus a data line per event.
func benchSSEEventStream() []byte {
	var sb strings.Builder
	sb.WriteString("event: message_start\n")
	sb.WriteString(`data: {"type":"message_start","message":{"id":"msg_01XyZaBcDeFgHiJkLmNoPqRs","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[]}}`)
	sb.WriteString("\n\n")
	for range benchSSEEventCount {
		sb.WriteString("event: content_block_delta\n")
		sb.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" incrementally streamed token"}}`)
		sb.WriteString("\n\n")
	}
	sb.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return []byte(sb.String())
}

func BenchmarkSSEDataReader(b *testing.B) {
	stream := benchSSEDataStream()
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		reader := GetSSEDataReader(nil, bytes.NewReader(stream))
		lines := 0
		for {
			line, err := reader.ReadDataLine()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				b.Fatalf("read data line: %v", err)
			}
			lines += len(line)
		}
		if lines == 0 {
			b.Fatal("no data lines read")
		}
	}
}

func BenchmarkSSEEventReader(b *testing.B) {
	stream := benchSSEEventStream()
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		reader := GetSSEEventReader(nil, bytes.NewReader(stream))
		events := 0
		for {
			eventType, data, err := reader.ReadEvent()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				b.Fatalf("read event: %v", err)
			}
			events += len(eventType) + len(data)
		}
		if events == 0 {
			b.Fatal("no events read")
		}
	}
}
