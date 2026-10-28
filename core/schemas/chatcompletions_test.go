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

// OpenRouter returns generated images on message.images / delta.images beside a
// string content. Decoding dropped the array: image tokens billed, no image returned.
func TestChatResponsePreservesGeneratedImages(t *testing.T) {
	const imageURL = "data:image/png;base64,iVBORw0KGgo="
	const images = `"images":[{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]`

	type wireMessage struct {
		Content *string `json:"content"`
		Images  []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"images"`
	}
	roundTrip := func(t *testing.T, upstream string) wireMessage {
		t.Helper()
		var resp BifrostChatResponse
		if err := Unmarshal([]byte(upstream), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out, err := json.Marshal(&resp)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var got struct {
			Choices []struct {
				Message wireMessage `json:"message"`
				Delta   wireMessage `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("re-decode %s: %v", out, err)
		}
		if len(got.Choices) != 1 {
			t.Fatalf("expected one choice: %s", out)
		}
		if strings.Contains(upstream, `"delta"`) {
			return got.Choices[0].Delta
		}
		return got.Choices[0].Message
	}
	assertImage := func(t *testing.T, msg wireMessage) {
		t.Helper()
		if len(msg.Images) != 1 || msg.Images[0].Type != "image_url" || msg.Images[0].ImageURL.URL != imageURL {
			t.Fatalf("generated image was dropped, got images %+v", msg.Images)
		}
	}

	t.Run("message with text keeps images and string content", func(t *testing.T) {
		msg := roundTrip(t, `{"id":"gen-1","object":"chat.completion","created":1,"model":"google/gemini-2.5-flash-image",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Here you go",`+images+`}}]}`)
		assertImage(t, msg)
		if msg.Content == nil || *msg.Content != "Here you go" {
			t.Fatalf("content must stay a string, got %v", msg.Content)
		}
	})

	t.Run("image-only message keeps images", func(t *testing.T) {
		assertImage(t, roundTrip(t, `{"id":"gen-1","object":"chat.completion","created":1,"model":"google/gemini-2.5-flash-image",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,`+images+`}}]}`))
	})

	t.Run("stream delta keeps images", func(t *testing.T) {
		assertImage(t, roundTrip(t, `{"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"google/gemini-2.5-flash-image",
			"choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","content":"",`+images+`}}]}`))
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

func TestBifrostLLMUsageNormalizeReasoningTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *int
	}{
		{"sgl top-level", `{"completion_tokens":300,"reasoning_tokens":180}`, Ptr(180)},
		{"openai nested", `{"completion_tokens":300,"completion_tokens_details":{"reasoning_tokens":180}}`, Ptr(180)},
		{"nested wins over top-level", `{"completion_tokens_details":{"reasoning_tokens":180},"reasoning_tokens":7}`, Ptr(180)},
		{"zero top-level leaves details nil", `{"completion_tokens":300,"reasoning_tokens":0}`, nil},
		{"absent", `{"completion_tokens":300}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u BifrostLLMUsage
			if err := Unmarshal([]byte(tc.in), &u); err != nil {
				t.Fatal(err)
			}
			u.NormalizeReasoningTokens()
			if u.ReasoningTokens != 0 {
				t.Fatalf("expected top-level reasoning_tokens cleared, got %d", u.ReasoningTokens)
			}
			if tc.want == nil {
				if u.CompletionTokensDetails != nil {
					t.Fatalf("expected nil completion_tokens_details, got %+v", u.CompletionTokensDetails)
				}
				return
			}
			if u.CompletionTokensDetails == nil || u.CompletionTokensDetails.ReasoningTokens != *tc.want {
				t.Fatalf("expected reasoning_tokens=%d, got %+v", *tc.want, u.CompletionTokensDetails)
			}
		})
	}
	var nilUsage *BifrostLLMUsage
	nilUsage.NormalizeReasoningTokens()
}

func BenchmarkBifrostLLMUsageUnmarshal(b *testing.B) {
	cases := map[string][]byte{
		"openai": []byte(`{"prompt_tokens":1200,"completion_tokens":300,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":1024,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":180,"audio_tokens":0,"accepted_prediction_tokens":0,"rejected_prediction_tokens":0}}`),
		"sgl":    []byte(`{"prompt_tokens":1200,"completion_tokens":300,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":1024},"reasoning_tokens":180}`),
	}
	for name, data := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var u BifrostLLMUsage
				if err := Unmarshal(data, &u); err != nil {
					b.Fatal(err)
				}
				u.NormalizeReasoningTokens()
			}
		})
	}
}
