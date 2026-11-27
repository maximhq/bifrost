import { describe, expect, it } from "vitest";
import { validZoomWindow } from "./traceQuery";

describe("validZoomWindow", () => {
	it("accepts an ordered window inside the dates a browser can show", () => {
		expect(validZoomWindow(1_763_000_000, 1_763_000_060)).toBe(true);
	});

	it("refuses a window that is missing, reversed, empty, negative, or past the last valid date", () => {
		expect(validZoomWindow(null, 1_763_000_060)).toBe(false);
		expect(validZoomWindow(1_763_000_060, 1_763_000_000)).toBe(false);
		expect(validZoomWindow(1_763_000_000, 1_763_000_000)).toBe(false);
		expect(validZoomWindow(-5, 10)).toBe(false);
		// In seconds this is past 8.64e15 ms, where toISOString throws.
		expect(validZoomWindow(9_000_000_000_000, 9_000_000_000_060)).toBe(false);
		expect(validZoomWindow(Number.NaN, 10)).toBe(false);
	});
});