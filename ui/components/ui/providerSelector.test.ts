import { describe, expect, it } from "vitest";
import { normalizeValueOption } from "./providerSelector.utils";

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