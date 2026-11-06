package gemini

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
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

// A line longer than the reader's buffer arrives in fragments. A space that lands
// on a fragment boundary is payload, so the reassembled line must keep it.
func TestReadNextSSEDataLine_KeepsSpaceAtFragmentBoundary(t *testing.T) {
	const bufSize = 1024
	prefix := "data: "
	text := `{"text":"` + strings.Repeat("a", bufSize-len(prefix)-len(`{"text":"`)-1) + " " + strings.Repeat("b", 64) + `"}`
	stream := prefix + text + "\n"

	line, err := readNextSSEDataLine(bufio.NewReaderSize(strings.NewReader(stream), bufSize), false)
	if err != nil {
		t.Fatalf("expected reassembled line, got error: %v", err)
	}
	if string(line) != text {
		t.Fatalf("reassembled line changed: got %d bytes, want %d", len(line), len(text))
	}
}

// fragmentBoundaryPayload builds a JSON payload that, after the given SSE prefix,
// puts sep as the last byte of the fragment that ends at offset boundary.
func fragmentBoundaryPayload(prefix string, boundary int, sep string) string {
	head := `{"text":"`
	fill := boundary - len(prefix) - len(head) - len(sep)
	return head + strings.Repeat("a", fill) + sep + strings.Repeat("b", 64) + `"}`
}

// Covers the fragment edges readNextSSEDataLine sees when a line is longer than
// the reader's buffer: whitespace inside the payload at a fragment boundary must
// survive, whitespace around the line must still be trimmed, and lines that are
// skipped or dropped must not swallow the next event.
func TestReadNextSSEDataLine_FragmentBoundaries(t *testing.T) {
	const bufSize = 1024
	const next = `{"text":"next"}`

	spaceAtSecondBoundary := fragmentBoundaryPayload("data: ", 2*bufSize, " ")
	spaceAtSecondBoundary = spaceAtSecondBoundary[:len(spaceAtSecondBoundary)-2] + strings.Repeat("c", bufSize) + `"}`
	tabAtBoundary := fragmentBoundaryPayload("data: ", bufSize, "\t")
	runOfSpaces := fragmentBoundaryPayload("data: ", bufSize, "    ")
	indented := fragmentBoundaryPayload("   data: ", bufSize, " ")
	trailing := fragmentBoundaryPayload("data: ", bufSize, " ")
	oversized := `{"text":"` + strings.Repeat("x", 600*1024) + `"}`

	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "space at a later fragment boundary",
			stream: "data: " + spaceAtSecondBoundary + "\n",
			want:   spaceAtSecondBoundary,
		},
		{
			name:   "tab at fragment boundary",
			stream: "data: " + tabAtBoundary + "\n",
			want:   tabAtBoundary,
		},
		{
			name:   "run of spaces ending at fragment boundary",
			stream: "data: " + runOfSpaces + "\n",
			want:   runOfSpaces,
		},
		{
			name:   "leading whitespace before data prefix",
			stream: "   data: " + indented + "\n",
			want:   indented,
		},
		{
			name:   "trailing whitespace and CRLF on a fragmented line",
			stream: "data: " + trailing + "  \t\r\n",
			want:   trailing,
		},
		{
			name:   "short line is still trimmed",
			stream: "  data:   " + next + "  \r\n",
			want:   next,
		},
		{
			name:   "fragmented comment line is skipped",
			stream: ": " + strings.Repeat("c", 3*bufSize) + "\n" + "data: " + next + "\n",
			want:   next,
		},
		{
			name:   "fragmented non-data field is skipped",
			stream: "event: " + strings.Repeat("e", 3*bufSize) + "\n" + "data: " + next + "\n",
			want:   next,
		},
		{
			name:   "line over the accumulation cap is dropped",
			stream: "data: " + oversized + "\n" + "data: " + next + "\n",
			want:   next,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := readNextSSEDataLine(bufio.NewReaderSize(strings.NewReader(tt.stream), bufSize), false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(line) != tt.want {
				t.Fatalf("got %d bytes, want %d bytes", len(line), len(tt.want))
			}
		})
	}
}

// With a small reader buffer, "inlineData" can be split across two fragments of
// one line. The line must still be skipped at every split point.
func TestReadNextSSEDataLine_SkipsInlineDataSplitAcrossFragments(t *testing.T) {
	const bufSize = 1024
	const next = `{"text":"next"}`
	const marker = `"inlineData"`
	const prefix = "data: "

	for _, boundary := range []int{bufSize, 2 * bufSize} {
		for split := 1; split < len(marker); split++ {
			// Place the marker so its first split bytes end the fragment at boundary.
			head := `{"text":"`
			fill := boundary - split - len(prefix) - len(head) - len(`",`)
			payload := head + strings.Repeat("a", fill) + `",` + marker + `:{"data":"abc"}}`
			stream := prefix + payload + "\n" + prefix + next + "\n"

			line, err := readNextSSEDataLine(bufio.NewReaderSize(strings.NewReader(stream), bufSize), true)
			if err != nil {
				t.Fatalf("boundary %d split %d: unexpected error: %v", boundary, split, err)
			}
			if string(line) != next {
				t.Fatalf("boundary %d split %d: inlineData line was not skipped, got %d bytes", boundary, split, len(line))
			}
		}
	}
}
