import { KnownProvidersNames, ProviderLabels } from "@/lib/constants/logs";
import { KnownProvider, ModelProvider } from "@/lib/types/config";

export interface CustomProviderCollision {
	customName: string;
	knownProvider: KnownProvider;
}

/** Lowercase and strip everything except letters and digits, so "GitHub Copilot" matches "github-copilot". */
export const normalizeProviderName = (name: string): string => name.toLowerCase().replace(/[^a-z0-9]/g, "");

let lookup: Map<string, KnownProvider> | undefined;

const getLookup = (): Map<string, KnownProvider> => {
	if (lookup) return lookup;
	lookup = new Map();
	for (const key of KnownProvidersNames) {
		lookup.set(normalizeProviderName(key), key);
		lookup.set(normalizeProviderName(ProviderLabels[key]), key);
	}
	return lookup;
};

/** Returns the first-party provider whose key or display label matches the given name, if any. */
export const findFirstPartyMatch = (name: string): KnownProvider | undefined => {
	const normalized = normalizeProviderName(name);
	if (!normalized) return undefined;
	return getLookup().get(normalized);
};

/** Custom providers whose name matches a provider Bifrost now supports natively. */
export const findCustomProviderCollisions = (providers: ModelProvider[]): CustomProviderCollision[] => {
	const collisions: CustomProviderCollision[] = [];
	for (const provider of providers) {
		if (!provider.custom_provider_config) continue;
		const knownProvider = findFirstPartyMatch(provider.name);
		if (knownProvider) {
			collisions.push({ customName: provider.name, knownProvider });
		}
	}
	return collisions;
};
