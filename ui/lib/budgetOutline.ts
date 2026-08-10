// Shared helpers for the provider-config outline/tree UI (Access Profile edit
// fragment + saved detail view, and the Virtual Key provider card). Keeps the
// compact mono budget labels, allocation-bar math, and the neutral swatch ramp
// consistent across every screen that renders a provider config.

export interface BudgetLineLike {
	max_limit?: number | null;
	reset_duration?: string;
}

// Short, mono-friendly period suffixes (e.g. "$20/wk"). Falls back to the raw
// duration string for anything not in the map.
const SHORT_PERIOD: Record<string, string> = {
	"1m": "min",
	"5m": "5min",
	"15m": "15min",
	"30m": "30min",
	"1h": "hr",
	"6h": "6hr",
	"1d": "day",
	"1w": "wk",
	"1M": "mo",
	"1Q": "qtr",
	"1Y": "yr",
};

export function shortPeriod(duration?: string | null): string {
	if (!duration) return "";
	return SHORT_PERIOD[duration] ?? duration;
}

// Ascending period order (minute → year) for displaying budget lines. Uses the
// SHORT_PERIOD key order; unknown durations sort last.
const PERIOD_ORDER = Object.keys(SHORT_PERIOD);

export function periodRank(duration?: string | null): number {
	const i = duration ? PERIOD_ORDER.indexOf(duration) : -1;
	return i === -1 ? PERIOD_ORDER.length : i;
}

// Compact currency: whole numbers render without decimals ("$20"), fractional
// values keep two places ("$23.48"). Matches the prototype's `money()`.
export function money(value: number | null | undefined): string {
	const v = Number(value) || 0;
	return "$" + (v % 1 ? v.toFixed(2) : String(v));
}

// "$50/wk · $150/mo" — every budget line joined, in ascending period order.
// Returns `fallback` when empty.
export function budgetLinesLabel(budgets: BudgetLineLike[] | undefined, fallback = "No budget"): string {
	if (!budgets || budgets.length === 0) return fallback;
	return [...budgets]
		.sort((a, b) => periodRank(a.reset_duration) - periodRank(b.reset_duration))
		.map((b) => `${money(b.max_limit)}/${shortPeriod(b.reset_duration)}`)
		.join(" · ");
}

// True when two or more budget lines share the same reset period. A budget
// group can only have one cap per reset window — the sheet blocks saving while
// any group violates this.
export function hasDuplicateBudgetPeriods(lines: { reset_duration?: string }[] | undefined): boolean {
	if (!lines || lines.length < 2) return false;
	const seen = new Set<string>();
	for (const l of lines) {
		const key = l.reset_duration ?? "";
		if (seen.has(key)) return true;
		seen.add(key);
	}
	return false;
}

// The provider cap is the first (primary) budget line's amount.
export function providerCap(budgets: BudgetLineLike[] | undefined): number {
	return budgets && budgets.length ? Number(budgets[0].max_limit) || 0 : 0;
}

// A budget group's face value = the largest single line amount (matches the
// prototype's `mAmt`, which takes the max across lines rather than summing).
export function maxLine(budgets: BudgetLineLike[] | undefined): number {
	return (budgets || []).reduce((acc, b) => Math.max(acc, Number(b.max_limit) || 0), 0);
}

// Total allocated to model budgets = Σ per-model face value. Model budgets are
// slices of the provider cap; when this exceeds the cap the UI goes amber.
export function sumModelAllocations(modelBudgets: { budgets?: BudgetLineLike[] }[] | undefined): number {
	return (modelBudgets || []).reduce((acc, mb) => acc + maxLine(mb.budgets), 0);
}

// Neutral zinc ramp for allocation-bar segments and model swatches, matching
// the design's hue order (#e4e4e7 #9f9fa8 #fafafa #71717a #c9c9cf #52525b).
// Index maps 1:1 between a provider's bar segments and its model-budget rows.
export const SWATCH_RAMP = ["bg-zinc-200", "bg-zinc-400", "bg-zinc-50", "bg-zinc-500", "bg-zinc-300", "bg-zinc-600"] as const;

export function swatchClass(index: number): string {
	return SWATCH_RAMP[index % SWATCH_RAMP.length];
}