import { ProviderIcons } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";

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