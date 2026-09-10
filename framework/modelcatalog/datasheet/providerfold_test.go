package datasheet

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestNormalizeProviderFoldsWandbOntoCoreWeave pins that the datasheet's "wandb"
// rows resolve for coreweave; without the fold every model prices as unknown.
func TestNormalizeProviderFoldsWandbOntoCoreWeave(t *testing.T) {
	t.Parallel()

	if got := normalizeProvider("wandb"); got != string(schemas.CoreWeave) {
		t.Fatalf("normalizeProvider(wandb) = %q, want %q", got, schemas.CoreWeave)
	}
	if got := normalizeProvider(string(schemas.CoreWeave)); got != string(schemas.CoreWeave) {
		t.Fatalf("normalizeProvider(coreweave) = %q, want it left alone", got)
	}
	// Row keys are "<provider>/<model>" and W&B IDs have an org prefix, so
	// only the first segment is stripped.
	if got := extractModelName("wandb/openai/gpt-oss-120b"); got != "openai/gpt-oss-120b" {
		t.Fatalf("extractModelName = %q, want %q", got, "openai/gpt-oss-120b")
	}
}
