import { describe, expect, it } from "vitest";

import { buildPatchFromForm, defaultFormState, type FormState } from "./pricingFields";

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
});
