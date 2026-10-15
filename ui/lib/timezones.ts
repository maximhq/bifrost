export const FALLBACK_TIMEZONES = [
	"UTC",
	"America/New_York",
	"America/Chicago",
	"America/Denver",
	"America/Los_Angeles",
	"America/Anchorage",
	"Pacific/Honolulu",
	"Europe/London",
	"Europe/Paris",
	"Europe/Berlin",
	"Asia/Tokyo",
	"Asia/Shanghai",
	"Asia/Kolkata",
	"Asia/Dubai",
	"Australia/Sydney",
	"Pacific/Auckland",
];

/** Returns the full list of IANA timezone identifiers, or a curated fallback. */
export function getSupportedTimezones(): string[] {
	try {
		if (typeof Intl.supportedValuesOf === "function") {
			return Intl.supportedValuesOf("timeZone");
		}
	} catch {
		// Fallback for older runtimes
	}
	return FALLBACK_TIMEZONES;
}
export const TIMEZONE_STORAGE_KEY = "bifrost.timezone";

/** Returns the browser's local IANA timezone (e.g. "America/New_York"). */
export function getLocalTimezone(): string {
	return Intl.DateTimeFormat().resolvedOptions().timeZone;
}

/**
 * Returns the timezone the user picked in a date picker (persisted by
 * useTimezonePreference), or the browser's local zone when none is stored or
 * the stored value is not a known zone.
 */
export function getPreferredTimezone(): string {
	try {
		const stored = localStorage.getItem(TIMEZONE_STORAGE_KEY);
		if (stored && getSupportedTimezones().includes(stored)) return stored;
	} catch {
		// localStorage unavailable (e.g. SSR, privacy mode).
	}
	return getLocalTimezone();
}