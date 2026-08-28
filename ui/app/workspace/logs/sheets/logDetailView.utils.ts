/**
 * Which message the Raw JSON tab shows when a log row carries no raw payload.
 *
 * - `loading`         — the provider setting is still being fetched; committing to a
 *                       message now would flash the wrong one.
 * - `storage-disabled` — the provider is explicitly configured not to persist raw
 *                       request/response payloads, so we can explain *why* it is empty.
 * - `unknown`         — we cannot attribute the empty tab to the provider setting
 *                       (no provider-read permission, the fetch failed, the provider is
 *                       not in the list, or storage is on and the row simply failed
 *                       before reaching the provider). Falls back to neutral copy.
 */
export type RawJsonNoticeState = "loading" | "storage-disabled" | "unknown";

export function resolveRawJsonNoticeState({
	hasProvidersAccess,
	isProvidersLoading,
	isProvidersError,
	providers,
	provider,
}: {
	hasProvidersAccess: boolean;
	isProvidersLoading: boolean;
	isProvidersError: boolean;
	providers: { name: string; store_raw_request_response?: boolean }[] | undefined;
	provider: string;
}): RawJsonNoticeState {
	// The query is skipped without provider-read permission, and a failed fetch never
	// delivers data - in both cases waiting would strand the tab on a spinner forever.
	if (!hasProvidersAccess || isProvidersError) return "unknown";
	// Otherwise an absent `providers` means the request is still in flight: hold the
	// message back rather than flashing "No raw JSON available." before the setting is known.
	if (isProvidersLoading || !providers) return "loading";
	const match = providers.find((p) => p.name === provider);
	return match && match.store_raw_request_response === false ? "storage-disabled" : "unknown";
}
