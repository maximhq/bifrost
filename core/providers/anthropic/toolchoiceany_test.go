package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConvertResponsesToolChoiceToAnthropic_ForcedChoicesStayAny verifies that
// Anthropic, which accepts "any" natively, still receives type "any" for both
// forced string choices; the OpenAI-only normalization must not affect it.
func TestConvertResponsesToolChoiceToAnthropic_ForcedChoicesStayAny(t *testing.T) {
	for _, value := range []string{"any", "required"} {
		t.Run(value, func(t *testing.T) {
			got := convertResponsesToolChoiceToAnthropic(&schemas.ResponsesToolChoice{
				ResponsesToolChoiceStr: schemas.Ptr(value),
			})
			if got == nil || got.Type != "any" {
				t.Fatalf("tool_choice %q converted to %+v, want type any", value, got)
			}
		})
	}
}
