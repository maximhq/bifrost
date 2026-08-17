import { describe, expect, it } from "vitest";
import { buildWarpConfigPayload, requireFiniteNumber, validateWarpBaseURL } from "./warpView.utils";

describe("requireFiniteNumber", () => {
	// Clearing a number input is the case that matters: valueAsNumber gives NaN,
	// React Hook Form skips min/max because the DOM value is empty, and NaN
	// serializes to null in the request body.
	it("rejects NaN from a cleared input", () => {
		expect(requireFiniteNumber(Number.NaN, "A value is required")).toBe("A value is required");
	});

	it("rejects the non-finite results of a bad parse", () => {
		expect(requireFiniteNumber(Number.POSITIVE_INFINITY, "nope")).toBe("nope");
		expect(requireFiniteNumber(Number.NEGATIVE_INFINITY, "nope")).toBe("nope");
	});

	// undefined and null are what an absent field looks like; they are not a
	// number either, so the same message applies.
	it("rejects values that are not numbers at all", () => {
		expect(requireFiniteNumber(undefined, "nope")).toBe("nope");
		expect(requireFiniteNumber(null, "nope")).toBe("nope");
		expect(requireFiniteNumber("8", "nope")).toBe("nope");
	});

	// Zero must pass this rule. It is out of range for both fields, but that is
	// min's job to say - reporting "a value is required" for a value the user
	// actually typed would be a confusing error.
	it("accepts any finite number, including zero and negatives", () => {
		expect(requireFiniteNumber(0, "nope")).toBe(true);
		expect(requireFiniteNumber(-1, "nope")).toBe(true);
		expect(requireFiniteNumber(8, "nope")).toBe(true);
	});
});
describe("buildWarpConfigPayload", () => {
	const form = {
		enabled: true,
		provider: "openai",
		model: " gpt-4o ",
		base_url: " https://api.openai.com ",
		api_key_id: "key-abc",
		max_iterations: 8,
		request_timeout_seconds: 120,
		history_retention_days: 30,
		system_prompt_suffix: "be brief",
	};

	// The server reads api_key_id; a payload keyed api_key is silently ignored
	// and SaveConfig then writes the zero value, clearing the stored reference.
	it("sends the key reference under api_key_id", () => {
		const payload = buildWarpConfigPayload(form);
		expect(payload.api_key_id).toBe("key-abc");
		expect(payload).not.toHaveProperty("api_key");
	});

	// An ordinary save - changing the model - must carry the existing reference
	// through, or the full PUT stores an empty one.
	it("preserves the reference on a save that did not touch it", () => {
		const payload = buildWarpConfigPayload({ ...form, model: "gpt-4o-mini" });
		expect(payload.api_key_id).toBe("key-abc");
		expect(payload.model).toBe("gpt-4o-mini");
	});

	// Clearing is explicit: an empty field means no key, which is legitimate for
	// a provider on a trusted network.
	it("sends an empty reference when the operator cleared it", () => {
		expect(buildWarpConfigPayload({ ...form, api_key_id: "" }).api_key_id).toBe("");
	});

	it("carries the retention setting", () => {
		expect(buildWarpConfigPayload({ ...form, history_retention_days: 7 }).history_retention_days).toBe(7);
	});

	it("trims the free-text fields", () => {
		const payload = buildWarpConfigPayload(form);
		expect(payload.model).toBe("gpt-4o");
		expect(payload.base_url).toBe("https://api.openai.com");
	});
});
describe("validateWarpBaseURL", () => {
	it("validates the value as buildWarpConfigPayload will submit it", () => {
		// The payload trims, so the form rejecting an untrimmed paste refused a
		// value the server would have accepted.
		expect(validateWarpBaseURL("  https://api.example.com  ")).toBe(true);
		expect(validateWarpBaseURL("")).toBe(true);
		expect(validateWarpBaseURL("   ")).toBe(true);
		expect(validateWarpBaseURL(undefined)).toBe(true);
		expect(validateWarpBaseURL("api.example.com")).toContain("http://");
	});

	it("rejects credentials in the URL, as the server does", () => {
		// base_url is stored and returned unredacted, so ValidateConfigInput
		// returns 400 for userinfo. Saying so here beats surfacing that 400.
		expect(validateWarpBaseURL("https://token@example.com")).toContain("must not contain credentials");
		expect(validateWarpBaseURL("https://user:pass@example.com")).toContain("must not contain credentials");
	});
});