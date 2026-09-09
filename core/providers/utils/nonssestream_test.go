package utils

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// errReader fails every read with a fixed error, standing in for a body stream
// that was closed or idled out before its first byte arrived.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestDrainNonSSEStream_JSONBodyIsCaptured(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.Header.SetContentType("application/json")

	body := `{"error":{"message":"something upstream","type":"server_error"}}`
	reader, info := DrainNonSSEStreamReader(resp, strings.NewReader(body))
	if reader != nil {
		t.Fatal("expected nil reader for a drained body")
	}
	if info == nil {
		t.Fatal("expected drain info for a JSON body")
	}
	if info.Kind != StreamBodyNonSSE {
		t.Fatalf("Kind: got %v, want StreamBodyNonSSE", info.Kind)
	}
	if info.ContentType != "application/json" {
		t.Fatalf("ContentType: got %q, want application/json", info.ContentType)
	}
	if string(info.Sample) != body {
		t.Fatalf("Sample: got %q, want %q", info.Sample, body)
	}
	// The original wording is load-bearing for existing log searches.
	if !strings.Contains(info.Err().Error(), "provider returned non-SSE response for streaming request") {
		t.Fatalf("Err() lost the original message: %v", info.Err())
	}
	if !strings.Contains(info.Err().Error(), "something upstream") {
		t.Fatalf("Err() should surface the captured body: %v", info.Err())
	}
}

func TestDrainNonSSEStream_SampleIsBoundedAndBodyFullyDrained(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.Header.SetContentType("application/json")

	// Far larger than the cap, and not SSE.
	body := bytes.Repeat([]byte("x"), nonSSESampleLimit*4)
	body[0] = '{'
	src := bytes.NewReader(body)

	_, info := DrainNonSSEStreamReader(resp, src)
	if info == nil {
		t.Fatal("expected drain info")
	}
	if len(info.Sample) != nonSSESampleLimit {
		t.Fatalf("Sample length: got %d, want %d", len(info.Sample), nonSSESampleLimit)
	}
	if src.Len() != 0 {
		t.Fatalf("body not fully drained: %d bytes left", src.Len())
	}
}

func TestDrainNonSSEStream_EmptyBody(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.Header.SetContentType("application/json")

	_, info := DrainNonSSEStreamReader(resp, errReader{err: io.EOF})
	if info == nil {
		t.Fatal("expected drain info for an empty body")
	}
	if info.Kind != StreamBodyEmpty {
		t.Fatalf("Kind: got %v, want StreamBodyEmpty", info.Kind)
	}
	if strings.Contains(info.Err().Error(), "non-SSE") {
		t.Fatalf("an empty body should not be reported as non-SSE: %v", info.Err())
	}
}

func TestDrainNonSSEStream_IdleTimeoutIsNotReportedAsNonSSE(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.Header.SetContentType("application/json")

	_, info := DrainNonSSEStreamReader(resp, errReader{err: ErrStreamIdleTimeout})
	if info == nil {
		t.Fatal("expected drain info for an idled-out body")
	}
	if info.Kind != StreamBodyIdleTimeout {
		t.Fatalf("Kind: got %v, want StreamBodyIdleTimeout", info.Kind)
	}
	if !errors.Is(info.Err(), ErrStreamIdleTimeout) {
		t.Fatalf("Err() should wrap ErrStreamIdleTimeout: %v", info.Err())
	}
	if strings.Contains(info.Err().Error(), "non-SSE") {
		t.Fatalf("an idle timeout should not be reported as non-SSE: %v", info.Err())
	}
}

func TestDrainNonSSEStream_ClosedStreamIsNotReportedAsNonSSE(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.Header.SetContentType("application/json")

	_, info := DrainNonSSEStreamReader(resp, errReader{err: ErrStreamClosed})
	if info == nil {
		t.Fatal("expected drain info for a closed body")
	}
	if info.Kind != StreamBodyClosed {
		t.Fatalf("Kind: got %v, want StreamBodyClosed", info.Kind)
	}
	if !errors.Is(info.Err(), ErrStreamClosed) {
		t.Fatalf("Err() should wrap ErrStreamClosed: %v", info.Err())
	}
	if strings.Contains(info.Err().Error(), "non-SSE") {
		t.Fatalf("a closed stream should not be reported as non-SSE: %v", info.Err())
	}
}

// Real upstreams have shipped all three of these. None is a reason to discard a
// stream that is otherwise well-formed SSE.
func TestHasSSEPrefix_ToleratesBOMWhitespaceAndCase(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "utf8 BOM", body: "\ufeffdata: {\"a\":1}\n\n"},
		{name: "leading space", body: " data: {\"a\":1}\n\n"},
		{name: "leading tab", body: "\tevent: created\n\n"},
		{name: "uppercase data", body: "DATA: {\"a\":1}\n\n"},
		{name: "mixed case event", body: "Event: created\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)

			reader, info := DrainNonSSEStreamReader(resp, strings.NewReader(tc.body))
			if info != nil {
				t.Fatalf("expected %s to be treated as SSE, drained as %v", tc.name, info.Kind)
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if string(got) != tc.body {
				t.Fatalf("body not preserved: got %q, want %q", got, tc.body)
			}
		})
	}
}

// TestProcessAndSendNonSSEStreamError_IsRetryable pins that a non-SSE 200 body
// is reported as an upstream failure, not an internal Bifrost error. The retry
// loop in executeRequestWithRetries peeks at the first stream chunk and breaks
// before any retry classification when IsBifrostError is set, so the flag
// decides whether the 502 ever reaches the transient-status retry path.
func TestProcessAndSendNonSSEStreamError_IsRetryable(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	passthrough := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return r, e
	}
	info := &NonSSEStreamBody{
		Kind:        StreamBodyNonSSE,
		ContentType: "application/json",
		Sample:      []byte(`{"error":{"message":"upstream hiccup"}}`),
	}
	out := make(chan *schemas.BifrostStreamChunk, 1)

	ProcessAndSendNonSSEStreamError(ctx, passthrough, info, out, getLogger(), nil)

	select {
	case chunk := <-out:
		if chunk == nil || chunk.BifrostError == nil {
			t.Fatalf("expected an error chunk, got %+v", chunk)
		}
		if chunk.BifrostError.IsBifrostError {
			t.Fatal("IsBifrostError must be false so the retry loop classifies the 502 instead of halting")
		}
		if chunk.BifrostError.StatusCode == nil || *chunk.BifrostError.StatusCode != fasthttp.StatusBadGateway {
			t.Fatalf("StatusCode: got %v, want 502", chunk.BifrostError.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no chunk emitted within 2s")
	}
}
