import { describe, expect, it } from "vitest";
import { isLogLevel, meetsMinLogLevel } from "./logLevel";

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

describe("isLogLevel", () => {
	it("accepts only the four known levels", () => {
		expect(isLogLevel("warn")).toBe(true);
		expect(isLogLevel("WARN")).toBe(false);
		expect(isLogLevel("")).toBe(false);
		expect(isLogLevel(3)).toBe(false);
	});
});