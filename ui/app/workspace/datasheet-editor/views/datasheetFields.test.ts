import type { DatasheetOverride } from "@/lib/store/apis/providersApi";
import { datasheetOverrideFormSchema } from "@/lib/types/schemas";
import { describe, expect, it } from "vitest";
import { buildSaveRequest, formStateFromOverride, overrideForMode, parseOtherParams, splitDriftedFields } from "./datasheetFields";

const target = { provider: "openai", model: "gpt-4o", mode: "chat", isCustom: false };

const override = (patch: Partial<DatasheetOverride>): DatasheetOverride => ({
	id: "o1",
	provider: "openai",
	model: "gpt-4o",
	mode: "chat",
	is_custom: false,
	created_at: "",
	updated_at: "",
	...patch,
});

describe("datasheet form <-> patch", () => {
	it("an untouched form saves empty patches, which the server treats as reset to upstream", () => {
		expect(buildSaveRequest(formStateFromOverride(target))).toEqual({
			provider: "openai",
			model: "gpt-4o",
			mode: "chat",
			pricing_patch: {},
			params_patch: {},
		});
	});

	it("round-trips every section of a saved override", () => {
		const saved = override({
			pricing_patch: {
				context_length: 64000,
				max_output_tokens: 4096,
				is_deprecated: true,
				input_cost_per_token: 0.000001,
				peak_hours: { timezone: "UTC", windows: [] },
				output_cost_per_token: null,
			},
			params_patch: { max_output_tokens: 4096, supports_mcp: false, reasoning_effort_levels: ["low", "high"] },
		});
		const form = formStateFromOverride(target, saved);
		expect(form.limits).toEqual({ context_length: "64000", max_input_tokens: "", max_output_tokens: "4096" });
		expect(form.deprecated).toBe("on");
		expect(form.pricingValues).toEqual({ input_cost_per_token: "0.000001" });
		expect(form.flags.supports_mcp).toBe("off");
		expect(form.flags.supports_reasoning).toBe("upstream");
		expect(JSON.parse(form.otherParamsJSON)).toEqual({ reasoning_effort_levels: ["low", "high"] });
		expect(form.preservedPricing).toEqual({ peak_hours: { timezone: "UTC", windows: [] }, output_cost_per_token: null });

		const request = buildSaveRequest(form);
		expect(request.pricing_patch).toEqual(saved.pricing_patch);
		expect(request.params_patch).toEqual(saved.params_patch);
	});

	it("mirrors max_output_tokens into the params patch so providers see the limit", () => {
		const form = formStateFromOverride(target);
		form.limits.max_output_tokens = "2048";
		const request = buildSaveRequest(form);
		expect(request.pricing_patch).toEqual({ max_output_tokens: 2048 });
		expect(request.params_patch).toEqual({ max_output_tokens: 2048 });
	});

	it("marks custom entries and trims the target", () => {
		const form = formStateFromOverride({ provider: " openai ", model: " ft:gpt-4o ", mode: "chat", isCustom: true });
		form.pricingValues.input_cost_per_token = "0.000004";
		expect(buildSaveRequest(form)).toMatchObject({
			provider: "openai",
			model: "ft:gpt-4o",
			is_custom: true,
			pricing_patch: { input_cost_per_token: 0.000004 },
		});
	});
});

describe("datasheet form schema", () => {
	const valid = () => formStateFromOverride(target);

	it("accepts empty limits and rejects non-whole or non-positive ones", () => {
		expect(datasheetOverrideFormSchema.safeParse(valid()).success).toBe(true);
		for (const bad of ["0", "-5", "1.5", "abc"]) {
			const form = valid();
			form.limits.context_length = bad;
			expect(datasheetOverrideFormSchema.safeParse(form).success, bad).toBe(false);
		}
	});

	it("rejects advanced params that are not a JSON object or that patch provider", () => {
		for (const bad of ["[1]", "nope", '{"provider":"x"}']) {
			const form = valid();
			form.otherParamsJSON = bad;
			expect(datasheetOverrideFormSchema.safeParse(form).success, bad).toBe(false);
		}
	});

	it("requires provider, model and mode on a custom entry", () => {
		const result = datasheetOverrideFormSchema.safeParse(formStateFromOverride({ provider: "", model: "", mode: "", isCustom: true }));
		expect(result.success).toBe(false);
		expect(result.error?.issues.map((i) => i.path.join("."))).toEqual(["provider", "model", "mode"]);
	});
});

describe("datasheet helpers", () => {
	it("splits drifted fields by patch", () => {
		const { pricing, params } = splitDriftedFields(["pricing.context_length", "params.supports_mcp", "other"]);
		expect([...pricing]).toEqual(["context_length"]);
		expect([...params]).toEqual(["supports_mcp"]);
	});

	it("picks only the override saved for the exact mode", () => {
		const modeWide = override({ id: "all", mode: "" });
		const chat = override({ id: "chat" });
		expect(overrideForMode([modeWide, chat], "chat")?.id).toBe("chat");
		expect(overrideForMode([modeWide], "chat")).toBeUndefined();
	});

	it("parses advanced params", () => {
		expect(parseOtherParams("")).toEqual({});
		expect(parseOtherParams('{"a":1}')).toEqual({ a: 1 });
		expect(parseOtherParams("[]")).toBeNull();
	});
});