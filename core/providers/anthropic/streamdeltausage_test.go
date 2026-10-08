package anthropic

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// These tests pin the client-visible usage of a streamed Anthropic turn.
//
// The defect they close: an upstream whose terminal message_delta reports only
// `output_tokens` (what Anthropic itself, and the subscription adapters in front
// of it, send) came back out of the Anthropic -> neutral Responses -> Anthropic
// round trip as a message_delta reporting `input_tokens: 0`. Accumulating
// clients -- the supported SDKs included -- treat each usage object as a
// snapshot and overwrite, so the prompt count the client had from message_start
// was erased. Bifrost's own accumulated usage, and therefore its accounting,
// was right the whole time, which is what made the disagreement silent.

// anthropicCtx returns a context shaped like the one the Anthropic /v1/messages
// integration runs with.
func anthropicCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	return ctx
}

// upstreamStream is one upstream SSE turn: raw event payloads in wire order.
// Raw strings, not structs, because field PRESENCE is the whole subject.
type upstreamStream []string

const streamUsageMessageStart100 = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`

// mockProviderTurn is the ordinary Anthropic-dialect upstream shape:
// message_start carries the prompt counters, and the terminal message_delta
// carries only the final output count.
func mockProviderTurn() upstreamStream {
	return upstreamStream{
		streamUsageMessageStart100,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}`,
		`{"type":"message_stop"}`,
	}
}

// relayTurn runs an upstream turn through the same two conversions the gateway
// does -- the provider-side reader (AnthropicStreamEvent.ToBifrostResponsesStream,
// plus the accumulation the shared streaming handler performs and the raw
// field-presence witness it records) and the client-side converter
// (ToAnthropicResponsesStreamResponse) -- and returns the marshalled frames the
// client receives plus the accumulated usage Bifrost bills.
//
// It deliberately mirrors HandleAnthropicResponsesStream's own loop rather than
// asserting on either converter in isolation: the defect lived in the seam
// between the two, and an isolated converter call cannot see it.
func relayTurn(t *testing.T, ctx *schemas.BifrostContext, upstream upstreamStream) ([]string, *schemas.ResponsesResponseUsage) {
	t.Helper()

	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	// The handler opens one presence ledger per stream before its read loop.
	openAnthropicStreamDeltaPromptUsageLedger(ctx)

	accumulated := &schemas.ResponsesResponseUsage{}
	billed := &schemas.BifrostLLMUsage{}
	chunkIndex := 0
	var frames []string

	for _, eventData := range upstream {
		eventDataBytes := []byte(eventData)
		var event AnthropicStreamEvent
		if err := sonic.Unmarshal(eventDataBytes, &event); err != nil {
			t.Fatalf("unmarshal upstream event %q: %v", eventData, err)
		}

		var usageToProcess *AnthropicUsage
		if event.Usage != nil {
			usageToProcess = event.Usage
		} else if event.Message != nil && event.Message.Usage != nil {
			usageToProcess = event.Message.Usage
		}
		if usageToProcess != nil {
			accumulateAnthropicResponsesUsage(accumulated, billed, usageToProcess)
		}

		// The handler's raw field-presence record, at the same point and with the
		// same arguments the shipped loop uses.
		if event.Type == AnthropicStreamEventTypeMessageDelta {
			recordAnthropicStreamDeltaPromptUsage(ctx, chunkIndex, eventDataBytes)
		}

		responses, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(ctx, chunkIndex, state)
		if bifrostErr != nil {
			t.Fatalf("ToBifrostResponsesStream(%q): %v", eventData, bifrostErr)
		}
		if state.HasEmittedMessageDelta {
			ctx.SetValue(schemas.BifrostContextKeyHasEmittedMessageDelta, true)
		}
		for i, response := range responses {
			if response == nil {
				continue
			}
			chunkIndex++
			if isLastChunk && i == len(responses)-1 {
				if response.Response == nil {
					response.Response = &schemas.BifrostResponsesResponse{}
				}
				if accumulated.InputTokensDetails != nil {
					accumulated.InputTokens += accumulated.InputTokensDetails.CachedReadTokens + accumulated.InputTokensDetails.CachedWriteTokens
					accumulated.TotalTokens += accumulated.InputTokensDetails.CachedReadTokens + accumulated.InputTokensDetails.CachedWriteTokens
				}
				response.Response.Usage = accumulated
			}
			for _, out := range ToAnthropicResponsesStreamResponse(ctx, response) {
				data, err := sonic.Marshal(out)
				if err != nil {
					t.Fatalf("marshal client frame: %v", err)
				}
				frames = append(frames, string(data))
			}
		}
	}
	return frames, accumulated
}

// frameOfType returns the single frame of the given type, failing if there is
// not exactly one -- a duplicated terminal frame is itself a double count.
func frameOfType(t *testing.T, frames []string, eventType string) string {
	t.Helper()
	var found []string
	for _, frame := range frames {
		if gjson.Get(frame, "type").String() == eventType {
			found = append(found, frame)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 %s frame, got %d:\n%s", eventType, len(found), strings.Join(frames, "\n"))
	}
	return found[0]
}

// framesOfType returns every frame of the given type, in wire order.
func framesOfType(frames []string, eventType string) []string {
	var found []string
	for _, frame := range frames {
		if gjson.Get(frame, "type").String() == eventType {
			found = append(found, frame)
		}
	}
	return found
}

// clientUsage accumulates usage the way a supported Anthropic SDK does: each
// usage object is a snapshot, so a field PRESENT in a later object replaces the
// running value while an ABSENT one leaves it alone. This is the semantics the
// defect broke.
type clientUsage map[string]int64

func (c clientUsage) observe(frame string) {
	usage := gjson.Get(frame, "usage")
	if !usage.Exists() {
		usage = gjson.Get(frame, "message.usage")
	}
	if !usage.IsObject() {
		return
	}
	usage.ForEach(func(key, value gjson.Result) bool {
		if value.Type == gjson.Number {
			c[key.String()] = value.Int()
		}
		return true
	})
}

func accumulateClientUsage(frames []string) clientUsage {
	c := clientUsage{}
	for _, frame := range frames {
		c.observe(frame)
	}
	return c
}

// TestStreamedPromptUsageSurvivesToTheClient is the reproduction. Without the
// correction the terminal frame carries input_tokens 0 and the client's final
// prompt count reads 0 while Bifrost bills 100.
func TestStreamedPromptUsageSurvivesToTheClient(t *testing.T) {
	ctx := anthropicCtx()
	frames, accumulated := relayTurn(t, ctx, mockProviderTurn())

	start := frameOfType(t, frames, "message_start")
	if got := gjson.Get(start, "message.usage.input_tokens"); !got.Exists() || got.Int() != 100 {
		t.Fatalf("message_start must report input_tokens 100, got %s", start)
	}

	delta := frameOfType(t, frames, "message_delta")
	for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cache_creation"} {
		if gjson.Get(delta, "usage."+key).Exists() {
			t.Errorf("the upstream message_delta reported no %s; the client frame must not invent one: %s", key, delta)
		}
	}
	if got := gjson.Get(delta, "usage.output_tokens"); !got.Exists() || got.Int() != 25 {
		t.Errorf("the terminal frame must report output_tokens 25, got %s", delta)
	}

	client := accumulateClientUsage(frames)
	if client["input_tokens"] != 100 || client["output_tokens"] != 25 {
		t.Errorf("client's final usage is input=%d output=%d; want 100/25", client["input_tokens"], client["output_tokens"])
	}

	// Accounting was never wrong, and must stay right: the two readings agree.
	if accumulated.InputTokens != 100 || accumulated.OutputTokens != 25 {
		t.Errorf("accumulated usage is input=%d output=%d; want 100/25", accumulated.InputTokens, accumulated.OutputTokens)
	}
	if int64(accumulated.InputTokens) != client["input_tokens"] || int64(accumulated.OutputTokens) != client["output_tokens"] {
		t.Errorf("client usage %v disagrees with the accumulated usage input=%d output=%d",
			client, accumulated.InputTokens, accumulated.OutputTokens)
	}
}

// TestStreamedTerminalZeroIsReportedNotDropped is the real-zero control. A
// prompt counter the upstream genuinely reported as 0 is still reported as 0:
// the correction preserves the missing/supplied distinction, it does not
// suppress zeros.
func TestStreamedTerminalZeroIsReportedNotDropped(t *testing.T) {
	upstream := mockProviderTurn()
	upstream[0] = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
	upstream[4] = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"cache_read_input_tokens":0,"output_tokens":25}}`

	frames, accumulated := relayTurn(t, anthropicCtx(), upstream)
	delta := frameOfType(t, frames, "message_delta")

	for _, key := range []string{"input_tokens", "cache_read_input_tokens"} {
		got := gjson.Get(delta, "usage."+key)
		if !got.Exists() {
			t.Errorf("the upstream reported %s=0; dropping it is not faithful: %s", key, delta)
		} else if got.Int() != 0 {
			t.Errorf("%s must stay 0, got %s", key, got.Raw)
		}
	}
	// cache_creation_input_tokens and cache_creation were NOT reported here.
	for _, key := range []string{"cache_creation_input_tokens", "cache_creation"} {
		if gjson.Get(delta, "usage."+key).Exists() {
			t.Errorf("the upstream reported no %s; the client frame must not invent one: %s", key, delta)
		}
	}

	client := accumulateClientUsage(frames)
	if client["input_tokens"] != 0 || client["output_tokens"] != 25 {
		t.Errorf("client's final usage is input=%d output=%d; want a genuine 0/25", client["input_tokens"], client["output_tokens"])
	}
	if accumulated.InputTokens != 0 || accumulated.OutputTokens != 25 {
		t.Errorf("accumulated usage is input=%d output=%d; want 0/25", accumulated.InputTokens, accumulated.OutputTokens)
	}
}

// TestStreamedPromptAndCacheCountersAreReportedVerbatim covers the shape where
// the upstream DOES report the prompt side on the terminal delta, including the
// cache counters and their 5m/1h split: every reported counter survives the
// round trip with its own value, and input_tokens stays the non-cache remainder
// rather than the summed neutral aggregate.
func TestStreamedPromptAndCacheCountersAreReportedVerbatim(t *testing.T) {
	upstream := mockProviderTurn()
	upstream[0] = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":40,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
	upstream[4] = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":40,"cache_creation_input_tokens":12,"cache_read_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":12,"ephemeral_1h_input_tokens":0},"output_tokens":25}}`

	frames, accumulated := relayTurn(t, anthropicCtx(), upstream)
	delta := frameOfType(t, frames, "message_delta")

	for key, want := range map[string]int64{
		"input_tokens":                             40,
		"cache_creation_input_tokens":              12,
		"cache_read_input_tokens":                  7,
		"cache_creation.ephemeral_5m_input_tokens": 12,
		"cache_creation.ephemeral_1h_input_tokens": 0,
		"output_tokens":                            25,
	} {
		got := gjson.Get(delta, "usage."+key)
		if !got.Exists() {
			t.Errorf("usage.%s must be reported, got %s", key, delta)
			continue
		}
		if got.Int() != want {
			t.Errorf("usage.%s is %s, want %d (%s)", key, got.Raw, want, delta)
		}
	}

	// 40 prompt + 12 written + 7 read = 59 billable prompt tokens, counted once.
	if accumulated.InputTokens != 59 || accumulated.OutputTokens != 25 {
		t.Errorf("accumulated usage is input=%d output=%d; want 59/25 with no double count",
			accumulated.InputTokens, accumulated.OutputTokens)
	}
}

// TestCodexTranslatedStreamIsUnchanged is the control for the GPT lanes. The
// Codex-translating adapter opens with a ZEROED usage block it cannot know yet
// and reports BOTH counts only on the terminal delta, so every counter is
// present there and the frame must be unaffected by this correction.
func TestCodexTranslatedStreamIsUnchanged(t *testing.T) {
	upstream := upstreamStream{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"gpt-6-luna","content":[],"stop_reason":null,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	}

	frames, accumulated := relayTurn(t, anthropicCtx(), upstream)
	delta := frameOfType(t, frames, "message_delta")

	for key, want := range map[string]int64{
		"input_tokens":                7,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
		"output_tokens":               5,
	} {
		got := gjson.Get(delta, "usage."+key)
		if !got.Exists() || got.Int() != want {
			t.Errorf("usage.%s must stay %d on a fully-reporting upstream, got %s (%s)", key, want, got.Raw, delta)
		}
	}
	if !gjson.Get(delta, "usage.cache_creation").IsObject() {
		t.Errorf("a reported cache_creation object must survive: %s", delta)
	}

	client := accumulateClientUsage(frames)
	if client["input_tokens"] != 7 || client["output_tokens"] != 5 {
		t.Errorf("client's final usage is input=%d output=%d; want 7/5", client["input_tokens"], client["output_tokens"])
	}
	if accumulated.InputTokens != 7 || accumulated.OutputTokens != 5 {
		t.Errorf("accumulated usage is input=%d output=%d; want 7/5", accumulated.InputTokens, accumulated.OutputTokens)
	}
}

// TestSynthesizedTerminalDeltaReportsTheFullAccumulatedUsage covers the other
// terminal-frame producer: an upstream that never sends message_delta at all.
// There Bifrost synthesizes the frame from its own accumulated usage, which IS
// complete and authoritative, so every counter must be reported.
func TestSynthesizedTerminalDeltaReportsTheFullAccumulatedUsage(t *testing.T) {
	upstream := upstreamStream{
		streamUsageMessageStart100,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_stop"}`,
	}

	frames, _ := relayTurn(t, anthropicCtx(), upstream)
	delta := frameOfType(t, frames, "message_delta")
	for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
		if !gjson.Get(delta, "usage."+key).Exists() {
			t.Errorf("a synthesized terminal frame carries the complete accumulated usage; %s is missing: %s", key, delta)
		}
	}
	if got := gjson.Get(delta, "usage.input_tokens"); got.Int() != 100 {
		t.Errorf("synthesized terminal input_tokens is %s, want 100", got.Raw)
	}
}

// TestMessageStartKeepsItsPromptCountersAtZero guards the invariant this
// correction must not break: message_start's usage object is REQUIRED to carry
// input_tokens by strict clients (@ai-sdk/anthropic marks it non-optional), so a
// genuinely-zero prompt count there is still rendered, never omitted.
func TestMessageStartKeepsItsPromptCountersAtZero(t *testing.T) {
	upstream := mockProviderTurn()
	upstream[0] = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0}}}`

	frames, _ := relayTurn(t, anthropicCtx(), upstream)
	start := frameOfType(t, frames, "message_start")
	for _, key := range []string{"input_tokens", "output_tokens"} {
		if !gjson.Get(start, "message.usage."+key).Exists() {
			t.Fatalf("message_start.message.usage.%s must be present even at zero: %s", key, start)
		}
	}
}

// TestPromptUsageWitnessIsBoundToItsOwnerAndChunk pins the two properties that
// make this witness safe to carry on the request context: it names the value
// store it was written into AND the chunk it describes, and the egress applies it
// only when both match.
//
// The ownership half is load-bearing, not belt-and-braces. BifrostContext.Value
// reads THROUGH to its parent, so a context derived for an internal sub-request
// sees the caller's witness; and a sub-request's own chunk numbering restarts at
// zero, so its sequence numbers collide with the caller's. A chunk binding alone
// therefore lets a caller's witness strip the prompt counters a sub-request's own
// frame legitimately reported -- the "matching sequence" cases below, which fail
// without the owner comparison.
func TestPromptUsageWitnessIsBoundToItsOwnerAndChunk(t *testing.T) {
	parent := anthropicCtx()
	recordAnthropicStreamDeltaPromptUsage(parent, 4, []byte(`{"type":"message_delta","usage":{"output_tokens":25}}`))

	t.Run("its own chunk", func(t *testing.T) {
		usage := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(parent, 4, usage)
		if usage.absentPromptCounters == 0 {
			t.Fatal("the witness must apply to the chunk it names")
		}
	})

	t.Run("another chunk", func(t *testing.T) {
		usage := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(parent, 9, usage)
		if usage.absentPromptCounters != 0 {
			t.Fatal("a witness recorded for chunk 4 must not reshape chunk 9")
		}
	})

	t.Run("inherited by an internal sub-request's context", func(t *testing.T) {
		child := schemas.NewBifrostContext(parent, schemas.NoDeadline)
		child.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		// The child reads the parent's ledger ID through the context chain, and
		// that ID resolves in the store -- so the handle really is inherited...
		id, ok := child.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		if !ok || id == "" {
			t.Fatal("precondition: a derived context reads the parent's value through")
		}
		if loadAnthropicStreamDeltaPromptUsageLedger(id) == nil {
			t.Fatal("precondition: the inherited ID resolves to the parent's ledger")
		}
		// ...and the ownership comparison is what refuses it, not its absence.
		if ownedAnthropicStreamDeltaPromptUsageLedger(child) != nil {
			t.Fatal("a ledger read through from the caller is not this request's own")
		}
		// ...and it is not the child's own witness, whatever chunk the child is on.
		usage := &AnthropicUsage{InputTokens: 11, OutputTokens: 3}
		applyAnthropicStreamDeltaPromptUsage(child, 0, usage)
		if usage.absentPromptCounters != 0 {
			t.Fatal("an inherited witness must not reshape an internal sub-request's own frame")
		}
	})

	t.Run("inherited at the SAME sequence number", func(t *testing.T) {
		// The case a chunk binding cannot see: a sub-request's chunk numbering
		// restarts, so its own chunk 4 carries the caller's witness's sequence
		// number. Every counter this frame reported must survive.
		child := schemas.NewBifrostContext(parent, schemas.NoDeadline)
		child.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		usage := &AnthropicUsage{InputTokens: 11, OutputTokens: 3, CacheCreationInputTokens: 2, CacheReadInputTokens: 5}
		applyAnthropicStreamDeltaPromptUsage(child, 4, usage)
		if usage.absentPromptCounters != 0 {
			t.Fatal("an inherited witness must not reshape a sub-request's frame that happens to share its sequence number")
		}
		assertUsageReports(t, usage, map[string]int64{"input_tokens": 11, "cache_creation_input_tokens": 2, "cache_read_input_tokens": 5, "output_tokens": 3})
	})

	t.Run("recorded by a sub-request, read by its caller", func(t *testing.T) {
		// The mirror image: a sub-request's own witness must not reach back out
		// and reshape the caller's frame at the same sequence number.
		caller := anthropicCtx()
		child := schemas.NewBifrostContext(caller, schemas.NoDeadline)
		recordAnthropicStreamDeltaPromptUsage(child, 4, []byte(`{"type":"message_delta","usage":{"output_tokens":3}}`))
		usage := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(caller, 4, usage)
		if usage.absentPromptCounters != 0 {
			t.Fatal("a sub-request's witness must not reshape its caller's frame")
		}
	})

	t.Run("a sub-request's own message_delta overwrites it", func(t *testing.T) {
		child := schemas.NewBifrostContext(parent, schemas.NoDeadline)
		recordAnthropicStreamDeltaPromptUsage(child, 4, []byte(`{"type":"message_delta","usage":{"input_tokens":11,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{},"output_tokens":3}}`))
		usage := &AnthropicUsage{InputTokens: 11, OutputTokens: 3}
		applyAnthropicStreamDeltaPromptUsage(child, 4, usage)
		if usage.absentPromptCounters != 0 {
			t.Fatal("the record is unconditional per message_delta, so a fully-reporting sub-request must read as fully reporting")
		}
	})

	t.Run("a plugin-scoped context is the same request", func(t *testing.T) {
		// The positive control on the ownership comparison: the one derivation the
		// serving path really uses delegates Value/SetValue to the root it was
		// scoped from, so a witness recorded through a plugin scope still belongs
		// to -- and still applies to -- the request's own frames. Without this the
		// ownership check could pass every negative above by refusing everything.
		owner := anthropicCtx()
		name := "usage-scope-control"
		scoped := owner.WithPluginScope(&name)
		recordAnthropicStreamDeltaPromptUsage(scoped, 7, []byte(`{"type":"message_delta","usage":{"output_tokens":25}}`))

		fromRoot := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(owner, 7, fromRoot)
		if fromRoot.absentPromptCounters == 0 {
			t.Fatal("a witness recorded through a plugin scope must still apply to its own request's frame")
		}

		fromScope := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(scoped, 7, fromScope)
		if fromScope.absentPromptCounters == 0 {
			t.Fatal("reading the witness back through the same plugin scope must apply it too")
		}
	})
}

// TestPromptUsageReadingsSurviveLaterChunks is the lifetime regression. The
// reader records and the egress renders in DIFFERENT goroutines, separated by a
// buffered channel, so on an upstream that sends more than one message_delta
// the second reading is taken while the first chunk is still queued. A single
// slot loses the first chunk's reading, that frame then renders the synthesized
// prompt zeros -- and an accumulating client applies them, which the later,
// correctly-stripped frame can no longer undo.
func TestPromptUsageReadingsSurviveLaterChunks(t *testing.T) {
	ctx := anthropicCtx()
	openAnthropicStreamDeltaPromptUsageLedger(ctx)

	// Both readings taken before either chunk is rendered -- the ordering the
	// buffered channel actually produces.
	recordAnthropicStreamDeltaPromptUsage(ctx, 4, []byte(`{"type":"message_delta","usage":{"output_tokens":10}}`))
	recordAnthropicStreamDeltaPromptUsage(ctx, 9, []byte(`{"type":"message_delta","usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{},"output_tokens":25}}`))

	earlier := &AnthropicUsage{InputTokens: 100, OutputTokens: 10}
	applyAnthropicStreamDeltaPromptUsage(ctx, 4, earlier)
	if earlier.absentPromptCounters == 0 {
		t.Error("the earlier chunk's own reading was displaced by a later event's; its frame renders synthesized prompt zeros a client then applies")
	}

	later := &AnthropicUsage{InputTokens: 7, OutputTokens: 25}
	applyAnthropicStreamDeltaPromptUsage(ctx, 9, later)
	if later.absentPromptCounters != 0 {
		t.Error("the later chunk reported every prompt counter and must render them all")
	}
	assertUsageReports(t, later, map[string]int64{"input_tokens": 7, "output_tokens": 25})

	// A chunk no message_delta produced still has no reading at all.
	unrelated := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
	applyAnthropicStreamDeltaPromptUsage(ctx, 5, unrelated)
	if unrelated.absentPromptCounters != 0 {
		t.Error("a chunk with no reading of its own must render exactly as the converter built it")
	}
}

// TestPromptUsageLedgerIsOpenedPerStream pins the other half of the lifetime
// rule. chunkIndex restarts at zero on every attempt, so a retry or fallback
// attempt's chunk 4 must not be reshaped by the PREVIOUS attempt's reading for
// chunk 4 -- which is why the handler opens a fresh ledger per stream rather
// than letting one accumulate across a request's attempts.
func TestPromptUsageLedgerIsOpenedPerStream(t *testing.T) {
	ctx := anthropicCtx()
	openAnthropicStreamDeltaPromptUsageLedger(ctx)
	recordAnthropicStreamDeltaPromptUsage(ctx, 4, []byte(`{"type":"message_delta","usage":{"output_tokens":10}}`))

	// The next attempt on the same request opens its own ledger.
	openAnthropicStreamDeltaPromptUsageLedger(ctx)

	usage := &AnthropicUsage{InputTokens: 11, OutputTokens: 3, CacheCreationInputTokens: 2, CacheReadInputTokens: 5}
	applyAnthropicStreamDeltaPromptUsage(ctx, 4, usage)
	if usage.absentPromptCounters != 0 {
		t.Fatal("a previous attempt's reading reshaped this attempt's frame at the same chunk number")
	}
	assertUsageReports(t, usage, map[string]int64{
		"input_tokens": 11, "cache_creation_input_tokens": 2,
		"cache_read_input_tokens": 5, "output_tokens": 3,
	})

	// Anti-vacuity: the fresh ledger still records and still applies.
	recordAnthropicStreamDeltaPromptUsage(ctx, 4, []byte(`{"type":"message_delta","usage":{"output_tokens":3}}`))
	own := &AnthropicUsage{InputTokens: 11, OutputTokens: 3}
	applyAnthropicStreamDeltaPromptUsage(ctx, 4, own)
	if own.absentPromptCounters == 0 {
		t.Fatal("the fresh ledger must still carry this attempt's own reading")
	}
}

// TestPromptUsageLedgerNeverLosesAReading pins the decision NOT to cap the
// ledger. A cap is not a neutral degradation here: a chunk whose reading was
// dropped renders exactly the synthesized prompt zeros this correction
// removes, and an accumulating client applies them -- so a bound would
// silently restore the defect on the one upstream shape that needs the
// correction most, an upstream sending many cumulative usage updates.
func TestPromptUsageLedgerNeverLosesAReading(t *testing.T) {
	ctx := anthropicCtx()
	openAnthropicStreamDeltaPromptUsageLedger(ctx)
	outputOnly := []byte(`{"type":"message_delta","usage":{"output_tokens":25}}`)
	const updates = 1025
	for i := 0; i < updates; i++ {
		recordAnthropicStreamDeltaPromptUsage(ctx, i, outputOnly)
	}
	for _, sequenceNumber := range []int{0, 1, updates / 2, updates - 2, updates - 1} {
		usage := &AnthropicUsage{OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(ctx, sequenceNumber, usage)
		data, err := sonic.Marshal(usage)
		if err != nil {
			t.Fatalf("marshal usage: %v", err)
		}
		for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			if gjson.GetBytes(data, key).Exists() {
				t.Errorf("chunk %d: the upstream event reported no %s; a dropped reading renders the synthesized zero again: %s",
					sequenceNumber, key, data)
			}
		}
		if got := gjson.GetBytes(data, "output_tokens"); !got.Exists() || got.Int() != 25 {
			t.Errorf("chunk %d must still report output_tokens 25, got %s", sequenceNumber, data)
		}
	}
	// A chunk no message_delta produced still has no reading at all, so the
	// assertions above are about recorded chunks rather than about a mask
	// applied to everything.
	unrecorded := &AnthropicUsage{InputTokens: 7, OutputTokens: 25}
	applyAnthropicStreamDeltaPromptUsage(ctx, updates+10, unrecorded)
	if unrecorded.absentPromptCounters != 0 {
		t.Error("a chunk with no reading of its own must render exactly as the converter built it")
	}
	// Re-recording a known chunk replaces its reading.
	recordAnthropicStreamDeltaPromptUsage(ctx, 0, []byte(`{"type":"message_delta","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{},"output_tokens":1}}`))
	rewritten := &AnthropicUsage{InputTokens: 1, OutputTokens: 1}
	applyAnthropicStreamDeltaPromptUsage(ctx, 0, rewritten)
	if rewritten.absentPromptCounters != 0 {
		t.Error("re-recording a known chunk must replace its reading")
	}
}

// TestPromptUsageLedgerIsConcurrencySafe drives the two goroutines the serving
// path really has -- the reader recording, the transport applying -- against one
// ledger. Without the mutex this is a concurrent map read and write.
func TestPromptUsageLedgerIsConcurrencySafe(t *testing.T) {
	ctx := anthropicCtx()
	openAnthropicStreamDeltaPromptUsageLedger(ctx)
	outputOnly := []byte(`{"type":"message_delta","usage":{"output_tokens":1}}`)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			recordAnthropicStreamDeltaPromptUsage(ctx, i, outputOnly)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			applyAnthropicStreamDeltaPromptUsage(ctx, i, &AnthropicUsage{InputTokens: 100, OutputTokens: 1})
		}
	}()
	wg.Wait()

	settled := &AnthropicUsage{InputTokens: 100, OutputTokens: 1}
	applyAnthropicStreamDeltaPromptUsage(ctx, 499, settled)
	if settled.absentPromptCounters == 0 {
		t.Fatal("every reading taken must still be readable afterwards")
	}
}

// assertUsageReports renders a usage object the way the transport does and
// asserts each named counter is present with the given value -- the reading that
// catches a counter silently dropped from the wire.
func assertUsageReports(t *testing.T, usage *AnthropicUsage, want map[string]int64) {
	t.Helper()
	data, err := sonic.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	for key, value := range want {
		got := gjson.GetBytes(data, key)
		if !got.Exists() {
			t.Errorf("usage.%s was dropped from the wire: %s", key, data)
			continue
		}
		if got.Int() != value {
			t.Errorf("usage.%s is %s, want %d (%s)", key, got.Raw, value, data)
		}
	}
}

// TestAbsentAnthropicPromptCountersReadsPresenceOnly is the unit on the raw
// reader: key presence, never a value, and never an inference from a zero.
func TestAbsentAnthropicPromptCountersReadsPresenceOnly(t *testing.T) {
	all := anthropicPromptCounterInputTokens | anthropicPromptCounterCacheCreationInputTokens |
		anthropicPromptCounterCacheReadInputTokens | anthropicPromptCounterCacheCreation

	for _, tc := range []struct {
		name  string
		event string
		want  anthropicPromptCounterBit
	}{
		{"output only", `{"type":"message_delta","usage":{"output_tokens":25}}`, all},
		{"explicit zeros are supplied", `{"type":"message_delta","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{},"output_tokens":25}}`, 0},
		{"partial", `{"type":"message_delta","usage":{"input_tokens":5,"output_tokens":25}}`, all ^ anthropicPromptCounterInputTokens},
		{"no usage object at all", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, 0},
		{"usage is not an object", `{"type":"message_delta","usage":7}`, 0},
		// A null is the clients' own no-op: @anthropic-ai/sdk guards each prompt
		// counter with `!= null` before overwriting its snapshot, so rendering it
		// as 0 would turn an upstream non-answer into an answer.
		{"a null counter is not reported", `{"type":"message_delta","usage":{"input_tokens":null,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{},"output_tokens":25}}`, anthropicPromptCounterInputTokens},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := absentAnthropicPromptCounters([]byte(tc.event)); got != tc.want {
				t.Fatalf("absent mask is %04b, want %04b", got, tc.want)
			}
		})
	}
}

// TestUsageMarshalIsStockWithoutTheWitness is the byte-identity control for the
// correction's one wire-level mechanism. Every usage object Bifrost renders
// without a witness -- non-streaming responses, count_tokens results,
// message_start, the synthesized terminal frame -- must marshal exactly as it
// did before AnthropicUsage gained a MarshalJSON.
func TestUsageMarshalIsStockWithoutTheWitness(t *testing.T) {
	usage := &AnthropicUsage{
		Type:                     schemas.Ptr("message"),
		Model:                    schemas.Ptr("claude-opus-5-5"),
		InputTokens:              40,
		CacheCreationInputTokens: 12,
		CacheReadInputTokens:     7,
		CacheCreation:            AnthropicUsageCacheCreation{Ephemeral5mInputTokens: 12},
		OutputTokens:             25,
		OutputTokensDetails:      &AnthropicOutputTokensDetails{ThinkingTokens: 9},
		ServerToolUse:            &AnthropicServerToolUseUsage{WebSearchRequests: 2},
		ServiceTier:              schemas.Ptr("standard"),
		Speed:                    schemas.Ptr("fast"),
		InferenceGeo:             schemas.Ptr("us"),
		Iterations:               []AnthropicUsage{{InputTokens: 1, OutputTokens: 2}},
	}

	const want = `{"type":"message","model":"claude-opus-5-5","input_tokens":40,"cache_creation_input_tokens":12,` +
		`"cache_read_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":12,"ephemeral_1h_input_tokens":0},` +
		`"output_tokens":25,"output_tokens_details":{"thinking_tokens":9},"server_tool_use":{"web_search_requests":2},` +
		`"service_tier":"standard","speed":"fast","inference_geo":"us",` +
		`"iterations":[{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":2}]}`

	data, err := sonic.Marshal(usage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != want {
		t.Fatalf("usage bytes changed:\n got %s\nwant %s", data, want)
	}

	// Nested inside a frame it is embedded verbatim too.
	frame, err := sonic.Marshal(&AnthropicStreamEvent{Type: AnthropicStreamEventTypeMessageDelta, Usage: usage})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if !strings.Contains(string(frame), want) {
		t.Fatalf("frame does not embed the stock usage bytes verbatim: %s", frame)
	}
}

// TestUsageMarshalOmitsOnlyTheFlaggedCounters pins the flagged rendering: the
// named prompt counters disappear, and nothing else about the object moves.
func TestUsageMarshalOmitsOnlyTheFlaggedCounters(t *testing.T) {
	const everything = `{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},` +
		`"output_tokens":25,"service_tier":"standard"}`

	for _, tc := range []struct {
		name   string
		absent anthropicPromptCounterBit
		want   string
	}{
		{"nothing flagged", 0, everything},
		{
			"input only",
			anthropicPromptCounterInputTokens,
			`{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,` +
				`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},` +
				`"output_tokens":25,"service_tier":"standard"}`,
		},
		{
			"every prompt counter",
			anthropicPromptCounterInputTokens | anthropicPromptCounterCacheCreationInputTokens |
				anthropicPromptCounterCacheReadInputTokens | anthropicPromptCounterCacheCreation,
			`{"output_tokens":25,"service_tier":"standard"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &AnthropicUsage{OutputTokens: 25, ServiceTier: schemas.Ptr("standard")}
			usage.absentPromptCounters = tc.absent
			data, err := sonic.Marshal(usage)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tc.want {
				t.Fatalf("usage bytes:\n got %s\nwant %s", data, tc.want)
			}
		})
	}
}

// TestChatDialectStreamIsUnchanged covers the other Anthropic frame source,
// ToAnthropicChatStreamResponse, which this correction does not touch: it builds
// its usage from Bifrost's own cumulative chat usage, which is complete.
func TestChatDialectStreamIsUnchanged(t *testing.T) {
	sse := ToAnthropicChatStreamResponse(&schemas.BifrostChatResponse{
		ID:    "msg_1",
		Model: "claude-opus-5-5",
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 25, TotalTokens: 125},
	})
	data := sseData(t, sse)
	for key, want := range map[string]int64{"input_tokens": 100, "output_tokens": 25} {
		got := gjson.Get(data, "usage."+key)
		if !got.Exists() || got.Int() != want {
			t.Fatalf("chat-dialect usage.%s is %s, want %d (%s)", key, got.Raw, want, data)
		}
	}
	if !strings.Contains(data, `"cache_creation_input_tokens":0`) {
		t.Fatalf("chat-dialect frame lost a stock counter: %s", data)
	}
}

// TestNonAnthropicIntegrationIsUnaffected pins the dialect gate: the neutral
// message_delta branch only renders for an Anthropic integration, so a witness
// cannot reach any other outbound dialect.
func TestNonAnthropicIntegrationIsUnaffected(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	recordAnthropicStreamDeltaPromptUsage(ctx, 0, []byte(`{"type":"message_delta","usage":{"output_tokens":25}}`))

	events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
		Type:           "message_delta",
		SequenceNumber: 0,
		Response: &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{InputTokens: 100, OutputTokens: 25},
		},
	})
	for _, event := range events {
		if event != nil && event.Usage != nil {
			t.Fatalf("a non-anthropic integration must not render an Anthropic message_delta usage: %+v", event)
		}
	}
}

// TestRelayTurnIsNotVacuous asserts the harness actually produced the frames the
// assertions above read, so a converter that silently emitted nothing could not
// pass them.
func TestRelayTurnIsNotVacuous(t *testing.T) {
	frames, _ := relayTurn(t, anthropicCtx(), mockProviderTurn())
	var types []string
	for _, frame := range frames {
		types = append(types, gjson.Get(frame, "type").String())
	}
	got := strings.Join(types, ",")
	const want = "message_start,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop"
	if got != want {
		t.Fatalf("relayed frame sequence is %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// End-to-end leg: the real streaming handler against a real SSE upstream.
//
// relayTurn above mirrors HandleAnthropicResponsesStream's loop; this runs it.
// It is the leg that proves the raw field-presence record is actually reached on
// the shipped path and bound to the chunk the converter stamps -- a mirrored
// harness can agree with itself while the handler never records anything.
// ---------------------------------------------------------------------------

const streamUsageSSEBody = "event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n"

// streamUsageSSEStart renders one message_start event payload as an SSE frame.
func streamUsageSSEStart(payload string) string {
	return "event: message_start\ndata: " + payload + "\n\n"
}

// streamUsageSSETail renders one terminal message_delta usage object plus
// message_stop.
func streamUsageSSETailWith(usage string) string {
	return "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":` + usage + `}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

func streamUsageResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-5-5",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

// TestLiveStreamHandlerPreservesPromptUsageEndToEnd drives the shipped handler
// and renders every chunk exactly as the Anthropic /v1/messages integration
// does, then reads the result both ways: as an accumulating client, and as the
// terminal usage Bifrost bills.
func TestLiveStreamHandlerPreservesPromptUsageEndToEnd(t *testing.T) {
	server := anthropicSSEServer(t,
		streamUsageSSEStart(streamUsageMessageStart100)+streamUsageSSEBody+streamUsageSSETailWith(`{"output_tokens":25}`),
		false)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := anthropicCtx()
	stream, bifrostErr := provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, streamUsageResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var frames []string
	var terminal *schemas.ResponsesResponseUsage
	for _, chunk := range collectTruncationChunks(t, stream) {
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

	if len(frames) == 0 {
		t.Fatal("the handler produced no client frames at all")
	}
	delta := frameOfType(t, frames, "message_delta")
	if gjson.Get(delta, "usage.input_tokens").Exists() {
		t.Errorf("the upstream message_delta reported no input_tokens; the client frame must not invent one: %s", delta)
	}
	if got := gjson.Get(delta, "usage.output_tokens"); !got.Exists() || got.Int() != 25 {
		t.Errorf("the terminal frame must report output_tokens 25, got %s", delta)
	}
	client := accumulateClientUsage(frames)
	if client["input_tokens"] != 100 || client["output_tokens"] != 25 {
		t.Errorf("client's final usage is input=%d output=%d; want 100/25.\nframes:\n%s",
			client["input_tokens"], client["output_tokens"], strings.Join(frames, "\n"))
	}

	if terminal == nil {
		t.Fatal("no terminal accumulated usage reached the stream consumer")
	}
	if terminal.InputTokens != 100 || terminal.OutputTokens != 25 {
		t.Errorf("accounting usage is input=%d output=%d; want 100/25", terminal.InputTokens, terminal.OutputTokens)
	}
	if int64(terminal.InputTokens) != client["input_tokens"] || int64(terminal.OutputTokens) != client["output_tokens"] {
		t.Errorf("client usage %v disagrees with the accounting usage input=%d output=%d",
			client, terminal.InputTokens, terminal.OutputTokens)
	}
}

// TestLiveStreamHandlerReportsASuppliedTerminalZero is the end-to-end real-zero
// control: an upstream that genuinely reports input_tokens 0 on the terminal
// delta still reports it, and the client's honest final prompt count is 0.
func TestLiveStreamHandlerReportsASuppliedTerminalZero(t *testing.T) {
	const zeroStart = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}`

	server := anthropicSSEServer(t,
		streamUsageSSEStart(zeroStart)+streamUsageSSEBody+streamUsageSSETailWith(`{"input_tokens":0,"output_tokens":25}`),
		false)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := anthropicCtx()
	stream, bifrostErr := provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, streamUsageResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var frames []string
	for _, chunk := range collectTruncationChunks(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %+v", chunk.BifrostError)
		}
		if resp := chunk.BifrostResponsesStreamResponse; resp != nil {
			for _, out := range ToAnthropicResponsesStreamResponse(ctx, resp) {
				data, err := sonic.Marshal(out)
				if err != nil {
					t.Fatalf("marshal client frame: %v", err)
				}
				frames = append(frames, string(data))
			}
		}
	}

	delta := frameOfType(t, frames, "message_delta")
	got := gjson.Get(delta, "usage.input_tokens")
	if !got.Exists() {
		t.Fatalf("the upstream reported input_tokens=0; dropping it is not faithful: %s", delta)
	}
	if got.Int() != 0 {
		t.Fatalf("input_tokens must stay 0, got %s", got.Raw)
	}
	if client := accumulateClientUsage(frames); client["input_tokens"] != 0 || client["output_tokens"] != 25 {
		t.Fatalf("client's final usage is input=%d output=%d; want a genuine 0/25", client["input_tokens"], client["output_tokens"])
	}
}

// TestLiveStreamHandlerPreservesPromptUsageAcrossSeveralDeltas is the
// asynchronous-transport regression, driven end to end on the shipped handler.
//
// The reader runs in its own goroutine and hands chunks to the egress over a
// 256-deep buffered channel, so on an upstream that sends cumulative usage
// updates BOTH readings are taken before either chunk is rendered. A witness
// held in one slot is overwritten by the second reading, the first frame then
// renders every prompt counter as a synthesized zero, and the installed client
// -- which overwrites on presence -- applies it. The second frame omitting them
// cannot put back what the first already erased, so the caller's final prompt
// count is 0/0/0 while Bifrost bills 50/20/30.
//
// Collecting the whole stream before rendering any of it is not a convenience
// here: it is the ordering the buffered channel produces, and it is why this
// leg fails against a single-slot witness and passes against a per-chunk one.
func TestLiveStreamHandlerPreservesPromptUsageAcrossSeveralDeltas(t *testing.T) {
	const startCached = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,` +
		`"usage":{"input_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":0}}}`
	const cumulativeDeltas = "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":10}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	server := anthropicSSEServer(t, streamUsageSSEStart(startCached)+streamUsageSSEBody+cumulativeDeltas, false)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := anthropicCtx()
	stream, bifrostErr := provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, streamUsageResponsesRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	// Drain first, render after: the buffered channel's own ordering.
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
	if len(deltas) != 2 {
		t.Fatalf("expected the two upstream message_delta frames to be relayed, got %d:\n%s", len(deltas), strings.Join(frames, "\n"))
	}
	for i, delta := range deltas {
		for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cache_creation"} {
			if gjson.Get(delta, "usage."+key).Exists() {
				t.Errorf("message_delta %d reported no %s upstream; the client frame must not invent one: %s", i, key, delta)
			}
		}
	}
	if got := gjson.Get(deltas[0], "usage.output_tokens"); !got.Exists() || got.Int() != 10 {
		t.Errorf("the first delta must report its own output count 10, got %s", deltas[0])
	}
	if got := gjson.Get(deltas[1], "usage.output_tokens"); !got.Exists() || got.Int() != 25 {
		t.Errorf("the terminal delta must report output_tokens 25, got %s", deltas[1])
	}

	client := accumulateClientUsage(frames)
	for key, want := range map[string]int64{
		"input_tokens":                50,
		"cache_creation_input_tokens": 20,
		"cache_read_input_tokens":     30,
		"output_tokens":               25,
	} {
		if client[key] != want {
			t.Errorf("client's final %s is %d, want %d (cumulative updates must not erase the message_start figures).\nframes:\n%s",
				key, client[key], want, strings.Join(frames, "\n"))
		}
	}

	// The accounting side was never wrong and must stay right. Bifrost's neutral
	// InputTokens is the SUM of the three prompt-side figures the client holds
	// separately, counted once each -- no double count across the two deltas.
	if terminal == nil {
		t.Fatal("no terminal accumulated usage reached the stream consumer")
	}
	if terminal.InputTokens != 100 || terminal.OutputTokens != 25 {
		t.Fatalf("accounting usage is input=%d output=%d; want 100/25", terminal.InputTokens, terminal.OutputTokens)
	}
	if terminal.InputTokensDetails == nil ||
		terminal.InputTokensDetails.CachedWriteTokens != 20 ||
		terminal.InputTokensDetails.CachedReadTokens != 30 {
		t.Fatalf("accounting cache breakdown is %+v; want write 20 / read 30", terminal.InputTokensDetails)
	}
	if sum := client["input_tokens"] + client["cache_creation_input_tokens"] + client["cache_read_input_tokens"]; sum != int64(terminal.InputTokens) {
		t.Errorf("the client's prompt figures sum to %d but the accounting input is %d", sum, terminal.InputTokens)
	}
}

// TestLiveStreamHandlerOpensAFreshLedgerPerStream pins the ledger's LIFETIME on
// the shipped handler. chunkIndex restarts at zero on every attempt, so a
// request that retries or falls back reuses the same chunk numbers for an
// entirely different stream; a ledger that accumulated across a request's
// attempts would hold readings describing chunks of a stream that is over, and
// would grow against its cap across every attempt rather than per stream.
//
// The handler therefore opens a fresh one before its read loop, and this is the
// regression for that: after a second stream on the SAME context, the request's
// ledger holds exactly the second stream's own readings.
func TestLiveStreamHandlerOpensAFreshLedgerPerStream(t *testing.T) {
	const twoDeltaTail = "event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":10}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":25}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	ctx := anthropicCtx()

	// deltaSequences runs one upstream turn on ctx through the shipped handler
	// and returns the sequence numbers of the message_delta chunks it produced.
	deltaSequences := func(body string) []int {
		// What the retry/fallback path itself clears before the next attempt
		// reads a fresh stream on the SAME context (core/bifrost.go's
		// CheckFirstStreamChunkForError branch, and clearCtxForFallback).
		// Without it the previous attempt's teardown flag makes every read of
		// the new stream fail with ErrStreamClosed.
		ctx.ClearValue(schemas.BifrostContextKeyStreamEndIndicator)
		ctx.ClearValue(schemas.BifrostContextKeyConnectionClosed)

		server := anthropicSSEServer(t, body, false)
		defer server.Close()
		provider := newTruncationTestProvider(server.URL)
		stream, bifrostErr := provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil,
			schemas.Key{Value: *schemas.NewSecretVar("test-key")}, streamUsageResponsesRequest())
		if bifrostErr != nil {
			t.Fatalf("stream setup failed: %v", bifrostErr)
		}
		var seqs []int
		for _, chunk := range collectTruncationChunks(t, stream) {
			if chunk.BifrostError != nil {
				t.Fatalf("unexpected stream error: %+v", chunk.BifrostError)
			}
			if resp := chunk.BifrostResponsesStreamResponse; resp != nil && resp.Type == "message_delta" {
				seqs = append(seqs, resp.SequenceNumber)
			}
		}
		return seqs
	}

	first := deltaSequences(streamUsageSSEStart(streamUsageMessageStart100) + streamUsageSSEBody + twoDeltaTail)
	if len(first) != 2 {
		t.Fatalf("precondition: the first attempt must produce two message_delta chunks, got %d", len(first))
	}
	second := deltaSequences(streamUsageSSEStart(streamUsageMessageStart100) + streamUsageSSEBody + streamUsageSSETailWith(`{"output_tokens":25}`))
	if len(second) != 1 {
		t.Fatalf("precondition: the second attempt must produce one message_delta chunk, got %d", len(second))
	}

	ledger := ownedAnthropicStreamDeltaPromptUsageLedger(ctx)
	if ledger == nil {
		t.Fatal("the second stream left this request no ledger of its own")
	}
	if got := len(ledger.absent); got != 1 {
		t.Errorf("after the second stream the ledger holds %d readings, want exactly the 1 that stream took -- a previous attempt's readings outlived their own stream", got)
	}
	if _, ok := ledger.lookup(second[0]); !ok {
		t.Errorf("the second stream's own chunk %d has no reading", second[0])
	}

	// A chunk only the FIRST attempt ever recorded must now read as unknown, so
	// nothing of that finished stream can reshape a later frame.
	for _, seq := range first {
		if seq == second[0] {
			continue
		}
		if _, ok := ledger.lookup(seq); ok {
			t.Errorf("chunk %d belongs to a stream that is over; its reading must not survive into the next attempt", seq)
		}
		usage := &AnthropicUsage{InputTokens: 11, OutputTokens: 3, CacheCreationInputTokens: 2, CacheReadInputTokens: 5}
		applyAnthropicStreamDeltaPromptUsage(ctx, seq, usage)
		if usage.absentPromptCounters != 0 {
			t.Errorf("a finished stream's reading for chunk %d reshaped a later frame", seq)
		}
	}
}

// TestPromptUsageLedgerIsReleasedOnDelivery pins the lifetime half of
// AGENTS.md's rule, and the release point is load-bearing in both directions.
//
// Too early and the terminal message_delta -- marshalled from the chunk channel
// after the reader goroutine has returned -- renders with the evidence already
// gone. Never, and the store grows for the life of the process: a caller holding
// NewBifrostContext(context.Background(), NoDeadline) never cancels, so a
// context lifetime is not a release signal at all. Delivery is: the egress
// releases once it has rendered the terminal frame, and a retry's own open()
// drops whatever the previous attempt left.
func TestPromptUsageLedgerIsReleasedOnDelivery(t *testing.T) {
	t.Run("rendering the terminal frame releases the readings", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		ledger := openAnthropicStreamDeltaPromptUsageLedger(ctx)
		if ledger == nil {
			t.Fatal("expected a ledger")
		}
		id, _ := ctx.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		if id == "" {
			t.Fatal("expected the context to carry the ledger ID")
		}
		ledger.record(0, anthropicPromptCounterInputTokens)

		// A non-terminal frame keeps them: later chunks still need the readings.
		ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
			Type:           "message_delta",
			SequenceNumber: 0,
			Response: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 100, OutputTokens: 25},
			},
		})
		if loadAnthropicStreamDeltaPromptUsageLedger(id) == nil {
			t.Fatal("a mid-stream frame must not release the readings")
		}

		// The terminal frame is the last one that can need a reading.
		events := ToAnthropicResponsesStreamResponse(ctx, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: 1,
			Response: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 100, OutputTokens: 25},
			},
		})
		var sawStop bool
		for _, event := range events {
			if event != nil && event.Type == AnthropicStreamEventTypeMessageStop {
				sawStop = true
			}
		}
		if !sawStop {
			t.Fatalf("precondition: the terminal response must render a message_stop, got %d events", len(events))
		}
		if loadAnthropicStreamDeltaPromptUsageLedger(id) != nil {
			t.Fatal("the readings outlived the terminal frame: the store would grow without bound")
		}
	})

	t.Run("the consumer's release covers an abnormal end", func(t *testing.T) {
		// The transport calls ReleaseStreamPromptUsage from the Anthropic stream
		// error callback, which runs after handleStreaming has converted the
		// in-order chunks. So the readings have to be intact for a queued frame
		// rendered BEFORE that callback, and gone after it.
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		ledger := openAnthropicStreamDeltaPromptUsageLedger(ctx)
		id, _ := ctx.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		ledger.record(0, anthropicPromptCounterInputTokens)

		// A queued frame converted before the error callback keeps its reading.
		usage := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(ctx, 0, usage)
		if usage.absentPromptCounters == 0 {
			t.Fatal("a queued frame lost its reading before the error callback ran")
		}

		ReleaseStreamPromptUsage(ctx)
		if loadAnthropicStreamDeltaPromptUsageLedger(id) != nil {
			t.Fatal("the consumer's release left the readings in the store")
		}

		// Idempotent: the callback can run on a stream that already ended
		// normally, and a second release must not panic or disturb anything.
		ReleaseStreamPromptUsage(ctx)
		ReleaseStreamPromptUsage(nil)
	})

	t.Run("a child request cannot release its parent's readings", func(t *testing.T) {
		// Value reads through, so a derived sub-request sees the parent's ledger
		// ID. Releasing on it must be refused, or a plugin's internal call would
		// strip the caller's still-running stream of its evidence.
		parent := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		parent.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		ledger := openAnthropicStreamDeltaPromptUsageLedger(parent)
		id, _ := parent.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		ledger.record(3, anthropicPromptCounterInputTokens)

		child := schemas.NewBifrostContext(parent, schemas.NoDeadline)
		ReleaseStreamPromptUsage(child)
		if loadAnthropicStreamDeltaPromptUsageLedger(id) == nil {
			t.Fatal("a child request released readings belonging to its parent's stream")
		}
		// And the parent's own frame still finds its reading afterwards.
		usage := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(parent, 3, usage)
		if usage.absentPromptCounters == 0 {
			t.Fatal("the parent's reading was lost to a child's release")
		}

		ReleaseStreamPromptUsage(parent)
		if loadAnthropicStreamDeltaPromptUsageLedger(id) != nil {
			t.Fatal("the owner's release did not take effect")
		}
	})

	t.Run("a truncated stream's queued frames still find their readings", func(t *testing.T) {
		// The reader returns before the consumer has converted what it queued, so
		// nothing on the producer side may release: a reading dropped here renders
		// exactly the prompt zeros this correction removes.
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
		ledger := openAnthropicStreamDeltaPromptUsageLedger(ctx)
		id, _ := ctx.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		ledger.record(0, anthropicPromptCounterInputTokens)

		// Stream ends with no terminal frame; a queued chunk is converted after.
		usage := &AnthropicUsage{InputTokens: 100, OutputTokens: 25}
		applyAnthropicStreamDeltaPromptUsage(ctx, 0, usage)
		if usage.absentPromptCounters == 0 {
			t.Fatal("a queued frame lost its reading: the prompt count would render as zero")
		}
		if loadAnthropicStreamDeltaPromptUsageLedger(id) == nil {
			t.Fatal("the readings must survive a stream that produced no terminal frame")
		}

		// Once the request itself is finished they cannot be needed, and the next
		// stream to open sweeps them. BifrostContext observes a parent's
		// cancellation on its own goroutine, so wait for that to land rather than
		// racing it.
		cancel()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("the request context never observed its parent's cancellation")
		}
		openAnthropicStreamDeltaPromptUsageLedger(schemas.NewBifrostContext(t.Context(), schemas.NoDeadline))
		if loadAnthropicStreamDeltaPromptUsageLedger(id) != nil {
			t.Fatal("a finished request's readings were not swept: the store would grow without bound")
		}
	})

	t.Run("a fresh stream drops the previous attempt's readings", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		first := openAnthropicStreamDeltaPromptUsageLedger(ctx)
		firstID, _ := ctx.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		first.record(4, anthropicPromptCounterInputTokens)

		second := openAnthropicStreamDeltaPromptUsageLedger(ctx)
		secondID, _ := ctx.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(string)
		if secondID == firstID {
			t.Fatal("a new stream must get its own ledger ID")
		}
		if loadAnthropicStreamDeltaPromptUsageLedger(firstID) != nil {
			t.Fatal("the replaced attempt's readings must be dropped from the store")
		}
		if _, ok := second.lookup(4); ok {
			t.Fatal("a retry's chunk 4 must not read the previous attempt's reading")
		}
	})
}
