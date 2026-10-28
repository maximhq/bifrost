package anthropic

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
// Why an empty stop reason rather than dropping `omitempty` from StopReason:
// AnthropicStreamDelta is shared with content_block_delta, whose frames
// legitimately carry no stop fields and whose bytes are pinned by the carried
// stock-byte controls. The message_delta egress sets an EMPTY stop reason when
// the upstream reported none, and AnthropicStopReason.MarshalJSON renders that
// as the required JSON null. Its stop reason is a nil pointer on every other
// frame, so `omitempty` drops it before any method runs: every content block,
// every synthesized terminal frame and every non-streaming response renders
// byte-identically to stock, at stock cost (BenchmarkStreamDeltaMarshal).
