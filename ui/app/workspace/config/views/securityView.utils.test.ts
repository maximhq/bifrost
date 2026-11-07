import { describe, expect, it } from "vitest";
import { isMaxRequestTimeoutChanged, parseMaxRequestTimeoutInput } from "./securityView.utils";

describe("isMaxRequestTimeoutChanged", () => {
	it("is unchanged when the input matches the saved cap", () => {
		expect(isMaxRequestTimeoutChanged("900", 900)).toBe(false);
		expect(isMaxRequestTimeoutChanged(" 900 ", 900)).toBe(false);
	});

	it("treats an empty input as 0, the saved default", () => {
		expect(isMaxRequestTimeoutChanged("", undefined)).toBe(false);
		expect(isMaxRequestTimeoutChanged("", 900)).toBe(true);
	});

	it("is changed for a different whole number", () => {
		expect(isMaxRequestTimeoutChanged("600", 900)).toBe(true);
	});

	// An input that is not a whole number never becomes the value to save, so that value still
	// matches the saved cap; it must still count as a change so Save stays enabled and the
	// save-time validation can reject it with a toast.
	it.each(["1.5", "-5", "abc", "1e3"])("counts the invalid input %s as a change", (raw) => {
		expect(isMaxRequestTimeoutChanged(raw, 900)).toBe(true);
	});
});

describe("parseMaxRequestTimeoutInput", () => {
	it("reads whole seconds, with empty as 0", () => {
		expect(parseMaxRequestTimeoutInput(" 600 ")).toBe(600);
		expect(parseMaxRequestTimeoutInput("")).toBe(0);
		expect(parseMaxRequestTimeoutInput("0")).toBe(0);
	});

	it.each(["1.5", "-5", "abc", "1e3", "10m"])("rejects %s", (raw) => {
		expect(parseMaxRequestTimeoutInput(raw)).toBeNull();
	});
});