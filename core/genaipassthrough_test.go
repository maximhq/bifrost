package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestClearGenAIPassthroughForNonGoogleProvider pins which attempts of a GenAI-integration request
// keep the caller's Gemini body: Gemini and Vertex do, any other provider gets it converted, and
// requests from other integrations are left alone.
func TestClearGenAIPassthroughForNonGoogleProvider(t *testing.T) {
	cases := []struct {
		name            string
		integrationType string
		baseProvider    schemas.ModelProvider
		wantCleared     bool
	}{
		{"genai to gemini keeps the raw body", "genai", schemas.Gemini, false},
		{"genai to vertex keeps the raw body", "genai", schemas.Vertex, false},
		{"genai to openai converts", "genai", schemas.OpenAI, true},
		{"genai to anthropic converts", "genai", schemas.Anthropic, true},
		{"genai to bedrock converts", "genai", schemas.Bedrock, true},
		{"anthropic integration is not this helper's", "anthropic", schemas.OpenAI, false},
		{"no integration is left alone", "", schemas.OpenAI, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.integrationType != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integrationType)
			}
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			ctx.SetValue(schemas.BifrostContextKeyRawRequestBodyTextRewriter, schemas.RawRequestBodyTextRewriter(func(body []byte, _ map[string]string) ([]byte, error) { return body, nil }))

			clearGenAIPassthroughForNonGoogleProvider(ctx, tc.baseProvider)

			raw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			_, hasRewriter := ctx.Value(schemas.BifrostContextKeyRawRequestBodyTextRewriter).(schemas.RawRequestBodyTextRewriter)
			if raw == tc.wantCleared || hasRewriter == tc.wantCleared {
				t.Fatalf("raw body on=%v rewriter=%v, want cleared=%v", raw, hasRewriter, tc.wantCleared)
			}
		})
	}
}
