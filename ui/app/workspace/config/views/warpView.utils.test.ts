import { describe, expect, it } from "vitest";
import { isValidBaseURL, requireFiniteNumber } from "./warpView.utils";

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
describe("isValidBaseURL", () => {
	it("accepts absolute http(s) URLs with a host", () => {
		expect(isValidBaseURL("https://api.openai.com")).toBe(true);
		expect(isValidBaseURL("http://localhost:8080")).toBe(true);
		expect(isValidBaseURL("https://gw.internal/v1")).toBe(true);
	});

	// The case a prefix test waves through: a scheme and nothing else. It only
	// fails on the first outbound call, long after this page was left.
	it("rejects a scheme with no host", () => {
		expect(isValidBaseURL("https://")).toBe(false);
		expect(isValidBaseURL("http://")).toBe(false);
	});

	it("rejects other schemes and non-URLs", () => {
		expect(isValidBaseURL("ftp://example.com")).toBe(false);
		expect(isValidBaseURL("notaurl")).toBe(false);
	});

	// The server refuses credentials in this field, so the form must not offer
	// them as valid and then fail the save.
	it("rejects embedded credentials", () => {
		expect(isValidBaseURL("https://user:pass@example.com")).toBe(false);
	});
});
describe("isValidBaseURL trimming", () => {
	it("checks the value as the save path submits it", () => {
		// onSubmit sends form.baseURL.trim(), so checking the raw field refused a
		// pasted value the server would have taken.
		expect(isValidBaseURL("  https://api.example.com  ")).toBe(true);
		expect(isValidBaseURL("\thttps://api.example.com\n")).toBe(true);
		// Trimming must not rescue a value that is genuinely wrong.
		expect(isValidBaseURL("  api.example.com  ")).toBe(false);
		expect(isValidBaseURL("  https://  ")).toBe(false);
		expect(isValidBaseURL("  https://token@example.com  ")).toBe(false);
	});
});
