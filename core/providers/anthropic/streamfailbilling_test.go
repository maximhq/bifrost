package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Every exit of a failed Anthropic stream bills what the provider already
// consumed, the same way: the cache-folded usage as the error's BilledUsage,
// carrying the served service tier so the bare-usage billing path can apply
// the tier's rate. Covers the chat and the Responses stream loops.

// anthropicTieredMessageStart is anthropicCachedMessageStart served on the
// priority tier.
const anthropicTieredMessageStart = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_repro","type":"message","role":"assistant","model":"claude-repro","content":[],` +
	`"usage":{"input_tokens":5,"output_tokens":0,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"service_tier":"priority"}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"

// anthropicOverloadedEvent is an in-stream Anthropic error event.
const anthropicOverloadedEvent = "event: error\n" +
	`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"

// errorCapture is a post-hook that records every error it sees: what billing
// plugins read, independent of whether the chunk reaches a cancelled reader.
type errorCapture struct {
	mu   sync.Mutex
	errs []*schemas.BifrostError
}

func (c *errorCapture) hook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	if err != nil {
		c.mu.Lock()
		c.errs = append(c.errs, err)
		c.mu.Unlock()
	}
	return resp, err
}

func (c *errorCapture) only(t *testing.T) *schemas.BifrostError {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.errs) != 1 {
		t.Fatalf("expected exactly one stream error, got %d: %+v", len(c.errs), c.errs)
	}
	return c.errs[0]
}

// anthropicMalformedChunkServer serves prelude as one HTTP chunk and then a
// chunk header that is not hex, so the body read fails with a non-EOF error.
func anthropicMalformedChunkServer(t *testing.T, prelude string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server ResponseWriter is not an http.Hijacker")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		_, _ = fmt.Fprintf(buf, "%x\r\n%s\r\n", len(prelude), prelude)
		_, _ = buf.WriteString("zz\r\n") // not a hex chunk size
		_ = buf.Flush()
	}))
}

// anthropicHeldServer serves prelude and holds the stream open until the
// request goes away or the test ends.
func anthropicHeldServer(t *testing.T, prelude string) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(prelude))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server
}

type anthropicStreamAPI string

const (
	anthropicChatStream      anthropicStreamAPI = "chat"
	anthropicResponsesStream anthropicStreamAPI = "responses"
)

func openAnthropicStream(t *testing.T, api anthropicStreamAPI, ctx *schemas.BifrostContext, baseURL string, hook schemas.PostHookRunner) chan *schemas.BifrostStreamChunk {
	t.Helper()
	provider := newTruncationTestProvider(baseURL)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
	var (
		stream chan *schemas.BifrostStreamChunk
		err    *schemas.BifrostError
	)
	switch api {
	case anthropicChatStream:
		stream, err = provider.ChatCompletionStream(ctx, hook, nil, key, truncationChatRequest())
	case anthropicResponsesStream:
		stream, err = provider.ResponsesStream(ctx, hook, nil, key, &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-repro",
			Input: []schemas.ResponsesMessage{{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
		})
	}
	if err != nil {
		t.Fatalf("stream setup failed: %+v", err)
	}
	return stream
}

func assertServedTierBilled(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	assertCacheFoldedBilledUsage(t, err)
	tier := err.ExtraFields.BilledUsage.ServiceTier
	if tier == nil || *tier != schemas.BifrostServiceTierPriority {
		t.Errorf("billed service tier = %v, want %q", tier, schemas.BifrostServiceTierPriority)
	}
}

func TestAnthropicStreamFailedExitsBillServedUsage(t *testing.T) {
	for _, api := range []anthropicStreamAPI{anthropicChatStream, anthropicResponsesStream} {
		t.Run(string(api), func(t *testing.T) {
			t.Run("error_event", func(t *testing.T) {
				server := anthropicSSEServer(t, anthropicTieredMessageStart+anthropicTextDelta+anthropicOverloadedEvent, false)
				defer server.Close()
				capture := &errorCapture{}
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				collectTruncationChunks(t, openAnthropicStream(t, api, ctx, server.URL, capture.hook))
				err := capture.only(t)
				if err.Error == nil || err.Error.Message != "Overloaded" {
					t.Fatalf("expected the upstream error event, got %+v", err.Error)
				}
				assertServedTierBilled(t, err)
			})
			t.Run("read_error", func(t *testing.T) {
				server := anthropicMalformedChunkServer(t, anthropicTieredMessageStart+anthropicTextDelta)
				defer server.Close()
				capture := &errorCapture{}
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				collectTruncationChunks(t, openAnthropicStream(t, api, ctx, server.URL, capture.hook))
				err := capture.only(t)
				if err.Error == nil || err.Error.Message == schemas.ErrProviderStreamTruncated {
					t.Fatalf("expected a stream read error, got %+v", err.Error)
				}
				assertServedTierBilled(t, err)
			})
			t.Run("truncated", func(t *testing.T) {
				server := anthropicSSEServer(t, anthropicTieredMessageStart+anthropicTextDelta, true)
				defer server.Close()
				capture := &errorCapture{}
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				collectTruncationChunks(t, openAnthropicStream(t, api, ctx, server.URL, capture.hook))
				err := capture.only(t)
				assertAnthropicTruncationError(t, err)
				assertServedTierBilled(t, err)
			})
			t.Run("cancel", func(t *testing.T) {
				server := anthropicHeldServer(t, anthropicTieredMessageStart+anthropicTextDelta)
				capture := &errorCapture{}
				ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
				defer cancel()
				stream := openAnthropicStream(t, api, ctx, server.URL, capture.hook)
				// The first forwarded chunk proves message_start's usage was read.
				select {
				case <-stream:
				case <-time.After(20 * time.Second):
					t.Fatal("timed out waiting for the first chunk")
				}
				cancel()
				collectTruncationChunks(t, stream)
				err := capture.only(t)
				if err.StatusCode == nil || *err.StatusCode != 499 {
					t.Fatalf("expected the cancellation error, got %+v", err)
				}
				assertServedTierBilled(t, err)
			})
			t.Run("timeout", func(t *testing.T) {
				server := anthropicHeldServer(t, anthropicTieredMessageStart+anthropicTextDelta)
				capture := &errorCapture{}
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 500*time.Millisecond)
				defer cancel()
				collectTruncationChunks(t, openAnthropicStream(t, api, ctx, server.URL, capture.hook))
				err := capture.only(t)
				if err.StatusCode == nil || *err.StatusCode != 504 {
					t.Fatalf("expected the timeout error, got %+v", err)
				}
				assertServedTierBilled(t, err)
			})
		})
	}
}
