import type { SecretVar } from "@/lib/types/schemas";

// Warp used to be read-only. It now reaches the gateway's own API through the
// Bifrost MCP: it reads configuration, and creates things after the person
// confirms a card. It needs the Bifrost MCP to answer at all, so a deployment
// that turned Warp on before that change gets the new behaviour on upgrade, or,
// without bifrost_url, a Warp that does not answer until an admin sets it. The
// Warp settings page and panel show admins a notice with the three settings that
// decide it. These helpers are the pure part.

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
export type WarpManagementMode = "off" | "reads" | "reads_and_writes";

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
 * Without bifrost_url the Bifrost MCP does not load, and Warp does not answer.
 * With it, Warp reads configuration, and makes changes only when its settings
 * allow some kinds (writes, reads only by default) and the Bifrost MCP serves
 * writes at all (allowWrites, the ceiling).
 */
export function warpManagementMode(settings: WarpBifrostMCPSettings, writes: readonly string[] = []): WarpManagementMode {
	if (!settings.bifrostURL) return "off";
	return settings.allowWrites && writes.length > 0 ? "reads_and_writes" : "reads";
}

/**
 * Whether a failed settings read means the caller may not read them (401 or 403),
 * as opposed to a network or server failure that a retry can fix.
 */
export function isAuthorizationError(error: unknown): boolean {
	const status = typeof error === "object" && error !== null && "status" in error ? (error as { status?: unknown }).status : undefined;
	return status === 401 || status === 403;
}

/** localStorage key counting how many times this browser's admin closed the notice without acknowledging it. */
export const WARP_BIFROST_MCP_NOTICE_LATER_COUNT_KEY = "bifrost.warpBifrostMCPNoticeLaterCount";

/** After this many "remind me later"s the notice stops opening by itself and becomes a banner on the page. */
export const WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS = 5;

/** How the notice presents: not at all, as a dialog that opens by itself, or as a banner that opens it on request. */
export type WarpBifrostMCPNoticeMode = "hidden" | "dialog" | "banner";

/**
 * Only admins of a deployment that has Warp turned on, and whose Warp uses the
 * Bifrost MCP (bifrost_mcp_enabled; off while the two are decoupled), see the notice. It opens by
 * itself until it has been put off WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS times,
 * then stays on the page as a banner. With the Bifrost MCP loaded it is
 * informational and acknowledging it hides it for good. Without it Warp does not
 * answer, so there is nothing to acknowledge: an earlier acknowledgement does not
 * hide it, and only the reminder cap turns it into a banner.
 */
export function warpBifrostMCPNoticeMode(input: {
	bifrostMCPEnabled: boolean;
	warpEnabled: boolean;
	canUpdateWarp: boolean;
	bifrostMCPAvailable: boolean;
	acknowledged: boolean;
	laterCount: number;
}): WarpBifrostMCPNoticeMode {
	if (!input.bifrostMCPEnabled || !input.warpEnabled || !input.canUpdateWarp) return "hidden";
	if (input.bifrostMCPAvailable && input.acknowledged) return "hidden";
	return input.laterCount >= WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS ? "banner" : "dialog";
}

export function readWarpBifrostMCPNoticeLaterCount(): number {
	try {
		const count = Number.parseInt(localStorage.getItem(WARP_BIFROST_MCP_NOTICE_LATER_COUNT_KEY) ?? "", 10);
		return Number.isFinite(count) && count > 0 ? count : 0;
	} catch {
		return 0;
	}
}

export function writeWarpBifrostMCPNoticeLaterCount(count: number): void {
	try {
		localStorage.setItem(WARP_BIFROST_MCP_NOTICE_LATER_COUNT_KEY, String(count));
	} catch {
		// localStorage unavailable: the count only lasts for this page.
	}
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