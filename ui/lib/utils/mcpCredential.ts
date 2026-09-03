// Formatting helpers shared by the MCP auth sessions table and the MCP
// server sheet's credential block, so both surfaces describe the same
// token row with the same words.

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