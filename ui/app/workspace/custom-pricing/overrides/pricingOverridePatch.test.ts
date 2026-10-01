import { describe, expect, it } from "vitest";

import { buildPatchFromForm, defaultFormState, isUnsafePatchKey, type FormState } from "./pricingFields";

function formWith(overrides: Partial<FormState>): FormState {
	return { ...defaultFormState, ...overrides };
}

describe("buildPatchFromForm", () => {
	it("emits only the fields that were filled in", () => {
		const { patch, errors } = buildPatchFromForm(
			formWith({ pricingValues: { input_cost_per_token: "0.000001", output_cost_per_token: "" } }),
		);
		expect(errors).toEqual({});
		expect(patch).toEqual({ input_cost_per_token: 0.000001 });
	});

	it("reports per-field validation errors and omits the bad field", () => {
		const { patch, errors } = buildPatchFromForm(
			formWith({ pricingValues: { off_peak_cost_multiplier: "1.5", input_cost_per_token: "0.000001" } }),
		);
		expect(errors.off_peak_cost_multiplier).toBe("Must be greater than 0 and at most 1");
		expect(patch).toEqual({ input_cost_per_token: 0.000001 });
	});

	// Regression: the form renders numeric fields only, so a schedule object set
	// through the API used to be dropped the moment someone opened and saved the
	// override in the UI.
	it("carries through patch fields the form cannot render", () => {
		const peakHours = {
			timezone: "UTC",
			windows: [{ days: [1, 2, 3, 4, 5], start: "01:00", end: "04:00" }],
		};
		const { patch, errors } = buildPatchFromForm(
			formWith({
				pricingValues: { off_peak_cost_multiplier: "0.5" },
				preservedPatch: { peak_hours: peakHours },
			}),
		);
		expect(errors).toEqual({});
		expect(patch).toEqual({ off_peak_cost_multiplier: 0.5, peak_hours: peakHours });
	});

	it("lets a rendered field win over a stale preserved value of the same name", () => {
		const { patch } = buildPatchFromForm(
			formWith({
				pricingValues: { input_cost_per_token: "0.000002" },
				preservedPatch: { input_cost_per_token: 0.000009 },
			}),
		);
		expect(patch.input_cost_per_token).toBe(0.000002);
	});

	// A key the form does render lands in preservedPatch whenever the stored
	// value was not a number, so clearing that field in the form used to save
	// the stale value straight back.
	it("drops a preserved value for a rendered field the user cleared", () => {
		const { patch } = buildPatchFromForm(
			formWith({
				pricingValues: { input_cost_per_token: "" },
				preservedPatch: { input_cost_per_token: "0.000009" },
			}),
		);
		expect(patch).not.toHaveProperty("input_cost_per_token");
	});

	it("keeps a rendered field out of the patch when it was never filled in", () => {
		const { patch } = buildPatchFromForm(formWith({ preservedPatch: { output_cost_per_token: null } }));
		expect(patch).toEqual({});
	});

	// Assigning "__proto__" onto a plain object hits Object.prototype's accessor
	// instead of storing a field, so the key vanishes and the patch object's
	// prototype is mutated. JSON.parse does produce it as an own property, so a
	// patch fetched from the API can carry one.
	it("refuses to copy prototype-polluting keys out of the preserved patch", () => {
		const preservedPatch = JSON.parse('{"__proto__": {"polluted": true}, "peak_hours": {"timezone": "UTC"}}');
		const { patch } = buildPatchFromForm(formWith({ preservedPatch }));

		expect(patch).toEqual({ peak_hours: { timezone: "UTC" } });
		expect(Object.getPrototypeOf(patch)).toBe(Object.prototype);
		expect(({} as Record<string, unknown>).polluted).toBeUndefined();
	});
});

describe("isUnsafePatchKey", () => {
	it("flags the keys that cannot be stored as own properties", () => {
		expect(isUnsafePatchKey("__proto__")).toBe(true);
		expect(isUnsafePatchKey("constructor")).toBe(true);
		expect(isUnsafePatchKey("prototype")).toBe(true);
	});

	it("leaves real pricing field names alone", () => {
		expect(isUnsafePatchKey("peak_hours")).toBe(false);
		expect(isUnsafePatchKey("off_peak_cost_multiplier")).toBe(false);
		expect(isUnsafePatchKey("input_cost_per_token")).toBe(false);
	});
});