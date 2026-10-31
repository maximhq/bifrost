import { describe, expect, it } from "vitest";
import { fitCount, measureKey, uniqueProviders } from "./utils";

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

	it("drops to no chips when one chip cannot share a row with the overflow chip", () => {
		// A 200px chip in a 50px box leaves the "+N" badge on a second row, where the
		// container's overflow-hidden clips it away. Showing only "+N" keeps it reachable.
		expect(fitCount({ ...base, widths: [200, 200, 200], containerWidth: 50 })).toBe(0);
	});

	it("handles varying chip widths", () => {
		// 90 + 4 + 40 = 134 > 140, so the wide chip owns row 0 and 40 starts row 1; the
		// overflow chip still fits beside the wide one (90 + 4 + 30 = 124).
		expect(fitCount({ ...base, lines: 1, widths: [90, 40, 40], containerWidth: 140 })).toBe(1);
	});

	it("gives up the wide chip when the overflow chip cannot fit beside it", () => {
		// Same chips in a 120px box: 90 + 4 + 30 = 124 leaves no room for "+2" on the only row.
		expect(fitCount({ ...base, lines: 1, widths: [90, 40, 40], containerWidth: 120 })).toBe(0);
	});

	it("returns 0 for an empty list", () => {
		expect(fitCount({ ...base, widths: [], containerWidth: 100 })).toBe(0);
	});

	it("shows everything before the container has been measured", () => {
		expect(fitCount({ ...base, widths: [20, 20], containerWidth: 0 })).toBe(2);
	});
});

describe("uniqueProviders", () => {
	it("drops duplicates and keeps first-seen order", () => {
		expect(uniqueProviders(["openai", "anthropic", "openai"])).toEqual(["openai", "anthropic"]);
	});

	it("leaves an already-unique list untouched", () => {
		expect(uniqueProviders(["openai", "anthropic"])).toEqual(["openai", "anthropic"]);
	});
});

describe("measureKey", () => {
	it("distinguishes sequences whose names contain the separator", () => {
		expect(measureKey("icon", ["a,b", "c"])).not.toBe(measureKey("icon", ["a", "b,c"]));
	});

	it("is stable for the same variant and sequence", () => {
		expect(measureKey("icon", ["a", "b"])).toBe(measureKey("icon", ["a", "b"]));
	});

	it("distinguishes variants", () => {
		expect(measureKey("icon", ["a"])).not.toBe(measureKey("label", ["a"]));
	});
});