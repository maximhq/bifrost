import { describe, expect, it } from "vitest";
import { tryParseSessionTtlMinutes } from "@/lib/types/complexityRouter";
import { DEFAULT_SESSION_FORM_VALUES, isPositiveDurationString, normalizeSessionTtl, sessionTtlFieldValue, toFormValues } from "./formSchema";

describe("isPositiveDurationString", () => {
	it("accepts positive single-unit duration strings", () => {
		expect(isPositiveDurationString("60m")).toBe(true);
		expect(isPositiveDurationString("1h")).toBe(true);
	});

	it("rejects blank, zero, malformed, and multi-unit Go strings", () => {
		expect(isPositiveDurationString("")).toBe(false);
		expect(isPositiveDurationString("0m")).toBe(false);
		expect(isPositiveDurationString("60")).toBe(false);
		expect(isPositiveDurationString("bogus")).toBe(false);
		// Form state must be single-unit; multi-unit belongs on the wire only.
		expect(isPositiveDurationString("1h0m0s")).toBe(false);
		expect(isPositiveDurationString("30m0s")).toBe(false);
	});
});

describe("tryParseSessionTtlMinutes", () => {
	it("parses single-unit and Go Duration.String multi-unit forms", () => {
		expect(tryParseSessionTtlMinutes("60m")).toBe(60);
		expect(tryParseSessionTtlMinutes("1h")).toBe(60);
		expect(tryParseSessionTtlMinutes("1h0m0s")).toBe(60);
		expect(tryParseSessionTtlMinutes("30m0s")).toBe(30);
		expect(tryParseSessionTtlMinutes("1h30m")).toBe(90);
		expect(tryParseSessionTtlMinutes("90s")).toBe(1.5);
	});

	it("returns null for blank, malformed, or partially matched input", () => {
		expect(tryParseSessionTtlMinutes(undefined)).toBeNull();
		expect(tryParseSessionTtlMinutes("")).toBeNull();
		expect(tryParseSessionTtlMinutes("a while")).toBeNull();
		// Trailing garbage must not silently truncate to the matched prefix.
		expect(tryParseSessionTtlMinutes("5m x")).toBeNull();
	});
});

describe("normalizeSessionTtl", () => {
	it("collapses any valid duration into the single-unit minute string the form owns", () => {
		expect(normalizeSessionTtl("1h0m0s")).toBe("60m");
		expect(normalizeSessionTtl("30m")).toBe("30m");
		expect(normalizeSessionTtl("1h30m")).toBe("90m");
	});

	it("falls back to the default TTL for blank or malformed input", () => {
		expect(normalizeSessionTtl(undefined)).toBe(DEFAULT_SESSION_FORM_VALUES.ttl);
		expect(normalizeSessionTtl("")).toBe(DEFAULT_SESSION_FORM_VALUES.ttl);
		expect(normalizeSessionTtl("bogus")).toBe(DEFAULT_SESSION_FORM_VALUES.ttl);
	});
});

describe("sessionTtlFieldValue", () => {
	it("feeds the minutes control the raw digits of a minute string", () => {
		expect(sessionTtlFieldValue("45m")).toBe("45");
	});

	it("converts other valid durations to a minute count", () => {
		expect(sessionTtlFieldValue("1h")).toBe(60);
		expect(sessionTtlFieldValue("1h30m")).toBe(90);
	});

	it("keeps blank input blank and drops garbage", () => {
		expect(sessionTtlFieldValue("")).toBe("");
		expect(sessionTtlFieldValue(undefined)).toBe("");
		expect(sessionTtlFieldValue("a while")).toBe("");
	});
});

describe("toFormValues", () => {
	const base = {
		keywords: { simple_keywords: ["a"], medium_keywords: ["b"], complex_keywords: ["c"] },
	};

	it("uses session defaults when the config carries no session block", () => {
		const values = toFormValues(base as never);
		expect(values.session).toEqual(DEFAULT_SESSION_FORM_VALUES);
	});

	it("fills the holes Go's omitempty leaves in a saved session block", () => {
		const values = toFormValues({
			...base,
			session: { mode: "cache_aware", ttl: "1h0m0s" },
		} as never);
		expect(values.session.mode).toBe("cache_aware");
		expect(values.session.ttl).toBe("60m");
		expect(values.session.identity_sources).toEqual(DEFAULT_SESSION_FORM_VALUES.identity_sources);
		expect(values.session.downgrade_after_n_turns).toBe(DEFAULT_SESSION_FORM_VALUES.downgrade_after_n_turns);
		expect(values.session.switch_min_similarity).toBe(0);
		expect(values.session.max_switches_per_session).toBe(0);
	});
});
