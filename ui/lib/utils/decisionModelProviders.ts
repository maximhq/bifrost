import type { ProviderIconType } from "@/lib/constants/icons";
import { SELF_HOSTED_DECISION_MODELS } from "@/lib/types/complexityRouter";
import type { ModelProvider } from "@/lib/types/config";

// Decision-model provider detection, for the Complexity Router only.
//
// Laya, Nimble, and Clef (on Cloudflare Workers AI) are decision models. Operators reach them by
// adding a CUSTOM provider on the Typesafe base format, because none of them is a first-class
// provider. Custom providers have no logo of their own: the name is whatever the operator typed,
// and the base format would give all three Typesafe's mark. These helpers recognise which of the
// three model families a custom provider serves, so surfaces that list decision providers can show
// that family's logo instead.
//
// This is NOT a general custom-provider icon mechanism. It exists only so decision-model providers
// are told apart, and it only knows these three families. Use it where decision providers are
// listed (the Complexity Router's provider picker, the Providers page); do not reach for it to
// give other custom providers a logo.

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