package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

// ChatParameters' reasoning union accepts clients that mirror the same
// directive in both spellings (flat reasoning_* shorthand plus the reasoning
// object) and canonicalizes to the object form, while still rejecting
// contradictory values. Agents built on ai-sdk are known to emit every vendor
// dialect at once — reasoning_effort and reasoning.effort carrying the same
// value — and the previous "both present" rejection 400'd those requests
// before routing.
func TestChatParametersReasoningUnion(t *testing.T) {
	t.Run("duplicate spellings with equal values are accepted", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_effort":"high","reasoning":{"effort":"high"}}`), &cp)
		if err != nil {
			t.Fatalf("equal duplicate effort should decode, got %v", err)
		}
		if cp.Reasoning == nil || cp.Reasoning.Effort == nil || *cp.Reasoning.Effort != "high" {
			t.Fatalf("effort should canonicalize onto the reasoning object, got %+v", cp.Reasoning)
		}

		// Re-encoding must carry exactly one spelling so the union invariant
		// still holds on the wire.
		out, err := json.Marshal(&cp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), "reasoning_effort") {
			t.Fatalf("marshalled payload should drop the shorthand, got %s", out)
		}
	})

	t.Run("conflicting effort values are rejected", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_effort":"high","reasoning":{"effort":"max"}}`), &cp)
		if err == nil {
			t.Fatal("conflicting effort values should error")
		}
		if !strings.Contains(err.Error(), "conflicts with reasoning.effort") {
			t.Fatalf("error should name the conflict, got %v", err)
		}
	})

	t.Run("shorthand and object max_tokens agree", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_max_tokens":2048,"reasoning":{"max_tokens":2048}}`), &cp)
		if err != nil {
			t.Fatalf("equal duplicate max_tokens should decode, got %v", err)
		}
		if cp.Reasoning == nil || cp.Reasoning.MaxTokens == nil || *cp.Reasoning.MaxTokens != 2048 {
			t.Fatalf("max_tokens should canonicalize onto the reasoning object, got %+v", cp.Reasoning)
		}
	})

	t.Run("conflicting max_tokens values are rejected", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_max_tokens":2048,"reasoning":{"max_tokens":4096}}`), &cp)
		if err == nil {
			t.Fatal("conflicting max_tokens values should error")
		}
	})

	t.Run("duplicate display agrees, conflict rejected", func(t *testing.T) {
		var cp ChatParameters
		if err := Unmarshal([]byte(`{"reasoning_display":"summarized","reasoning":{"display":"summarized"}}`), &cp); err != nil {
			t.Fatalf("equal duplicate display should decode, got %v", err)
		}
		if err := Unmarshal([]byte(`{"reasoning_display":"summarized","reasoning":{"display":"omitted"}}`), &cp); err == nil {
			t.Fatal("conflicting display values should error")
		}
	})

	t.Run("single-spelling requests keep working", func(t *testing.T) {
		var shorthand, object ChatParameters
		if err := Unmarshal([]byte(`{"reasoning_effort":"low"}`), &shorthand); err != nil {
			t.Fatalf("shorthand-only decode: %v", err)
		}
		if err := Unmarshal([]byte(`{"reasoning":{"effort":"low"}}`), &object); err != nil {
			t.Fatalf("object-only decode: %v", err)
		}
		if shorthand.Reasoning == nil || object.Reasoning == nil ||
			*shorthand.Reasoning.Effort != "low" || *object.Reasoning.Effort != "low" {
			t.Fatal("both single-spelling forms should decode to the same canonical state")
		}
	})
}

// Azure OpenAI decorates a chat completion with a top-level prompt_filter_results
// array and a per-choice content_filter_results object. Clients read them to tell
// "filtered" from "empty", so a response must round-trip them untouched.
func TestChatResponsePreservesAzureContentFilterAnnotations(t *testing.T) {
	const upstream = `{
		"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o",
		"prompt_filter_results":[{"prompt_index":0,"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}}}],
		"choices":[{"index":0,"finish_reason":"stop",
			"message":{"role":"assistant","content":"hello"},
			"content_filter_results":{"violence":{"filtered":false,"severity":"safe"}}}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

	var resp BifrostChatResponse
	if err := Unmarshal([]byte(upstream), &resp); err != nil {
		t.Fatalf("decode azure response: %v", err)
	}
	out, err := json.Marshal(&resp)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}

	var got struct {
		PromptFilterResults []struct {
			ContentFilterResults map[string]struct {
				Severity string `json:"severity"`
			} `json:"content_filter_results"`
		} `json:"prompt_filter_results"`
		Choices []struct {
			ContentFilterResults map[string]struct {
				Severity string `json:"severity"`
			} `json:"content_filter_results"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-decode %s: %v", out, err)
	}
	if len(got.PromptFilterResults) != 1 || got.PromptFilterResults[0].ContentFilterResults["hate"].Severity != "safe" {
		t.Fatalf("prompt_filter_results was dropped: %s", out)
	}
	if len(got.Choices) != 1 || got.Choices[0].ContentFilterResults["violence"].Severity != "safe" {
		t.Fatalf("choice content_filter_results was dropped: %s", out)
	}

	t.Run("responses without annotations stay unchanged", func(t *testing.T) {
		var plain BifrostChatResponse
		if err := Unmarshal([]byte(`{"id":"c","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}]}`), &plain); err != nil {
			t.Fatalf("decode: %v", err)
		}
		enc, err := json.Marshal(&plain)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if strings.Contains(string(enc), "filter_results") {
			t.Fatalf("annotation keys leaked into a response that had none: %s", enc)
		}
	})
}

// reasoning.mode ("standard" | "pro") is an OpenAI Responses-only knob that chat
// callers send inside the reasoning object, next to effort.
func TestChatParametersReasoningMode(t *testing.T) {
	var cp ChatParameters
	if err := Unmarshal([]byte(`{"reasoning":{"effort":"high","mode":"pro"}}`), &cp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cp.Reasoning == nil || cp.Reasoning.Mode == nil || *cp.Reasoning.Mode != "pro" {
		t.Fatalf("reasoning.mode should decode, got %+v", cp.Reasoning)
	}
	if cp.Reasoning.Effort == nil || *cp.Reasoning.Effort != "high" {
		t.Fatalf("reasoning.effort should decode alongside mode, got %+v", cp.Reasoning)
	}
}
