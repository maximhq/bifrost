import { describe, expect, it } from "vitest";
import { shouldClearModelOnProviderChange } from "./modelLimitSheet.utils";

describe("shouldClearModelOnProviderChange", () => {
	it("keeps the all-models wildcard, which no catalog lookup can confirm", () => {
		expect(shouldClearModelOnProviderChange("*", ["gpt-4o", "gpt-4o-mini"])).toBe(false);
	});

	it("keeps the wildcard even when the new provider returns nothing", () => {
		expect(shouldClearModelOnProviderChange("*", [])).toBe(false);
	});

	it("keeps a model the new provider still offers", () => {
		expect(shouldClearModelOnProviderChange("gpt-4o", ["gpt-4o", "gpt-4o-mini"])).toBe(false);
	});

	it("clears a model the new provider does not offer", () => {
		expect(shouldClearModelOnProviderChange("gpt-4o", ["claude-opus-4"])).toBe(true);
	});

	it("keeps an empty field untouched", () => {
		expect(shouldClearModelOnProviderChange("", [])).toBe(false);
	});
});