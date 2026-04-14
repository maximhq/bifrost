export function capitalize(name: string) {
	return name.charAt(0).toUpperCase() + name.slice(1);
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