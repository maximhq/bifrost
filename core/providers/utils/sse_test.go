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

// [DONE] and a bare body EOF both surface as io.EOF from ReadDataLine, so the
// reader must record which one it was — that flag is the only way a provider
// loop can tell a finished stream from a dead upstream connection.
func TestSSEDataReader_SawDoneMarkerOnDone(t *testing.T) {
	reader := newDefaultSSEDataReader(strings.NewReader("data: {\"a\":1}\n\ndata: [DONE]\n\n"))
	drainSSEDataReader(t, reader)
	if !reader.SawDoneMarker() {
		t.Error("expected SawDoneMarker to be true after reading [DONE]")
	}
	if !SSEStreamEndedOnMarker(reader) {
		t.Error("expected SSEStreamEndedOnMarker to be true after reading [DONE]")
	}
}

// A stream that just stops (upstream connection died on a chunk boundary) ends
// with the same io.EOF but no marker.
func TestSSEDataReader_SawDoneMarkerAbsentOnBareEOF(t *testing.T) {
	reader := newDefaultSSEDataReader(strings.NewReader("data: {\"a\":1}\n\n"))
	drainSSEDataReader(t, reader)
	if reader.SawDoneMarker() {
		t.Error("expected SawDoneMarker to be false when the body ended without [DONE]")
	}
	if SSEStreamEndedOnMarker(reader) {
		t.Error("expected SSEStreamEndedOnMarker to be false when the body ended without [DONE]")
	}
}

// A reader with no bytes at all (upstream died before its first byte) must also
// report no marker.
func TestSSEDataReader_SawDoneMarkerAbsentOnEmptyStream(t *testing.T) {
	reader := newDefaultSSEDataReader(strings.NewReader(""))
	drainSSEDataReader(t, reader)
	if SSEStreamEndedOnMarker(reader) {
		t.Error("expected SSEStreamEndedOnMarker to be false for an empty stream")
	}
}

// stubSSEDataReader stands in for an enterprise-injected reader that predates
// SSEStreamTerminator.
type stubSSEDataReader struct{}

func (stubSSEDataReader) ReadDataLine() ([]byte, error) { return nil, io.EOF }

// Readers that cannot report a marker must be assumed to have terminated
// normally: reporting them as truncated would fail every enterprise stream.
func TestSSEStreamEndedOnMarker_UnknownReaderDefaultsTrue(t *testing.T) {
	if !SSEStreamEndedOnMarker(stubSSEDataReader{}) {
		t.Error("expected SSEStreamEndedOnMarker to default to true for readers without SSEStreamTerminator")
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
