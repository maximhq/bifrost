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