import { describe, test, expect } from "vitest";
import { DEFAULT_SEMANTIC_TIMEOUT_MS, formatSemanticTimeout, parseSemanticTimeoutMs } from "./complexityRouter";

describe("parseSemanticTimeoutMs", () => {
	test("parses single-unit durations", () => {
		expect(parseSemanticTimeoutMs("500ms")).toBe(500);
		expect(parseSemanticTimeoutMs("2s")).toBe(2000);
		expect(parseSemanticTimeoutMs("1.5s")).toBe(1500);
		expect(parseSemanticTimeoutMs("1h")).toBe(3600000);
	});

	// time.Duration.String() compounds anything from a minute up, so these are the forms
	// the API actually returns once an operator sets a timeout of 60s or more.
	test("parses compound durations the way time.ParseDuration does", () => {
		expect(parseSemanticTimeoutMs("1m0s")).toBe(60000);
		expect(parseSemanticTimeoutMs("1m30s")).toBe(90000);
		expect(parseSemanticTimeoutMs("1h0m0s")).toBe(3600000);
		expect(parseSemanticTimeoutMs("1h2m3s")).toBe(3723000);
		expect(parseSemanticTimeoutMs("2m500ms")).toBe(120500);
	});

	test("round-trips what formatSemanticTimeout emits", () => {
		for (const ms of [250, 1000, 60000, 90000, 3600000]) {
			expect(parseSemanticTimeoutMs(formatSemanticTimeout(ms))).toBe(ms);
		}
	});

	test("accepts a bare millisecond number", () => {
		expect(parseSemanticTimeoutMs("750")).toBe(750);
	});

	test("falls back to the default rather than sending a value the server rejects", () => {
		expect(parseSemanticTimeoutMs(undefined)).toBe(DEFAULT_SEMANTIC_TIMEOUT_MS);
		expect(parseSemanticTimeoutMs("")).toBe(DEFAULT_SEMANTIC_TIMEOUT_MS);
		expect(parseSemanticTimeoutMs("soon")).toBe(DEFAULT_SEMANTIC_TIMEOUT_MS);
		expect(parseSemanticTimeoutMs("0s")).toBe(DEFAULT_SEMANTIC_TIMEOUT_MS);
		expect(parseSemanticTimeoutMs("-5s")).toBe(DEFAULT_SEMANTIC_TIMEOUT_MS);
		// Partially valid input must not be read as the part that happened to parse.
		expect(parseSemanticTimeoutMs("1m30x")).toBe(DEFAULT_SEMANTIC_TIMEOUT_MS);
	});
});
