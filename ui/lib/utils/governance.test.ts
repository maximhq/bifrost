import { describe, expect, it } from "vitest";
import { getBudgetOverrideValidUntil, getEffectiveBudgetLimit, getModelRateLimitRules, hasActiveBudgetOverride, validateBudgetOverride } from "./governance";

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

describe("getModelRateLimitRules", () => {
	it("normalizes every single-metric model rule without truncating windows", () => {
		const rules = getModelRateLimitRules({
			id: "model-config",
			model_name: "gemini-2.5-flash",
			rate_limits: [
				{ id: "rpm", metric: "requests", request_max_limit: 15, request_reset_duration: "1m", request_current_usage: 3 } as never,
				{ id: "rpd", metric: "requests", request_max_limit: 1500, request_reset_duration: "1d", request_current_usage: 81 } as never,
				{ id: "tpm", metric: "tokens", token_max_limit: 1000, token_reset_duration: "1m", token_current_usage: 20 } as never,
				{ id: "tpd", metric: "tokens", token_max_limit: 10000, token_reset_duration: "1d", token_current_usage: 800 } as never,
			],
		} as never);

		expect(rules).toEqual([
			{ id: "rpm", metric: "requests", max_limit: 15, reset_duration: "1m", current_usage: 3 },
			{ id: "rpd", metric: "requests", max_limit: 1500, reset_duration: "1d", current_usage: 81 },
			{ id: "tpm", metric: "tokens", max_limit: 1000, reset_duration: "1m", current_usage: 20 },
			{ id: "tpd", metric: "tokens", max_limit: 10000, reset_duration: "1d", current_usage: 800 },
		]);
	});

	it("converts a legacy paired row into two editable rules", () => {
		const rules = getModelRateLimitRules({
			id: "legacy-model",
			model_name: "gpt-4o",
			rate_limit: {
				id: "legacy",
				token_max_limit: 1000,
				token_reset_duration: "1m",
				token_current_usage: 10,
				token_last_reset: "",
				request_max_limit: 15,
				request_reset_duration: "1d",
				request_current_usage: 2,
				request_last_reset: "",
			} as never,
		} as never);

		expect(rules).toEqual([
			{ metric: "tokens", max_limit: 1000, reset_duration: "1m", current_usage: 10 },
			{ metric: "requests", max_limit: 15, reset_duration: "1d", current_usage: 2 },
		]);
	});

	it("returns no rules for an empty or absent model config", () => {
		expect(getModelRateLimitRules(undefined)).toEqual([]);
		expect(getModelRateLimitRules({ id: "empty", model_name: "gpt-4o" } as never)).toEqual([]);
	});
});
