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

export function formatCurrencyNumber(value: number, maximumFractionDigits = 2): string {
	if (!Number.isFinite(value)) return "$0";
	if (value !== 0 && Math.abs(value) < 0.01) {
		return `$${value.toFixed(4)}`;
	}
	return new Intl.NumberFormat("en-US", {
		...COMPACT_NUMBER_FORMAT,
		style: "currency",
		currency: "USD",
		maximumFractionDigits,
	}).format(value);
}

const TOKEN_PRICE_MULTIPLIER = 1_000_000;
export const DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS = 6;

/**
 * Formats a raw per-token cost into a dollar-denominated per-1M rate string.
 * Uses the "en-US" locale to maintain predictable formatting across host environments.
 *
 * @param cost Raw cost per token (e.g. 7.5e-8 for $0.075 / 1M).
 * @param maximumFractionDigits Maximum decimal places to display (defaults to 6).
 * @returns Formatted dollar price string.
 */
function formatTokenPriceValue(cost: number, maximumFractionDigits = DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS): string {
	return `$${(cost * TOKEN_PRICE_MULTIPLIER).toLocaleString("en-US", {
		minimumFractionDigits: 2,
		maximumFractionDigits,
	})}`;
}

/**
 * Formats a per-token price for compact tabular displays in the model catalog.
 * Returns "—" if the cost is missing or non-finite.
 *
 * @param cost Raw cost per token.
 * @param maximumFractionDigits Maximum decimal places to display (defaults to 6).
 * @returns Formatted compact price string.
 */
export function formatTokenPriceCompact(cost?: number, maximumFractionDigits = DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS): string {
	if (cost === undefined || cost === null || !Number.isFinite(cost)) return "—";
	return formatTokenPriceValue(cost, maximumFractionDigits);
}

/**
 * Formats a per-token price for full label displays (e.g. attribute drawer).
 * Appends " / 1M tokens" and returns "Not available" for missing or non-finite costs.
 *
 * @param cost Raw cost per token.
 * @param maximumFractionDigits Maximum decimal places to display (defaults to 6).
 * @returns Formatted full token price string.
 */
export function formatTokenPriceFull(cost?: number, maximumFractionDigits = DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS): string {
	if (cost === undefined || cost === null || !Number.isFinite(cost)) return "Not available";
	return `${formatTokenPriceValue(cost, maximumFractionDigits)} / 1M tokens`;
}

/**
 * Formats a per-character price for full label displays (e.g. attribute drawer).
 * Appends " / 1M characters" and returns "Not available" for missing or non-finite costs.
 *
 * @param cost Raw cost per character.
 * @param maximumFractionDigits Maximum decimal places to display (defaults to 6).
 * @returns Formatted full character price string.
 */
export function formatCharacterPriceFull(cost?: number, maximumFractionDigits = DEFAULT_TOKEN_PRICE_MAX_FRACTION_DIGITS): string {
	if (cost === undefined || cost === null || !Number.isFinite(cost)) return "Not available";
	return `${formatTokenPriceValue(cost, maximumFractionDigits)} / 1M characters`;
}