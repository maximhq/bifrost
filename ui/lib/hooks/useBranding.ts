import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetBrandingQuery, type BrandingState } from "@/lib/store/apis/brandingApi";
import { getApiBaseUrl } from "@/lib/utils/port";
import { useEffect } from "react";

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

/**
 * Last known branding state, mirrored in localStorage.
 *
 * The GET is a round trip, and until it lands every branded surface would
 * render the bundled Bifrost defaults — so a client-side navigation into the
 * dashboard, or a reload once past the pre-hydration shell, flashed the wrong
 * logo. Persisting the resolved state lets the first paint use the customer's
 * assets: the URLs are content-versioned and served with a long max-age, so the
 * images themselves come from the browser cache and nothing is fetched.
 *
 * A cached URL can be stale if another admin re-uploaded since — it 404s and
 * shows a broken image for the one frame before the in-flight response replaces
 * it. Acceptable against a guaranteed wrong-logo flash on every load.
 */
const CACHE_KEY = "bifrost-branding";

/** Read once per page load and kept in sync from the query below, so every
 * consumer of the hook agrees and repeat renders don't re-parse the entry. */
let cachedBranding: BrandingState | null | undefined;

/**
 * Validates a parsed cache entry before it is trusted.
 *
 * Checking `enabled` alone is not enough: the URLs reach
 * resolveBrandingAssetUrl, which calls startsWith on them, so a
 * wrong-typed URL would throw during render rather than degrade to the
 * defaults. null counts as absent — if the branding endpoint ever
 * serializes an unset URL as null instead of omitting it, the entry is
 * still usable and discarding it would restore the flash this cache
 * exists to prevent.
 */
function isOptionalString(value: unknown): boolean {
	return value === undefined || value === null || typeof value === "string";
}

function isBrandingState(value: unknown): value is BrandingState {
	if (!value || typeof value !== "object") return false;
	const state = value as Record<string, unknown>;
	return (
		typeof state.enabled === "boolean" &&
		typeof state.has_logo === "boolean" &&
		typeof state.has_icon === "boolean" &&
		isOptionalString(state.logo_url) &&
		isOptionalString(state.icon_url) &&
		isOptionalString(state.updated_at)
	);
}

function readCachedBranding(): BrandingState | undefined {
	if (cachedBranding === undefined) {
		cachedBranding = null;
		try {
			const raw = localStorage.getItem(CACHE_KEY);
			// Guard against a hand-edited or older-shaped entry: a bad parse must
			// fall back to the defaults, not render undefined URLs.
			const parsed: unknown = raw ? JSON.parse(raw) : null;
			if (isBrandingState(parsed)) {
				cachedBranding = parsed;
			}
		} catch {
			// localStorage unavailable (privacy mode) or unparseable — no cache.
		}
	}
	return cachedBranding ?? undefined;
}

function writeCachedBranding(state: BrandingState) {
	// Nothing to restore on a deployment with no branding, and the entry has to
	// be dropped on reset or the defaults would never come back.
	cachedBranding = state.enabled ? state : null;
	try {
		if (cachedBranding) {
			localStorage.setItem(CACHE_KEY, JSON.stringify(cachedBranding));
		} else {
			localStorage.removeItem(CACHE_KEY);
		}
	} catch {
		// localStorage unavailable — branding just won't paint early next load.
	}
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
 * While the query is in flight the last known state from localStorage is used,
 * falling back to the defaults when there is none — so the logo never renders
 * as a blank gap, and a branded deployment doesn't flash the Bifrost logo while
 * the response is outstanding. The pre-hydration shell is separately rewritten
 * server-side to cover the initial document load, before any of this runs.
 */
export function useBranding(isDark: boolean): BrandingAssets {
	// Fires on the login screen too, where no session exists — the endpoint is
	// public precisely so this works pre-auth. Skipped on OSS, where the whole
	// branding surface lives in the enterprise build and the route does not
	// exist: without this every page load would spend a request on a 404 and
	// then fall back to the defaults it already has.
	const { data } = useGetBrandingQuery(undefined, { skip: !IS_ENTERPRISE });

	// The mutations invalidate the Branding tag, so a save or reset refetches and
	// lands here — every cache write funnels through this one effect.
	useEffect(() => {
		if (data) writeCachedBranding(data);
	}, [data]);

	// Safe to read during render: the app is client-rendered (createRoot, not
	// hydrateRoot), so there is no server markup for this to mismatch against.
	const branding = data ?? (IS_ENTERPRISE ? readCachedBranding() : undefined);

	const hasLogo = Boolean(branding?.has_logo && branding.logo_url);
	const hasIcon = Boolean(branding?.has_icon && branding.icon_url);

	return {
		logoSrc: hasLogo ? resolveBrandingAssetUrl(branding!.logo_url) : isDark ? DEFAULT_LOGO_DARK : DEFAULT_LOGO_LIGHT,
		iconSrc: hasIcon ? resolveBrandingAssetUrl(branding!.icon_url) : isDark ? DEFAULT_ICON_DARK : DEFAULT_ICON_LIGHT,
		isCustom: Boolean(branding?.enabled),
		logoAlt: branding?.enabled ? "" : "Bifrost",
	};
}