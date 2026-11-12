import { describe, expect, it } from "vitest";
import type { Budget, LimitScope, ModelConfig, RateLimit } from "@/lib/types/governance";
import { budgetPeriodKey, hasDuplicateBudgetPeriods, periodRank, shortPeriod } from "@/lib/budgetOutline";
import {
	budgetResetDurationOptions,
	currentQuarterIndex,
	fiscalQuarterNote,
	formatQuarterPreview,
	nextQuarterReset,
	quarterRanges,
	resetDurationLabels,
	resetDurationOptions,
	supportsCalendarAlignment,
} from "@/lib/constants/governance";
import {
	actionDisabledReason,
	budgetScopeChargedBy,
	budgetSignature,
	getBudgetOverrideValidUntil,
	getEffectiveBudgetLimit,
	hasActiveBudgetOverride,
	isHolderLevelModelConfig,
	modelConfigAppliesToMCP,
	isRequestLimitExhausted,
	limitsLeftAfterRemoving,
	parseResetPeriod,
	requestLimitLinesOf,
	requestLimitsOf,
	toRequestLimitRequests,
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

	it("anchors calendar-aligned validity to the current period boundary", () => {
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-08-03T08:59:52.077Z", reset_duration: "1M" }, 1, true)?.toISOString(),
		).toBe("2026-09-01T00:00:00.000Z");
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-08-05T08:59:52.077Z", reset_duration: "1w" }, 1, true)?.toISOString(),
		).toBe("2026-08-10T00:00:00.000Z");
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-08-03T08:59:52.077Z", reset_duration: "1d" }, 1, true)?.toISOString(),
		).toBe("2026-08-04T00:00:00.000Z");
		expect(
			getBudgetOverrideValidUntil({ max_limit: 100, last_reset: "2026-08-03T08:59:52.077Z", reset_duration: "1Y" }, 1, true)?.toISOString(),
		).toBe("2027-01-01T00:00:00.000Z");
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
describe("fiscalQuarterNote", () => {
	it("names the fiscal start only for a non-January quarterly budget", () => {
		expect(fiscalQuarterNote("1Q", { quarter_start_month: 4 })).toBe(" · FY starts Apr");
		expect(fiscalQuarterNote("1Q", { quarter_start_month: 10 })).toBe(" · FY starts Oct");
	});

	it("is empty for a January or unset start (the default)", () => {
		expect(fiscalQuarterNote("1Q", { quarter_start_month: 1 })).toBe("");
		expect(fiscalQuarterNote("1Q", {})).toBe("");
		expect(fiscalQuarterNote("1Q", undefined)).toBe("");
	});

	it("is empty for a non-quarterly duration regardless of config", () => {
		expect(fiscalQuarterNote("1M", { quarter_start_month: 4 })).toBe("");
		expect(fiscalQuarterNote(undefined, { quarter_start_month: 4 })).toBe("");
	});

	it("is empty for an out-of-range or non-integer month", () => {
		expect(fiscalQuarterNote("1Q", { quarter_start_month: 0 })).toBe("");
		expect(fiscalQuarterNote("1Q", { quarter_start_month: 13 })).toBe("");
		// A fractional month sits in range but indexes MONTH_ABBREVIATIONS between
		// slots, which would render "FY starts undefined" without the integer guard.
		expect(fiscalQuarterNote("1Q", { quarter_start_month: 2.5 })).toBe("");
	});
});
describe("quarter map helpers", () => {
	it("labels the four quarters with en-dash ranges from the start month", () => {
		expect(quarterRanges(4)).toEqual([
			{ label: "Q1", range: "Apr–Jun" },
			{ label: "Q2", range: "Jul–Sep" },
			{ label: "Q3", range: "Oct–Dec" },
			{ label: "Q4", range: "Jan–Mar" },
		]);
	});

	it("falls back to January for a missing or invalid start month", () => {
		expect(quarterRanges(undefined)).toEqual(quarterRanges(1));
		expect(quarterRanges(2.5)).toEqual(quarterRanges(1));
		expect(quarterRanges(13)).toEqual(quarterRanges(1));
	});

	it("finds the quarter containing a date for a non-January fiscal year", () => {
		// April fiscal year: Q1 Apr–Jun, Q2 Jul–Sep, Q3 Oct–Dec, Q4 Jan–Mar.
		expect(currentQuarterIndex(4, new Date(2026, 3, 15))).toBe(0); // April
		expect(currentQuarterIndex(4, new Date(2026, 8, 1))).toBe(1); // September
		expect(currentQuarterIndex(4, new Date(2026, 0, 31))).toBe(3); // January
	});

	it("returns the first day of the next quarter strictly after now", () => {
		// April fiscal year, mid-May → next boundary is Jul 1.
		expect(nextQuarterReset(4, new Date(2026, 4, 10)).getTime()).toBe(new Date(2026, 6, 1).getTime());
		// On a boundary day, the next reset is the following quarter, not today.
		expect(nextQuarterReset(4, new Date(2026, 3, 1)).getTime()).toBe(new Date(2026, 6, 1).getTime());
		// A late-starting fiscal year wraps across the calendar year end.
		expect(nextQuarterReset(11, new Date(2026, 11, 5)).getTime()).toBe(new Date(2027, 1, 1).getTime());
	});
});
describe("budget period identity", () => {
	it("treats a missing scope as the shared default", () => {
		expect(budgetPeriodKey("1d", undefined)).toBe(budgetPeriodKey("1d", "shared"));
		expect(budgetPeriodKey("1d", "mcp")).not.toBe(budgetPeriodKey("1d", "shared"));
	});

	// One reset period may be used once per scope: Shared + MCP over a day is fine, two MCP days are not.
	it("flags a duplicate only when period and scope shared repeat", () => {
		expect(hasDuplicateBudgetPeriods([{ reset_duration: "1d" }, { reset_duration: "1d", scope: "mcp" }])).toBe(false);
		expect(
			hasDuplicateBudgetPeriods([
				{ reset_duration: "1d", scope: "model" },
				{ reset_duration: "1d", scope: "mcp" },
			]),
		).toBe(false);
		expect(
			hasDuplicateBudgetPeriods([
				{ reset_duration: "1d", scope: "mcp" },
				{ reset_duration: "1d", scope: "mcp" },
			]),
		).toBe(true);
		expect(hasDuplicateBudgetPeriods([{ reset_duration: "1d" }, { reset_duration: "1d", scope: "shared" }])).toBe(true);
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

	it("changes when only the scope changes, and keeps the pre-scope signature for the default", () => {
		const daily = (scope?: string) => [{ id: "b-1", max_limit: 500, reset_duration: "1d", ...(scope ? { scope } : {}) }];
		expect(budgetSignature(daily("mcp"))).not.toBe(budgetSignature(daily()));
		expect(budgetSignature(daily("mcp"))).not.toBe(budgetSignature(daily("model")));
		expect(budgetSignature(daily("shared"))).toBe(budgetSignature(daily()));
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

describe("request limits", () => {
	const row = (fields: Partial<RateLimit>): RateLimit => ({
		id: "rl",
		token_current_usage: 0,
		token_last_reset: "",
		request_current_usage: 0,
		request_last_reset: "",
		...fields,
	});

	it("lists a legacy combined rate limit's request limit before the holder's rules", () => {
		const legacy = row({ id: "legacy", token_max_limit: 100, request_max_limit: 10, request_reset_duration: "1h" });
		const rule = row({ id: "r1", request_max_limit: 5, request_reset_duration: "1d", scope: "mcp" });
		expect(requestLimitsOf({ rate_limit: legacy, request_limits: [rule] })).toEqual([legacy, rule]);
		expect(requestLimitsOf({ rate_limit: row({ token_max_limit: 100 }), request_limits: [rule] })).toEqual([rule]);
		expect(requestLimitsOf(undefined)).toEqual([]);
	});

	it("edits a legacy request limit as one shared line without an id", () => {
		const legacy = row({ id: "legacy", request_max_limit: 10, request_reset_duration: "1h" });
		const rule = row({ id: "r1", request_max_limit: 5, request_reset_duration: "1d", scope: "mcp" });
		expect(requestLimitLinesOf({ rate_limit: legacy, request_limits: [rule] })).toEqual([
			{ id: undefined, max_limit: 10, reset_duration: "1h", scope: undefined },
			{ id: "r1", max_limit: 5, reset_duration: "1d", scope: "mcp" },
		]);
	});

	it("drops empty lines from the payload and keeps each line's scope", () => {
		const lines = [
			{ id: "r1", max_limit: 5, reset_duration: "1h", scope: "model" as const },
			{ max_limit: undefined, reset_duration: "1d" },
			{ max_limit: 7, reset_duration: "1d" },
		];
		expect(toRequestLimitRequests(lines)).toEqual([
			{ id: "r1", max_limit: 5, reset_duration: "1h", scope: "model" },
			{ max_limit: 7, reset_duration: "1d" },
		]);
		expect(toRequestLimitRequests(undefined)).toEqual([]);
	});

	it("treats a rule as exhausted once usage reaches its maximum", () => {
		expect(isRequestLimitExhausted({ request_max_limit: 5, request_current_usage: 5 })).toBe(true);
		expect(isRequestLimitExhausted({ request_max_limit: 5, request_current_usage: 4 })).toBe(false);
		expect(isRequestLimitExhausted({ request_current_usage: 9 })).toBe(false);
	});
});

describe("actionDisabledReason", () => {
	it("is undefined when the action is allowed", () => {
		expect(actionDisabledReason(true, "delete", "teams")).toBeUndefined();
	});

	it("names the action and the entity when permission is missing", () => {
		expect(actionDisabledReason(false, "delete", "teams")).toBe("You don't have permission to delete teams.");
		expect(actionDisabledReason(false, "create", "virtual keys")).toBe("You don't have permission to create virtual keys.");
	});

	it("prefers a non-permission reason, even when the permission is also missing", () => {
		const managed = "This virtual key is managed by an access profile and can't be deleted here.";
		expect(actionDisabledReason(false, "delete", "virtual keys", managed)).toBe(managed);
		expect(actionDisabledReason(true, "delete", "virtual keys", managed)).toBe(managed);
	});
});
// Multi-unit overrides must preview the rolling schedule used by the backend.
describe("multi-unit override expiry", () => {
	it.each(["7d", "2w", "3M", "2Q", "2Y"])("uses rolling expiry for %s despite calendar alignment", (duration) => {
		const budget = { max_limit: 100, last_reset: "2026-03-10T12:30:00Z", reset_duration: duration };
		expect(getBudgetOverrideValidUntil(budget, 2, true)).toEqual(getBudgetOverrideValidUntil(budget, 2, false));
	});
});
describe("holder-level limits", () => {
	const budget = (id: string, scope?: LimitScope): Budget => ({
		id,
		max_limit: 1,
		reset_duration: "1d",
		scope,
		current_usage: 0,
		last_reset: "",
	});
	const row = (budgets: Budget[], extra: Partial<ModelConfig> = {}): ModelConfig =>
		({ id: "mc", model_name: "*", budgets, created_at: "", updated_at: "", ...extra }) as ModelConfig;

	it("charges Shared lines, and unset scopes, to both kinds of traffic", () => {
		expect(budgetScopeChargedBy(undefined, "mcp")).toBe(true);
		expect(budgetScopeChargedBy("shared", "model")).toBe(true);
		expect(budgetScopeChargedBy("mcp", "mcp")).toBe(true);
		expect(budgetScopeChargedBy("mcp", "model")).toBe(false);
		expect(budgetScopeChargedBy("model", "mcp")).toBe(false);
	});

	it("knows the holder-level row from narrower ones", () => {
		expect(isHolderLevelModelConfig({ model_name: "*" })).toBe(true);
		expect(isHolderLevelModelConfig({ model_name: "*", provider: "openai" })).toBe(false);
		expect(isHolderLevelModelConfig({ model_name: "gpt-4o" })).toBe(false);
	});

	it("lists a row on the MCP page only for a Shared or MCP line on the holder-level row", () => {
		expect(modelConfigAppliesToMCP(row([budget("a", "model"), budget("b", "mcp")]))).toBe(true);
		expect(modelConfigAppliesToMCP(row([budget("a")]))).toBe(true);
		expect(modelConfigAppliesToMCP(row([budget("a", "model")]))).toBe(false);
		expect(modelConfigAppliesToMCP(row([budget("a", "shared")], { provider: "openai" }))).toBe(false);
		const rule = (scope: LimitScope) => ({ id: scope, scope, request_max_limit: 1 }) as RateLimit;
		expect(modelConfigAppliesToMCP(row([], { request_limits: [rule("mcp")] }))).toBe(true);
		expect(modelConfigAppliesToMCP(row([], { request_limits: [rule("model")] }))).toBe(false);
	});

	it("leaves the other page's lines when one page removes its limits", () => {
		const rule = (id: string, scope?: LimitScope) => ({ id, scope, request_max_limit: 1 }) as RateLimit;
		const mixed = row([budget("shared", "shared"), budget("model", "model"), budget("mcp", "mcp")], {
			request_limits: [rule("shared-rule"), rule("model-rule", "model"), rule("mcp-rule", "mcp")],
		});
		const fromMCP = limitsLeftAfterRemoving(mixed, "mcp");
		expect(fromMCP?.budgets.map((b) => b.id)).toEqual(["model"]);
		expect(fromMCP?.requestLimits.map((r) => r.id)).toEqual(["model-rule"]);
		const fromModels = limitsLeftAfterRemoving(mixed, "model");
		expect(fromModels?.budgets.map((b) => b.id)).toEqual(["mcp"]);
		expect(fromModels?.requestLimits.map((r) => r.id)).toEqual(["mcp-rule"]);
		expect(limitsLeftAfterRemoving(row([], { request_limits: [rule("mcp-rule", "mcp")] }), "model")?.budgets).toEqual([]);
	});

	it("deletes the row when nothing of the other page is left", () => {
		expect(limitsLeftAfterRemoving(row([budget("shared"), budget("mcp", "mcp")]), "mcp")).toBeNull();
		expect(limitsLeftAfterRemoving(row([budget("shared"), budget("model", "model")]), "model")).toBeNull();
		expect(limitsLeftAfterRemoving(row([budget("gpt", "shared")], { model_name: "gpt-4o" }), "model")).toBeNull();
	});

	it("keeps the row for the token limit the Models page edits", () => {
		const withTokenLimit = row([budget("mcp", "mcp")], { rate_limit: { id: "rl", token_max_limit: 10 } as ModelConfig["rate_limit"] });
		expect(limitsLeftAfterRemoving(withTokenLimit, "mcp")).toEqual({ budgets: [], requestLimits: [] });
	});
});