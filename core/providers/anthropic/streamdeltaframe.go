package anthropic

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// A streaming `message_delta` frame carries TWO things: the turn's usage so far
// and its stop state. The usage half is in streamdeltausage.go. This is the
// stop half, and it is a protocol requirement rather than a nicety: the
// Anthropic Messages event declares `delta.stop_reason` and
// `delta.stop_sequence` as required-and-nullable, and the supported clients
// read them UNGUARDED. @anthropic-ai/sdk 0.91.1's message accumulator does
//
//	case 'message_delta':
//	  snapshot.stop_reason = event.delta.stop_reason;
//	  snapshot.stop_sequence = event.delta.stop_sequence;
//
// (lib/MessageStream.js), so a frame carrying no delta object at all does not
// degrade -- it THROWS `Cannot read properties of undefined (reading
// 'stop_reason')` and aborts the whole stream mid-turn.
//
// That is what the egress produced for an upstream reporting its running usage
// on several message_delta frames. A non-final update sets no stop reason, so
// the neutral chunk carried none, so the converter built no delta object, so
// the `delta,omitempty` tag dropped the key: the client was handed
// `{"type":"message_delta","usage":{"output_tokens":9}}` and refused it. The
// turn's content, order and usage were all correct; the frame was simply not a
// frame a client could consume. Reproduced against a running gateway with
// @anthropic-ai/sdk 0.91.1 and covered by
// TestCumulativeMessageDeltasCarryTheRequiredDeltaObject.
//
// The correction renders the stop state the upstream ACTUALLY reported. A null
// `stop_reason` says "this turn has not ended", which is what a cumulative
// update says; synthesizing an `end_turn` there would tell the client the turn
// was over while more output was still coming, and would be the same class of
// mistake as synthesizing a usage figure. The terminal frame is untouched: it
// has a stop reason of its own and already rendered both fields.
//
// Why a flag and a MarshalJSON rather than dropping `omitempty` from
// StopReason: AnthropicStreamDelta is shared with content_block_delta, whose
// frames legitimately carry no stop fields and whose bytes are pinned by the
// carried stock-byte controls. The flag is set at exactly one call site, so
// everything else -- every content block, every synthesized terminal frame,
// every non-streaming response -- renders byte-identically to stock.

// newAnthropicMessageDeltaStopFields returns the delta object a `message_delta`
// frame must carry when the upstream event behind it reported no stop reason:
// the two required fields, both explicitly null, and nothing else.
func newAnthropicMessageDeltaStopFields() *AnthropicStreamDelta {
	return &AnthropicStreamDelta{requireStopFields: true}
}

// anthropicStreamDeltaWire is AnthropicStreamDelta without its MarshalJSON
// method, so the encoder's own field rendering stays the single source of the
// delta object's bytes and the unflagged path is byte-identical to stock.
type anthropicStreamDeltaWire AnthropicStreamDelta

// MarshalJSON renders the delta object, adding an explicit null `stop_reason`
// only for a frame that requires the message-level stop fields and has no stop
// reason to report. `stop_sequence` needs no such handling: its tag already
// carries no omitempty, because `message_delta` has always needed it present.
//
// The flag itself (AnthropicStreamDelta.requireStopFields) asks for the two
// MESSAGE-level stop fields to be rendered explicitly -- stop_reason as a JSON
// null when unset -- because a streaming message_delta frame's delta object
// declares both as required and nullable, and the supported clients read
// delta.stop_reason unguarded. It is unexported and set at one call site.
//
// The default (zero) flag reproduces stock bytes exactly, and so does a flagged
// object that does have a stop reason -- the rendering only ever ADDS the key
// the protocol requires, never changes or removes one.
func (d AnthropicStreamDelta) MarshalJSON() ([]byte, error) {
	data, err := sonic.Marshal(anthropicStreamDeltaWire(d))
	if err != nil {
		return nil, err
	}
	if !d.requireStopFields || d.StopReason != nil {
		return data, nil
	}
	return providerUtils.SetRawJSONField(data, "stop_reason", []byte("null"))
}
