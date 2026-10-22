import type { ProviderIconType } from "@/lib/constants/icons";
import {
	AZURE_DECISION_MODEL,
	CLOUDFLARE_DECISION_MODEL,
	DATABRICKS_DECISION_MODEL,
	DEFAULT_DECISION_MODEL,
	OPENAI_DECISION_MODEL,
	OPENROUTER_DECISION_MODEL,
	PERPLEXITY_DECISION_MODEL,
	SELF_HOSTED_DECISION_MODELS,
} from "@/lib/types/complexityRouter";
import type { ModelProvider } from "@/lib/types/config";

// Decision-model provider detection, shared by every surface that picks a
// decision model (the Complexity Router, Prompt Guardrails' decision judge) and
// the Providers page that lists decision providers.
//
// Laya, Nimble, and Clef are decision models. Operators reach Laya and Nimble by adding a CUSTOM
// provider on the Typesafe base format, because neither is a first-class provider. Clef is served
// by the first-class cloudflare provider, but setups that predate it reach it the same custom way,
// so it is still recognised here. Custom providers have no logo of their own: the name is whatever
// the operator typed, and the base format would give all three Typesafe's mark. These helpers recognise which of the
// three model families a custom provider serves, so surfaces that list decision providers can show
// that family's logo instead.
//
// The icon helper is NOT a general custom-provider icon mechanism. It exists only so decision-model
// providers are told apart, and it only knows these three families; do not reach for it to give
// other custom providers a logo.

// CLEF_URL_MODEL matches a decisions override that runs a Clef model on Cloudflare
// Workers AI, capturing the model the URL serves.
const CLEF_URL_MODEL = /\/ai\/run\/@cf\/cloudflare\/(clef(?:-flash)?)\/?$/;

// clefModelFromProvider reads the Clef model a provider serves from its decisions
// URL. Cloudflare binds the model to the URL and rejects any other in the body,
// so the URL is the one source of truth for it.
export function clefModelFromProvider(provider: ModelProvider | undefined): string | undefined {
	const url = provider?.custom_provider_config?.request_path_overrides?.decisions;
	return url?.match(CLEF_URL_MODEL)?.[1];
}

export type SelfHostedModelGroup = (typeof SELF_HOSTED_DECISION_MODELS)[number];

// namedSelfHostedGroup finds the model a self-hosted provider is named after
// ("Laya", "nimble-gpu"), when its name points at exactly one.
export function namedSelfHostedGroup(providerName: string): SelfHostedModelGroup | undefined {
	const name = providerName.toLowerCase();
	const named = SELF_HOSTED_DECISION_MODELS.filter((group) => name.includes(group.label.toLowerCase()));
	return named.length === 1 ? named[0] : undefined;
}

// decisionProviderIconKey picks the logo for a decision-model provider by the model family it
// serves: Clef from its Cloudflare URL, Laya or Nimble from the name the provider carries. See the
// file header for why this exists and where it applies. Only a custom provider on the Typesafe base
// qualifies, since that is the only way these models are added: a name is a guess, and an
// OpenAI-based provider called "nimble-gpu" must keep OpenAI's mark. Undefined means "not
// recognisably one of these three", so the caller keeps its standard icon.
export function decisionProviderIconKey(provider: ModelProvider): ProviderIconType | undefined {
	if (provider.custom_provider_config?.base_provider_type !== "typesafe") return undefined;
	if (clefModelFromProvider(provider) !== undefined) return "clef";
	const family = namedSelfHostedGroup(provider.name)?.label.toLowerCase();
	return family === "laya" || family === "nimble" || family === "clef" ? family : undefined;
}

// isDecisionProvider reports a provider that answers /v1/decisions natively:
// Typesafe, OpenRouter, Cloudflare (Clef), Perplexity, Ollama, Azure
// (Microsoft-Decision-1), Databricks (ai_decide), OpenAI or a custom provider built
// on it (their decision models, such as gpt-6-luna), or a custom provider built on
// the Typesafe base (Laya, Nimble, Clef). Other providers would only emulate
// decisions through chat, so decision-model pickers never offer them.
export function isDecisionProvider(provider: ModelProvider): boolean {
	return (
		provider.name === "typesafe" ||
		provider.name === "openrouter" ||
		provider.name === "cloudflare" ||
		provider.name === "perplexity" ||
		provider.name === "ollama" ||
		provider.name === "azure" ||
		provider.name === "databricks" ||
		isOpenAIDecisionProvider(provider) ||
		provider.custom_provider_config?.base_provider_type === "typesafe"
	);
}

// isOpenAIDecisionProvider reports OpenAI or a custom provider built on it.
// Only the models the datasheet marks as decision models are offered on it.
export function isOpenAIDecisionProvider(provider: ModelProvider | undefined): boolean {
	return provider?.name === "openai" || provider?.custom_provider_config?.base_provider_type === "openai";
}

// selfHostedModelGroups is the checkpoint list offered for a self-hosted provider
// that cannot list its models: the checkpoints of the model it is named after,
// otherwise every group.
export function selfHostedModelGroups(providerName: string): SelfHostedModelGroup[] {
	const named = namedSelfHostedGroup(providerName);
	return named ? [named] : [...SELF_HOSTED_DECISION_MODELS];
}

// defaultDecisionModel is the model a newly selected provider starts on: Jev's
// latest alias (named per provider), Clef Flash on Cloudflare, Azure's and
// Databricks' decision models, OpenAI's decision
// model (a custom OpenAI-based provider starts empty, as only its listing says
// what it serves), the Clef model a custom provider's Cloudflare URL serves, or
// the first checkpoint of the model a self-hosted provider is named after (Laya
// starts on english). A provider whose name says nothing starts empty, since only
// the operator knows which model it runs.
export function defaultDecisionModel(provider: ModelProvider | undefined): string {
	if (provider?.name === "typesafe") return DEFAULT_DECISION_MODEL;
	if (provider?.name === "openrouter") return OPENROUTER_DECISION_MODEL;
	if (provider?.name === "cloudflare") return CLOUDFLARE_DECISION_MODEL;
	if (provider?.name === "perplexity") return PERPLEXITY_DECISION_MODEL;
	if (provider?.name === "ollama") return "";
	if (provider?.name === "azure") return AZURE_DECISION_MODEL;
	if (provider?.name === "databricks") return DATABRICKS_DECISION_MODEL;
	if (provider?.name === "openai") return OPENAI_DECISION_MODEL;
	if (isOpenAIDecisionProvider(provider)) return "";
	return clefModelFromProvider(provider) ?? namedSelfHostedGroup(provider?.name ?? "")?.models[0] ?? "";
}

// listsDecisionModels reports a provider whose catalog mixes decision models with
// chat models, so a decision-model picker lists only the models the datasheet
// marks as decision models. Typesafe and self-hosted decision providers serve
// nothing else, so any model they name is one.
export function listsDecisionModels(provider: ModelProvider | undefined, providerName: string): boolean {
	if (provider ? isOpenAIDecisionProvider(provider) : providerName === "openai") return true;
	return ["openrouter", "cloudflare", "perplexity", "ollama", "azure"].includes(providerName);
}

// servesFixedDecisionModel reports a provider whose decision model is fixed by its
// configuration rather than chosen: Databricks serves only ai_decide, and a Cloudflare
// URL on a custom provider binds its Clef model. Pickers show the model, not a choice.
export function servesFixedDecisionModel(provider: ModelProvider | undefined, providerName: string): boolean {
	return providerName === "databricks" || clefModelFromProvider(provider) !== undefined;
}