package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// The provider stream loops decoded every event with sonic.Unmarshal before
// decodeAnthropicStreamEvent took a fast path for the common shapes. These
// tests pin that, for any payload, the result is exactly what sonic produces:
// the same decoded value, or an error exactly when sonic errors.

// sonicDecodeStreamEvent is the reference: the decode the loops used before.
func sonicDecodeStreamEvent(data []byte) (AnthropicStreamEvent, error) {
	var event AnthropicStreamEvent
	err := sonic.Unmarshal(data, &event)
	return event, err
}

// streamDecodeEdgeCases are payloads around the fast path's boundary: shapes it
// takes, shapes it must hand to sonic, and malformed input.
var streamDecodeEdgeCases = []string{
	// Taken by the fast path.
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	" {\n\t\"type\" : \"content_block_delta\" ,\r\n \"index\" : 3 , \"delta\" : { \"type\" : \"text_delta\" , \"text\" : \"a b\" } } ",
	`{"delta":{"text":"reordered","type":"text_delta"},"index":7,"type":"content_block_delta"}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"esc \" \\ \/ \b \f \n \r \t end"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"raw utf-8: café 日本 😀"}}`,
	"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"invalid utf-8: \xff\xfe \xe2\x82 end\"}}",
	"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"del \x7f\"}}",
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\": \"Paris\"}"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCkYIBhgCKkBz9fJ3x0Qm"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"some_future_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":999999999,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","delta":{"type":"text_delta","text":"no index"}}`,
	`{"type":"content_block_delta","index":0}`,
	`{"type":"content_block_delta","index":0,"delta":{}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"ping"}`,
	`{"type":"message_stop"}`,
	`{"type":""}`,
	`{"index":0}`,
	`{}`,
	`{ }`,
	// Handed to sonic: escapes and numbers outside the subset.
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"caf\u00e9 \ud83d\ude00"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lone \ud800 surrogate"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"nul \u0000"}}`,
	`{"type":"content_block_delt\u0061","index":0,"delta":{"type":"text_delta","text":"x"}}`,
	`{"typ\u0065":"content_block_delta","index":0}`,
	`{"type":"content_block_delta","index":1234567890,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":-1,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":01,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":1.0,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":1e2,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":"0","delta":{"type":"text_delta","text":"x"}}`,
	// Handed to sonic: nulls, duplicates, other keys, other casing.
	`{"type":"content_block_delta","index":null,"delta":{"type":"text_delta","text":"x"}}`,
	`{"type":"content_block_delta","index":0,"delta":null}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":null}}`,
	`{"type":null}`,
	`{"type":"content_block_delta","type":"ping"}`,
	`{"type":"content_block_delta","index":0,"index":1}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a","text":"b"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","type":"thinking_delta","text":"a"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a","thinking":"b"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"},"delta":{"type":"text_delta","text":"b"}}`,
	`{"Type":"content_block_delta","Index":0,"Delta":{"Type":"text_delta","Text":"x"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","TEXT":"x"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"},"extra":1}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x","citation":null}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x","document_index":0,"start_char_index":0,"end_char_index":1}}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"summary"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"},"safeguard_results":{"verdict":"allow"}}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":10}}`,
	`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal"}},"usage":{"output_tokens":3}}`,
	`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	// Malformed: sonic's error is the result.
	"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ctl \x01\"}}",
	"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"tab \t\"}}",
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bad \x escape"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"bad \a escape"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"trailing backslash \`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}x`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}{}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}},`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}`,
	`{"type":"content_block_delta"`,
	`{"type":"content_block_delta",}`,
	`{"type":"a","index":0,}`,
	`{"type":"a","delta":{"type":"b",}}`,
	`{,}`,
	`{"type"}`,
	`{"type":}`,
	`{"index":}`,
	`{"index":0x1}`,
	`{"index":0 1}`,
	`{'type':'ping'}`,
	`{"type":"ping"`,
	"\xef\xbb\xbf{\"type\":\"ping\"}",
	`[]`,
	`"content_block_delta"`,
	`null`,
	`1`,
	``,
	` `,
	`{`,
	`}`,
}

// streamDecodeCorpus is every distinct fixture payload plus the edge cases.
func streamDecodeCorpus() [][]byte {
	var corpus [][]byte
	seen := make(map[string]bool)
	add := func(payload []byte) {
		if !seen[string(payload)] {
			seen[string(payload)] = true
			corpus = append(corpus, payload)
		}
	}
	for _, fx := range streamFixtures() {
		for _, payload := range fx.payloads() {
			add(payload)
		}
	}
	for _, payload := range streamDecodeEdgeCases {
		add([]byte(payload))
	}
	return corpus
}

// assertDecodeMatchesSonic fails if decodeAnthropicStreamEvent's result for
// payload differs from sonic's.
func assertDecodeMatchesSonic(t *testing.T, payload []byte) {
	t.Helper()
	want, wantErr := sonicDecodeStreamEvent(payload)
	var got AnthropicStreamEvent
	gotErr := decodeAnthropicStreamEvent(payload, &got)
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("payload %q: error = %v, sonic error = %v", payload, gotErr, wantErr)
	}
	if wantErr != nil {
		if gotErr.Error() != wantErr.Error() {
			t.Fatalf("payload %q: error %q, sonic error %q", payload, gotErr, wantErr)
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := sonic.Marshal(got)
		wantJSON, _ := sonic.Marshal(want)
		t.Fatalf("payload %q:\n got  %+v\n      %s\n want %+v\n      %s", payload, got, gotJSON, want, wantJSON)
	}
}

func TestDecodeAnthropicStreamEvent_MatchesSonic(t *testing.T) {
	t.Parallel()
	for _, payload := range streamDecodeCorpus() {
		assertDecodeMatchesSonic(t, payload)
	}
}

// The decoded value must not alias the payload: the SSE reader hands each
// event its own buffer today, but a decoded string must stay valid if the
// buffer is reused.
func TestDecodeAnthropicStreamEvent_DoesNotAliasPayload(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"hello"}}`)
	var event AnthropicStreamEvent
	if err := decodeAnthropicStreamEvent(payload, &event); err != nil {
		t.Fatal(err)
	}
	for i := range payload {
		payload[i] = 'x'
	}
	if event.Type != AnthropicStreamEventTypeContentBlockDelta || event.Index == nil || *event.Index != 4 ||
		event.Delta == nil || event.Delta.Type != AnthropicStreamDeltaTypeText || event.Delta.Text == nil || *event.Delta.Text != "hello" {
		t.Fatalf("decoded event changed with its payload: %+v", event)
	}
}

// The fast path must actually carry the hot events, or the change is inert.
func TestDecodeAnthropicStreamEvent_FastPathCoversDeltas(t *testing.T) {
	t.Parallel()
	for _, fx := range streamFixtures() {
		var deltas, fast int
		for _, payload := range fx.payloads() {
			if !strings.Contains(string(payload), `"content_block_delta"`) {
				continue
			}
			deltas++
			var event AnthropicStreamEvent
			if decodeAnthropicStreamEventFast(payload, &event) {
				fast++
			}
		}
		// Fixture deltas outside the fast path: \u escapes in text and citations_delta.
		if deltas > 0 && fast*100 < deltas*90 {
			t.Errorf("%s: fast path decoded %d of %d content_block_delta events, want >= 90%%", fx.name, fast, deltas)
		}
	}
}

// The fast path never writes to event when it declines a payload, so sonic
// decodes into the zero value the loop handed in.
func TestDecodeAnthropicStreamEventFast_DeclineLeavesEventZero(t *testing.T) {
	t.Parallel()
	for _, payload := range streamDecodeCorpus() {
		var event AnthropicStreamEvent
		if !decodeAnthropicStreamEventFast(payload, &event) && !reflect.DeepEqual(event, AnthropicStreamEvent{}) {
			t.Fatalf("payload %q: declined but wrote %+v", payload, event)
		}
	}
}

func FuzzDecodeAnthropicStreamEvent(f *testing.F) {
	for _, payload := range streamDecodeCorpus() {
		f.Add(payload)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		assertDecodeMatchesSonic(t, payload)
	})
}

// Old vs new over whole streams: every fixture decoded with sonic and with
// decodeAnthropicStreamEvent, then converted event by event with
// ToBifrostChatCompletionStream (one stream state each), yields the same
// chunks, errors and terminal flags.
func TestAnthropicChatStreamConversion_SameAsSonicDecode(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, fx := range streamFixtures() {
		wantState, gotState := NewAnthropicStreamState(), NewAnthropicStreamState()
		for i, payload := range fx.payloads() {
			wantEvent, err := sonicDecodeStreamEvent(payload)
			if err != nil {
				t.Fatalf("%s event %d: sonic: %v", fx.name, i, err)
			}
			var gotEvent AnthropicStreamEvent
			if err := decodeAnthropicStreamEvent(payload, &gotEvent); err != nil {
				t.Fatalf("%s event %d: decode: %v", fx.name, i, err)
			}
			wantResp, wantErr, wantLast := wantEvent.ToBifrostChatCompletionStream(ctx, "", wantState)
			gotResp, gotErr, gotLast := gotEvent.ToBifrostChatCompletionStream(ctx, "", gotState)
			if !reflect.DeepEqual(gotResp, wantResp) || !reflect.DeepEqual(gotErr, wantErr) || gotLast != wantLast {
				t.Fatalf("%s event %d %q: conversion differs:\n got  %+v %+v %v\n want %+v %+v %v",
					fx.name, i, payload, gotResp, gotErr, gotLast, wantResp, wantErr, wantLast)
			}
			wantJSON, _ := sonic.Marshal(wantResp)
			gotJSON, _ := sonic.Marshal(gotResp)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("%s event %d: encoded chunk differs:\n got  %s\n want %s", fx.name, i, gotJSON, wantJSON)
			}
		}
	}
}

// End to end through HandleAnthropicChatCompletionStreaming (which reuses one
// event value per stream): the chunks it sends carry exactly the choices the
// reference pipeline (sonic decode + conversion) produces, in order, with the
// raw event payload when raw responses are requested, followed by the final
// usage chunk, or by the error for the error fixture.
func TestHandleAnthropicChatCompletionStreaming_SameChunksAsSonicDecode(t *testing.T) {
	t.Parallel()
	jsonBody := []byte(`{"model":"claude-sonnet-4-5-20250929","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`)
	for _, fx := range streamFixtures() {
		// Reference: the choices and raw payloads of every chunk the loop sends.
		type wantChunk struct {
			choices []schemas.BifrostResponseChoice
			raw     string
		}
		var want []wantChunk
		var wantErr *schemas.BifrostError
		refCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		refState := NewAnthropicStreamState()
		for _, payload := range fx.payloads() {
			event, err := sonicDecodeStreamEvent(payload)
			if err != nil {
				t.Fatalf("%s: sonic: %v", fx.name, err)
			}
			resp, bErr, last := event.ToBifrostChatCompletionStream(refCtx, "", refState)
			if bErr != nil {
				wantErr = bErr
				break
			}
			if resp != nil {
				want = append(want, wantChunk{choices: resp.Choices, raw: string(payload)})
			}
			if last {
				break
			}
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fx.sse)
		}))
		t.Cleanup(server.Close)
		for _, sendRaw := range []bool{false, true} {
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			stream, bifrostErr := HandleAnthropicChatCompletionStreaming(ctx, &fasthttp.Client{}, server.URL+"/v1/messages", jsonBody,
				map[string]string{}, nil, 30, nil, false, sendRaw, schemas.Anthropic,
				truncationPassthroughPostHook, nil, nil, truncationTestLogger{}, nil)
			if bifrostErr != nil {
				t.Fatalf("%s: stream: %v", fx.name, bifrostErr.Error.Message)
			}
			chunks := collectTruncationChunks(t, stream)
			ctx.Cancel()

			wantLen := len(want) + 1 // + final usage chunk, or the error
			if len(chunks) != wantLen {
				t.Fatalf("%s raw=%v: %d chunks, want %d", fx.name, sendRaw, len(chunks), wantLen)
			}
			for i, w := range want {
				got := chunks[i].BifrostChatResponse
				if got == nil {
					t.Fatalf("%s raw=%v chunk %d: no chat response (error %+v)", fx.name, sendRaw, i, chunks[i].BifrostError)
				}
				if !reflect.DeepEqual(got.Choices, w.choices) {
					t.Fatalf("%s raw=%v chunk %d: choices differ:\n got  %+v\n want %+v", fx.name, sendRaw, i, got.Choices, w.choices)
				}
				if got.ID != "msg_01XyZaBcDeFgHiJkLmNoPqRs" || got.ExtraFields.ChunkIndex != i {
					t.Fatalf("%s raw=%v chunk %d: id %q chunk index %d", fx.name, sendRaw, i, got.ID, got.ExtraFields.ChunkIndex)
				}
				var gotRaw string
				if got.ExtraFields.RawResponse != nil {
					gotRaw, _ = got.ExtraFields.RawResponse.(string)
				}
				if sendRaw && gotRaw != w.raw || !sendRaw && got.ExtraFields.RawResponse != nil {
					t.Fatalf("%s raw=%v chunk %d: raw response %q, want %q", fx.name, sendRaw, i, gotRaw, w.raw)
				}
			}
			tail := chunks[len(chunks)-1]
			if wantErr != nil {
				if tail.BifrostError == nil || tail.BifrostError.Error == nil || tail.BifrostError.Error.Message != wantErr.Error.Message {
					t.Fatalf("%s raw=%v: last chunk %+v, want error %q", fx.name, sendRaw, tail, wantErr.Error.Message)
				}
				continue
			}
			if tail.BifrostChatResponse == nil || tail.BifrostChatResponse.Usage == nil || tail.BifrostChatResponse.Usage.PromptTokensDetails == nil {
				t.Fatalf("%s raw=%v: final chunk carries no usage: %+v", fx.name, sendRaw, tail)
			}
		}
	}
}
