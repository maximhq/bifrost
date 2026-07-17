package utils

import (
	"io"
	"strings"
	"testing"
)

// drainSSEDataReader reads payloads until EOF, failing the test on any other error.
func drainSSEDataReader(t *testing.T, r SSEDataReader) []string {
	t.Helper()
	var payloads []string
	for {
		data, err := r.ReadDataLine()
		if err == io.EOF {
			return payloads
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		payloads = append(payloads, string(data))
	}
}

func TestSSEDataReader_DataLinesAndDone(t *testing.T) {
	stream := "data: {\"a\":1}\n" +
		": comment\n" +
		"\n" +
		"data: {\"b\":2}\n" +
		"data: [DONE]\n"
	payloads := drainSSEDataReader(t, newDefaultSSEDataReader(strings.NewReader(stream)))
	if len(payloads) != 2 || payloads[0] != `{"a":1}` || payloads[1] != `{"b":2}` {
		t.Errorf("unexpected payloads: %#v", payloads)
	}
}

func TestSSEDataReader_SingleLineRawJSONFallback(t *testing.T) {
	stream := `{"error": {"code": 429, "status": "RESOURCE_EXHAUSTED"}}` + "\n"
	payloads := drainSSEDataReader(t, newDefaultSSEDataReader(strings.NewReader(stream)))
	if len(payloads) != 1 || payloads[0] != `{"error": {"code": 429, "status": "RESOURCE_EXHAUSTED"}}` {
		t.Errorf("unexpected payloads: %#v", payloads)
	}
}

// Vertex aborts streams (e.g. mid-stream 429s) with a pretty-printed error
// body outside SSE framing; the reader must reassemble it into one payload.
func TestSSEDataReader_MultilineErrorReassembly(t *testing.T) {
	stream := "data: {\"candidates\":[{}]}\n" +
		"{\n" +
		"  \"error\": {\n" +
		"    \"code\": 429,\n" +
		"    \"message\": \"Resource exhausted. Please try again later.\",\n" +
		"    \"status\": \"RESOURCE_EXHAUSTED\"\n" +
		"  }\n" +
		"}\n"
	payloads := drainSSEDataReader(t, newDefaultSSEDataReader(strings.NewReader(stream)))
	if len(payloads) != 2 {
		t.Fatalf("expected 2 payloads, got %d: %#v", len(payloads), payloads)
	}
	if payloads[0] != `{"candidates":[{}]}` {
		t.Errorf("unexpected data payload: %q", payloads[0])
	}
	want := "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource exhausted. Please try again later.\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}"
	if payloads[1] != want {
		t.Errorf("unexpected reassembled payload: %q", payloads[1])
	}
}

// A data: line arriving mid-accumulation aborts reassembly and is delivered
// on the next read, so a live stream can never be swallowed.
func TestSSEDataReader_AccumulationAbortedByDataLine(t *testing.T) {
	stream := "{\n" +
		"data: {\"b\":2}\n"
	payloads := drainSSEDataReader(t, newDefaultSSEDataReader(strings.NewReader(stream)))
	if len(payloads) != 2 || payloads[0] != "{" || payloads[1] != `{"b":2}` {
		t.Errorf("unexpected payloads: %#v", payloads)
	}
}

// An opening line indented with whitespace must still enter reassembly.
func TestSSEDataReader_MultilineReassemblyWithLeadingWhitespace(t *testing.T) {
	stream := "  {\n" +
		"    \"error\": {\"code\": 429}\n" +
		"  }\n"
	payloads := drainSSEDataReader(t, newDefaultSSEDataReader(strings.NewReader(stream)))
	want := "  {\n    \"error\": {\"code\": 429}\n  }"
	if len(payloads) != 1 || payloads[0] != want {
		t.Errorf("unexpected payloads: %#v", payloads)
	}
}

// A stream ending mid-object returns the partial buffer as-is for the caller
// to surface (warn + skip), matching the previous per-line behavior.
func TestSSEDataReader_PartialObjectAtEOF(t *testing.T) {
	stream := "{\n" +
		"  \"error\": {\n"
	payloads := drainSSEDataReader(t, newDefaultSSEDataReader(strings.NewReader(stream)))
	if len(payloads) != 1 || payloads[0] != "{\n  \"error\": {" {
		t.Errorf("unexpected payloads: %#v", payloads)
	}
}
