// Governance-related constants
import i18n, { getLocale } from "@/lib/i18n";

const RESET_DURATION_VALUES = ["1m", "5m", "15m", "30m", "1h", "6h", "1d", "1w", "1M"] as const;
type ResetDurationValue = (typeof RESET_DURATION_VALUES)[number] | "1Q";

// Labels resolve through i18n at read time (getters) so these shared constants stay
// drop-in for every existing caller while following the active locale.
const resetDurationLabel = (value: ResetDurationValue): string => i18n.t(`virtualKeys.resetDurations.${value}`, { ns: "models" });
const resetDurationOption = (value: ResetDurationValue) => ({
	get label(): string {
		return resetDurationLabel(value);
	},
	value: value as string,
});

export const resetDurationOptions = RESET_DURATION_VALUES.map(resetDurationOption);

// Reset periods offered on budgets. Quarterly is budget-only: resetDurationOptions
// above is shared with the rate-limit selects, and the backend has no notion of a
// quarterly token or request limit, so adding "1Q" there would offer a window it
// cannot enforce.
export const budgetResetDurationOptions = [...resetDurationOptions, resetDurationOption("1Q")];

// Durations that support calendar-aligned resets (snap to day/week/month/quarter/year boundaries).
// Must stay in sync with IsCalendarAlignableDuration in framework/configstore/tables/utils.go.
// Case matters: "M" is a month while "m" is a minute, so "1q" is not a quarter.
export const supportsCalendarAlignment = (duration: string): boolean => /^(1d|1w|1M|1Q|1Y)$/.test(duration);

// Map of duration values to short labels for display
export const resetDurationLabels: Record<string, string> = Object.defineProperties(
	{},
	Object.fromEntries(
		[...RESET_DURATION_VALUES, "1Q" as const].map((value) => [value, { get: () => resetDurationLabel(value), enumerable: true }]),
	),
);

// Month name in the active locale; "short" gives "Apr" in English.
const monthName = (index: number, month: "short" | "long"): string =>
	new Date(Date.UTC(2026, index, 1)).toLocaleString(getLocale(), { month, timeZone: "UTC" });

// Fall back to January for a missing, fractional, or out-of-range month, matching
// BudgetResetConfig.QuarterStart on the Go side. Number.isInteger also rejects a
// fractional month, which would otherwise index the month tables between slots.
function normalizeQuarterStart(startMonth?: number): number {
	return startMonth !== undefined && Number.isInteger(startMonth) && startMonth >= 1 && startMonth <= 12 ? startMonth : 1;
}

// Month indices (0-11) for the four fiscal quarters — the single source of the
// modulo-3 boundary math the preview string, chip ranges, and reset date all build on.
function quarterMonthIndices(startMonth?: number): { first: number; last: number }[] {
	const start = normalizeQuarterStart(startMonth);
	return [0, 1, 2, 3].map((quarter) => {
		const first = (start - 1 + quarter * 3) % 12;
		return { first, last: (first + 2) % 12 };
	});
}

/**
 * Renders the four fiscal quarters implied by a start month, e.g. April gives
 * "Q1 Apr-Jun · Q2 Jul-Sep · Q3 Oct-Dec · Q4 Jan-Mar".
 *
 * This preview is the setting's main affordance, not decoration. Quarter
 * boundaries repeat every three months, so the start month only changes reset
 * dates modulo 3: January, April, July and October all reset on the same days.
 * An operator picking "April" for a UK or Indian fiscal year would otherwise see
 * no change anywhere in the UI and reasonably conclude the setting is broken.
 * The preview shows what actually differs, which is the Q1-Q4 labelling.
 *
 * Out-of-range or missing months fall back to January, matching
 * BudgetResetConfig.QuarterStart on the Go side.
 */
export function formatQuarterPreview(startMonth?: number): string {
	return quarterMonthIndices(startMonth)
		.map((month, quarter) => `Q${quarter + 1} ${monthName(month.first, "short")}-${monthName(month.last, "short")}`)
		.join(" · ");
}

/**
 * Compact read-only note naming a quarterly budget's fiscal-year start, e.g.
 * " · FY starts Apr". Returns "" for non-quarterly budgets and for a January /
 * unset start (the default), so it only ever appears when it changes behaviour.
 * Callers append it after the reset-period label (which already reads "Quarterly").
 */
export function fiscalQuarterNote(resetDuration?: string, resetConfig?: { quarter_start_month?: number } | null): string {
	if (!resetDuration || !resetDuration.endsWith("Q")) return "";
	const start = resetConfig?.quarter_start_month;
	// Number.isInteger also rejects undefined/NaN; a fractional month like 2.5 would
	// otherwise pass the range check and index the month names between slots.
	if (start === undefined || !Number.isInteger(start) || start === 1 || start < 1 || start > 12) return "";
	return ` · ${i18n.t("virtualKeys.fiscalYearStarts", { ns: "models", month: monthName(start - 1, "short") })}`;
}

/**
 * The four fiscal quarters as `{label, range}` for the quarter-map chips, e.g.
 * April → `[{label:"Q1", range:"Apr–Jun"}, …]`. Uses an en-dash to match the design.
 */
export function quarterRanges(startMonth?: number): { label: string; range: string }[] {
	return quarterMonthIndices(startMonth).map((month, quarter) => ({
		label: `Q${quarter + 1}`,
		range: `${monthName(month.first, "short")}–${monthName(month.last, "short")}`,
	}));
}

/** Index (0-3) of the fiscal quarter containing `now`, for the current-quarter highlight. */
export function currentQuarterIndex(startMonth?: number, now: Date = new Date()): number {
	const start = normalizeQuarterStart(startMonth);
	return Math.floor(((now.getMonth() - (start - 1) + 12) % 12) / 3);
}

/**
 * First day of the next fiscal quarter strictly after `now`, for a fiscal year
 * starting `startMonth` (1-12). Display-only — the authoritative reset schedule
 * lives in BudgetResetConfig.QuarterStart on the Go side.
 */
export function nextQuarterReset(startMonth?: number, now: Date = new Date()): Date {
	const start = normalizeQuarterStart(startMonth);
	const candidates: Date[] = [];
	for (let year = now.getFullYear() - 1; year <= now.getFullYear() + 1; year++) {
		for (let quarter = 0; quarter < 4; quarter++) {
			// A month index past 11 rolls into the next year, which is exactly the
			// wrap we want for fiscal years that start late in the calendar year.
			candidates.push(new Date(year, start - 1 + quarter * 3, 1));
		}
	}
	return candidates.filter((date) => date > now).sort((a, b) => a.getTime() - b.getTime())[0];
}

// Month choices for the fiscal quarter start select.
export const quarterStartMonthOptions = Array.from({ length: 12 }, (_, index) => ({
	get label(): string {
		return monthName(index, "long");
	},
	value: String(index + 1),
}));
// Multi-unit windows remain rolling even when their owner enables alignment.
export const isMultiUnitDuration = (duration: string): boolean => /^\d+[smhdwMQY]$/.test(duration) && Number(duration.slice(0, -1)) > 1;

export interface CalendarWindowInput {
	budgets?: { max_limit?: number; reset_duration?: string }[];
	rate_limit?: {
		token_max_limit?: number;
		token_reset_duration?: string;
		request_max_limit?: number;
		request_reset_duration?: string;
	} | null;
	model_budgets?: CalendarWindowInput[];
}

export function calendarWindowDurations(windows: CalendarWindowInput[]): string[] {
	return windows.flatMap((window) => [
		...(window.budgets ?? []).filter((b) => b.max_limit !== undefined).map((b) => b.reset_duration ?? ""),
		...(window.rate_limit?.token_max_limit !== undefined ? [window.rate_limit.token_reset_duration ?? ""] : []),
		...(window.rate_limit?.request_max_limit !== undefined ? [window.rate_limit.request_reset_duration ?? ""] : []),
		...calendarWindowDurations(window.model_budgets ?? []),
	]);
}