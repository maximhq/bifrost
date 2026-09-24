import { ProviderIcons } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";

/**
 * Says whether a provider has a mark to draw.
 *
 * `RenderProviderIcon` returns null for anything not in `ProviderIcons`, which
 * is silent - a custom or self-hosted provider simply leaves the row showing a
 * model name with nothing to say who is answering it.
 */
export function hasProviderIcon(provider?: string): boolean {
	if (!provider) return false;
	return Object.prototype.hasOwnProperty.call(ProviderIcons, provider);
}

/**
 * Builds the composer's "who is answering" label.
 *
 * The provider is named in text whenever there is no icon for it, so the row
 * degrades to something readable rather than to the model alone. Where the icon
 * does render, it already carries the provider and repeating it in text would
 * just crowd a row that has to truncate.
 */
export function warpModelLabel(provider?: string, model?: string): string {
	const name = model ?? "";
	if (!provider || hasProviderIcon(provider)) return name;
	const label = getProviderLabel(provider);
	return name ? `${label} · ${name}` : label;
}