// Formatting helpers shared by the MCP auth sessions table and the MCP
// server sheet's credential block, so both surfaces describe the same
// token row with the same words.

import type { MCPAuthType } from "@/lib/types/mcp";

export function formatRelativePast(iso: string): string {
	try {
		const t = new Date(iso).getTime();
		if (Number.isNaN(t)) return iso;
		const diffMs = Date.now() - t;
		if (diffMs < 60_000) return "just now";
		const mins = Math.floor(diffMs / 60_000);
		if (mins < 60) return `${mins} min ago`;
		const hours = Math.floor(diffMs / 3_600_000);
		if (hours < 48) return `${hours}h ago`;
		const days = Math.floor(diffMs / 86_400_000);
		return `${days}d ago`;
	} catch {
		return iso;
	}
}

/**
 * formatTokenExpiry describes an OAuth access token's expiry relative to now.
 * A past expiry only reads as "expired" when nothing can renew the token:
 * needs_reauth (refresh token rejected), or no refresh token at all. With
 * one, an active row refreshes on next use, and an orphaned row still holds
 * a valid upstream credential that refreshes once access is restored. An
 * unknown hasRefreshToken (older rows on the wire) keeps the status reading.
 */
export function formatTokenExpiry(expiresAt: string | null | undefined, status: string, hasRefreshToken?: boolean): string {
	if (!expiresAt) return "-";
	try {
		const t = new Date(expiresAt).getTime();
		if (Number.isNaN(t)) return expiresAt;
		const diffMs = t - Date.now();
		if (diffMs <= 0) {
			if (hasRefreshToken === false) return "expired";
			switch (status) {
				case "active":
					return "Refreshes on next use";
				case "orphaned":
					return "Refreshes when access is restored";
				default:
					return "expired";
			}
		}
		const days = Math.floor(diffMs / 86_400_000);
		if (days > 1) return `in ${days} days`;
		const hours = Math.floor(diffMs / 3_600_000);
		if (hours > 1) return `in ${hours} hours`;
		const mins = Math.floor(diffMs / 60_000);
		return `in ${Math.max(mins, 1)} min`;
	} catch {
		return expiresAt;
	}
}

export function formatAbsoluteDateTime(iso: string): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	return d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function formatAbsoluteDate(iso: string): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	return d.toLocaleDateString(undefined, { dateStyle: "medium" });
}

/**
 * missingHeaderKeys lists the required header names the stored admin values
 * do not cover: keys added to the required list after the values were
 * submitted. Header names compare case-insensitively, since the stored keys
 * are canonicalized on submission while the required list is typed by hand.
 */
export function missingHeaderKeys(required: readonly string[] | undefined, covered: readonly string[] | undefined): string[] {
	if (!required?.length) return [];
	const have = new Set((covered ?? []).map((k) => k.trim().toLowerCase()));
	return required.map((k) => k.trim()).filter((k) => k && !have.has(k.toLowerCase()));
}

/**
 * providerRejectedTheClient reads a credential's recorded rejection for the
 * OAuth error meaning the provider does not know Bifrost's client_id at all
 * (RFC 6749 invalid_client), as opposed to the far more common case of the
 * grant behind one token being revoked. Only the former needs a replacement
 * client registered before consent; plain Reauthorize fixes the latter.
 *
 * A loose substring match is the right amount of certainty here: it decides
 * whether to show one extra sentence of guidance, so a provider that words its
 * rejection differently costs the admin a hint, not a broken repair. Nothing
 * about replacing a credential keys off this.
 */
export function providerRejectedTheClient(statusReason?: string): boolean {
	return !!statusReason && statusReason.toLowerCase().includes("invalid_client");
}

/**
 * supportsClientReregistration reports whether an auth type has the
 * "Reauthorize with a new client" action: the ones whose OAuth client Bifrost
 * can register for itself, which is also exactly what POST /reregister accepts.
 * token_exchange is OAuth-shaped but not one of them. Its client is configured
 * by hand and its repair is "Re-verify as me".
 *
 * The servers table's menu item and the hint below both read this rather than
 * each spelling the list out, so neither can name an action the other does not
 * offer.
 */
export function supportsClientReregistration(authType?: MCPAuthType): boolean {
	return authType === "oauth" || authType === "per_user_oauth";
}

/**
 * shouldSuggestReplacementClient decides whether a needs_reauth credential
 * gets the extra sentence sending the admin to "Reauthorize with a new client".
 * Both halves matter: the provider has to have disowned the client, and the
 * server has to be of a kind that has that action. A token_exchange credential
 * can carry invalid_client too (the identity provider's error code is kept
 * verbatim in its status reason), and pointing its admin at a menu item that is
 * not there is worse than saying nothing.
 */
export function shouldSuggestReplacementClient(authType: MCPAuthType | undefined, statusReason?: string): boolean {
	return supportsClientReregistration(authType) && providerRejectedTheClient(statusReason);
}