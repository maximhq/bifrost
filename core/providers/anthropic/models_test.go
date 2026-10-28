package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToAnthropicListModelsResponseDisplayName pins the label precedence in
// the Anthropic drop-in listing (#8914): DisplayName wins when present, Name
// is the fallback for providers that populate only it, and a row with neither
// carries no label. It also pins that IDs are emitted with the provider
// prefix stripped (ParseModelString), which the provider-harness 293.4 case
// for /anthropic/v1/models relies on when it filters by id.
func TestToAnthropicListModelsResponseDisplayName(t *testing.T) {
	t.Parallel()

	response := &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openrouter/openai/gpt-4o-mini", DisplayName: schemas.Ptr("OpenAI: GPT-4o-mini")},
		{ID: "anthropic/claude-haiku-4-5", Name: schemas.Ptr("Claude Haiku 4.5")},
		{ID: "bare/model", Name: schemas.Ptr("Bare"), DisplayName: schemas.Ptr("Both set")},
		{ID: "unlabeled/model"},
	}}

	got := ToAnthropicListModelsResponse(response).Data
	if len(got) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(got))
	}

	if got[0].ID != "openai/gpt-4o-mini" {
		t.Fatalf("openrouter prefix must be stripped, got id %q", got[0].ID)
	}
	if got[1].ID != "claude-haiku-4-5" {
		t.Fatalf("anthropic prefix must be stripped, got id %q", got[1].ID)
	}

	if got[0].DisplayName != "OpenAI: GPT-4o-mini" {
		t.Fatalf("DisplayName should win, got %q", got[0].DisplayName)
	}
	if got[1].DisplayName != "Claude Haiku 4.5" {
		t.Fatalf("Name should be the fallback, got %q", got[1].DisplayName)
	}
	if got[2].DisplayName != "Both set" {
		t.Fatalf("DisplayName should win when both are set, got %q", got[2].DisplayName)
	}
	if got[3].DisplayName != "" {
		t.Fatalf("a row with no label must stay unlabeled, got %q", got[3].DisplayName)
	}
}
