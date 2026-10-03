package anthropic

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// These tests pin the SHAPE of a streamed Anthropic `message_delta` frame, as
// streamdeltausage_test.go pins the usage it reports. Two halves of the same
// frame: that file answers "which counters may it report", this one answers
// "is it a frame a client can consume at all".
//
// The defect they close: an upstream reporting its running output count on
// several message_delta frames sets no stop reason on the non-final ones, so
// the egress built no delta object and `delta,omitempty` dropped the key. The
// supported clients read `event.delta.stop_reason` UNGUARDED
// (@anthropic-ai/sdk 0.91.1, lib/MessageStream.js, case 'message_delta'), so
// they do not degrade on that frame -- they throw `Cannot read properties of
// undefined (reading 'stop_reason')` and abandon a turn whose content, order
// and usage were all correct. Reproduced against a running gateway with that
// client.

// clientDeltaFaults is a STRICTER mirror of a supported client: it reads
// delta.stop_reason and delta.stop_sequence off each `message_delta` WITHOUT
// checking `delta` is there. An ABSENT object makes that read an exception the
// client really raises; an object merely missing a required key reads as
// `undefined` and does NOT raise -- it is assigned, unconditionally, on every
// message_delta, so the LAST one decides the turn's stop state and an earlier
// reported reason does not survive a later omission -- so this refuses a shape
// that client tolerates on a non-final frame.
//
// It is a MIRROR and says so: it refuses the DECLARED frame, which is stricter
// than any one client's own exception behaviour. Driving a real client over
// HTTP+SSE is an integration concern and is not attempted here.
func clientDeltaFaults(frames []string) []string {
	var faults []string
	for i, frame := range frames {
		if gjson.Get(frame, "type").String() != "message_delta" {
			continue
		}
		position := strconv.Itoa(i)
		delta := gjson.Get(frame, "delta")
		if !delta.Exists() || !delta.IsObject() {
			faults = append(faults, "frame "+position+": reading delta.stop_reason of an absent delta object raises: "+frame)
			continue
		}
		for _, key := range []string{"stop_reason", "stop_sequence"} {
			if !delta.Get(key).Exists() {
				faults = append(faults, "frame "+position+": delta carries no "+key+", a required-and-nullable field a client reads as undefined: "+frame)
			}
		}
	}
	return faults
}

// cumulativeUpdateTurn is the upstream shape that reports a running output
// total on several message_delta frames: the stop state is null until the last
// one, and the prompt side is reported on none of them.
func cumulativeUpdateTurn() upstreamStream {
	return upstreamStream{
		streamUsageMessageStart100,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null},"usage":{"output_tokens":9}}`,
		`{"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null},"usage":{"output_tokens":19}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":29}}`,
		`{"type":"message_stop"}`,
	}
}

// assertDeltaFrameShape holds the protocol contract on one rendered
// `message_delta`: a delta OBJECT carrying both message-level stop fields,
// reporting the stop state the upstream itself reported -- null while the turn
// is still running, the real reason on the frame that ends it.
func assertDeltaFrameShape(t *testing.T, frame string, wantStopReason string) {
	t.Helper()
	delta := gjson.Get(frame, "delta")
	if !delta.Exists() || !delta.IsObject() {
		t.Fatalf("message_delta carries no delta object; the supported clients read delta.stop_reason unguarded and throw on this frame: %s", frame)
	}
	reason := delta.Get("stop_reason")
	if !reason.Exists() {
		t.Errorf("the delta object carries no stop_reason key, which the protocol declares required-and-nullable: %s", frame)
	} else if wantStopReason == "" {
		if reason.Type != gjson.Null {
			t.Errorf("a non-final update reported stop_reason %s; the upstream reported none and the turn had not ended: %s", reason.Raw, frame)
		}
	} else if reason.String() != wantStopReason {
		t.Errorf("the terminal frame reported stop_reason %s, want %q: %s", reason.Raw, wantStopReason, frame)
	}
	if !delta.Get("stop_sequence").Exists() {
		t.Errorf("the delta object carries no stop_sequence key, which the protocol declares required-and-nullable: %s", frame)
	}
}

// TestCumulativeMessageDeltasCarryTheRequiredDeltaObject is the reproduction,
// on the SHIPPED handler: a real SSE upstream read by provider.ResponsesStream,
// every chunk rendered as the transport renders it, and the chunks DRAINED
// before any frame is rendered -- the asynchronous ordering the live path has.
func TestCumulativeMessageDeltasCarryTheRequiredDeltaObject(t *testing.T) {
	const cumulativeTail = "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null},"usage":{"output_tokens":9}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null},"usage":{"output_tokens":19}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":29}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	server := anthropicSSEServer(t,
		streamUsageSSEStart(streamUsageMessageStart100)+streamUsageSSEBody+cumulativeTail, false)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := anthropicCtx()
	stream, bifrostErr := provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, streamUsageResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	var frames []string
	var terminal *schemas.ResponsesResponseUsage
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %+v", chunk.BifrostError)
		}
		resp := chunk.BifrostResponsesStreamResponse
		if resp == nil {
			continue
		}
		if resp.Type == schemas.ResponsesStreamResponseTypeCompleted && resp.Response != nil && resp.Response.Usage != nil {
			terminal = resp.Response.Usage
		}
		for _, out := range ToAnthropicResponsesStreamResponse(ctx, resp) {
			data, err := sonic.Marshal(out)
			if err != nil {
				t.Fatalf("marshal client frame: %v", err)
			}
			frames = append(frames, string(data))
		}
	}

	deltas := framesOfType(frames, "message_delta")
	if len(deltas) != 3 {
		t.Fatalf("expected the three upstream message_delta frames to be relayed, got %d:\n%s", len(deltas), strings.Join(frames, "\n"))
	}
	assertDeltaFrameShape(t, deltas[0], "")
	assertDeltaFrameShape(t, deltas[1], "")
	assertDeltaFrameShape(t, deltas[2], "end_turn")

	// The usage half of the same frames, unchanged by this correction: each
	// reports its OWN running output count and invents no prompt counter.
	for i, want := range []int64{9, 19, 29} {
		if got := gjson.Get(deltas[i], "usage.output_tokens"); !got.Exists() || got.Int() != want {
			t.Errorf("message_delta %d must report output_tokens %d, got %s", i, want, deltas[i])
		}
		for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cache_creation"} {
			if gjson.Get(deltas[i], "usage."+key).Exists() {
				t.Errorf("message_delta %d reported no %s upstream; the client frame must not invent one: %s", i, key, deltas[i])
			}
		}
	}

	if faults := clientDeltaFaults(frames); len(faults) > 0 {
		t.Errorf("a supported client aborts this stream:\n%s", strings.Join(faults, "\n"))
	}

	client := accumulateClientUsage(frames)
	for key, want := range map[string]int64{"input_tokens": 100, "output_tokens": 29} {
		if client[key] != want {
			t.Errorf("client's final %s is %d, want %d\nframes:\n%s", key, client[key], want, strings.Join(frames, "\n"))
		}
	}
	if terminal == nil {
		t.Fatal("no terminal accumulated usage reached the stream consumer")
	}
	if terminal.InputTokens != 100 || terminal.OutputTokens != 29 {
		t.Fatalf("accounting usage is input=%d output=%d; want 100/29 -- several usage frames are one turn", terminal.InputTokens, terminal.OutputTokens)
	}
}

// TestMessageDeltaWithoutAnUpstreamDeltaObjectIsServed covers the malformed
// upstream shape: a `message_delta` carrying usage and NO delta object, which
// the protocol does not allow but an adapter can still send.
//
// Reading that object unconditionally made the shape fatal rather than merely
// wrong: the Anthropic streaming goroutine has no recover(), so the nil
// dereference took the whole gateway process down. The event's
// usage is worth relaying and the egress supplies the stop fields the client
// requires, so the turn is served -- reporting what the upstream reported, and
// nothing it did not.
func TestMessageDeltaWithoutAnUpstreamDeltaObjectIsServed(t *testing.T) {
	ctx := anthropicCtx()
	frames, accumulated := relayTurn(t, ctx, upstreamStream{
		streamUsageMessageStart100,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","usage":{"output_tokens":9}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}`,
		`{"type":"message_stop"}`,
	})

	deltas := framesOfType(frames, "message_delta")
	if len(deltas) != 2 {
		t.Fatalf("expected both message_delta frames to be relayed, got %d:\n%s", len(deltas), strings.Join(frames, "\n"))
	}
	assertDeltaFrameShape(t, deltas[0], "")
	assertDeltaFrameShape(t, deltas[1], "end_turn")
	if faults := clientDeltaFaults(frames); len(faults) > 0 {
		t.Errorf("a supported client aborts this stream:\n%s", strings.Join(faults, "\n"))
	}
	if got := gjson.Get(deltas[0], "usage.output_tokens"); !got.Exists() || got.Int() != 9 {
		t.Errorf("the relayed frame must still report the output count the upstream sent, got %s", deltas[0])
	}
	if accumulated.OutputTokens != 25 || accumulated.InputTokens != 100 {
		t.Errorf("accounting usage is input=%d output=%d; want 100/25", accumulated.InputTokens, accumulated.OutputTokens)
	}
}

// TestMessageDeltaStopMetadataStillCarriesTheRequiredStopFields covers the branch
// an independent review round found still open: the egress supplies the delta
// object when NOTHING ELSE built one, but `stop_details`, a sandbox `container`
// and a text delta each allocate one of their own. On a frame that carries any of
// those and no stop reason -- every non-final frame of an upstream reporting
// refusal detail or a container alongside its running usage -- the object existed
// and the unflagged marshaller omitted `stop_reason,omitempty`, so a client reads
// the required field as `undefined` and assigns THAT as its stop state -- the
// turn's own unless a later message_delta reports one, since that assignment is
// unconditional on every such event. The exception belongs to the ABSENT object
// above; this is the omission.
//
// The chunks are built directly rather than read off an upstream because the
// subject is the egress branch: this is the one place the neutral chunk can
// carry that metadata without a stop reason.
func TestMessageDeltaStopMetadataStillCarriesTheRequiredStopFields(t *testing.T) {
	expires := "2026-10-01T00:00:00Z"
	for _, probe := range []struct {
		name  string
		chunk *schemas.BifrostResponsesStreamResponse
	}{
		{
			name: "stop_details with no stop reason",
			chunk: &schemas.BifrostResponsesStreamResponse{
				Type: "message_delta",
				Response: &schemas.BifrostResponsesResponse{
					StopDetails: &schemas.ResponsesStopDetails{Type: "refusal"},
				},
			},
		},
		{
			name: "a sandbox container with no stop reason",
			chunk: &schemas.BifrostResponsesStreamResponse{
				Type: "message_delta",
				Response: &schemas.BifrostResponsesResponse{
					Container: &schemas.ResponsesResponseContainer{ID: "container_x", ExpiresAt: &expires},
				},
			},
		},
		{
			name: "a text delta with no stop reason",
			chunk: &schemas.BifrostResponsesStreamResponse{
				Type:  "message_delta",
				Delta: schemas.Ptr("hi"),
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			ctx := anthropicCtx()
			var frames []string
			for _, out := range ToAnthropicResponsesStreamResponse(ctx, probe.chunk) {
				data, err := sonic.Marshal(out)
				if err != nil {
					t.Fatalf("marshal client frame: %v", err)
				}
				frames = append(frames, string(data))
			}
			deltas := framesOfType(frames, "message_delta")
			if len(deltas) != 1 {
				t.Fatalf("expected exactly one rendered message_delta, got %d: %s", len(deltas), strings.Join(frames, "\n"))
			}
			// Both required fields, the stop reason reported as the null the
			// upstream reported -- never a fabricated end_turn.
			assertDeltaFrameShape(t, deltas[0], "")
			if faults := clientDeltaFaults(frames); len(faults) > 0 {
				t.Errorf("a supported client aborts this stream:\n%s", strings.Join(faults, "\n"))
			}
			// And the metadata the branch was building the object FOR is still
			// there: the stop fields are added beside it, never instead of it.
			switch probe.name {
			case "stop_details with no stop reason":
				if got := gjson.Get(deltas[0], "delta.stop_details.type"); got.String() != "refusal" {
					t.Errorf("the frame lost its stop_details: %s", deltas[0])
				}
			case "a sandbox container with no stop reason":
				if got := gjson.Get(deltas[0], "delta.container.id"); got.String() != "container_x" {
					t.Errorf("the frame lost its container: %s", deltas[0])
				}
			case "a text delta with no stop reason":
				if got := gjson.Get(deltas[0], "delta.text"); got.String() != "hi" {
					t.Errorf("the frame lost its text delta: %s", deltas[0])
				}
			}
		})
	}

	// A terminal frame that DOES report a stop reason alongside the same
	// metadata renders it, not a null: the flag only ever adds the key the
	// protocol requires when there is nothing to report.
	ctx := anthropicCtx()
	events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
		Type: "message_delta",
		Response: &schemas.BifrostResponsesResponse{
			StopReason:  schemas.Ptr("stop"),
			StopDetails: &schemas.ResponsesStopDetails{Type: "refusal"},
		},
	})
	if len(events) != 1 {
		t.Fatalf("expected one terminal event, got %d", len(events))
	}
	data, err := sonic.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal terminal frame: %v", err)
	}
	if got := gjson.GetBytes(data, "delta.stop_reason"); got.Type != gjson.String {
		t.Errorf("a terminal frame must report its own stop reason, got %s", data)
	}
}

// TestContentBlockDeltaBytesAreUnchanged is the stock-byte control on the
// frames that share AnthropicStreamDelta with the corrected one. The
// message-level stop fields must appear on `message_delta` and NOWHERE else:
// dropping `omitempty` from StopReason would have added `"stop_reason":null`
// to every content delta of every Anthropic stream, which is why this
// correction is a flag set at one call site rather than a tag change.
func TestContentBlockDeltaBytesAreUnchanged(t *testing.T) {
	ctx := anthropicCtx()
	frames, _ := relayTurn(t, ctx, mockProviderTurn())

	const wantStart = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	const wantDelta = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi","stop_sequence":null}}`
	const wantStop = `{"type":"content_block_stop","index":0}`
	if got := frameOfType(t, frames, "content_block_start"); got != wantStart {
		t.Errorf("content_block_start bytes changed:\n got %s\nwant %s", got, wantStart)
	}
	if got := frameOfType(t, frames, "content_block_delta"); got != wantDelta {
		t.Errorf("content_block_delta bytes changed:\n got %s\nwant %s", got, wantDelta)
	}
	if got := frameOfType(t, frames, "content_block_stop"); got != wantStop {
		t.Errorf("content_block_stop bytes changed:\n got %s\nwant %s", got, wantStop)
	}
	// And the ordinary single-delta turn's terminal frame, which already
	// reported its own stop reason and must render exactly as it did.
	const wantTerminal = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}`
	if got := frameOfType(t, frames, "message_delta"); got != wantTerminal {
		t.Errorf("the terminal message_delta bytes changed:\n got %s\nwant %s", got, wantTerminal)
	}
}

// TestStreamDeltaMarshalIsStockWithoutTheFlag drives the rendering directly.
// Every delta object Bifrost builds for a content block, a thinking block, a
// tool call or a terminal frame is unflagged, and must render exactly as the
// encoder's own field rendering does -- the definition of "byte-identical to
// stock" for this type.
func TestStreamDeltaMarshalIsStockWithoutTheFlag(t *testing.T) {
	for _, delta := range []AnthropicStreamDelta{
		{},
		{Type: AnthropicStreamDeltaTypeText, Text: schemas.Ptr("hi")},
		{Type: AnthropicStreamDeltaTypeInputJSON, PartialJSON: schemas.Ptr(`{"a":1}`)},
		{Type: AnthropicStreamDeltaTypeThinking, Thinking: schemas.Ptr("because")},
		{StopReason: schemas.Ptr(AnthropicStopReasonEndTurn), StopSequence: schemas.Ptr("")},
		{StopReason: schemas.Ptr(AnthropicStopReasonEndTurn)},
	} {
		got, err := sonic.Marshal(delta)
		if err != nil {
			t.Fatalf("marshal %+v: %v", delta, err)
		}
		want, err := sonic.Marshal(anthropicStreamDeltaWire(delta))
		if err != nil {
			t.Fatalf("marshal wire %+v: %v", delta, err)
		}
		if string(got) != string(want) {
			t.Errorf("unflagged delta rendering changed:\n got %s\nwant %s", got, want)
		}
		if delta.StopReason == nil && gjson.GetBytes(got, "stop_reason").Exists() {
			t.Errorf("an unflagged delta with no stop reason must not report one: %s", got)
		}
	}
}

// TestStreamDeltaMarshalAddsOnlyTheRequiredNull pins what the flag does and
// does not do: it ADDS the one key the protocol requires, changes nothing
// else, and is inert on an object that reports a stop reason of its own.
func TestStreamDeltaMarshalAddsOnlyTheRequiredNull(t *testing.T) {
	got, err := sonic.Marshal(newAnthropicMessageDeltaStopFields())
	if err != nil {
		t.Fatalf("marshal flagged delta: %v", err)
	}
	reason := gjson.GetBytes(got, "stop_reason")
	if !reason.Exists() || reason.Type != gjson.Null {
		t.Errorf("a flagged delta with no stop reason must report stop_reason null, got %s", got)
	}
	sequence := gjson.GetBytes(got, "stop_sequence")
	if !sequence.Exists() || sequence.Type != gjson.Null {
		t.Errorf("a flagged delta must report stop_sequence null, got %s", got)
	}
	gjson.ParseBytes(got).ForEach(func(key, _ gjson.Result) bool {
		switch key.String() {
		case "stop_reason", "stop_sequence":
		default:
			t.Errorf("a flagged delta reported %q; it carries the two required stop fields and nothing else: %s", key.String(), got)
		}
		return true
	})

	// Inert when the frame reports a real stop reason: that path is the
	// terminal frame's, whose bytes are pinned above.
	flagged := AnthropicStreamDelta{requireStopFields: true, StopReason: schemas.Ptr(AnthropicStopReasonEndTurn)}
	withFlag, err := sonic.Marshal(flagged)
	if err != nil {
		t.Fatalf("marshal flagged delta with a stop reason: %v", err)
	}
	plain := flagged
	plain.requireStopFields = false
	withoutFlag, err := sonic.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal unflagged delta with a stop reason: %v", err)
	}
	if string(withFlag) != string(withoutFlag) {
		t.Errorf("the flag changed a delta that reports its own stop reason:\n got %s\nwant %s", withFlag, withoutFlag)
	}
}

// TestClientDeltaFaultsIsNotVacuous proves the mirror above refuses the
// exact bytes the host captured AND a present object missing a required
// key, which that client tolerates, and accepts the corrected frame.
func TestClientDeltaFaultsIsNotVacuous(t *testing.T) {
	if faults := clientDeltaFaults([]string{`{"type":"message_delta","usage":{"output_tokens":9}}`}); len(faults) != 1 {
		t.Errorf("the recorded defective frame must raise exactly one fault, got %v", faults)
	}
	if faults := clientDeltaFaults([]string{`{"type":"message_delta","delta":{"stop_sequence":null},"usage":{"output_tokens":9}}`}); len(faults) != 1 {
		t.Errorf("a delta object with no stop_reason must raise exactly one fault, got %v", faults)
	}
	if faults := clientDeltaFaults([]string{`{"type":"message_delta","delta":{"stop_sequence":null,"stop_reason":null},"usage":{"output_tokens":9}}`}); len(faults) != 0 {
		t.Errorf("the corrected frame must raise no fault, got %v", faults)
	}
	if faults := clientDeltaFaults([]string{`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi","stop_sequence":null}}`}); len(faults) != 0 {
		t.Errorf("a content_block_delta carries no message-level stop fields and must raise no fault, got %v", faults)
	}
}

// TestCumulativeUpdateTurnRelaysThroughTheConverters is the converter-level
// leg of the same property, so a failure points at the conversion rather than
// at the handler, and the shared fixture is proven non-vacuous: three deltas
// in, three deltas out.
func TestCumulativeUpdateTurnRelaysThroughTheConverters(t *testing.T) {
	ctx := anthropicCtx()
	frames, _ := relayTurn(t, ctx, cumulativeUpdateTurn())
	deltas := framesOfType(frames, "message_delta")
	if len(deltas) != 3 {
		t.Fatalf("expected three relayed message_delta frames, got %d:\n%s", len(deltas), strings.Join(frames, "\n"))
	}
	for i, want := range []string{"", "", "end_turn"} {
		assertDeltaFrameShape(t, deltas[i], want)
	}
	if faults := clientDeltaFaults(frames); len(faults) > 0 {
		t.Errorf("a supported client aborts this stream:\n%s", strings.Join(faults, "\n"))
	}
}
