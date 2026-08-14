// Governance-related constants

export const resetDurationOptions = [
	{ label: "每分钟", value: "1m" },
	{ label: "每 5 分钟", value: "5m" },
	{ label: "每 15 分钟", value: "15m" },
	{ label: "每 30 分钟", value: "30m" },
	{ label: "每小时", value: "1h" },
	{ label: "每 6 小时", value: "6h" },
	{ label: "每天", value: "1d" },
	{ label: "每周", value: "1w" },
	{ label: "每月", value: "1M" },
];

// Reset periods offered on budgets. Quarterly is budget-only: resetDurationOptions
// above is shared with the rate-limit selects, and the backend has no notion of a
// quarterly token or request limit, so adding "1Q" there would offer a window it
// cannot enforce.
export const budgetResetDurationOptions = [...resetDurationOptions, { label: "每季度", value: "1Q" }];

// Durations that support calendar-aligned resets (snap to day/week/month/quarter/year boundaries).
// Must stay in sync with IsCalendarAlignableDuration in framework/configstore/tables/utils.go.
// Case matters: "M" is a month while "m" is a minute, so "1q" is not a quarter.
export const supportsCalendarAlignment = (duration: string): boolean => duration.length > 0 && /[dwMQY]$/.test(duration);

// Map of duration values to short labels for display
export const resetDurationLabels: Record<string, string> = {
	"1m": "每分钟",
	"5m": "每 5 分钟",
	"15m": "每 15 分钟",
	"30m": "每 30 分钟",
	"1h": "每小时",
	"6h": "每 6 小时",
	"1d": "每天",
	"1w": "每周",
	"1M": "每月",
	"1Q": "每季度",
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