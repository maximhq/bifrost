/**
 * Reusable CSV export utilities.
 *
 * Usage:
 *   const csv = buildCSV(headers, rows);
 *   downloadCSV(csv, "my-export");
 */

/** Escape a cell value for CSV (RFC 4180). */
function escapeCell(value: unknown): string {
	const str = String(value ?? "");
	return `"${str.replace(/"/g, '""')}"`;
}

/**
 * Build a CSV string from headers and rows.
 *
 * Each row is an array of cell values (string | number | boolean | null | undefined).
 */
export function buildCSV(headers: string[], rows: unknown[][]): string {
	return [headers, ...rows]
		.map((row) => row.map(escapeCell).join(","))
		.join("\n");
}

/**
 * Trigger a browser download of a CSV string.
 *
 * @param content  The CSV string content
 * @param filename Base filename without extension (date suffix is appended automatically)
 */
export function downloadCSV(content: string, filename: string): void {
	const blob = new Blob([content], { type: "text/csv;charset=utf-8;" });
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
