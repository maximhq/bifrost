import { coreConfigSchema } from "@/lib/types/schemas";
import { describe, expect, it } from "vitest";
import { CODE_MODE_LIMIT_FIELDS, codeModeLimitsEqual, parseCodeModeLimitInput, validateCodeModeLimits } from "./codeModeLimits.utils";

describe("CODE_MODE_LIMIT_FIELDS", () => {
	it("covers every server limit with its default", () => {
		expect(Object.fromEntries(CODE_MODE_LIMIT_FIELDS.map((f) => [f.key, f.defaultValue]))).toEqual({
			max_source_bytes: 65536,
			max_steps: 1000000,
			max_memory_bytes: 67108864,
			max_log_bytes: 65536,
			max_tool_calls: 64,
			max_value_bytes: 1048576,
			max_nesting_depth: 64,
		});
	});
});

describe("codeModeLimitsEqual", () => {
	it("treats missing, undefined and 0 fields as the default", () => {
		expect(codeModeLimitsEqual(undefined, {})).toBe(true);
		expect(codeModeLimitsEqual(undefined, { max_steps: 0 })).toBe(true);
		expect(codeModeLimitsEqual({ max_tool_calls: 5 }, { max_tool_calls: 5, max_steps: 0 })).toBe(true);
	});

	it("detects a changed field", () => {
		expect(codeModeLimitsEqual(undefined, { max_tool_calls: 5 })).toBe(false);
		expect(codeModeLimitsEqual({ max_steps: 5 }, { max_steps: 6 })).toBe(false);
	});
});

describe("parseCodeModeLimitInput", () => {
	it("maps an empty input to 0 (the default)", () => {
		expect(parseCodeModeLimitInput("")).toBe(0);
	});

	it("parses whole numbers", () => {
		expect(parseCodeModeLimitInput("5000000")).toBe(5000000);
	});

	it("rejects negative and non-numeric input", () => {
		expect(parseCodeModeLimitInput("-1")).toBeUndefined();
		expect(parseCodeModeLimitInput("abc")).toBeUndefined();
		expect(parseCodeModeLimitInput("1.5")).toBeUndefined();
	});

	it("leaves the limit alone while a number input holds unparseable text", () => {
		// A number input reports "" for half-typed text such as "-" or "1e"; that is not a cleared field.
		expect(parseCodeModeLimitInput("", { badInput: true })).toBeUndefined();
		expect(parseCodeModeLimitInput("", { badInput: false })).toBe(0);
	});
});

describe("validateCodeModeLimits", () => {
	it("accepts defaults and large limits", () => {
		expect(validateCodeModeLimits(undefined)).toBeNull();
		expect(
			validateCodeModeLimits({ max_steps: 1e9, max_memory_bytes: 2 ** 34, max_tool_calls: 100000, max_nesting_depth: 1000 }),
		).toBeNull();
	});

	it("enforces the server's bounds", () => {
		expect(validateCodeModeLimits({ max_value_bytes: 1023 })).toMatch(/at least 1024/);
		expect(validateCodeModeLimits({ max_nesting_depth: 1001 })).toMatch(/at most 1000/);
		expect(validateCodeModeLimits({ max_steps: -1 })).toMatch(/negative/);
	});
});
describe("coreConfigSchema mcp_code_mode_limits", () => {
	const limits = coreConfigSchema.shape.mcp_code_mode_limits;

	it("accepts a max_value_bytes of 0 or at least 1024", () => {
		for (const value of [0, 1024, 1048576]) {
			expect(limits.safeParse({ max_value_bytes: value }).success).toBe(true);
		}
		expect(limits.safeParse(undefined).success).toBe(true);
	});

	it("rejects a nonzero max_value_bytes below 1024, as the server does", () => {
		for (const value of [10, 1023]) {
			expect(limits.safeParse({ max_value_bytes: value }).success).toBe(false);
		}
	});
});