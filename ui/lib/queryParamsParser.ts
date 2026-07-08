import { createParser, parseAsArrayOf } from "nuqs";

// nuqs's encodeQueryValue skips characters like "/" that TanStack Router's
// navigate({ to }) interprets as path/query delimiters. Full URI-encoding
// the value prevents any special character from breaking navigation.
export const parseAsSafeString = createParser({
	parse: (value: string) => {
		try {
			return decodeURIComponent(value);
		} catch {
			return value;
		}
	},
	serialize: (value: string) => {
		try {
			return encodeURIComponent(value);
		} catch {
			return value;
		}
	},
});

// Comma-separated filter values (models, providers, etc.) with the same
// encoding guarantees as parseAsSafeString.
export const parseAsSafeArrayOf = parseAsArrayOf(parseAsSafeString);