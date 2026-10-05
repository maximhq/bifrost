import { describe, expect, it } from "vitest";
import {
	denormalizeFallback,
	formatTargetsTTFT,
	MAX_TTFT_TIMEOUT_MS,
	normalizeFallback,
	parseTTFTTimeoutInput,
	resolveTargetTTFTMs,
	summarizeTTFTDisplay,
	summarizeTargetsTTFT,
} from "./routingRules";

describe("routing fallback wire format", () => {
	it.each([
		["openai/gpt-4o", { provider: "openai", model: "gpt-4o", key_id: "" }],
		["azure/", { provider: "azure", model: "", key_id: "" }],
		["openai/ft:model/org/v2", { provider: "openai", model: "ft:model/org/v2", key_id: "" }],
	] as const)("round-trips %s", (wire, form) => {
		expect(normalizeFallback(wire)).toEqual(form);
		expect(denormalizeFallback(form)).toEqual(wire);
	});

	it("keeps the provider delimiter when an unpinned fallback uses the incoming model", () => {
		expect(denormalizeFallback({ provider: " azure " })).toBe("azure/");
	});

	it("round-trips pinned fallback objects with and without a model", () => {
		for (const wire of [
			{ provider: "openai", model: "gpt-4o", key_id: "key-1" },
			{ provider: "azure", key_id: "key-2" },
		]) {
			expect(denormalizeFallback(normalizeFallback(wire))).toEqual(wire);
		}
	});

	it("returns to valid legacy syntax when a provider-only fallback pin is cleared", () => {
		const form = normalizeFallback({ provider: "azure", key_id: "key-1" });
		expect(denormalizeFallback({ ...form, key_id: "" })).toBe("azure/");
	});
});

describe("TTFT deadline input", () => {
	it("treats an empty input as no deadline", () => {
		expect(parseTTFTTimeoutInput("")).toBeUndefined();
		expect(parseTTFTTimeoutInput("  ")).toBeUndefined();
	});

	it.each([
		["1", 1],
		[" 1500 ", 1500],
		[String(MAX_TTFT_TIMEOUT_MS), MAX_TTFT_TIMEOUT_MS],
	] as const)("accepts %s", (raw, ms) => {
		expect(parseTTFTTimeoutInput(raw)).toBe(ms);
	});

	it.each(["0", "-5", "1.5", "abc", "1e3", String(MAX_TTFT_TIMEOUT_MS + 1)])("rejects %s", (raw) => {
		expect(parseTTFTTimeoutInput(raw)).toBeNull();
	});
});

describe("TTFT deadline on targets", () => {
	it("is off when no target has a deadline", () => {
		expect(summarizeTargetsTTFT([{ weight: 1 }, { weight: 0, ttft_timeout_ms: null }])).toEqual({ ms: undefined, mixed: false });
		expect(formatTargetsTTFT([{ weight: 1 }])).toBe("Off");
	});

	it("uses the shared deadline when every target agrees", () => {
		const targets = [
			{ weight: 0.5, ttft_timeout_ms: 1500 },
			{ weight: 0.5, ttft_timeout_ms: 1500 },
		];
		expect(summarizeTargetsTTFT(targets)).toEqual({ ms: 1500, mixed: false });
		expect(formatTargetsTTFT(targets)).toBe("1500 ms (streaming)");
	});

	it("reports mixed when targets differ, including set vs unset", () => {
		expect(
			summarizeTargetsTTFT([
				{ weight: 0.5, ttft_timeout_ms: 1500 },
				{ weight: 0.5, ttft_timeout_ms: 800 },
			]).mixed,
		).toBe(true);
		expect(summarizeTargetsTTFT([{ weight: 0.5, ttft_timeout_ms: 1500 }, { weight: 0.5 }]).mixed).toBe(true);
		expect(formatTargetsTTFT([{ weight: 0.5, ttft_timeout_ms: 1500 }, { weight: 0.5 }])).toBe("Mixed");
	});
});

describe("resolveTargetTTFTMs", () => {
	it("keeps each target's own deadline while the input is unchanged (mixed rule)", () => {
		expect(resolveTargetTTFTMs("", "", 1500)).toBe(1500);
		expect(resolveTargetTTFTMs("", "", 800)).toBe(800);
		expect(resolveTargetTTFTMs("", "", undefined)).toBe(0);
	});

	it("gives a target added to a uniform rule the shared deadline", () => {
		expect(resolveTargetTTFTMs("1500", "1500", 1500)).toBe(1500);
		expect(resolveTargetTTFTMs("1500", "1500", undefined)).toBe(1500);
	});

	it("applies an edited value to every target", () => {
		expect(resolveTargetTTFTMs("900", "", 1500)).toBe(900);
		expect(resolveTargetTTFTMs("900", "1500", undefined)).toBe(900);
	});

	it("clears every target when the loaded value is emptied", () => {
		expect(resolveTargetTTFTMs("", "1500", 1500)).toBe(0);
	});

	it("clears every target when the user empties a mixed field", () => {
		expect(resolveTargetTTFTMs("", "", 1500, true)).toBe(0);
		expect(resolveTargetTTFTMs("", "", 800, true)).toBe(0);
	});

	it("applies the shared value when an edit returns to the loaded text", () => {
		expect(resolveTargetTTFTMs("1500", "1500", 800, true)).toBe(1500);
	});
});

describe("summarizeTTFTDisplay", () => {
	const mixed = [{ weight: 0.5, ttft_timeout_ms: 1500 }, { weight: 0.5 }];
	const uniform = [
		{ weight: 0.5, ttft_timeout_ms: 1500 },
		{ weight: 0.5, ttft_timeout_ms: 1500 },
	];
	const off = [{ weight: 1 }];

	it("flags an untouched mixed rule as mixed and active", () => {
		expect(summarizeTTFTDisplay(mixed, "", false)).toEqual({ mixed: true, active: true });
	});

	it("is active but not mixed for an untouched uniform rule", () => {
		expect(summarizeTTFTDisplay(uniform, "1500", false)).toEqual({ mixed: false, active: true });
	});

	it("is inactive for an untouched rule with no deadlines, and for a new rule", () => {
		expect(summarizeTTFTDisplay(off, "", false)).toEqual({ mixed: false, active: false });
		expect(summarizeTTFTDisplay(undefined, "", false)).toEqual({ mixed: false, active: false });
	});

	it("follows the input once the user has edited it", () => {
		expect(summarizeTTFTDisplay(mixed, "", true)).toEqual({ mixed: false, active: false });
		expect(summarizeTTFTDisplay(mixed, "900", true)).toEqual({ mixed: false, active: true });
		expect(summarizeTTFTDisplay(uniform, "abc", true)).toEqual({ mixed: false, active: false });
	});
});