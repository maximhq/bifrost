package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// A "data: [DONE]" marker states that the stream finished normally. Emitting it
// after an error frame reproduces, one layer up, the same ambiguity that made
// truncated upstream streams look successful: a client keyed on the marker sees
// a clean end either way. See https://github.com/maximhq/bifrost/issues/5546.

// serveStreamingResponse runs handleStreamingResponse against an in-process
// connection and returns the raw SSE body the client would receive.
func serveStreamingResponse(t *testing.T, chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()

	handler := func(reqCtx *fasthttp.RequestCtx) {
		// A zero Config loads no transport plugins, so no chunk interceptor runs.
		h := &CompletionHandler{config: &lib.Config{}}

		base, cancel := context.WithCancel(context.Background())
		bifrostCtx := schemas.NewBifrostContext(base, schemas.NoDeadline)

		getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			stream := make(chan *schemas.BifrostStreamChunk, len(chunks))
			for _, chunk := range chunks {
				stream <- chunk
			}
			close(stream)
			return stream, nil
		}

		h.handleStreamingResponse(reqCtx, bifrostCtx, schemas.ChatCompletionStreamRequest, getStream, cancel)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		_ = fasthttp.ServeConn(serverConn, handler)
	}()

	if err := clientConn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	if _, err := clientConn.Write([]byte("POST /v1/chat/completions HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	br := bufio.NewReader(clientConn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read response header: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	var body strings.Builder
	for {
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read chunk size: %v", err)
		}
		var chunkSize int
		if _, err := fmt.Sscanf(strings.TrimSpace(sizeLine), "%x", &chunkSize); err != nil {
			t.Fatalf("failed to parse chunk size %q: %v", sizeLine, err)
		}
		if chunkSize == 0 {
			break
		}
		data := make([]byte, chunkSize+2) // +2 for the trailing CRLF
		for n := 0; n < len(data); {
			nn, err := br.Read(data[n:])
			if err != nil {
				t.Fatalf("failed to read chunk data: %v", err)
			}
			n += nn
		}
		body.Write(data[:chunkSize])
	}
	return body.String()
}

func contentChunk(text string) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ID:     "chatcmpl-repro",
			Object: "chat.completion.chunk",
			Model:  "repro-model",
			Choices: []schemas.BifrostResponseChoice{{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr(text)},
				},
			}},
		},
	}
}

func truncationErrorChunk() *schemas.BifrostStreamChunk {
	statusCode := 502
	errorType := schemas.ProviderConnectionFailed
	return &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderStreamTruncated,
				Type:    &errorType,
			},
		},
	}
}

// A stream that ends on an error frame is already terminated for the client;
// appending [DONE] would report it as a clean finish.
func TestStreamingResponseSkipsDoneAfterErrorChunk(t *testing.T) {
	body := serveStreamingResponse(t, []*schemas.BifrostStreamChunk{
		contentChunk("partial answer"),
		truncationErrorChunk(),
	})

	if !strings.Contains(body, schemas.ErrProviderStreamTruncated) {
		t.Errorf("expected the error frame to reach the client, got:\n%s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("[DONE] must not follow an error frame, got:\n%s", body)
	}
}

// Guard against over-suppression: a clean stream still gets its marker.
func TestStreamingResponseSendsDoneOnCleanStream(t *testing.T) {
	body := serveStreamingResponse(t, []*schemas.BifrostStreamChunk{
		contentChunk("hello"),
		contentChunk(" world"),
	})

	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("expected [DONE] to terminate a clean stream, got:\n%s", body)
	}
}

// Regression test for issue #8536, on the native /v1/responses route.
//
// A provider that emits a thinking signature reuses response.reasoning_summary_text.delta
// to carry it, setting Signature and leaving Delta nil. Delta is tagged
// `json:"delta,omitempty"`, and this route marshals the chunk verbatim rather than
// normalizing it, so the required `delta` field was dropped on the wire and strict OpenAI
// SDKs aborted the stream (openai-java: "`delta` is not set").
//
// This drives the real writer end to end over an in-process connection, so it asserts on
// the bytes a client actually receives rather than on the struct. The OpenAI drop-in
// routes normalize through BifrostResponsesStreamResponse.WithDefaults instead and are
// covered in core/schemas.

// serveResponsesStream runs handleStreamingResponse for a Responses stream and returns the
// raw SSE body a client would receive. It mirrors serveStreamingResponse in
// streamdonemarker_test.go, with the Responses request type so the `event:` lines are
// emitted.
func serveResponsesStream(t *testing.T, chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()

	handler := func(reqCtx *fasthttp.RequestCtx) {
		// A zero Config loads no transport plugins, so no chunk interceptor runs.
		h := &CompletionHandler{config: &lib.Config{}}

		base, cancel := context.WithCancel(context.Background())
		bifrostCtx := schemas.NewBifrostContext(base, schemas.NoDeadline)

		getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			stream := make(chan *schemas.BifrostStreamChunk, len(chunks))
			for _, chunk := range chunks {
				stream <- chunk
			}
			close(stream)
			return stream, nil
		}

		h.handleStreamingResponse(reqCtx, bifrostCtx, schemas.ResponsesStreamRequest, getStream, cancel)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		_ = fasthttp.ServeConn(serverConn, handler)
	}()

	if err := clientConn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	if _, err := clientConn.Write([]byte("POST /v1/responses HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	br := bufio.NewReader(clientConn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read response header: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	var body strings.Builder
	for {
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read chunk size: %v", err)
		}
		var chunkSize int
		if _, err := fmt.Sscanf(strings.TrimSpace(sizeLine), "%x", &chunkSize); err != nil {
			t.Fatalf("failed to parse chunk size %q: %v", sizeLine, err)
		}
		if chunkSize == 0 {
			break
		}
		data := make([]byte, chunkSize+2) // +2 for the trailing CRLF
		for n := 0; n < len(data); {
			nn, err := br.Read(data[n:])
			if err != nil {
				t.Fatalf("failed to read chunk data: %v", err)
			}
			n += nn
		}
		body.Write(data[:chunkSize])
	}
	return body.String()
}

func reasoningDeltaChunk(delta, signature *string) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{
		BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			SequenceNumber: 4,
			OutputIndex:    schemas.Ptr(0),
			SummaryIndex:   schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         schemas.Ptr("msg_abc_reasoning_0"),
			Delta:          delta,
			Signature:      signature,
		},
	}
}

// reasoningDeltaEvents pulls the decoded payload of every
// response.reasoning_summary_text.delta frame out of an SSE body.
func reasoningDeltaEvents(t *testing.T, body string) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, frame := range strings.Split(body, "\n\n") {
		if !strings.Contains(frame, "response.reasoning_summary_text.delta") {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
			if !ok {
				continue
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				t.Fatalf("failed to decode SSE payload %q: %v", payload, err)
			}
			if decoded["type"] == string(schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta) {
				out = append(out, decoded)
			}
		}
	}
	return out
}

func TestResponsesStreamSignatureOnlyReasoningDeltaCarriesDelta(t *testing.T) {
	body := serveResponsesStream(t, []*schemas.BifrostStreamChunk{
		reasoningDeltaChunk(schemas.Ptr("Working it out"), nil),
		reasoningDeltaChunk(nil, schemas.Ptr("EtUCCnkIEhABGAIqQ")),
	})

	events := reasoningDeltaEvents(t, body)
	if len(events) != 2 {
		t.Fatalf("expected 2 reasoning delta events, got %d (body: %q)", len(events), body)
	}

	// The text delta is untouched.
	if got := events[0]["delta"]; got != "Working it out" {
		t.Errorf("text delta changed: got %#v, want %q", got, "Working it out")
	}

	// The signature-only event is the one the issue is about. Checked for presence as
	// well as value: the defect was the key being absent, which a plain value compare
	// against "" would also satisfy via the zero value of a missing key.
	delta, present := events[1]["delta"]
	if !present {
		t.Fatalf("signature-only reasoning delta reached the wire with no `delta` field; "+
			"strict OpenAI SDKs abort the stream over this (event: %#v)", events[1])
	}
	if delta != "" {
		t.Errorf("delta = %#v, want the empty string", delta)
	}

	// The signature must still travel: it is additive to the OpenAI spec, and clients
	// already read it, so the fix must not trade one missing field for another.
	if got := events[1]["signature"]; got != "EtUCCnkIEhABGAIqQ" {
		t.Errorf("signature = %#v, want %q", got, "EtUCCnkIEhABGAIqQ")
	}
}

// TestResponsesStreamLeavesOtherEventsAlone guards against the fill becoming a blanket
// "every event gets a delta" rule, which would put a spurious empty delta on event types
// whose schema has no delta at all.
func TestResponsesStreamLeavesOtherEventsAlone(t *testing.T) {
	body := serveResponsesStream(t, []*schemas.BifrostStreamChunk{{
		BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
			SequenceNumber: 5,
			OutputIndex:    schemas.Ptr(0),
			ItemID:         schemas.Ptr("msg_abc_reasoning_0"),
		},
	}})

	if strings.Contains(body, `"delta"`) {
		t.Errorf("a non-delta event gained a `delta` field: %q", body)
	}
}
