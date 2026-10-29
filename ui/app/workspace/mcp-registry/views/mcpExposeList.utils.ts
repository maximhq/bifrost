import { isRegexEntry, regexEntryPattern } from "@/components/modelAccess/utils";

const sameKey = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

// matchesEntry mirrors the gateway: plain entries match case-insensitively, regex: entries as a full
// case-insensitive match; a pattern that does not compile never matches.
function matchesEntry(entry: string, key: string): boolean {
	if (!isRegexEntry(entry)) return sameKey(entry, key);
	try {
		return new RegExp(`^(?:${regexEntryPattern(entry)})$`, "i").test(key);
	} catch {
		return false;
	}
}

const isUnrestricted = (value: string[] | null | undefined) => !value || value.includes("*");

// isExposed reports whether key is served under an allow-list; absent or ["*"] serves everything.
export function isExposed(value: string[] | null | undefined, key: string): boolean {
	return isUnrestricted(value) || value!.some((entry) => matchesEntry(entry, key));
}

// exposedByRule reports whether a regex: entry serves key, so its switch cannot turn it off.
export function exposedByRule(value: string[] | null | undefined, key: string): boolean {
	return !isUnrestricted(value) && value!.some((entry) => isRegexEntry(entry) && matchesEntry(entry, key));
}

// nextExposeList is the allow-list after toggling key. Rules and saved keys missing from discovery are
// kept; it collapses to ["*"] only when the list is exactly the discovered keys.
export function nextExposeList(value: string[] | null | undefined, discovered: string[], key: string, exposed: boolean): string[] {
	// Keys differing only by case are one entry to the gateway, and it rejects both in one list.
	const allKeys = discovered.filter((k, i) => discovered.findIndex((other) => sameKey(other, k)) === i);
	if (isUnrestricted(value)) {
		return exposed ? ["*"] : allKeys.filter((k) => !sameKey(k, key));
	}
	const rest = value!.filter((entry) => isRegexEntry(entry) || !sameKey(entry, key));
	const next = exposed && !rest.some((entry) => matchesEntry(entry, key)) ? [...rest, key] : rest;
	const isExactlyDiscovered =
		next.length === allKeys.length && !next.some(isRegexEntry) && allKeys.every((k) => next.some((entry) => sameKey(entry, k)));
	return isExactlyDiscovered ? ["*"] : next;
}