package traceingest

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func otelSpan(attrs map[string]any) *OTelSpan {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	return &OTelSpan{TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331", ParentSpanID: "00f067aa0ba902b7", Name: "span", StartTime: start, EndTime: start.Add(time.Second), Attributes: attrs}
}

// TestMapOTelSpanReadsEveryConvention pins the mapping of each LLM convention on the OTLP wire onto
// the same Bifrost fields.
func TestMapOTelSpanReadsEveryConvention(t *testing.T) {
	t.Run("openinference with flattened messages", func(t *testing.T) {
		part := MapOTelSpan(otelSpan(map[string]any{
			"openinference.span.kind":                   "LLM",
			"llm.model_name":                            "gpt-4o",
			"llm.provider":                              "openai",
			"llm.token_count.prompt":                    int64(12),
			"llm.token_count.completion":                int64(5),
			"llm.token_count.total":                     int64(17),
			"llm.token_count.prompt_details.cache_read": int64(4),
			"llm.input_messages.0.message.role":         "system",
			"llm.input_messages.0.message.content":      "be brief",
			"llm.input_messages.1.message.role":         "user",
			"llm.input_messages.1.message.content":      "hi",
			"output.value":                              `{"id":"chatcmpl-123","choices":[{"message":{"content":"hello"}}]}`,
			"session.id":                                "sess-1",
			"user.id":                                   "u-1",
			"metadata":                                  `{"team":"search"}`,
			"tag.tags":                                  []any{"prod", "v2"},
			"custom.attribute":                          "kept",
		}))
		assert.Equal(t, logstore.TraceSpanKindLLM, part.Kind)
		assert.Equal(t, "gpt-4o", part.Model)
		assert.Equal(t, "openai", part.Provider)
		assert.Equal(t, 12, part.PromptTokens)
		assert.Equal(t, 5, part.CompletionTokens)
		assert.Equal(t, 17, part.TotalTokens)
		assert.Equal(t, 4, part.CachedReadTokens)
		assert.Equal(t, []any{
			map[string]any{"message": map[string]any{"role": "system", "content": "be brief"}},
			map[string]any{"message": map[string]any{"role": "user", "content": "hi"}},
		}, part.Input, "flattened message keys are rebuilt into a list")
		assert.Equal(t, "chatcmpl-123", part.ProviderResponseID, "the provider's response id is read from the raw output")
		assert.Equal(t, "sess-1", part.SessionID)
		assert.Equal(t, "u-1", part.UserID)
		assert.Equal(t, map[string]any{"team": "search"}, part.Metadata)
		assert.Equal(t, []string{"prod", "v2"}, part.Tags)
		assert.Equal(t, "kept", part.Attributes["custom.attribute"])
		assert.NotContains(t, part.Attributes, "output.value", "content folded into output is not stored twice")
		assert.NotContains(t, part.Attributes, "llm.input_messages.0.message.role")
		assert.Equal(t, []string{"00f067aa0ba902b7"}, part.ParentSpanIDs)
	})

	t.Run("otel gen_ai", func(t *testing.T) {
		part := MapOTelSpan(otelSpan(map[string]any{
			"gen_ai.operation.name":      "chat",
			"gen_ai.provider.name":       "anthropic",
			"gen_ai.request.model":       "claude-sonnet-5-5",
			"gen_ai.response.model":      "claude-sonnet-5-5-20260101",
			"gen_ai.response.id":         "msg_01",
			"gen_ai.usage.input_tokens":  int64(100),
			"gen_ai.usage.output_tokens": int64(20),
			"gen_ai.input.messages":      `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]`,
			"gen_ai.output.messages":     `[{"role":"assistant","parts":[{"type":"text","content":"hello"}]}]`,
		}))
		assert.Equal(t, logstore.TraceSpanKindLLM, part.Kind)
		assert.Equal(t, "claude-sonnet-5-5-20260101", part.Model, "the response model wins over the requested one")
		assert.Equal(t, "anthropic", part.Provider)
		assert.Equal(t, "msg_01", part.ProviderResponseID)
		assert.Equal(t, 100, part.PromptTokens)
		assert.Equal(t, 20, part.CompletionTokens)
		require.IsType(t, []any{}, part.Input)
	})

	t.Run("langfuse", func(t *testing.T) {
		part := MapOTelSpan(otelSpan(map[string]any{
			"langfuse.observation.type":            "generation",
			"langfuse.observation.input":           `[{"role":"user","content":"hi"}]`,
			"langfuse.observation.output":          "hello",
			"langfuse.observation.model.name":      "gpt-4o-mini",
			"langfuse.observation.usage_details":   `{"input":7,"output":2,"total":9}`,
			"langfuse.observation.cost_details":    `{"total":0.0003}`,
			"langfuse.observation.metadata.source": "cron",
			"langfuse.session.id":                  "s-9",
			"langfuse.user.id":                     "u-9",
			"langfuse.trace.tags":                  []any{"nightly"},
			"langfuse.environment":                 "staging",
		}))
		assert.Equal(t, logstore.TraceSpanKindLLM, part.Kind)
		assert.Equal(t, "hello", part.Output)
		assert.Equal(t, "gpt-4o-mini", part.Model)
		assert.Equal(t, 7, part.PromptTokens)
		assert.Equal(t, 9, part.TotalTokens)
		require.NotNil(t, part.Cost)
		assert.InDelta(t, 0.0003, *part.Cost, 1e-12, "a cost the SDK sent is kept")
		assert.Equal(t, map[string]any{"source": "cron"}, part.Metadata)
		assert.Equal(t, "s-9", part.SessionID)
		assert.Equal(t, "staging", part.Environment)
		assert.Equal(t, []string{"nightly"}, part.Tags)
	})

	t.Run("openllmetry", func(t *testing.T) {
		part := MapOTelSpan(otelSpan(map[string]any{
			"traceloop.span.kind":         "workflow",
			"traceloop.entity.input":      `{"inputs":{"q":"hi"}}`,
			"traceloop.entity.output":     `{"outputs":"ok"}`,
			"gen_ai.prompt.0.role":        "user",
			"gen_ai.prompt.0.content":     "hi",
			"gen_ai.completion.0.content": "hello",
		}))
		assert.Equal(t, logstore.TraceSpanKindChain, part.Kind)
		assert.Equal(t, []any{map[string]any{"role": "user", "content": "hi"}}, part.Input, "legacy indexed prompt keys are rebuilt")
	})

	t.Run("vercel ai sdk", func(t *testing.T) {
		inner := MapOTelSpan(otelSpan(map[string]any{
			"ai.operationId":            "ai.generateText.doGenerate",
			"ai.model.id":               "gpt-4o",
			"ai.model.provider":         "openai.chat",
			"ai.prompt.messages":        `[{"role":"user","content":"hi"}]`,
			"ai.response.text":          "hello",
			"ai.response.id":            "chatcmpl-9",
			"ai.usage.promptTokens":     int64(3),
			"ai.usage.completionTokens": int64(1),
		}))
		assert.Equal(t, logstore.TraceSpanKindLLM, inner.Kind)
		assert.Equal(t, 3, inner.PromptTokens)
		assert.Equal(t, "chatcmpl-9", inner.ProviderResponseID)
		outer := MapOTelSpan(otelSpan(map[string]any{"ai.operationId": "ai.generateText", "ai.usage.promptTokens": int64(3)}))
		assert.Equal(t, logstore.TraceSpanKindFunction, outer.Kind, "the outer span repeats the inner call's usage and must not count as a model call")
	})

	t.Run("mlflow", func(t *testing.T) {
		part := MapOTelSpan(otelSpan(map[string]any{
			"mlflow.spanType":        `"CHAT_MODEL"`,
			"mlflow.spanInputs":      `{"messages":[{"role":"user","content":"hi"}]}`,
			"mlflow.spanOutputs":     `{"content":"hello"}`,
			"mlflow.chat.tokenUsage": `{"input_tokens":4,"output_tokens":2,"total_tokens":6}`,
		}))
		assert.Equal(t, logstore.TraceSpanKindLLM, part.Kind)
		assert.Equal(t, 6, part.TotalTokens)
	})
}

// TestMapOTelSpanNeverDropsASpan pins the raw-storage rule: a span no convention explains is kept
// as kind unknown with every attribute, and an HTTP client span is recognised so the tree can hide it.
func TestMapOTelSpanNeverDropsASpan(t *testing.T) {
	span := otelSpan(map[string]any{"db.system": "postgresql", "db.statement": "select 1"})
	span.StatusError, span.StatusMessage = true, "timeout"
	span.Resource = map[string]any{"service.name": "api", "deployment.environment": "prod"}
	part := MapOTelSpan(span)
	assert.Empty(t, part.Kind, "the fold stores an empty kind as unknown")
	assert.Equal(t, "select 1", part.Attributes["db.statement"])
	assert.Equal(t, logstore.TraceSpanStatusError, part.Status)
	assert.Equal(t, "timeout", part.Error)
	assert.Equal(t, "prod", part.Environment)
	assert.Equal(t, span.Resource, part.Attributes["resource"])

	http := MapOTelSpan(otelSpan(map[string]any{"http.request.method": "POST", "url.full": "https://api.openai.com/v1/chat/completions"}))
	assert.Equal(t, logstore.TraceSpanKindHTTP, http.Kind)
}
