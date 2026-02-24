package gemini

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestReadNextSSEDataLine_SkipInlineDataOnGzipReader(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	payload := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"data\":\"abc\"}}]}}]}\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n"
	if _, err := gz.Write([]byte(payload)); err != nil {
		t.Fatalf("failed to write gzip payload: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	reader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer reader.Close()

	line, err := readNextSSEDataLine(bufio.NewReaderSize(reader, 64*1024), true)
	if err != nil {
		t.Fatalf("expected next non-inline line, got error: %v", err)
	}

	want := []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)
	if !bytes.Equal(line, want) {
		t.Fatalf("expected %q, got %q", string(want), string(line))
	}
}

func TestReadNextSSEDataLine_SkipInlineDataContinuedLine(t *testing.T) {
	longInline := bytes.Repeat([]byte("x"), 70*1024)
	var stream bytes.Buffer
	stream.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"data\":\"")
	stream.Write(longInline)
	stream.WriteString("\"}}]}}]}\n")
	stream.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n")

	line, err := readNextSSEDataLine(bufio.NewReaderSize(bytes.NewReader(stream.Bytes()), 64*1024), true)
	if err != nil {
		t.Fatalf("expected next non-inline line, got error: %v", err)
	}

	want := []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)
	if !bytes.Equal(line, want) {
		t.Fatalf("expected %q, got %q", string(want), string(line))
	}

	_, err = readNextSSEDataLine(bufio.NewReaderSize(bytes.NewReader(nil), 64*1024), true)
	if err != io.EOF {
		t.Fatalf("expected EOF on empty reader, got %v", err)
	}
}
