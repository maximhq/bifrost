import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetBrandingQuery } from "@/lib/store/apis/brandingApi";
import { getApiBaseUrl } from "@/lib/utils/port";

/** Bundled Bifrost assets. These are the OSS values and the fallback for every
 * slot an enterprise deployment has not overridden. */
const DEFAULT_LOGO_LIGHT = "/bifrost-logo.webp";
const DEFAULT_LOGO_DARK = "/bifrost-logo-dark.webp";
const DEFAULT_ICON_LIGHT = "/bifrost-icon.webp";
const DEFAULT_ICON_DARK = "/bifrost-icon-dark.webp";

/**
 * Resolves a branding asset URL returned by the API against the current API
 * base. The server returns paths rooted at /api; in development the app is
 * served from a different origin than the API, so those need the resolved base
 * prepended or the browser would request them from the Vite dev server.
 */
export function resolveBrandingAssetUrl(url: string | undefined): string {
	if (!url) return "";
	const apiBase = getApiBaseUrl();
	return url.startsWith("/api/") ? `${apiBase}${url.slice("/api".length)}` : url;
}

export interface BrandingAssets {
	/** Wordmark for the expanded sidebar and the login screen. */
	logoSrc: string;
	/** Square mark for the collapsed sidebar. */
	iconSrc: string;
	/** True when a custom logo or icon is in use. Surfaces that hardcode
	 * "Bifrost" as alt text or a product name should fall back to a neutral
	 * label when this is set. */
	isCustom: boolean;
	/** Alt text: the customer's logo is not the Bifrost logo, and labelling it
	 * as such would be wrong on a custom branding deployment. */
	logoAlt: string;
}

/**
 * Resolves which logo and icon to render.
 *
 * Each slot falls back independently: a deployment that uploaded only a logo
 * keeps the default Bifrost icon. Custom assets are theme-agnostic — a single
 * upload serves both light and dark — so `isDark` only selects between the two
 * bundled defaults.
 *
 * While the query is in flight the defaults are returned rather than nothing,
 * so the logo never renders as a blank gap. On a branded deployment that means
 * a brief flash of the Bifrost logo on first paint; the pre-hydration shell is
 * rewritten server-side to avoid it on the initial document load, and the
 * response is cached for subsequent navigations.
 */
export function useBranding(isDark: boolean): BrandingAssets {
	// Fires on the login screen too, where no session exists — the endpoint is
	// public precisely so this works pre-auth. Skipped on OSS, where the whole
	// branding surface lives in the enterprise build and the route does not
	// exist: without this every page load would spend a request on a 404 and
	// then fall back to the defaults it already has.
	const { data } = useGetBrandingQuery(undefined, { skip: !IS_ENTERPRISE });

	const hasLogo = Boolean(data?.has_logo && data.logo_url);
	const hasIcon = Boolean(data?.has_icon && data.icon_url);

	return {
		logoSrc: hasLogo ? resolveBrandingAssetUrl(data!.logo_url) : isDark ? DEFAULT_LOGO_DARK : DEFAULT_LOGO_LIGHT,
		iconSrc: hasIcon ? resolveBrandingAssetUrl(data!.icon_url) : isDark ? DEFAULT_ICON_DARK : DEFAULT_ICON_LIGHT,
		isCustom: Boolean(data?.enabled),
		logoAlt: data?.enabled ? "" : "Bifrost",
	};
}