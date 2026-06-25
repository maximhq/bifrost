import { createParser } from "nuqs";

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