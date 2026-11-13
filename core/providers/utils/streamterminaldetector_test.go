package utils

import "testing"

// frame builds an SSE frame (without the trailing delimiter, which callers strip before
// passing frames to extractSSEDataPayload/isTerminalSSEPayload in production).
func terminalForFrame(t *testing.T, frame string) bool {
	t.Helper()
	return isTerminalSSEPayload(extractSSEDataPayload([]byte(frame)))
}

func TestIsTerminalSSEPayloadSSEFinishReason(t *testing.T) {
	if terminalForFrame(t, `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`) != true {
		t.Fatalf("expected terminal detection for candidates finishReason")
	}
}

func TestIsTerminalSSEPayloadSSETopLevelFinishReason(t *testing.T) {
	if !terminalForFrame(t, `data: {"id":"abc","finishReason":"STOP"}`) {
		t.Fatalf("expected terminal detection for top-level finishReason")
	}
}

func TestIsTerminalSSEPayloadDoneMarker(t *testing.T) {
	if !terminalForFrame(t, `data: [DONE]`) {
		t.Fatalf("expected [DONE] marker to be terminal")
	}
}

func TestFindSSEFrameDelimiterRecognizesCRLF(t *testing.T) {
	data := []byte("data: [DONE]\r\n\r\ntrailing")
	idx, delimLen := findFirstSSEFrameDelimiter(data)
	if idx != len("data: [DONE]") || delimLen != len("\r\n\r\n") {
		t.Fatalf("expected CRLF delimiter to be found at %d with length 4, got idx=%d delimLen=%d", len("data: [DONE]"), idx, delimLen)
	}
}

func TestIsTerminalSSEPayloadIgnoresUnspecifiedFinishReason(t *testing.T) {
	if terminalForFrame(t, `data: {"finishReason":"FINISH_REASON_UNSPECIFIED"}`) {
		t.Fatalf("unexpected terminal detection for FINISH_REASON_UNSPECIFIED")
	}
}

func TestIsTerminalSSEPayloadPlainJSON(t *testing.T) {
	// Passthrough streams without SSE framing (e.g. Vertex ?alt=json) feed the raw trimmed
	// JSON directly to isTerminalSSEPayload, bypassing extractSSEDataPayload.
	if !isTerminalSSEPayload([]byte(`{"content":"hello","finishReason":"STOP"}`)) {
		t.Fatalf("expected terminal detection for plain json stream")
	}
}

func TestIsTerminalSSEPayloadJSONWithDataURIToken(t *testing.T) {
	frame := `{"finishReason":"STOP","content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"data:image/png;base64,AAAA"}}]}}`
	if !terminalForFrame(t, frame) {
		t.Fatalf("expected terminal detection for delimited JSON containing data URI token")
	}
}

func TestIsTerminalSSEPayloadMultiEventSSEInSingleChunk(t *testing.T) {
	chunk := []byte("data: {}\n\ndata: {\"finishReason\":\"STOP\"}\n\n")
	idx, delimLen := findFirstSSEFrameDelimiter(chunk)
	if idx < 0 {
		t.Fatalf("expected to find first frame delimiter")
	}
	firstFrame := chunk[:idx]
	rest := chunk[idx+delimLen:]
	if isTerminalSSEPayload(extractSSEDataPayload(firstFrame)) {
		t.Fatalf("unexpected terminal detection on first event")
	}
	idx2, delimLen2 := findFirstSSEFrameDelimiter(rest)
	if idx2 < 0 {
		t.Fatalf("expected to find second frame delimiter")
	}
	secondFrame := rest[:idx2+delimLen2]
	if !terminalForFrame(t, string(secondFrame)) {
		t.Fatalf("expected terminal detection for second event")
	}
}

func TestIsTerminalSSEPayloadMetadataOnlyFrameIsNotTerminal(t *testing.T) {
	if terminalForFrame(t, `data: {"usageMetadata":{"totalTokenCount":12}}`) {
		t.Fatalf("unexpected terminal detection for metadata-only frame")
	}
}

func TestIsTerminalSSEPayloadMetadataWithFinishedCandidateIsTerminal(t *testing.T) {
	if !terminalForFrame(t, `data: {"usageMetadata":{"totalTokenCount":12},"candidates":[{"finishReason":"STOP"}]}`) {
		t.Fatalf("expected terminal detection for metadata with finished candidate")
	}
}

func TestIsTerminalSSEPayloadMetadataWithUnspecifiedCandidateIsNotTerminal(t *testing.T) {
	if terminalForFrame(t, `data: {"usageMetadata":{"totalTokenCount":12},"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`) {
		t.Fatalf("unexpected terminal detection for metadata with unfinished candidate")
	}
}

func TestIsTerminalSSEPayloadMetadataWithMixedCandidatesIsNotTerminal(t *testing.T) {
	if terminalForFrame(t, `data: {"usageMetadata":{"totalTokenCount":12},"candidates":[{"finishReason":"STOP"},{}]}`) {
		t.Fatalf("unexpected terminal detection for metadata with mixed finished/unfinished candidates")
	}
}

func TestIsTerminalSSEPayloadTopLevelArrayWithCandidatesFinishReason(t *testing.T) {
	if !terminalForFrame(t, `data: [{"candidates":[{"finishReason":"STOP"}]}]`) {
		t.Fatalf("expected terminal detection for top-level array payload")
	}
}

func TestIsTerminalSSEPayloadCandidatesRequireAllFinished(t *testing.T) {
	if terminalForFrame(t, `data: {"candidates":[{"finishReason":"STOP"},{"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`) {
		t.Fatalf("unexpected terminal detection when not all candidates are finished")
	}
}

func TestIsTerminalSSEPayloadCandidatesAllFinishedIsTerminal(t *testing.T) {
	if !terminalForFrame(t, `data: {"candidates":[{"finishReason":"STOP"},{"finishReason":"MAX_TOKENS"}]}`) {
		t.Fatalf("expected terminal detection when all candidates are finished")
	}
}

func TestIsTerminalSSEPayloadAnthropicMessageStop(t *testing.T) {
	if terminalForFrame(t, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}") {
		t.Fatalf("unexpected terminal detection on content_block_stop")
	}
	if terminalForFrame(t, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":522,\"output_tokens\":13}}") {
		t.Fatalf("unexpected terminal detection on message_delta (usage must still be observed)")
	}
	if !terminalForFrame(t, "event: message_stop\ndata: {\"type\":\"message_stop\"}") {
		t.Fatalf("expected terminal detection for anthropic message_stop")
	}
}

// Vertex returns errors as a streamed JSON array with the body left open; without terminal
// detection the stream hangs until the idle timeout (issue #6073).
func TestIsTerminalSSEPayloadVertexErrorArray(t *testing.T) {
	body := []byte("[{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Publisher Model is not servable in region us-central1.\",\n    \"status\": \"FAILED_PRECONDITION\"\n  }\n}\n]")
	if !isTerminalSSEPayload(body) {
		t.Fatalf("expected terminal detection for vertex error array")
	}
}

func TestIsTerminalSSEPayloadGoogleErrorObject(t *testing.T) {
	if !terminalForFrame(t, `data: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`) {
		t.Fatalf("expected terminal detection for google error object")
	}
}

func TestIsTerminalSSEPayloadPlainErrorStringIsNotTerminal(t *testing.T) {
	if terminalForFrame(t, `data: {"error":"","candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`) {
		t.Fatalf("unexpected terminal detection for a non-envelope error field")
	}
}
