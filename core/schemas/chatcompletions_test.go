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

// OpenAI-compatible clients such as the AI SDK's openai-compatible provider put
// cache_control on the message object rather than on a content block. The marker
// must survive decoding as the same breakpoint on the message's last block, or no
// provider ever sees it and the prompt is never cached.
func TestChatMessageUnmarshalMessageLevelCacheControl(t *testing.T) {
	decode := func(t *testing.T, raw string) ChatMessage {
		t.Helper()
		var msg ChatMessage
		if err := Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return msg
	}

	t.Run("string content becomes one marked text block", func(t *testing.T) {
		msg := decode(t, `{"role":"system","content":"You are a coding agent.","cache_control":{"type":"ephemeral"}}`)
		if msg.Content == nil || msg.Content.ContentStr != nil || len(msg.Content.ContentBlocks) != 1 {
			t.Fatalf("expected one content block, got %+v", msg.Content)
		}
		block := msg.Content.ContentBlocks[0]
		if block.Type != ChatContentBlockTypeText || block.Text == nil || *block.Text != "You are a coding agent." {
			t.Fatalf("block should carry the original text, got %+v", block)
		}
		if block.CacheControl == nil || block.CacheControl.Type != CacheControlTypeEphemeral {
			t.Fatalf("block should carry the marker, got %+v", block.CacheControl)
		}
	})

	t.Run("marker lands on the last block and ttl is kept", func(t *testing.T) {
		msg := decode(t, `{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"cache_control":{"type":"ephemeral","ttl":"1h"}}`)
		blocks := msg.Content.ContentBlocks
		if blocks[0].CacheControl != nil {
			t.Fatalf("first block should stay unmarked, got %+v", blocks[0].CacheControl)
		}
		if blocks[1].CacheControl == nil || blocks[1].CacheControl.TTL == nil || *blocks[1].CacheControl.TTL != "1h" {
			t.Fatalf("last block should carry the marker with its ttl, got %+v", blocks[1].CacheControl)
		}
	})

	t.Run("a block marker the client set wins", func(t *testing.T) {
		msg := decode(t, `{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}],"cache_control":{"type":"ephemeral"}}`)
		got := msg.Content.ContentBlocks[0].CacheControl
		if got == nil || got.TTL == nil || *got.TTL != "1h" {
			t.Fatalf("block marker should be kept as sent, got %+v", got)
		}
	})

	t.Run("tool result string content is marked", func(t *testing.T) {
		msg := decode(t, `{"role":"tool","tool_call_id":"call_1","content":"2 notes.txt","cache_control":{"type":"ephemeral"}}`)
		if msg.ChatToolMessage == nil || msg.ChatToolMessage.ToolCallID == nil || *msg.ChatToolMessage.ToolCallID != "call_1" {
			t.Fatalf("tool fields should decode, got %+v", msg.ChatToolMessage)
		}
		if len(msg.Content.ContentBlocks) != 1 || msg.Content.ContentBlocks[0].CacheControl == nil {
			t.Fatalf("tool content should carry the marker, got %+v", msg.Content)
		}
	})

	t.Run("assistant tool call without content takes the marker", func(t *testing.T) {
		msg := decode(t, `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"bash","arguments":"{}"}}],"cache_control":{"type":"ephemeral"}}`)
		calls := msg.ChatAssistantMessage.ToolCalls
		if calls[0].CacheControl != nil || calls[1].CacheControl == nil {
			t.Fatalf("only the last tool call should carry the marker, got %+v / %+v", calls[0].CacheControl, calls[1].CacheControl)
		}
	})

	t.Run("nowhere to put it is not an error", func(t *testing.T) {
		msg := decode(t, `{"role":"assistant","content":null,"cache_control":{"type":"ephemeral"}}`)
		if msg.Content != nil && len(msg.Content.ContentBlocks) > 0 {
			t.Fatalf("no block should be invented, got %+v", msg.Content)
		}
	})

	t.Run("empty string content is not promoted", func(t *testing.T) {
		msg := decode(t, `{"role":"user","content":"","cache_control":{"type":"ephemeral"}}`)
		if msg.Content == nil || msg.Content.ContentStr == nil || *msg.Content.ContentStr != "" || msg.Content.ContentBlocks != nil {
			t.Fatalf("empty content should stay an empty string, got %+v", msg.Content)
		}
	})

	t.Run("messages without a marker decode unchanged", func(t *testing.T) {
		msg := decode(t, `{"role":"user","content":"hi"}`)
		if msg.Content == nil || msg.Content.ContentStr == nil || *msg.Content.ContentStr != "hi" || msg.Content.ContentBlocks != nil {
			t.Fatalf("string content should stay a string, got %+v", msg.Content)
		}
	})

	t.Run("re-encoding carries the marker on the block only", func(t *testing.T) {
		msg := decode(t, `{"role":"user","content":"hi","cache_control":{"type":"ephemeral"}}`)
		out, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}`
		if string(out) != want {
			t.Fatalf("re-encoded message\n got: %s\nwant: %s", out, want)
		}
	})
}
