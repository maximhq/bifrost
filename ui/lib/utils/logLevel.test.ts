import { describe, expect, it } from "vitest";
import { countAtOrAboveEachLevel, isLogLevel, meetsMinLogLevel } from "./logLevel";

describe("meetsMinLogLevel", () => {
	it("treats the chosen level as a floor", () => {
		expect(meetsMinLogLevel("warn", "warn")).toBe(true);
		expect(meetsMinLogLevel("error", "warn")).toBe(true);
		expect(meetsMinLogLevel("info", "warn")).toBe(false);
		expect(meetsMinLogLevel("debug", "warn")).toBe(false);
	});

	it("shows everything at the debug floor", () => {
		for (const level of ["debug", "info", "warn", "error"]) {
			expect(meetsMinLogLevel(level, "debug")).toBe(true);
		}
	});

	it("keeps entries whose level is missing or unrecognised", () => {
		expect(meetsMinLogLevel(undefined, "error")).toBe(true);
		expect(meetsMinLogLevel(null, "error")).toBe(true);
		expect(meetsMinLogLevel("fatal", "error")).toBe(true);
	});
});

describe("countAtOrAboveEachLevel", () => {
	it("counts what each floor would show", () => {
		expect(countAtOrAboveEachLevel(["debug", "info", "info", "warn", "error"])).toEqual({ debug: 5, info: 4, warn: 2, error: 1 });
	});

	it("counts an unlevelled entry toward every floor, matching what is rendered", () => {
		expect(countAtOrAboveEachLevel([null, "error"])).toEqual({ debug: 2, info: 2, warn: 2, error: 2 });
	});

	it("is all zeros for no entries", () => {
		expect(countAtOrAboveEachLevel([])).toEqual({ debug: 0, info: 0, warn: 0, error: 0 });
	});
});

describe("isLogLevel", () => {
	it("accepts only the four known levels", () => {
		expect(isLogLevel("warn")).toBe(true);
		expect(isLogLevel("WARN")).toBe(false);
		expect(isLogLevel("")).toBe(false);
		expect(isLogLevel(3)).toBe(false);
	});
});