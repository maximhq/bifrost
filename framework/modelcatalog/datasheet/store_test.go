package datasheet

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestPricingLookupsNormalizeRuntimeProvider(t *testing.T) {
	const model = "deepseek-ai/DeepSeek-V4-Flash-0731"
	inputCost := 0.00000014
	provider := schemas.ModelProvider("together")
	s := NewTestStore(nil)
	s.pricingData[makeKey(model, "together_ai", "chat")] = configstoreTables.TableModelPricing{
		Model:             model,
		Provider:          "together_ai",
		Mode:              "chat",
		InputCostPerToken: &inputCost,
	}

	row := s.Get(model, provider, schemas.ChatCompletionRequest)
	if row == nil || row.InputCostPerToken == nil || *row.InputCostPerToken != inputCost {
		t.Fatalf("Get() did not resolve the catalog provider: %#v", row)
	}

	pricing := s.GetPricingEntryForModel(model, provider)
	if pricing == nil || pricing.InputCostPerToken == nil || *pricing.InputCostPerToken != inputCost {
		t.Fatalf("GetPricingEntryForModel() did not resolve the catalog provider: %#v", pricing)
	}

	capability := s.GetCapabilityEntry(model, provider)
	if capability == nil || capability.InputCostPerToken == nil || *capability.InputCostPerToken != inputCost {
		t.Fatalf("GetCapabilityEntry() did not resolve the catalog provider: %#v", capability)
	}

	s.mu.Lock()
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()
	if got := s.DatasheetModelsForProvider(provider); !slices.Equal(got, []string{model}) {
		t.Fatalf("DatasheetModelsForProvider() = %v, want [%s]", got, model)
	}
	if got := s.DatasheetProviders(); !slices.Equal(got, []schemas.ModelProvider{provider}) {
		t.Fatalf("DatasheetProviders() = %v, want [%s]", got, provider)
	}
}

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

// GetSupportedParameters used to look up only the exact model string, while
// every sibling lookup in this store resolves through modelParameterCandidates.
// That is how bifrost#8091 happened: the datasheet carries a bare
// "us.anthropic.claude-opus-5-5" row but no bare
// "us.anthropic.claude-sonnet-5-5", so the Bedrock sonnet id resolved to
// nothing, the compat plugin logged "no supported-parameter list" and dropped
// nothing, and Bedrock rejected the request for sending a temperature that
// Sonnet 5.5 no longer accepts. Opus worked only because its exact key exists.
//
// Every sonnet-5-5 row in the sheet omits "temperature", so resolving to any of
// them is what lets the parameter be dropped.
func TestGetSupportedParameters_ResolvesBedrockInferenceProfileID(t *testing.T) {
	s := NewTestStore(nil)
	// The shape the live sheet has: a base row, and no row for the Bedrock
	// region-prefixed spelling the caller actually sends.
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens", "stop_sequences", "stream"},
	})

	got := s.GetSupportedParameters("us.anthropic.claude-sonnet-5-5")

	if got == nil {
		t.Fatal("no supported-parameter list for a Bedrock inference-profile id whose base model has a row")
	}
	if slices.Contains(got, "temperature") {
		t.Errorf("temperature reported as supported; got %v", got)
	}
	if !slices.Contains(got, "max_tokens") {
		t.Errorf("resolved to the wrong row; got %v", got)
	}
}

// Other region and vendor prefixes are the same shape, and must resolve too.
func TestGetSupportedParameters_ResolvesEveryVendorPrefix(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens"},
	})

	for _, model := range []string{
		"us.anthropic.claude-sonnet-5-5",
		"eu.anthropic.claude-sonnet-5-5",
		"apac.anthropic.claude-sonnet-5-5",
		"global.anthropic.claude-sonnet-5-5",
		"anthropic.claude-sonnet-5-5",
	} {
		if got := s.GetSupportedParameters(model); got == nil {
			t.Errorf("%s resolved to nothing", model)
		}
	}
}

// An exact row still wins, so a model that resolves today keeps resolving to
// the same list rather than to its base model's.
func TestGetSupportedParameters_PrefersTheExactRow(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"us.anthropic.claude-opus-5-5": {"max_tokens"},
		"claude-opus-5-5":              {"max_tokens", "temperature", "top_p"},
	})

	got := s.GetSupportedParameters("us.anthropic.claude-opus-5-5")

	if !slices.Equal(got, []string{"max_tokens"}) {
		t.Errorf("exact row not preferred; got %v", got)
	}
}

// A model the sheet says nothing about must still report nothing, so the compat
// plugin keeps passing its parameters through rather than dropping them all.
func TestGetSupportedParameters_UnknownModelStillReportsNothing(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens"},
	})

	if got := s.GetSupportedParameters("some-model-nobody-has-heard-of"); got != nil {
		t.Errorf("unknown model resolved to %v", got)
	}
}

// A digit-dotted name must not be mistaken for a vendor-prefixed one: stripping
// "gpt-3." would resolve gpt-3.5-turbo to an unrelated "5-turbo" row.
//
// Deliberately NO exact row for gpt-3.5-turbo. With one, it wins and the test
// passes whether or not the prefix was wrongly stripped -- it could not fail for
// the reason it describes.
func TestGetSupportedParameters_DoesNotStripADigitDottedName(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"5-turbo": {"stream"},
	})

	if got := s.GetSupportedParameters("gpt-3.5-turbo"); got != nil {
		t.Errorf("gpt-3.5-turbo resolved to %v; the digit-dotted prefix was stripped", got)
	}
}

// The returned slice is a copy, as it was before: a caller mutating it must not
// corrupt the store for the next request.
func TestGetSupportedParameters_ReturnsACopy(t *testing.T) {
	s := NewTestStore(nil)
	s.SetSupportedParamsForTest(map[string][]string{
		"claude-sonnet-5-5": {"max_tokens", "stream"},
	})

	got := s.GetSupportedParameters("us.anthropic.claude-sonnet-5-5")
	if len(got) == 0 {
		t.Fatal("nothing resolved")
	}
	got[0] = "mutated"

	again := s.GetSupportedParameters("us.anthropic.claude-sonnet-5-5")
	if slices.Contains(again, "mutated") {
		t.Error("the store handed out its own slice")
	}
}
