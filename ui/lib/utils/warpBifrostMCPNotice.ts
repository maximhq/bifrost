import type { SecretVar } from "@/lib/types/schemas";

// Warp used to be read-only. It now also reaches the gateway's own API through
// the Bifrost MCP: it reads configuration, and creates things after the person
// confirms a card. A deployment that turned Warp on before that change gets the
// new behaviour on upgrade, so the Warp settings page shows its admins a one-time
// notice with the three settings that decide it. These helpers are the pure part.

/** localStorage key recording that this browser's admin has acknowledged the notice. */
export const WARP_BIFROST_MCP_NOTICE_KEY = "bifrost.warpBifrostMCPNoticeSeen";

/** The settings the notice reports, read from the core config. */
export interface WarpBifrostMCPSettings {
	/** bifrost_url as shown: an env or vault reference by its name, a plain value as is; "" when unset. */
	bifrostURL: string;
	/** framework_config.openapi_url; "" when unset (the build's published spec is fetched). */
	openAPIURL: string;
	/** client_config.bifrost_mcp_allow_writes, true unless explicitly turned off. */
	allowWrites: boolean;
}

/** What Warp does with the given settings. */
export type WarpManagementMode = "traffic_only" | "reads" | "reads_and_creates";

export function shownSecretVar(secret: SecretVar | undefined): string {
	if (!secret) return "";
	const ref = secret.ref?.trim();
	if (ref) return ref;
	return secret.value?.trim() ?? "";
}

export function warpBifrostMCPSettings(
	clientConfig: { bifrost_url?: SecretVar; bifrost_mcp_allow_writes?: boolean } | undefined,
	frameworkConfig: { openapi_url?: string } | undefined,
): WarpBifrostMCPSettings {
	return {
		bifrostURL: shownSecretVar(clientConfig?.bifrost_url),
		openAPIURL: frameworkConfig?.openapi_url?.trim() ?? "",
		allowWrites: clientConfig?.bifrost_mcp_allow_writes ?? true,
	};
}

/**
 * Without bifrost_url the Bifrost MCP does not load, so Warp keeps only its
 * traffic tools. With it, Warp reads configuration, and creates when writes are allowed.
 */
export function warpManagementMode(settings: WarpBifrostMCPSettings): WarpManagementMode {
	if (!settings.bifrostURL) return "traffic_only";
	return settings.allowWrites ? "reads_and_creates" : "reads";
}

/**
 * Whether a failed settings read means the caller may not read them (401 or 403),
 * as opposed to a network or server failure that a retry can fix.
 */
export function isAuthorizationError(error: unknown): boolean {
	const status = typeof error === "object" && error !== null && "status" in error ? (error as { status?: unknown }).status : undefined;
	return status === 401 || status === 403;
}

/** Only admins of a deployment that has Warp turned on see the notice, and only until they acknowledge it. */
export function shouldShowWarpBifrostMCPNotice(input: { warpEnabled: boolean; canUpdateWarp: boolean; acknowledged: boolean }): boolean {
	return input.warpEnabled && input.canUpdateWarp && !input.acknowledged;
}

export function readWarpBifrostMCPNoticeAcknowledged(): boolean {
	try {
		return localStorage.getItem(WARP_BIFROST_MCP_NOTICE_KEY) === "1";
	} catch {
		return false;
	}
}

export function writeWarpBifrostMCPNoticeAcknowledged(): void {
	try {
		localStorage.setItem(WARP_BIFROST_MCP_NOTICE_KEY, "1");
	} catch {
		// localStorage unavailable: the notice shows again next visit.
	}
}