/**
 * Reusable CSV export utilities.
 *
 * Usage:
 *   const csv = buildCSV(headers, rows);
 *   downloadCSV(csv, "my-export");
 */

const FORMULA_PREFIX_REGEX = /^[=+\-@\t\r]/;

/**
 * Escape a cell value for CSV (RFC 4180) and sanitize formula injection.
 *
 * If a string value begins with formula characters (=, +, -, @, tab, CR)
 * and is not a legitimate finite number, it is prefixed with a single quote (')
 * to prevent spreadsheet calculation/execution.
 */
export function escapeCell(value: unknown): string {
	if (value === null || value === undefined) {
		return '""';
	}
	let str = String(value);
	if (
		typeof value === "string" &&
		(FORMULA_PREFIX_REGEX.test(str) || FORMULA_PREFIX_REGEX.test(str.trimStart())) &&
		!Number.isFinite(Number(str))
	) {
		str = `'${str}`;
	}
	return `"${str.replace(/"/g, '""')}"`;
}

/**
 * Build a CSV string from headers and rows.
 *
 * Each row is an array of cell values (string | number | boolean | null | undefined).
 */
export function buildCSV(headers: string[], rows: unknown[][]): string {
	return [headers, ...rows].map((row) => row.map(escapeCell).join(",")).join("\n");
}

/**
 * Trigger a browser download of a CSV string.
 *
 * @param content  The CSV string content
 * @param filename Base filename without extension (date suffix is appended automatically)
 */
export function downloadCSV(content: string, filename: string): void {
	// Prepend UTF-8 BOM (\uFEFF) so Excel and spreadsheet applications correctly interpret UTF-8 encoding
	const blob = new Blob(["\uFEFF", content], { type: "text/csv;charset=utf-8;" });
	const url = URL.createObjectURL(blob);
	const link = document.createElement("a");
	link.href = url;
	const now = new Date();
	const dateStamp = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
	link.download = `${filename}-${dateStamp}.csv`;
	document.body.appendChild(link);
	link.click();
	link.remove();
	setTimeout(() => URL.revokeObjectURL(url), 0);
}