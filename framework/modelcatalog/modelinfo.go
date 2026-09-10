package modelcatalog

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

var modelInfoEndpointByMode = map[string]string{
	"completion":          "/v1/completions",
	"chat":                "/v1/chat/completions",
	"responses":           "/v1/responses",
	"embedding":           "/v1/embeddings",
	"rerank":              "/v1/rerank",
	"ocr":                 "/v1/ocr",
	"audio_speech":        "/v1/audio/speech",
	"audio_transcription": "/v1/audio/transcriptions",
	"image_generation":    "/v1/images/generations",
	"image_edit":          "/v1/images/edits",
	"image_variation":     "/v1/images/variations",
	"video_generation":    "/v1/videos",
}

var modelInfoMethodByEndpoint = map[string]string{
	"/v1/completions":          string(schemas.TextCompletionRequest),
	"/v1/chat/completions":     string(schemas.ChatCompletionRequest),
	"/v1/responses":            string(schemas.ResponsesRequest),
	"/v1/embeddings":           string(schemas.EmbeddingRequest),
	"/v1/rerank":               string(schemas.RerankRequest),
	"/v1/ocr":                  string(schemas.OCRRequest),
	"/v1/audio/speech":         string(schemas.SpeechRequest),
	"/v1/audio/transcriptions": string(schemas.TranscriptionRequest),
	"/v1/images/generations":   string(schemas.ImageGenerationRequest),
	"/v1/images/edits":         string(schemas.ImageEditRequest),
	"/v1/images/variations":    string(schemas.ImageVariationRequest),
	"/v1/videos":               string(schemas.VideoGenerationRequest),
}

// GetModelInfo returns pricing and capability metadata for a (provider, model)
// pair in the same shape the /v1/models endpoint reports, or nil when the
// catalog has no entry for it.
//
// Pricing and capability metadata are resolved independently so pricing comes
// from the best pricing row while capability surface metadata (mode,
// supported_endpoints, supported_methods) comes from the catalog's preferred
// capability entry (chat, then responses, then text completion, else a
// deterministic fallback). That keeps list-models enrichment and plugin-facing
// ctx.GetModelInfo aligned.
//
// The returned *schemas.Model is freshly allocated and owned by the caller.
func (mc *ModelCatalog) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	if mc == nil || model == "" {
		return nil
	}

	pricingEntry := mc.GetPricingEntryForModel(model, provider)
	capabilityEntry := mc.GetModelCapabilityEntryForModel(model, provider)
	if pricingEntry == nil && capabilityEntry == nil {
		return nil
	}

	info := &schemas.Model{ID: model}
	ApplyModelInfo(info, capabilityEntry)
	ApplyModelCapabilitySurface(info, capabilityEntry)
	ApplyModelInfo(info, pricingEntry)

	if params := mc.datasheet.GetSupportedParameters(model); len(params) > 0 {
		info.SupportedParameters = params
	}
	return info
}

// ApplyModelInfo merges catalog metadata from entry into model, filling only
// fields the caller has not already populated. Provider-reported values always
// win: the catalog is a backfill for what a provider's list-models response
// leaves out, never an override of what it did report.
//
// Exported so the list-models handlers can enrich provider responses through
// the same mapping that GetModelInfo uses.
//
// Every reference-typed field is cloned on the way in. An Entry's maps,
// pointers and slices alias the datasheet's shared pricing row (reading a
// struct out of that map is a shallow copy), so assigning them straight
// through would hand callers - including third-party plugins via
// ctx.GetModelInfo - a live handle into catalog state. A write through such a
// handle silently rewrites the catalog for every later request, and racing the
// pricing sync on the map is a fatal concurrent map access, not a recoverable
// panic.
func ApplyModelInfo(model *schemas.Model, entry *PricingEntry) {
	if model == nil || entry == nil {
		return
	}

	model.IsDeprecated = model.IsDeprecated || entry.IsDeprecated

	if entry.BaseModel != "" && model.NormalizedName == nil {
		model.NormalizedName = new(providerUtils.NormalizeBaseModelSlug(entry.BaseModel))
	}
	if len(entry.AdditionalAttributes) > 0 && model.AdditionalAttributes == nil {
		// A shallow clone is the right depth here: the values are strings, which
		// are immutable, so there is nothing further down to alias.
		model.AdditionalAttributes = maps.Clone(entry.AdditionalAttributes)
	}

	// ContextLength falls back to MaxInputTokens: some datasheet rows carry only
	// the input limit, and callers treat context length as the headline number.
	if model.ContextLength == nil {
		if entry.ContextLength != nil {
			model.ContextLength = new(*entry.ContextLength)
		} else if entry.MaxInputTokens != nil {
			model.ContextLength = new(*entry.MaxInputTokens)
		}
	}
	if entry.MaxInputTokens != nil && model.MaxInputTokens == nil {
		model.MaxInputTokens = new(*entry.MaxInputTokens)
	}
	if entry.MaxOutputTokens != nil && model.MaxOutputTokens == nil {
		model.MaxOutputTokens = new(*entry.MaxOutputTokens)
	}
	if entry.Architecture != nil && model.Architecture == nil {
		arch := *entry.Architecture
		// The scalar pointers need copying too, not just the slices: a struct
		// copy carries them straight through, and the entry's Architecture is
		// the very pointer the datasheet stores.
		if entry.Architecture.Modality != nil {
			arch.Modality = new(*entry.Architecture.Modality)
		}
		if entry.Architecture.Tokenizer != nil {
			arch.Tokenizer = new(*entry.Architecture.Tokenizer)
		}
		if entry.Architecture.InstructType != nil {
			arch.InstructType = new(*entry.Architecture.InstructType)
		}
		arch.InputModalities = slices.Clone(entry.Architecture.InputModalities)
		arch.OutputModalities = slices.Clone(entry.Architecture.OutputModalities)
		model.Architecture = &arch
	}

	if model.Pricing != nil {
		return
	}
	pricing := &schemas.Pricing{}
	if entry.InputCostPerToken != nil {
		pricing.Prompt = new(formatCost(*entry.InputCostPerToken))
	}
	if entry.OutputCostPerToken != nil {
		pricing.Completion = new(formatCost(*entry.OutputCostPerToken))
	}
	if entry.InputCostPerImage != nil {
		pricing.Image = new(formatCost(*entry.InputCostPerImage))
	}
	if entry.CacheReadInputTokenCost != nil {
		pricing.InputCacheRead = new(formatCost(*entry.CacheReadInputTokenCost))
	}
	if entry.CacheCreationInputTokenCost != nil {
		pricing.InputCacheWrite = new(formatCost(*entry.CacheCreationInputTokenCost))
	}
	if entry.SearchContextCostPerQuery != nil {
		pricing.WebSearch = new(formatCost(*entry.SearchContextCostPerQuery))
	}
	if entry.CostPerRequest != nil {
		pricing.Request = new(formatCost(*entry.CostPerRequest))
	}
	model.Pricing = pricing
}

// ApplyModelCapabilitySurface fills the preferred mode / endpoint / method
// surface derived from a capability entry. Provider-reported values always win:
// mode, supported_endpoints, and supported_methods are only backfilled when the
// provider left them empty.
func ApplyModelCapabilitySurface(model *schemas.Model, entry *PricingEntry) {
	if model == nil || entry == nil {
		return
	}

	mode := strings.TrimSpace(entry.Mode)
	if mode == "" {
		return
	}

	effectiveMode := mode
	if model.Mode != nil && strings.TrimSpace(*model.Mode) != "" {
		effectiveMode = strings.TrimSpace(*model.Mode)
	}

	endpoint := modelInfoEndpointByMode[effectiveMode]
	if endpoint == "" {
		return
	}

	if model.Mode == nil || strings.TrimSpace(*model.Mode) == "" {
		modeCopy := effectiveMode
		model.Mode = &modeCopy
	}
	if len(model.SupportedEndpoints) == 0 {
		model.SupportedEndpoints = []string{endpoint}
	}
	if len(model.SupportedMethods) == 0 {
		if method := modelInfoMethodByEndpoint[endpoint]; method != "" {
			model.SupportedMethods = []string{method}
		}
	}
}

// CalculateRequestCost returns the dollar cost of resp, resolving governance
// pricing overrides (virtual key / user / provider key scopes) from ctx.
//
// This is the plugin-facing entry point behind ctx.CalculateCost. It is a thin
// wrapper over CalculateCost so tiered pricing, batch/priority/flex rates and
// the provider/mode fallback chain all apply unchanged.
//
// Must be called synchronously while ctx is still live; the scope lookup reads
// request identity off the context.
func (mc *ModelCatalog) CalculateRequestCost(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) float64 {
	if mc == nil || resp == nil {
		return 0
	}
	extraFields := resp.GetExtraFields()
	provider := extraFields.RoutingInfo.Provider
	if provider == "" {
		provider = extraFields.Provider
	}
	return mc.CalculateCost(resp, PricingLookupScopesFromContext(ctx, string(provider)))
}

// formatCost renders a per-unit rate the way the models API reports it. Fixed
// 10-decimal notation rather than %g so sub-cent token rates never surface in
// scientific notation, which clients parsing these as decimals choke on.
func formatCost(v float64) string {
	return fmt.Sprintf("%.10f", v)
}
