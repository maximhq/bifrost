package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// This test lives in the core package, not beside the Anthropic provider, for one
// reason: ClearContextForInternalRequest is here. The provider's streaming
// prompt-usage witness is carried on the request context, and
// BifrostContext.Value reads THROUGH to a parent -- so a context derived to carry
// a plugin's internal sub-request sees the caller's witness. The sub-request's own
// chunk numbering restarts at zero, so its sequence numbers collide with the
// caller's, and a witness bound to a chunk number alone would strip the prompt
// counters the sub-request's own frame legitimately reported.
//
// The witness is therefore bound to the value store that recorded it, and this is
// the regression for that boundary: it drives the REAL streaming handler to record
// a real witness, derives a child the way an internal sub-request is derived,
// passes it through the REAL clearing helper, and asserts the child's own
// fully-reporting terminal frame keeps every counter -- while the owning request's
// frame still gets the correction.

const anthropicStreamUsageTurn = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// anthropicStreamUsageServer serves one fixed Anthropic SSE turn.
func anthropicStreamUsageServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server ResponseWriter is not an http.Flusher")
			return
		}
		flusher.Flush()
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("failed writing SSE body: %v", err)
			return
		}
		flusher.Flush()
	}))
}

// anthropicStreamUsageCtx is shaped like the context the /v1/messages integration
// runs with.
func anthropicStreamUsageCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	return ctx
}

// runAnthropicStreamUsageTurn drives the shipped Anthropic streaming handler on
// ctx and returns the sequence number of the neutral message_delta chunk it
// produced -- the chunk whose witness the handler recorded on ctx.
func runAnthropicStreamUsageTurn(t *testing.T, ctx *schemas.BifrostContext, baseURL string) int {
	t.Helper()
	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, NewDefaultLogger(schemas.LogLevelError))

	stream, bifrostErr := provider.ResponsesStream(ctx,
		func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return resp, err
		}, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")},
		&schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-opus-5-5",
			Input: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
		})
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	sequenceNumber := -1
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				if sequenceNumber < 0 {
					t.Fatal("the handler produced no message_delta chunk")
				}
				return sequenceNumber
			}
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				t.Fatalf("unexpected stream error: %+v", chunk.BifrostError)
			}
			if resp := chunk.BifrostResponsesStreamResponse; resp != nil && resp.Type == "message_delta" {
				sequenceNumber = resp.SequenceNumber
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for the provider stream to close")
			return sequenceNumber
		}
	}
}

// subRequestTerminalFrame renders a message_delta chunk that reports every prompt
// counter -- 11 uncached + 2 written + 5 read -- as an internal sub-request's own
// terminal frame, and returns the client-visible usage bytes.
func subRequestTerminalFrame(t *testing.T, ctx *schemas.BifrostContext, sequenceNumber int) string {
	t.Helper()
	events := anthropic.ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
		Type:           "message_delta",
		SequenceNumber: sequenceNumber,
		Response: &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  18,
				OutputTokens: 3,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					CachedReadTokens:  5,
					CachedWriteTokens: 2,
				},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 client frame, got %d", len(events))
	}
	data, err := sonic.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal client frame: %v", err)
	}
	return string(data)
}

func TestClearContextForInternalRequestKeepsTheAnthropicStreamPromptUsageWitnessOwned(t *testing.T) {
	server := anthropicStreamUsageServer(t, anthropicStreamUsageTurn)
	defer server.Close()

	caller := anthropicStreamUsageCtx()
	sequenceNumber := runAnthropicStreamUsageTurn(t, caller, server.URL)

	// Anti-vacuity control: the witness the handler recorded DOES reshape the
	// owning request's own frame at that sequence number. Without this, every
	// assertion below would pass on a correction that never fires at all.
	t.Run("the owning request's frame is still corrected", func(t *testing.T) {
		frame := subRequestTerminalFrame(t, caller, sequenceNumber)
		for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			if gjson.Get(frame, "usage."+key).Exists() {
				t.Errorf("the upstream reported no %s; the owning request's frame must not invent one: %s", key, frame)
			}
		}
		if got := gjson.Get(frame, "usage.output_tokens"); !got.Exists() || got.Int() != 3 {
			t.Errorf("the terminal frame must still report output_tokens, got %s", frame)
		}
	})

	for _, tc := range []struct {
		name    string
		prepare func(child *schemas.BifrostContext)
	}{
		{name: "a plain derived child", prepare: func(*schemas.BifrostContext) {}},
		{
			name:    "a child through the real clearing helper",
			prepare: func(child *schemas.BifrostContext) { ClearContextForInternalRequest(child) },
		},
		{
			name: "a child through the real clearing helper with an allocated value map",
			prepare: func(child *schemas.BifrostContext) {
				// ClearValue only writes a nil shadow into an ALREADY allocated
				// value map, so the fresh-child and allocated-child shapes are
				// genuinely different code paths. Both must be isolated.
				child.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
				ClearContextForInternalRequest(child)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := schemas.NewBifrostContext(caller, schemas.NoDeadline)
			child.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
			tc.prepare(child)

			// The sub-request's own terminal frame, at the SAME sequence number the
			// caller's witness names -- chunk numbering restarts per request.
			frame := subRequestTerminalFrame(t, child, sequenceNumber)
			for key, want := range map[string]int64{
				"input_tokens":                11,
				"cache_creation_input_tokens": 2,
				"cache_read_input_tokens":     5,
				"output_tokens":               3,
			} {
				got := gjson.Get(frame, "usage."+key)
				if !got.Exists() {
					t.Errorf("the caller's witness crossed the boundary: usage.%s was dropped from the sub-request's own frame: %s", key, frame)
					continue
				}
				if got.Int() != want {
					t.Errorf("usage.%s is %s, want %d (%s)", key, got.Raw, want, frame)
				}
			}
		})
	}
}
