package handlers

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// decisionsModality is the output modality that marks a model served on a
// decisions endpoint, the value OpenRouter's /v1/models reports for its own
// decision models.
const decisionsModality = "decisions"

// decisionRule is how a provider's Decision call treats one listed model.
type decisionRule struct {
	provider schemas.ModelProvider
	model    string // the model the request path checks: a key alias resolved to its target
	// settled marks a verdict that does not consult the datasheet, held in served.
	settled bool
	served  bool
	// fallback is the provider's name-based verdict, used when the datasheet has no
	// supports_decisions row.
	fallback bool
	// dedicated marks a model that serves nothing but decisions when it serves them:
	// every model of a decisions-only provider, or a name in the provider's dedicated
	// decision-model family.
	dedicated bool
}

// decisionRuleFor returns the rule the provider's Decision call gates the listed
// model on, so a listing neither offers a model the request would reject nor hides
// one it would serve.
//
// A custom provider is gated by its allowed requests alone: every model when it
// allows decisions on a base that serves them (Typesafe, serving Laya, Nimble, or
// Clef, OpenAI, exposing /v1/decisions, or vLLM and SGLang, serving /v1/systemone
// for every loaded model), since its server rejects a model it does not serve. A
// custom provider on the Gemini base that allows decisions is gated as Gemini is,
// since its Decision checks the datasheet and the name for custom providers too.
//
// Typesafe serves every model, vLLM and SGLang every model they load, and any other
// provider a model the datasheet marks supports_decisions, or without a row the
// provider's name-based fallback (the gpt-6-luna family on OpenAI, a TypeSafe System
// One model on OpenRouter, a Clef model on Cloudflare, a decider model on Perplexity,
// a System One model on Ollama, an ai_decide or System One model on Databricks, a
// decision-models/ slug on Baseten, a Gemini Decision model on Gemini, a Microsoft
// decision model or the gpt-6-luna family on Azure, which serves it on
// /openai/v1/decisions). Any other provider hosting gpt-6-luna serves it as a chat
// model (OpenRouter serves its decisions under its own id,
// openai/gpt-6-luna-decisions, marked by its datasheet row).
//
// A key alias is checked as the model it points at, as the request path checks it;
// on Azure the alias is the model name and its target a deployment name of the
// user's choosing, so the alias name is checked too.
//
// A model is dedicated to decisions by a strict rule, since a dedicated model is
// advertised as such to clients that would otherwise offer it for chat: Typesafe
// (and a custom provider on its base) serves nothing else; otherwise only a name in
// the provider's dedicated decision-model family qualifies (the fallbacks above
// except gpt-6-luna, which OpenAI and Azure serve on chat too), and a datasheet row
// marking it supports_decisions=false still withholds it. A datasheet row marking a
// model supports_decisions=true does not make it dedicated, as the same row marks
// models that also serve chat. vLLM and SGLang serve chat for every model they
// load, and a custom provider on the OpenAI base serves chat beside decisions, so
// none of their models is dedicated.
func decisionRuleFor(config *lib.Config, provider schemas.ModelProvider, model string) decisionRule {
	return decisionRuleForTarget(config, provider, model, keyAliasTarget(config, provider, model))
}

// decisionRuleForTarget is decisionRuleFor with the key alias already resolved:
// listed is the name a caller sends and model the one the request path checks for it.
func decisionRuleForTarget(config *lib.Config, provider schemas.ModelProvider, listed, model string) decisionRule {
	rule := decisionRule{provider: provider, model: model}
	if custom := customProviderConfig(config, provider); custom != nil {
		rule.settled = true
		if !custom.IsOperationAllowed(schemas.DecisionRequest) {
			return rule
		}
		switch custom.BaseProviderType {
		case schemas.Typesafe:
			rule.served, rule.dedicated = true, true
		case schemas.Gemini:
			rule.settled = false
			rule.dedicated = schemas.IsGeminiDecisionModel(model)
			rule.fallback = rule.dedicated
		case schemas.OpenAI, schemas.VLLM, schemas.SGL:
			rule.served = true
		}
		return rule
	}
	switch provider {
	case schemas.Typesafe:
		rule.settled, rule.served, rule.dedicated = true, true, true
	case schemas.VLLM, schemas.SGL:
		rule.settled, rule.served = true, true
	case schemas.OpenAI:
		rule.fallback = schemas.DefaultSupportsDecisions(model)
	case schemas.Azure:
		rule.dedicated = schemas.IsMicrosoftDecisionModel(listed) || schemas.IsMicrosoftDecisionModel(model)
		rule.fallback = rule.dedicated || schemas.DefaultSupportsDecisions(listed) || schemas.DefaultSupportsDecisions(model)
	case schemas.OpenRouter:
		rule.dedicated = schemas.IsTypesafeModel(model)
	case schemas.Cloudflare:
		rule.dedicated = schemas.IsClefModel(model)
	case schemas.Perplexity:
		rule.dedicated = schemas.IsPerplexityDeciderModel(model)
	case schemas.Ollama:
		rule.dedicated = schemas.IsOllamaDecisionModel(model)
	case schemas.Databricks:
		rule.dedicated = schemas.IsDatabricksDecisionModel(model)
	case schemas.Baseten:
		rule.dedicated = schemas.IsBasetenDecisionModel(model)
	case schemas.Gemini:
		rule.dedicated = schemas.IsGeminiDecisionModel(model)
	}
	if provider != schemas.OpenAI && provider != schemas.Azure {
		rule.fallback = rule.dedicated
	}
	return rule
}

// serves reports whether the provider serves the model on its decisions endpoint.
func (r decisionRule) serves() bool {
	if r.settled {
		return r.served
	}
	return schemas.ResolveModelCaps(r.provider, r.model).SupportsDecisions(r.fallback)
}

// servesOnlyDecisions reports whether the model is served on the decisions endpoint
// and on nothing else. The datasheet is consulted only for a dedicated model, so a
// full listing does not load a capability record per row.
func (r decisionRule) servesOnlyDecisions() bool {
	return r.dedicated && r.serves()
}

// customProviderConfig returns the provider's custom configuration, or nil for
// a built-in provider or one that is not configured.
func customProviderConfig(config *lib.Config, provider schemas.ModelProvider) *schemas.CustomProviderConfig {
	stored, ok := config.LiveStore.Collections.Providers.Get(string(provider))
	if !ok {
		return nil
	}
	return stored.Config.CustomProviderConfig
}

// keyAliasTarget returns the model a key alias points at, in the order the
// request path resolves it (schemas.ResolveCanonicalModel): the alias's
// ModelName, then its ModelID. A name that is not an alias is returned as is.
// Listings name key aliases as models, and a capability lookup on the alias
// itself finds nothing, so it is resolved through the catalog's alias index
// first, as GetModelCapabilityEntryForModel does for the details listing.
func keyAliasTarget(config *lib.Config, provider schemas.ModelProvider, model string) string {
	catalog := config.ModelCatalog
	if catalog == nil {
		return model
	}
	owner, ok := catalog.ResolveAlias(provider, model)
	if !ok {
		return model
	}
	return aliasConfigTarget(&owner.Config, model)
}

// aliasConfigTarget returns the model an alias points at, its ModelName, then its
// ModelID, or model itself when there is no alias or it names neither.
func aliasConfigTarget(alias *schemas.AliasConfig, model string) string {
	if alias == nil {
		return model
	}
	if name := alias.ModelName; name != nil && *name != "" {
		return *name
	}
	if alias.ModelID != "" {
		return alias.ModelID
	}
	return model
}

// servesOnlyDecisionsOnEveryKey reports whether every request for the listed model
// lands on a decision-only model. A name that is no key's alias is checked as itself.
// An alias is checked through each enabled key that may serve it, by that key's own
// target, since key selection can send the request through any of them and two keys
// can map the same alias to different models, while the catalog's alias index keeps
// only one of them. A key that allows the name without aliasing it sends the name
// as is. When no enabled key may serve the name, the catalog's alias decides.
func servesOnlyDecisionsOnEveryKey(config *lib.Config, provider schemas.ModelProvider, listed string) bool {
	catalog := config.ModelCatalog
	if catalog == nil {
		return decisionRuleForTarget(config, provider, listed, listed).servesOnlyDecisions()
	}
	owner, ok := catalog.ResolveAlias(provider, listed)
	if !ok {
		return decisionRuleForTarget(config, provider, listed, listed).servesOnlyDecisions()
	}
	checked := false
	for _, entry := range catalog.KeyConfigEntries(provider) {
		if !entry.Enabled || !entry.Allowed.IsAllowed(listed) || entry.Blacklisted.IsBlocked(listed) {
			continue
		}
		checked = true
		target := aliasConfigTarget(entry.Aliases.ResolveConfig(listed), listed)
		if !decisionRuleForTarget(config, provider, listed, target).servesOnlyDecisions() {
			return false
		}
	}
	if !checked {
		return decisionRuleForTarget(config, provider, listed, aliasConfigTarget(&owner.Config, listed)).servesOnlyDecisions()
	}
	return true
}

// tagDecisionModels adds the decisions output modality to each listed model its
// provider serves on the decisions endpoint and on nothing else (see
// tagDecisionModel).
func tagDecisionModels(config *lib.Config, resp *schemas.BifrostListModelsResponse) {
	if config == nil || resp == nil {
		return
	}
	for i := range resp.Data {
		tagDecisionModel(config, &resp.Data[i])
	}
}

// tagDecisionModel adds the decisions output modality to a model its provider
// serves on the decisions endpoint and on nothing else, through every key that may
// serve it (see decisionRuleFor and servesOnlyDecisionsOnEveryKey), so
// an OpenAI-compatible client can tell decision models from chat models by
// architecture.output_modalities alone, as it already can for OpenRouter's. A
// model that also serves chat is left as it is, so a client does not stop offering
// it for chat, and so is a model that already reports the modality.
//
// The id is of the form "<provider>/<model>". The architecture and its slice are
// replaced rather than written through: they may alias catalog or cached provider
// data.
func tagDecisionModel(config *lib.Config, row *schemas.Model) {
	if config == nil || row == nil {
		return
	}
	if row.Architecture != nil && slices.Contains(row.Architecture.OutputModalities, decisionsModality) {
		return
	}
	provider, model, ok := strings.Cut(row.ID, "/")
	if !ok || provider == "" || model == "" {
		return
	}
	if !servesOnlyDecisionsOnEveryKey(config, schemas.ModelProvider(provider), model) {
		return
	}
	var arch schemas.Architecture
	if row.Architecture != nil {
		arch = *row.Architecture
	}
	modalities := make([]string, 0, len(arch.OutputModalities)+1)
	arch.OutputModalities = append(append(modalities, arch.OutputModalities...), decisionsModality)
	row.Architecture = &arch
}
