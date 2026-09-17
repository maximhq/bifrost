import { describe, expect, it } from "vitest";
import { fitCount } from "./utils";

const base = { lines: 2, overflowWidth: 30, gap: 4 };

describe("fitCount", () => {
	it("shows everything when it all fits on the allowed rows", () => {
		// 4 chips of 20 wrap into two rows of 2, inside a 44px box.
		expect(fitCount({ ...base, widths: [20, 20, 20, 20], containerWidth: 44 })).toBe(4);
	});

	it("shows everything on a single row when there is room", () => {
		expect(fitCount({ ...base, widths: [20, 20, 20], containerWidth: 200 })).toBe(3);
	});

	it("leaves room for the overflow chip on the last allowed row", () => {
		// 6 chips of 20 in a 100px box: 4 fit per row, so rows 0-1 hold 8 slots but
		// the 5th and 6th spill; the last row gives up a chip so "+N" fits.
		expect(fitCount({ ...base, widths: Array(12).fill(20), containerWidth: 100 })).toBe(6);
	});

	it("respects a single-line budget", () => {
		expect(fitCount({ ...base, lines: 1, widths: Array(10).fill(20), containerWidth: 100 })).toBe(2);
	});

	it("keeps one chip even when nothing fits", () => {
		expect(fitCount({ ...base, widths: [200, 200, 200], containerWidth: 50 })).toBe(1);
	});

	it("handles varying chip widths", () => {
		// 90 + 4 + 40 = 134 > 120, so the wide chip owns row 0 and 40 starts row 1.
		expect(fitCount({ ...base, lines: 1, widths: [90, 40, 40], containerWidth: 120 })).toBe(1);
	});

	it("returns 0 for an empty list", () => {
		expect(fitCount({ ...base, widths: [], containerWidth: 100 })).toBe(0);
	});

	it("shows everything before the container has been measured", () => {
		expect(fitCount({ ...base, widths: [20, 20], containerWidth: 0 })).toBe(2);
	});
});