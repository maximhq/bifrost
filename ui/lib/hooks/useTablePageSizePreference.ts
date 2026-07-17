"use client";

import { useCallback, useEffect, useState } from "react";

/** Default fixed page-size options offered in a table's page-size dropdown. */
export const DEFAULT_PAGE_SIZE_OPTIONS = [10, 25, 50, 100, 200] as const;

/** Default page size used when no preference is stored. */
export const DEFAULT_PAGE_SIZE = 25;

/**
 * Persists a table's preferred page size in localStorage under `storageKey`.
 *
 * Reusable across any table — pass a unique key per table (e.g.
 * "bifrost.logs.pageSize", "bifrost.mcpLogs.pageSize"). Returns a
 * `[pageSize, setPageSize, hydrated]` tuple. The value is sent as the `limit`
 * query param on the list request. Defaults to `defaultPageSize` until a
 * stored value is hydrated.
 *
 * A persisted value is only accepted if it is one of `allowedSizes`. This
 * guards against a stale or hand-edited localStorage entry (e.g. `1000000`)
 * being sent as the query limit, which would bypass the dropdown's maximum and
 * recreate the oversized-response / expensive-query problem this hook prevents.
 *
 * `hydrated` is false until the localStorage read completes. Callers that sync
 * the preference into URL state must wait for it — writing the pre-hydration
 * default can clobber an explicit value already in the URL.
 *
 * Safe for SSR/Next.js — reads localStorage lazily in an effect to avoid
 * hydration mismatches.
 */
export function useTablePageSizePreference(
	storageKey: string,
	defaultPageSize: number = DEFAULT_PAGE_SIZE,
	allowedSizes: readonly number[] = DEFAULT_PAGE_SIZE_OPTIONS,
): [number, (value: number) => void, boolean] {
	const [pageSize, setPageSizeState] = useState<number>(defaultPageSize);
	const [hydrated, setHydrated] = useState(false);

	// Hydrate from localStorage after mount (client-only).
	useEffect(() => {
		try {
			const stored = localStorage.getItem(storageKey);
			if (stored) {
				const parsed = Number(stored);
				// Only accept a stored value that is one of the offered options —
				// never trust an arbitrary integer as the query limit.
				if (allowedSizes.some((size) => size === parsed)) {
					setPageSizeState(parsed);
				}
			}
		} catch {
			// localStorage unavailable (e.g. SSR, privacy mode) — keep default.
		}
		setHydrated(true);
	}, [storageKey, allowedSizes]);

	const setPageSize = useCallback(
		(value: number) => {
			setPageSizeState(value);
			try {
				localStorage.setItem(storageKey, String(value));
			} catch {
				// localStorage unavailable — preference won't persist.
			}
		},
		[storageKey],
	);

	return [pageSize, setPageSize, hydrated];
}