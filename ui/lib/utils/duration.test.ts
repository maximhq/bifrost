import { describe, test, expect } from "vitest";
import { formatCooldown } from "./duration";

describe("formatCooldown", () => {
	test("empty and zero values render empty", () => {
		expect(formatCooldown(undefined)).toBe("");
		expect(formatCooldown(0)).toBe("");
		expect(formatCooldown("")).toBe("");
	});

	test("strings pass through trimmed", () => {
		expect(formatCooldown(" 5m ")).toBe("5m");
		expect(formatCooldown("1h30m")).toBe("1h30m");
	});

	test("whole hours, minutes, seconds pick the largest exact unit", () => {
		expect(formatCooldown(3_600_000_000_000)).toBe("1h");
		expect(formatCooldown(300_000_000_000)).toBe("5m");
		expect(formatCooldown(90_000_000_000)).toBe("90s");
	});

	test("sub-second nanosecond values are preserved, not hidden", () => {
		expect(formatCooldown(500_000_000)).toBe("500ms");
		expect(formatCooldown(1_000)).toBe("1µs");
		expect(formatCooldown(1)).toBe("1ns");
	});

	test("mixed values are not truncated to whole seconds", () => {
		expect(formatCooldown(1_500_000_000)).toBe("1500ms");
	});
});
