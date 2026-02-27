/**
 * Converts a Date object to an RFC 3339 string with the local time zone offset.
 *
 * Example: 2025-11-19T12:23:19.421+05:30
 *
 * @param dateObj The Date object to convert (defaults to new Date() if null/undefined).
 * @returns The RFC 3339 formatted string with local offset.
 */
export function dateToRfc3339Local(dateObj?: Date): string {
	const now = dateObj instanceof Date ? dateObj : new Date();

	// Helper function to pad single digits with a leading zero
	const pad = (num: number): string => (num < 10 ? "0" + num : String(num));

	const Y = now.getFullYear();
	const M = pad(now.getMonth() + 1); // Month is 0-indexed (Jan=0)
	const D = pad(now.getDate());
	const H = pad(now.getHours());
	const m = pad(now.getMinutes());
	const S = pad(now.getSeconds());
	const ms = String(now.getMilliseconds()).padStart(3, "0");

	// getTimezoneOffset() returns the difference in minutes from UTC for the local time.
	// The result is positive for time zones west of Greenwich and negative for those east.
	// We negate it to get the standard ISO/RFC sign convention (+ for East, - for West).
	const timezoneOffsetMinutes = -now.getTimezoneOffset();
	const sign = timezoneOffsetMinutes >= 0 ? "+" : "-";
	const absoluteOffset = Math.abs(timezoneOffsetMinutes);
	const offsetHours = pad(Math.floor(absoluteOffset / 60));
	const offsetMinutes = pad(absoluteOffset % 60);
	const rfc3339Local = `${Y}-${M}-${D}T${H}:${m}:${S}.${ms}${sign}${offsetHours}:${offsetMinutes}`;
	return rfc3339Local;
}
