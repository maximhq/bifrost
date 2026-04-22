export const COMPACT_NUMBER_FORMAT = {
	notation: "compact",
	compactDisplay: "short",
	maximumFractionDigits: 2,
} as const;

export function formatCompactNumber(value: number, maximumFractionDigits = 2): string {
	if (!Number.isFinite(value)) return "0";
	return new Intl.NumberFormat("en-US", {
		...COMPACT_NUMBER_FORMAT,
		maximumFractionDigits,
	}).format(value);
}