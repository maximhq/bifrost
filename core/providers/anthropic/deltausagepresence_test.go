package anthropic

import (
	"testing"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Anthropic reports usage as per-request totals, not per-event increments, and
// the final message_delta may carry output_tokens alone. AnthropicUsage holds
// those counters as plain ints, so an omitted input_tokens is indistinguishable
// from zero once unmarshalled and is re-emitted as an explicit
// "input_tokens": 0 — overwriting the real count for any client that applies
// delta fields whenever they are present, which the Anthropic SDK's usage
// accumulator does.
//
// Dropping the field instead is not an option: messagestartusage_test.go pins
// input_tokens and output_tokens as always present, because @ai-sdk/anthropic's
// schema marks message.usage required. So the value has to be completed rather
// than omitted.

// replayNativeStream runs raw SSE payloads through the inbound converter and
// back out through the Anthropic converter, as the native /v1/messages path
// does, and returns the re-emitted events.
func replayNativeStream(t *testing.T, raws []string) []*AnthropicStreamEvent {
	t.Helper()

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var emitted []*AnthropicStreamEvent
	seq := 0
	for _, raw := range raws {
		var chunk AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		for _, r := range responses {
			seq++
			emitted = append(emitted, ToAnthropicResponsesStreamResponse(ctx, r)...)
		}
	}
	return emitted
}

// messageDeltaUsage returns the usage object of the single re-emitted
// message_delta, as it would appear on the wire.
func messageDeltaUsage(t *testing.T, emitted []*AnthropicStreamEvent) gjson.Result {
	t.Helper()

	var found []string
	for _, e := range emitted {
		if e == nil || e.Type != AnthropicStreamEventTypeMessageDelta {
			continue
		}
		data, err := sonic.Marshal(e)
		if err != nil {
			t.Fatalf("marshal message_delta: %v", err)
		}
		found = append(found, string(data))
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 message_delta, got %d: %v", len(found), found)
	}
	usage := gjson.Get(found[0], "usage")
	if !usage.IsObject() {
		t.Fatalf("message_delta must carry a usage object, got %s", found[0])
	}
	return usage
}

const (
	msgStartWithInput = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":100,"output_tokens":1}}}`
	msgDeltaOutputOnly = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`
)

// TestMessageDeltaKeepsInputTokensFromMessageStart is the case reported in
// #7966: an upstream whose final delta omits the input side.
func TestMessageDeltaKeepsInputTokensFromMessageStart(t *testing.T) {
	usage := messageDeltaUsage(t, replayNativeStream(t, []string{
		msgStartWithInput,
		msgDeltaOutputOnly,
	}))

	if !usage.Get("input_tokens").Exists() {
		t.Fatalf("input_tokens must stay present on message_delta, got %s", usage.Raw)
	}
	if got := usage.Get("input_tokens").Int(); got != 100 {
		t.Errorf("input_tokens = %d, want 100 (the count from message_start); got %s", got, usage.Raw)
	}
	if got := usage.Get("output_tokens").Int(); got != 20 {
		t.Errorf("output_tokens = %d, want 20 (the delta is authoritative for output)", got)
	}
}

// TestMessageDeltaKeepsCacheCountsFromMessageStart covers the rest of the input
// side, which is where the cache-aware totals come from.
func TestMessageDeltaKeepsCacheCountsFromMessageStart(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1,` +
		`"cache_read_input_tokens":90,"cache_creation_input_tokens":7,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":2}}}}`

	usage := messageDeltaUsage(t, replayNativeStream(t, []string{start, msgDeltaOutputOnly}))

	if got := usage.Get("cache_read_input_tokens").Int(); got != 90 {
		t.Errorf("cache_read_input_tokens = %d, want 90; got %s", got, usage.Raw)
	}
	if got := usage.Get("cache_creation_input_tokens").Int(); got != 7 {
		t.Errorf("cache_creation_input_tokens = %d, want 7; got %s", got, usage.Raw)
	}
	if got := usage.Get("cache_creation.ephemeral_5m_input_tokens").Int(); got != 5 {
		t.Errorf("ephemeral_5m_input_tokens = %d, want 5; got %s", got, usage.Raw)
	}
	if got := usage.Get("cache_creation.ephemeral_1h_input_tokens").Int(); got != 2 {
		t.Errorf("ephemeral_1h_input_tokens = %d, want 2; got %s", got, usage.Raw)
	}
}

// TestMessageDeltaPrefersItsOwnInputTokens guards the direction of the fill:
// real Claude repeats the input count on the final delta, and that value — not
// the one from message_start — has to win.
func TestMessageDeltaPrefersItsOwnInputTokens(t *testing.T) {
	delta := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
		`"usage":{"input_tokens":150,"output_tokens":20}}`

	usage := messageDeltaUsage(t, replayNativeStream(t, []string{msgStartWithInput, delta}))

	if got := usage.Get("input_tokens").Int(); got != 150 {
		t.Errorf("input_tokens = %d, want 150 (the delta's own, larger count)", got)
	}
}

// TestMessageDeltaWithoutMessageStartUsageIsUnchanged covers the upstreams that
// send no usage on message_start at all (Bedrock Converse): there is nothing to
// complete, and the delta must pass through as before.
func TestMessageDeltaWithoutMessageStartUsageIsUnchanged(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-4-6","content":[]}}`

	usage := messageDeltaUsage(t, replayNativeStream(t, []string{start, msgDeltaOutputOnly}))

	if got := usage.Get("input_tokens").Int(); got != 0 {
		t.Errorf("input_tokens = %d, want 0 when message_start carried no usage", got)
	}
	if got := usage.Get("output_tokens").Int(); got != 20 {
		t.Errorf("output_tokens = %d, want 20", got)
	}
}

// TestStreamStateDoesNotLeakStartUsage pins the pool resets. These states are
// reused across requests, so a retained StartUsage would attribute one
// request's input tokens to the next.
//
// flush() is called directly rather than through Release/Acquire: sync.Pool is
// free to hand back a different object, which would let this pass without the
// reset ever running.
func TestStreamStateDoesNotLeakStartUsage(t *testing.T) {
	t.Run("flush", func(t *testing.T) {
		state := AcquireAnthropicResponsesStreamState()
		defer ReleaseAnthropicResponsesStreamState(state)

		state.StartUsage = &AnthropicUsage{InputTokens: 4242}
		state.flush()
		if state.StartUsage != nil {
			t.Errorf("flush left StartUsage set: %+v", state.StartUsage)
		}
	})

	// Acquire resets the same fields again for states that predate a flush.
	t.Run("acquire", func(t *testing.T) {
		seeded := AcquireAnthropicResponsesStreamState()
		seeded.StartUsage = &AnthropicUsage{InputTokens: 4242}
		anthropicResponsesStreamStatePool.Put(seeded) // bypass flush on purpose

		state := AcquireAnthropicResponsesStreamState()
		defer ReleaseAnthropicResponsesStreamState(state)
		if state == seeded && state.StartUsage != nil {
			t.Errorf("Acquire returned a state still carrying StartUsage: %+v", state.StartUsage)
		}
	})
}
