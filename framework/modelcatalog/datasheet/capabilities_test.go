package datasheet

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestGetCapabilityEntry_PrefersChatThenResponsesThenCompletion(t *testing.T) {
	contextLengthChat := 128000
	maxInputTokensChat := 64000
	maxOutputTokensChat := 16000
	modality := "text"

	s := &Store{
		pricingData: map[string]configstoreTables.TableModelPricing{
			makeKey("gpt-4o", "openai", "responses"): {
				Model:           "gpt-4o",
				Provider:        "openai",
				Mode:            "responses",
				ContextLength:   capabilityIntPtr(200000),
				MaxInputTokens:  capabilityIntPtr(100000),
				MaxOutputTokens: capabilityIntPtr(32000),
			},
			makeKey("gpt-4o", "openai", "chat"): {
				Model:           "gpt-4o",
				Provider:        "openai",
				Mode:            "chat",
				ContextLength:   &contextLengthChat,
				MaxInputTokens:  &maxInputTokensChat,
				MaxOutputTokens: &maxOutputTokensChat,
				Architecture: &schemas.Architecture{
					Modality: &modality,
				},
			},
		},
	}

	entry := s.GetCapabilityEntry("gpt-4o", schemas.OpenAI)
	if entry == nil {
		t.Fatal("expected capability entry")
	}
	if entry.Mode != "chat" {
		t.Fatalf("expected chat mode to win, got %q", entry.Mode)
	}
	if entry.ContextLength == nil || *entry.ContextLength != contextLengthChat {
		t.Fatalf("expected context_length=%d, got %#v", contextLengthChat, entry.ContextLength)
	}
	if entry.MaxInputTokens == nil || *entry.MaxInputTokens != maxInputTokensChat {
		t.Fatalf("expected max_input_tokens=%d, got %#v", maxInputTokensChat, entry.MaxInputTokens)
	}
	if entry.MaxOutputTokens == nil || *entry.MaxOutputTokens != maxOutputTokensChat {
		t.Fatalf("expected max_output_tokens=%d, got %#v", maxOutputTokensChat, entry.MaxOutputTokens)
	}
	if entry.Architecture == nil || entry.Architecture.Modality == nil || *entry.Architecture.Modality != modality {
		t.Fatalf("expected architecture modality=%q, got %#v", modality, entry.Architecture)
	}
}

func TestGetCapabilityEntry_FallsBackToAnyModeDeterministically(t *testing.T) {
	s := &Store{
		pricingData: map[string]configstoreTables.TableModelPricing{
			makeKey("imagen", "vertex", "image_generation"): {
				Model:           "imagen",
				Provider:        "vertex",
				Mode:            "image_generation",
				ContextLength:   capabilityIntPtr(4096),
				MaxOutputTokens: capabilityIntPtr(1),
			},
		},
	}

	entry := s.GetCapabilityEntry("imagen", schemas.Vertex)
	if entry == nil {
		t.Fatal("expected capability entry")
	}
	if entry.Mode != "image_generation" {
		t.Fatalf("expected image_generation fallback, got %q", entry.Mode)
	}
}

func TestGetCapabilityEntry_ResolvesAliasFamilyViaBaseModel(t *testing.T) {
	contextLengthChat := 128000

	s := &Store{
		pricingData: map[string]configstoreTables.TableModelPricing{
			makeKey("gpt-4o-2024-08-06", "openai", "responses"): {
				Model:           "gpt-4o-2024-08-06",
				BaseModel:       "gpt-4o",
				Provider:        "openai",
				Mode:            "responses",
				ContextLength:   capabilityIntPtr(64000),
				MaxOutputTokens: capabilityIntPtr(8000),
			},
			makeKey("gpt-4o-2024-08-06", "openai", "chat"): {
				Model:           "gpt-4o-2024-08-06",
				BaseModel:       "gpt-4o",
				Provider:        "openai",
				Mode:            "chat",
				ContextLength:   &contextLengthChat,
				MaxOutputTokens: capabilityIntPtr(16000),
			},
		},
		baseModelIndex: map[string]string{
			"gpt-4o-2024-08-06": "gpt-4o",
		},
	}

	entry := s.GetCapabilityEntry("gpt-4o", schemas.OpenAI)
	if entry == nil {
		t.Fatal("expected capability entry for base-model alias")
	}
	if entry.Mode != "chat" {
		t.Fatalf("expected chat mode to win for alias family, got %q", entry.Mode)
	}
	if entry.ContextLength == nil || *entry.ContextLength != contextLengthChat {
		t.Fatalf("expected alias family context_length=%d, got %#v", contextLengthChat, entry.ContextLength)
	}
}

func TestGetCapabilityEntry_ResolvesProviderPrefixedAlias(t *testing.T) {
	s := &Store{
		pricingData: map[string]configstoreTables.TableModelPricing{
			makeKey("gpt-4o-2024-08-06", "openai", "chat"): {
				Model:           "gpt-4o-2024-08-06",
				BaseModel:       "gpt-4o",
				Provider:        "openai",
				Mode:            "chat",
				ContextLength:   capabilityIntPtr(128000),
				MaxOutputTokens: capabilityIntPtr(16000),
			},
		},
		baseModelIndex: map[string]string{
			"gpt-4o-2024-08-06": "gpt-4o",
		},
	}

	entry := s.GetCapabilityEntry("openai/gpt-4o", schemas.OpenAI)
	if entry == nil {
		t.Fatal("expected capability entry for provider-prefixed alias")
	}
	if entry.Mode != "chat" {
		t.Fatalf("expected chat mode for provider-prefixed alias, got %q", entry.Mode)
	}
}

func TestGetCapabilityEntry_PrefersLiteralMatchOverAliasFamily(t *testing.T) {
	literalContextLength := 32000
	aliasContextLength := 128000

	s := &Store{
		pricingData: map[string]configstoreTables.TableModelPricing{
			makeKey("gpt-4o", "openai", "chat"): {
				Model:           "gpt-4o",
				BaseModel:       "gpt-4o",
				Provider:        "openai",
				Mode:            "chat",
				ContextLength:   &literalContextLength,
				MaxOutputTokens: capabilityIntPtr(4000),
			},
			makeKey("gpt-4o-2024-08-06", "openai", "chat"): {
				Model:           "gpt-4o-2024-08-06",
				BaseModel:       "gpt-4o",
				Provider:        "openai",
				Mode:            "chat",
				ContextLength:   &aliasContextLength,
				MaxOutputTokens: capabilityIntPtr(16000),
			},
		},
		baseModelIndex: map[string]string{
			"gpt-4o":            "gpt-4o",
			"gpt-4o-2024-08-06": "gpt-4o",
		},
	}

	entry := s.GetCapabilityEntry("gpt-4o", schemas.OpenAI)
	if entry == nil {
		t.Fatal("expected literal capability entry")
	}
	if entry.ContextLength == nil || *entry.ContextLength != literalContextLength {
		t.Fatalf("expected literal match to win with context_length=%d, got %#v", literalContextLength, entry.ContextLength)
	}
}

func TestCapabilityFieldsRoundTripThroughPricingConversions(t *testing.T) {
	modality := "text"
	inputCost := float64(1)
	outputCost := float64(2)
	entry := Entry{
		BaseModel: "gpt-4o",
		Provider:  "openai",
		Mode:      "chat",
		Options: Options{
			InputCostPerToken:  &inputCost,
			OutputCostPerToken: &outputCost,
		},
		ContextLength:   capabilityIntPtr(128000),
		MaxInputTokens:  capabilityIntPtr(64000),
		MaxOutputTokens: capabilityIntPtr(16000),
		Architecture: &schemas.Architecture{
			Modality: &modality,
		},
	}

	table := convertEntryToTablePricing("gpt-4o", entry)
	roundTrip := convertTablePricingToEntry(&table)

	if roundTrip.ContextLength == nil || *roundTrip.ContextLength != 128000 {
		t.Fatalf("expected context_length to round-trip, got %#v", roundTrip.ContextLength)
	}
	if roundTrip.MaxInputTokens == nil || *roundTrip.MaxInputTokens != 64000 {
		t.Fatalf("expected max_input_tokens to round-trip, got %#v", roundTrip.MaxInputTokens)
	}
	if roundTrip.MaxOutputTokens == nil || *roundTrip.MaxOutputTokens != 16000 {
		t.Fatalf("expected max_output_tokens to round-trip, got %#v", roundTrip.MaxOutputTokens)
	}
	if roundTrip.Architecture == nil || roundTrip.Architecture.Modality == nil || *roundTrip.Architecture.Modality != modality {
		t.Fatalf("expected architecture to round-trip, got %#v", roundTrip.Architecture)
	}
}

func capabilityIntPtr(v int) *int { return &v }

func capabilityBoolPtr(v bool) *bool { return &v }

// TestExtractSupportedParams_WebSearch guards the two web-search keys: the
// model_parameters "web_search" id and the supports_web_search flag must each
// yield both web_search (responses-path tool) and web_search_options (chat-path
// param), so the compat plugin's drop checks match either way.
func TestExtractSupportedParams_WebSearch(t *testing.T) {
	webSearchParam := []struct {
		ID string `json:"id"`
	}{{ID: "web_search"}}

	cases := []struct {
		name   string
		parsed *modelParametersParseResult
	}{
		{
			name:   "web_search model parameter",
			parsed: &modelParametersParseResult{ModelParameters: webSearchParam},
		},
		{
			name:   "supports_web_search flag",
			parsed: &modelParametersParseResult{SupportsWebSearch: capabilityBoolPtr(true)},
		},
		{
			name: "both set",
			parsed: &modelParametersParseResult{
				ModelParameters:   webSearchParam,
				SupportsWebSearch: capabilityBoolPtr(true),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractSupportedParams(tc.parsed)
			for _, want := range []string{"web_search", "web_search_options"} {
				if !slices.Contains(got, want) {
					t.Errorf("expected supported params to contain %q, got %v", want, got)
				}
			}
		})
	}
}

// TestExtractSupportedParams_WebSearchAbsent confirms neither key is added when
// the datasheet declares no web-search support, so the tool is still stripped
// for models that genuinely lack it.
func TestExtractSupportedParams_WebSearchAbsent(t *testing.T) {
	got := extractSupportedParams(&modelParametersParseResult{SupportsWebSearch: capabilityBoolPtr(false)})
	for _, unexpected := range []string{"web_search", "web_search_options"} {
		if slices.Contains(got, unexpected) {
			t.Errorf("expected supported params to omit %q, got %v", unexpected, got)
		}
	}
}
