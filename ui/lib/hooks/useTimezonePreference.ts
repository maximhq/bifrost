"use client";

import { useCallback, useEffect, useState } from "react";
import { getSupportedTimezones } from "../timezones";

export { getSupportedTimezones };

const STORAGE_KEY = "bifrost.timezone";

/** Returns the browser's local IANA timezone (e.g. "America/New_York"). */
function getLocalTimezone(): string {
	return Intl.DateTimeFormat().resolvedOptions().timeZone;
}

/** Validates an IANA timezone string against the supported list. */
function isValidTimezone(tz: string): boolean {
	return getSupportedTimezones().includes(tz);
}

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
		try {
			const stored = localStorage.getItem(STORAGE_KEY);
			if (stored && isValidTimezone(stored)) {
				setTimezoneState(stored);
			}
		} catch {
			// localStorage unavailable (e.g. SSR, privacy mode) — keep default.
		}
	}, []);

	const setTimezone = useCallback((tz: string) => {
		setTimezoneState(tz);
		try {
			localStorage.setItem(STORAGE_KEY, tz);
		} catch {
			// localStorage unavailable — preference won't persist.
		}
	}, []);

	return [timezone, setTimezone];
}