package anthropic

import "testing"

// Anthropic-dialect upstreams do not agree on what message_start.usage.input_tokens means.
// Anthropic itself documents it as EXCLUDING the cache counters, so input + cache_read +
// cache_creation is the prompt total. QwenCloud's /apps/anthropic endpoint instead reports the
// FULL prompt count there and omits the cache counters entirely, only sending the real
// cache-aware split in message_delta. Max-merging input_tokens across both events therefore
// counted the prefix-cached prompt twice — once as input, once as cache_read — and roughly
// doubled the logged prompt_tokens on every streaming request to Qwen.
//
// Frames below are captured verbatim from the live upstreams on 2026-08-20.

// captured from https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1/messages
const (
	qwenMessageStart = `{"type":"message_start","message":{"id":"msg_ba0b5ef1","type":"message",` +
		`"role":"assistant","model":"qwen3.7-plus","content":[],"stop_reason":null,` +
		`"stop_sequence":null,"usage":{"input_tokens":8834,"output_tokens":0}}}`
	qwenMessageDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
		`"usage":{"input_tokens":18,"output_tokens":426,"cache_creation_input_tokens":0,` +
		`"cache_read_input_tokens":8831,"cache_creation":{"ephemeral_5m_input_tokens":0}}}`
)

// captured from https://api.minimax.io/anthropic/v1/messages — the control: its message_start
// reports input_tokens 0, so the old max-merge already produced the right answer here.
const (
	minimaxMessageStart = `{"type":"message_start","message":{"id":"msg_mm","type":"message",` +
		`"role":"assistant","model":"MiniMax-M3","content":[],` +
		`"usage":{"input_tokens":0,"output_tokens":0,"service_tier":"standard"}}}`
	minimaxMessageDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
		`"usage":{"cache_read_input_tokens":8990,"input_tokens":1,"output_tokens":2,` +
		`"service_tier":"standard"}}`
)

// a stock Anthropic pair: cache counters ride on message_start, message_delta carries only output.
const (
	stockAnthropicMessageStart = `{"type":"message_start","message":{"id":"msg_a","type":"message",` +
		`"role":"assistant","model":"claude-opus-5","content":[],` +
		`"usage":{"input_tokens":18,"cache_creation_input_tokens":0,` +
		`"cache_read_input_tokens":8831,"output_tokens":1}}}`
	stockAnthropicMessageDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
		`"usage":{"output_tokens":426}}`
)

func observePassthroughUsage(t *testing.T, events ...string) (prompt, completion int) {
	t.Helper()
	var acc AnthropicPassthroughStreamUsage
	var last = struct{ p, c int }{}
	for _, e := range events {
		if u := acc.ObserveEvent([]byte(e)); u != nil && u.LLMUsage != nil {
			last.p, last.c = u.LLMUsage.PromptTokens, u.LLMUsage.CompletionTokens
		}
	}
	return last.p, last.c
}

func TestPassthroughStreamUsageQwenDoesNotDoubleCountCachedPrompt(t *testing.T) {
	// truth, straight from the non-streaming body for the same request:
	//   {"input_tokens":18,"cache_creation_input_tokens":0,"cache_read_input_tokens":8831}
	const wantPrompt = 18 + 8831

	got, gotOut := observePassthroughUsage(t, qwenMessageStart, qwenMessageDelta)
	if got != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d (inflated by %d — message_start.input_tokens "+
			"counted on top of cache_read)", got, wantPrompt, got-wantPrompt)
	}
	if gotOut != 426 {
		t.Errorf("completion tokens = %d, want 426", gotOut)
	}
}

// Event order must not change the result: the merge is documented as order-independent.
func TestPassthroughStreamUsageQwenOrderIndependent(t *testing.T) {
	forward, _ := observePassthroughUsage(t, qwenMessageStart, qwenMessageDelta)
	reverse, _ := observePassthroughUsage(t, qwenMessageDelta, qwenMessageStart)
	if forward != reverse {
		t.Errorf("order-dependent: forward=%d reverse=%d", forward, reverse)
	}
}

// The two upstreams that already reported correctly must be unaffected by the fix.
func TestPassthroughStreamUsageCorrectUpstreamsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name             string
		start, delta     string
		wantPrompt, want int
	}{
		{"minimax", minimaxMessageStart, minimaxMessageDelta, 1 + 8990, 2},
		{"anthropic", stockAnthropicMessageStart, stockAnthropicMessageDelta, 18 + 8831, 426},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, c := observePassthroughUsage(t, tc.start, tc.delta)
			if p != tc.wantPrompt {
				t.Errorf("prompt tokens = %d, want %d", p, tc.wantPrompt)
			}
			if c != tc.want {
				t.Errorf("completion tokens = %d, want %d", c, tc.want)
			}
		})
	}
}

// A stream that never sends a cache-aware event must still report its input_tokens.
func TestPassthroughStreamUsageNoCacheCountersFallsBackToLooseInput(t *testing.T) {
	p, _ := observePassthroughUsage(t,
		`{"type":"message_start","message":{"usage":{"input_tokens":500,"output_tokens":0}}}`,
		`{"type":"message_delta","usage":{"output_tokens":7}}`)
	if p != 500 {
		t.Errorf("prompt tokens = %d, want 500", p)
	}
}
