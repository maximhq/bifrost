import { describe, expect, it } from "vitest";
import { decimalText, readDecimalText } from "./decimalInput";

describe("readDecimalText", () => {
	it("refuses anything but digits and one decimal point", () => {
		for (const raw of ["e", "1e5", "-1", "+2", "1.2.3", "abc", "0,5"]) expect(readDecimalText(raw), raw).toBeUndefined();
	});

	it("reports a cleared field as null so it reads as unset", () => {
		expect(readDecimalText("")).toEqual({ text: "", value: null });
		expect(readDecimalText(".")).toEqual({ text: ".", value: null });
	});

	it("keeps a half-typed decimal as typed and reports its number", () => {
		expect(readDecimalText("0.")).toEqual({ text: "0.", value: 0 });
		expect(readDecimalText(".5")).toEqual({ text: ".5", value: 0.5 });
		expect(readDecimalText("0.0325")).toEqual({ text: "0.0325", value: 0.0325 });
	});
});

describe("decimalText", () => {
	it("shows a loaded price as plain decimal text the field accepts, never in exponent form", () => {
		for (const value of [0.0000005, 1.23e-7, 1e-10, 0.000123, 0.1, 12.5, 0, 1e21]) {
			const text = decimalText(value);
			expect(text, String(value)).not.toMatch(/e/i);
			expect(readDecimalText(text)?.value, String(value)).toBe(value);
		}
		expect(decimalText(0.0000005)).toBe("0.0000005");
		expect(decimalText(1.23e-7)).toBe("0.000000123");
		expect(decimalText(0.1)).toBe("0.1");
		expect(decimalText(null)).toBe("");
		expect(decimalText(undefined)).toBe("");
	});
});