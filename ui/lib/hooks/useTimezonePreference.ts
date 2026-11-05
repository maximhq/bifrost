"use client";

import { useCallback, useEffect, useState } from "react";
import { getLocalTimezone, getPreferredTimezone, getSupportedTimezones, TIMEZONE_STORAGE_KEY } from "../timezones";

export { getSupportedTimezones };

/**
 * Hook that persists the user's preferred timezone in localStorage.
 *
 * Returns a `[timezone, setTimezone]` tuple. Defaults to the browser's
 * local timezone. Invalid stored values are silently replaced with the default.
 *
 * Safe for SSR/Next.js — reads localStorage lazily in an effect to avoid
 * hydration mismatches.
 */
export function useTimezonePreference(): [string, (tz: string) => void] {
	const [timezone, setTimezoneState] = useState<string>(getLocalTimezone);

	// Hydrate from localStorage after mount (client-only).
	useEffect(() => {
		setTimezoneState(getPreferredTimezone());
	}, []);

	const setTimezone = useCallback((tz: string) => {
		setTimezoneState(tz);
		try {
			localStorage.setItem(TIMEZONE_STORAGE_KEY, tz);
		} catch {
			// localStorage unavailable — preference won't persist.
		}
	}, []);

	return [timezone, setTimezone];
}