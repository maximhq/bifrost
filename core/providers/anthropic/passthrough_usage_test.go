package anthropic_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractAnthropicPassthroughUsage(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		body  string
		check func(t *testing.T, u *schemas.BifrostPassthroughUsage)
	}{
		{
			name: "messages non-stream usage + service tier mapping",
			path: "/v1/messages",
			body: `{"usage":{"input_tokens":66,"output_tokens":26,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"service_tier":"standard"}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				anthropicMustLLM(t, u, 66, 26, 92)
				// Anthropic "standard" normalizes to the neutral "default".
				if u.ServiceTier == nil || *u.ServiceTier != schemas.BifrostServiceTierDefault {
					t.Fatalf("service tier = %v, want default", u.ServiceTier)
				}
			},
		},
		{
			name: "messages with cache 5m/1h breakdown",
			path: "/v1/messages",
			body: `{"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":3}}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				// PromptTokens = input + cache_read + cache_creation = 10 + 3 + 7 = 20
				anthropicMustLLM(t, u, 20, 5, 25)
				d := u.LLMUsage.PromptTokensDetails
				if d == nil || d.CachedReadTokens != 3 || d.CachedWriteTokens != 7 {
					t.Fatalf("cache tokens = %+v", d)
				}
				if d.CachedWriteTokenDetails == nil || d.CachedWriteTokenDetails.CachedWriteTokens1h != 3 ||
					d.CachedWriteTokenDetails.CachedWriteTokens5m != 4 {
					t.Fatalf("5m/1h breakdown = %+v", d.CachedWriteTokenDetails)
				}
			},
		},
		{
			name: "messages with web search server tool",
			path: "/v1/messages",
			body: `{"usage":{"input_tokens":10,"output_tokens":5,"server_tool_use":{"web_search_requests":2}}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.LLMUsage == nil || u.LLMUsage.CompletionTokensDetails == nil ||
					u.LLMUsage.CompletionTokensDetails.NumSearchQueries == nil ||
					*u.LLMUsage.CompletionTokensDetails.NumSearchQueries != 2 ||
					u.LLMUsage.ToolUsage.WebSearch.NumRequests != 2 {
					t.Fatalf("web search requests = %+v, want 2", u)
				}
			},
		},
		{
			name: "messages with priority tier",
			path: "/v1/messages",
			body: `{"usage":{"input_tokens":1,"output_tokens":1,"service_tier":"priority"}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u == nil || u.ServiceTier == nil || *u.ServiceTier != schemas.BifrostServiceTierPriority {
					t.Fatalf("service tier = %v, want priority", u.ServiceTier)
				}
			},
		},
		{
			name: "legacy complete endpoint",
			path: "/v1/complete",
			body: `{"completion":"hi","usage":{"input_tokens":8,"output_tokens":4}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				anthropicMustLLM(t, u, 8, 4, 12)
			},
		},
		{
			name: "messages zero usage -> nil",
			path: "/v1/messages",
			body: `{"usage":{"input_tokens":0,"output_tokens":0}}`,
			check: func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
				if u != nil {
					t.Fatalf("expected nil, got %+v", u)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := anthropic.ExtractAnthropicPassthroughUsage(tt.path, nil, []byte(tt.body))
			tt.check(t, u)
		})
	}
}

// TestAnthropicPassthroughStreamUsage exercises the per-event max-merge accumulator used for
// streaming /messages: input/cache/tier come from message_start, final output from message_delta.
func TestAnthropicPassthroughStreamUsage(t *testing.T) {
	t.Run("merges message_start + message_delta", func(t *testing.T) {
		acc := &anthropic.AnthropicPassthroughStreamUsage{}

		// Non-usage event before any usage -> still nil.
		if u := acc.ObserveEvent([]byte(`{"type":"content_block_start","index":0}`)); u != nil {
			t.Fatalf("expected nil before any usage event, got %+v", u)
		}

		// message_start: input + cache 5m/1h + service_tier (output is a placeholder here).
		acc.ObserveEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":66,"cache_read_input_tokens":2,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3},"output_tokens":1,"service_tier":"standard"}}}`))
		// content delta carries no usage; must not disturb the running totals.
		acc.ObserveEvent([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`))
		// message_delta: final output tokens.
		u := acc.ObserveEvent([]byte(`{"type":"message_delta","usage":{"output_tokens":26}}`))

		// PromptTokens = input(66) + cache_read(2) + cache_creation(5) = 73; output = 26.
		anthropicMustLLM(t, u, 73, 26, 99)
		d := u.LLMUsage.PromptTokensDetails
		if d == nil || d.CachedWriteTokenDetails == nil || d.CachedWriteTokenDetails.CachedWriteTokens1h != 3 {
			t.Fatalf("1h cache split lost in merge: %+v", d)
		}
		// service_tier from message_start, normalized.
		if u.ServiceTier == nil || *u.ServiceTier != schemas.BifrostServiceTierDefault {
			t.Fatalf("service tier = %v, want default", u.ServiceTier)
		}
	})

	t.Run("server tool use from message_delta", func(t *testing.T) {
		acc := &anthropic.AnthropicPassthroughStreamUsage{}
		acc.ObserveEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`))
		u := acc.ObserveEvent([]byte(`{"type":"message_delta","usage":{"output_tokens":4,"server_tool_use":{"web_search_requests":3}}}`))
		if u == nil || u.LLMUsage == nil || u.LLMUsage.CompletionTokensDetails == nil ||
			u.LLMUsage.CompletionTokensDetails.NumSearchQueries == nil ||
			*u.LLMUsage.CompletionTokensDetails.NumSearchQueries != 3 ||
			u.LLMUsage.ToolUsage.WebSearch.NumRequests != 3 {
			t.Fatalf("web search requests = %+v, want 3", u)
		}
	})
}

// An advisor pass runs on the advisor tool's model and is reported only in usage.iterations[], so the
// passthrough usage keeps that breakdown next to the folded totals: pricing reads the advisor passes
// from it. The folded totals stay exactly what they were (executor plus compaction), on both the
// unary body and the stream, where the cumulative iterations list must not be folded a second time.
func TestPassthroughUsageKeepsAdvisorIterations(t *testing.T) {
	const usage = `{"input_tokens":1000,"output_tokens":500,"iterations":[` +
		`{"type":"compaction","input_tokens":300,"output_tokens":100},` +
		`{"type":"message","input_tokens":1000,"output_tokens":500},` +
		`{"type":"advisor_message","model":"claude-opus-4-8","input_tokens":2000,"cache_read_input_tokens":100,"output_tokens":1000}]}`
	check := func(t *testing.T, u *schemas.BifrostPassthroughUsage) {
		t.Helper()
		// Folded totals: executor 1000/500 plus compaction 300/100; the advisor pass is not folded in.
		anthropicMustLLM(t, u, 1300, 600, 1900)
		if len(u.Iterations) != 3 {
			t.Fatalf("iterations = %d, want 3: %+v", len(u.Iterations), u.Iterations)
		}
		advisor := u.Iterations[2]
		if advisor.Type == nil || *advisor.Type != "advisor_message" || advisor.Model == nil || *advisor.Model != "claude-opus-4-8" {
			t.Fatalf("advisor pass lost its type or model: %+v", advisor)
		}
		if advisor.InputTokens != 2100 || advisor.OutputTokens != 1000 || advisor.InputTokensDetails == nil || advisor.InputTokensDetails.CachedReadTokens != 100 {
			t.Fatalf("advisor pass lost its token breakdown: %+v", advisor)
		}
	}
	t.Run("unary", func(t *testing.T) {
		check(t, anthropic.ExtractAnthropicPassthroughUsage("/v1/messages", nil, []byte(`{"usage":`+usage+`}`)))
	})
	t.Run("stream", func(t *testing.T) {
		acc := &anthropic.AnthropicPassthroughStreamUsage{}
		acc.ObserveEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1000,"output_tokens":1}}}`))
		delta := []byte(`{"type":"message_delta","usage":` + usage + `}`)
		check(t, acc.ObserveEvent(delta))
		check(t, acc.ObserveEvent(delta)) // a repeated cumulative list replaces, never adds
		check(t, acc.ObserveEvent([]byte(`{"type":"message_stop"}`)))
	})
	t.Run("no iterations", func(t *testing.T) {
		u := anthropic.ExtractAnthropicPassthroughUsage("/v1/messages", nil, []byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`))
		anthropicMustLLM(t, u, 10, 5, 15)
		if u.Iterations != nil {
			t.Fatalf("iterations = %+v, want nil", u.Iterations)
		}
	})
}

func BenchmarkExtractAnthropicPassthroughUsageIterations(b *testing.B) {
	for _, tc := range []struct{ name, body string }{
		{"no_iterations", `{"usage":{"input_tokens":1000,"output_tokens":500}}`},
		{"advisor_iterations", `{"usage":{"input_tokens":1000,"output_tokens":500,"iterations":[{"type":"message","input_tokens":1000,"output_tokens":500},{"type":"advisor_message","model":"claude-opus-4-8","input_tokens":2000,"output_tokens":1000}]}}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := []byte(tc.body)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				anthropic.ExtractAnthropicPassthroughUsage("/v1/messages", nil, body)
			}
		})
	}
}

func TestHasAnthropicPassthroughUsage(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  bool
	}{
		{"message_start (nested message.usage)", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`, true},
		{"message_delta (top-level usage)", `{"type":"message_delta","usage":{"output_tokens":1}}`, true},
		{"content_block_delta", `{"type":"content_block_delta","delta":{"text":"x"}}`, false},
		{"ping", `{"type":"ping"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anthropic.HasAnthropicPassthroughUsage([]byte(tt.event)); got != tt.want {
				t.Fatalf("HasAnthropicPassthroughUsage = %v, want %v", got, tt.want)
			}
		})
	}
}

func anthropicMustLLM(t *testing.T, u *schemas.BifrostPassthroughUsage, prompt, completion, total int) {
	t.Helper()
	if u == nil || u.LLMUsage == nil {
		t.Fatalf("expected LLMUsage, got %+v", u)
	}
	if u.LLMUsage.PromptTokens != prompt || u.LLMUsage.CompletionTokens != completion || u.LLMUsage.TotalTokens != total {
		t.Fatalf("LLMUsage = {prompt:%d completion:%d total:%d}, want {%d %d %d}",
			u.LLMUsage.PromptTokens, u.LLMUsage.CompletionTokens, u.LLMUsage.TotalTokens, prompt, completion, total)
	}
}

func TestClassifyPassthroughPath(t *testing.T) {
	tests := []struct {
		path string
		want schemas.RequestType
	}{
		{"/v1/messages", schemas.ResponsesRequest},
		{"/v1/messages?beta=true", schemas.ResponsesRequest},
		{"/anthropic/v1/messages", schemas.ResponsesRequest},
		{"/v1/messages/count_tokens", schemas.CountTokensRequest},
		{"/v1/complete", schemas.TextCompletionRequest},
		{"/v1/messages/batches", ""},
		{"/v1/models", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := anthropic.ClassifyPassthroughPath(tt.path); got != tt.want {
				t.Fatalf("ClassifyPassthroughPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

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
// already includes the cached prefix, so message_start's explicit counters must not be trusted
// over message_delta's.
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
	var acc anthropic.AnthropicPassthroughStreamUsage
	var last = struct{ p, c int }{}
	for _, e := range events {
		if u := acc.ObserveEvent([]byte(e)); u != nil && u.LLMUsage != nil {
			last.p, last.c = u.LLMUsage.PromptTokens, u.LLMUsage.CompletionTokens
		}
	}
	return last.p, last.c
}

// promptDetails replays the events and returns the final PromptTokensDetails, for the cases that
// assert on the cache breakdown rather than only on the totals.
func promptDetails(t *testing.T, events ...string) *schemas.ChatPromptTokensDetails {
	t.Helper()
	var acc anthropic.AnthropicPassthroughStreamUsage
	var last *schemas.ChatPromptTokensDetails
	for _, e := range events {
		if u := acc.ObserveEvent([]byte(e)); u != nil && u.LLMUsage != nil {
			last = u.LLMUsage.PromptTokensDetails
		}
	}
	return last
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

// An upstream may report cache creation only as the ephemeral breakdown, without the top-level
// cache_creation_input_tokens. Anthropic documents cache_creation as the 5m/1h split OF that
// total, so such a frame is still cache-aware and must be treated as authoritative for
// input_tokens. Deciding authority on the two top-level counters alone misread it as cache-less
// and fell back to the prompt-scale figure.
func TestPassthroughStreamUsageBreakdownOnlyFrameIsAuthoritative(t *testing.T) {
	const (
		breakdownOnlyStart = `{"type":"message_start","message":{"id":"msg_b","type":"message",` +
			`"role":"assistant","model":"claude-opus-5","content":[],` +
			`"usage":{"input_tokens":50000,"output_tokens":0}}}`
		breakdownOnlyDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
			`"usage":{"input_tokens":1000,"cache_creation":{"ephemeral_5m_input_tokens":49000},` +
			`"output_tokens":20}}`
	)
	// message_delta is authoritative via its ephemeral breakdown, so input_tokens is 1000, not the
	// prompt-scale 50000. cache_creation_input_tokens is absent, so the 49000-token breakdown IS
	// the cache-creation total and must be counted — dropping it silently undercounts the prompt.
	const wantPrompt = 1000 + 49000

	got, gotOut := observePassthroughUsage(t, breakdownOnlyStart, breakdownOnlyDelta)
	if got != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d (breakdown-only frame misread as cache-less, "+
			"so the prompt-scale message_start figure was kept)", got, wantPrompt)
	}
	if gotOut != 20 {
		t.Errorf("completion tokens = %d, want 20", gotOut)
	}
	// the breakdown must survive into the cache-write details, not just the total
	if d := promptDetails(t, breakdownOnlyStart, breakdownOnlyDelta); d == nil {
		t.Error("prompt token details = nil, want cache-write details carrying the breakdown")
	} else {
		if d.CachedWriteTokens != 49000 {
			t.Errorf("cached write tokens = %d, want 49000", d.CachedWriteTokens)
		}
		if d.CachedWriteTokenDetails == nil || d.CachedWriteTokenDetails.CachedWriteTokens5m != 49000 {
			t.Errorf("cached write 5m breakdown = %+v, want 49000", d.CachedWriteTokenDetails)
		}
	}
	// order-independent, like every other case here
	if rev, _ := observePassthroughUsage(t, breakdownOnlyDelta, breakdownOnlyStart); rev != wantPrompt {
		t.Errorf("reversed order: prompt tokens = %d, want %d", rev, wantPrompt)
	}
}

// A message_start can carry a prompt-scale input_tokens together with a PARTIALLY known cache_read
// — the shape #5510's hierarchical-cache explanation predicts, where the final split is not settled
// when message_start is emitted. That frame clears any authority test, so max-merging input_tokens
// and cache_read independently keeps the prompt-scale figure and adds the final cache_read on top,
// reproducing the original double-count. Latching input_tokens from the event with the largest
// cache total instead takes the two numbers from the same frame.
func TestPassthroughStreamUsagePartiallyKnownCacheOnMessageStart(t *testing.T) {
	const (
		partialStart = `{"type":"message_start","message":{"id":"msg_p","type":"message",` +
			`"role":"assistant","model":"kimi-k3","content":[],"usage":{"input_tokens":173306,` +
			`"cache_creation_input_tokens":0,"cache_read_input_tokens":5000,"output_tokens":0}}}`
		partialDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
			`"usage":{"input_tokens":250,"cache_creation_input_tokens":0,` +
			`"cache_read_input_tokens":173056,"output_tokens":166}}`
	)
	const wantPrompt = 250 + 173056

	got, gotOut := observePassthroughUsage(t, partialStart, partialDelta)
	if got != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d (inflated by %d — message_start.input_tokens kept "+
			"alongside the final cache_read)", got, wantPrompt, got-wantPrompt)
	}
	if gotOut != 166 {
		t.Errorf("completion tokens = %d, want 166", gotOut)
	}
	if rev, _ := observePassthroughUsage(t, partialDelta, partialStart); rev != wantPrompt {
		t.Errorf("reversed order: prompt tokens = %d, want %d", rev, wantPrompt)
	}
}

// passthroughBothOrders asserts the prompt total for start+delta in both arrival orders.
func passthroughBothOrders(t *testing.T, start, delta string, wantPrompt int) {
	t.Helper()
	if got, _ := observePassthroughUsage(t, start, delta); got != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d", got, wantPrompt)
	}
	if got, _ := observePassthroughUsage(t, delta, start); got != wantPrompt {
		t.Errorf("reversed order: prompt tokens = %d, want %d", got, wantPrompt)
	}
}

// Anthropic's message_delta usage is cumulative: with server tools each extra sampling round adds
// input, so message_delta.input_tokens exceeds message_start's while the cache counters can stay the
// same. The delta's figure is the bill; keeping message_start's smaller input_tokens under-bills.
func TestPassthroughStreamUsageCumulativeServerToolDeltaWins(t *testing.T) {
	const (
		start = `{"type":"message_start","message":{"usage":{"input_tokens":1000,` +
			`"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":1}}}`
		delta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":9000,` +
			`"cache_read_input_tokens":20000,"cache_creation_input_tokens":0,"output_tokens":300,` +
			`"server_tool_use":{"web_search_requests":1}}}`
	)
	passthroughBothOrders(t, start, delta, 9000+20000)
}

// A partially known cache_creation on message_start must not survive next to message_delta's final
// split: all prompt counters come from the delta, so the 5000 is not added on top.
func TestPassthroughStreamUsagePartiallyKnownCacheCreationOnMessageStart(t *testing.T) {
	const (
		start = `{"type":"message_start","message":{"usage":{"input_tokens":173306,` +
			`"cache_creation_input_tokens":5000,"cache_creation":{"ephemeral_5m_input_tokens":5000},` +
			`"cache_read_input_tokens":0,"output_tokens":0}}}`
		delta = `{"type":"message_delta","usage":{"input_tokens":250,"cache_creation_input_tokens":0,` +
			`"cache_read_input_tokens":173056,"output_tokens":166}}`
	)
	passthroughBothOrders(t, start, delta, 250+173056)
	if d := promptDetails(t, start, delta); d == nil || d.CachedWriteTokens != 0 || d.CachedWriteTokenDetails != nil {
		t.Errorf("prompt details = %+v, want no cache writes (message_start's split is stale)", d)
	}
}

// Anthropic's delta repeats cache_creation_input_tokens but not the 5m/1h object. message_start's
// split still describes that total, so the 1h share (billed at the higher rate) must survive.
func TestPassthroughStreamUsageDeltaWithoutSplitKeepsStartSplit(t *testing.T) {
	const (
		start = `{"type":"message_start","message":{"usage":{"input_tokens":10,` +
			`"cache_creation_input_tokens":3000,"cache_creation":{"ephemeral_5m_input_tokens":1000,` +
			`"ephemeral_1h_input_tokens":2000},"cache_read_input_tokens":0,"output_tokens":1}}}`
		delta = `{"type":"message_delta","usage":{"input_tokens":10,"cache_creation_input_tokens":3000,` +
			`"cache_read_input_tokens":0,"output_tokens":50}}`
	)
	passthroughBothOrders(t, start, delta, 10+3000)
	d := promptDetails(t, start, delta)
	if d == nil || d.CachedWriteTokens != 3000 || d.CachedWriteTokenDetails == nil ||
		d.CachedWriteTokenDetails.CachedWriteTokens1h != 2000 || d.CachedWriteTokenDetails.CachedWriteTokens5m != 1000 {
		t.Errorf("prompt details = %+v, want 3000 writes split 1000 5m / 2000 1h", d)
	}
}

// A delta that reports prompt counters only as zeros carries no prompt information, so it must not
// wipe message_start's split. A delta that reports input_tokens but omits the cache counters
// replaces only input_tokens.
func TestPassthroughStreamUsageDeltaReplacesOnlyReportedCounters(t *testing.T) {
	const start = `{"type":"message_start","message":{"usage":{"input_tokens":40,` +
		`"cache_read_input_tokens":8931,"output_tokens":0}}}`
	t.Run("all-zero delta keeps message_start", func(t *testing.T) {
		passthroughBothOrders(t, start,
			`{"type":"message_delta","usage":{"input_tokens":0,"cache_read_input_tokens":0,"output_tokens":16}}`,
			40+8931)
	})
	t.Run("input-only delta keeps message_start cache_read", func(t *testing.T) {
		passthroughBothOrders(t, start,
			`{"type":"message_delta","usage":{"input_tokens":250,"output_tokens":16}}`,
			250+8931)
	})
	t.Run("null cache_read on the delta is not a report", func(t *testing.T) {
		passthroughBothOrders(t, start,
			`{"type":"message_delta","usage":{"input_tokens":250,"cache_read_input_tokens":null,"output_tokens":16}}`,
			250+8931)
	})
}

// Compaction passes arrive only in message_delta usage.iterations. They are folded into the total
// once, on top of whichever prompt split won, including when the delta carries no top-level prompt
// counter of its own.
func TestPassthroughStreamUsageCompactionFoldedOnceOverResolvedSplit(t *testing.T) {
	const (
		start = `{"type":"message_start","message":{"usage":{"input_tokens":100,` +
			`"cache_read_input_tokens":2000,"output_tokens":1}}}`
		delta = `{"type":"message_delta","usage":{"output_tokens":300,"iterations":[` +
			`{"type":"compaction","input_tokens":500,"cache_read_input_tokens":50,"output_tokens":40},` +
			`{"type":"message","input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":260}]}}`
	)
	passthroughBothOrders(t, start, delta, 100+2000+500+50)
	var acc anthropic.AnthropicPassthroughStreamUsage
	acc.ObserveEvent([]byte(start))
	acc.ObserveEvent([]byte(delta))
	anthropicMustLLM(t, acc.ObserveEvent([]byte(delta)), 2650, 340, 2990)
}

func BenchmarkAnthropicPassthroughStreamUsageCacheSplit(b *testing.B) {
	for _, tc := range []struct{ name, start, delta string }{
		{"output_only_delta", stockAnthropicMessageStart, stockAnthropicMessageDelta},
		{"cache_split_delta", kimiMessageStart, kimiMessageDelta},
	} {
		b.Run(tc.name, func(b *testing.B) {
			start, delta := []byte(tc.start), []byte(tc.delta)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var acc anthropic.AnthropicPassthroughStreamUsage
				acc.ObserveEvent(start)
				acc.ObserveEvent(delta)
			}
		})
	}
}
