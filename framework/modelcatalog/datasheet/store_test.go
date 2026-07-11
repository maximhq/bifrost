package datasheet

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestDeprecatedDatasheetModelsForProviderUsesRebuiltIndex(t *testing.T) {
	s := NewTestStore(nil)
	s.mu.Lock()
	s.pricingData[makeKey("deprecated-b", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-b",
		Provider:     "openai",
		Mode:         "chat",
		IsDeprecated: true,
	}
	s.pricingData[makeKey("deprecated-a", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-a",
		Provider:     "openai",
		Mode:         "chat",
		IsDeprecated: true,
	}
	s.pricingData[makeKey("deprecated-a", "openai", "responses")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-a",
		Provider:     "openai",
		Mode:         "responses",
		IsDeprecated: true,
	}
	s.pricingData[makeKey("active", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:    "active",
		Provider: "openai",
		Mode:     "chat",
	}
	s.pricingData[makeKey("deprecated-vertex", "vertex_ai", "chat")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-vertex",
		Provider:     "vertex_ai",
		Mode:         "chat",
		IsDeprecated: true,
	}
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	got := s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI)
	want := []string{"deprecated-a", "deprecated-b"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected deprecated OpenAI models %v, got %v", want, got)
	}

	got[0] = "mutated"
	got = s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI)
	if !slices.Equal(got, want) {
		t.Fatalf("expected defensive copy from index %v, got %v", want, got)
	}

	got = s.DeprecatedDatasheetModelsForProvider(schemas.Vertex)
	want = []string{"deprecated-vertex"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected deprecated Vertex models %v, got %v", want, got)
	}
}
