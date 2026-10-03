package anthropic

import (
	"sync"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Anthropic splits streaming usage across two frames: message_start nests the
// prompt-side counters under message.usage, and the terminal message_delta
// carries the final output count at the top level. Every accumulating client --
// the official SDKs included -- treats each usage object as a snapshot and
// OVERWRITES the fields it finds, so a message_delta that reports
// `input_tokens: 0` does not mean "no new prompt tokens", it erases the prompt
// count the client already has.
//
// That is exactly what the Anthropic -> neutral Responses -> Anthropic round
// trip produced. ConvertAnthropicUsageToBifrostUsage lands the per-event usage
// in schemas.ResponsesResponseUsage, whose counters are plain ints, so an
// upstream object carrying only `output_tokens` becomes InputTokens == 0 and is
// indistinguishable from one that genuinely reported zero. The egress converter
// then rebuilt a full AnthropicUsage, whose prompt counters have no omitempty
// (message_start needs them present -- see messagestartusage_test.go), and the
// client's final prompt count read 0 while Bifrost's own accumulated usage, and
// therefore its accounting, stayed correct.
//
// The fix preserves the distinction rather than guessing: which prompt-side
// counters the raw event actually reported is recorded at the SSE boundary, and
// the egress frame reports exactly those. An upstream that reports a real zero
// still gets a zero on the wire; one that reports nothing gets nothing, and the
// client keeps its message_start figure. No value, text or payload byte is
// retained -- only key presence, the identity of the request store the evidence
// belongs to, and the sequence number of the chunk it describes.
//
// The evidence is carried PER CHUNK, in a ledger opened once per upstream
// stream. That is not tidiness: the reader runs in its own goroutine and hands
// chunks to the egress over a 256-deep buffered channel
// (schemas.DefaultStreamBufferSize), so an upstream that sends more than one
// message_delta -- cumulative usage updates -- has its later readings taken
// while earlier chunks are still queued. A single slot would be overwritten,
// the earlier chunk would then fail its own chunk check and render the
// synthesized zeros this correction exists to remove. Opening the ledger per
// stream matters for the same reason in the other direction: chunk numbering
// restarts on every attempt, so a fallback attempt's chunk 4 must not read the
// previous attempt's reading for chunk 4.

// anthropicPromptCounterBit identifies one prompt-side usage counter whose
// presence on the wire is tracked independently.
type anthropicPromptCounterBit = uint8

const (
	anthropicPromptCounterInputTokens anthropicPromptCounterBit = 1 << iota
	anthropicPromptCounterCacheCreationInputTokens
	anthropicPromptCounterCacheReadInputTokens
	anthropicPromptCounterCacheCreation
)

// anthropicPromptCounterKeys maps each tracked bit to its wire key. The set is
// closed and prompt-side only: output_tokens is deliberately NOT tracked, so
// the delta frame always reports the final output count and a usage object can
// never render empty. That exclusion is load-bearing rather than tidy --
// @anthropic-ai/sdk's accumulator assigns `snapshot.usage.output_tokens =
// event.usage.output_tokens` UNGUARDED, so a terminal frame omitting it would
// leave the client's output count undefined.
var anthropicPromptCounterKeys = []struct {
	bit anthropicPromptCounterBit
	key string
}{
	{anthropicPromptCounterInputTokens, "input_tokens"},
	{anthropicPromptCounterCacheCreationInputTokens, "cache_creation_input_tokens"},
	{anthropicPromptCounterCacheReadInputTokens, "cache_read_input_tokens"},
	{anthropicPromptCounterCacheCreation, "cache_creation"},
}

// anthropicStreamDeltaPromptUsageLedger is the payload-blind evidence one
// upstream stream accumulates: for each chunk a raw `message_delta` produced,
// which prompt-side counters that event reported. Nothing else about the event
// is read or kept.
//
// It is keyed BY CHUNK rather than held in a single slot, and that is the
// property the asynchronous transport requires. The reader runs in its own
// goroutine and hands chunks to the egress over a buffered channel
// (schemas.DefaultStreamBufferSize chunks deep), so an upstream that sends
// several message_delta events -- cumulative usage updates -- has its later
// readings taken long before the earlier chunks are rendered. One slot would be
// overwritten by the later reading, the earlier chunk would fail its own chunk
// check, and that frame would render the synthesized prompt zeros this
// correction exists to remove -- which an accumulating client applies and the
// later, correctly-stripped frame can no longer undo.
//
// owner is the value store the ledger was opened in -- ctx.Root(), the identity
// a request's context values actually live in. That is what makes the ledger
// non-inheritable: BifrostContext.Value reads THROUGH to its parent, so a
// context derived for an internal sub-request (one the plugin pipeline then
// passes through ClearContextForInternalRequest, or any plain derived child)
// sees the caller's ledger, and a sub-request's own chunk numbering restarts at
// zero -- so its chunk numbers collide with the caller's. A chunk number alone
// therefore cannot establish ownership: a parent's reading for chunk 4 would
// silently strip the prompt counters a child's own chunk 4 legitimately
// reported. Pointer identity of the store does establish it, and it survives
// the one derivation the serving path really uses: WithPluginScope delegates
// Value/SetValue to the root it was scoped from, so Root() on either side of a
// plugin hook is the same pointer.
//
// The mutex is load-bearing for the same reason the chunk key is: the reader
// goroutine records while the transport goroutine applies.
//
// There is no cap. One was considered and rejected: past a cap a chunk goes
// unrecorded, and an unrecorded chunk renders exactly the synthesized prompt
// zeros this file exists to remove -- so a bound would silently restore the
// defect on the one upstream shape that needs the correction most. The cost of
// not bounding it is one byte of key-presence evidence per message_delta the
// upstream actually sent, for the lifetime of that request.
type anthropicStreamDeltaPromptUsageLedger struct {
	owner  *schemas.BifrostContext
	mu     sync.Mutex
	absent map[int]anthropicPromptCounterBit
}

// openAnthropicStreamDeltaPromptUsageLedger installs a FRESH ledger for the
// stream about to be read, replacing any the same request accumulated earlier.
// Chunk numbering restarts on every attempt, so a fallback or retry attempt's
// chunk 4 must never read the previous attempt's reading for chunk 4.
func openAnthropicStreamDeltaPromptUsageLedger(ctx *schemas.BifrostContext) *anthropicStreamDeltaPromptUsageLedger {
	if ctx == nil {
		return nil
	}
	ledger := &anthropicStreamDeltaPromptUsageLedger{
		owner:  ctx.Root(),
		absent: make(map[int]anthropicPromptCounterBit, 1),
	}
	ctx.SetValue(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage, ledger)
	return ledger
}

// ownedAnthropicStreamDeltaPromptUsageLedger returns the ledger this context's
// own request opened, or nil -- for a missing one, or for one read through from
// another request's context.
func ownedAnthropicStreamDeltaPromptUsageLedger(ctx *schemas.BifrostContext) *anthropicStreamDeltaPromptUsageLedger {
	if ctx == nil {
		return nil
	}
	ledger, _ := ctx.Value(schemas.BifrostContextKeyAnthropicStreamDeltaPromptUsage).(*anthropicStreamDeltaPromptUsageLedger)
	if ledger == nil || ledger.owner == nil || ledger.owner != ctx.Root() {
		return nil
	}
	return ledger
}

// record files one chunk's reading. Deliberately UNCAPPED: a cap was
// considered and rejected, because losing a reading is not a neutral
// degradation here -- a chunk whose reading has been dropped renders exactly
// the synthesized prompt zeros this correction removes, and an accumulating
// client applies them. The ledger holds one byte of key-presence evidence per
// message_delta the upstream actually sent, nothing else, and it dies with the
// request's context.
func (l *anthropicStreamDeltaPromptUsageLedger) record(sequenceNumber int, absent anthropicPromptCounterBit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.absent[sequenceNumber] = absent
}

func (l *anthropicStreamDeltaPromptUsageLedger) lookup(sequenceNumber int) (anthropicPromptCounterBit, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	absent, ok := l.absent[sequenceNumber]
	return absent, ok
}

// absentAnthropicPromptCounters reports which prompt-side counters the raw
// `message_delta` event did NOT report. An event with no usage object at all
// yields 0: there is nothing to reshape, and the egress keeps stock behaviour.
//
// A key present with a JSON null counts as NOT reported, matching the rule the
// clients themselves apply: @anthropic-ai/sdk's message accumulator guards each
// prompt counter with `if (event.usage.<counter> != null)` before overwriting
// its snapshot, so a null is already a no-op there and rendering it as a 0
// would turn an upstream non-answer into an answer the client acts on.
func absentAnthropicPromptCounters(event []byte) anthropicPromptCounterBit {
	usage := providerUtils.GetJSONField(event, "usage")
	if !usage.Exists() || !usage.IsObject() {
		return 0
	}
	var absent anthropicPromptCounterBit
	for _, counter := range anthropicPromptCounterKeys {
		if reported := usage.Get(counter.key); !reported.Exists() || reported.Type == gjson.Null {
			absent |= counter.bit
		}
	}
	return absent
}

// recordAnthropicStreamDeltaPromptUsage records, against the chunk this event
// produces, which prompt-side counters it reported. A reading is written
// unconditionally for every message_delta -- a zero mask is a real reading
// ("the upstream reported all of them"), not an absence of one.
//
// It opens a ledger if the request does not own one yet, so the record is never
// silently dropped; the streaming handler opens one per stream up front, which
// is what keeps one attempt's readings out of the next.
func recordAnthropicStreamDeltaPromptUsage(ctx *schemas.BifrostContext, sequenceNumber int, event []byte) {
	if ctx == nil {
		return
	}
	ledger := ownedAnthropicStreamDeltaPromptUsageLedger(ctx)
	if ledger == nil {
		if ledger = openAnthropicStreamDeltaPromptUsageLedger(ctx); ledger == nil {
			return
		}
	}
	ledger.record(sequenceNumber, absentAnthropicPromptCounters(event))
}

// applyAnthropicStreamDeltaPromptUsage marks the counters the upstream event
// behind this chunk never reported, so MarshalJSON renders them as absent.
//
// It applies a reading only to a frame of the request that recorded it, and
// only to the chunk that reading describes: no ledger, one read through from
// another request's context, or no reading for this chunk all leave the usage
// object exactly as the converter built it. Refusing is always the stock
// rendering, so an unrecognised reading can only ever cost this correction,
// never add an error.
//
// Readings are not consumed: the same chunk may legitimately be rendered more
// than once, and a second rendering must agree with the first.
func applyAnthropicStreamDeltaPromptUsage(ctx *schemas.BifrostContext, sequenceNumber int, usage *AnthropicUsage) {
	if ctx == nil || usage == nil {
		return
	}
	ledger := ownedAnthropicStreamDeltaPromptUsageLedger(ctx)
	if ledger == nil {
		return
	}
	absent, ok := ledger.lookup(sequenceNumber)
	if !ok {
		return
	}
	usage.absentPromptCounters = absent
}

// anthropicUsageWire is AnthropicUsage without its MarshalJSON method, so the
// encoder's own field-order rendering stays the single source of the usage
// object's bytes and the unflagged path is byte-identical to stock.
type anthropicUsageWire AnthropicUsage

// MarshalJSON renders the usage object, omitting only those prompt-side
// counters the upstream event this object describes did not report.
//
// The default (zero) flag reproduces stock bytes exactly: every usage object
// Bifrost builds for a non-streaming response, a count_tokens result, a
// message_start frame or a synthesized terminal frame carries the complete
// accumulated figures and renders unchanged.
func (u AnthropicUsage) MarshalJSON() ([]byte, error) {
	data, err := sonic.Marshal(anthropicUsageWire(u))
	if err != nil {
		return nil, err
	}
	if u.absentPromptCounters == 0 {
		return data, nil
	}
	for _, counter := range anthropicPromptCounterKeys {
		if u.absentPromptCounters&counter.bit == 0 {
			continue
		}
		if data, err = providerUtils.DeleteJSONField(data, counter.key); err != nil {
			return nil, err
		}
	}
	return data, nil
}
