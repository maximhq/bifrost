import { describe, expect, it } from "vitest";
import { requireFiniteNumber } from "./warpView.utils";

describe("requireFiniteNumber", () => {
	// Clearing a number input is the case that matters: valueAsNumber gives NaN,
	// React Hook Form skips min/max because the DOM value is empty, and NaN
	// serializes to null in the request body.
	it("rejects NaN from a cleared input", () => {
		expect(requireFiniteNumber(Number.NaN, "A value is required")).toBe("A value is required");
	});

	it("rejects the non-finite results of a bad parse", () => {
		expect(requireFiniteNumber(Number.POSITIVE_INFINITY, "nope")).toBe("nope");
		expect(requireFiniteNumber(Number.NEGATIVE_INFINITY, "nope")).toBe("nope");
	});

	// undefined and null are what an absent field looks like; they are not a
	// number either, so the same message applies.
	it("rejects values that are not numbers at all", () => {
		expect(requireFiniteNumber(undefined, "nope")).toBe("nope");
		expect(requireFiniteNumber(null, "nope")).toBe("nope");
		expect(requireFiniteNumber("8", "nope")).toBe("nope");
	});

	// Zero must pass this rule. It is out of range for both fields, but that is
	// min's job to say - reporting "a value is required" for a value the user
	// actually typed would be a confusing error.
	it("accepts any finite number, including zero and negatives", () => {
		expect(requireFiniteNumber(0, "nope")).toBe(true);
		expect(requireFiniteNumber(-1, "nope")).toBe(true);
		expect(requireFiniteNumber(8, "nope")).toBe(true);
	});
});