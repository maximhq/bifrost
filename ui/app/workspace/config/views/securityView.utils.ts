// parseMaxRequestTimeoutInput reads the max per-request timeout field as whole seconds. An empty
// input is 0 (x-bf-request-timeout ignored); anything that is not a whole number is null.
export function parseMaxRequestTimeoutInput(raw: string): number | null {
	const value = raw.trim();
	if (value === "") return 0;
	return /^\d+$/.test(value) ? Number(value) : null;
}

// isMaxRequestTimeoutChanged reports whether the max per-request timeout field differs from the
// saved client.max_request_timeout_in_seconds. An invalid input counts as a change, so Save stays
// enabled and the save-time validation can reject it instead of the click doing nothing.
export function isMaxRequestTimeoutChanged(raw: string, saved: number | undefined): boolean {
	const parsed = parseMaxRequestTimeoutInput(raw);
	return parsed === null || parsed !== (saved ?? 0);
}