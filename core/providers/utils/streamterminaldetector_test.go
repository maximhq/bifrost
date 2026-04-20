package utils

import (
	"bytes"
	"testing"
)

func TestStreamTerminalDetectorObserveChunkSSEFinishReasonAcrossChunks(t *testing.T) {
	detector := &StreamTerminalDetector{}

	chunks := [][]byte{
		[]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},"),
		[]byte("\"finishReason\":\"STOP\"}]}\n\n"),
	}

	if detector.ObserveChunk(chunks[0]) {
		t.Fatalf("unexpected terminal detection on first chunk")
	}
	if !detector.ObserveChunk(chunks[1]) {
		t.Fatalf("expected terminal detection for candidates finishReason")
	}
}

func TestStreamTerminalDetectorObserveChunkSSETopLevelFinishReasonAcrossChunks(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunks := [][]byte{
		[]byte("data: {\"id\":\"abc\","),
		[]byte("\"finishReason\":\"STOP\"}\n\n"),
	}
	if detector.ObserveChunk(chunks[0]) {
		t.Fatalf("unexpected terminal detection on first chunk")
	}
	if !detector.ObserveChunk(chunks[1]) {
		t.Fatalf("expected terminal detection for top-level finishReason")
	}
}

func TestStreamTerminalDetectorObserveChunkDoneMarker(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if !detector.ObserveChunk([]byte("data: [DONE]\n\n")) {
		t.Fatalf("expected [DONE] marker to be terminal")
	}
}

func TestStreamTerminalDetectorObserveChunkDoneMarkerCRLF(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if !detector.ObserveChunk([]byte("data: [DONE]\r\n\r\n")) {
		t.Fatalf("expected [DONE] marker with CRLF delimiter to be terminal")
	}
}

func TestStreamTerminalDetectorObserveChunkIgnoresUnspecifiedFinishReason(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("data: {\"finishReason\":\"FINISH_REASON_UNSPECIFIED\"}\n\n")) {
		t.Fatalf("unexpected terminal detection for FINISH_REASON_UNSPECIFIED")
	}
}

func TestStreamTerminalDetectorObserveChunkPlainJSONAcrossChunks(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("{\"content\":\"hello\",")) {
		t.Fatalf("unexpected terminal detection for incomplete json")
	}
	if !detector.ObserveChunk([]byte("\"finishReason\":\"STOP\"}")) {
		t.Fatalf("expected terminal detection for plain json stream")
	}
}

func TestStreamTerminalDetectorObserveChunkJSONWithDataURITokenAndDelimiter(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunk := []byte("{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"data:image/png;base64,AAAA\"}}]}}\n\n")
	if !detector.ObserveChunk(chunk) {
		t.Fatalf("expected terminal detection for delimited JSON containing data URI token")
	}
}

func TestStreamTerminalDetectorObserveChunkMultiEventSSEInSingleChunk(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunk := []byte("data: {}\n\ndata: {\"finishReason\":\"STOP\"}\n\n")
	if !detector.ObserveChunk(chunk) {
		t.Fatalf("expected terminal detection for multi-event SSE chunk")
	}
}

func TestStreamTerminalDetectorObserveChunkMetadataOnlyFrameIsNotTerminal(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("data: {\"usageMetadata\":{\"totalTokenCount\":12}}\n\n")) {
		t.Fatalf("unexpected terminal detection for metadata-only frame")
	}
}

func TestStreamTerminalDetectorObserveChunkMetadataWithFinishedCandidateIsTerminal(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if !detector.ObserveChunk([]byte("data: {\"usageMetadata\":{\"totalTokenCount\":12},\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")) {
		t.Fatalf("expected terminal detection for metadata with finished candidate")
	}
}

func TestStreamTerminalDetectorObserveChunkMetadataWithUnspecifiedCandidateIsNotTerminal(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("data: {\"usageMetadata\":{\"totalTokenCount\":12},\"candidates\":[{\"finishReason\":\"FINISH_REASON_UNSPECIFIED\"}]}\n\n")) {
		t.Fatalf("unexpected terminal detection for metadata with unfinished candidate")
	}
}

func TestStreamTerminalDetectorObserveChunkMetadataWithMixedCandidatesIsNotTerminal(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("data: {\"usageMetadata\":{\"totalTokenCount\":12},\"candidates\":[{\"finishReason\":\"STOP\"},{}]}\n\n")) {
		t.Fatalf("unexpected terminal detection for metadata with mixed finished/unfinished candidates")
	}
}

func TestStreamTerminalDetectorObserveChunkTopLevelArrayWithCandidatesFinishReason(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunk := []byte("data: [{\"candidates\":[{\"finishReason\":\"STOP\"}]}]\n\n")
	if !detector.ObserveChunk(chunk) {
		t.Fatalf("expected terminal detection for top-level array payload")
	}
}

func TestStreamTerminalDetectorObserveChunkCandidatesRequireAllFinished(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunk := []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"},{\"finishReason\":\"FINISH_REASON_UNSPECIFIED\"}]}\n\n")
	if detector.ObserveChunk(chunk) {
		t.Fatalf("unexpected terminal detection when not all candidates are finished")
	}
}

func TestStreamTerminalDetectorObserveChunkCandidatesAllFinishedIsTerminal(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunk := []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"},{\"finishReason\":\"MAX_TOKENS\"}]}\n\n")
	if !detector.ObserveChunk(chunk) {
		t.Fatalf("expected terminal detection when all candidates are finished")
	}
}

func TestStreamTerminalDetectorObserveChunkUndelimitedOverflowKeepsPrefix(t *testing.T) {
	detector := &StreamTerminalDetector{}
	originalPrefix := bytes.Repeat([]byte("a"), maxTerminalDetectorBufferBytes/2)
	chunk := append([]byte(nil), originalPrefix...)
	chunk = append(chunk, bytes.Repeat([]byte("b"), maxTerminalDetectorBufferBytes)...)

	if detector.ObserveChunk(chunk) {
		t.Fatalf("unexpected terminal detection for non-json buffer")
	}

	trimTo := maxTerminalDetectorBufferBytes / 2
	got := detector.pending.Bytes()
	if len(got) != trimTo {
		t.Fatalf("expected pending length %d, got %d", trimTo, len(got))
	}
	if !bytes.Equal(got, originalPrefix) {
		t.Fatalf("expected pending buffer to keep original prefix")
	}
}
