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
	// Number.isInteger rejects a fractional month, which would otherwise pass the
	// range check and index MONTH_ABBREVIATIONS between slots.
	const start = startMonth !== undefined && Number.isInteger(startMonth) && startMonth >= 1 && startMonth <= 12 ? startMonth : 1;
	return [0, 1, 2, 3]
		.map((quarter) => {
			const first = (start - 1 + quarter * 3) % 12;
			const last = (first + 2) % 12;
			return `Q${quarter + 1} ${MONTH_ABBREVIATIONS[first]}-${MONTH_ABBREVIATIONS[last]}`;
		})
		.join(" · ");
}

// Month choices for the fiscal quarter start select.
export const quarterStartMonthOptions = MONTH_ABBREVIATIONS.map((_, index) => ({
	label: new Date(Date.UTC(2026, index, 1)).toLocaleString("en-US", { month: "long", timeZone: "UTC" }),
	value: String(index + 1),
}));