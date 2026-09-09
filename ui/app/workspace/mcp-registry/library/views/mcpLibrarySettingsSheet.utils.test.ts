import { describe, expect, it } from "vitest";
import { toIntervalHours } from "./mcpLibrarySettingsSheet.utils";

describe("toIntervalHours", () => {
	it("defaults to 24 when the interval is missing", () => {
		expect(toIntervalHours(undefined)).toBe(24);
		expect(toIntervalHours(null)).toBe(24);
	});

	it("keeps 0 as 0 so 'syncing disabled' survives the round trip", () => {
		expect(toIntervalHours(0)).toBe(0);
	});

	it("converts whole-hour intervals", () => {
		expect(toIntervalHours(3600)).toBe(1);
		expect(toIntervalHours(86400)).toBe(24);
	});

	it("preserves non-whole-hour intervals instead of rounding them", () => {
		// 5400s is valid per transports/config.schema.json (anyOf: const 0 |
		// minimum 3600). Rounding it to 2 makes the next save write 7200.
		expect(toIntervalHours(5400)).toBe(1.5);
		expect(toIntervalHours(4500)).toBe(1.25);
	});

	it("round-trips a fractional interval back to the exact stored seconds", () => {
		expect(toIntervalHours(5400) * 3600).toBe(5400);
	});
});