import { describe, expect, it } from "vitest";
import { periodRank, shortPeriod } from "@/lib/budgetOutline";
import {
	budgetResetDurationOptions,
	formatQuarterPreview,
	resetDurationLabels,
	resetDurationOptions,
	supportsCalendarAlignment,
} from "@/lib/constants/governance";
import {
	budgetSignature,
	getBudgetOverrideValidUntil,
	getEffectiveBudgetLimit,
	hasActiveBudgetOverride,
	parseResetPeriod,
	validateBudgetOverride,
} from "./governance";

describe("budget overrides", () => {
	it("adds active finite and permanent overrides to the base limit", () => {
		expect(getEffectiveBudgetLimit({ max_limit: 100, override_amount: 25, override_mode: "cycles", override_cycles_remaining: 2 })).toBe(
			125,
		);
		expect(getEffectiveBudgetLimit({ max_limit: 100, override_amount: 50, override_mode: "forever" })).toBe(150);
	});

	it("ignores incomplete or expired override state", () => {
		expect(hasActiveBudgetOverride({ max_limit: 100, override_amount: 25, override_mode: "cycles", override_cycles_remaining: 0 })).toBe(
			false,
		);
		expect(getEffectiveBudgetLimit({ max_limit: 100, override_amount: 25, override_mode: "cycles", override_cycles_remaining: 0 })).toBe(
			100,
		);
	});

	it("validates positive amounts and whole finite cycle counts", () => {
		expect(validateBudgetOverride(0, "forever", 0)).toMatch(/greater than 0/);
		expect(validateBudgetOverride(25, "cycles", 1.5)).toMatch(/whole number/);
		expect(validateBudgetOverride(25, "cycles", 1)).toBeNull();
		expect(validateBudgetOverride(25, "forever", 0)).toBeNull();
	});

	it("calculates the validity date from the current reset schedule", () => {
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-07-01T00:00:00.000Z", reset_duration: "1d" }, 4)?.toISOString(),
		).toBe("2026-07-05T00:00:00.000Z");
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-01-01T00:00:00.000Z", reset_duration: "1M" }, 2, true)?.toISOString(),
		).toBe("2026-03-01T00:00:00.000Z");
	});
});
describe("quarterly budgets", () => {
	it("offers Quarterly on budgets but not on rate limits", () => {
		// resetDurationOptions is shared by budget and rate-limit selects. A
		// quarterly token limit is not a thing the backend can enforce, so the
		// option has to live on a budget-only list.
		expect(budgetResetDurationOptions.map((o) => o.value)).toContain("1Q");
		expect(resetDurationOptions.map((o) => o.value)).not.toContain("1Q");
	});

	it("keeps the budget list ordered with Quarterly after Monthly", () => {
		const values = budgetResetDurationOptions.map((o) => o.value);
		expect(values.indexOf("1Q")).toBeGreaterThan(values.indexOf("1M"));
		expect(resetDurationLabels["1Q"]).toBe("Quarterly");
	});

	it("treats a quarter as calendar-alignable", () => {
		expect(supportsCalendarAlignment("1Q")).toBe(true);
		// Case matters: "M" is a month and "m" is a minute, so a lowercase typo
		// must not be mistaken for a quarter. Mirrors IsQuarterlyDuration in Go.
		expect(supportsCalendarAlignment("1q")).toBe(false);
		expect(supportsCalendarAlignment("1h")).toBe(false);
	});

	it("renders a quarter in human readable and compact forms", () => {
		expect(parseResetPeriod("1Q")).toBe("1 quarter");
		expect(parseResetPeriod("2Q")).toBe("2 quarters");
		expect(shortPeriod("1Q")).toBe("qtr");
		expect(periodRank("1Q")).toBeGreaterThan(periodRank("1M"));
		expect(periodRank("1Q")).toBeLessThan(periodRank("1Y"));
	});

	it("expires a quarterly override three months per cycle when calendar aligned", () => {
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-01-01T00:00:00.000Z", reset_duration: "1Q" }, 2, true)?.toISOString(),
		).toBe("2026-07-01T00:00:00.000Z");

		// A rolling quarter is 90 days per cycle, matching ParseDuration in Go.
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-01-01T00:00:00.000Z", reset_duration: "1Q" }, 1)?.toISOString(),
		).toBe("2026-04-01T00:00:00.000Z");
	});
});

describe("fiscal quarter preview", () => {
	it("labels the four quarters from the configured start month", () => {
		expect(formatQuarterPreview(4)).toBe("Q1 Apr-Jun · Q2 Jul-Sep · Q3 Oct-Dec · Q4 Jan-Mar");
		expect(formatQuarterPreview(1)).toBe("Q1 Jan-Mar · Q2 Apr-Jun · Q3 Jul-Sep · Q4 Oct-Dec");
		expect(formatQuarterPreview(10)).toBe("Q1 Oct-Dec · Q2 Jan-Mar · Q3 Apr-Jun · Q4 Jul-Sep");
	});

	it("handles a start month that straddles the year end", () => {
		expect(formatQuarterPreview(11)).toBe("Q1 Nov-Jan · Q2 Feb-Apr · Q3 May-Jul · Q4 Aug-Oct");
	});

	it("falls back to January for an absent or out-of-range month", () => {
		const january = formatQuarterPreview(1);
		expect(formatQuarterPreview(undefined)).toBe(january);
		expect(formatQuarterPreview(0)).toBe(january);
		expect(formatQuarterPreview(13)).toBe(january);
	});

	// A fractional month sits inside the 1-12 range but indexes the abbreviation
	// table between slots, so without an integer check the preview renders
	// "Q1 undefined-undefined" rather than falling back.
	it("falls back to January for a non-integer month", () => {
		expect(formatQuarterPreview(1.5)).toBe(formatQuarterPreview(1));
	});
});
describe("budgetSignature", () => {
	const quarterly = (quarterStartMonth?: number) => [
		{
			id: "b-1",
			max_limit: 500,
			reset_duration: "1Q",
			...(quarterStartMonth === undefined ? {} : { reset_config: { quarter_start_month: quarterStartMonth } }),
		},
	];

	// The reset-warning flow only runs when the signature changes, so a quarter-only
	// edit that hashes identically submits with no reset/preserve prompt at all.
	it("changes when only the fiscal quarter start moves", () => {
		expect(budgetSignature(quarterly(4))).not.toBe(budgetSignature(quarterly(7)));
	});

	it("treats an absent quarter start and an explicit January as the same window", () => {
		expect(budgetSignature(quarterly(undefined))).toBe(budgetSignature(quarterly(1)));
	});

	it("ignores the quarter start for non-quarterly durations", () => {
		const monthly = (quarterStartMonth: number) => [
			{ id: "b-1", max_limit: 500, reset_duration: "1M", reset_config: { quarter_start_month: quarterStartMonth } },
		];
		expect(budgetSignature(monthly(4))).toBe(budgetSignature(monthly(7)));
	});

	it("still tracks limit and duration edits", () => {
		expect(budgetSignature(quarterly(4))).not.toBe(
			budgetSignature([{ id: "b-1", max_limit: 900, reset_duration: "1Q", reset_config: { quarter_start_month: 4 } }]),
		);
		expect(budgetSignature(quarterly(4))).not.toBe(
			budgetSignature([{ id: "b-1", max_limit: 500, reset_duration: "1M", reset_config: { quarter_start_month: 4 } }]),
		);
	});

	it("skips budgets with no limit set", () => {
		expect(budgetSignature([{ id: "b-1", reset_duration: "1Q" }])).toBe("");
	});
});
// The owner sheets compare budgets without ids: form rows have no id while
// persisted rows do, so feeding ids in would make every save look like a change.
describe("budgetSignature without ids", () => {
	const row = (quarterStartMonth?: number) => [
		{
			max_limit: 500,
			reset_duration: "1Q",
			...(quarterStartMonth === undefined ? {} : { reset_config: { quarter_start_month: quarterStartMonth } }),
		},
	];

	it("detects a fiscal quarter change on an otherwise identical budget", () => {
		expect(budgetSignature(row(4))).not.toBe(budgetSignature(row(7)));
	});

	it("reports no change when only ids differ", () => {
		const persisted = [{ id: "b-1", max_limit: 500, reset_duration: "1Q", reset_config: { quarter_start_month: 4 } }];
		const stripped = persisted.map((b) => ({
			max_limit: b.max_limit,
			reset_duration: b.reset_duration,
			reset_config: b.reset_config,
		}));
		expect(budgetSignature(stripped)).toBe(budgetSignature(row(4)));
	});
});