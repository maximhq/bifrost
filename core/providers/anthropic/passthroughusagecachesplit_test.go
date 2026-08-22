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

// Moonshot/Kimi, the provider #5510 was originally filed against. Frames as reported in that
// issue. Note message_start carries cache_read_input_tokens EXPLICITLY AS ZERO while input_tokens
// already includes the cached prefix — so a merge that decides authority by field *presence*
// rather than by value is fooled by this frame and still double-counts.
const (
	kimiMessageStart = `{"type":"message_start","message":{"id":"chatcmpl-6a5e00ec","type":"message",` +
		`"role":"assistant","content":[],"model":"kimi-k3","usage":{"input_tokens":173306,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,` +
		`"service_tier":"standard","prompt_tokens":173306,"cached_tokens":0}}}`
	kimiMessageDelta = `{"type":"message_delta","usage":{"input_tokens":250,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":173056,"output_tokens":166,` +
		`"output_tokens_details":{"thinking_tokens":1},"prompt_tokens":173306,` +
		`"completion_tokens":166,"total_tokens":173472,"cached_tokens":173056}}`
)

// The SAME provider does not always deviate: captured from api.kimi.com on 2026-08-20, message_start
// was fully cache-aware. Per #5510 this is consistent with a hierarchical cache whose final split is
// not known when message_start is emitted, so the deviation is per-REQUEST, not per-provider. The
// fix must therefore leave this compliant variant untouched too.
const (
	kimiCacheAwareMessageStart = `{"type":"message_start","message":{"id":"msg_fWQAebqcV0MJ",` +
		`"type":"message","role":"assistant","content":[],"model":"k3","usage":{"input_tokens":0,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":8931,"output_tokens":0,` +
		`"service_tier":"standard","inference_geo":"not_available"}}}`
	kimiCacheAwareMessageDelta = `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},` +
		`"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":8931,` +
		`"output_tokens":16,"output_tokens_details":{"thinking_tokens":13}}}`
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

// Moonshot/Kimi, the case #5510 was filed for. Its own numbers are internally consistent:
// 250 + 173056 = 173306 input, + 166 output = 173472 total.
func TestPassthroughStreamUsageKimiDoesNotDoubleCountCachedPrompt(t *testing.T) {
	const wantPrompt = 250 + 173056

	got, gotOut := observePassthroughUsage(t, kimiMessageStart, kimiMessageDelta)
	if got != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d (inflated by %d — message_start.input_tokens "+
			"counted on top of cache_read)", got, wantPrompt, got-wantPrompt)
	}
	if gotOut != 166 {
		t.Errorf("completion tokens = %d, want 166", gotOut)
	}
}

// Event order must not change the result: the merge is documented as order-independent.
func TestPassthroughStreamUsageQwenOrderIndependent(t *testing.T) {
	for _, tc := range []struct{ name, start, delta string }{
		{"qwen", qwenMessageStart, qwenMessageDelta},
		{"kimi", kimiMessageStart, kimiMessageDelta},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forward, _ := observePassthroughUsage(t, tc.start, tc.delta)
			reverse, _ := observePassthroughUsage(t, tc.delta, tc.start)
			if forward != reverse {
				t.Errorf("order-dependent: forward=%d reverse=%d", forward, reverse)
			}
		})
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
		{"kimi/cache-aware message_start", kimiCacheAwareMessageStart, kimiCacheAwareMessageDelta, 0 + 8931, 16},
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
