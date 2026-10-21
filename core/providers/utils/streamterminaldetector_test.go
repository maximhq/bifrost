package utils

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// The production structural predicates are unchanged by the optimization.
// This oracle retains the original full decoding path; literal expectations
// below also pin the wire spellings independently of production constants.
func referenceFinishReasonMarker(payload []byte) bool {
	var root any
	if err := sonic.Unmarshal(payload, &root); err != nil {
		return false
	}
	switch value := root.(type) {
	case map[string]any:
		return hasTerminalMarkerInTopLevelObject(value)
	case []any:
		for _, item := range value {
			if object, ok := item.(map[string]any); ok && hasTerminalMarkerInTopLevelObject(object) {
				return true
			}
		}
	}
	return false
}

const markerFreeStreamPayload = `{"candidates":[{"content":{"parts":[{"text":"hello"}],"role":"model"},"index":0}]}`

var terminalPayloadCases = []struct {
	name, payload string
	terminal      bool
}{
	{"ordinary delta", markerFreeStreamPayload, false},
	{"unicode text", `{"candidates":[{"content":{"parts":[{"text":"café 東京"}]}}]}`, false},
	{"escaped text", `{"candidates":[{"content":{"parts":[{"text":"line one\n\"line two\""}]}}]}`, false},
	{"marker text", `{"text":"finishReason message_stop error blockReason"}`, false},
	{"nested marker", `{"metadata":{"finishReason":"STOP","type":"message_stop"}}`, false},
	{"camel finish", `{"finishReason":"STOP"}`, true},
	{"snake finish", `{"finish_reason":"stop"}`, true},
	{"escaped key", `{"finish\u0052eason":"STOP"}`, true},
	{"empty finish", `{"finishReason":" "}`, false},
	{"null finish", `{"finishReason":null}`, false},
	{"number finish", `{"finishReason":42}`, false},
	{"unspecified finish", `{"finishReason":"FINISH_REASON_UNSPECIFIED"}`, false},
	{"literal whitespace semantics", `{"finishReason":" FINISH_REASON_UNSPECIFIED "}`, true},
	{"duplicate last empty", `{"finishReason":"STOP","finishReason":""}`, false},
	{"duplicate last terminal", `{"finishReason":"","finishReason":"STOP"}`, true},
	{"all candidates finished", `{"candidates":[{"finishReason":"STOP"},{"finish_reason":"length"}]}`, true},
	{"mixed candidates", `{"candidates":[{"finishReason":"STOP"},{}]}`, false},
	{"non-object candidate", `{"candidates":[{"finishReason":"STOP"},null]}`, false},
	{"empty candidates", `{"candidates":[]}`, false},
	{"usage only", `{"usageMetadata":{"totalTokenCount":6}}`, false},
	{"usage with unfinished candidate", `{"usageMetadata":{"totalTokenCount":6},"candidates":[{}]}`, false},
	{"message delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, false},
	{"message stop", `{"type":"message_stop"}`, true},
	{"escaped event", `{"ty\u0070e":"message_\u0073top"}`, true},
	{"error event", `{"type":"error"}`, true},
	{"error code", `{"error":{"code":429}}`, true},
	{"error code presence", `{"error":{"code":null}}`, true},
	{"error status", `{"error":{"status":"RESOURCE_EXHAUSTED"}}`, true},
	{"escaped error key", `{"err\u006fr":{"code":429}}`, true},
	{"empty error status", `{"error":{"status":" "}}`, false},
	{"error string", `{"error":"not an envelope"}`, false},
	{"blocked prompt", `{"promptFeedback":{"blockReason":"SAFETY"}}`, true},
	{"escaped block key", `{"promptFeedback":{"block\u0052eason":"SAFETY"}}`, true},
	{"empty block reason", `{"promptFeedback":{"blockReason":" "}}`, false},
	{"candidate precedence", `{"candidates":[],"promptFeedback":{"blockReason":"SAFETY"}}`, false},
	{"array terminal", `[null,42,{"type":"message_stop"}]`, true},
	{"array ordinary", `[{},42,"hello"]`, false},
	{"string", `"message_stop"`, false},
	{"null", `null`, false},
	{"empty", ``, false},
	{"truncated", `{"type":"message_stop"`, false},
	{"trailing data", `{"type":"message_stop"}x`, false},
	{"invalid comma", `{"finishReason":"STOP",}`, false},
	{"overflow", `{"unused":1e999,"type":"message_stop"}`, false},
}

func TestFinishReasonMarkerMatchesReference(t *testing.T) {
	for _, tc := range terminalPayloadCases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			if got := referenceFinishReasonMarker(payload); got != tc.terminal {
				t.Fatalf("baseline terminal = %v, want %v", got, tc.terminal)
			}
			if got := hasFinishReasonMarker(payload); got != tc.terminal {
				t.Fatalf("terminal = %v, want %v", got, tc.terminal)
			}
		})
	}
}

func FuzzFinishReasonMarkerMatchesReference(f *testing.F) {
	for _, tc := range terminalPayloadCases {
		f.Add([]byte(tc.payload))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if got, want := hasFinishReasonMarker(payload), referenceFinishReasonMarker(payload); got != want {
			t.Fatalf("%q: terminal = %v, baseline = %v", payload, got, want)
		}
	})
}

func TestFinishReasonMarkerOrdinaryDeltaAllocations(t *testing.T) {
	payload := []byte(markerFreeStreamPayload)
	allocations := testing.AllocsPerRun(100, func() {
		if hasFinishReasonMarker(payload) {
			t.Fatal("ordinary content delta was terminal")
		}
	})
	if allocations != 0 {
		t.Fatalf("ordinary delta allocated %v times, want zero", allocations)
	}
}

func TestPassthroughTerminalStreamPreservesBytesAndUsage(t *testing.T) {
	for _, delimiter := range []string{"\n\n", "\r\n\r\n"} {
		t.Run(delimiter, func(t *testing.T) {
			// The first event reaches the fast path. Usage alone and malformed
			// marker-bearing events must not stop the stream before the last event.
			body := []byte("data: " + markerFreeStreamPayload + delimiter +
				"data: {\"usageMetadata\":{\"totalTokenCount\":6}}" + delimiter +
				"data: {\"finishReason\":\"STOP\",}" + delimiter +
				"data: {\"candidates\":[{\"finish\\u0052eason\":\"STOP\"}]}" + delimiter)
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			defer ctx.Cancel()
			usage := &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{TotalTokens: 6}}
			observations := 0
			stream := StreamPassthroughHTTP(ctx, passthroughTestHooks, nil,
				io.NopCloser(iotest.OneByteReader(bytes.NewReader(body))), PassthroughStreamParams{
					StatusCode: 200, StartTime: time.Now(), UseTerminalDetector: true,
					HasUsage: func(event []byte) bool { return GetJSONField(event, "usageMetadata").Exists() },
					Observe: func([]byte) *schemas.BifrostPassthroughUsage {
						observations++
						return usage
					},
				})
			var forwarded []byte
			var final *schemas.BifrostPassthroughUsage
			for chunk := range stream {
				if chunk.BifrostError != nil || chunk.BifrostPassthroughResponse == nil {
					t.Fatalf("unexpected chunk: %+v", chunk)
				}
				forwarded = append(forwarded, chunk.BifrostPassthroughResponse.Body...)
				if value := chunk.BifrostPassthroughResponse.PassthroughUsage; value != nil {
					final = value
				}
			}
			if !bytes.Equal(forwarded, body) || final != usage || observations != 1 {
				t.Fatalf("bytes %d/%d, final usage %v, observations %d", len(forwarded), len(body), final, observations)
			}
			if ctx.Err() != nil {
				t.Fatalf("semantic completion cancelled the request: %v", ctx.Err())
			}
		})
	}
}

func passthroughTestHooks(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return response, err
}

func TestPassthroughStreamCancellation(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	defer ctx.Cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	stream := StreamPassthroughHTTP(ctx, passthroughTestHooks, nil, reader,
		PassthroughStreamParams{StatusCode: 200, StartTime: time.Now(), UseTerminalDetector: true})
	ctx.Cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, open := <-stream:
			if !open {
				if ctx.Err() != context.Canceled {
					t.Fatalf("cancellation lost: %v", ctx.Err())
				}
				return
			}
		case <-timer.C:
			t.Fatal("cancelled passthrough stream did not close")
		}
	}
}

func BenchmarkTerminalPayload(b *testing.B) {
	for _, tc := range terminalPayloadCases {
		b.Run(tc.name, func(b *testing.B) {
			payload := []byte(tc.payload)
			b.ReportAllocs()
			for b.Loop() {
				if hasFinishReasonMarker(payload) != tc.terminal {
					b.Fatal("terminal decision changed")
				}
			}
		})
	}
}

func BenchmarkPassthroughTerminalStream(b *testing.B) {
	var body strings.Builder
	for range 256 {
		body.WriteString("data: " + markerFreeStreamPayload + "\n\n")
	}
	body.WriteString("data: {\"finishReason\":\"STOP\"}\n\n")
	data := []byte(body.String())
	for _, detect := range []bool{true, false} {
		name := "enabled"
		if !detect {
			name = "disabled"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
				stream := StreamPassthroughHTTP(ctx, passthroughTestHooks, nil,
					io.NopCloser(bytes.NewReader(data)), PassthroughStreamParams{
						StatusCode: 200, StartTime: time.Now(), UseTerminalDetector: detect,
					})
				forwarded := 0
				for chunk := range stream {
					if chunk.BifrostError != nil || chunk.BifrostPassthroughResponse == nil {
						b.Fatalf("unexpected chunk: %+v", chunk)
					}
					forwarded += len(chunk.BifrostPassthroughResponse.Body)
				}
				if forwarded != len(data) {
					b.Fatalf("forwarded %d/%d bytes", forwarded, len(data))
				}
			}
		})
	}
}

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

func TestStreamTerminalDetectorObserveChunkAnthropicMessageStop(t *testing.T) {
	detector := &StreamTerminalDetector{}
	chunks := [][]byte{
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"),
		[]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":522,\"output_tokens\":13}}\n\n"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
	}
	if detector.ObserveChunk(chunks[0]) {
		t.Fatalf("unexpected terminal detection on content_block_stop")
	}
	if detector.ObserveChunk(chunks[1]) {
		t.Fatalf("unexpected terminal detection on message_delta (usage must still be observed)")
	}
	if !detector.ObserveChunk(chunks[2]) {
		t.Fatalf("expected terminal detection for anthropic message_stop")
	}
}

func TestStreamTerminalDetectorObserveChunkAnthropicMessageStopSplitAcrossChunks(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("event: message_stop\ndata: {\"type\":")) {
		t.Fatalf("unexpected terminal detection on partial frame")
	}
	if !detector.ObserveChunk([]byte("\"message_stop\"}\n\n")) {
		t.Fatalf("expected terminal detection for split anthropic message_stop")
	}
}

// Vertex returns errors as a streamed JSON array with the body left open; without terminal
// detection the stream hangs until the idle timeout (issue #6073).
func TestStreamTerminalDetectorObserveChunkVertexErrorArray(t *testing.T) {
	detector := &StreamTerminalDetector{}
	body := []byte("[{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Publisher Model is not servable in region us-central1.\",\n    \"status\": \"FAILED_PRECONDITION\"\n  }\n}\n]")
	if !detector.ObserveChunk(body) {
		t.Fatalf("expected terminal detection for vertex error array")
	}
}

func TestStreamTerminalDetectorObserveChunkGoogleErrorObject(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if !detector.ObserveChunk([]byte("data: {\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"message\":\"quota\"}}\n\n")) {
		t.Fatalf("expected terminal detection for google error object")
	}
}

func TestStreamTerminalDetectorObserveChunkPlainErrorStringIsNotTerminal(t *testing.T) {
	detector := &StreamTerminalDetector{}
	if detector.ObserveChunk([]byte("data: {\"error\":\"\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n")) {
		t.Fatalf("unexpected terminal detection for a non-envelope error field")
	}
}
