package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic accepts "any" natively, so the OpenAI-only normalization of "any"
// to "required" must not change what Anthropic receives.
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
