import { describe, expect, it } from "vitest";
import { hasMCPBudgetOnNarrowLimit, saveHasNoLimit, shouldClearModelOnProviderChange } from "./modelLimitSheet.utils";

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
describe("hasMCPBudgetOnNarrowLimit", () => {
	const mcp = { scope: "mcp", max_limit: 5 };
	it("never objects on a limit for all models on all providers", () => {
		expect(hasMCPBudgetOnNarrowLimit("*", undefined, [mcp])).toBe(false);
		expect(hasMCPBudgetOnNarrowLimit("*", "", [mcp])).toBe(false);
	});
	it("objects to a submitted MCP budget once the limit is narrowed to a model or a provider", () => {
		expect(hasMCPBudgetOnNarrowLimit("gpt-4o", undefined, [mcp])).toBe(true);
		expect(hasMCPBudgetOnNarrowLimit("*", "openai", [mcp])).toBe(true);
	});
	it("lets a narrowed, rate-limit-only configuration save with an empty MCP row left in the editor", () => {
		expect(
			hasMCPBudgetOnNarrowLimit("gpt-4o", undefined, [
				{ scope: "mcp", max_limit: undefined },
				{ scope: "shared", max_limit: 5 },
			]),
		).toBe(false);
		expect(hasMCPBudgetOnNarrowLimit("gpt-4o", undefined, [{ scope: "mcp", max_limit: null }])).toBe(false);
		expect(hasMCPBudgetOnNarrowLimit("gpt-4o", undefined, undefined)).toBe(false);
	});
});
describe("saveHasNoLimit", () => {
	it("lets the last Models-page line go while the edited row keeps an MCP line for the other page", () => {
		expect(saveHasNoLimit(false, true, true, false)).toBe(false);
	});
	it("does not let an Add succeed on lines the other page owns, with nothing entered", () => {
		expect(saveHasNoLimit(false, false, true, false)).toBe(true);
	});
	it("still blocks a save with nothing on the form and nothing kept on the row", () => {
		expect(saveHasNoLimit(false, true, false, false)).toBe(true);
	});
	it("allows emptying an existing MCP limit, which deletes it", () => {
		expect(saveHasNoLimit(false, true, false, true)).toBe(false);
	});
	it("allows a save with a limit on the form", () => {
		expect(saveHasNoLimit(true, false, false, false)).toBe(false);
	});
});