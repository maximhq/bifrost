package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// BenchmarkAnthropicStreamEventDecode measures the provider stream loop's
// per-event decode alone over each recorded-shape stream (one op = one
// stream). Divide by events/op for the per-event cost.
func BenchmarkAnthropicStreamEventDecode(b *testing.B) {
	for _, fx := range streamFixtures() {
		payloads := fx.payloads()
		b.Run(fx.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, payload := range payloads {
					var event AnthropicStreamEvent
					if err := decodeAnthropicStreamEvent(payload, &event); err != nil {
						b.Fatalf("decode %q: %v", payload, err)
					}
				}
			}
			b.ReportMetric(float64(len(payloads)), "events/op")
		})
	}
}

// BenchmarkAnthropicChatStreamHandler measures a whole chat stream through
// HandleAnthropicChatCompletionStreaming against a local upstream that writes
// the recorded stream in one Write: SSE read, per-event decode, conversion,
// post-hook and channel send, drained by the caller. One op = one stream.
func BenchmarkAnthropicChatStreamHandler(b *testing.B) {
	jsonBody := []byte(`{"model":"claude-sonnet-4-5-20250929","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`)
	for _, fx := range streamFixtures() {
		events := len(fx.payloads())
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fx.sse)
		}))
		b.Run(fx.name, func(b *testing.B) {
			client := &fasthttp.Client{}
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
				stream, bifrostErr := HandleAnthropicChatCompletionStreaming(ctx, client, server.URL+"/v1/messages", jsonBody,
					map[string]string{}, nil, 30, nil, false, false, schemas.Anthropic,
					truncationPassthroughPostHook, nil, nil, truncationTestLogger{}, nil)
				if bifrostErr != nil {
					b.Fatalf("stream: %v", bifrostErr.Error.Message)
				}
				for range stream {
				}
				ctx.Cancel()
			}
			b.ReportMetric(float64(events), "events/op")
		})
		server.Close()
	}
}
