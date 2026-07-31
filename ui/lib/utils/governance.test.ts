import { describe, expect, it } from "vitest";
import { getBudgetOverrideValidUntil, getEffectiveBudgetLimit, hasActiveBudgetOverride, validateBudgetOverride } from "./governance";

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