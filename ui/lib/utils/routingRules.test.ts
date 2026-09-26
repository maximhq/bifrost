import { describe, expect, it } from "vitest";
import { denormalizeFallback, normalizeFallback } from "./routingRules";

describe("routing fallback wire format", () => {
	it.each([
		["openai/gpt-4o", { provider: "openai", model: "gpt-4o", key_id: "" }],
		["azure/", { provider: "azure", model: "", key_id: "" }],
		["openai/ft:model/org/v2", { provider: "openai", model: "ft:model/org/v2", key_id: "" }],
	] as const)("round-trips %s", (wire, form) => {
		expect(normalizeFallback(wire)).toEqual(form);
		expect(denormalizeFallback(form)).toEqual(wire);
	});

	it("keeps the provider delimiter when an unpinned fallback uses the incoming model", () => {
		expect(denormalizeFallback({ provider: " azure " })).toBe("azure/");
	});

	it("round-trips pinned fallback objects with and without a model", () => {
		for (const wire of [
			{ provider: "openai", model: "gpt-4o", key_id: "key-1" },
			{ provider: "azure", key_id: "key-2" },
		]) {
			expect(denormalizeFallback(normalizeFallback(wire))).toEqual(wire);
		}
	});

	it("returns to valid legacy syntax when a provider-only fallback pin is cleared", () => {
		const form = normalizeFallback({ provider: "azure", key_id: "key-1" });
		expect(denormalizeFallback({ ...form, key_id: "" })).toBe("azure/");
	});
});