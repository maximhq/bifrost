// Auth schemes offered as a one-click prefix for "Authorization"-named header
// fields, so callers can paste a bare token instead of typing "Bearer " (or
// "Basic ") themselves every time. Pure text concatenation on a literal
// value — callers decide whether/when it applies (e.g. not to env/vault
// references, which resolve to the header value at request time).
export const AUTH_SCHEMES = ["Bearer", "Basic"];

export function detectAuthScheme(raw: string): string {
	for (const scheme of AUTH_SCHEMES) {
		if (new RegExp(`^${scheme}\\s`, "i").test(raw)) return scheme;
	}
	return "none";
}

export function stripAuthScheme(raw: string): string {
	for (const scheme of AUTH_SCHEMES) {
		const re = new RegExp(`^${scheme}\\s+`, "i");
		if (re.test(raw)) return raw.replace(re, "");
	}
	return raw;
}