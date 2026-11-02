export function capitalize(name: string) {
	return name.charAt(0).toUpperCase() + name.slice(1);
}

// titleCaseFromSnakeCase turns a snake_case identifier (e.g. an enum-style
// state value like "pending_verification") into a title-cased display string
// (e.g. "Pending Verification"), for rendering in badges, labels, and the like.
export function titleCaseFromSnakeCase(value: string): string {
	return value
		.split("_")
		.filter(Boolean)
		.map((word) => capitalize(word.toLowerCase()))
		.join(" ");
}

// isSentinelValue reports whether value is an internal placeholder like "__none__" that must never reach the screen.
export function isSentinelValue(value: string): boolean {
	return /^__.+__$/.test(value);
}

export type TrimmableKeys<T> = { [K in keyof T]: T[K] extends string | string[] | undefined ? K : never }[keyof T];

// trimFields trims whitespace from the named string (or string[]) fields of obj, in place.
// Undefined fields are left untouched.
export function trimFields<T extends object>(obj: T, ...keys: TrimmableKeys<T>[]): T {
	for (const key of keys) {
		const value = obj[key];
		if (typeof value === "string") {
			obj[key] = value.trim() as T[typeof key];
		} else if (Array.isArray(value)) {
			obj[key] = value.map((item) => (typeof item === "string" ? item.trim() : item)) as T[typeof key];
		}
	}
	return obj;
}

// Cleans raw input into a valid numeric string:
// - Single non-alphabetic separator between digits (commas, spaces, underscores) → stripped
// - Alphabetic characters → stop processing
// - 2+ consecutive non-digit characters → stop processing
// - First decimal point preserved, subsequent dots stripped
export function cleanNumericInput(raw: string): string {
	raw = raw.trim();
	let result = "";
	let hasDecimal = false;
	let i = 0;
	while (i < raw.length) {
		const ch = raw[i];
		if (/\d/.test(ch)) {
			result += ch;
			i++;
		} else if (ch === "." && !hasDecimal) {
			result += ch;
			hasDecimal = true;
			i++;
		} else if (/[a-zA-Z]/.test(ch)) {
			break;
		} else {
			// Non-alphabetic, non-digit character (comma, space, extra dot, etc.)
			// Accept only if it's a single separator followed by a digit
			if (i + 1 < raw.length && /\d/.test(raw[i + 1])) {
				i++; // skip the separator
			} else {
				break;
			}
		}
	}
	return result;
}

export function formatBytes(bytes: number): string {
	if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
	const k = 1024;
	const sizes = ["B", "KB", "MB", "GB", "TB"];
	const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
	return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
}

/** A duration in seconds as words ("2 minutes 5 seconds"), as the network timeout fields describe their value. */
export const secondsToHumanReadable = (seconds: number) => {
	// Handle edge cases
	if (!seconds || seconds < 0 || isNaN(seconds)) {
		return "0 seconds";
	}
	seconds = Math.floor(seconds);
	if (seconds < 60) {
		return `${seconds} ${seconds === 1 ? "second" : "seconds"}`;
	}
	if (seconds < 3600) {
		const minutes = Math.floor(seconds / 60);
		const remainingSeconds = seconds % 60;
		const parts: string[] = [`${minutes} ${minutes === 1 ? "minute" : "minutes"}`];
		if (remainingSeconds > 0) parts.push(`${remainingSeconds} ${remainingSeconds === 1 ? "second" : "seconds"}`);
		return parts.join(" ");
	}
	if (seconds < 86400) {
		const hours = Math.floor(seconds / 3600);
		const minutes = Math.floor((seconds % 3600) / 60);
		const remainingSeconds = seconds % 60;
		const parts: string[] = [`${hours} ${hours === 1 ? "hour" : "hours"}`];
		if (minutes > 0) parts.push(`${minutes} ${minutes === 1 ? "minute" : "minutes"}`);
		if (remainingSeconds > 0) parts.push(`${remainingSeconds} ${remainingSeconds === 1 ? "second" : "seconds"}`);
		return parts.join(" ");
	}
	// For >= 1 day, only show non-zero components
	const days = Math.floor(seconds / 86400);
	const hours = Math.floor((seconds % 86400) / 3600);
	const minutes = Math.floor((seconds % 3600) / 60);
	const remainingSeconds = seconds % 60;
	const parts: string[] = [];
	parts.push(`${days} ${days === 1 ? "day" : "days"}`);
	if (hours > 0) parts.push(`${hours} ${hours === 1 ? "hour" : "hours"}`);
	if (minutes > 0) parts.push(`${minutes} ${minutes === 1 ? "minute" : "minutes"}`);
	if (remainingSeconds > 0) parts.push(`${remainingSeconds} ${remainingSeconds === 1 ? "second" : "seconds"}`);
	return parts.join(" ");
};