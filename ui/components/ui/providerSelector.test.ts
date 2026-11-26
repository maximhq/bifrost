import { describe, expect, it } from "vitest";
import { customGroupHeading, normalizeValueOption, providerListClassName } from "./providerSelector.utils";

describe("normalizeValueOption", () => {
	it("gives a bare name the standard provider label and mark", () => {
		expect(normalizeValueOption("openai")).toEqual({ value: "openai", label: "OpenAI", iconKey: "openai" });
	});

	// The CEL builder substitutes a disabled placeholder row when nothing is configured.
	// Rebuilding it from its value alone would relabel it and make it selectable.
	it("keeps a caller's label and disabled flag", () => {
		const sentinel = { value: "_no_providers", label: "No providers configured", disabled: true };
		expect(normalizeValueOption(sentinel)).toEqual(sentinel);
	});

	it("keeps a caller's label when the row is selectable", () => {
		const row = { value: "openai", label: "OpenAI (primary)" };
		expect(normalizeValueOption(row)).toEqual(row);
	});
});

describe("customGroupHeading", () => {
	it("falls back to the translated custom providers heading", () => {
		expect(customGroupHeading(undefined, "Custom providers")).toBe("Custom providers");
	});

	// FinOps lists cloud billing sources there, which are not custom providers.
	it("uses the caller's heading when one is given", () => {
		expect(customGroupHeading("Cloud providers", "Custom providers")).toBe("Cloud providers");
	});
});

describe("providerListClassName", () => {
	it("caps the list at 300px by default", () => {
		expect(providerListClassName()).toBe("max-h-[300px]");
	});

	// Both classes set max-height, and the stylesheet's rule order, not the class order, would pick the winner.
	it("drops the 300px cap when the caller sets a taller one", () => {
		const taller = "max-h-[min(520px,calc(var(--radix-popper-available-height)-3.5rem))]";
		expect(providerListClassName(taller)).toBe(taller);
	});

	it("keeps the cap when the caller only adds unrelated classes", () => {
		expect(providerListClassName("pr-1")).toBe("max-h-[300px] pr-1");
	});
});