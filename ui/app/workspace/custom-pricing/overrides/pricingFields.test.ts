import { describe, expect, it } from "vitest";

import { PRICING_FIELDS, pricingFieldError, pricingFieldUnit } from "./pricingFields";

describe("pricingFieldUnit", () => {
	// Character-priced fields carry a "/ character" label, so rendering them
	// with the token formatter's "/ 1M tokens" suffix names the wrong unit.
	it("classifies character-priced fields separately from token-priced ones", () => {
		expect(pricingFieldUnit("input_cost_per_character")).toBe("character");
	});

	it("classifies token-priced fields, including cache and modality-specific ones", () => {
		for (const key of [
			"input_cost_per_token",
			"output_cost_per_token",
			"cache_read_input_token_cost",
			"cache_creation_input_token_cost",
			"cache_creation_input_audio_token_cost",
			"cache_read_input_image_token_cost",
			"input_cost_per_audio_token",
			"output_cost_per_audio_token",
			"input_cost_per_image_token",
			"output_cost_per_image_token",
		]) {
			expect(pricingFieldUnit(key), key).toBe("token");
		}
	});

	it("keeps the token unit across context-tier and service-tier suffixes", () => {
		for (const key of [
			"input_cost_per_token_above_128k_tokens",
			"input_cost_per_token_ultrafast",
			"output_cost_per_token_ultrafast",
			"cache_read_input_token_cost_ultrafast",
			"cache_creation_input_token_cost_ultrafast",
			"cache_read_input_token_cost_above_200k_tokens",
			"cache_read_input_token_cost_above_200k_tokens_priority",
			"cache_creation_input_token_cost_above_1hr_fast",
			"cache_read_input_token_cost_flex_above_272k_tokens",
		]) {
			expect(pricingFieldUnit(key), key).toBe("token");
		}
	});

	// The trap: these price per second/image/video, but their context-tier
	// suffix mentions tokens. A bare "contains token" rule gets them wrong.
	it("does not treat a tokens-based context tier as a token-priced unit", () => {
		for (const key of [
			"input_cost_per_audio_per_second_above_128k_tokens",
			"input_cost_per_image_above_128k_tokens",
			"input_cost_per_video_per_second_above_128k_tokens",
		]) {
			expect(pricingFieldUnit(key), key).toBe("currency");
		}
	});

	it("classifies the geo multiplier as a multiplier, not currency", () => {
		expect(pricingFieldUnit("inference_geo_us_multiplier")).toBe("multiplier");
	});

	it("classifies flat and per-unit costs as currency", () => {
		for (const key of [
			"cost_per_request",
			"input_cost_per_second",
			"output_cost_per_pixel",
			"input_cost_per_image",
			"ocr_cost_per_page",
			"annotation_cost_per_page",
			"search_context_cost_per_query",
			"input_cost_per_query",
			"code_interpreter_cost_per_session",
			"output_cost_per_image_high_quality",
		]) {
			expect(pricingFieldUnit(key), key).toBe("currency");
		}
	});

	// Guards future additions: every catalogued field must resolve to a unit,
	// and only the one known multiplier may be non-currency/non-token.
	it("resolves a unit for every catalogued pricing field", () => {
		const byUnit: Record<string, string[]> = { token: [], currency: [], multiplier: [], character: [] };
		for (const field of PRICING_FIELDS) {
			const unit = pricingFieldUnit(field.key);
			expect(byUnit[unit], `${field.key} resolved to unexpected unit ${unit}`).toBeDefined();
			byUnit[unit].push(field.key);
		}
		expect(PRICING_FIELDS).toHaveLength(107);
		expect(byUnit.multiplier).toEqual(["inference_geo_us_multiplier", "off_peak_cost_multiplier"]);
		expect(byUnit.character).toEqual(["input_cost_per_character"]);
		// Sanity: the split is real, not everything collapsing into one bucket.
		expect(byUnit.token.length).toBeGreaterThan(20);
		expect(byUnit.currency.length).toBeGreaterThan(20);
	});
});

describe("pricingFieldError", () => {
	it("treats an empty value as no override rather than an error", () => {
		expect(pricingFieldError("input_cost_per_token", "")).toBeUndefined();
		expect(pricingFieldError("input_cost_per_token", "   ")).toBeUndefined();
		expect(pricingFieldError("input_cost_per_token", undefined)).toBeUndefined();
	});

	it("rejects non-numeric input", () => {
		expect(pricingFieldError("input_cost_per_token", "abc")).toBe("Must be a number");
		expect(pricingFieldError("input_cost_per_token", "Infinity")).toBe("Must be a number");
	});

	it("keeps the default non-negative rule for ordinary cost fields", () => {
		expect(pricingFieldError("input_cost_per_token", "0")).toBeUndefined();
		expect(pricingFieldError("input_cost_per_token", "0.000001")).toBeUndefined();
		expect(pricingFieldError("input_cost_per_token", "-1")).toBe("Must be >= 0");
	});

	// Every base rate is the peak price, so the off-peak multiplier can only
	// scale downward: 0 would make off-peak free, >1 would exceed peak. The Go
	// engine rejects both and bills at peak, so the form must not accept them.
	it("bounds the off-peak multiplier to (0, 1]", () => {
		expect(pricingFieldError("off_peak_cost_multiplier", "0.5")).toBeUndefined();
		expect(pricingFieldError("off_peak_cost_multiplier", "1")).toBeUndefined();
		expect(pricingFieldError("off_peak_cost_multiplier", "0")).toBe("Must be greater than 0 and at most 1");
		expect(pricingFieldError("off_peak_cost_multiplier", "-0.5")).toBe("Must be greater than 0 and at most 1");
		expect(pricingFieldError("off_peak_cost_multiplier", "1.5")).toBe("Must be greater than 0 and at most 1");
	});
});
