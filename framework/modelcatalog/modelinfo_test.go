package modelcatalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

func modelInfoCatalog(t *testing.T) *ModelCatalog {
	t.Helper()
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"claude-opus-5": {
			"provider": "anthropic",
			"mode": "chat",
			"base_model": "claude-opus-5",
			"max_input_tokens": 200000,
			"max_output_tokens": 64000,
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000025,
			"cache_read_input_token_cost": 0.0000005,
			"cache_creation_input_token_cost": 0.00000625
		},
		"retired-model": {
			"provider": "anthropic",
			"mode": "chat",
			"base_model": "retired-model",
			"is_deprecated": true
		}
	}`)
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing testdata: %v", err)
	}

	// Supported parameters live in a separate index fed by its own feed, so
	// seeding pricing alone leaves SupportedParameters empty and any assertion
	// about it passes for the wrong reason.
	paramsPath := filepath.Join(filepath.Dir(pricingPath), "params.json")
	paramsJSON := []byte(`{
		"claude-opus-5": {
			"mode": "chat",
			"model_parameters": [{"id": "temperature"}, {"id": "top_p"}]
		}
	}`)
	if err := os.WriteFile(paramsPath, paramsJSON, 0o600); err != nil {
		t.Fatalf("write model-parameters testdata: %v", err)
	}

	ds := datasheet.New(nil, nil, datasheet.Config{
		URL:                "file://" + pricingPath,
		ModelParametersURL: "file://" + paramsPath,
	})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing testdata: %v", err)
	}
	if err := ds.LoadModelParamsFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load model-parameters testdata: %v", err)
	}
	return &ModelCatalog{
		datasheet: ds,
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
}

func TestGetModelInfoPopulatesPricingAndLimits(t *testing.T) {
	mc := modelInfoCatalog(t)

	info := mc.GetModelInfo(schemas.Anthropic, "claude-opus-5")
	if info == nil {
		t.Fatal("GetModelInfo = nil, want populated model")
	}
	if info.ID != "claude-opus-5" {
		t.Errorf("ID = %q, want claude-opus-5", info.ID)
	}
	// context_length is absent from the row, so it falls back to max_input_tokens.
	if info.ContextLength == nil || *info.ContextLength != 200000 {
		t.Errorf("ContextLength = %v, want 200000", info.ContextLength)
	}
	if info.MaxInputTokens == nil || *info.MaxInputTokens != 200000 {
		t.Errorf("MaxInputTokens = %v, want 200000", info.MaxInputTokens)
	}
	if info.MaxOutputTokens == nil || *info.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %v, want 64000", info.MaxOutputTokens)
	}
	if info.Pricing == nil {
		t.Fatal("Pricing = nil, want populated")
	}
	// Fixed-decimal, never scientific notation: clients parse these as decimals.
	if info.Pricing.Prompt == nil || *info.Pricing.Prompt != "0.0000050000" {
		t.Errorf("Pricing.Prompt = %v, want 0.0000050000", info.Pricing.Prompt)
	}
	if info.Pricing.Completion == nil || *info.Pricing.Completion != "0.0000250000" {
		t.Errorf("Pricing.Completion = %v, want 0.0000250000", info.Pricing.Completion)
	}
	if info.Pricing.InputCacheRead == nil || *info.Pricing.InputCacheRead != "0.0000005000" {
		t.Errorf("Pricing.InputCacheRead = %v, want 0.0000005000", info.Pricing.InputCacheRead)
	}
	if info.Pricing.InputCacheWrite == nil || *info.Pricing.InputCacheWrite != "0.0000062500" {
		t.Errorf("Pricing.InputCacheWrite = %v, want 0.0000062500", info.Pricing.InputCacheWrite)
	}
}

func TestGetModelInfoReportsDeprecation(t *testing.T) {
	mc := modelInfoCatalog(t)

	info := mc.GetModelInfo(schemas.Anthropic, "retired-model")
	if info == nil {
		t.Fatal("GetModelInfo = nil, want populated model")
	}
	if !info.IsDeprecated {
		t.Error("IsDeprecated = false, want true")
	}
}

func TestGetModelInfoUnknownModel(t *testing.T) {
	mc := modelInfoCatalog(t)

	if got := mc.GetModelInfo(schemas.Anthropic, "not-a-real-model"); got != nil {
		t.Errorf("GetModelInfo for unknown model = %v, want nil", got)
	}
	if got := mc.GetModelInfo(schemas.Anthropic, ""); got != nil {
		t.Errorf("GetModelInfo for empty model = %v, want nil", got)
	}
}

// The catalog backfills what a provider's list-models response omitted; it must
// never overwrite what the provider actually reported.
func TestApplyModelInfoDoesNotOverwriteProviderValues(t *testing.T) {
	mc := modelInfoCatalog(t)
	entry := mc.datasheet.GetPricingEntryForModel("claude-opus-5", schemas.Anthropic)
	if entry == nil {
		t.Fatal("seeded pricing entry not found")
	}

	providerReported := 999
	model := &schemas.Model{
		ID:             "claude-opus-5",
		ContextLength:  &providerReported,
		MaxInputTokens: &providerReported,
		Pricing:        &schemas.Pricing{},
	}
	ApplyModelInfo(model, entry)

	if *model.ContextLength != providerReported {
		t.Errorf("ContextLength = %d, want provider-reported %d", *model.ContextLength, providerReported)
	}
	if *model.MaxInputTokens != providerReported {
		t.Errorf("MaxInputTokens = %d, want provider-reported %d", *model.MaxInputTokens, providerReported)
	}
	if model.Pricing.Prompt != nil {
		t.Errorf("Pricing.Prompt = %v, want untouched nil on a caller-set Pricing", model.Pricing.Prompt)
	}
	// Fields the provider left empty still get filled.
	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %v, want backfilled 64000", model.MaxOutputTokens)
	}
}

// GetModelInfo hands its result to third-party plugin code via ctx.GetModelInfo,
// and its doc promises the caller owns it. Entry fields alias the shared
// datasheet row, so every reference-typed field must be cloned on the way out:
// a write through an escaped handle would rewrite the catalog for all later
// requests, and racing the pricing sync on the attributes map is a fatal
// concurrent map access rather than a recoverable panic.
func TestGetModelInfoReturnsCallerOwnedCopy(t *testing.T) {
	mc := modelInfoCatalog(t)

	first := mc.GetModelInfo(schemas.Anthropic, "claude-opus-5")
	if first == nil {
		t.Fatal("GetModelInfo = nil, want populated model")
	}

	*first.ContextLength = 1
	*first.MaxInputTokens = 2
	*first.MaxOutputTokens = 3
	first.AdditionalAttributes = map[string]string{"tier": "mutated"}
	if len(first.SupportedParameters) == 0 {
		t.Fatal("SupportedParameters is empty, so mutating it would prove nothing")
	}
	first.SupportedParameters[0] = "mutated"

	second := mc.GetModelInfo(schemas.Anthropic, "claude-opus-5")
	if second == nil {
		t.Fatal("second GetModelInfo = nil, want populated model")
	}
	if *second.ContextLength != 200000 {
		t.Errorf("ContextLength = %d after caller mutation, want untouched 200000", *second.ContextLength)
	}
	if *second.MaxInputTokens != 200000 {
		t.Errorf("MaxInputTokens = %d after caller mutation, want untouched 200000", *second.MaxInputTokens)
	}
	if *second.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %d after caller mutation, want untouched 64000", *second.MaxOutputTokens)
	}
	if second.AdditionalAttributes["tier"] == "mutated" {
		t.Error("AdditionalAttributes leaked a caller mutation into the catalog")
	}
	// SupportedParameters comes straight off the datasheet lookup rather than
	// through ApplyModelInfo's clones, so it needs its own check that the
	// lookup hands back a fresh slice.
	if slices.Contains(second.SupportedParameters, "mutated") {
		t.Errorf("SupportedParameters leaked a caller mutation into the catalog: %v", second.SupportedParameters)
	}

	// The pointers themselves must differ, else a later caller mutating through
	// one handle would still be seen by the other.
	if first.ContextLength == second.ContextLength {
		t.Error("ContextLength pointer is shared between calls, want a fresh allocation per call")
	}
	if first.MaxOutputTokens == second.MaxOutputTokens {
		t.Error("MaxOutputTokens pointer is shared between calls, want a fresh allocation per call")
	}
}

// The rows reached through ApplyModelInfo carry an Architecture and an
// attributes map, which GetModelInfo's seeded rows do not - cover those clones
// directly against the shared entry.
func TestApplyModelInfoClonesMutableEntryFields(t *testing.T) {
	entry := &PricingEntry{
		AdditionalAttributes: map[string]string{"tier": "original"},
		Architecture: &schemas.Architecture{
			// Architecture's scalar pointers are as shareable as its slices: a
			// struct copy carries them through untouched, and the entry's
			// Architecture is the pointer the datasheet itself stores.
			Modality:         new("text->text"),
			Tokenizer:        new("cl100k"),
			InstructType:     new("none"),
			InputModalities:  []string{"text"},
			OutputModalities: []string{"text"},
		},
	}

	model := &schemas.Model{ID: "claude-opus-5"}
	ApplyModelInfo(model, entry)

	if model.Architecture == entry.Architecture {
		t.Fatal("Architecture pointer is shared with the entry, want a clone")
	}
	if model.Architecture.Modality == entry.Architecture.Modality {
		t.Error("Architecture.Modality pointer is shared with the entry, want a clone")
	}
	if model.Architecture.Tokenizer == entry.Architecture.Tokenizer {
		t.Error("Architecture.Tokenizer pointer is shared with the entry, want a clone")
	}
	if model.Architecture.InstructType == entry.Architecture.InstructType {
		t.Error("Architecture.InstructType pointer is shared with the entry, want a clone")
	}

	model.AdditionalAttributes["tier"] = "mutated"
	model.Architecture.InputModalities[0] = "mutated"
	model.Architecture.OutputModalities[0] = "mutated"
	*model.Architecture.Modality = "mutated"
	*model.Architecture.Tokenizer = "mutated"
	*model.Architecture.InstructType = "mutated"

	if entry.AdditionalAttributes["tier"] != "original" {
		t.Errorf("entry AdditionalAttributes[tier] = %q, want untouched original", entry.AdditionalAttributes["tier"])
	}
	if entry.Architecture.InputModalities[0] != "text" {
		t.Errorf("entry InputModalities[0] = %q, want untouched text", entry.Architecture.InputModalities[0])
	}
	if entry.Architecture.OutputModalities[0] != "text" {
		t.Errorf("entry OutputModalities[0] = %q, want untouched text", entry.Architecture.OutputModalities[0])
	}
	if *entry.Architecture.Modality != "text->text" {
		t.Errorf("entry Modality = %q, want untouched text->text", *entry.Architecture.Modality)
	}
	if *entry.Architecture.Tokenizer != "cl100k" {
		t.Errorf("entry Tokenizer = %q, want untouched cl100k", *entry.Architecture.Tokenizer)
	}
	if *entry.Architecture.InstructType != "none" {
		t.Errorf("entry InstructType = %q, want untouched none", *entry.Architecture.InstructType)
	}
}

func TestApplyModelInfoNilSafe(t *testing.T) {
	ApplyModelInfo(nil, nil)
	ApplyModelInfo(&schemas.Model{}, nil)

	var nilCatalog *ModelCatalog
	if got := nilCatalog.GetModelInfo(schemas.Anthropic, "claude-opus-5"); got != nil {
		t.Errorf("GetModelInfo on nil catalog = %v, want nil", got)
	}
	if got := nilCatalog.CalculateRequestCost(nil, nil); got != 0 {
		t.Errorf("CalculateRequestCost on nil catalog = %v, want 0", got)
	}
}

// seedLive caches one provider reply under keyID.
func seedLive(mc *ModelCatalog, provider schemas.ModelProvider, keyID string, models ...schemas.Model) {
	mc.UpsertLiveFromResponse(provider, keyID, false, &schemas.BifrostListModelsResponse{Data: models})
}

// The gap this closes: a provider serving a model the datasheet has no row for
// used to be reported by /v1/models and unknown to plugins in the same request.
func TestGetModelInfoAnswersFromLiveWhenDatasheetHasNoRow(t *testing.T) {
	mc := modelInfoCatalog(t)
	seedLive(mc, schemas.Anthropic, "key-1", schemas.Model{
		ID:            "brand-new-model",
		ContextLength: new(512000),
		OwnedBy:       new("anthropic"),
	})

	info := mc.GetModelInfo(schemas.Anthropic, "brand-new-model")
	if info == nil {
		t.Fatal("GetModelInfo = nil, want the live entry")
	}
	if info.ContextLength == nil || *info.ContextLength != 512000 {
		t.Errorf("ContextLength = %v, want 512000", info.ContextLength)
	}
}

// Live is the more current account of what a provider serves, so its values
// stand and the datasheet only fills the gaps.
func TestGetModelInfoLiveWinsDatasheetBackfills(t *testing.T) {
	mc := modelInfoCatalog(t)
	seedLive(mc, schemas.Anthropic, "key-1", schemas.Model{
		ID:            "claude-opus-5",
		ContextLength: new(999000),
	})

	info := mc.GetModelInfo(schemas.Anthropic, "claude-opus-5")
	if info == nil {
		t.Fatal("GetModelInfo = nil, want populated model")
	}
	if info.ContextLength == nil || *info.ContextLength != 999000 {
		t.Errorf("ContextLength = %v, want the live 999000 to win", info.ContextLength)
	}
	if info.Pricing == nil || info.Pricing.Prompt == nil {
		t.Fatal("Pricing.Prompt = nil, want the datasheet to backfill what live omitted")
	}
	if info.MaxOutputTokens == nil || *info.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %v, want the datasheet's 64000", info.MaxOutputTokens)
	}
}

// A live entry carrying only an identifier is not knowledge. Same rule
// /v1/models applies before listing a model.
func TestGetModelInfoBareLiveEntryStaysUnknown(t *testing.T) {
	mc := modelInfoCatalog(t)
	seedLive(mc, schemas.Anthropic, "key-1", schemas.Model{ID: "nameless"})

	if got := mc.GetModelInfo(schemas.Anthropic, "nameless"); got != nil {
		t.Errorf("GetModelInfo = %+v, want nil for an ID-only entry", got)
	}
	if got := mc.GetModelInfo(schemas.Anthropic, "never-heard-of-it"); got != nil {
		t.Errorf("GetModelInfo = %+v, want nil when both halves miss", got)
	}
}

// The question is "what is this model", not "may this key reach it", so a model
// cached under any key answers.
func TestGetModelInfoIgnoresKeyScope(t *testing.T) {
	mc := modelInfoCatalog(t)
	seedLive(mc, schemas.Anthropic, "some-other-key", schemas.Model{
		ID:      "scoped-model",
		OwnedBy: new("anthropic"),
	})

	if got := mc.GetModelInfo(schemas.Anthropic, "scoped-model"); got == nil {
		t.Error("GetModelInfo = nil, want the entry regardless of which key cached it")
	}
}

// The cached identifier may be provider-prefixed; callers ask in either form.
func TestGetModelInfoMatchesProviderPrefixedIDs(t *testing.T) {
	mc := modelInfoCatalog(t)
	seedLive(mc, schemas.Anthropic, "key-1", schemas.Model{
		ID:      "anthropic/prefixed-model",
		OwnedBy: new("anthropic"),
	})

	for _, ask := range []string{"prefixed-model", "anthropic/prefixed-model"} {
		info := mc.GetModelInfo(schemas.Anthropic, ask)
		if info == nil {
			t.Fatalf("GetModelInfo(%q) = nil, want the cached entry", ask)
		}
		if info.ID != ask {
			t.Errorf("ID = %q, want the caller's spelling %q", info.ID, ask)
		}
	}
}

// Plugins are third-party code. A result that still aliased the live cache
// would let one rewrite the catalog for every later request, and reading it
// while the refresher swaps the entry is a fatal concurrent map access.
func TestGetModelInfoResultIsCallerOwned(t *testing.T) {
	mc := modelInfoCatalog(t)
	seedLive(mc, schemas.Anthropic, "key-1", schemas.Model{
		ID:                   "owned-model",
		ContextLength:        new(1000),
		SupportedMethods:     []string{"chat"},
		AdditionalAttributes: map[string]string{"tier": "original"},
		Architecture:         &schemas.Architecture{InputModalities: []string{"text"}},
	})

	first := mc.GetModelInfo(schemas.Anthropic, "owned-model")
	if first == nil {
		t.Fatal("GetModelInfo = nil, want populated model")
	}
	first.AdditionalAttributes["tier"] = "mutated"
	first.SupportedMethods[0] = "mutated"
	first.Architecture.InputModalities[0] = "mutated"
	*first.ContextLength = 1

	second := mc.GetModelInfo(schemas.Anthropic, "owned-model")
	if second.AdditionalAttributes["tier"] != "original" {
		t.Errorf("AdditionalAttributes[tier] = %q, want untouched original", second.AdditionalAttributes["tier"])
	}
	if second.SupportedMethods[0] != "chat" {
		t.Errorf("SupportedMethods[0] = %q, want untouched chat", second.SupportedMethods[0])
	}
	if second.Architecture.InputModalities[0] != "text" {
		t.Errorf("InputModalities[0] = %q, want untouched text", second.Architecture.InputModalities[0])
	}
	if second.ContextLength == nil || *second.ContextLength != 1000 {
		t.Errorf("ContextLength = %v, want untouched 1000", second.ContextLength)
	}
}
