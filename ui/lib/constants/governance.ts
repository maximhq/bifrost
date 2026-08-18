// Governance-related constants

export const resetDurationOptions = [
	{ label: "Every Minute", value: "1m" },
	{ label: "Every 5 Minutes", value: "5m" },
	{ label: "Every 15 Minutes", value: "15m" },
	{ label: "Every 30 Minutes", value: "30m" },
	{ label: "Hourly", value: "1h" },
	{ label: "Every 6 Hours", value: "6h" },
	{ label: "Daily", value: "1d" },
	{ label: "Weekly", value: "1w" },
	{ label: "Monthly", value: "1M" },
];

// Reset periods offered on budgets. Quarterly is budget-only: resetDurationOptions
// above is shared with the rate-limit selects, and the backend has no notion of a
// quarterly token or request limit, so adding "1Q" there would offer a window it
// cannot enforce.
export const budgetResetDurationOptions = [...resetDurationOptions, { label: "Quarterly", value: "1Q" }];

// Durations that support calendar-aligned resets (snap to day/week/month/quarter/year boundaries).
// Must stay in sync with IsCalendarAlignableDuration in framework/configstore/tables/utils.go.
// Case matters: "M" is a month while "m" is a minute, so "1q" is not a quarter.
export const supportsCalendarAlignment = (duration: string): boolean => duration.length > 0 && /[dwMQY]$/.test(duration);

// Map of duration values to short labels for display
export const resetDurationLabels: Record<string, string> = {
	"1m": "Every Minute",
	"5m": "Every 5 Minutes",
	"15m": "Every 15 Minutes",
	"30m": "Every 30 Minutes",
	"1h": "Hourly",
	"6h": "Every 6 Hours",
	"1d": "Daily",
	"1w": "Weekly",
	"1M": "Monthly",
	"1Q": "Quarterly",
};

const MONTH_ABBREVIATIONS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

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
		.map((month, quarter) => `Q${quarter + 1} ${MONTH_ABBREVIATIONS[month.first]}-${MONTH_ABBREVIATIONS[month.last]}`)
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
	// otherwise pass the range check and index MONTH_ABBREVIATIONS between slots.
	if (start === undefined || !Number.isInteger(start) || start === 1 || start < 1 || start > 12) return "";
	return ` · FY starts ${MONTH_ABBREVIATIONS[start - 1]}`;
}

/**
 * The four fiscal quarters as `{label, range}` for the quarter-map chips, e.g.
 * April → `[{label:"Q1", range:"Apr–Jun"}, …]`. Uses an en-dash to match the design.
 */
export function quarterRanges(startMonth?: number): { label: string; range: string }[] {
	return quarterMonthIndices(startMonth).map((month, quarter) => ({
		label: `Q${quarter + 1}`,
		range: `${MONTH_ABBREVIATIONS[month.first]}–${MONTH_ABBREVIATIONS[month.last]}`,
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
export const quarterStartMonthOptions = MONTH_ABBREVIATIONS.map((_, index) => ({
	label: new Date(Date.UTC(2026, index, 1)).toLocaleString("en-US", { month: "long", timeZone: "UTC" }),
	value: String(index + 1),
}));