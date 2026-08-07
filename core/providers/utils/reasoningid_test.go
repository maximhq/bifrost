package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestEmbedAndExtractReasoningItemID_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		payload string
	}{
		{"short id and payload", "rs_abc123", "ciphertext"},
		{"empty payload", "rs_abc123", ""},
		{"long opaque payload", "rs_68f2abcdef", "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVoxMjM0NTY3ODkwYWJjZGVmZ2hpams="},
		{"payload containing the separator", "rs_1", "a:b:c"},
		{"payload containing the marker text", "rs_1", "brid_not_at_offset_zero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.id
			embedded := EmbedReasoningItemID(&id, tc.payload)
			gotID, gotPayload, ok := ExtractReasoningItemID(embedded)
			if !ok {
				t.Fatalf("ExtractReasoningItemID(%q) ok = false, want true", embedded)
			}
			if gotID == nil || *gotID != tc.id {
				t.Errorf("ExtractReasoningItemID(%q) id = %v, want %q", embedded, gotID, tc.id)
			}
			if gotPayload != tc.payload {
				t.Errorf("ExtractReasoningItemID(%q) payload = %q, want %q", embedded, gotPayload, tc.payload)
			}
		})
	}
}

func TestEmbedReasoningItemID_NilOrEmptyIDIsNoOp(t *testing.T) {
	payload := "some-real-signature-or-ciphertext"
	if got := EmbedReasoningItemID(nil, payload); got != payload {
		t.Errorf("EmbedReasoningItemID(nil, %q) = %q, want unchanged", payload, got)
	}
	empty := ""
	if got := EmbedReasoningItemID(&empty, payload); got != payload {
		t.Errorf("EmbedReasoningItemID(&\"\", %q) = %q, want unchanged", payload, got)
	}
}

func TestExtractReasoningItemID_UnmarkedPayloadFallsThrough(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"empty string", ""},
		{"plain anthropic-style base64 signature", "EqoBCkYIARgCKkCVn3G8..."},
		{"contains the separator but no marker", "abc:def"},
		{"contains the marker text not at offset zero", "xbrid_rs_1:payload"},
		{"marker present but embedded id is empty", "brid_:payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotPayload, ok := ExtractReasoningItemID(tc.payload)
			if ok {
				t.Errorf("ExtractReasoningItemID(%q) ok = true, want false", tc.payload)
			}
			if gotID != nil {
				t.Errorf("ExtractReasoningItemID(%q) id = %v, want nil", tc.payload, gotID)
			}
			if gotPayload != tc.payload {
				t.Errorf("ExtractReasoningItemID(%q) payload = %q, want unchanged", tc.payload, gotPayload)
			}
		})
	}
}

// TestShouldEmbedReasoningItemID is the regression test for the reviewer comment that
// gating solely on provider == OpenAI missed Azure, Bedrock Mantle, and Vertex, all of
// which can also serve genuine OpenAI models (Azure AI Foundry, Bedrock Mantle, and
// Vertex Model Garden all host gpt-oss) alongside entirely unrelated model families
// (Llama/Mistral/DeepSeek on Azure, Claude on Bedrock Mantle, Gemini/Claude on Vertex) --
// so those three must additionally check the resolved model, while plain OpenAI (which by
// definition only ever serves OpenAI models) and every other provider stay unconditional.
//
// The baseProvider cases cover custom providers, which report their own key rather than the
// built-in provider they wrap: without resolving through the base type Bifrost records on the
// context, OpenAI models served via an openai-based custom provider fall to the default arm
// and lose the id.
func TestShouldEmbedReasoningItemID(t *testing.T) {
	cases := []struct {
		name         string
		provider     schemas.ModelProvider
		model        string
		baseProvider schemas.ModelProvider // set on the context when non-empty
		want         bool
	}{
		{"OpenAI is always true regardless of model", schemas.OpenAI, "gpt-5", "", true},
		{"OpenAI is true even for an unusual model string", schemas.OpenAI, "some-future-model", "", true},
		{"Azure with an OpenAI model", schemas.Azure, "o3", "", true},
		{"Azure with a non-OpenAI Foundry model", schemas.Azure, "Meta-Llama-3.1-70B-Instruct", "", false},
		{"Azure with Mistral", schemas.Azure, "mistral-large", "", false},
		{"Bedrock Mantle with an OpenAI model", schemas.BedrockMantle, "openai.gpt-oss-120b", "", true},
		{"Bedrock Mantle with a Claude model", schemas.BedrockMantle, "anthropic.claude-opus-4-8", "", false},
		{"Vertex with an OpenAI model", schemas.Vertex, "openai/gpt-oss-120b", "", true},
		{"Vertex with Gemini", schemas.Vertex, "gemini-2.5-pro", "", false},
		{"Vertex with Claude", schemas.Vertex, "claude-sonnet-4-6", "", false},
		{"DeepSeek is never true", schemas.DeepSeek, "deepseek-chat", "", false},
		{"Anthropic is never true", schemas.Anthropic, "claude-sonnet-4-6", "", false},
		{"Bedrock is never true", schemas.Bedrock, "anthropic.claude-opus-4-8", "", false},
		{"custom provider on base OpenAI, deployment-style model", "my-openai", "my-gpt-deployment", schemas.OpenAI, true},
		{"custom provider on base OpenAI, OpenAI model", "my-openai", "gpt-5", schemas.OpenAI, true},
		{"custom provider on base Anthropic", "my-claude", "claude-sonnet-4-6", schemas.Anthropic, false},
		{"custom provider with no base type on the context", "my-openai", "gpt-5", "", false},
		{"base type overrides a standard-looking provider key", schemas.Vertex, "gemini-2.5-pro", schemas.OpenAI, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			if tc.baseProvider != "" {
				ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, tc.baseProvider)
			}
			if got := ShouldEmbedReasoningItemID(ctx, tc.provider, tc.model); got != tc.want {
				t.Errorf("ShouldEmbedReasoningItemID(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

// TestShouldEmbedReasoningItemID_NilContext pins the fallback: converters called
// outside a request (and tests) pass no context, so the provider key must still
// be matched directly rather than panicking or resolving to empty.
func TestShouldEmbedReasoningItemID_NilContext(t *testing.T) {
	if !ShouldEmbedReasoningItemID(nil, schemas.OpenAI, "gpt-5") {
		t.Error("ShouldEmbedReasoningItemID(nil, OpenAI, gpt-5) = false, want true")
	}
	if ShouldEmbedReasoningItemID(nil, schemas.Anthropic, "claude-sonnet-4-6") {
		t.Error("ShouldEmbedReasoningItemID(nil, Anthropic, claude-sonnet-4-6) = true, want false")
	}
}

func TestExtractReasoningItemID_MalformedMarkerFallsThrough(t *testing.T) {
	// Marker present but no separator -- shouldn't happen from EmbedReasoningItemID,
	// but must be handled conservatively rather than guessing at a split point.
	payload := ReasoningItemIDMarker + "rs_no_separator_here"
	gotID, gotPayload, ok := ExtractReasoningItemID(payload)
	if ok {
		t.Errorf("ExtractReasoningItemID(%q) ok = true, want false", payload)
	}
	if gotID != nil {
		t.Errorf("ExtractReasoningItemID(%q) id = %v, want nil", payload, gotID)
	}
	if gotPayload != payload {
		t.Errorf("ExtractReasoningItemID(%q) payload = %q, want unchanged", payload, gotPayload)
	}
}
