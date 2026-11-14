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
