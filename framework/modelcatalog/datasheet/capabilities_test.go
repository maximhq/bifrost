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
		BaseModel:    "gpt-4o",
		Provider:     "openai",
		Mode:         "chat",
		IsDeprecated: true,
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
	if !roundTrip.IsDeprecated {
		t.Fatalf("expected is_deprecated to round-trip")
	}
}

func capabilityIntPtr(v int) *int { return &v }

func capabilityBoolPtr(v bool) *bool { return &v }

// TestExtractSupportedParams_StopSpellings guards both datasheet spellings of the
// stop parameter. Anthropic rows use stop_sequences and Bedrock's Nova/Titan rows use
// the Converse camelCase stopSequences; compat's dropUnsupportedParams gates only on
// the neutral "stop", so a spelling that fails to map makes the model silently lose
// its stop sequences and run to end_turn.
func TestExtractSupportedParams_StopSpellings(t *testing.T) {
	for _, id := range []string{"stop", "stop_sequences", "stopSequences"} {
		t.Run(id, func(t *testing.T) {
			parsed := &schemas.ModelCapabilities{
				ModelParameters: []schemas.ModelParameterDescriptor{{ID: id}},
			}
			if got := extractSupportedParams(parsed); !slices.Contains(got, "stop") {
				t.Errorf("model_parameters id %q must yield supported param \"stop\", got %v", id, got)
			}
		})
	}
}

// TestExtractSupportedParams_WebSearch guards the two web-search keys: the
// model_parameters "web_search" id and the supports_web_search flag must each
// yield both web_search (responses-path tool) and web_search_options (chat-path
// param), so the compat plugin's drop checks match either way.
func TestExtractSupportedParams_WebSearch(t *testing.T) {
	webSearchParam := []schemas.ModelParameterDescriptor{{ID: "web_search"}}

	cases := []struct {
		name   string
		parsed *schemas.ModelCapabilities
	}{
		{
			name:   "web_search model parameter",
			parsed: &schemas.ModelCapabilities{ModelParameters: webSearchParam},
		},
		{
			name:   "supports_web_search flag",
			parsed: &schemas.ModelCapabilities{SupportsWebSearch: capabilityBoolPtr(true)},
		},
		{
			name: "both set",
			parsed: &schemas.ModelCapabilities{
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
	got := extractSupportedParams(&schemas.ModelCapabilities{SupportsWebSearch: capabilityBoolPtr(false)})
	for _, unexpected := range []string{"web_search", "web_search_options"} {
		if slices.Contains(got, unexpected) {
			t.Errorf("expected supported params to omit %q, got %v", unexpected, got)
		}
	}
}

// TestExtractSupportedParams_NoneReasoningEffort guards the
// supports_none_reasoning_effort flag: compat's dropUnsupportedParams uses it
// to decide whether to force reasoning.effort to "none" (vs. dropping
// reasoning entirely) for models that reason by default even when
// reasoning_effort is omitted.
func TestExtractSupportedParams_NoneReasoningEffort(t *testing.T) {
	if got := extractSupportedParams(&schemas.ModelCapabilities{SupportsNoneReasoningEffort: capabilityBoolPtr(true)}); !slices.Contains(got, "supports_none_reasoning_effort") {
		t.Errorf("expected supported params to contain \"supports_none_reasoning_effort\", got %v", got)
	}
	if got := extractSupportedParams(&schemas.ModelCapabilities{SupportsNoneReasoningEffort: capabilityBoolPtr(false)}); slices.Contains(got, "supports_none_reasoning_effort") {
		t.Errorf("expected supported params to omit \"supports_none_reasoning_effort\", got %v", got)
	}
	if got := extractSupportedParams(&schemas.ModelCapabilities{}); slices.Contains(got, "supports_none_reasoning_effort") {
		t.Errorf("expected supported params to omit \"supports_none_reasoning_effort\" when unset, got %v", got)
	}
}

// TestExtractSupportedParams_SparseRowDefaultsCoreParams guards against a
// datasheet row that sets a few supports_* flags (e.g. from a pricing-only
// sync) but carries no model_parameters array at all — the only place
// temperature/top_p/stop/max_tokens/etc. are normally sourced from. Without
// this default, such a row would make compat's dropUnsupportedParams strip
// those core params from every request to that model.
func TestExtractSupportedParams_SparseRowDefaultsCoreParams(t *testing.T) {
	sparse := &schemas.ModelCapabilities{
		SupportsFunctionCalling: capabilityBoolPtr(true),
		SupportsToolChoice:      capabilityBoolPtr(true),
	}
	got := extractSupportedParams(sparse)
	for _, want := range []string{"temperature", "top_p", "stop", "max_tokens", "frequency_penalty", "presence_penalty", "seed", "logprobs", "top_logprobs", "n", "logit_bias", "metadata"} {
		if !slices.Contains(got, want) {
			t.Errorf("expected sparse row (no model_parameters) to default-include %q, got %v", want, got)
		}
	}

	// A row with an explicit model_parameters array is well-formed — its
	// omissions are a real restriction (e.g. reasoning-only models), so the
	// default must not kick in.
	explicit := &schemas.ModelCapabilities{
		ModelParameters: []schemas.ModelParameterDescriptor{{ID: "tools"}},
	}
	got = extractSupportedParams(explicit)
	if slices.Contains(got, "temperature") {
		t.Errorf("expected explicit model_parameters row to omit \"temperature\" when not listed, got %v", got)
	}

	// An explicit supports_sampling_params=false must suppress the default
	// even on a sparse row (adaptive-only models that reject sampling params).
	rejectsSampling := &schemas.ModelCapabilities{
		SupportsSamplingParams: capabilityBoolPtr(false),
	}
	got = extractSupportedParams(rejectsSampling)
	for _, unexpected := range []string{"temperature", "top_p"} {
		if slices.Contains(got, unexpected) {
			t.Errorf("expected supports_sampling_params=false to omit %q, got %v", unexpected, got)
		}
	}
	if !slices.Contains(got, "stop") {
		t.Errorf("expected supports_sampling_params=false to still default-include \"stop\", got %v", got)
	}
}
