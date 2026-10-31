import { ProviderIcons } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import type { WarpConfig, WarpModel } from "@/lib/types/warp";

/** `RenderProviderIcon` silently renders null for providers missing from `ProviderIcons`. */
export function hasProviderIcon(provider?: string): boolean {
	if (!provider) return false;
	return Object.prototype.hasOwnProperty.call(ProviderIcons, provider);
}

/** Names the provider in text only when there is no icon to say it. */
export function warpModelLabel(provider?: string, model?: string): string {
	const name = model ?? "";
	if (!provider || hasProviderIcon(provider)) return name;
	const label = getProviderLabel(provider);
	return name ? `${label} · ${name}` : label;
}

/** Every model the panel may run on, the default first. Mirrors schemas.WarpConfig.Models. */
export function warpModels(config?: Pick<WarpConfig, "provider" | "model" | "api_key_id" | "additional_models">): WarpModel[] {
	if (!config) return [];
	const models: WarpModel[] = [];
	if (config.provider && config.model) models.push({ provider: config.provider, model: config.model, api_key_id: config.api_key_id });
	return [...models, ...(config.additional_models ?? [])];
}

/** Identifies a model by the pair the server matches on. NUL cannot appear in either half. */
export function warpModelKey(model: Pick<WarpModel, "provider" | "model">): string {
	return `${model.provider}\u0000${model.model}`;
}

/**
 * The model the next question runs on: the remembered choice while the operator
 * still exposes it, otherwise the default.
 *
 * Resolved against the live list on every read rather than trusted as stored. A
 * remembered model an administrator has since removed would be refused by the
 * server on every question, so it has to fall back here, where it is cheap.
 */
export function resolveWarpModel(models: WarpModel[], selectedKey: string | null): WarpModel | undefined {
	return models.find((model) => warpModelKey(model) === selectedKey) ?? models[0];
}

/**
 * What a chat request names as its model, or undefined to leave it to the
 * server's default.
 *
 * The default is never named. Sent explicitly, a default the administrator
 * changed since this page loaded its config would be refused as no longer
 * exposed; left out, the question simply runs on whatever the default now is.
 */
export function warpModelForRequest(
	models: WarpModel[],
	selected: WarpModel | undefined,
): Pick<WarpModel, "provider" | "model"> | undefined {
	if (!selected || models.length === 0 || warpModelKey(selected) === warpModelKey(models[0])) return undefined;
	return { provider: selected.provider, model: selected.model };
}

const WARP_MODEL_STORAGE_KEY = "bifrost.warp.model";

/** The model this browser last picked, as a warpModelKey. Null when none, or when storage is unavailable. */
export function readStoredWarpModelKey(): string | null {
	try {
		return localStorage.getItem(WARP_MODEL_STORAGE_KEY);
	} catch {
		return null;
	}
}

/** Remembers the picked model for this browser. A preference only: the server decides what may actually run. */
export function storeWarpModelKey(key: string): void {
	try {
		localStorage.setItem(WARP_MODEL_STORAGE_KEY, key);
	} catch {
		// Storage unavailable: the choice lasts for this page load only.
	}
}
