package databricks

import (
	"testing"

	"github.com/tidwall/gjson"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Budgets the shared effort ladder yields against the default 4096 ceiling.
var (
	spread     = float64(4096 - 1024)
	highBudget = 1024 + int(0.80*spread)
	lowBudget  = 1024 + int(0.15*spread)
)

func TestThinkingParam(t *testing.T) {
	t.Parallel()

	claude := paramSupport{anthropicThinking: true}
	adaptive := paramSupport{anthropicThinking: true, adaptiveOnlyThinking: true}

	tests := []struct {
		name          string
		support       paramSupport
		params        *schemas.ChatParameters
		wantThinking  map[string]any
		wantMaxTokens *int
	}{
		{
			name:    "non-claude endpoint sends nothing",
			support: paramSupport{},
			params:  &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high")}},
		},
		{
			name:    "no reasoning requested sends nothing",
			support: claude,
			params:  &schemas.ChatParameters{},
		},
		{
			name:    "effort none sends nothing",
			support: claude,
			params:  &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("none")}},
		},
		{
			name:    "caller-supplied thinking passes through untouched",
			support: claude,
			params: &schemas.ChatParameters{
				Reasoning:   &schemas.ChatReasoning{Effort: schemas.Ptr("high")},
				ExtraParams: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048}},
			},
		},
		{
			name:          "explicit budget is sent and the ceiling is raised above it",
			support:       claude,
			params:        &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high"), MaxTokens: schemas.Ptr(1500)}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": 1500},
			wantMaxTokens: schemas.Ptr(1500 + 4096),
		},
		{
			name:         "explicit budget below the caller's ceiling leaves the ceiling alone",
			support:      claude,
			params:       &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(8000), Reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(1500)}},
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": 1500},
		},
		{
			name:          "effort alone is scaled against the default ceiling",
			support:       claude,
			params:        &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high")}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": highBudget},
			wantMaxTokens: schemas.Ptr(highBudget + 4096),
		},
		{
			name:         "effort max against the caller's ceiling stays one token below it",
			support:      claude,
			params:       &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(2000), Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("max")}},
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": 1999},
		},
		{
			name:    "enabled false omits thinking despite effort and budget",
			support: claude,
			params:  &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Enabled: schemas.Ptr(false), Effort: schemas.Ptr("high"), MaxTokens: schemas.Ptr(2048)}},
		},
		{
			name:    "enabled false omits thinking on adaptive-only models too",
			support: adaptive,
			params:  &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Enabled: schemas.Ptr(false), Effort: schemas.Ptr("high")}},
		},
		{
			name:          "enabled true alone turns thinking on at the default budget",
			support:       claude,
			params:        &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Enabled: schemas.Ptr(true)}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": 1024},
			wantMaxTokens: schemas.Ptr(1024 + 4096),
		},
		{
			name:          "enabled true overrides effort none",
			support:       claude,
			params:        &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Enabled: schemas.Ptr(true), Effort: schemas.Ptr("none")}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": 1024},
			wantMaxTokens: schemas.Ptr(1024 + 4096),
		},
		{
			name:          "enabled true keeps an explicit budget",
			support:       claude,
			params:        &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Enabled: schemas.Ptr(true), MaxTokens: schemas.Ptr(2048)}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": 2048},
			wantMaxTokens: schemas.Ptr(2048 + 4096),
		},
		{
			name:         "enabled true on an adaptive-only model sends adaptive",
			support:      adaptive,
			params:       &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Enabled: schemas.Ptr(true)}},
			wantThinking: map[string]any{"type": "adaptive"},
		},
		{
			name:         "adaptive-only model forwards display omitted to suppress returned reasoning",
			support:      adaptive,
			params:       &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high"), Display: schemas.Ptr("omitted")}},
			wantThinking: map[string]any{"type": "adaptive", "display": "omitted"},
		},
		{
			name:         "budget model ignores display",
			support:      claude,
			params:       &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(8000), Reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(1500), Display: schemas.Ptr("omitted")}},
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": 1500},
		},
		{
			name:    "effort against a ceiling too small for the minimum budget sends nothing",
			support: claude,
			params:  &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024), Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high")}},
		},
		{
			name:          "dynamic budget becomes the minimum",
			support:       claude,
			params:        &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(-1)}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": 1024},
			wantMaxTokens: schemas.Ptr(1024 + 4096),
		},
		{
			name:          "extra-param reasoning_effort spelling is honoured",
			support:       claude,
			params:        &schemas.ChatParameters{ExtraParams: map[string]any{"reasoning_effort": "low"}},
			wantThinking:  map[string]any{"type": "enabled", "budget_tokens": lowBudget},
			wantMaxTokens: schemas.Ptr(lowBudget + 4096),
		},
		{
			name:         "adaptive-only model gets adaptive thinking with no budget",
			support:      adaptive,
			params:       &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high"), MaxTokens: schemas.Ptr(1500)}},
			wantThinking: map[string]any{"type": "adaptive"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotThinking, gotMaxTokens := thinkingParam(tt.support, tt.params)
			if len(gotThinking) != len(tt.wantThinking) {
				t.Fatalf("thinking: got %#v, want %#v", gotThinking, tt.wantThinking)
			}
			for k, want := range tt.wantThinking {
				if gotThinking[k] != want {
					t.Errorf("thinking[%s]: got %#v, want %#v", k, gotThinking[k], want)
				}
			}
			switch {
			case tt.wantMaxTokens == nil && gotMaxTokens != nil:
				t.Errorf("max tokens: got %d, want unchanged", *gotMaxTokens)
			case tt.wantMaxTokens != nil && gotMaxTokens == nil:
				t.Errorf("max tokens: got unchanged, want %d", *tt.wantMaxTokens)
			case tt.wantMaxTokens != nil && *gotMaxTokens != *tt.wantMaxTokens:
				t.Errorf("max tokens: got %d, want %d", *gotMaxTokens, *tt.wantMaxTokens)
			}
		})
	}
}

// TestNormalizeReasoningBlocks pins the rewrite from Databricks' reasoning content blocks to
// the fields Bifrost's OpenAI-shaped parser reads, for both the unary message and the
// streaming delta as observed on a live workspace.
func TestNormalizeReasoningBlocks(t *testing.T) {
	t.Parallel()

	t.Run("message with reasoning and text collapses to string content", func(t *testing.T) {
		t.Parallel()
		body := `{"choices":[{"message":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"17 * 23 = 391","signature":"sig-1"}]},{"type":"text","text":"391"}]},"index":0,"finish_reason":"stop"}]}`
		got := normalizeReasoningBlocks([]byte(body))

		if v := gjson.GetBytes(got, "choices.0.message.content"); v.Type != gjson.String || v.String() != "391" {
			t.Errorf("content: got %s, want the text block as a string", v.Raw)
		}
		if v := gjson.GetBytes(got, "choices.0.message.reasoning").String(); v != "17 * 23 = 391" {
			t.Errorf("reasoning: got %q", v)
		}
		if v := gjson.GetBytes(got, "choices.0.message.reasoning_details.0.signature").String(); v != "sig-1" {
			t.Errorf("signature: got %q", v)
		}
		if v := gjson.GetBytes(got, "choices.0.message.reasoning_details.0.type").String(); v != string(schemas.BifrostReasoningDetailsTypeText) {
			t.Errorf("details type: got %q", v)
		}
		if gjson.GetBytes(got, "choices.0.finish_reason").String() != "stop" {
			t.Error("unrelated fields were disturbed")
		}
	})

	t.Run("delta with only reasoning drops content", func(t *testing.T) {
		t.Parallel()
		body := `{"choices":[{"delta":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"17","signature":""}]}]},"index":0,"finish_reason":null}]}`
		got := normalizeReasoningBlocks([]byte(body))

		if gjson.GetBytes(got, "choices.0.delta.content").Exists() {
			t.Errorf("content survived: %s", gjson.GetBytes(got, "choices.0.delta.content").Raw)
		}
		if v := gjson.GetBytes(got, "choices.0.delta.reasoning").String(); v != "17" {
			t.Errorf("reasoning: got %q", v)
		}
		if gjson.GetBytes(got, "choices.0.delta.reasoning_details.0.signature").Exists() {
			t.Error("an empty signature must be omitted")
		}
	})

	t.Run("signature-only delta still carries the signature", func(t *testing.T) {
		t.Parallel()
		body := `{"choices":[{"delta":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"","signature":"sig-final"}]}]},"index":0}]}`
		got := normalizeReasoningBlocks([]byte(body))
		if v := gjson.GetBytes(got, "choices.0.delta.reasoning_details.0.signature").String(); v != "sig-final" {
			t.Errorf("signature: got %q", v)
		}
	})

	t.Run("string content is untouched", func(t *testing.T) {
		t.Parallel()
		body := `{"choices":[{"delta":{"role":"assistant","content":"Hi"},"index":0,"finish_reason":null}]}`
		if got := normalizeReasoningBlocks([]byte(body)); string(got) != body {
			t.Errorf("body was rewritten: %s", got)
		}
	})

	t.Run("non-text blocks are kept as an array", func(t *testing.T) {
		t.Parallel()
		body := `{"choices":[{"message":{"role":"assistant","content":[{"type":"reasoning","summary":[{"type":"summary_text","text":"r"}]},{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]},"index":0}]}`
		got := normalizeReasoningBlocks([]byte(body))
		content := gjson.GetBytes(got, "choices.0.message.content")
		if !content.IsArray() || len(content.Array()) != 2 {
			t.Fatalf("content: got %s, want the two non-reasoning blocks", content.Raw)
		}
		if content.Array()[1].Get("type").String() != "image_url" {
			t.Errorf("block order changed: %s", content.Raw)
		}
	})

	t.Run("no choices is a no-op", func(t *testing.T) {
		t.Parallel()
		body := `{"error":{"message":"nope"}}`
		if got := normalizeReasoningBlocks([]byte(body)); string(got) != body {
			t.Errorf("body was rewritten: %s", got)
		}
	})
}
